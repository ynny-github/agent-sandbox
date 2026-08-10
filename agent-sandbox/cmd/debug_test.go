package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/claude"
)

func TestRunDebug_MissingConfig(t *testing.T) {
	orig := configPath
	configPath = "/nonexistent/path.toml"
	t.Cleanup(func() { configPath = orig })
	if err := runDebug(debugCmd, nil); err == nil {
		t.Fatal("expected config error, got nil")
	}
}

// debug must print the same invocation the launcher builds, including the
// broker socket grant — otherwise it misrepresents the wrap command in exactly
// the place a user looks when brokered commands fail.
func TestRunDebug_PrintsBrokerSocketGrant(t *testing.T) {
	dir := t.TempDir()
	nono := filepath.Join(dir, "nono")
	if err := os.WriteFile(nono, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake nono: %v", err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))

	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	cfgBody := "[mcp]\ncommand_output_dir = " + toTOMLString(filepath.Join(dir, "out")) + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	orig := configPath
	configPath = cfgPath
	t.Cleanup(func() { configPath = orig })

	wantSocket, err := claude.BrokerSocketPath()
	if err != nil {
		t.Fatalf("BrokerSocketPath() error = %v", err)
	}

	out := captureStdout(t, func() {
		if err := runDebug(debugCmd, nil); err != nil {
			t.Fatalf("runDebug() error = %v", err)
		}
	})
	want := "--allow-unix-socket " + wantSocket
	if !strings.Contains(out, want) {
		t.Errorf("debug output missing %q; got:\n%s", want, out)
	}
}

func toTOMLString(s string) string { return `"` + s + `"` }

// captureStdout runs fn with os.Stdout replaced by a pipe and returns what it
// wrote. runDebug prints with fmt.Println, so there is no injectable writer.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		r.Close()
		done <- string(b)
	}()
	// The inner closure's defer also runs when fn calls t.Fatal (runtime.Goexit),
	// so os.Stdout is always restored.
	func() {
		defer func() {
			os.Stdout = orig
			w.Close()
		}()
		fn()
	}()
	return <-done
}

func TestFormatGeneratedConfigs_ProfileAndEnabledMCP(t *testing.T) {
	profile := []byte(`{"extends":"claude"}`)
	mcp := []byte(`{"mcpServers":{"github":{"env":{"GITHUB_PERSONAL_ACCESS_TOKEN":"***redacted***"}}}}`)
	out := formatGeneratedConfigs("/tmp/p.json", profile, true, mcp)
	for _, want := range []string{
		"# generated nono profile (/tmp/p.json):",
		`"extends": "claude"`, // pretty-printed (indented)
		"# github mcp config (enabled; token redacted):",
		"***redacted***",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

func TestFormatGeneratedConfigs_MCPDisabledLabel(t *testing.T) {
	out := formatGeneratedConfigs("/tmp/p.json", []byte(`{"a":1}`), false, []byte(`{"b":2}`))
	if !strings.Contains(out, "# github mcp config (disabled; token redacted):") {
		t.Errorf("expected disabled label; got:\n%s", out)
	}
}
