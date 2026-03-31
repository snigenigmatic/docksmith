package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
		buildStart := time.Now()
		// Example: docksmith build -t myapp:latest .
		// basic parsing
		var tag, contextDir string
		noCache := false

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

		fmt.Printf("Building %s from context %s (No-cache: %v)\n", tag, contextDir, noCache)

		targetName, targetTag := parseImageRef(tag)
		existingTarget, existingTargetPath, err := findImageWithPath(targetName, targetTag)
		if err != nil && !strings.Contains(err.Error(), "not found") {
			fmt.Printf("Build failed: %v\n", err)
			os.Exit(1)
		}

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
						// Cache hit: reuse the existing layer
						fmt.Printf(" ---> [CACHE HIT] %.2fs\n", time.Since(stepStart).Seconds())
						currentLayers = append(currentLayers, layer{
							Digest:    layerDigest,
							Size:      layerSizeOnDisk(layerDigest),
							CreatedBy: inst.Raw,
						})
						prevLayerDigest = layerDigest
						continue
					}
				}

				cascadeMiss = true
				fmt.Printf(" ---> [CACHE MISS] Executing %s...\n", inst.Type)

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

				if err := ensureWorkdirExists(rootfs, currentConfig); err != nil {
					fmt.Printf("Build failed: could not prepare WORKDIR %q: %v\n", currentConfig.WorkingDir, err)
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

				fmt.Printf(" ---> Created layer %s (%.2fs)\n", newDigest[:12], time.Since(stepStart).Seconds())
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

		// Construct the final manifest
		finalManifest := Manifest{
			Name:   targetName,
			Tag:    targetTag,
			Config: currentConfig,
			Layers: currentLayers,
		}
		finalManifest.Created = createdTimestampForBuild(existingTarget, finalManifest)

		// Compute the digest and get the JSON payload
		manifestBytes, err := finalManifest.ComputeAndSetDigest()
		if err != nil {
			fmt.Printf("Build failed: could not compute manifest digest: %v\n", err)
			os.Exit(1)
		}

		if existingTargetPath != "" && existingTargetPath != filepath.Join(imagesDir, strings.ReplaceAll(finalManifest.Digest, ":", "_")+".json") {
			if err := os.Remove(existingTargetPath); err != nil && !os.IsNotExist(err) {
				fmt.Printf("Build failed: could not replace previous manifest: %v\n", err)
				os.Exit(1)
			}
		}

		// Save to the images directory
		manifestFilename := strings.ReplaceAll(finalManifest.Digest, ":", "_") + ".json"
		manifestPath := filepath.Join(imagesDir, manifestFilename)

		if err := os.WriteFile(manifestPath, manifestBytes, 0644); err != nil {
			fmt.Printf("Build failed: could not save manifest: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("\nSuccessfully built %s:%s (%.2fs)\nDigest: %s\n", targetName, targetTag, time.Since(buildStart).Seconds(), finalManifest.Digest)

	case "images":
		// Example: docksmith images
		if err := listImages(os.Stdout, imagesDir); err != nil {
			fmt.Printf("Failed to list images: %v\n", err)
			os.Exit(1)
		}
	case "rmi":
		if len(os.Args) != 3 {
			fmt.Println("Usage : docksmith rmi <name:tag>")
			os.Exit(1)
		}

		if err := removeImage(os.Args[2]); err != nil {
			fmt.Printf("Failed to remove image: %v\n", err)
			os.Exit(1)
		}
	case "run":
		if len(os.Args) < 3 {
			fmt.Println("Usage : docksmith run [-e KEY=VALUE...] <name:tag> [cmd]")
			os.Exit(1)
		}

		exitCode, err := runImage(os.Args[2:])
		if err != nil {
			fmt.Printf("Run failed: %v\n", err)
			os.Exit(1)
		}
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
	fmt.Println("  docksmith run [-e KEY=VALUE...] <name:tag> [cmd]")
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

func parseImageRef(ref string) (string, string) {
	parts := strings.SplitN(ref, ":", 2)
	name := parts[0]
	tag := "latest"
	if len(parts) == 2 && parts[1] != "" {
		tag = parts[1]
	}
	return name, tag
}

func findImageWithPath(name, tag string) (*Manifest, string, error) {
	files, err := os.ReadDir(imagesDir)
	if err != nil {
		return nil, "", fmt.Errorf("could not read images directory: %w", err)
	}

	for _, f := range files {
		if f.IsDir() {
			continue
		}

		path := filepath.Join(imagesDir, f.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}

		if m.Name == name && m.Tag == tag {
			return &m, path, nil
		}
	}

	return nil, "", fmt.Errorf("image %s:%s not found in local store", name, tag)
}

func createdTimestampForBuild(existing *Manifest, next Manifest) string {
	if existing != nil &&
		existing.Name == next.Name &&
		existing.Tag == next.Tag &&
		reflect.DeepEqual(existing.Config, next.Config) &&
		reflect.DeepEqual(existing.Layers, next.Layers) {
		return existing.Created
	}

	return time.Now().UTC().Format(time.RFC3339)
}

func removeImage(ref string) error {
	name, tag := parseImageRef(ref)
	manifest, manifestPath, err := findImageWithPath(name, tag)
	if err != nil {
		return err
	}

	for _, layer := range manifest.Layers {
		layerPath := filepath.Join(layersDir, digestToFilename(layer.Digest))
		if err := os.Remove(layerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("could not remove layer %s: %w", layer.Digest, err)
		}
	}

	if err := os.Remove(manifestPath); err != nil {
		return fmt.Errorf("could not remove manifest: %w", err)
	}

	fmt.Printf("Removed image %s:%s\n", name, tag)
	return nil
}

func ensureWorkdirExists(rootfs string, cfg config) error {
	workdir := normalizeContainerPath(cfg.WorkingDir)
	if workdir == "/" {
		return nil
	}
	return os.MkdirAll(filepath.Join(rootfs, strings.TrimPrefix(workdir, "/")), 0755)
}

func runImage(args []string) (int, error) {
	imageRef, cmdOverride, envOverrides, err := parseRunInvocation(args)
	if err != nil {
		return 0, err
	}

	imageName, imageTag := parseImageRef(imageRef)
	manifest, err := FindImage(imageName, imageTag)
	if err != nil {
		return 0, err
	}

	command := manifest.Config.Cmd
	if len(cmdOverride) > 0 {
		command = cmdOverride
	}
	if len(command) == 0 {
		return 0, fmt.Errorf("no command specified: image %s:%s has no default CMD and no override was provided", imageName, imageTag)
	}

	runtimeConfig := manifest.Config
	runtimeConfig.Env = mergeEnv(runtimeConfig.Env, envOverrides)

	rootfs, err := os.MkdirTemp("", "docksmith-run-*")
	if err != nil {
		return 0, fmt.Errorf("could not create temp rootfs: %w", err)
	}
	defer os.RemoveAll(rootfs)

	for _, imageLayer := range manifest.Layers {
		if err := ExtractLayer(imageLayer.Digest, rootfs); err != nil {
			return 0, fmt.Errorf("could not extract layer %s: %w", imageLayer.Digest, err)
		}
	}

	if err := ensureWorkdirExists(rootfs, runtimeConfig); err != nil {
		return 0, fmt.Errorf("could not prepare WORKDIR %q: %w", runtimeConfig.WorkingDir, err)
	}

	resolvedCommand, err := resolveContainerCommand(rootfs, command, runtimeConfig.Env)
	if err != nil {
		return 0, err
	}

	cmd, err := newIsolatedCommand(rootfs, runtimeConfig, resolvedCommand)
	if err != nil {
		return 0, err
	}

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode := exitErr.ExitCode()
			fmt.Printf("Exit code: %d\n", exitCode)
			return exitCode, nil
		}
		return 0, err
	}

	fmt.Println("Exit code: 0")
	return 0, nil
}

func parseRunInvocation(args []string) (string, []string, []string, error) {
	var positional []string
	var envOverrides []string

	for i := 0; i < len(args); i++ {
		if args[i] == "-e" {
			if i+1 >= len(args) {
				return "", nil, nil, fmt.Errorf("missing value for -e, expected KEY=VALUE")
			}
			if !strings.Contains(args[i+1], "=") {
				return "", nil, nil, fmt.Errorf("invalid environment override %q, expected KEY=VALUE", args[i+1])
			}
			envOverrides = append(envOverrides, args[i+1])
			i++
			continue
		}
		positional = append(positional, args[i])
	}

	if len(positional) == 0 {
		return "", nil, nil, fmt.Errorf("missing image reference")
	}

	imageRef := positional[0]
	command := positional[1:]

	return imageRef, command, envOverrides, nil
}

func mergeEnv(base, overrides []string) []string {
	merged := append([]string(nil), base...)
	indexByKey := make(map[string]int, len(merged))

	for i, envVar := range merged {
		key, _, ok := strings.Cut(envVar, "=")
		if ok {
			indexByKey[key] = i
		}
	}

	for _, envVar := range overrides {
		key, _, _ := strings.Cut(envVar, "=")
		if idx, found := indexByKey[key]; found {
			merged[idx] = envVar
			continue
		}
		indexByKey[key] = len(merged)
		merged = append(merged, envVar)
	}

	return merged
}
