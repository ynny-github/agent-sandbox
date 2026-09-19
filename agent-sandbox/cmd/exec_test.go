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

// startFakeExecd starts a real execd server (backed by ShellExecutor) on a
// temp socket and points AGENT_SANDBOX_EXECD_SOCKET at it, so runExecCore can
// be exercised end to end without a nono session.
func startFakeExecd(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("", "brk")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "b.sock")

	srv, err := execd.NewServer(sock, execd.NewShellExecutor())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	go srv.Serve()

	t.Setenv(execd.SocketEnvVar, sock)
}

func TestRunExecCore_Success(t *testing.T) {
	startFakeExecd(t)

	var out, errBuf bytes.Buffer
	code := runExecCore(context.Background(), "echo hello", &out, &errBuf)
	if code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr=%q)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "hello") {
		t.Errorf("stdout = %q, want it to contain hello", out.String())
	}
}

// Running `agent-sandbox exec` outside a claude session leaves the execd
// socket variable unset, so the client cannot be built at all. That is the
// most likely way a user meets this failure, and it must produce the
// actionable hint rather than a raw dial/lookup error.
func TestRunExecCore_NoExecdSocket_ShowsHint(t *testing.T) {
	t.Setenv(execd.SocketEnvVar, "")

	var out, errBuf bytes.Buffer
	code := runExecCore(context.Background(), "true", &out, &errBuf)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), execd.SandboxNotRunningHint) {
		t.Errorf("stderr = %q, want the actionable execd hint", errBuf.String())
	}
	if strings.Contains(errBuf.String(), "exec daemon:") {
		t.Errorf("stderr = %q, want the hint instead of the raw setup error", errBuf.String())
	}
}

func TestRunExecCore_NonZeroExit(t *testing.T) {
	startFakeExecd(t)

	var out, errBuf bytes.Buffer
	code := runExecCore(context.Background(), "exit 3", &out, &errBuf)
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

func TestRunExecCore_ParseFailure(t *testing.T) {
	startFakeExecd(t)

	var out, errBuf bytes.Buffer
	code := runExecCore(context.Background(), `echo "hi`, &out, &errBuf)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if errBuf.String() == "" {
		t.Error("stderr should describe the parse error")
	}
}
