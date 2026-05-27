package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteNonoProfile_WritesJSON(t *testing.T) {
	tmp := t.TempDir()
	tomlContent := "[meta]\nname = \"test\"\n"
	tomlPath := filepath.Join(tmp, "test.toml")
	if err := os.WriteFile(tomlPath, []byte(tomlContent), 0644); err != nil {
		t.Fatal(err)
	}

	outputDir := t.TempDir()
	got, err := writeNonoProfile(tomlPath, tmp, outputDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "test.json" {
		t.Errorf("returned filename = %q, want test.json", got)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "test.json")); err != nil {
		t.Errorf("expected test.json to exist: %v", err)
	}
}

func TestWriteNonoProfile_MissingToml(t *testing.T) {
	_, err := writeNonoProfile("/nonexistent/nono.toml", t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing TOML, got nil")
	}
}
