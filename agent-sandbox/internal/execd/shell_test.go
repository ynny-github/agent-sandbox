package execd_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/execd"
)

// syncBuffer is a mutex-protected bytes.Buffer. A pipeline stage can run more
// than one command concurrently (mvdan.cc/sh's own Pipe case does exactly
// this), and each command's own stdout/stderr drain (see wiring) writes
// into whatever the caller supplied independently of the others — a plain
// bytes.Buffer's internal bookkeeping is not safe for that, and a race there
// can silently truncate or lose one side's output. That is not hypothetical: it
// was measured here while this file was being written, with a bare bytes.Buffer
// in this type's place — a message written by one command's own exec handler
// vanished, racing against a different concurrent command's own, empty stderr
// drain. Swapping syncBuffer back for a bytes.Buffer is how to see it again;
// note that -race cannot be used to catch it in this repository, because it
// needs cgo and this build sets CGO_ENABLED=0. execd never has
// this problem:
// internal/execd/server.go's frameWriter already serializes every write
// with its own mutex, for the same reason.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// runShell executes command in dir and returns exit code, stdout and stderr.
// Every case is bounded: the failure this executor exists to prevent is a
// pipeline that produces its output and then never returns, and a test that
// hangs forever reports that as a timeout of the whole package rather than of
// the case that caused it.
func runShell(t *testing.T, dir, command string, stdin string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var out, errb syncBuffer
	// in must stay a nil *interface*, not a typed nil pointer: a nil
	// *strings.Reader assigned to an io.Reader makes a non-nil interface, and
	// the executor would take the stdin path for every case that wants none.
	var in io.Reader
	if stdin != "" {
		in = strings.NewReader(stdin)
	}
	e := execd.NewShellExecutor()
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
	e := execd.NewShellExecutor()
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
		code, err := e.Run(context.Background(), c.line, t.TempDir(), nil, io.Discard, io.Discard)
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
// execHandler and wireOutputs — against a stdout that implements
// io.Closer, which is exactly what Task 3 hands the executor for its response
// stream. Only the interpreter — which alone knows when the whole request is
// done with the writer — may end its lifetime; if Run closed it after the
// first command, the second command's output would be silently lost.
func TestShellExecutorDoesNotCloseTheCallersStdout(t *testing.T) {
	out := &recordingWriteCloser{}
	var errb bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	e := execd.NewShellExecutor()
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
	var out, errb bytes.Buffer
	code, err := e.Execute(context.Background(),
		execd.Request{
			Command:         "echo hi",
			Cwd:             t.TempDir(),
			ProtocolVersion: execd.ProtocolVersion,
		}, nil, &out, &errb)
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
	e := execd.NewShellExecutor()
	for _, cwd := range []string{"", "relative/path", "./here"} {
		var out, errb bytes.Buffer
		_, err := e.Execute(context.Background(),
			execd.Request{
				Command:         "echo hi",
				Cwd:             cwd,
				ProtocolVersion: execd.ProtocolVersion,
			}, nil, &out, &errb)
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
	var errb strings.Builder
	start := time.Now()
	code, err := e.Execute(context.Background(), execd.Request{
		Command:         "sleep 30",
		Cwd:             t.TempDir(),
		TimeoutMs:       400,
		ProtocolVersion: execd.ProtocolVersion,
	}, nil, io.Discard, &errb)
	if err != nil {
		t.Fatalf("Execute = %v", err)
	}
	if code != 124 {
		t.Errorf("exit = %d, want 124", code)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v; want it bounded by the timeout", d)
	}
	if !strings.Contains(errb.String(), "timed out") {
		t.Errorf("stderr = %q, want it to say the command timed out", errb.String())
	}
}

// fakePolicyShim creates a real, executable file at
// <dir>/nono-tool-sandbox-<id>/shims/<name>, a script that execs "cat" by
// name. This is the directory shape isPolicyControlledPath matches, which
// lets these tests drive the launch pacer through the executor's public API
// without a real nono session — the shape of the resolved path is all that
// tier check ever looks at. cat, not a symlink straight to its own
// resolved binary, because on a host where coreutils are one combined
// multi-call binary dispatching on argv[0], a symlink named anything other
// than "cat" would exec that binary with the wrong argv[0] and fail outright.
// NixOS is such a host, and it is checkable in one line there:
// `readlink -f "$(command -v cat)"` ends at .../bin/coreutils, not at a binary
// named cat. Shelling out here is
// fine: this file drives the executor under test from the host, unsandboxed,
// same as every other fixture in this file.
func fakePolicyShim(t *testing.T, dir, id, name string) string {
	t.Helper()
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skipf("cat not found on PATH, needed to build a fake policy-command shim: %v", err)
	}
	shimsDir := filepath.Join(dir, "nono-tool-sandbox-"+id, "shims")
	if err := os.MkdirAll(shimsDir, 0o755); err != nil {
		t.Fatalf("mkdir shims dir: %v", err)
	}
	script := filepath.Join(shimsDir, name)
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec cat\n"), 0o755); err != nil {
		t.Fatalf("write fake shim script: %v", err)
	}
	return shimsDir
}

// TestShellExecutorRunsTwoPolicyCommandsInOnePipe pins the behaviour that
// replaced refusePolicyPipeChains, deleted 2026-09-20: a pipeline with a
// policy-controlled command on both ends is no longer refused, it runs.
//
// This guards the reintroduction of a refusal, and that is all it can guard.
// The hazard the refusal stood in for lives on nono's side of the boundary —
// nono retains a duplicate of the pipe's write end, the downstream stage waits
// for an EOF that never comes, and DrainGrace is what breaks the stall — and
// these fake shims are ordinary host processes that hold no such duplicate, so
// nothing here exercises it. The measurement that does — 21 runs of `git log
// --oneline | git cat-file --batch-check` against nono 0.74.0, 14 of them
// ending only because the grace expired — is recorded on DrainGrace in job.go,
// with the denominator for each of its figures. Read it there rather than
// restating it here: those aggregates do not all share one denominator, and two
// copies of them is two things to keep true.
func TestShellExecutorRunsTwoPolicyCommandsInOnePipe(t *testing.T) {
	dir := t.TempDir()
	shimsDir := fakePolicyShim(t, dir, "pipe-test", "fakepolicy")
	t.Setenv("PATH", shimsDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, tc := range []struct {
		name    string
		command string
	}{
		{"two stages", "fakepolicy | fakepolicy"},
		{"floor command between two policy stages", "fakepolicy | cat | fakepolicy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := runShell(t, dir, tc.command, "hello\n")
			if code != 0 {
				t.Errorf("exit = %d, want 0 (a refusal would be %d); stderr = %q",
					code, execd.ExitCannotStart, errOut)
			}
			if strings.TrimSpace(out) != "hello" {
				t.Errorf("stdout = %q, want %q: both stages must run and the bytes reach the end of the pipe", out, "hello")
			}
			if strings.Contains(errOut, "refused") {
				t.Errorf("stderr = %q, want no refusal: piping two policy-controlled commands together is allowed now", errOut)
			}
		})
	}
}

// TestShellExecutorPacesConsecutivePolicyCommands covers the launch-rate
// defect measured on nono 0.77.0 (Ubuntu 24.04, this repository's
// command-profile.json): policy-controlled commands launched back-to-back
// inside one nono session exhaust something the session reclaims only over
// time, and the shim for every launch past that point fails with "Sandbox
// initialization failed: tool-sandbox shim failed to connect to
// .../supervisor.sock". Measured with `git --version` run 12 times through
// xargs in a single session: at ~50ms apart, 6 succeed and the rest fail
// (3/3 runs); at 500ms apart, 12/12 succeed (3/3 runs). Spacing the launches
// is what avoids it, so that is what this asserts.
func TestShellExecutorPacesConsecutivePolicyCommands(t *testing.T) {
	dir := t.TempDir()
	shimsDir := fakePolicyShim(t, dir, "pacing-test", "fakepolicy")
	t.Setenv("PATH", shimsDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	line := strings.TrimSuffix(strings.Repeat("fakepolicy; ", execd.PolicyLaunchBurst+2), "; ")
	start := time.Now()
	code, _, errOut := runShell(t, dir, line, "")
	elapsed := time.Since(start)

	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %q)", code, errOut)
	}
	if want := 2 * execd.PolicyLaunchInterval; elapsed < want {
		t.Errorf("elapsed = %v, want at least %v: the two launches past the burst should each wait for the bucket to refill", elapsed, want)
	}
}

// TestShellExecutorDoesNotPaceFloorCommands guards the other side of the
// pacing rule: only policy-controlled commands spend the session budget
// launchPacer rations, so a line made of floor commands must run at full
// speed. A regression here would slow every command execd serves.
func TestShellExecutorDoesNotPaceFloorCommands(t *testing.T) {
	dir := t.TempDir()

	// cat, not the `true` builtin: a builtin never reaches the exec handler
	// at all, so it could not show a pacing regression even if one existed.
	line := strings.TrimSuffix(strings.Repeat("cat /dev/null; ", execd.PolicyLaunchBurst+2), "; ")
	start := time.Now()
	code, _, errOut := runShell(t, dir, line, "")
	elapsed := time.Since(start)

	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %q)", code, errOut)
	}
	if elapsed >= execd.PolicyLaunchInterval {
		t.Errorf("elapsed = %v, want well under %v: floor commands must not be paced", elapsed, execd.PolicyLaunchInterval)
	}
}

// TestShellExecutorStopsPacingWhenTheContextIsCancelled covers execd's
// own cancellation path: Server.handle cancels the request context when the
// client goes away, and a pacing wait that ignored it would keep a dead
// request's line crawling through its remaining launches, one per interval.
func TestShellExecutorStopsPacingWhenTheContextIsCancelled(t *testing.T) {
	dir := t.TempDir()
	shimsDir := fakePolicyShim(t, dir, "pacing-cancel-test", "fakepolicy")
	t.Setenv("PATH", shimsDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Six launches past the burst: finishing this line unpaced-by-nothing
	// would take six intervals of waiting.
	line := strings.TrimSuffix(strings.Repeat("fakepolicy; ", execd.PolicyLaunchBurst+6), "; ")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(PolicyLaunchCancelDelay)
		cancel()
	}()

	var out, errb syncBuffer
	var in io.Reader
	start := time.Now()
	code, err := execd.NewShellExecutor().Run(ctx, line, dir, in, &out, &errb)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Run(): %v", err)
	}
	if code == 0 {
		t.Errorf("exit = 0, want non-zero: the line was cancelled partway through")
	}
	// The bound is what makes this test about the pacer rather than about
	// the interpreter: mvdan.cc/sh checks the context between statements, so
	// a pacer that ignored it would still stop the line — but only after
	// sitting out the full interval it was already waiting. Returning inside
	// that wait is the behaviour under test, so the bound sits between the
	// cancellation delay and one interval.
	if want := execd.PolicyLaunchInterval / 2; elapsed >= want {
		t.Errorf("elapsed = %v, want under %v: the cancellation must interrupt the pacing wait, not merely follow it", elapsed, want)
	}
}

// PolicyLaunchCancelDelay fires the cancellation above early in the first
// paced launch's wait, leaving room to tell "the wait was interrupted" apart
// from "the wait finished and then the line stopped".
const PolicyLaunchCancelDelay = execd.PolicyLaunchInterval / 10

// TestRunLeavesNoDescendants reproduces the measured defect this branch
// exists to fix: before Job was wired in, cancelling a command killed only
// its direct child, a surviving grandchild kept the command's stdout pipe
// open, and the handler goroutine — and so Run — never returned. "sleep 2972
// & wait" gives the shell a child (the backgrounded sleep) that is not the
// direct process exec.Command starts (sh is), so a teardown that only kills
// sh's own pid leaves sleep 2972 running and its inherited stdout pipe held
// open.
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
		code, err := e.Run(ctx, "sh -c 'sleep 2972 & wait'", t.TempDir(), nil, io.Discard, io.Discard)
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
