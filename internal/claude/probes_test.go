package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveExecd_NotOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := ResolveExecd()
	if err == nil || !strings.Contains(err.Error(), "go install github.com/ynny-github/agent-sandbox/cmd/execd@latest") {
		t.Fatalf("ResolveExecd() error = %v, want the install hint", err)
	}
}

func TestResolveExecd_ResolvesSymlinks(t *testing.T) {
	real := filepath.Join(t.TempDir(), "execd-real")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(real, filepath.Join(bin, "execd")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	got, err := ResolveExecd()
	if err != nil {
		t.Fatalf("ResolveExecd() error = %v", err)
	}
	want, _ := filepath.EvalSymlinks(real)
	if got != want {
		t.Errorf("ResolveExecd() = %q, want the symlink target %q (Landlock checks the target)", got, want)
	}
}
