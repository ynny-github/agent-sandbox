package claude

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
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

func TestGithubMCPEnabled(t *testing.T) {
	t.Setenv("GITHUB_MCP_TOKEN", "")
	if GithubMCPEnabled() {
		t.Error("want disabled when GITHUB_MCP_TOKEN empty")
	}
	t.Setenv("GITHUB_MCP_TOKEN", "  ")
	if GithubMCPEnabled() {
		t.Error("want disabled when GITHUB_MCP_TOKEN is whitespace")
	}
	t.Setenv("GITHUB_MCP_TOKEN", "ghp_x")
	if !GithubMCPEnabled() {
		t.Error("want enabled when GITHUB_MCP_TOKEN set")
	}
}

func TestWriteGithubMCPConfig_UsesEnvToken(t *testing.T) {
	t.Setenv("GITHUB_MCP_TOKEN", "ghp_from_env")
	path, cleanup, err := writeGithubMCPConfig(nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	defer cleanup()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("temp file not written: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	env := m["mcpServers"].(map[string]any)["github"].(map[string]any)["env"].(map[string]any)
	if env["GITHUB_PERSONAL_ACCESS_TOKEN"] != "ghp_from_env" {
		t.Errorf("token = %v, want ghp_from_env", env["GITHUB_PERSONAL_ACCESS_TOKEN"])
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup did not remove the temp file")
	}
}

func TestWriteGithubMCPConfig_MissingTokenErrors(t *testing.T) {
	t.Setenv("GITHUB_MCP_TOKEN", "")
	if _, _, err := writeGithubMCPConfig(nil); err == nil {
		t.Fatal("expected error when GITHUB_MCP_TOKEN unset, got nil")
	}
}
