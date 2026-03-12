package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"encoding/json"
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
				// TODO: Step 5 - Layer producing instructions and Build Cache
				fmt.Println(" -> (TODO: Implement layer creation and caching)")
			}
		}

		// At the end of the build, we will save the final manifest
		fmt.Printf("\nFinal Config State:\n WorkingDir: %s\n Env: %v\n Cmd: %v\n", 
			currentConfig.WorkingDir, currentConfig.Env, currentConfig.Cmd)

	case "images":
		// Example: docksmith images
		fmt.Println("TODO: Implement images")
	case "rmi":
		// Example: docksmith rmi myapp:latest
		fmt.Println("TODO: Implement rmi")
	case "run":
		// Example: docksmith run myapp:latest
		fmt.Println("TODO: Implement run")
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
