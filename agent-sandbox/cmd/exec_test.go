package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/internal/execd"
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

// outFile returns a file to pass as a command's stdout or stderr, and a func
// that reads back what landed in it. A duplicate of internal/execd's own test
// helper, deliberately: a test fixture is not part of that package's API, and
// one short copy costs less than an export that exists only for this file.
func outFile(t *testing.T) (*os.File, func() string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, func() string {
		b, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
}

// devNull is the stdin a case with no input to send passes; Stdio has no
// branch for an absent file.
func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestRunExecCore_Success(t *testing.T) {
	startFakeExecd(t)

	out, readOut := outFile(t)
	errFile, readErr := outFile(t)
	code := runExecCore(context.Background(), "echo hello",
		execd.Stdio{In: devNull(t), Out: out, Err: errFile})
	if code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr=%q)", code, readErr())
	}
	if !strings.Contains(readOut(), "hello") {
		t.Errorf("stdout = %q, want it to contain hello", readOut())
	}
}

// Running `agent-sandbox exec` outside a claude session leaves the execd
// socket variable unset, so the client cannot be built at all. That is the
// most likely way a user meets this failure, and it must produce the
// actionable hint rather than a raw dial/lookup error.
func TestRunExecCore_NoExecdSocket_ShowsHint(t *testing.T) {
	t.Setenv(execd.SocketEnvVar, "")

	out, _ := outFile(t)
	errFile, readErr := outFile(t)
	code := runExecCore(context.Background(), "true",
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

func TestRunExecCore_NonZeroExit(t *testing.T) {
	startFakeExecd(t)

	out, _ := outFile(t)
	errFile, _ := outFile(t)
	code := runExecCore(context.Background(), "exit 3",
		execd.Stdio{In: devNull(t), Out: out, Err: errFile})
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

func TestRunExecCore_Timeout(t *testing.T) {
	startFakeExecd(t)
	execTimeout = 400 * time.Millisecond
	t.Cleanup(func() { execTimeout = 0 })

	out, _ := outFile(t)
	errFile, readErr := outFile(t)
	start := time.Now()
	code := runExecCore(context.Background(), "sleep 30",
		execd.Stdio{In: devNull(t), Out: out, Err: errFile})

	if code != 124 {
		t.Errorf("exit code = %d, want 124 (stderr=%q)", code, readErr())
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v; want it bounded by --timeout", d)
	}
	if !strings.Contains(readErr(), "timed out") {
		t.Errorf("stderr = %q, want it to say the command timed out", readErr())
	}
}

func TestRunExecCore_ParseFailure(t *testing.T) {
	startFakeExecd(t)

	out, _ := outFile(t)
	errFile, readErr := outFile(t)
	code := runExecCore(context.Background(), `echo "hi`,
		execd.Stdio{In: devNull(t), Out: out, Err: errFile})
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if readErr() == "" {
		t.Error("stderr should describe the parse error")
	}
}

// TestTwoStageSignals_FirstRelaysSecondEscapes pins the interrupt policy
// runExecCore installs: the first signal is forwarded to the command, and the
// second abandons the connection instead. It drives the policy directly rather
// than signalling the test binary, because the real escape resets this
// process's SIGINT disposition and a test must not leave that behind.
func TestTwoStageSignals_FirstRelaysSecondEscapes(t *testing.T) {
	sigs := make(chan os.Signal, 2)
	relay := make(chan syscall.Signal, 1)
	forced := make(chan syscall.Signal, 1)
	stop := make(chan struct{})
	defer close(stop)
	var errBuf bytes.Buffer
	escaped := make(chan struct{})

	go twoStageSignals(sigs, relay, stop, &errBuf, forced, func() { close(escaped) })

	sigs <- syscall.SIGINT
	select {
	case got := <-relay:
		if got != syscall.SIGINT {
			t.Errorf("relayed %v, want SIGINT", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first signal was not relayed")
	}
	select {
	case <-escaped:
		t.Fatal("the first signal closed the connection; only the second may")
	default:
	}

	sigs <- syscall.SIGINT
	select {
	case <-escaped:
	case <-time.After(2 * time.Second):
		t.Fatal("the second signal did not close the connection")
	}
	select {
	case got := <-forced:
		if got != syscall.SIGINT {
			t.Errorf("forced signal = %v, want SIGINT", got)
		}
	default:
		t.Error("the second signal was not reported back for the exit status")
	}
	select {
	case got := <-relay:
		t.Errorf("the second signal was relayed as %v; it must close the connection instead", got)
	default:
	}
	if !strings.Contains(errBuf.String(), "second signal") {
		t.Errorf("stderr = %q, want a line explaining why the second signal differed", errBuf.String())
	}
}
