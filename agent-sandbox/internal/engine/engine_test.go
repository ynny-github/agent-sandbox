package engine_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/engine"
)

type mockRunner struct {
	exitCode    int
	stdout      string
	stderr      string
	err         error
	called      bool
	capturedEnv []string
}

func (m *mockRunner) RunContainer(ctx context.Context, serviceName, cmd string, env []string, stdout, stderr io.Writer) (int, error) {
	m.called = true
	m.capturedEnv = env
	if m.stdout != "" {
		io.WriteString(stdout, m.stdout)
	}
	if m.stderr != "" {
		io.WriteString(stderr, m.stderr)
	}
	return m.exitCode, m.err
}

var _ engine.ContainerRunner = (*mockRunner)(nil)

func TestRun_HostSuccess(t *testing.T) {
	var out, errBuf bytes.Buffer
	code, err := engine.Run(context.Background(), engine.Request{
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
	code, err := engine.Run(context.Background(), engine.Request{
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
	code, err := engine.Run(context.Background(), engine.Request{
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

func TestRun_HostShellOperator_Rejected(t *testing.T) {
	var out, errBuf bytes.Buffer
	code, err := engine.Run(context.Background(), engine.Request{
		Command:       "git log | head -20",
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
	if !strings.Contains(errBuf.String(), "shell operator not allowed on host") {
		t.Errorf("stderr = %q, want host shell-operator rejection", errBuf.String())
	}
}

func TestRun_ContainerNotConfigured(t *testing.T) {
	var out, errBuf bytes.Buffer
	code, err := engine.Run(context.Background(), engine.Request{
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
	code, err := engine.Run(context.Background(), engine.Request{
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
	if !strings.Contains(out.String(), "container output") {
		t.Errorf("stdout = %q, want container output", out.String())
	}
}

func TestRun_ContainerRunnerError(t *testing.T) {
	var out, errBuf bytes.Buffer
	runner := &mockRunner{exitCode: 0, stdout: "partial output\n", err: errors.New("attach interrupted")}
	code, err := engine.Run(context.Background(), engine.Request{
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
	_, err := engine.Run(context.Background(), engine.Request{
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
