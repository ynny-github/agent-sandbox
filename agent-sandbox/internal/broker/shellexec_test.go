package broker_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
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
