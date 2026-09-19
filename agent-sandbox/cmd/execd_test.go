package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/execd"
)

func TestExecdServesOnTheGivenSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "brk")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "b.sock")

	srv, err := startExecdServer(sock)
	if err != nil {
		t.Fatalf("startExecdServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	go srv.Serve()

	// Chdir affects the whole process, so it must be undone: otherwise a later
	// test's os.Getwd() fails once this test's TempDir is removed on cleanup.
	prevWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { os.Chdir(prevWd) })

	work := t.TempDir()
	if err := os.Chdir(work); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	var out, errb bytes.Buffer
	code, err := execd.NewClient(sock).RunCommand(
		context.Background(), "echo served", nil, &out, &errb, execd.RunOptions{})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0 (stderr=%q)", code, errb.String())
	}
	if strings.TrimSpace(out.String()) != "served" {
		t.Errorf("stdout = %q, want %q", out.String(), "served")
	}
}
