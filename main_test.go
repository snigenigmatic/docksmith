package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeManifestFile(t *testing.T, dir, fileName string, m Manifest) {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), data, 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func parseOutputRows(t *testing.T, output string) [][]string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	rows := make([][]string, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, strings.Fields(line))
	}
	return rows
}

func TestListImages_NoImages(t *testing.T) {
	tempDir := t.TempDir()
	var out bytes.Buffer

	if err := listImages(&out, tempDir); err != nil {
		t.Fatalf("listImages returned error: %v", err)
	}

	if got, want := out.String(), "No images found.\n"; got != want {
		t.Fatalf("unexpected output\nwant: %q\n got: %q", want, got)
	}
}

func TestListImages_ParsesAndFormatsOutput(t *testing.T) {
	tempDir := t.TempDir()

	writeManifestFile(t, tempDir, "b.json", Manifest{
		Name:    "myapp",
		Tag:     "latest",
		Digest:  "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		Created: "2026-03-30T14:20:00Z",
	})

	writeManifestFile(t, tempDir, "a.json", Manifest{
		Name:    "api",
		Tag:     "v1",
		Digest:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Created: "2026-03-29T10:00:00Z",
	})

	if err := os.WriteFile(filepath.Join(tempDir, "ignore.txt"), []byte("not json"), 0644); err != nil {
		t.Fatalf("write ignore file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "broken.json"), []byte("{not-valid-json"), 0644); err != nil {
		t.Fatalf("write broken json: %v", err)
	}

	var out bytes.Buffer
	if err := listImages(&out, tempDir); err != nil {
		t.Fatalf("listImages returned error: %v", err)
	}

	rows := parseOutputRows(t, out.String())
	if len(rows) != 3 {
		t.Fatalf("expected header + 2 rows, got %d rows\noutput:\n%s", len(rows), out.String())
	}

	if got, want := strings.Join(rows[0], "|"), "NAME|TAG|IMAGE|ID|CREATED"; got != want {
		t.Fatalf("unexpected header\nwant: %s\n got: %s", want, got)
	}

	if got, want := strings.Join(rows[1], "|"), "api|v1|aaaaaaaaaaaa|2026-03-29T10:00:00Z"; got != want {
		t.Fatalf("unexpected first row\nwant: %s\n got: %s", want, got)
	}

	if got, want := strings.Join(rows[2], "|"), "myapp|latest|1234567890ab|2026-03-30T14:20:00Z"; got != want {
		t.Fatalf("unexpected second row\nwant: %s\n got: %s", want, got)
	}
}

func TestListImages_ShortDigestWithoutPrefix(t *testing.T) {
	tempDir := t.TempDir()
	writeManifestFile(t, tempDir, "short.json", Manifest{
		Name:    "tiny",
		Tag:     "dev",
		Digest:  "abcdef",
		Created: "2026-03-30T00:00:00Z",
	})

	var out bytes.Buffer
	if err := listImages(&out, tempDir); err != nil {
		t.Fatalf("listImages returned error: %v", err)
	}

	rows := parseOutputRows(t, out.String())
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 row, got %d rows\noutput:\n%s", len(rows), out.String())
	}

	if got, want := strings.Join(rows[1], "|"), "tiny|dev|abcdef|2026-03-30T00:00:00Z"; got != want {
		t.Fatalf("unexpected row\nwant: %s\n got: %s", want, got)
	}
}

func TestMergeEnv_OverridesTakePrecedence(t *testing.T) {
	base := []string{"PATH=/bin", "PORT=8080", "DEBUG=false"}
	overrides := []string{"PORT=9000", "MODE=prod"}

	got := mergeEnv(base, overrides)
	want := []string{"PATH=/bin", "PORT=9000", "DEBUG=false", "MODE=prod"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected merged env\nwant: %#v\n got: %#v", want, got)
	}
}

func TestParseRunInvocation_SupportsEnvBeforeImage(t *testing.T) {
	imageRef, command, envOverrides, err := parseRunInvocation([]string{"-e", "GREETING=Howdy", "myapp:latest"})
	if err != nil {
		t.Fatalf("parseRunInvocation returned error: %v", err)
	}

	if imageRef != "myapp:latest" {
		t.Fatalf("unexpected image ref: %q", imageRef)
	}

	if len(command) != 0 {
		t.Fatalf("expected no command override, got %#v", command)
	}

	wantEnv := []string{"GREETING=Howdy"}
	if !reflect.DeepEqual(envOverrides, wantEnv) {
		t.Fatalf("unexpected env overrides\nwant: %#v\n got: %#v", wantEnv, envOverrides)
	}
}

func TestParseRunInvocation_SupportsEnvAfterImage(t *testing.T) {
	imageRef, command, envOverrides, err := parseRunInvocation([]string{"myapp:latest", "-e", "APP_VERSION=9.9"})
	if err != nil {
		t.Fatalf("parseRunInvocation returned error: %v", err)
	}

	if imageRef != "myapp:latest" {
		t.Fatalf("unexpected image ref: %q", imageRef)
	}

	if len(command) != 0 {
		t.Fatalf("expected no command override, got %#v", command)
	}

	wantEnv := []string{"APP_VERSION=9.9"}
	if !reflect.DeepEqual(envOverrides, wantEnv) {
		t.Fatalf("unexpected env overrides\nwant: %#v\n got: %#v", wantEnv, envOverrides)
	}
}

func TestParseRunInvocation_SeparatesImageFromCommand(t *testing.T) {
	imageRef, command, envOverrides, err := parseRunInvocation([]string{"-e", "GREETING=Howdy", "myapp:latest", "echo", "hello"})
	if err != nil {
		t.Fatalf("parseRunInvocation returned error: %v", err)
	}

	if imageRef != "myapp:latest" {
		t.Fatalf("unexpected image ref: %q", imageRef)
	}

	wantCommand := []string{"echo", "hello"}
	if !reflect.DeepEqual(command, wantCommand) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", wantCommand, command)
	}

	wantEnv := []string{"GREETING=Howdy"}
	if !reflect.DeepEqual(envOverrides, wantEnv) {
		t.Fatalf("unexpected env overrides\nwant: %#v\n got: %#v", wantEnv, envOverrides)
	}
}

func TestResolveContainerCommand_FindsAbsoluteSymlinkInRootfsPath(t *testing.T) {
	rootfs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootfs, "bin"), 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	busyboxPath := filepath.Join(rootfs, "bin", "busybox")
	if err := os.WriteFile(busyboxPath, []byte("busybox"), 0755); err != nil {
		t.Fatalf("write busybox: %v", err)
	}

	if err := os.Symlink("/bin/busybox", filepath.Join(rootfs, "bin", "sh")); err != nil {
		t.Fatalf("symlink sh: %v", err)
	}

	got, err := resolveContainerCommand(rootfs, []string{"sh", "-c", "echo hi"}, []string{"PATH=/bin"})
	if err != nil {
		t.Fatalf("resolveContainerCommand returned error: %v", err)
	}

	want := []string{"/bin/sh", "-c", "echo hi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected resolved command\nwant: %#v\n got: %#v", want, got)
	}
}

func TestResolveCopySources_SupportsRecursiveGlob(t *testing.T) {
	contextDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(contextDir, "nested", "deeper"), 0755); err != nil {
		t.Fatalf("mkdir nested dirs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "nested", "deeper", "main.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "top.txt"), []byte("root"), 0644); err != nil {
		t.Fatalf("write top file: %v", err)
	}

	got, err := resolveCopySources(contextDir, "**/*.txt")
	if err != nil {
		t.Fatalf("resolveCopySources returned error: %v", err)
	}

	want := []string{filepath.Join(contextDir, "nested", "deeper", "main.txt")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected matches\nwant: %#v\n got: %#v", want, got)
	}
}

func TestEnsureWorkdirExists_CreatesDirectoryInsideRootfs(t *testing.T) {
	rootfs := t.TempDir()
	cfg := config{WorkingDir: "/app/data"}

	if err := ensureWorkdirExists(rootfs, cfg); err != nil {
		t.Fatalf("ensureWorkdirExists returned error: %v", err)
	}

	info, err := os.Stat(filepath.Join(rootfs, "app", "data"))
	if err != nil {
		t.Fatalf("expected workdir to exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected workdir path to be a directory")
	}
}

func TestConfigMarshalJSON_UsesSpecFieldNames(t *testing.T) {
	data, err := json.Marshal(config{
		Env:        []string{"PORT=9000"},
		Cmd:        []string{"/bin/sh"},
		WorkingDir: "/app",
	})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	jsonText := string(data)
	if !strings.Contains(jsonText, `"Env"`) || !strings.Contains(jsonText, `"Cmd"`) || !strings.Contains(jsonText, `"WorkingDir"`) {
		t.Fatalf("expected spec field names in JSON, got %s", jsonText)
	}
}

func TestConfigUnmarshalJSON_SupportsLegacyFieldNames(t *testing.T) {
	var cfg config
	err := json.Unmarshal([]byte(`{"env":["PORT=8080"],"cmd":["python","main.py"],"working_dir":"/app"}`), &cfg)
	if err != nil {
		t.Fatalf("unmarshal legacy config: %v", err)
	}

	want := config{
		Env:        []string{"PORT=8080"},
		Cmd:        []string{"python", "main.py"},
		WorkingDir: "/app",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("unexpected config\nwant: %#v\n got: %#v", want, cfg)
	}
}

func TestCreatedTimestampForBuild_PreservesExistingTimestampForIdenticalManifest(t *testing.T) {
	existing := &Manifest{
		Name:    "myapp",
		Tag:     "latest",
		Created: "2026-03-30T12:00:00Z",
		Config: config{
			Env:        []string{"PORT=8080"},
			Cmd:        []string{"python", "main.py"},
			WorkingDir: "/app",
		},
		Layers: []layer{
			{Digest: "sha256:abc", Size: 10, CreatedBy: "COPY . /app"},
		},
	}

	next := Manifest{
		Name:   "myapp",
		Tag:    "latest",
		Config: existing.Config,
		Layers: existing.Layers,
	}

	if got := createdTimestampForBuild(existing, next); got != existing.Created {
		t.Fatalf("expected created timestamp %q, got %q", existing.Created, got)
	}
}

func TestRemoveImage_DeletesManifestAndAllLayers(t *testing.T) {
	tempHome := t.TempDir()
	oldImagesDir, oldLayersDir := imagesDir, layersDir
	imagesDir = filepath.Join(tempHome, "images")
	layersDir = filepath.Join(tempHome, "layers")
	t.Cleanup(func() {
		imagesDir = oldImagesDir
		layersDir = oldLayersDir
	})

	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	if err := os.MkdirAll(layersDir, 0755); err != nil {
		t.Fatalf("mkdir layers: %v", err)
	}

	manifest := Manifest{
		Name:    "myapp",
		Tag:     "latest",
		Digest:  "sha256:manifest",
		Created: "2026-03-30T12:00:00Z",
		Layers: []layer{
			{Digest: "sha256:layer1"},
			{Digest: "sha256:layer2"},
		},
	}

	writeManifestFile(t, imagesDir, "manifest.json", manifest)
	for _, imageLayer := range manifest.Layers {
		if err := os.WriteFile(filepath.Join(layersDir, digestToFilename(imageLayer.Digest)), []byte("layer"), 0644); err != nil {
			t.Fatalf("write layer: %v", err)
		}
	}

	if err := removeImage("myapp:latest"); err != nil {
		t.Fatalf("removeImage returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(imagesDir, "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("expected manifest to be removed, got err=%v", err)
	}

	for _, imageLayer := range manifest.Layers {
		if _, err := os.Stat(filepath.Join(layersDir, digestToFilename(imageLayer.Digest))); !os.IsNotExist(err) {
			t.Fatalf("expected layer %s to be removed, got err=%v", imageLayer.Digest, err)
		}
	}
}
