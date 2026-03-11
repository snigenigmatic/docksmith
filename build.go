package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ─── Build state ──────────────────────────────────────────────────────────────

type buildState struct {
	// Accumulated manifest fields.
	layers  []LayerEntry
	config  ImageConfig
	envMap  map[string]string // KEY → value
	workdir string

	// Runtime state.
	rootfs          string // temp directory containing the assembled filesystem
	prevLayerDigest string // digest used in cache key computation
	cacheValid      bool   // false after the first cache miss
	created         string // ISO-8601 timestamp (preserved from old manifest on full hit)
}

// ─── Build entry point ────────────────────────────────────────────────────────

// BuildImage parses and executes the Docksmithfile in contextDir, writing the
// final image as name:tag.
func BuildImage(contextDir, name, tag string, noCache bool) error {
	docksmithFile := filepath.Join(contextDir, "Docksmithfile")
	instructions, err := ParseDocksmithfile(docksmithFile)
	if err != nil {
		return err
	}

	// Try to load any existing manifest so we can preserve its `created` timestamp
	// on a fully cached rebuild.
	var existingManifest *ImageManifest
	if m, err := LoadImage(name, tag); err == nil {
		existingManifest = m
	}

	state := &buildState{
		envMap:     make(map[string]string),
		cacheValid: !noCache,
		created:    time.Now().UTC().Format(time.RFC3339),
	}

	// Create a temporary working rootfs.
	tmpDir, err := os.MkdirTemp("", "docksmith-build-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	state.rootfs = filepath.Join(tmpDir, "rootfs")
	if err := os.MkdirAll(state.rootfs, 0755); err != nil {
		return err
	}

	// Count total steps for display.
	total := len(instructions)
	allCacheHits := !noCache

	for i, inst := range instructions {
		stepNum := i + 1
		fmt.Printf("Step %d/%d : %s %s", stepNum, total, inst.Op, inst.Args)

		var stepErr error
		var wasCacheHit bool

		switch inst.Op {
		case "FROM":
			fmt.Println() // FROM gets no cache status or timing
			stepErr = execFROM(inst, state)
		case "COPY":
			wasCacheHit, stepErr = execCOPY(inst, state, contextDir, noCache)
			if !wasCacheHit {
				allCacheHits = false
			}
		case "RUN":
			wasCacheHit, stepErr = execRUN(inst, state, noCache)
			if !wasCacheHit {
				allCacheHits = false
			}
		case "WORKDIR":
			fmt.Println()
			execWORKDIR(inst, state)
		case "ENV":
			fmt.Println()
			execENV(inst, state)
		case "CMD":
			fmt.Println()
			stepErr = execCMD(inst, state)
		}

		if stepErr != nil {
			return fmt.Errorf("step %d (%s): %w", stepNum, inst.Op, stepErr)
		}
	}

	// Build final manifest.
	manifest := &ImageManifest{
		Name:    name,
		Tag:     tag,
		Created: state.created,
		Config:  state.config,
		Layers:  state.layers,
	}

	// Assemble config.Env from envMap in sorted key order.
	envKeys := make([]string, 0, len(state.envMap))
	for k := range state.envMap {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	manifest.Config.Env = nil
	for _, k := range envKeys {
		manifest.Config.Env = append(manifest.Config.Env, k+"="+state.envMap[k])
	}
	manifest.Config.WorkingDir = state.workdir

	// On a fully cached rebuild, preserve the original `created` timestamp so
	// the manifest digest is identical to the previous run.
	if allCacheHits && existingManifest != nil {
		manifest.Created = existingManifest.Created
	}

	if err := SaveImage(manifest); err != nil {
		return fmt.Errorf("save manifest: %w", err)
	}

	// Reload to get the computed digest.
	saved, _ := LoadImage(name, tag)
	shortID := ""
	if saved != nil {
		shortID = saved.Digest
		if len(shortID) > 19 {
			shortID = shortID[:19] // "sha256:" + 12 hex chars
		}
	}
	fmt.Printf("\nSuccessfully built %s %s:%s\n", shortID, name, tag)
	return nil
}

// ─── Instruction handlers ─────────────────────────────────────────────────────

func execFROM(inst Instruction, state *buildState) error {
	parts := strings.SplitN(inst.Args, ":", 2)
	imgName := parts[0]
	imgTag := "latest"
	if len(parts) == 2 {
		imgTag = parts[1]
	}

	m, err := LoadImage(imgName, imgTag)
	if err != nil {
		return fmt.Errorf("FROM: %w", err)
	}

	// Extract all base layers into the working rootfs.
	for _, l := range m.Layers {
		if err := ExtractLayer(l.Digest, state.rootfs); err != nil {
			return fmt.Errorf("FROM: extract layer %s: %w", l.Digest[:16], err)
		}
	}

	// Inherit base layers and config.
	state.layers = append([]LayerEntry{}, m.Layers...)
	state.config = m.Config
	// Merge base ENV into envMap.
	for _, kv := range m.Config.Env {
		idx := strings.IndexByte(kv, '=')
		if idx > 0 {
			state.envMap[kv[:idx]] = kv[idx+1:]
		}
	}
	state.workdir = m.Config.WorkingDir

	// The cache key for the first layer-producing step uses the base manifest digest.
	state.prevLayerDigest = m.Digest
	return nil
}

func execCOPY(inst Instruction, state *buildState, contextDir string, noCache bool) (cacheHit bool, err error) {
	// Parse "COPY <src> <dest>" — first word is src, rest is dest.
	parts := strings.Fields(inst.Args)
	if len(parts) < 2 {
		return false, fmt.Errorf("COPY requires <src> <dest>")
	}
	dest := parts[len(parts)-1]
	srcs := parts[:len(parts)-1]

	// Compute source file hashes for the cache key.
	srcHashes, err := HashSourceFiles(contextDir, srcs)
	if err != nil {
		return false, fmt.Errorf("hashing source files: %w", err)
	}

	var layerDigest string
	var hit bool

	if state.cacheValid && !noCache {
		key := ComputeCacheKey(CacheKeyInput{
			PrevDigest:    state.prevLayerDigest,
			Instruction:   inst.Op + " " + inst.Args,
			Workdir:       state.workdir,
			Env:           state.envMap,
			SrcFileHashes: srcHashes,
		})
		layerDigest, hit = CacheLookup(key)
		if hit {
			fmt.Printf(" [CACHE HIT]\n")
			if err := ExtractLayer(layerDigest, state.rootfs); err != nil {
				return false, err
			}
			state.layers = appendLayer(state.layers, layerDigest, inst.Op+" "+inst.Args)
			state.prevLayerDigest = layerDigest
			return true, nil
		}
		// Cache miss — invalidate all downstream.
		state.cacheValid = false
	}

	start := time.Now()

	// Ensure the destination directory exists in rootfs.
	destInRootfs := filepath.Join(state.rootfs, dest)
	if err := os.MkdirAll(destInRootfs, 0755); err != nil {
		return false, err
	}

	// Ensure WORKDIR exists (created silently if needed).
	if state.workdir != "" {
		_ = os.MkdirAll(filepath.Join(state.rootfs, state.workdir), 0755)
	}

	// Copy source files into rootfs.
	copiedFiles, err := copyGlobsToRootfs(contextDir, srcs, destInRootfs, state.rootfs)
	if err != nil {
		return false, fmt.Errorf("COPY: %w", err)
	}

	// Build the tar entries from the copied files.
	tarEntries := BuildCopyEntries(state.rootfs, dest, copiedFiles)
	tarData, err := CreateLayerTar(tarEntries)
	if err != nil {
		return false, fmt.Errorf("create COPY layer: %w", err)
	}

	layerDigest, err = StoreLayer(tarData)
	if err != nil {
		return false, err
	}

	// Update cache index.
	if !noCache {
		key := ComputeCacheKey(CacheKeyInput{
			PrevDigest:    state.prevLayerDigest,
			Instruction:   inst.Op + " " + inst.Args,
			Workdir:       state.workdir,
			Env:           state.envMap,
			SrcFileHashes: srcHashes,
		})
		_ = CacheStore(key, layerDigest)
	}

	elapsed := time.Since(start)
	fmt.Printf(" [CACHE MISS] %.2fs\n", elapsed.Seconds())

	state.layers = appendLayer(state.layers, layerDigest, inst.Op+" "+inst.Args)
	state.prevLayerDigest = layerDigest
	return false, nil
}

func execRUN(inst Instruction, state *buildState, noCache bool) (cacheHit bool, err error) {
	var layerDigest string

	if state.cacheValid && !noCache {
		key := ComputeCacheKey(CacheKeyInput{
			PrevDigest:  state.prevLayerDigest,
			Instruction: inst.Op + " " + inst.Args,
			Workdir:     state.workdir,
			Env:         state.envMap,
		})
		var hit bool
		layerDigest, hit = CacheLookup(key)
		if hit {
			fmt.Printf(" [CACHE HIT]\n")
			if err := ExtractLayer(layerDigest, state.rootfs); err != nil {
				return false, err
			}
			state.layers = appendLayer(state.layers, layerDigest, inst.Op+" "+inst.Args)
			state.prevLayerDigest = layerDigest
			return true, nil
		}
		state.cacheValid = false
	}

	start := time.Now()
	fmt.Printf(" [CACHE MISS]\n")

	// Ensure WORKDIR exists before running the command.
	if state.workdir != "" {
		_ = os.MkdirAll(filepath.Join(state.rootfs, state.workdir), 0755)
	}

	// Snapshot filesystem before running the command.
	beforeSnap, err := SnapshotRootfs(state.rootfs)
	if err != nil {
		return false, fmt.Errorf("snapshot before RUN: %w", err)
	}

	// Build the environment to inject (image ENV vars so far).
	runEnv := buildEnvSlice(state.envMap)

	// Execute the shell command inside the container filesystem.
	shellCmd := []string{"/bin/sh", "-c", inst.Args}
	exitCode, err := RunContainer(state.rootfs, shellCmd, runEnv, state.workdir)
	if err != nil {
		return false, fmt.Errorf("RUN: %w", err)
	}
	if exitCode != 0 {
		return false, fmt.Errorf("RUN: command exited with code %d", exitCode)
	}

	// Snapshot filesystem after.
	afterSnap, err := SnapshotRootfs(state.rootfs)
	if err != nil {
		return false, fmt.Errorf("snapshot after RUN: %w", err)
	}

	// Build delta layer from changed/new files.
	diffEntries := DiffRootfs(beforeSnap, afterSnap, state.rootfs)
	tarData, err := CreateLayerTar(diffEntries)
	if err != nil {
		return false, fmt.Errorf("create RUN layer: %w", err)
	}

	layerDigest, err = StoreLayer(tarData)
	if err != nil {
		return false, err
	}

	// Store in cache.
	if !noCache {
		key := ComputeCacheKey(CacheKeyInput{
			PrevDigest:  state.prevLayerDigest,
			Instruction: inst.Op + " " + inst.Args,
			Workdir:     state.workdir,
			Env:         state.envMap,
		})
		_ = CacheStore(key, layerDigest)
	}

	elapsed := time.Since(start)
	fmt.Printf(" ---> %.2fs\n", elapsed.Seconds())

	state.layers = appendLayer(state.layers, layerDigest, inst.Op+" "+inst.Args)
	state.prevLayerDigest = layerDigest
	return false, nil
}

func execWORKDIR(inst Instruction, state *buildState) {
	state.workdir = inst.Args
}

func execENV(inst Instruction, state *buildState) {
	// Support both KEY=VALUE and KEY VALUE forms.
	if idx := strings.IndexByte(inst.Args, '='); idx > 0 {
		k := inst.Args[:idx]
		v := inst.Args[idx+1:]
		// Strip surrounding quotes from value.
		v = strings.Trim(v, `"'`)
		state.envMap[k] = v
	} else {
		parts := strings.SplitN(inst.Args, " ", 2)
		if len(parts) == 2 {
			state.envMap[parts[0]] = strings.Trim(parts[1], `"'`)
		}
	}
}

func execCMD(inst Instruction, state *buildState) error {
	var cmd []string
	if err := json.Unmarshal([]byte(inst.Args), &cmd); err != nil {
		return fmt.Errorf("CMD must be a JSON array (e.g. [\"/bin/sh\", \"-c\", \"echo hi\"]): %w", err)
	}
	state.config.Cmd = cmd
	return nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func appendLayer(layers []LayerEntry, digest, createdBy string) []LayerEntry {
	size := int64(0)
	if info, err := os.Stat(LayerPath(digest)); err == nil {
		size = info.Size()
	}
	return append(layers, LayerEntry{
		Digest:    digest,
		Size:      size,
		CreatedBy: createdBy,
	})
}

func buildEnvSlice(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(m))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out
}

// copyGlobsToRootfs expands each pattern relative to contextDir and copies
// matching files/directories into destDir inside rootfs.
// Returns the list of all individual files copied (for tar entry creation).
func copyGlobsToRootfs(contextDir string, patterns []string, destDir, rootfs string) ([]string, error) {
	var allCopied []string
	for _, pattern := range patterns {
		absPattern := pattern
		if !filepath.IsAbs(pattern) {
			absPattern = filepath.Join(contextDir, pattern)
		}

		var matches []string
		if strings.Contains(pattern, "**") {
			var err error
			matches, err = doubleStarGlob(contextDir, pattern)
			if err != nil {
				return nil, err
			}
		} else {
			var err error
			matches, err = filepath.Glob(absPattern)
			if err != nil {
				return nil, fmt.Errorf("glob %q: %w", pattern, err)
			}
		}

		if len(matches) == 0 {
			return nil, fmt.Errorf("no matches for %q", pattern)
		}

		for _, src := range matches {
			copied, err := copyPath(src, contextDir, destDir, rootfs)
			if err != nil {
				return nil, err
			}
			allCopied = append(allCopied, copied...)
		}
	}
	return allCopied, nil
}

// copyPath copies src (file or directory) into destDir, preserving relative
// structure when multiple files are copied.
func copyPath(src, contextDir, destDir, rootfs string) ([]string, error) {
	info, err := os.Lstat(src)
	if err != nil {
		return nil, err
	}

	if info.IsDir() {
		return copyDirContents(src, contextDir, destDir, rootfs)
	}

	// Single file: copy it preserving its name.
	dstFile := filepath.Join(destDir, filepath.Base(src))
	if err := copyFileOnDisk(src, dstFile, info.Mode()); err != nil {
		return nil, err
	}
	return []string{dstFile}, nil
}

func copyDirContents(srcDir, contextDir, destDir, rootfs string) ([]string, error) {
	var copied []string
	err := filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcDir, path)
		dst := filepath.Join(destDir, rel)

		if d.IsDir() {
			return os.MkdirAll(dst, 0755)
		}
		info, _ := d.Info()
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.Remove(dst)
			if err := os.Symlink(target, dst); err != nil {
				return err
			}
			copied = append(copied, dst)
			return nil
		}
		if err := copyFileOnDisk(path, dst, info.Mode()); err != nil {
			return err
		}
		copied = append(copied, dst)
		return nil
	})
	return copied, err
}