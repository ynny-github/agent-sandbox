// agent-sandbox/cmd/claude_test.go
package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

func TestValidateClaudePassthrough_SettingsBlocked(t *testing.T) {
	err := validateClaudePassthrough([]string{"--settings", "foo.json"})
	if err == nil {
		t.Fatal("expected error for --settings, got nil")
	}
}

func TestValidateClaudePassthrough_SettingsEqualBlocked(t *testing.T) {
	err := validateClaudePassthrough([]string{"--settings=foo.json"})
	if err == nil {
		t.Fatal("expected error for --settings=..., got nil")
	}
}

func TestValidateClaudePassthrough_AllowsOtherArgs(t *testing.T) {
	err := validateClaudePassthrough([]string{"--print", "--model", "opus"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateClaudePassthrough_Empty(t *testing.T) {
	err := validateClaudePassthrough(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildNonoArgs_MissingProfile(t *testing.T) {
	cfg := &config.Config{}
	_, _, err := buildNonoArgs(cfg)
	if err == nil {
		t.Fatal("expected error when profile is empty, got nil")
	}
}

func TestBuildNonoArgs_NonoNotInPath(t *testing.T) {
	t.Setenv("PATH", "")
	cfg := &config.Config{Nono: config.NonoConfig{Profile: "test-profile"}}
	_, _, err := buildNonoArgs(cfg)
	if err == nil {
		t.Fatal("expected error when nono not in PATH, got nil")
	}
}

func makeFakeNono(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "nono")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return path
}

func TestBuildNonoArgs_DefaultSubcommandIsRun(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{Nono: config.NonoConfig{Profile: "test-profile"}}
	_, args, err := buildNonoArgs(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(args) < 2 {
		t.Fatalf("expected at least 2 args, got %v", args)
	}
	if args[1] != "run" {
		t.Errorf("args[1] = %q, want \"run\"; full args: %v", args[1], args)
	}
}

func TestBuildNonoArgs_SubcommandWrap(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{Nono: config.NonoConfig{Profile: "test-profile", Subcommand: "wrap"}}
	_, args, err := buildNonoArgs(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(args) < 2 {
		t.Fatalf("expected at least 2 args, got %v", args)
	}
	if args[1] != "wrap" {
		t.Errorf("args[1] = %q, want \"wrap\"; full args: %v", args[1], args)
	}
}

func TestBuildNonoArgs_SubcommandRunExplicit(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{Nono: config.NonoConfig{Profile: "test-profile", Subcommand: "run"}}
	_, args, err := buildNonoArgs(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(args) < 2 {
		t.Fatalf("expected at least 2 args, got %v", args)
	}
	if args[1] != "run" {
		t.Errorf("args[1] = %q, want \"run\"; full args: %v", args[1], args)
	}
}

func TestRunDebug_MissingConfig(t *testing.T) {
	orig := configPath
	configPath = "/nonexistent/path.toml"
	t.Cleanup(func() { configPath = orig })
	err := runDebug(debugCmd, nil)
	if err == nil {
		t.Fatal("expected config error, got nil")
	}
}
