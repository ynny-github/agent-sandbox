package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/internal/claude"
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
// execd socket grant — otherwise it misrepresents the wrap command in exactly
// the place a user looks when a command run through execd fails.
func TestRunDebug_PrintsExecdSocketGrant(t *testing.T) {
	dir := t.TempDir()
	nono := filepath.Join(dir, "nono")
	if err := os.WriteFile(nono, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake nono: %v", err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))

	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	cfgBody := ""
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// validate requires a command profile on disk; write the default name
	// beside the config so this fixture keeps exercising the default path.
	if err := os.WriteFile(filepath.Join(dir, "command-profile.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write command profile: %v", err)
	}
	orig := configPath
	configPath = cfgPath
	t.Cleanup(func() { configPath = orig })

	wantSocket, err := claude.ExecdSocketPath()
	if err != nil {
		t.Fatalf("ExecdSocketPath() error = %v", err)
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

// The exec daemon line must print the resolved nono binary, not a literal
// "nono": debug exists to show the exact invocation the launcher builds, and
// ExecdArgs now honours whatever path it is given.
func TestRunDebug_PrintsResolvedNonoPath(t *testing.T) {
	dir := t.TempDir()
	nono := filepath.Join(dir, "nono")
	if err := os.WriteFile(nono, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake nono: %v", err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))

	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	cfgBody := ""
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "command-profile.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write command profile: %v", err)
	}
	orig := configPath
	configPath = cfgPath
	t.Cleanup(func() { configPath = orig })

	out := captureStdout(t, func() {
		if err := runDebug(debugCmd, nil); err != nil {
			t.Fatalf("runDebug() error = %v", err)
		}
	})
	if !strings.Contains(out, "exec daemon:\n  "+nono+" run") {
		t.Errorf("debug output missing the resolved nono path %q in the exec daemon line; got:\n%s", nono, out)
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

// debug must print the profile path the launcher will actually pass, so the
// value can be pasted straight into `nono profile show`.
func TestRunDebug_PrintsTheConfiguredAgentProfilePath(t *testing.T) {
	dir := t.TempDir()
	nono := filepath.Join(dir, "nono")
	if err := os.WriteFile(nono, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake nono: %v", err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))

	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	cfgBody := ""
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	for _, name := range []string{"command-profile.json", "claude-profile.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	orig := configPath
	configPath = cfgPath
	t.Cleanup(func() { configPath = orig })

	out := captureStdout(t, func() {
		if err := runDebug(debugCmd, nil); err != nil {
			t.Fatalf("runDebug() error = %v", err)
		}
	})
	want := "--profile " + filepath.Join(dir, "claude-profile.json")
	if !strings.Contains(out, want) {
		t.Errorf("debug output missing %q; got:\n%s", want, out)
	}
}

// debugFixture writes the temp config, command profile and fake nono that
// runDebug needs, points configPath at them, and returns the config's dir.
// Mirrors the setup in TestRunDebug_PrintsExecdSocketGrant.
func debugFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	nono := filepath.Join(dir, "nono")
	if err := os.WriteFile(nono, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake nono: %v", err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	if err := os.WriteFile(filepath.Join(dir, "agent-sandbox.toml"), []byte(""), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "command-profile.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write command profile: %v", err)
	}
	orig := configPath
	configPath = filepath.Join(dir, "agent-sandbox.toml")
	t.Cleanup(func() { configPath = orig })
	return dir
}

// debug prints the invocation a launch would build. --context-mode adds no
// argument to it — it changes the environment — so debug has to print that,
// or two different sessions would render identically.
func TestRunDebug_PrintsContextModeVariable(t *testing.T) {
	debugFixture(t)
	out := captureStdout(t, func() {
		if err := runDebug(debugCmd, []string{"--context-mode", "--"}); err != nil {
			t.Fatalf("runDebug() error = %v", err)
		}
	})
	if !strings.Contains(out, "environment: CONTEXT_MODE_EXEC_BACKEND=execd") {
		t.Errorf("debug output must name the published variable:\n%s", out)
	}
}

func TestRunDebug_OmitsContextModeVariableWithoutTheFlag(t *testing.T) {
	debugFixture(t)
	out := captureStdout(t, func() {
		if err := runDebug(debugCmd, []string{"--"}); err != nil {
			t.Fatalf("runDebug() error = %v", err)
		}
	})
	if strings.Contains(out, "CONTEXT_MODE_EXEC_BACKEND") {
		t.Errorf("debug must not mention the variable without the flag:\n%s", out)
	}
}
