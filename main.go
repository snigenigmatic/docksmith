package main

import (
	"fmt"
	"os"
	"path/filepath"
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
		for i:= 0;i<len(args);i++{
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

		if tag=="" || contextDir == "" {
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
		for _, inst := range instructions {
			fmt.Printf("Line %d: [%s] %s\n", inst.LineNum, inst.Type, inst.Args)
		}

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
