package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ynny-github/agent-sandbox/internal/execd"
)

func TestRun_UnknownSubcommand(t *testing.T) {
	if code := run([]string{"bogus"}); code != 2 {
		t.Errorf("run(bogus) = %d, want 2", code)
	}
	if code := run(nil); code != 2 {
		t.Errorf("run() = %d, want 2", code)
	}
}

func TestServe_RequiresSocket(t *testing.T) {
	if code := run([]string{"serve"}); code != 2 {
		t.Errorf("run(serve) = %d, want 2 without --socket", code)
	}
}

func TestExec_NoCommand(t *testing.T) {
	if code := run([]string{"exec", "--"}); code != 2 {
		t.Errorf("run(exec --) = %d, want 2", code)
	}
}

// TestExec_ReturnsTheCommandsStatus drives the real flag parsing and the real
// client loop against an in-process server: the binary's whole job on this
// path is to hand everything after -- to RunCLI and exit with its status.
func TestExec_ReturnsTheCommandsStatus(t *testing.T) {
	dir, err := os.MkdirTemp("", "brk") // short: unix socket paths cap near 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "b.sock")
	srv, err := execd.NewServer(sock, execd.NewShellExecutor())
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	t.Setenv(execd.SocketEnvVar, sock)

	if code := run([]string{"exec", "--timeout", "5s", "--", "exit", "3"}); code != 3 {
		t.Errorf("run(exec -- exit 3) = %d, want 3", code)
	}
}

// TestSessionDeclaresDumbTerm asserts on sessionDeclaresDumbTerm itself:
// what serve adds is that its own process overrides whatever TERM it was
// started with.
func TestSessionDeclaresDumbTerm(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	sessionDeclaresDumbTerm()
	if got := os.Getenv("TERM"); got != "dumb" {
		t.Errorf("TERM = %q, want dumb", got)
	}
}
