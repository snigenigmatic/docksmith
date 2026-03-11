package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ─── Manifest types ───────────────────────────────────────────────────────────

// LayerEntry describes one layer in an image manifest.
type LayerEntry struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	CreatedBy string `json:"createdBy"`
}

// ImageConfig holds runtime configuration stored in the manifest.
type ImageConfig struct {
	Env        []string `json:"Env,omitempty"`
	Cmd        []string `json:"Cmd,omitempty"`
	WorkingDir string   `json:"WorkingDir,omitempty"`
}

// ImageManifest is the JSON file stored under ~/.docksmith/images/.
// Field order matches Go struct layout; json.MarshalIndent serialises them
// in declaration order, which we rely on for deterministic digest computation.
type ImageManifest struct {
	Name    string      `json:"name"`
	Tag     string      `json:"tag"`
	Digest  string      `json:"digest"`
	Created string      `json:"created"`
	Config  ImageConfig `json:"config"`
	Layers  []LayerEntry `json:"layers"`
}

// ─── Directory layout ─────────────────────────────────────────────────────────

func DocksmithDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".docksmith")
}
func ImagesDir() string { return filepath.Join(DocksmithDir(), "images") }
func LayersDir() string { return filepath.Join(DocksmithDir(), "layers") }
func CacheDir() string  { return filepath.Join(DocksmithDir(), "cache") }

// InitDirs creates the state directories if they don't already exist.
func InitDirs() error {
	for _, d := range []string{ImagesDir(), LayersDir(), CacheDir()} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return nil
}

// LayerPath returns the on-disk path for a layer identified by its digest.
func LayerPath(digest string) string {
	hash := strings.TrimPrefix(digest, "sha256:")
	return filepath.Join(LayersDir(), hash+".tar")
}

// manifestFilename returns the filename used to store an image manifest.
func manifestFilename(name, tag string) string {
	return filepath.Join(ImagesDir(), name+":"+tag+".json")
}

// ─── Manifest I/O ─────────────────────────────────────────────────────────────

// LoadImage reads and parses the manifest for name:tag.
func LoadImage(name, tag string) (*ImageManifest, error) {
	data, err := os.ReadFile(manifestFilename(name, tag))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("image %s:%s not found in local store", name, tag)
		}
		return nil, err
	}
	var m ImageManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s:%s: %w", name, tag, err)
	}
	return &m, nil
}

// SaveImage writes a manifest to disk.  The digest field is computed here:
// the manifest is first serialised with digest="", the SHA-256 of those bytes
// is the digest, then the final file is written with the real digest value.
func SaveImage(m *ImageManifest) error {
	if err := os.MkdirAll(ImagesDir(), 0755); err != nil {
		return err
	}

	// Compute digest over the canonical form (digest field = "").
	orig := m.Digest
	m.Digest = ""
	canonical, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		m.Digest = orig
		return err
	}
	h := sha256.Sum256(canonical)
	m.Digest = fmt.Sprintf("sha256:%x", h)

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifestFilename(m.Name, m.Tag), data, 0644)
}

// ComputeManifestDigest returns the digest that SaveImage would assign to m,
// without modifying m or writing to disk.
func ComputeManifestDigest(m *ImageManifest) (string, error) {
	orig := m.Digest
	m.Digest = ""
	canonical, err := json.MarshalIndent(m, "", "  ")
	m.Digest = orig
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(canonical)
	return fmt.Sprintf("sha256:%x", h), nil
}

// ListImages returns all image manifests in the local store.
func ListImages() ([]ImageManifest, error) {
	entries, err := os.ReadDir(ImagesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var images []ImageManifest
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(ImagesDir(), e.Name()))
		if err != nil {
			continue
		}
		var m ImageManifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		images = append(images, m)
	}
	return images, nil
}

// RemoveImage deletes the manifest and all associated layer files.
// Note: no reference counting — other images sharing a layer will be broken.
func RemoveImage(name, tag string) error {
	m, err := LoadImage(name, tag)
	if err != nil {
		return err
	}
	for _, l := range m.Layers {
		_ = os.Remove(LayerPath(l.Digest)) // best-effort
	}
	return os.Remove(manifestFilename(name, tag))
}