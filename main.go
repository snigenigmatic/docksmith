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
	for -, dir := range dirs {
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
		fmt.Println("TODO: Implement build")
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
