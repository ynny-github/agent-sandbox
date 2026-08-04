package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/policysnapshot"
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
	cfg.Sandbox.Command.Drop = []config.DropRule{{Pattern: "rm -rf *"}}

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

func TestRunExecCore_DropPattern_CustomMessage(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Command.Drop = []config.DropRule{
		{Pattern: "gh *", Message: "gh is disabled; use the GitHub MCP tools."},
	}

	var out, errBuf bytes.Buffer
	code := runExecCore(context.Background(), cfg, "gh pr view 42", &out, &errBuf)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	want := "gh is disabled; use the GitHub MCP tools.\n"
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

func TestResolveExecConfig_PolicyFileIgnoresConfig(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	snap := &config.Config{}
	snap.Sandbox.Command.Allow = []string{"echo *"}
	path, cleanup, err := policysnapshot.Write(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// configPath points at a nonexistent file; if it were read, this errors.
	cfg, err := resolveExecConfig(path, "/nonexistent/agent-sandbox.toml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Sandbox.Command.Allow) != 1 || cfg.Sandbox.Command.Allow[0] != "echo *" {
		t.Errorf("allow = %v, want [echo *] from snapshot", cfg.Sandbox.Command.Allow)
	}
}

func TestResolveExecConfig_MissingPolicyFile_FailsClosed(t *testing.T) {
	if _, err := resolveExecConfig("/nonexistent/policy.json", "agent-sandbox.toml"); err == nil {
		t.Fatal("expected fail-closed error for missing snapshot, got nil")
	}
}

func TestResolveExecConfig_FallsBackToConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	body := "tool_mode=\"hook\"\n[sandbox.container]\nbuild_context=\"./x\"\ndockerfile=\"Dockerfile\"\nimage=\"img:1\"\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveExecConfig("", cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ToolMode != "hook" {
		t.Errorf("tool_mode = %q, want hook (loaded from config)", cfg.ToolMode)
	}
}
