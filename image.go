package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Manifest - represents the image's internal structure, including its layers and metadata
type Manifest struct {
	Name    string  `json:"name"`
	Tag     string  `json:"tag"`
	Digest  string  `json:"digest"`
	Created string  `json:"created"`
	Config  config  `json:"config"`
	Layers  []layer `json:"layers"`
}

type config struct {
	Env        []string
	Cmd        []string
	WorkingDir string
}

func (c config) MarshalJSON() ([]byte, error) {
	type configJSON struct {
		Env        []string `json:"Env"`
		Cmd        []string `json:"Cmd"`
		WorkingDir string   `json:"WorkingDir"`
	}

	return json.Marshal(configJSON{
		Env:        c.Env,
		Cmd:        c.Cmd,
		WorkingDir: c.WorkingDir,
	})
}

func (c *config) UnmarshalJSON(data []byte) error {
	type configJSON struct {
		Env        []string `json:"Env"`
		Cmd        []string `json:"Cmd"`
		WorkingDir string   `json:"WorkingDir"`
	}
	type legacyConfigJSON struct {
		Env        []string `json:"env"`
		Cmd        []string `json:"cmd"`
		WorkingDir string   `json:"working_dir"`
	}

	var current configJSON
	if err := json.Unmarshal(data, &current); err != nil {
		return err
	}

	var legacy legacyConfigJSON
	if err := json.Unmarshal(data, &legacy); err != nil {
		return err
	}

	c.Env = current.Env
	if len(c.Env) == 0 {
		c.Env = legacy.Env
	}

	c.Cmd = current.Cmd
	if len(c.Cmd) == 0 {
		c.Cmd = legacy.Cmd
	}

	c.WorkingDir = current.WorkingDir
	if c.WorkingDir == "" {
		c.WorkingDir = legacy.WorkingDir
	}

	return nil
}

type layer struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	CreatedBy string `json:"created_by"`
}

// ComputeAndSetDigest - calculates canonical digest for the manifest
func (m *Manifest) ComputeAndSetDigest() ([]byte, error) {
	// Spec: serialize with digest field set to empty string
	m.Digest = ""
	canonicalBytes, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}

	// Compute SHA-256
	hash := sha256.Sum256(canonicalBytes)
	m.Digest = fmt.Sprintf("sha256:%x", hash)

	// Return the final JSON with the digest included
	return json.MarshalIndent(m, "", "  ")
}

// FindImage - searches ~/.docksmith/images/ for a matching name and tag
func FindImage(name, tag string) (*Manifest, error) {
	files, err := os.ReadDir(imagesDir)
	if err != nil {
		return nil, fmt.Errorf("could not read images directory: %w", err)
	}

	for _, f := range files {
		if f.IsDir() {
			continue
		}

		data, err := os.ReadFile(filepath.Join(imagesDir, f.Name()))
		if err != nil {
			continue
		}

		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}

		if m.Name == name && m.Tag == tag {
			return &m, nil
		}
	}

	return nil, fmt.Errorf("image %s:%s not found in local store", name, tag)
}
