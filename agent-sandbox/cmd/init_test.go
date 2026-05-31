package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInitIn_WritesDoc(t *testing.T) {
	dir := t.TempDir()
	if err := runInitIn(dir, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInitIn: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".claude", "rules", "agent-sandbox.md"))
	if err != nil {
		t.Fatalf(".claude/rules/agent-sandbox.md not created: %v", err)
	}
	for _, want := range []string{"Command Router", "run_command"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("doc missing %q, got:\n%s", want, data)
		}
	}
}

func TestRunInitIn_UpdatesGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := runInitIn(dir, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInitIn: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf(".gitignore not created: %v", err)
	}
	if !strings.Contains(string(data), ".claude/rules/agent-sandbox.md") {
		t.Errorf(".gitignore missing entry, got:\n%s", data)
	}
}

func TestRunInitIn_Idempotent(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		if err := runInitIn(dir, &bytes.Buffer{}); err != nil {
			t.Fatalf("run %d: runInitIn: %v", i+1, err)
		}
	}
	gi, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if count := strings.Count(string(gi), ".claude/rules/agent-sandbox.md"); count != 1 {
		t.Errorf(".gitignore: expected 1 entry, got %d\n%s", count, gi)
	}
}

func TestRunInitIn_PreservesExistingGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("tmp/\nnode_modules/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runInitIn(dir, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInitIn: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(data), "tmp/") {
		t.Error(".gitignore lost existing entries")
	}
	if !strings.Contains(string(data), ".claude/rules/agent-sandbox.md") {
		t.Error(".gitignore missing new entry")
	}
}

func TestRunInitIn_PrintsOutput(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := runInitIn(dir, &buf); err != nil {
		t.Fatalf("runInitIn: %v", err)
	}
	if !strings.Contains(buf.String(), ".claude/rules/agent-sandbox.md") {
		t.Errorf("output missing file name, got: %q", buf.String())
	}
}
