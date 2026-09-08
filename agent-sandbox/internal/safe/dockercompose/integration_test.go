//go:build integration

package dockercompose_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe/dockercompose"
)

// dockerComposeAvailable reports whether `<RealBinary> compose version`
// succeeds. This probes RealBinary ("realdocker"), not "docker": the
// resolver under test (dockercompose.NewResolver) now runs RealBinary itself
// (see resolve.go), so a skip guard that probed plain "docker" could pass in
// an environment where "docker" works but "realdocker" is not on PATH,
// masking every test below with an unrelated failure instead of a skip.
func dockerComposeAvailable() bool {
	return exec.Command(dockercompose.RealBinary, "compose", "version").Run() == nil
}

func writeCompose(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPrepare_Integration_OutOfCwdBindRefused(t *testing.T) {
	if !dockerComposeAvailable() {
		t.Skip("docker compose not available")
	}
	dir := t.TempDir()
	writeCompose(t, dir, `
services:
  web:
    image: busybox
    volumes:
      - /etc:/host-etc
`)
	v, err := dockercompose.Prepare(context.Background(),
		[]string{"-f", filepath.Join(dir, "compose.yaml"), "config"}, dir, dockercompose.NewResolver())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(v) == 0 {
		t.Fatal("expected a violation for /etc bind, got none")
	}
}

func TestPrepare_Integration_InCwdBindAllowed(t *testing.T) {
	if !dockerComposeAvailable() {
		t.Skip("docker compose not available")
	}
	dir := t.TempDir()
	writeCompose(t, dir, `
services:
  web:
    image: busybox
    volumes:
      - ./data:/data
`)
	v, err := dockercompose.Prepare(context.Background(),
		[]string{"-f", filepath.Join(dir, "compose.yaml"), "config"}, dir, dockercompose.NewResolver())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("expected no violations for ./data bind, got %v", v)
	}
}
