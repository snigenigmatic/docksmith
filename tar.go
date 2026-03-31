package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ExtractLayer unpacks a tar layer into the target rootfs directory
func ExtractLayer(layerDigest, rootfs string) error {
	layerPath := filepath.Join(layersDir, digestToFilename(layerDigest))
	file, err := os.Open(layerPath)
	if err != nil {
		return fmt.Errorf("could not open layer %s: %w", layerDigest, err)
	}
	defer file.Close()

	var reader io.Reader = file
	gzipReader, err := gzip.NewReader(file)
	if err == nil {
		defer gzipReader.Close()
		reader = gzipReader
	} else {
		// ADD THIS: The gzip reader consumed bytes trying to find a header.
		// We must reset the file pointer to the beginning for the tar reader.
		file.Seek(0, io.SeekStart)
	}

	tr := tar.NewReader(reader)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(rootfs, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			// Ensure parent dir exists
			os.MkdirAll(filepath.Dir(target), 0755)
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
		case tar.TypeSymlink:
			if err := os.Symlink(header.Linkname, target); err != nil {
				if !os.IsExist(err) {
					return err
				}
			}
		}
	}
	return nil
}

// SnapshotFS records the state of the filesystem before an instruction runs
func SnapshotFS(rootfs string) (map[string]os.FileInfo, error) {
	state := make(map[string]os.FileInfo)
	err := filepath.Walk(rootfs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(rootfs, path)
		state[rel] = info
		return nil
	})
	return state, err
}

// CreateDeltaTar finds new/modified files, writes a deterministic tar, and returns the digest
func CreateDeltaTar(rootfs string, beforeState map[string]os.FileInfo) (string, int64, error) {
	var changedFiles []string

	// 1. Find all new or modified files
	err := filepath.Walk(rootfs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(rootfs, path)

		// Skip the root dir itself
		if rel == "." {
			return nil
		}

		beforeInfo, exists := beforeState[rel]
		if !exists || beforeInfo.ModTime() != info.ModTime() || beforeInfo.Size() != info.Size() {
			changedFiles = append(changedFiles, rel)
		}
		return nil
	})
	if err != nil {
		return "", 0, err
	}

	// 2. Sort files lexicographically for deterministic builds (Hard Requirement)
	sort.Strings(changedFiles)

	// 3. Create a temporary file to hold the tar
	tmpFile, err := os.CreateTemp("", "layer-*.tar")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmpFile.Name())

	hasher := sha256.New()
	multiWriter := io.MultiWriter(tmpFile, hasher)
	tw := tar.NewWriter(multiWriter)

	// 4. Write files to tar with zeroed timestamps
	for _, relPath := range changedFiles {
		fullPath := filepath.Join(rootfs, relPath)
		info, err := os.Stat(fullPath)
		if err != nil {
			return "", 0, err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return "", 0, err
		}

		header.Name = relPath
		// ZERO OUT TIMESTAMPS FOR REPRODUCIBILITY!
		header.ModTime = time.Unix(0, 0)
		header.AccessTime = time.Unix(0, 0)
		header.ChangeTime = time.Unix(0, 0)

		if err := tw.WriteHeader(header); err != nil {
			return "", 0, err
		}

		if !info.IsDir() {
			file, err := os.Open(fullPath)
			if err != nil {
				return "", 0, err
			}
			_, err = io.Copy(tw, file)
			file.Close()
			if err != nil {
				return "", 0, err
			}
		}
	}

	tw.Close()
	tmpFile.Close()

	// 5. Move the temporary tar to the content-addressed layers directory
	digest := fmt.Sprintf("sha256:%x", hasher.Sum(nil))
	layerPath := filepath.Join(layersDir, digestToFilename(digest))

	// Get final size
	stat, _ := os.Stat(tmpFile.Name())
	size := stat.Size()

	if err := os.Rename(tmpFile.Name(), layerPath); err != nil {
		return "", 0, err
	}

	return digest, size, nil
}
