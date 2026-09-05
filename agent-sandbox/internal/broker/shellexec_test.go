package broker_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
)

// runShell executes command in dir and returns exit code, stdout and stderr.
// Every case is bounded: the failure this executor exists to prevent is a
// pipeline that produces its output and then never returns, and a test that
// hangs forever reports that as a timeout of the whole package rather than of
// the case that caused it.
func runShell(t *testing.T, dir, command string, stdin string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var out, errb bytes.Buffer
	// in must stay a nil *interface*, not a typed nil pointer: a nil
	// *strings.Reader assigned to an io.Reader makes a non-nil interface, and
	// the executor would take the stdin path for every case that wants none.
	var in io.Reader
	if stdin != "" {
		in = strings.NewReader(stdin)
	}
	e := broker.NewShellExecutor()
	code, err := e.Run(ctx, command, dir, in, &out, &errb)
	if err != nil {
		t.Fatalf("Run(%q): %v", command, err)
	}
	if ctx.Err() != nil {
		t.Fatalf("Run(%q) did not finish within the timeout", command)
	}
	return code, out.String(), errb.String()
}

func TestShellExecutorRunsASimpleCommand(t *testing.T) {
	code, out, _ := runShell(t, t.TempDir(), "echo hello", "")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Errorf("stdout = %q, want %q", out, "hello")
	}
}

func TestShellExecutorReportsExitStatus(t *testing.T) {
	code, _, _ := runShell(t, t.TempDir(), "exit 3", "")
	if code != 3 {
		t.Errorf("exit = %d, want 3", code)
	}
}

func TestShellExecutorRunsAPipeline(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := runShell(t, dir, "printf 'a\\nb\\nc\\n' | grep b", "")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "b" {
		t.Errorf("stdout = %q, want %q", out, "b")
	}
}

func TestShellExecutorPipelineWithAnEarlyExitingReaderFinishes(t *testing.T) {
	dir := t.TempDir()
	// The reader stops after one line while the writer still has output to
	// produce. Without closing the os/exec read end when the copy fails, the
	// writer blocks forever on a pipe nobody drains.
	code, out, _ := runShell(t, dir, "seq 1 200000 | head -n 1", "")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "1" {
		t.Errorf("stdout = %q, want %q", out, "1")
	}
}

func TestShellExecutorRedirectsStderrIntoAPipe(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := runShell(t, dir, "sh -c 'echo oops >&2' 2>&1 | grep oops", "")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "oops") {
		t.Errorf("stdout = %q, want it to contain %q", out, "oops")
	}
}

func TestShellExecutorHonoursSequencingOperators(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := runShell(t, dir, "false && echo yes || echo no", "")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "no" {
		t.Errorf("stdout = %q, want %q", out, "no")
	}
}

func TestShellExecutorWritesRedirectsRelativeToCwd(t *testing.T) {
	dir := t.TempDir()
	code, _, _ := runShell(t, dir, "echo written > out.txt", "")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	body, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatalf("read out.txt: %v", err)
	}
	if strings.TrimSpace(string(body)) != "written" {
		t.Errorf("out.txt = %q, want %q", body, "written")
	}
}

func TestShellExecutorExpandsGlobsItself(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	code, out, _ := runShell(t, dir, "echo *.go", "")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "a.go b.go" {
		t.Errorf("stdout = %q, want %q", out, "a.go b.go")
	}
}

func TestShellExecutorFeedsStdinToTheFirstCommand(t *testing.T) {
	code, out, _ := runShell(t, t.TempDir(), "cat", "piped-in")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "piped-in" {
		t.Errorf("stdout = %q, want %q", out, "piped-in")
	}
}

func TestShellExecutorReportsAParseError(t *testing.T) {
	e := broker.NewShellExecutor()
	var out, errb bytes.Buffer
	code, err := e.Run(context.Background(), "echo 'unterminated", t.TempDir(), nil, &out, &errb)
	if err != nil {
		t.Fatalf("Run returned an infrastructure error for a syntax error: %v", err)
	}
	if code == 0 {
		t.Errorf("exit = 0, want non-zero for a syntax error")
	}
	if errb.Len() == 0 {
		t.Errorf("stderr is empty; a syntax error must say what is wrong")
	}
}

func TestShellExecutorReportsAMissingCommand(t *testing.T) {
	code, _, errb := runShell(t, t.TempDir(), "definitely-not-a-real-command-xyz", "")
	if code != 127 {
		t.Errorf("exit = %d, want 127", code)
	}
	if errb == "" {
		t.Errorf("stderr is empty; a missing command must say so")
	}
}

// TestShellExecutorRunsMultipleExternalCommandsInOnePipelineStage guards
// against closing the interpreter's own pipe writer once a command finishes:
// the group runs two external commands into the same downstream reader, so if
// anything closed that writer after "echo one" finished, "echo two" would
// write to a closed pipe and "two" would never reach cat's stdin.
func TestShellExecutorRunsMultipleExternalCommandsInOnePipelineStage(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := runShell(t, dir, "{ sh -c 'echo one'; sh -c 'echo two'; } | cat", "")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if got := strings.TrimSpace(out); got != "one\ntwo" {
		t.Errorf("stdout = %q, want %q", got, "one\ntwo")
	}
}

// recordingWriteCloser is a stdout stand-in that notices whether it was
// closed and, once closed, behaves like a real closed transport by failing
// further writes — the way an HTTP response writer or a closed file would.
type recordingWriteCloser struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closed bool
}

func (w *recordingWriteCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, errors.New("write to closed writer")
	}
	return w.buf.Write(p)
}

func (w *recordingWriteCloser) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

func (w *recordingWriteCloser) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func (w *recordingWriteCloser) wasClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

// TestShellExecutorDoesNotCloseTheCallersStdout runs two external commands in
// sequence — sh, not a shell builtin like echo, so each one actually reaches
// execHandler and interposeOutputs — against a stdout that implements
// io.Closer, which is exactly what Task 3 hands the executor for its response
// stream. Only the interpreter — which alone knows when the whole request is
// done with the writer — may end its lifetime; if Run closed it after the
// first command, the second command's output would be silently lost.
func TestShellExecutorDoesNotCloseTheCallersStdout(t *testing.T) {
	out := &recordingWriteCloser{}
	var errb bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	e := broker.NewShellExecutor()
	code, err := e.Run(ctx, "sh -c 'echo one'; sh -c 'echo two'", t.TempDir(), nil, out, &errb)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if out.wasClosed() {
		t.Errorf("Run closed the caller's stdout; the interpreter must own that writer's lifetime")
	}
	if got := strings.TrimSpace(out.String()); got != "one\ntwo" {
		t.Errorf("stdout = %q, want %q (a close between commands would drop \"two\")", got, "one\ntwo")
	}
}

// TestShellExecutorRedirectsBothStreamsOfAnAliasedWriterWithoutLoss covers
// `2>&1`, where hc.Stdout and hc.Stderr become the same writer. Wiring that
// through two independent pipes and copy goroutines races them against each
// other with no synchronization, unlike os/exec's own guarantee for an
// aliased writer ("at most one goroutine at a time will call Write"); the
// race drops roughly half of one stream's output under that bug, so the loop
// below would flake reliably if the aliasing weren't collapsed to one pipe.
func TestShellExecutorRedirectsBothStreamsOfAnAliasedWriterWithoutLoss(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 30; i++ {
		code, out, _ := runShell(t, dir, "sh -c 'echo out; echo err >&2' 2>&1", "")
		if code != 0 {
			t.Fatalf("iteration %d: exit = %d, want 0", i, code)
		}
		if !strings.Contains(out, "out") || !strings.Contains(out, "err") {
			t.Fatalf("iteration %d: stdout = %q, want it to contain both %q and %q", i, out, "out", "err")
		}
	}
}

// TestShellExecutorLooksUpCommandsRelativeToCwd guards the LookPathDir fix:
// exec.LookPath resolves "./script.sh" against the broker process's own
// working directory, not the cwd this executor was given, so a script that
// exists only in the command's cwd would wrongly report "command not found".
func TestShellExecutorLooksUpCommandsRelativeToCwd(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho scripted\n"), 0o700); err != nil {
		t.Fatalf("write script.sh: %v", err)
	}
	code, out, _ := runShell(t, dir, "./script.sh", "")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "scripted" {
		t.Errorf("stdout = %q, want %q", out, "scripted")
	}
}

// TestExecuteAcceptsAnAbsoluteCwd is the positive case for the Execute-level
// cwd check: a well-formed request from a real client runs normally.
func TestExecuteAcceptsAnAbsoluteCwd(t *testing.T) {
	e := broker.NewShellExecutor()
	var out, errb bytes.Buffer
	code, err := e.Execute(context.Background(),
		broker.Request{Command: "echo hi", Cwd: t.TempDir()}, nil, &out, &errb)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0; stderr = %q", code, errb.String())
	}
	if strings.TrimSpace(out.String()) != "hi" {
		t.Errorf("stdout = %q, want %q", out.String(), "hi")
	}
}

// TestExecuteRejectsANonAbsoluteCwd guards the fallback a client's own
// workingDir() helper can produce: it returns "" when os.Getwd fails, and
// silently running the command against this process's own working directory
// in that case would run it somewhere the agent never asked for. A relative
// path is refused for the same reason.
func TestExecuteRejectsANonAbsoluteCwd(t *testing.T) {
	e := broker.NewShellExecutor()
	for _, cwd := range []string{"", "relative/path", "./here"} {
		var out, errb bytes.Buffer
		_, err := e.Execute(context.Background(),
			broker.Request{Command: "echo hi", Cwd: cwd}, nil, &out, &errb)
		if err == nil {
			t.Errorf("Execute() with Cwd=%q: error = nil, want a rejection", cwd)
			continue
		}
		if !strings.Contains(err.Error(), "not an absolute path") {
			t.Errorf("Execute() with Cwd=%q: error = %q, want it to mention an absolute path", cwd, err.Error())
		}
	}
}
