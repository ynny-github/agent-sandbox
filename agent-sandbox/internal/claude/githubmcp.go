package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

// GithubMCPTokenEnv is the environment variable that both enables the GitHub MCP
// server (by being non-empty) and supplies its token. It is mapped to the
// github-mcp-server's expected GITHUB_PERSONAL_ACCESS_TOKEN in the generated
// config. Supply it via --env or the ambient host environment.
const GithubMCPTokenEnv = "GITHUB_MCP_TOKEN"

const (
	githubMCPImage    = "ghcr.io/github/github-mcp-server"
	githubMCPToolsets = "pull_requests,issues,repos,projects"
)

// GithubMCPEnabled reports whether the GitHub MCP server should be configured:
// true iff GITHUB_MCP_TOKEN holds a non-empty (trimmed) value.
func GithubMCPEnabled() bool {
	return strings.TrimSpace(os.Getenv(GithubMCPTokenEnv)) != ""
}

// githubMCPWriteDenyRules are the mutating tools of the github MCP `repos`
// toolset, blocked via Claude's permissions.deny so the agent cannot write to
// GitHub through the API and bypass the sandbox's routed local git. The read and
// search tools of `repos` (and the other toolsets) remain available. Tools are
// named mcp__<server>__<tool>; the generated config registers the server as
// "github".
var githubMCPWriteDenyRules = []string{
	"mcp__github__create_or_update_file",
	"mcp__github__delete_file",
	"mcp__github__push_files",
	"mcp__github__create_branch",
	"mcp__github__create_repository",
	"mcp__github__fork_repository",
}

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

// writeGithubMCPConfig reads the token from GITHUB_MCP_TOKEN, renders the GitHub
// MCP config JSON, and writes it to a 0600 temp file. It returns the absolute
// path and a cleanup func. Callers invoke it only when GithubMCPEnabled() is
// true; it still errors defensively if the token is empty. The *config.Config
// param is retained for the launcher's dependency signature but unused.
func writeGithubMCPConfig(_ *config.Config) (string, func(), error) {
	token := strings.TrimSpace(os.Getenv(GithubMCPTokenEnv))
	if token == "" {
		return "", nil, fmt.Errorf("github mcp: %s is not set", GithubMCPTokenEnv)
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
