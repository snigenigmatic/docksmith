package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

func resolveCopySourceFiles(contextDir, srcPattern string) ([]string, error) {
	normalizedPattern := normalizeCopyPattern(srcPattern)
	if normalizedPattern == "" {
		return nil, fmt.Errorf("COPY source pattern cannot be empty")
	}

	var matches []string
	err := filepath.Walk(contextDir, func(absPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		rel, err := filepath.Rel(contextDir, absPath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		if matchesCopySourcePattern(rel, normalizedPattern) {
			matches = append(matches, absPath)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk build context: %w", err)
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("COPY source %q matched no files in context %q", srcPattern, contextDir)
	}

	sort.Strings(matches)
	return matches, nil
}

func normalizeCopyPattern(srcPattern string) string {
	pattern := strings.TrimSpace(srcPattern)
	if pattern == "" {
		return ""
	}

	pattern = filepath.ToSlash(pattern)
	pattern = strings.TrimPrefix(pattern, "./")
	pattern = strings.TrimPrefix(pattern, "/")

	if pattern == "" || pattern == "." {
		return "."
	}

	return path.Clean(pattern)
}

func matchesCopySourcePattern(relPath, pattern string) bool {
	if pattern == "." {
		return true
	}

	relPath = filepath.ToSlash(relPath)
	relPath = strings.TrimPrefix(relPath, "./")

	if !hasGlobMeta(pattern) {
		return relPath == pattern || strings.HasPrefix(relPath, pattern+"/")
	}

	patternSegments := splitSegments(pattern)
	pathSegments := splitSegments(relPath)

	memo := make(map[[2]int]bool)
	seen := make(map[[2]int]bool)

	var match func(pi, si int) bool
	match = func(pi, si int) bool {
		key := [2]int{pi, si}
		if seen[key] {
			return memo[key]
		}
		seen[key] = true

		if pi == len(patternSegments) {
			memo[key] = si == len(pathSegments)
			return memo[key]
		}

		segment := patternSegments[pi]
		if segment == "**" {
			if pi == len(patternSegments)-1 {
				memo[key] = true
				return true
			}
			for k := si; k <= len(pathSegments); k++ {
				if match(pi+1, k) {
					memo[key] = true
					return true
				}
			}
			memo[key] = false
			return false
		}

		if si >= len(pathSegments) {
			memo[key] = false
			return false
		}

		ok, err := path.Match(segment, pathSegments[si])
		if err != nil || !ok {
			memo[key] = false
			return false
		}

		memo[key] = match(pi+1, si+1)
		return memo[key]
	}

	return match(0, 0)
}

func hasGlobMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

func splitSegments(p string) []string {
	parts := strings.Split(p, "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		segments = append(segments, part)
	}
	return segments
}
