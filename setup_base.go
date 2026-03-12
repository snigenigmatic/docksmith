//go:build ignore
// +build ignore

package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	homeDir, _ := os.UserHomeDir()
	layersDir := filepath.Join(homeDir, ".docksmith", "layers")
	imagesDir := filepath.Join(homeDir, ".docksmith", "images")

	// 1. Download Alpine Mini Rootfs
	fmt.Println("Downloading Alpine 3.18 base layer...")
	url := "https://dl-cdn.alpinelinux.org/alpine/v3.18/releases/x86_64/alpine-minirootfs-3.18.4-x86_64.tar.gz"
	
	resp, err := http.Get(url)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	// 2. Write to a temp file and compute SHA256
	tmpFile, _ := os.CreateTemp("", "alpine-*.tar.gz")
	defer os.Remove(tmpFile.Name())

	hasher := sha256.New()
	writer := io.MultiWriter(tmpFile, hasher)
	size, err := io.Copy(writer, resp.Body)
	if err != nil {
		panic(err)
	}
	tmpFile.Close()

	layerDigest := fmt.Sprintf("sha256:%x", hasher.Sum(nil))
	layerPath := filepath.Join(layersDir, layerDigest)

	// 3. Move layer to the content-addressed store
	os.Rename(tmpFile.Name(), layerPath)
	fmt.Printf("Saved layer: %s (%d bytes)\n", layerDigest, size)

	// 4. Create the Manifest
	manifest := map[string]interface{}{
		"name":    "alpine",
		"tag":     "3.18",
		"digest":  "",
		"created": time.Now().UTC().Format(time.RFC3339),
		"config": map[string]interface{}{
			"Env":[]string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
			"Cmd":[]string{"/bin/sh"},
			"WorkingDir": "/",
		},
		"layers": []map[string]interface{}{
			{
				"digest":    layerDigest,
				"size":      size,
				"createdBy": "Alpine 3.18 minirootfs",
			},
		},
	}

	// 5. Compute Manifest Digest (Spec requirement)
	canonicalBytes, _ := json.MarshalIndent(manifest, "", "  ")
	manifestHash := sha256.Sum256(canonicalBytes)
	manifestDigest := fmt.Sprintf("sha256:%x", manifestHash)
	manifest["digest"] = manifestDigest

	finalBytes, _ := json.MarshalIndent(manifest, "", "  ")
	manifestPath := filepath.Join(imagesDir, manifestDigest)
	os.WriteFile(manifestPath, finalBytes, 0644)

	fmt.Printf("Successfully imported alpine:3.18\nManifest: %s\n", manifestDigest)
}