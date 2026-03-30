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
