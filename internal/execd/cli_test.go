package execd_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/internal/execd"
)

// serveShell starts a real ShellExecutor-backed server and points
// AGENT_SANDBOX_EXECD_SOCKET at it, so RunCLI runs end to end without nono.
func serveShell(t *testing.T) {
	t.Helper()
	t.Setenv(execd.SocketEnvVar, startTestServer(t, execd.NewShellExecutor()))
}

func TestRunCLI_Success(t *testing.T) {
	serveShell(t)
	out, readOut := outFile(t)
	errFile, readErr := outFile(t)
	code := execd.RunCLI(context.Background(), "echo hello", 0,
		execd.Stdio{In: devNull(t), Out: out, Err: errFile})
	if code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr=%q)", code, readErr())
	}
	if !strings.Contains(readOut(), "hello") {
		t.Errorf("stdout = %q, want it to contain hello", readOut())
	}
}

// Running the client outside a session leaves the socket variable unset. That
// is the most likely way a user meets this failure, and it must produce the
// actionable hint rather than a raw dial/lookup error.
func TestRunCLI_NoSocket_ShowsHint(t *testing.T) {
	t.Setenv(execd.SocketEnvVar, "")
	out, _ := outFile(t)
	errFile, readErr := outFile(t)
	code := execd.RunCLI(context.Background(), "true", 0,
		execd.Stdio{In: devNull(t), Out: out, Err: errFile})
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(readErr(), execd.SandboxNotRunningHint) {
		t.Errorf("stderr = %q, want the actionable execd hint", readErr())
	}
	if strings.Contains(readErr(), "exec daemon:") {
		t.Errorf("stderr = %q, want the hint instead of the raw setup error", readErr())
	}
}

func TestRunCLI_NonZeroExit(t *testing.T) {
	serveShell(t)
	out, _ := outFile(t)
	errFile, _ := outFile(t)
	code := execd.RunCLI(context.Background(), "exit 3", 0,
		execd.Stdio{In: devNull(t), Out: out, Err: errFile})
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

func TestRunCLI_Timeout(t *testing.T) {
	serveShell(t)
	out, _ := outFile(t)
	errFile, readErr := outFile(t)
	start := time.Now()
	code := execd.RunCLI(context.Background(), "sleep 30", 400*time.Millisecond,
		execd.Stdio{In: devNull(t), Out: out, Err: errFile})
	if code != 124 {
		t.Errorf("exit code = %d, want 124 (stderr=%q)", code, readErr())
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v; want it bounded by the timeout", d)
	}
	if !strings.Contains(readErr(), "timed out") {
		t.Errorf("stderr = %q, want it to say the command timed out", readErr())
	}
}

func TestRunCLI_ParseFailure(t *testing.T) {
	serveShell(t)
	out, _ := outFile(t)
	errFile, readErr := outFile(t)
	code := execd.RunCLI(context.Background(), `echo "hi`, 0,
		execd.Stdio{In: devNull(t), Out: out, Err: errFile})
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if readErr() == "" {
		t.Error("stderr should describe the parse error")
	}
}
