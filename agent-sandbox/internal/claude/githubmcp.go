package claude

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/secret"
)

const (
	githubMCPImage    = "ghcr.io/github/github-mcp-server"
	githubMCPToolsets = "pull_requests,issues,repos"
)

// githubMCPConfigJSON renders the single-server MCP config for the GitHub MCP
// server, with the token embedded in the server env. The docker args reference
// the env by name (-e NAME passthrough), so the token stays out of argv.
func githubMCPConfigJSON(token string) ([]byte, error) {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"github": map[string]any{
				"command": "docker",
				"args": []string{
					"run", "-i", "--rm",
					"-e", "GITHUB_PERSONAL_ACCESS_TOKEN",
					"-e", "GITHUB_TOOLSETS",
					githubMCPImage,
				},
				"env": map[string]any{
					"GITHUB_PERSONAL_ACCESS_TOKEN": token,
					"GITHUB_TOOLSETS":              githubMCPToolsets,
				},
			},
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("github mcp config: encode: %w", err)
	}
	return data, nil
}

// writeGithubMCPConfig loads the token from cfg's secret reference, renders the
// GitHub MCP config JSON, and writes it to a 0600 temp file. It returns the
// absolute temp-file path and a cleanup func that removes it; callers must call
// cleanup when Claude exits. It assumes cfg.Claude.GithubMCP.Enabled is true.
func writeGithubMCPConfig(cfg *config.Config) (string, func(), error) {
	src, err := secret.Resolve(cfg.Claude.GithubMCP.Secret)
	if err != nil {
		return "", nil, err
	}
	token, err := src.Load()
	if err != nil {
		return "", nil, err
	}
	data, err := githubMCPConfigJSON(token)
	if err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp("", "agent-sandbox-mcp-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("github mcp config: temp file: %w", err)
	}
	path := f.Name()
	cleanup := func() { os.Remove(path) }
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		cleanup()
		return "", nil, fmt.Errorf("github mcp config: chmod: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		cleanup()
		return "", nil, fmt.Errorf("github mcp config: write: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("github mcp config: close: %w", err)
	}
	return path, cleanup, nil
}

// RedactedGithubMCPConfigJSON renders the GitHub MCP config the launcher would
// generate, with the token replaced by a placeholder. It is for display (e.g.
// `agent-sandbox debug`) so the real PAT never reaches the terminal or logs.
func RedactedGithubMCPConfigJSON() ([]byte, error) {
	return githubMCPConfigJSON("***redacted***")
}
