package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

func TestRunExecCore_HostSuccess(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Command.Allow = []string{"echo *"}

	var out, errBuf bytes.Buffer
	code := runExecCore(context.Background(), cfg, "echo hello", &out, &errBuf)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "hello") {
		t.Errorf("stdout = %q, want it to contain hello", out.String())
	}
}

func TestRunExecCore_DropPattern(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Command.Drop = []string{"rm -rf *"}

	var out, errBuf bytes.Buffer
	code := runExecCore(context.Background(), cfg, "rm -rf /tmp/x", &out, &errBuf)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	want := "dropped: command matches drop pattern \"rm -rf *\"\n"
	if errBuf.String() != want {
		t.Errorf("stderr = %q, want %q", errBuf.String(), want)
	}
}

func TestRunExecCore_ParseFailure(t *testing.T) {
	// An unterminated quote is a parse error; NeedsContainer returns the error
	// and runExecCore writes it to stderr, returning exit code 1.
	cfg := &config.Config{}
	cfg.Sandbox.Command.Allow = []string{"echo *"}

	var out, errBuf bytes.Buffer
	code := runExecCore(context.Background(), cfg, `echo "hi`, &out, &errBuf)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "unterminated quote") {
		t.Errorf("stderr = %q, want it to contain 'unterminated quote'", errBuf.String())
	}
}
