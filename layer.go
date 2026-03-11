package main

import (
	"archive/tar"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// epoch is used to zero out all timestamps in layer tars, ensuring
// identical content always produces the same digest.
var epoch = time.Unix(0, 0)

// ─── Tar creation ─────────────────────────────────────────────────────────────

// CreateLayerTar builds a deterministic tar archive containing the given files
// from rootfs.  Entries are sorted by path and all timestamps are zeroed.
// The caller provides a slice of absolute paths on disk and their corresponding
// archive paths (relative to the tar root, starting with "./").
func CreateLayerTar(entries []TarEntry) ([]byte, error) {
	// Sort by archive path for determinism.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ArchivePath < entries[j].ArchivePath
	})

	pr, pw := io.Pipe()
	errCh := make(chan error, 1)

	go func() {
		tw := tar.NewWriter(pw)
		var writeErr error
		for _, e := range entries {
			if err := addTarEntry(tw, e); err != nil {
				writeErr = err
				break
			}
		}
		if writeErr == nil {
			writeErr = tw.Close()
		}
		pw.CloseWithError(writeErr)
		errCh <- writeErr
	}()

	data, readErr := io.ReadAll(pr)
	writeErr := <-errCh
	if readErr != nil {
		return nil, readErr
	}
	if writeErr != nil {
		return nil, writeErr
	}
	return data, nil
}

// TarEntry maps a file on disk to its path inside the tar archive.
type TarEntry struct {
	DiskPath    string // absolute path on the host filesystem
	ArchivePath string // path inside the tar (e.g. "./app/main.py")
}

func addTarEntry(tw *tar.Writer, e TarEntry) error {
	info, err := os.Lstat(e.DiskPath)
	if err != nil {
		return err
	}

	// Build a header with zeroed timestamps.
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = e.ArchivePath
	hdr.ModTime = epoch
	hdr.ChangeTime = epoch
	hdr.AccessTime = epoch
	// Zero out uid/gid names for reproducibility across systems.
	hdr.Uname = ""
	hdr.Gname = ""

	// Handle symlinks.
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(e.DiskPath)
		if err != nil {
			return err
		}
		hdr.Linkname = target
		return tw.WriteHeader(hdr)
	}

	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}

	// Write file content for regular files.
	if info.Mode().IsRegular() {
		f, err := os.Open(e.DiskPath)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	}
	return nil
}

// ─── Digest computation ───────────────────────────────────────────────────────

// DigestBytes returns "sha256:<hex>" for the given byte slice.
func DigestBytes(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", h)
}

// DigestFile computes the SHA-256 digest of a file on disk.
func DigestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}

// ─── Layer storage ─────────────────────────────────────────────────────────────

// StoreLayer writes tar bytes to the layers directory and returns the digest.
func StoreLayer(data []byte) (string, error) {
	digest := DigestBytes(data)
	path := LayerPath(digest)
	if _, err := os.Stat(path); err == nil {
		return digest, nil // already exists
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	return digest, nil
}

// ─── Tar extraction ───────────────────────────────────────────────────────────

// ExtractLayer extracts a stored layer (identified by digest) into rootfs.
func ExtractLayer(digest, rootfs string) error {
	data, err := os.ReadFile(LayerPath(digest))
	if err != nil {
		return fmt.Errorf("read layer %s: %w", digest, err)
	}
	return ExtractTar(data, rootfs)
}

// ExtractTar unpacks a tar archive into the given directory.
// Later layers overwrite earlier ones at the same path (union semantics).
func ExtractTar(data []byte, rootfs string) error {
	return extractTarFromReader(tar.NewReader(bytesReader(data)), rootfs)
}

func bytesReader(b []byte) io.Reader {
	return &bytesReaderImpl{data: b}
}

type bytesReaderImpl struct {
	data []byte
	pos  int
}

func (r *bytesReaderImpl) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func extractTarFromReader(tr *tar.Reader, rootfs string) error {
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}

		// Clean the path to prevent directory traversal.
		clean := filepath.Clean(hdr.Name)
		clean = strings.TrimPrefix(clean, "./")
		clean = strings.TrimPrefix(clean, "/")
		target := filepath.Join(rootfs, clean)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, hdr.FileInfo().Mode()); err != nil {
				return err
			}

		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode())
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(f, tr)
			f.Close()
			if copyErr != nil {
				return copyErr
			}

		case tar.TypeSymlink:
			_ = os.Remove(target) // overwrite existing
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}

		case tar.TypeLink:
			linkTarget := filepath.Join(rootfs, filepath.Clean(hdr.Linkname))
			_ = os.Remove(target)
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			if err := os.Link(linkTarget, target); err != nil {
				// Fall back to copy on cross-device links.
				if err2 := copyFileOnDisk(linkTarget, target, hdr.FileInfo().Mode()); err2 != nil {
					return err2
				}
			}
		}
	}
	return nil
}

// ─── Filesystem snapshot & diff ───────────────────────────────────────────────

// skipDirs lists virtual/transient directories to exclude from snapshots.
var skipDirs = map[string]bool{
	"proc": true,
	"sys":  true,
}

// FileSnapshot maps rootfs-relative paths to the SHA-256 of their content.
type FileSnapshot map[string]string

// SnapshotRootfs walks rootfs and records the content digest of every
// regular file and symlink, skipping virtual directories.
func SnapshotRootfs(rootfs string) (FileSnapshot, error) {
	snap := make(FileSnapshot)
	err := filepath.WalkDir(rootfs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		rel, _ := filepath.Rel(rootfs, path)
		if rel == "." {
			return nil
		}

		// Skip virtual/transient directories at the top level.
		topLevel := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if skipDirs[topLevel] {
			return filepath.SkipDir
		}

		if d.IsDir() {
			// Record directories too so we know if new ones appear.
			snap[rel] = "dir"
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return nil
			}
			snap[rel] = "symlink:" + target
			return nil
		}

		if info.Mode().IsRegular() {
			digest, err := DigestFile(path)
			if err != nil {
				return nil
			}
			snap[rel] = digest
		}
		return nil
	})
	return snap, err
}

// DiffRootfs compares before and after snapshots and returns the relative
// paths (inside rootfs) of files that are new or have changed content.
// Directories themselves are only included if they are brand new.
func DiffRootfs(before, after FileSnapshot, rootfs string) []TarEntry {
	var entries []TarEntry
	seen := map[string]bool{}

	for rel, afterVal := range after {
		beforeVal, existed := before[rel]
		if !existed || beforeVal != afterVal {
			diskPath := filepath.Join(rootfs, rel)
			info, err := os.Lstat(diskPath)
			if err != nil {
				continue
			}
			// For new/changed directories, include a dir entry.
			if info.IsDir() {
				if !existed { // only truly new dirs
					archivePath := "./" + strings.ReplaceAll(rel, string(filepath.Separator), "/")
					entries = append(entries, TarEntry{DiskPath: diskPath, ArchivePath: archivePath})
					seen[rel] = true
				}
				continue
			}
			archivePath := "./" + strings.ReplaceAll(rel, string(filepath.Separator), "/")
			if !seen[rel] {
				entries = append(entries, TarEntry{DiskPath: diskPath, ArchivePath: archivePath})
				seen[rel] = true
			}
		}
	}
	return entries
}

// BuildCopyEntries builds the TarEntry list for a COPY layer.
// src files are already copied into rootfs/destDir; we walk them and create
// entries with archive paths rooted at the container destination.
func BuildCopyEntries(rootfs, destInContainer string, copiedFiles []string) []TarEntry {
	var entries []TarEntry
	for _, diskPath := range copiedFiles {
		rel, err := filepath.Rel(rootfs, diskPath)
		if err != nil {
			continue
		}
		archivePath := "./" + strings.ReplaceAll(rel, string(filepath.Separator), "/")
		entries = append(entries, TarEntry{DiskPath: diskPath, ArchivePath: archivePath})
	}
	return entries
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func copyFileOnDisk(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// unused import fix
var _ = tar.TypeDir