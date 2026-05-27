// agent-sandbox/cmd/claude_test.go
package cmd

import (
	"testing"
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
