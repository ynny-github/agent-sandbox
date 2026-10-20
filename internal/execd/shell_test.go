package execd_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/internal/execd"
)

// runShell executes command in dir and returns exit code, stdout and stderr.
// Every case is bounded: the failure this executor exists to prevent is a
// pipeline that produces its output and then never returns, and a test that
// hangs forever reports that as a timeout of the whole package rather than of
// the case that caused it.
//
// The three streams are files, because that is what Run takes now: a request
// runs on the caller's own descriptors and there is no writer for this side to
// collect bytes into. That also removes the mutex-protected buffer this helper
// used to need — a pipeline stage can run more than one command concurrently
// (mvdan.cc/sh's own Pipe case does exactly this), and a plain bytes.Buffer
// written by two of them at once was measured losing output here. A file has
// no such bookkeeping to corrupt.
func runShell(t *testing.T, dir, command string, stdin string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	out, readOut := outFile(t)
	errf, readErr := outFile(t)
	in := devNull(t)
	if stdin != "" {
		in = inFile(t, stdin)
	}
	e := execd.NewShellExecutor()
	code, err := e.Run(ctx, command, dir, execd.Stdio{In: in, Out: out, Err: errf})
	if err != nil {
		t.Fatalf("Run(%q): %v", command, err)
	}
	if ctx.Err() != nil {
		t.Fatalf("Run(%q) did not finish within the timeout", command)
	}
	return code, readOut(), readErr()
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
	e := execd.NewShellExecutor()
	out, _ := outFile(t)
	errf, readErr := outFile(t)
	code, err := e.Run(context.Background(), "echo 'unterminated", t.TempDir(),
		execd.Stdio{In: devNull(t), Out: out, Err: errf})
	if err != nil {
		t.Fatalf("Run returned an infrastructure error for a syntax error: %v", err)
	}
	if code == 0 {
		t.Errorf("exit = 0, want non-zero for a syntax error")
	}
	if readErr() == "" {
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

// TestExitCodesAreNamed checks the reported status against the named
// constants directly, rather than the bare literal, so a caller that reads
// execd.ExitNotFound / execd.ExitSyntaxError in their own code sees them tied
// to the outcome that produces each one.
func TestExitCodesAreNamed(t *testing.T) {
	cases := []struct {
		line string
		want int
	}{
		{"definitely-not-a-command", execd.ExitNotFound},
		{"if", execd.ExitSyntaxError},
		{"sh -c 'exit 42'", 42},
	}
	e := execd.NewShellExecutor()
	for _, c := range cases {
		code, err := e.Run(context.Background(), c.line, t.TempDir(),
			execd.Stdio{In: devNull(t), Out: nullOut(t), Err: nullOut(t)})
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		if code != c.want {
			t.Errorf("%q exit = %d, want %d", c.line, code, c.want)
		}
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

// TestShellExecutorDoesNotCloseTheCallersStdout runs two external commands in
// sequence — sh, not a shell builtin like echo, so each one actually reaches
// execHandler and wireOutputs — and then writes to the caller's stdout itself.
// That last write is the assertion: the file belongs to whoever passed it, and
// only they may end its lifetime. Run closing it after the first command would
// silently lose the second command's output; Run closing it at all would break
// a caller that passes one file to several requests, which is exactly what the
// server does when it hands the same trio to a whole request.
func TestShellExecutorDoesNotCloseTheCallersStdout(t *testing.T) {
	out, readOut := outFile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	e := execd.NewShellExecutor()
	code, err := e.Run(ctx, "sh -c 'echo one'; sh -c 'echo two'", t.TempDir(),
		execd.Stdio{In: devNull(t), Out: out, Err: nullOut(t)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if _, werr := out.WriteString("still-open\n"); werr != nil {
		t.Errorf("Run closed the caller's stdout: %v; only the caller may end that file's lifetime", werr)
	}
	if got := strings.Fields(readOut()); len(got) != 3 ||
		got[0] != "one" || got[1] != "two" || got[2] != "still-open" {
		t.Errorf("stdout = %q, want \"one\", \"two\" and then the caller's own write "+
			"(a close between commands would drop \"two\")", readOut())
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
// exec.LookPath resolves "./script.sh" against execd's own
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
	e := execd.NewShellExecutor()
	out, readOut := outFile(t)
	errf, readErr := outFile(t)
	code, err := e.Execute(context.Background(),
		execd.Request{
			Command: "echo hi",
			Cwd:     t.TempDir(),
		}, execd.Stdio{In: devNull(t), Out: out, Err: errf})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0; stderr = %q", code, readErr())
	}
	if strings.TrimSpace(readOut()) != "hi" {
		t.Errorf("stdout = %q, want %q", readOut(), "hi")
	}
}

// TestExecuteRejectsANonAbsoluteCwd guards the fallback a client's own
// workingDir() helper can produce: it returns "" when os.Getwd fails, and
// silently running the command against this process's own working directory
// in that case would run it somewhere the agent never asked for. A relative
// path is refused for the same reason.
func TestExecuteRejectsANonAbsoluteCwd(t *testing.T) {
	e := execd.NewShellExecutor()
	for _, cwd := range []string{"", "relative/path", "./here"} {
		_, err := e.Execute(context.Background(),
			execd.Request{
				Command: "echo hi",
				Cwd:     cwd,
			}, execd.Stdio{In: devNull(t), Out: nullOut(t), Err: nullOut(t)})
		if err == nil {
			t.Errorf("Execute() with Cwd=%q: error = nil, want a rejection", cwd)
			continue
		}
		if !strings.Contains(err.Error(), "not an absolute path") {
			t.Errorf("Execute() with Cwd=%q: error = %q, want it to mention an absolute path", cwd, err.Error())
		}
	}
}

// TestExecuteTimesOut checks that a request whose TimeoutMs elapses is
// bounded by it rather than by the command's own duration, and that it
// reports ExitTimeout rather than whatever status the killed command would
// have produced.
func TestExecuteTimesOut(t *testing.T) {
	e := execd.NewShellExecutor()
	errf, readErr := outFile(t)
	start := time.Now()
	code, err := e.Execute(context.Background(), execd.Request{
		Command:   "sleep 30",
		Cwd:       t.TempDir(),
		TimeoutMs: 400,
	}, execd.Stdio{In: devNull(t), Out: nullOut(t), Err: errf})
	if err != nil {
		t.Fatalf("Execute = %v", err)
	}
	if code != 124 {
		t.Errorf("exit = %d, want 124", code)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v; want it bounded by the timeout", d)
	}
	if !strings.Contains(readErr(), "timed out") {
		t.Errorf("stderr = %q, want it to say the command timed out", readErr())
	}
}

// TestRunLeavesNoDescendants reproduces the measured defect this branch
// exists to fix: before Job was wired in, cancelling a command killed only
// its direct child, a surviving grandchild kept the command's stdout pipe
// open, and the handler goroutine — and so Run — never returned. "sleep 2972
// & wait" gives the shell a child (the backgrounded sleep) that is not the
// direct process exec.Command starts (sh is), so a teardown that only kills
// sh's own pid leaves sleep 2972 running.
//
// The held-pipe half of that defect is no longer reachable from Run at all:
// the caller's stdout is a descriptor the child is given directly, so there is
// no drain of this side's for a survivor to hold open. What this pins is the
// teardown — that the group, grandchild included, is gone when the request is
// — and that Run returns.
//
// The test is only a guard against that defect if the descendant is proven
// to have actually run: without the existence poll below, a Run that
// rejected the line in milliseconds — an unresolvable "sh", a pacer refusal,
// a future change that makes Run reject the line outright — would return
// well inside the 4s bound and leave pgrep with nothing to find either way,
// and this test would pass green having exercised none of the teardown
// machinery it exists to check. So it polls for the descendant to exist
// before the context's cancellation gets a chance to tear it down, and it
// fails on a 126/127 exit the same way a stray infrastructure error fails
// it: those mean the line never ran rather than that it ran and was torn
// down cleanly.
//
// What it pins is HOST behaviour, and the distinction is the whole caveat:
// the "sh" it resolves is the host's real shell, so sh, the backgrounded
// sleep and Job's killpg are all on the same side of the sandbox boundary and
// the teardown reaches. Under nono, where sh is a policy-controlled command
// reached through a shim, the identical line strands: measured on nono
// 0.74.0, 2026-09-20, `sh -c 'sleep 60'` under a 3000ms request timeout
// reported 124 and left the sleep running, 3/3 runs, because the shim puts
// the real shell and its children inside a child sandbox outside the process
// group this side created. So read a green run here as "execd tears down what
// it started", never as "no command can outlive a request". See the Job doc
// comment in job.go.
func TestRunLeavesNoDescendants(t *testing.T) {
	e := execd.NewShellExecutor()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Registered before the existence poll, so a t.Fatal there (the
	// descendant never appearing) still tears down a leaked sleep 2972
	// instead of skipping straight past cleanup.
	t.Cleanup(func() { exec.Command("pkill", "-f", "sleep 2972").Run() })

	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, err := e.Run(ctx, "sh -c 'sleep 2972 & wait'", t.TempDir(),
			execd.Stdio{In: devNull(t), Out: nullOut(t), Err: nullOut(t)})
		done <- result{code, err}
	}()

	// Poll for the grandchild rather than assuming a fixed delay is long
	// enough: the deadline sits comfortably inside the 500ms the context
	// gives the line before cancelling it, and sh forks the backgrounded
	// sleep near-instantly, so this reliably observes it before teardown
	// starts rather than racing that teardown.
	deadline := time.Now().Add(400 * time.Millisecond)
	for {
		out, _ := exec.Command("pgrep", "-f", "sleep 2972").Output()
		if len(strings.Fields(string(out))) != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("descendant never appeared; sleep 2972 was not observed running")
		}
		time.Sleep(5 * time.Millisecond)
	}

	var res result
	select {
	case res = <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("Run did not return within 4s after the context was cancelled")
	}
	if res.err != nil {
		t.Fatalf("Run returned an infrastructure error: %v", res.err)
	}
	if res.code == 126 || res.code == 127 {
		t.Fatalf("Run rejected the line (exit %d) instead of running it; the descendant this test checks for never had a chance to run", res.code)
	}

	out, _ := exec.Command("pgrep", "-f", "sleep 2972").Output()
	if pids := strings.Fields(string(out)); len(pids) != 0 {
		t.Errorf("descendants survived the request: %v", pids)
	}
}

// A non-interactive bash sources $BASH_ENV before the command line; execd's
// own environment must not get to inject a script that way.
func TestShellExecutorDoesNotSourceBashEnv(t *testing.T) {
	script := filepath.Join(t.TempDir(), "env.sh")
	if err := os.WriteFile(script, []byte("echo SOURCED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASH_ENV", script)
	code, out, _ := runShell(t, t.TempDir(), "echo ran", "")
	if code != 0 || strings.Contains(out, "SOURCED") {
		t.Errorf("exit %d, stdout %q: BASH_ENV must not be sourced", code, out)
	}
}
