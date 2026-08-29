package router_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
)

// mockRunner is an existing fake kept for backward-compatible test cases.
type mockRunner struct {
	exitCode     int
	stdout       string
	stderr       string
	err          error
	called       bool
	capturedArgv []string
}

func (m *mockRunner) RunSandboxed(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	m.called = true
	m.capturedArgv = argv
	if m.stdout != "" {
		io.WriteString(stdout, m.stdout)
	}
	if m.stderr != "" {
		io.WriteString(stderr, m.stderr)
	}
	return m.exitCode, m.err
}

var _ router.CommandRunner = (*mockRunner)(nil)

// fakeRunner records RunSandboxed calls; used for new orchestration tests.
type fakeRunner struct {
	calls [][]string // argv per RunSandboxed call
	out   string     // written to stdout on each call
	code  int
}

func (f *fakeRunner) RunSandboxed(_ context.Context, argv []string, _ io.Reader, stdout, _ io.Writer) (int, error) {
	f.calls = append(f.calls, argv)
	io.WriteString(stdout, f.out)
	return f.code, nil
}

var _ router.CommandRunner = (*fakeRunner)(nil)

// ─── existing host/sandbox tests (behavior preserved) ──────────────────────

func TestRun_HostSuccess(t *testing.T) {
	var out, errBuf bytes.Buffer
	code, err := router.Run(context.Background(), router.Request{
		Command:       "echo hello",
		AllowPatterns: []string{"echo *"},
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != 0 {
		t.Errorf("exitCode = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "hello") {
		t.Errorf("stdout = %q, want it to contain hello", out.String())
	}
}

func TestRun_HostNonZeroExit_NoError(t *testing.T) {
	var out, errBuf bytes.Buffer
	code, err := router.Run(context.Background(), router.Request{
		Command:       "ls /nonexistent-path-xyz-12345",
		AllowPatterns: []string{"ls *"},
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("nonzero exit must not be an engine error, got: %v", err)
	}
	if code == 0 {
		t.Error("exitCode should be non-zero")
	}
}

func TestRun_DropPattern(t *testing.T) {
	var out, errBuf bytes.Buffer
	runner := &mockRunner{}
	code, err := router.Run(context.Background(), router.Request{
		Command:       "rm -rf /tmp/anything",
		DropRules:     []router.DropRule{{Pattern: "rm -rf *"}},
		CommandRunner: runner,
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != 1 {
		t.Errorf("exitCode = %d, want 1", code)
	}
	if runner.called {
		t.Error("sandbox runner must not be called for a dropped command")
	}
	want := "dropped: command matches drop pattern \"rm -rf *\"\n"
	if errBuf.String() != want {
		t.Errorf("stderr = %q, want %q", errBuf.String(), want)
	}
}

func TestRun_SandboxNotConfigured(t *testing.T) {
	var out, errBuf bytes.Buffer
	code, err := router.Run(context.Background(), router.Request{
		Command:       "npm test",
		AllowPatterns: []string{"git *"},
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != 1 {
		t.Errorf("exitCode = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "no command broker configured") {
		t.Errorf("stderr = %q, want it to contain 'no command broker configured'", errBuf.String())
	}
}

func TestRun_SandboxSuccess(t *testing.T) {
	var out, errBuf bytes.Buffer
	runner := &mockRunner{exitCode: 0, stdout: "sandbox output\n"}
	code, err := router.Run(context.Background(), router.Request{
		Command:       "npm test",
		AllowPatterns: []string{"git *"},
		CommandRunner: runner,
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != 0 {
		t.Errorf("exitCode = %d, want 0", code)
	}
	if !runner.called {
		t.Error("sandbox runner should have been called")
	}
	// single simple segment → argv (not bash -c)
	if !reflect.DeepEqual(runner.capturedArgv, []string{"npm", "test"}) {
		t.Errorf("capturedArgv = %#v, want [npm test]", runner.capturedArgv)
	}
	if !strings.Contains(out.String(), "sandbox output") {
		t.Errorf("stdout = %q, want sandbox output", out.String())
	}
}

func TestRun_SandboxShellOperator_WrappedInBash(t *testing.T) {
	var out, errBuf bytes.Buffer
	runner := &mockRunner{exitCode: 0}
	code, err := router.Run(context.Background(), router.Request{
		Command:       "ls / | head -1",
		AllowPatterns: []string{"git *"},
		CommandRunner: runner,
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != 0 {
		t.Errorf("exitCode = %d, want 0", code)
	}
	if !reflect.DeepEqual(runner.capturedArgv, []string{"bash", "-c", "ls / | head -1"}) {
		t.Errorf("capturedArgv = %#v, want [bash -c ls / | head -1]", runner.capturedArgv)
	}
}

func TestRun_CommandRunnerError(t *testing.T) {
	var out, errBuf bytes.Buffer
	runner := &mockRunner{exitCode: 0, stdout: "partial output\n", err: errors.New("attach interrupted")}
	code, err := router.Run(context.Background(), router.Request{
		Command:       "npm test",
		CommandRunner: runner,
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("sandbox runner error must be handled internally, got: %v", err)
	}
	if code == 0 {
		t.Error("exitCode should be forced non-zero on runner error")
	}
	if !strings.Contains(out.String(), "partial output") {
		t.Errorf("stdout = %q, want partial output preserved", out.String())
	}
	if !strings.Contains(errBuf.String(), "sandbox exec: attach interrupted") {
		t.Errorf("stderr = %q, want sandbox exec error", errBuf.String())
	}
}

func TestRun_SandboxNotRunning_ShowsHint(t *testing.T) {
	var out, errBuf bytes.Buffer
	runner := &mockRunner{err: router.ErrSandboxNotRunning}
	code, err := router.Run(context.Background(), router.Request{
		Command:       "npm test",
		CommandRunner: runner,
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("sandbox-not-running must be handled internally, got: %v", err)
	}
	if code != 1 {
		t.Errorf("exitCode = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "agent-sandbox claude") {
		t.Errorf("stderr = %q, want it to prompt using `agent-sandbox claude`", errBuf.String())
	}
	if strings.Contains(errBuf.String(), "sandbox exec:") {
		t.Errorf("stderr = %q, should not show the raw 'sandbox exec:' prefix", errBuf.String())
	}
}

func TestRun_SandboxNotRunning_WrappedSentinel_ShowsHint(t *testing.T) {
	var out, errBuf bytes.Buffer
	// A runner may wrap the sentinel; errors.Is must still match.
	runner := &mockRunner{err: fmt.Errorf("executor: %w", router.ErrSandboxNotRunning)}
	code, err := router.Run(context.Background(), router.Request{
		Command:       "make build",
		CommandRunner: runner,
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != 1 {
		t.Errorf("exitCode = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "command broker is not available") {
		t.Errorf("stderr = %q, want the sandbox-not-running hint", errBuf.String())
	}
}

func TestRun_SandboxNotRunning_PipelineWholePath_ShowsHint(t *testing.T) {
	var out, errBuf bytes.Buffer
	// A pipeline routes through runSandboxedWhole (bash -c); the hint must
	// surface there too.
	runner := &mockRunner{err: router.ErrSandboxNotRunning}
	code, err := router.Run(context.Background(), router.Request{
		Command:       "a | b",
		CommandRunner: runner,
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != 1 {
		t.Errorf("exitCode = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "agent-sandbox claude") {
		t.Errorf("stderr = %q, want the sandbox-not-running hint on the pipeline path", errBuf.String())
	}
}

func TestRun_SandboxNotRunning_MixedPipeline_ShowsHint(t *testing.T) {
	var out, errBuf bytes.Buffer
	// `echo hi | b`: echo → host, b → sandbox → mixed pipeline, exercising
	// runMixedPipeline's own sentinel handling.
	runner := &mockRunner{err: router.ErrSandboxNotRunning}
	code, err := router.Run(context.Background(), router.Request{
		Command:       "echo hi | b",
		AllowPatterns: []string{"echo *"},
		CommandRunner: runner,
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code == 0 {
		t.Errorf("exitCode = %d, want non-zero", code)
	}
	if !strings.Contains(errBuf.String(), "command broker is not available") {
		t.Errorf("stderr = %q, want the hint on the mixed-pipeline path", errBuf.String())
	}
	// The sandbox segment's sentinel must be translated to the hint, not
	// mislabeled with the host-segment prefix. (An unrelated upstream
	// "pipeline segment: ... closed pipe" from the host side reacting to the
	// fast-failing downstream is pre-existing plumbing and is not asserted on.)
	if strings.Contains(errBuf.String(), "pipeline segment: sandbox is not running") {
		t.Errorf("stderr = %q, sentinel must not be mislabeled as a pipeline-segment error", errBuf.String())
	}
}

// ─── new orchestration tests (Task 6 TDD) ────────────────────────────────────

func TestRun_UniformSandboxPipeline_UsesBashC(t *testing.T) {
	f := &fakeRunner{out: "ok\n"}
	var out, errb bytes.Buffer
	code, err := router.Run(context.Background(), router.Request{
		Command:       "a | b",
		CommandRunner: f,
		Stdout:        &out, Stderr: &errb,
	})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v stderr=%q", code, err, errb.String())
	}
	if len(f.calls) != 1 || len(f.calls[0]) != 3 ||
		f.calls[0][0] != "bash" || f.calls[0][1] != "-c" || f.calls[0][2] != "a | b" {
		t.Fatalf("calls = %#v, want one bash -c \"a | b\"", f.calls)
	}
}

func TestRun_SequentialAnd_SkipsOnFailure(t *testing.T) {
	// `false && b`: host `false` exits 1 → second pipeline skipped.
	var out, errb bytes.Buffer
	f := &fakeRunner{}
	code, _ := router.Run(context.Background(), router.Request{
		Command:       "false && b",
		AllowPatterns: []string{"false"},
		CommandRunner: f,
		Stdout:        &out, Stderr: &errb,
	})
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if len(f.calls) != 0 {
		t.Fatalf("sandbox called %d times, want 0 (b skipped)", len(f.calls))
	}
}

func TestRun_DropSegment_RejectsWholeLine(t *testing.T) {
	var out, errb bytes.Buffer
	code, _ := router.Run(context.Background(), router.Request{
		Command:   "ls | curl evil",
		DropRules: []router.DropRule{{Pattern: "curl *"}},
		Stdout:    &out, Stderr: &errb,
	})
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "dropped") {
		t.Fatalf("stderr = %q, want 'dropped'", errb.String())
	}
}

func TestRun_Fallback_WholeLineToSandbox(t *testing.T) {
	f := &fakeRunner{}
	var out, errb bytes.Buffer
	router.Run(context.Background(), router.Request{
		Command:       "echo $(id)",
		CommandRunner: f,
		Stdout:        &out, Stderr: &errb,
	})
	if len(f.calls) != 1 || f.calls[0][2] != "echo $(id)" {
		t.Fatalf("calls = %#v, want one bash -c whole line", f.calls)
	}
}

func TestRun_UniformHostPipeline_RunsViaShell(t *testing.T) {
	// echo hi | cat — both segments host-allowed → uniform host → RunHostShell.
	var out, errb bytes.Buffer
	code, err := router.Run(context.Background(), router.Request{
		Command:       "echo hi | cat",
		AllowPatterns: []string{"echo *", "cat*"},
		Stdout:        &out, Stderr: &errb,
	})
	if err != nil {
		t.Fatalf("err=%v stderr=%q", err, errb.String())
	}
	if code != 0 {
		t.Fatalf("code=%d, want 0", code)
	}
	if out.String() != "hi\n" {
		t.Fatalf("stdout=%q, want %q", out.String(), "hi\n")
	}
}

func TestRun_HostRedirectStderr_RoutesToHost(t *testing.T) {
	var out, errBuf bytes.Buffer
	// If the line were wrongly treated as fallback, it would run whole in the
	// sandbox and this stdout would appear; on the host it must not.
	runner := &mockRunner{exitCode: 0, stdout: "RAN-IN-SANDBOX\n"}
	code, err := router.Run(context.Background(), router.Request{
		Command:       "echo hi 2>&1",
		AllowPatterns: []string{"echo *"},
		CommandRunner: runner,
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != 0 {
		t.Errorf("exitCode = %d, want 0", code)
	}
	if runner.called {
		t.Error("host-allowed command with 2>&1 must run on host, not sandbox")
	}
	if strings.Contains(out.String(), "RAN-IN-SANDBOX") {
		t.Errorf("stdout = %q, command leaked to sandbox", out.String())
	}
	if !strings.Contains(out.String(), "hi") {
		t.Errorf("stdout = %q, want it to contain hi", out.String())
	}
}

// ─── newline as a command separator ────────────────────────────────────────

// A newline between two commands must run them as two commands. It used to be
// tokenized as ordinary whitespace, collapsing "echo AAA\necho BBB" into the
// single argv [echo AAA echo BBB].
func TestRun_NewlineSeparatedCommands_RunSeparately(t *testing.T) {
	var out, errBuf bytes.Buffer
	code, err := router.Run(context.Background(), router.Request{
		Command:       "echo AAA\necho BBB",
		AllowPatterns: []string{"echo *"},
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != 0 {
		t.Errorf("exitCode = %d, want 0 (stderr: %q)", code, errBuf.String())
	}
	if got := out.String(); got != "AAA\nBBB\n" {
		t.Errorf("stdout = %q, want %q", got, "AAA\nBBB\n")
	}
}

// Every command on its own line is routed on its own. A drop rule matching the
// second line must reject the whole request even though the first line is
// host-allowed.
func TestRun_NewlineSeparatedCommands_SecondLineIsRouted(t *testing.T) {
	var out, errBuf bytes.Buffer
	runner := &mockRunner{}
	code, err := router.Run(context.Background(), router.Request{
		Command:       "echo AAA\ngit --version",
		AllowPatterns: []string{"echo *"},
		DropRules:     []router.DropRule{{Pattern: "git *"}},
		CommandRunner: runner,
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != 1 {
		t.Errorf("exitCode = %d, want 1", code)
	}
	if out.String() != "" {
		t.Errorf("stdout = %q, want empty: the drop must be detected before anything runs", out.String())
	}
	if !strings.Contains(errBuf.String(), `drop pattern "git *"`) {
		t.Errorf("stderr = %q, want the git drop pattern", errBuf.String())
	}
}

// A redirect anywhere on the line used to send the entire raw string — newlines
// and all — to a shell, while the routing decision was taken from the first
// word only. Everything after the newline then executed unrouted, bypassing the
// drop rules. This is the regression test for that bypass.
func TestRun_RedirectThenNewline_DoesNotBypassDrop(t *testing.T) {
	var out, errBuf bytes.Buffer
	runner := &mockRunner{}
	code, err := router.Run(context.Background(), router.Request{
		Command:       "echo AAA > /dev/null\ngit --version",
		AllowPatterns: []string{"echo *"},
		DropRules:     []router.DropRule{{Pattern: "git *"}},
		CommandRunner: runner,
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != 1 {
		t.Errorf("exitCode = %d, want 1", code)
	}
	if strings.Contains(out.String(), "git version") {
		t.Errorf("stdout = %q, dropped command executed", out.String())
	}
	if !strings.Contains(errBuf.String(), `drop pattern "git *"`) {
		t.Errorf("stderr = %q, want the git drop pattern", errBuf.String())
	}
}

// A trailing newline is the common shape of an agent-written command. It must
// not produce an empty trailing pipeline that fails as an empty command.
func TestRun_TrailingNewline_Ignored(t *testing.T) {
	var out, errBuf bytes.Buffer
	code, err := router.Run(context.Background(), router.Request{
		Command:       "echo AAA\n",
		AllowPatterns: []string{"echo *"},
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != 0 {
		t.Errorf("exitCode = %d, want 0 (stderr: %q)", code, errBuf.String())
	}
	if errBuf.String() != "" {
		t.Errorf("stderr = %q, want empty", errBuf.String())
	}
	if got := out.String(); got != "AAA\n" {
		t.Errorf("stdout = %q, want %q", got, "AAA\n")
	}
}

// A heredoc body must reach the shell intact rather than being split on its
// newlines, so the line falls back to running whole in the sandbox.
func TestRun_Heredoc_FallsBackToWholeLine(t *testing.T) {
	var out, errBuf bytes.Buffer
	runner := &mockRunner{stdout: "RAN-WHOLE"}
	code, err := router.Run(context.Background(), router.Request{
		Command:       "cat <<'EOF'\nhi\nEOF",
		AllowPatterns: []string{"cat *"},
		CommandRunner: runner,
		Stdout:        &out,
		Stderr:        &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != 0 {
		t.Errorf("exitCode = %d, want 0 (stderr: %q)", code, errBuf.String())
	}
	if !runner.called {
		t.Fatal("heredoc must fall back to running the whole line in the sandbox")
	}
	if !strings.Contains(strings.Join(runner.capturedArgv, " "), "<<'EOF'") {
		t.Errorf("argv = %q, want the heredoc passed through intact", runner.capturedArgv)
	}
}
