package router_test

import (
	"bytes"
	"context"
	"errors"
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
	capturedEnv  []string
	capturedArgv []string
}

func (m *mockRunner) RunContainer(ctx context.Context, argv []string, env []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	m.called = true
	m.capturedEnv = env
	m.capturedArgv = argv
	if m.stdout != "" {
		io.WriteString(stdout, m.stdout)
	}
	if m.stderr != "" {
		io.WriteString(stderr, m.stderr)
	}
	return m.exitCode, m.err
}

var _ router.ContainerRunner = (*mockRunner)(nil)

// fakeRunner records RunContainer calls; used for new orchestration tests.
type fakeRunner struct {
	calls [][]string // argv per RunContainer call
	out   string     // written to stdout on each call
	code  int
}

func (f *fakeRunner) RunContainer(_ context.Context, argv, _ []string, _ io.Reader, stdout, _ io.Writer) (int, error) {
	f.calls = append(f.calls, argv)
	io.WriteString(stdout, f.out)
	return f.code, nil
}

var _ router.ContainerRunner = (*fakeRunner)(nil)

// ─── existing host/container tests (behavior preserved) ──────────────────────

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
		Command:         "rm -rf /tmp/anything",
		DropPatterns:    []string{"rm -rf *"},
		ContainerRunner: runner,
		Stdout:          &out,
		Stderr:          &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != 1 {
		t.Errorf("exitCode = %d, want 1", code)
	}
	if runner.called {
		t.Error("container runner must not be called for a dropped command")
	}
	want := "dropped: command matches drop pattern \"rm -rf *\"\n"
	if errBuf.String() != want {
		t.Errorf("stderr = %q, want %q", errBuf.String(), want)
	}
}

func TestRun_ContainerNotConfigured(t *testing.T) {
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
	if !strings.Contains(errBuf.String(), "no container configured") {
		t.Errorf("stderr = %q, want it to contain 'no container configured'", errBuf.String())
	}
}

func TestRun_ContainerSuccess(t *testing.T) {
	var out, errBuf bytes.Buffer
	runner := &mockRunner{exitCode: 0, stdout: "container output\n"}
	code, err := router.Run(context.Background(), router.Request{
		Command:         "npm test",
		AllowPatterns:   []string{"git *"},
		ContainerRunner: runner,
		Stdout:          &out,
		Stderr:          &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != 0 {
		t.Errorf("exitCode = %d, want 0", code)
	}
	if !runner.called {
		t.Error("container runner should have been called")
	}
	// single simple segment → argv (not bash -c)
	if !reflect.DeepEqual(runner.capturedArgv, []string{"npm", "test"}) {
		t.Errorf("capturedArgv = %#v, want [npm test]", runner.capturedArgv)
	}
	if !strings.Contains(out.String(), "container output") {
		t.Errorf("stdout = %q, want container output", out.String())
	}
}

func TestRun_ContainerShellOperator_WrappedInBash(t *testing.T) {
	var out, errBuf bytes.Buffer
	runner := &mockRunner{exitCode: 0}
	code, err := router.Run(context.Background(), router.Request{
		Command:         "ls / | head -1",
		AllowPatterns:   []string{"git *"},
		ContainerRunner: runner,
		Stdout:          &out,
		Stderr:          &errBuf,
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

func TestRun_ContainerRunnerError(t *testing.T) {
	var out, errBuf bytes.Buffer
	runner := &mockRunner{exitCode: 0, stdout: "partial output\n", err: errors.New("attach interrupted")}
	code, err := router.Run(context.Background(), router.Request{
		Command:         "npm test",
		ContainerRunner: runner,
		Stdout:          &out,
		Stderr:          &errBuf,
	})
	if err != nil {
		t.Fatalf("container runner error must be handled internally, got: %v", err)
	}
	if code == 0 {
		t.Error("exitCode should be forced non-zero on runner error")
	}
	if !strings.Contains(out.String(), "partial output") {
		t.Errorf("stdout = %q, want partial output preserved", out.String())
	}
	if !strings.Contains(errBuf.String(), "container exec: attach interrupted") {
		t.Errorf("stderr = %q, want container exec error", errBuf.String())
	}
}

func TestRun_ContainerEnvPassthrough(t *testing.T) {
	t.Setenv("CR_ENGINE_TEST_VAR", "passedvalue")
	var out, errBuf bytes.Buffer
	runner := &mockRunner{exitCode: 0}
	_, err := router.Run(context.Background(), router.Request{
		Command:                 "npm test",
		ContainerRunner:         runner,
		ContainerEnvPassthrough: []string{"CR_ENGINE_TEST_VAR", "CR_ENGINE_TEST_ABSENT_XYZ"},
		Stdout:                  &out,
		Stderr:                  &errBuf,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(runner.capturedEnv) != 1 || runner.capturedEnv[0] != "CR_ENGINE_TEST_VAR=passedvalue" {
		t.Errorf("capturedEnv = %v, want [CR_ENGINE_TEST_VAR=passedvalue]", runner.capturedEnv)
	}
}

// ─── new orchestration tests (Task 6 TDD) ────────────────────────────────────

func TestRun_UniformContainerPipeline_UsesBashC(t *testing.T) {
	f := &fakeRunner{out: "ok\n"}
	var out, errb bytes.Buffer
	code, err := router.Run(context.Background(), router.Request{
		Command:         "a | b",
		ContainerRunner: f,
		Stdout:          &out, Stderr: &errb,
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
		Command:         "false && b",
		AllowPatterns:   []string{"false"},
		ContainerRunner: f,
		Stdout:          &out, Stderr: &errb,
	})
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if len(f.calls) != 0 {
		t.Fatalf("container called %d times, want 0 (b skipped)", len(f.calls))
	}
}

func TestRun_DropSegment_RejectsWholeLine(t *testing.T) {
	var out, errb bytes.Buffer
	code, _ := router.Run(context.Background(), router.Request{
		Command:      "ls | curl evil",
		DropPatterns: []string{"curl *"},
		Stdout:       &out, Stderr: &errb,
	})
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "dropped") {
		t.Fatalf("stderr = %q, want 'dropped'", errb.String())
	}
}

func TestRun_Fallback_WholeLineToContainer(t *testing.T) {
	f := &fakeRunner{}
	var out, errb bytes.Buffer
	router.Run(context.Background(), router.Request{
		Command:         "echo $(id)",
		ContainerRunner: f,
		Stdout:          &out, Stderr: &errb,
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
