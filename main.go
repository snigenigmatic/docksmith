package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

var (
	homeDir, _   = os.UserHomeDir()
	docksmithDir = filepath.Join(homeDir, ".docksmith")
	imagesDir    = filepath.Join(docksmithDir, "images")
	layersDir    = filepath.Join(docksmithDir, "layers")
	cacheDir     = filepath.Join(docksmithDir, "cache")
)

func initDirs() error {
	dirs := []string{imagesDir, layersDir, cacheDir}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s : %w", dir, err)
		}
	}
	return nil
}

func main() {
	if err := initDirs(); err != nil {
		fmt.Printf("Initialization error: %v\n", err)
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "build":
		// Example: docksmith build -t myapp:latest .
		// basic parsing
		var tag, contextDir string
		noCache := false
		buildStart := time.Now()

		args := os.Args[2:]
		for i := 0; i < len(args); i++ {
			if args[i] == "-t" && i+1 < len(args) {
				tag = args[i+1]
				i++
			} else if args[i] == "--no-cache" {
				noCache = true
			} else {
				// Assume the last non-flag argument is the context directory
				contextDir = args[i]
			}
		}

		if tag == "" || contextDir == "" {
			fmt.Println("Usage : docksmith build -t <name:tag> <context> [--no-cache]")
			os.Exit(1)
		}

		targetName, targetTag, err := parseImageReference(tag)
		if err != nil {
			fmt.Printf("Build failed: invalid target tag %q: %v\n", tag, err)
			os.Exit(1)
		}

		existingTaggedManifests, err := findImageManifestsByNameTag(imagesDir, targetName, targetTag)
		if err != nil {
			fmt.Printf("Build failed: could not inspect existing manifests for %s:%s: %v\n", targetName, targetTag, err)
			os.Exit(1)
		}

		existingCreated := ""
		if len(existingTaggedManifests) > 0 {
			sort.Slice(existingTaggedManifests, func(i, j int) bool {
				return existingTaggedManifests[i].Manifest.Created < existingTaggedManifests[j].Manifest.Created
			})
			existingCreated = existingTaggedManifests[len(existingTaggedManifests)-1].Manifest.Created
		}

		fmt.Printf("Building %s from context %s (No-cache: %v)\n", tag, contextDir, noCache)

		// Parse the Docksmithfile
		instructions, err := ParseDocksmithfile(contextDir)
		if err != nil {
			fmt.Printf("Build failed: %v\n", err)
			os.Exit(1)
		}

		// Print parsed instructions to verify it works
		var baseManifest *Manifest

		// The first instruction is guaranteed to be FROM by our parser
		fromInst := instructions[0]

		// Parse <image>[:<tag>]
		parts := strings.Split(fromInst.Args, ":")
		imageName := parts[0]
		imageTag := "latest"
		if len(parts) > 1 {
			imageTag = parts[1]
		}

		// Look up the image in the local store
		baseManifest, err = FindImage(imageName, imageTag)
		if err != nil {
			fmt.Printf("Build failed: %v\n", err)
			os.Exit(1)
		}

		// Spec: "FROM always prints its step line with no cache status or timing
		// — it is not a layer-producing step and performs no cache lookup."
		fmt.Printf("Step 1/%d : %s\n", len(instructions), fromInst.Raw)

		// Initialize build state from the base image
		currentConfig := baseManifest.Config
		currentLayers := baseManifest.Layers

		fmt.Printf(" -> Base image loaded: %s (Layers: %d)\n", baseManifest.Digest, len(currentLayers))

		// --- CACHE SETUP ---
		prevLayerDigest := baseManifest.Digest
		cascadeMiss := false

		var cacheIndex CacheIndex
		if !noCache {
			var err error
			cacheIndex, err = LoadCacheIndex()
			if err != nil {
				fmt.Printf("Warning: could not load cache index, proceeding without cache: %v\n", err)
				noCache = true
			}
		}
		if cacheIndex == nil {
			cacheIndex = make(CacheIndex)
		}
		allLayerStepsCacheHit := true

		// --- BUILD ENGINE: Instruction Loop ---
		for i := 1; i < len(instructions); i++ {
			inst := instructions[i]

			// Print the step header
			fmt.Printf("Step %d/%d : %s\n", i+1, len(instructions), inst.Raw)

			switch inst.Type {
			case "WORKDIR":
				// Simply update the current working directory in the config
				currentConfig.WorkingDir = inst.Args

			case "ENV":
				// Parse KEY=VALUE
				parts := strings.SplitN(inst.Args, "=", 2)
				if len(parts) != 2 {
					fmt.Printf("Build failed: invalid ENV format on line %d. Expected KEY=VALUE\n", inst.LineNum)
					os.Exit(1)
				}
				key := parts[0]
				val := parts[1]
				envEntry := key + "=" + val

				// Check if the key already exists and overwrite it, otherwise append
				updated := false
				for j, existingEnv := range currentConfig.Env {
					if strings.HasPrefix(existingEnv, key+"=") {
						currentConfig.Env[j] = envEntry
						updated = true
						break
					}
				}
				if !updated {
					currentConfig.Env = append(currentConfig.Env, envEntry)
				}

			case "CMD":
				// Must be a valid JSON array according to the spec
				var cmdArray []string
				if err := json.Unmarshal([]byte(inst.Args), &cmdArray); err != nil {
					fmt.Printf("Build failed: invalid CMD format on line %d. Expected JSON array (e.g., [\"exec\", \"arg\"]): %v\n", inst.LineNum, err)
					os.Exit(1)
				}
				currentConfig.Cmd = cmdArray

			case "COPY", "RUN":
				stepStart := time.Now()

				// --- Build Cache Logic ---
				var copySrcHash string

				if inst.Type == "COPY" {
					// Parse COPY args: <src> <dest>
					copyParts := strings.Fields(inst.Args)
					if len(copyParts) != 2 {
						fmt.Printf("Build failed: invalid COPY format on line %d. Expected: COPY <src> <dest>\n", inst.LineNum)
						os.Exit(1)
					}
					srcPattern := copyParts[0]

					var err error
					copySrcHash, err = hashCopySources(contextDir, srcPattern)
					if err != nil {
						fmt.Printf("Build failed: %v\n", err)
						os.Exit(1)
					}
				}

				cacheKey := ComputeCacheKey(prevLayerDigest, inst.Raw, currentConfig.WorkingDir, currentConfig.Env, copySrcHash)

				// Check cache (unless --no-cache or cascade miss)
				if !noCache && !cascadeMiss {
					if layerDigest, found := cacheIndex[cacheKey]; found && layerExistsOnDisk(layerDigest) {
						layerSize, err := layerFileSize(layerDigest)
						if err != nil {
							fmt.Printf("Build failed: cached layer %s has invalid metadata: %v\n", layerDigest, err)
							os.Exit(1)
						}

						// Cache hit: reuse the existing layer
						fmt.Printf(" [CACHE HIT] %.2fs\n", time.Since(stepStart).Seconds())
						currentLayers = append(currentLayers, layer{
							Digest:    layerDigest,
							Size:      layerSize,
							CreatedBy: inst.Raw,
						})
						prevLayerDigest = layerDigest
						continue
					}
				}

				allLayerStepsCacheHit = false
				cascadeMiss = true

				// 1. Create a temporary rootfs directory
				rootfs, err := os.MkdirTemp("", "docksmith-rootfs-*")
				if err != nil {
					fmt.Printf("Build failed: could not create temp rootfs: %v\n", err)
					os.Exit(1)
				}
				defer os.RemoveAll(rootfs)

				// 2. Extract all previous layers into the rootfs to assemble the filesystem
				for _, layer := range currentLayers {
					if err := ExtractLayer(layer.Digest, rootfs); err != nil {
						fmt.Printf("Build failed: could not extract layer %s: %v\n", layer.Digest, err)
						os.Exit(1)
					}
				}

				// WORKDIR must exist in the assembled fs before the next layer-producing step executes.
				if err := ensureWorkdirExists(rootfs, currentConfig.WorkingDir); err != nil {
					fmt.Printf("Build failed: could not ensure WORKDIR %q: %v\n", currentConfig.WorkingDir, err)
					os.Exit(1)
				}

				// 3. Take a snapshot of the filesystem before execution
				beforeState, err := SnapshotFS(rootfs)
				if err != nil {
					fmt.Printf("Build failed: could not snapshot fs: %v\n", err)
					os.Exit(1)
				}

				// 4. Execute the instruction (Isolated!)
				if err := ExecuteInstruction(inst, rootfs, currentConfig, contextDir); err != nil {
					fmt.Printf("Build failed: execution error: %v\n", err)
					os.Exit(1)
				}

				// 5. Compute the delta, create the tarball, and get the new digest
				newDigest, layerSize, err := CreateDeltaTar(rootfs, beforeState)
				if err != nil {
					fmt.Printf("Build failed: could not create layer tar: %v\n", err)
					os.Exit(1)
				}

				// 6. Update state and cache
				currentLayers = append(currentLayers, layer{
					Digest:    newDigest,
					Size:      layerSize,
					CreatedBy: inst.Raw,
				})

				if !noCache {
					cacheIndex[cacheKey] = newDigest
				}
				prevLayerDigest = newDigest

				fmt.Printf(" [CACHE MISS] %.2fs\n", time.Since(stepStart).Seconds())
				fmt.Printf(" ---> Created layer %s\n", newDigest[:12])
			}
		}

		// Save the cache index after the build
		if !noCache {
			if err := SaveCacheIndex(cacheIndex); err != nil {
				fmt.Printf("Warning: failed to save cache index: %v\n", err)
			}
		}

		// At the end of the build, we will save the final manifest
		fmt.Printf("\nFinal Config State:\n WorkingDir: %s\n Env: %v\n Cmd: %v\n",
			currentConfig.WorkingDir, currentConfig.Env, currentConfig.Cmd)

		createdAt := time.Now().UTC().Format(time.RFC3339)
		if allLayerStepsCacheHit && existingCreated != "" {
			createdAt = existingCreated
		}

		// Construct the final manifest
		finalManifest := Manifest{
			Name:    targetName,
			Tag:     targetTag,
			Created: createdAt,
			Config:  currentConfig,
			Layers:  currentLayers,
		}

		// Compute the digest and get the JSON payload
		manifestBytes, err := finalManifest.ComputeAndSetDigest()
		if err != nil {
			fmt.Printf("Build failed: could not compute manifest digest: %v\n", err)
			os.Exit(1)
		}

		// Save to the images directory
		for _, existing := range existingTaggedManifests {
			if err := os.Remove(existing.Path); err != nil && !os.IsNotExist(err) {
				fmt.Printf("Build failed: could not replace existing manifest %s: %v\n", existing.Path, err)
				os.Exit(1)
			}
		}

		manifestFilename := strings.ReplaceAll(finalManifest.Digest, ":", "_") + ".json"
		manifestPath := filepath.Join(imagesDir, manifestFilename)

		if err := os.WriteFile(manifestPath, manifestBytes, 0644); err != nil {
			fmt.Printf("Build failed: could not save manifest: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("\nSuccessfully built %s:%s\nDigest: %s\nTotal time: %.2fs\n", targetName, targetTag, finalManifest.Digest, time.Since(buildStart).Seconds())

	case "images":
		// Example: docksmith images
		if err := listImages(os.Stdout, imagesDir); err != nil {
			fmt.Printf("Failed to list images: %v\n", err)
			os.Exit(1)
		}
	case "rmi":
		// Example: docksmith rmi myapp:latest
		if len(os.Args) < 3 {
			fmt.Println("Usage : docksmith rmi <name:tag>")
			os.Exit(1)
		}

		imageName, imageTag, err := parseImageReference(os.Args[2])
		if err != nil {
			fmt.Printf("Remove failed: %v\n", err)
			os.Exit(1)
		}

		removedLayers, err := removeImage(imageName, imageTag, imagesDir, layersDir)
		if err != nil {
			fmt.Printf("Remove failed: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Removed image %s:%s\n", imageName, imageTag)
		if removedLayers > 0 {
			fmt.Printf("Removed %d layer(s)\n", removedLayers)
		}
	case "run":
		// Example: docksmith run myapp:latest
		imageRef, envOverrides, cmdOverride, err := parseRunArgs(os.Args[2:])
		if err != nil {
			fmt.Printf("Run failed: %v\n", err)
			fmt.Println("Usage : docksmith run <name:tag> [cmd] [-e KEY=VALUE...]")
			os.Exit(1)
		}

		imageName, imageTag, err := parseImageReference(imageRef)
		if err != nil {
			fmt.Printf("Run failed: %v\n", err)
			os.Exit(1)
		}

		manifest, err := FindImage(imageName, imageTag)
		if err != nil {
			fmt.Printf("Run failed: %v\n", err)
			os.Exit(1)
		}

		runtimeConfig := manifest.Config
		runtimeConfig.Env, err = mergeEnv(runtimeConfig.Env, envOverrides)
		if err != nil {
			fmt.Printf("Run failed: %v\n", err)
			os.Exit(1)
		}

		cmdToRun := runtimeConfig.Cmd
		if len(cmdOverride) > 0 {
			cmdToRun = cmdOverride
		}
		if len(cmdToRun) == 0 {
			fmt.Printf("Run failed: no command provided and image %s:%s has no CMD\n", imageName, imageTag)
			os.Exit(1)
		}

		exitCode, runErr := func() (int, error) {
			rootfs, err := os.MkdirTemp("", "docksmith-run-rootfs-*")
			if err != nil {
				return 1, fmt.Errorf("could not create temp rootfs: %w", err)
			}
			defer os.RemoveAll(rootfs)

			for _, layer := range manifest.Layers {
				if err := ExtractLayer(layer.Digest, rootfs); err != nil {
					return 1, fmt.Errorf("could not extract layer %s: %w", layer.Digest, err)
				}
			}

			shellCommand := commandToShell(cmdToRun)
			return runIsolatedCommand(shellCommand, rootfs, runtimeConfig)
		}()

		if runErr != nil {
			fmt.Printf("Run failed: %v\n", runErr)
			os.Exit(1)
		}

		fmt.Printf("Container exited with code %d\n", exitCode)
		os.Exit(exitCode)
	default:
		fmt.Printf("Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Docksmith - A simplified container build and runtime system")
	fmt.Println("\nUsage:")
	fmt.Println("  docksmith build -t <name:tag> <context> [--no-cache]")
	fmt.Println("  docksmith images")
	fmt.Println("  docksmith rmi <name:tag>")
	fmt.Println("  docksmith run <name:tag> [cmd] [-e KEY=VALUE...]")
}

func listImages(out io.Writer, imagesPath string) error {
	entries, err := os.ReadDir(imagesPath)
	if err != nil {
		return err
	}

	var manifests []Manifest
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		manifestPath := filepath.Join(imagesPath, entry.Name())
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}

		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}

		manifests = append(manifests, m)
	}

	if len(manifests) == 0 {
		_, err := fmt.Fprintln(out, "No images found.")
		return err
	}

	sort.Slice(manifests, func(i, j int) bool {
		if manifests[i].Name == manifests[j].Name {
			return manifests[i].Tag < manifests[j].Tag
		}
		return manifests[i].Name < manifests[j].Name
	})

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tTAG\tIMAGE ID\tCREATED")

	for _, m := range manifests {
		imageID := strings.TrimPrefix(m.Digest, "sha256:")
		if len(imageID) > 12 {
			imageID = imageID[:12]
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.Name, m.Tag, imageID, m.Created)
	}

	return w.Flush()
}

type manifestFileRecord struct {
	Path     string
	Manifest Manifest
}

func findImageManifestsByNameTag(imagesPath, name, tag string) ([]manifestFileRecord, error) {
	entries, err := os.ReadDir(imagesPath)
	if err != nil {
		return nil, err
	}

	var matches []manifestFileRecord
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		manifestPath := filepath.Join(imagesPath, entry.Name())
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}

		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}

		if m.Name == name && m.Tag == tag {
			matches = append(matches, manifestFileRecord{Path: manifestPath, Manifest: m})
		}
	}

	return matches, nil
}

func layerFileSize(layerDigest string) (int64, error) {
	layerPath := filepath.Join(layersDir, digestToFilename(layerDigest))
	info, err := os.Stat(layerPath)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func ensureWorkdirExists(rootfs, workingDir string) error {
	if strings.TrimSpace(workingDir) == "" {
		return nil
	}

	workdirPath, err := containerPathOnRootfs(rootfs, workingDir)
	if err != nil {
		return err
	}

	return os.MkdirAll(workdirPath, 0755)
}

func parseImageReference(ref string) (name, tag string, err error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", "", fmt.Errorf("image reference cannot be empty")
	}

	lastColon := strings.LastIndex(trimmed, ":")
	lastSlash := strings.LastIndex(trimmed, "/")

	if lastColon > lastSlash {
		name = trimmed[:lastColon]
		tag = trimmed[lastColon+1:]
	} else {
		name = trimmed
		tag = "latest"
	}

	if name == "" || tag == "" {
		return "", "", fmt.Errorf("invalid image reference %q", ref)
	}

	return name, tag, nil
}

func parseRunArgs(args []string) (imageRef string, envOverrides []string, cmdOverride []string, err error) {
	if len(args) == 0 {
		return "", nil, nil, fmt.Errorf("missing image reference")
	}

	commandStarted := false
	for i := 0; i < len(args); i++ {
		arg := args[i]

		if commandStarted {
			cmdOverride = append(cmdOverride, arg)
			continue
		}

		switch {
		case arg == "--":
			commandStarted = true
		case arg == "-e":
			if i+1 >= len(args) {
				return "", nil, nil, fmt.Errorf("missing value for -e (expected KEY=VALUE)")
			}
			envOverrides = append(envOverrides, args[i+1])
			i++
		case strings.HasPrefix(arg, "-e="):
			envOverrides = append(envOverrides, strings.TrimPrefix(arg, "-e="))
		case imageRef == "":
			imageRef = arg
		default:
			if imageRef == "" {
				return "", nil, nil, fmt.Errorf("missing image reference")
			}
			commandStarted = true
			cmdOverride = append(cmdOverride, arg)
		}
	}

	if imageRef == "" {
		return "", nil, nil, fmt.Errorf("missing image reference")
	}

	return imageRef, envOverrides, cmdOverride, nil
}

func mergeEnv(baseEnv, overrides []string) ([]string, error) {
	merged := append([]string(nil), baseEnv...)
	keyIndex := make(map[string]int, len(merged))

	for i, entry := range merged {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 && parts[0] != "" {
			keyIndex[parts[0]] = i
		}
	}

	for _, override := range overrides {
		parts := strings.SplitN(override, "=", 2)
		if len(parts) != 2 || parts[0] == "" {
			return nil, fmt.Errorf("invalid env override %q: expected KEY=VALUE", override)
		}

		entry := parts[0] + "=" + parts[1]
		if idx, exists := keyIndex[parts[0]]; exists {
			merged[idx] = entry
		} else {
			keyIndex[parts[0]] = len(merged)
			merged = append(merged, entry)
		}
	}

	return merged, nil
}

func removeImage(name, tag, imagesPath, layersPath string) (int, error) {
	entries, err := os.ReadDir(imagesPath)
	if err != nil {
		return 0, fmt.Errorf("could not read images directory: %w", err)
	}

	var targetManifest Manifest
	targetManifestPath := ""

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		manifestPath := filepath.Join(imagesPath, entry.Name())
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}

		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			continue
		}

		if manifest.Name == name && manifest.Tag == tag {
			targetManifest = manifest
			targetManifestPath = manifestPath
			continue
		}
	}

	if targetManifestPath == "" {
		return 0, fmt.Errorf("image %s:%s not found in local store", name, tag)
	}

	if err := os.Remove(targetManifestPath); err != nil {
		return 0, fmt.Errorf("failed to remove manifest: %w", err)
	}

	removedLayers := 0
	removedLayerDigests := make(map[string]struct{})
	for _, l := range targetManifest.Layers {
		if _, alreadyRemoved := removedLayerDigests[l.Digest]; alreadyRemoved {
			continue
		}

		layerPath := filepath.Join(layersPath, digestToFilename(l.Digest))
		if err := os.Remove(layerPath); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removedLayers, fmt.Errorf("failed to remove layer %s: %w", l.Digest, err)
		}

		removedLayerDigests[l.Digest] = struct{}{}
		removedLayers++
	}

	return removedLayers, nil
}

func commandToShell(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuoteArg(arg)
	}
	return strings.Join(quoted, " ")
}

func shellQuoteArg(arg string) string {
	if arg == "" {
		return "''"
	}

	if isShellSafeArg(arg) {
		return arg
	}

	return "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
}

func isShellSafeArg(arg string) bool {
	for _, r := range arg {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}

		switch r {
		case '-', '_', '.', '/', ':', '@', '%', '+', ',', '=':
			continue
		default:
			return false
		}
	}

	return true
}
