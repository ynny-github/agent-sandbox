package cmd

import (
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

	out, readOut := outFile(t)
	errFile, readErr := outFile(t)
	code, err := execd.NewClient(sock).RunCommand(
		context.Background(), "echo served",
		execd.Stdio{In: devNull(t), Out: out, Err: errFile}, execd.RunOptions{})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0 (stderr=%q)", code, readErr())
	}
	if strings.TrimSpace(readOut()) != "served" {
		t.Errorf("stdout = %q, want %q", readOut(), "served")
	}
}

// TestSessionDeclaresDumbTerm asserts on sessionDeclaresDumbTerm itself,
// rather than on a child process's inherited environment: a child seeing
// TERM=dumb after t.Setenv("TERM", "dumb") would only prove the interpreter
// passes the environment through, which was already true before this task.
// What this task adds is that execd's own session overrides whatever TERM it
// was started with, which is what this test drives directly.
func TestSessionDeclaresDumbTerm(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	sessionDeclaresDumbTerm()
	if got := os.Getenv("TERM"); got != "dumb" {
		t.Errorf("TERM = %q, want %q", got, "dumb")
	}
}
