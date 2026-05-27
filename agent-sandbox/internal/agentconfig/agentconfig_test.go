package agentconfig_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/agentconfig"
)

func TestPrint_KnownFormats_OutputsSnippet(t *testing.T) {
	for _, format := range []string{"claude", "agents", "gemini"} {
		var buf bytes.Buffer
		if err := agentconfig.Print(format, &buf); err != nil {
			t.Errorf("format=%q: unexpected error: %v", format, err)
			continue
		}
		got := buf.String()
		if !strings.Contains(got, "run_command") {
			t.Errorf("format=%q: output missing 'run_command', got: %q", format, got)
		}
		if !strings.Contains(got, "Command Router") {
			t.Errorf("format=%q: output missing 'Command Router', got: %q", format, got)
		}
	}
}

func TestPrint_UnknownFormat_ReturnsError(t *testing.T) {
	var buf bytes.Buffer
	err := agentconfig.Print("vscode", &buf)
	if err == nil {
		t.Fatal("expected error for unknown format, got nil")
	}
	if !strings.Contains(err.Error(), "vscode") {
		t.Errorf("error message should mention the unknown format, got: %v", err)
	}
	for _, f := range []string{"claude", "agents", "gemini"} {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("error message should list %q as supported format, got: %v", f, err)
		}
	}
}

func TestPrint_DefaultContent_ContainsKeyLines(t *testing.T) {
	var buf bytes.Buffer
	if err := agentconfig.Print("claude", &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := buf.String()
	for _, want := range []string{
		"native shell commands",
		"run_command",
		"host",
		"container",
		"stdout/stderr",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}
}
