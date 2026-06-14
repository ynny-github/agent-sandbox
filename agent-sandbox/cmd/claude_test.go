// agent-sandbox/cmd/claude_test.go
package cmd

import (
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
