package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

func TestRedactedGithubMCPConfigJSON_RedactsToken(t *testing.T) {
	data, err := RedactedGithubMCPConfigJSON()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if strings.Contains(string(data), "ghp_") {
		t.Errorf("redacted config must not contain a token; got %s", data)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	env := m["mcpServers"].(map[string]any)["github"].(map[string]any)["env"].(map[string]any)
	if env["GITHUB_PERSONAL_ACCESS_TOKEN"] != "***redacted***" {
		t.Errorf("token = %v, want ***redacted***", env["GITHUB_PERSONAL_ACCESS_TOKEN"])
	}
	if env["GITHUB_TOOLSETS"] != "pull_requests,issues,repos,projects" {
		t.Errorf("toolsets = %v", env["GITHUB_TOOLSETS"])
	}
}

func TestGithubMCPConfigJSON_EmbedsToken(t *testing.T) {
	data, err := githubMCPConfigJSON("ghp_tok")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	srv := m["mcpServers"].(map[string]any)["github"].(map[string]any)
	if srv["command"] != "docker" {
		t.Errorf("command = %v, want docker", srv["command"])
	}
	env := srv["env"].(map[string]any)
	if env["GITHUB_PERSONAL_ACCESS_TOKEN"] != "ghp_tok" {
		t.Errorf("token = %v, want ghp_tok", env["GITHUB_PERSONAL_ACCESS_TOKEN"])
	}
	if env["GITHUB_TOOLSETS"] != "pull_requests,issues,repos,projects" {
		t.Errorf("toolsets = %v", env["GITHUB_TOOLSETS"])
	}
	args := srv["args"].([]any)
	last := args[len(args)-1]
	if last != "ghcr.io/github/github-mcp-server" {
		t.Errorf("image = %v, want ghcr.io/github/github-mcp-server", last)
	}
}

func TestWriteGithubMCPConfig_WritesTempAndCleans(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(secretPath, []byte("ghp_from_file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Claude.GithubMCP.Enabled = true
	cfg.Claude.GithubMCP.Secret = secretPath

	path, cleanup, err := writeGithubMCPConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("temp file not written: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if !json.Valid(data) {
		t.Fatalf("temp file is not valid JSON")
	}
	var m map[string]any
	json.Unmarshal(data, &m)
	env := m["mcpServers"].(map[string]any)["github"].(map[string]any)["env"].(map[string]any)
	if env["GITHUB_PERSONAL_ACCESS_TOKEN"] != "ghp_from_file" {
		t.Errorf("token in file = %v, want ghp_from_file", env["GITHUB_PERSONAL_ACCESS_TOKEN"])
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup did not remove the temp file")
	}
}

func TestWriteGithubMCPConfig_SecretMissing(t *testing.T) {
	cfg := &config.Config{}
	cfg.Claude.GithubMCP.Enabled = true
	cfg.Claude.GithubMCP.Secret = filepath.Join(t.TempDir(), "nope")
	if _, _, err := writeGithubMCPConfig(cfg); err == nil {
		t.Fatal("expected error for missing secret file, got nil")
	}
}
