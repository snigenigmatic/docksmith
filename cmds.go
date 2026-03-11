package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ─── docksmith build ──────────────────────────────────────────────────────────

func cmdBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	tagFlag := fs.String("t", "", "Image name and tag (name:tag)")
	noCache := fs.Bool("no-cache", false, "Skip all cache lookups and writes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tagFlag == "" {
		return fmt.Errorf("build requires -t <name:tag>")
	}
	remaining := fs.Args()
	if len(remaining) == 0 {
		return fmt.Errorf("build requires a context directory")
	}
	contextDir := remaining[0]

	contextDir, err := filepath.Abs(contextDir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(contextDir); err != nil {
		return fmt.Errorf("context directory %q: %w", contextDir, err)
	}

	name, tag := parseNameTag(*tagFlag)
	start := time.Now()
	if err := BuildImage(contextDir, name, tag, *noCache); err != nil {
		return err
	}
	elapsed := time.Since(start)
	_ = elapsed // already printed inside BuildImage
	return nil
}

// ─── docksmith images ─────────────────────────────────────────────────────────

func cmdImages() error {
	images, err := ListImages()
	if err != nil {
		return err
	}
	fmt.Printf("%-20s %-10s %-14s  %s\n", "NAME", "TAG", "ID", "CREATED")
	for _, m := range images {
		shortID := m.Digest
		if len(shortID) > 19 { // "sha256:" + 12 chars
			shortID = shortID[7:19]
		}
		fmt.Printf("%-20s %-10s %-14s  %s\n", m.Name, m.Tag, shortID, m.Created)
	}
	return nil
}

// ─── docksmith rmi ────────────────────────────────────────────────────────────

func cmdRmi(nameTag string) error {
	name, tag := parseNameTag(nameTag)
	if err := RemoveImage(name, tag); err != nil {
		return err
	}
	fmt.Printf("Removed %s:%s\n", name, tag)
	return nil
}

// ─── docksmith run ────────────────────────────────────────────────────────────

func cmdRun(args []string) error {
	// Collect -e flags, then image name, then optional command override.
	var envOverrides []string
	var remaining []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-e" && i+1 < len(args) {
			envOverrides = append(envOverrides, args[i+1])
			i++
		} else if strings.HasPrefix(args[i], "-e=") {
			envOverrides = append(envOverrides, args[i][3:])
		} else {
			remaining = append(remaining, args[i])
		}
	}

	if len(remaining) == 0 {
		return fmt.Errorf("run requires <name:tag>")
	}
	name, tag := parseNameTag(remaining[0])
	cmdOverride := remaining[1:]

	m, err := LoadImage(name, tag)
	if err != nil {
		return err
	}

	// Determine the command to run.
	runCmd := m.Config.Cmd
	if len(cmdOverride) > 0 {
		runCmd = cmdOverride
	}
	if len(runCmd) == 0 {
		return fmt.Errorf("no CMD defined in image %s:%s and no command provided", name, tag)
	}

	// Build environment: image ENV values first, then -e overrides.
	envMap := make(map[string]string)
	for _, kv := range m.Config.Env {
		idx := strings.IndexByte(kv, '=')
		if idx > 0 {
			envMap[kv[:idx]] = kv[idx+1:]
		}
	}
	for _, kv := range envOverrides {
		idx := strings.IndexByte(kv, '=')
		if idx > 0 {
			envMap[kv[:idx]] = kv[idx+1:]
		} else {
			// KEY without value: pass through from host.
			if v, ok := os.LookupEnv(kv); ok {
				envMap[kv] = v
			}
		}
	}
	envSlice := buildEnvSlice(envMap)

	workdir := m.Config.WorkingDir
	if workdir == "" {
		workdir = "/"
	}

	// Assemble rootfs.
	tmpDir, err := AssembleRootfs(m)
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	rootfs := tmpDir + "/rootfs"
	exitCode, err := RunContainer(rootfs, runCmd, envSlice, workdir)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		fmt.Fprintf(os.Stderr, "container exited with code %d\n", exitCode)
		os.Exit(exitCode)
	}
	return nil
}

// ─── docksmith import-base ────────────────────────────────────────────────────

// cmdImportBase imports a raw layer tar as a base image.
// Usage: docksmith import-base <name:tag> <layer.tar> [--cmd '["/bin/sh"]'] [--env KEY=VALUE ...]
func cmdImportBase(args []string) error {
	fs := flag.NewFlagSet("import-base", flag.ContinueOnError)
	cmdJSON := fs.String("cmd", `["/bin/sh"]`, "Default CMD as JSON array")
	var envFlags multiStringFlag
	fs.Var(&envFlags, "env", "Environment variable KEY=VALUE (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	remaining := fs.Args()
	if len(remaining) < 2 {
		return fmt.Errorf("usage: docksmith import-base <name:tag> <layer.tar>")
	}
	name, tag := parseNameTag(remaining[0])
	layerFile := remaining[1]

	// Read and store the layer.
	data, err := os.ReadFile(layerFile)
	if err != nil {
		return fmt.Errorf("read layer file: %w", err)
	}
	layerDigest, err := StoreLayer(data)
	if err != nil {
		return fmt.Errorf("store layer: %w", err)
	}

	layerSize := int64(len(data))

	// Parse CMD.
	var cmd []string
	if err := json.Unmarshal([]byte(*cmdJSON), &cmd); err != nil {
		return fmt.Errorf("parse --cmd: %w", err)
	}

	// Build ENV map.
	envMap := make(map[string]string)
	for _, kv := range envFlags {
		idx := strings.IndexByte(kv, '=')
		if idx > 0 {
			envMap[kv[:idx]] = kv[idx+1:]
		}
	}
	envSlice := buildEnvSlice(envMap)

	m := &ImageManifest{
		Name:    name,
		Tag:     tag,
		Created: time.Now().UTC().Format(time.RFC3339),
		Config: ImageConfig{
			Env: envSlice,
			Cmd: cmd,
		},
		Layers: []LayerEntry{
			{
				Digest:    layerDigest,
				Size:      layerSize,
				CreatedBy: "imported from " + filepath.Base(layerFile),
			},
		},
	}

	if err := SaveImage(m); err != nil {
		return fmt.Errorf("save manifest: %w", err)
	}

	m2, _ := LoadImage(name, tag)
	fmt.Printf("Imported %s:%s  digest=%s\n", name, tag, shortDigest(m2.Digest))
	return nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func shortDigest(d string) string {
	if len(d) > 19 {
		return d[:19]
	}
	return d
}

// multiStringFlag is a flag.Value that accumulates repeated --flag values.
type multiStringFlag []string

func (f *multiStringFlag) String() string { return strings.Join(*f, ",") }
func (f *multiStringFlag) Set(v string) error {
	*f = append(*f, v)
	return nil
}

// unused import guard
var _ = io.EOF