package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// cacheIndexPath is the JSON file that maps cache keys to layer digests.
func cacheIndexPath() string {
	return filepath.Join(CacheDir(), "index.json")
}

// cacheIndex is a map[cacheKey]layerDigest.
type cacheIndex map[string]string

func loadCacheIndex() (cacheIndex, error) {
	data, err := os.ReadFile(cacheIndexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return make(cacheIndex), nil
		}
		return nil, err
	}
	var idx cacheIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return make(cacheIndex), nil
	}
	return idx, nil
}

func saveCacheIndex(idx cacheIndex) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cacheIndexPath(), data, 0644)
}

// ─── Cache key computation ────────────────────────────────────────────────────

// CacheKeyInput holds all inputs that determine a layer's cache key.
type CacheKeyInput struct {
	PrevDigest  string            // digest of the previous layer (or base manifest)
	Instruction string            // full instruction text as written
	Workdir     string            // current WORKDIR value (empty string if none)
	Env         map[string]string // accumulated ENV state
	// For COPY instructions only:
	SrcFileHashes map[string]string // sorted map[contextRelPath]sha256
}

// ComputeCacheKey returns the hex SHA-256 of all cache key inputs concatenated
// in a deterministic order.
func ComputeCacheKey(in CacheKeyInput) string {
	h := sha256.New()
	writeStr := func(s string) { _, _ = io.WriteString(h, s+"\x00") }

	writeStr(in.PrevDigest)
	writeStr(in.Instruction)
	writeStr(in.Workdir)

	// ENV: sorted key=value pairs.
	envKeys := make([]string, 0, len(in.Env))
	for k := range in.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		writeStr(k + "=" + in.Env[k])
	}
	writeStr("--env-end--")

	// COPY source file hashes: sorted by path.
	srcPaths := make([]string, 0, len(in.SrcFileHashes))
	for p := range in.SrcFileHashes {
		srcPaths = append(srcPaths, p)
	}
	sort.Strings(srcPaths)
	for _, p := range srcPaths {
		writeStr(p + "=" + in.SrcFileHashes[p])
	}

	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// ─── Cache lookup & store ─────────────────────────────────────────────────────

// CacheLookup checks the cache for a given key.
// Returns the layer digest and true if there is a valid cache entry AND the
// layer file exists on disk.  Returns "", false otherwise.
func CacheLookup(key string) (string, bool) {
	idx, err := loadCacheIndex()
	if err != nil {
		return "", false
	}
	digest, ok := idx[key]
	if !ok {
		return "", false
	}
	// A hit is only valid if the layer file is still present.
	if _, err := os.Stat(LayerPath(digest)); err != nil {
		return "", false
	}
	return digest, true
}

// CacheStore records a cache key → layer digest mapping.
func CacheStore(key, digest string) error {
	idx, err := loadCacheIndex()
	if err != nil {
		idx = make(cacheIndex)
	}
	idx[key] = digest
	return saveCacheIndex(idx)
}

// ─── Source file hashing (for COPY) ──────────────────────────────────────────

// HashSourceFiles returns a map of context-relative path → SHA-256 for all
// files matched by the glob patterns, sorted lexicographically by path.
func HashSourceFiles(contextDir string, srcPatterns []string) (map[string]string, error) {
	result := make(map[string]string)
	for _, pattern := range srcPatterns {
		// Support both relative and rooted patterns.
		absPattern := pattern
		if !filepath.IsAbs(pattern) {
			absPattern = filepath.Join(contextDir, pattern)
		}
		matches, err := filepath.Glob(absPattern)
		if err != nil {
			return nil, fmt.Errorf("glob %q: %w", pattern, err)
		}
		// Also handle ** via walk.
		if strings.Contains(pattern, "**") {
			matches, err = doubleStarGlob(contextDir, pattern)
			if err != nil {
				return nil, err
			}
		}
		for _, m := range matches {
			info, err := os.Lstat(m)
			if err != nil {
				continue
			}
			if info.IsDir() {
				// Hash all files inside the directory.
				if err := hashDir(m, contextDir, result); err != nil {
					return nil, err
				}
			} else {
				rel, _ := filepath.Rel(contextDir, m)
				digest, err := DigestFile(m)
				if err != nil {
					return nil, err
				}
				result[rel] = digest
			}
		}
	}
	return result, nil
}

func hashDir(dir, contextDir string, result map[string]string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(contextDir, path)
		digest, err := DigestFile(path)
		if err != nil {
			return err
		}
		result[rel] = digest
		return nil
	})
}

// doubleStarGlob handles ** patterns by walking the directory.
func doubleStarGlob(contextDir, pattern string) ([]string, error) {
	// Replace ** with a walk; naive but sufficient.
	prefix := pattern[:strings.Index(pattern, "**")]
	suffix := pattern[strings.Index(pattern, "**")+2:]
	suffix = strings.TrimPrefix(suffix, "/")

	baseDir := filepath.Join(contextDir, prefix)
	var matches []string
	_ = filepath.WalkDir(baseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(baseDir, path)
		if suffix == "" || strings.HasSuffix(rel, suffix) {
			matches = append(matches, path)
		}
		return nil
	})
	return matches, nil
}