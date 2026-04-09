package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestParseImageReference(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantRef string
		wantTag string
		wantErr bool
	}{
		{name: "name and tag", input: "myapp:v1", wantRef: "myapp", wantTag: "v1"},
		{name: "default latest", input: "myapp", wantRef: "myapp", wantTag: "latest"},
		{name: "registry with port", input: "localhost:5000/myapp", wantRef: "localhost:5000/myapp", wantTag: "latest"},
		{name: "registry with port and tag", input: "localhost:5000/myapp:v2", wantRef: "localhost:5000/myapp", wantTag: "v2"},
		{name: "empty ref", input: "", wantErr: true},
		{name: "empty tag", input: "myapp:", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRef, gotTag, err := parseImageReference(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if gotRef != tt.wantRef || gotTag != tt.wantTag {
				t.Fatalf("unexpected parse result: got (%q, %q), want (%q, %q)", gotRef, gotTag, tt.wantRef, tt.wantTag)
			}
		})
	}
}

func TestParseRunArgs(t *testing.T) {
	imageRef, envOverrides, cmdOverride, err := parseRunArgs([]string{"alpine:3.18", "-e", "PORT=8080", "echo", "hello"})
	if err != nil {
		t.Fatalf("parseRunArgs returned error: %v", err)
	}

	if imageRef != "alpine:3.18" {
		t.Fatalf("unexpected image ref: %q", imageRef)
	}
	if got, want := strings.Join(envOverrides, ","), "PORT=8080"; got != want {
		t.Fatalf("unexpected env overrides: got %q, want %q", got, want)
	}
	if got, want := strings.Join(cmdOverride, "|"), "echo|hello"; got != want {
		t.Fatalf("unexpected cmd override: got %q, want %q", got, want)
	}
}

func TestParseRunArgs_EnvBeforeImage(t *testing.T) {
	imageRef, envOverrides, cmdOverride, err := parseRunArgs([]string{"-e", "PORT=8080", "myapp:latest", "echo", "ok"})
	if err != nil {
		t.Fatalf("parseRunArgs returned error: %v", err)
	}

	if imageRef != "myapp:latest" {
		t.Fatalf("unexpected image ref: %q", imageRef)
	}
	if got, want := strings.Join(envOverrides, ","), "PORT=8080"; got != want {
		t.Fatalf("unexpected env overrides: got %q, want %q", got, want)
	}
	if got, want := strings.Join(cmdOverride, "|"), "echo|ok"; got != want {
		t.Fatalf("unexpected cmd override: got %q, want %q", got, want)
	}
}

func TestParseRunArgs_RespectsDoubleDash(t *testing.T) {
	_, envOverrides, cmdOverride, err := parseRunArgs([]string{"alpine", "-e", "A=1", "--", "-e", "literal"})
	if err != nil {
		t.Fatalf("parseRunArgs returned error: %v", err)
	}

	if got, want := strings.Join(envOverrides, ","), "A=1"; got != want {
		t.Fatalf("unexpected env overrides: got %q, want %q", got, want)
	}
	if got, want := strings.Join(cmdOverride, "|"), "-e|literal"; got != want {
		t.Fatalf("unexpected cmd override: got %q, want %q", got, want)
	}
}

func TestMergeEnv(t *testing.T) {
	base := []string{"A=1", "B=2"}
	merged, err := mergeEnv(base, []string{"B=updated", "C=3"})
	if err != nil {
		t.Fatalf("mergeEnv returned error: %v", err)
	}

	if got, want := strings.Join(merged, ","), "A=1,B=updated,C=3"; got != want {
		t.Fatalf("unexpected merged env: got %q, want %q", got, want)
	}
}

func TestMergeEnv_InvalidOverride(t *testing.T) {
	_, err := mergeEnv([]string{"A=1"}, []string{"INVALID"})
	if err == nil {
		t.Fatalf("expected error for invalid override")
	}
}

func TestRemoveImage_RemovesAllImageLayers(t *testing.T) {
	tempDir := t.TempDir()
	imagesPath := filepath.Join(tempDir, "images")
	layersPath := filepath.Join(tempDir, "layers")

	if err := os.MkdirAll(imagesPath, 0755); err != nil {
		t.Fatalf("mkdir images path: %v", err)
	}
	if err := os.MkdirAll(layersPath, 0755); err != nil {
		t.Fatalf("mkdir layers path: %v", err)
	}

	uniqueLayer := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	sharedLayer := "sha256:2222222222222222222222222222222222222222222222222222222222222222"

	if err := os.WriteFile(filepath.Join(layersPath, digestToFilename(uniqueLayer)), []byte("unique"), 0644); err != nil {
		t.Fatalf("write unique layer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(layersPath, digestToFilename(sharedLayer)), []byte("shared"), 0644); err != nil {
		t.Fatalf("write shared layer: %v", err)
	}

	writeManifestFile(t, imagesPath, "app.json", Manifest{
		Name: "app",
		Tag:  "latest",
		Layers: []layer{
			{Digest: uniqueLayer},
			{Digest: sharedLayer},
		},
	})

	writeManifestFile(t, imagesPath, "other.json", Manifest{
		Name: "other",
		Tag:  "v1",
		Layers: []layer{
			{Digest: sharedLayer},
		},
	})

	removed, err := removeImage("app", "latest", imagesPath, layersPath)
	if err != nil {
		t.Fatalf("removeImage returned error: %v", err)
	}

	if removed != 2 {
		t.Fatalf("expected 2 removed layers, got %d", removed)
	}

	if _, err := os.Stat(filepath.Join(imagesPath, "app.json")); !os.IsNotExist(err) {
		t.Fatalf("expected app manifest removed, stat err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(imagesPath, "other.json")); err != nil {
		t.Fatalf("expected other manifest to remain: %v", err)
	}

	if _, err := os.Stat(filepath.Join(layersPath, digestToFilename(uniqueLayer))); !os.IsNotExist(err) {
		t.Fatalf("expected unique layer removed, stat err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(layersPath, digestToFilename(sharedLayer))); !os.IsNotExist(err) {
		t.Fatalf("expected shared layer removed, stat err: %v", err)
	}
}

func TestRemoveImage_NotFound(t *testing.T) {
	tempDir := t.TempDir()
	imagesPath := filepath.Join(tempDir, "images")
	layersPath := filepath.Join(tempDir, "layers")

	if err := os.MkdirAll(imagesPath, 0755); err != nil {
		t.Fatalf("mkdir images path: %v", err)
	}
	if err := os.MkdirAll(layersPath, 0755); err != nil {
		t.Fatalf("mkdir layers path: %v", err)
	}

	if _, err := removeImage("missing", "latest", imagesPath, layersPath); err == nil {
		t.Fatalf("expected error for missing image")
	}
}

func TestResolveCopySourceFiles_SupportsDoubleStar(t *testing.T) {
	contextDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(contextDir, "src", "nested"), 0755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "src", "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "src", "nested", "b.txt"), []byte("b"), 0644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "src", "nested", "c.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("write c.go: %v", err)
	}

	matches, err := resolveCopySourceFiles(contextDir, "src/**/*.txt")
	if err != nil {
		t.Fatalf("resolveCopySourceFiles returned error: %v", err)
	}

	relMatches := make([]string, 0, len(matches))
	for _, m := range matches {
		rel, err := filepath.Rel(contextDir, m)
		if err != nil {
			t.Fatalf("rel path failed: %v", err)
		}
		relMatches = append(relMatches, filepath.ToSlash(rel))
	}

	if got, want := strings.Join(relMatches, "|"), "src/a.txt|src/nested/b.txt"; got != want {
		t.Fatalf("unexpected matches: got %q, want %q", got, want)
	}
}
