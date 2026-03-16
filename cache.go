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

// CacheIndex maps cache keys (SHA-256 hex strings) to layer digests.
type CacheIndex map[string]string

// LoadCacheIndex reads the cache index from ~/.docksmith/cache/index.json.
// If the file does not exist, it returns an empty index.
func LoadCacheIndex() (CacheIndex, error) {
	indexPath := filepath.Join(cacheDir, "index.json")

	data, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return make(CacheIndex), nil
		}
		return nil, fmt.Errorf("failed to read cache index: %w", err)
	}

	var index CacheIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("failed to parse cache index: %w", err)
	}

	return index, nil
}

// SaveCacheIndex writes the cache index to ~/.docksmith/cache/index.json.
func SaveCacheIndex(index CacheIndex) error {
	indexPath := filepath.Join(cacheDir, "index.json")

	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal cache index: %w", err)
	}

	if err := os.WriteFile(indexPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache index: %w", err)
	}

	return nil
}

// ComputeCacheKey generates a deterministic SHA-256 cache key for a layer-producing instruction.
//
// The key components are:
//   - prevLayerDigest: digest of the previous layer (or base image manifest digest for the first instruction)
//   - instructionRaw: the full instruction text exactly as written in the Docksmithfile
//   - workdir: the current WORKDIR state
//   - env: the current ENV state (accumulated key=value pairs)
//   - copySrcHash: for COPY instructions, the SHA-256 of the source files' bytes (empty string for RUN)
func ComputeCacheKey(prevLayerDigest, instructionRaw, workdir string, env []string, copySrcHash string) string {
	// Sort env lexicographically for determinism
	sortedEnv := make([]string, len(env))
	copy(sortedEnv, env)
	sort.Strings(sortedEnv)
	envSerialized := strings.Join(sortedEnv, "\n")

	// Build the canonical input string with strict formatting
	// Each component is on its own labeled line to avoid ambiguity
	var sb strings.Builder
	sb.WriteString("prevLayerDigest:")
	sb.WriteString(prevLayerDigest)
	sb.WriteString("\n")
	sb.WriteString("instruction:")
	sb.WriteString(instructionRaw)
	sb.WriteString("\n")
	sb.WriteString("workdir:")
	sb.WriteString(workdir)
	sb.WriteString("\n")
	sb.WriteString("env:")
	sb.WriteString(envSerialized)
	sb.WriteString("\n")
	if copySrcHash != "" {
		sb.WriteString("copySrcHash:")
		sb.WriteString(copySrcHash)
		sb.WriteString("\n")
	}

	hash := sha256.Sum256([]byte(sb.String()))
	return fmt.Sprintf("%x", hash)
}

// hashCopySources computes a SHA-256 hash over the raw bytes of all source files
// matched by the COPY instruction's source glob pattern. Files are read in
// lexicographically sorted path order and their bytes are concatenated.
//
// The srcPattern is resolved relative to the contextDir (the build context).
func hashCopySources(contextDir, srcPattern string) (string, error) {
	// Resolve the glob relative to the context directory
	fullPattern := filepath.Join(contextDir, srcPattern)

	matches, err := filepath.Glob(fullPattern)
	if err != nil {
		return "", fmt.Errorf("invalid glob pattern %q: %w", srcPattern, err)
	}

	// If the pattern is "." (the entire context), walk the directory instead
	if srcPattern == "." {
		matches = nil
		walkRoot := contextDir
		err := filepath.Walk(walkRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			// Only include regular files
			if !info.IsDir() {
				matches = append(matches, path)
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("failed to walk context directory: %w", err)
		}
	} else {
		// For glob results, expand any directories and filter to regular files only
		var expanded []string
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil {
				return "", fmt.Errorf("failed to stat %q: %w", match, err)
			}
			if info.IsDir() {
				// Walk the matched directory
				err := filepath.Walk(match, func(path string, fi os.FileInfo, err error) error {
					if err != nil {
						return err
					}
					if !fi.IsDir() {
						expanded = append(expanded, path)
					}
					return nil
				})
				if err != nil {
					return "", fmt.Errorf("failed to walk directory %q: %w", match, err)
				}
			} else {
				expanded = append(expanded, match)
			}
		}
		matches = expanded
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("COPY source %q matched no files in context %q", srcPattern, contextDir)
	}

	// Sort paths lexicographically for deterministic ordering
	sort.Strings(matches)

	// Hash all file contents in order
	hasher := sha256.New()
	for _, filePath := range matches {
		f, err := os.Open(filePath)
		if err != nil {
			return "", fmt.Errorf("failed to open %q: %w", filePath, err)
		}
		if _, err := io.Copy(hasher, f); err != nil {
			f.Close()
			return "", fmt.Errorf("failed to read %q: %w", filePath, err)
		}
		f.Close()
	}

	return fmt.Sprintf("sha256:%x", hasher.Sum(nil)), nil
}

// digestToFilename converts a digest like "sha256:abcdef..." to a
// filesystem-safe name like "sha256_abcdef..." (Windows does not allow ':').
func digestToFilename(digest string) string {
	return strings.ReplaceAll(digest, ":", "_")
}

// layerExistsOnDisk checks whether a layer file exists in the layers store.
func layerExistsOnDisk(layerDigest string) bool {
	layerPath := filepath.Join(layersDir, digestToFilename(layerDigest))
	_, err := os.Stat(layerPath)
	return err == nil
}
