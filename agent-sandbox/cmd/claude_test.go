// agent-sandbox/cmd/claude_test.go
package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestBuildNonoArgs_NonoNotInPath(t *testing.T) {
	t.Setenv("PATH", "")
	cfg := &config.Config{}
	_, _, err := buildNonoArgs(cfg, nil, nil)
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

func TestBuildNonoArgs_AlwaysUsesWrap(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{}
	_, args, err := buildNonoArgs(cfg, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(args) < 2 {
		t.Fatalf("expected at least 2 args, got %v", args)
	}
	if args[0] != "nono" {
		t.Errorf("args[0] = %q, want \"nono\"; full args: %v", args[0], args)
	}
	if args[1] != "wrap" {
		t.Errorf("args[1] = %q, want \"wrap\"; full args: %v", args[1], args)
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

func argsContain(args []string, target string) bool {
	for _, a := range args {
		if a == target {
			return true
		}
	}
	return false
}

func TestBuildNonoArgs_McpMode_DisablesTools(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{ToolMode: "mcp"}

	_, args, err := buildNonoArgs(cfg, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !argsContain(args, "--disallowed-tools") || !argsContain(args, "Bash,Monitor") {
		t.Errorf("mcp mode should disable Bash,Monitor; got %v", args)
	}
}

func TestBuildNonoArgs_HookMode_HookInstalled_NoDisallowedTools(t *testing.T) {
	makeFakeNono(t)
	dir := t.TempDir()
	if err := runInstallHookIn(dir, io.Discard); err != nil {
		t.Fatalf("install hook: %v", err)
	}
	t.Chdir(dir)

	cfg := &config.Config{ToolMode: "hook"}
	_, args, err := buildNonoArgs(cfg, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if argsContain(args, "--disallowed-tools") {
		t.Errorf("hook mode should not disable tools; got %v", args)
	}
}

func TestBuildNonoArgs_HookMode_HookMissing_Errors(t *testing.T) {
	makeFakeNono(t)
	t.Chdir(t.TempDir()) // empty dir, no .claude/settings.json

	cfg := &config.Config{ToolMode: "hook"}
	_, _, err := buildNonoArgs(cfg, nil, nil)
	if err == nil {
		t.Fatal("expected error when hook not installed in hook mode")
	}
	if !strings.Contains(err.Error(), "install-hook") {
		t.Errorf("error should mention install-hook; got %v", err)
	}
}

func argsIndex(args []string, target string) int {
	for i, a := range args {
		if a == target {
			return i
		}
	}
	return -1
}

func TestBuildNonoArgs_NonoOptsBeforeClaude(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{ToolMode: "mcp"}
	_, args, err := buildNonoArgs(cfg, []string{"--profile", "nono.jsonc"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pi := argsIndex(args, "--profile")
	ci := argsIndex(args, "claude")
	if pi < 0 || ci < 0 || pi > ci {
		t.Errorf("--profile must appear before claude; got %v", args)
	}
	if args[pi+1] != "nono.jsonc" {
		t.Errorf("--profile value misplaced; got %v", args)
	}
}

func TestBuildNonoArgs_ClaudeOptsAfterClaude(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{ToolMode: "mcp"}
	_, args, err := buildNonoArgs(cfg, nil, []string{"--model", "opus"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ci := argsIndex(args, "claude")
	mi := argsIndex(args, "--model")
	if ci < 0 || mi < 0 || mi < ci {
		t.Errorf("--model must appear after claude; got %v", args)
	}
	if args[mi+1] != "opus" {
		t.Errorf("--model value misplaced; got %v", args)
	}
}

func TestParseClaudeArgs_SplitsOnFirstDash(t *testing.T) {
	cfgFile, nonoOpts, claudeOpts := parseClaudeArgs(
		[]string{"--profile", "nono.jsonc", "--", "--model", "opus"})
	if cfgFile != configPath {
		t.Errorf("configFile = %q, want default %q", cfgFile, configPath)
	}
	if strings.Join(nonoOpts, " ") != "--profile nono.jsonc" {
		t.Errorf("nonoOpts = %v", nonoOpts)
	}
	if strings.Join(claudeOpts, " ") != "--model opus" {
		t.Errorf("claudeOpts = %v", claudeOpts)
	}
}

func TestParseClaudeArgs_NoDash_AllNono(t *testing.T) {
	_, nonoOpts, claudeOpts := parseClaudeArgs([]string{"--profile", "nono.jsonc"})
	if strings.Join(nonoOpts, " ") != "--profile nono.jsonc" {
		t.Errorf("nonoOpts = %v", nonoOpts)
	}
	if len(claudeOpts) != 0 {
		t.Errorf("claudeOpts = %v, want empty", claudeOpts)
	}
}

func TestParseClaudeArgs_ConfigSpaceForm(t *testing.T) {
	cfgFile, nonoOpts, _ := parseClaudeArgs(
		[]string{"--config", "custom.toml", "--profile", "p", "--", "--print"})
	if cfgFile != "custom.toml" {
		t.Errorf("configFile = %q, want custom.toml", cfgFile)
	}
	if strings.Join(nonoOpts, " ") != "--profile p" {
		t.Errorf("nonoOpts = %v, want [--profile p]", nonoOpts)
	}
}

func TestParseClaudeArgs_ConfigEqualsForm(t *testing.T) {
	cfgFile, nonoOpts, _ := parseClaudeArgs(
		[]string{"--config=custom.toml", "--allow", "/repo"})
	if cfgFile != "custom.toml" {
		t.Errorf("configFile = %q, want custom.toml", cfgFile)
	}
	if strings.Join(nonoOpts, " ") != "--allow /repo" {
		t.Errorf("nonoOpts = %v, want [--allow /repo]", nonoOpts)
	}
}

func TestParseClaudeArgs_DashAtEnd_EmptyClaudeOpts(t *testing.T) {
	_, nonoOpts, claudeOpts := parseClaudeArgs([]string{"--profile", "p", "--"})
	if strings.Join(nonoOpts, " ") != "--profile p" {
		t.Errorf("nonoOpts = %v", nonoOpts)
	}
	if len(claudeOpts) != 0 {
		t.Errorf("claudeOpts = %v, want empty", claudeOpts)
	}
}

func TestParseClaudeArgs_Empty(t *testing.T) {
	cfgFile, nonoOpts, claudeOpts := parseClaudeArgs(nil)
	if cfgFile != configPath {
		t.Errorf("configFile = %q, want default", cfgFile)
	}
	if len(nonoOpts) != 0 || len(claudeOpts) != 0 {
		t.Errorf("expected empty groups, got nono=%v claude=%v", nonoOpts, claudeOpts)
	}
}
