package claude

import (
	"encoding/json"
	"fmt"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/shellquote"
)

const hookCommand = "agent-sandbox hook"

// denyReadRule returns a Claude Code permission deny rule blocking the Read tool
// (and, best-effort, Grep/Glob/Edit) on absPath. absPath must be absolute;
// Claude Code's "//" prefix anchors the pattern at the filesystem root.
func denyReadRule(absPath string) string {
	return "Read(/" + absPath + ")" // absPath starts with "/", yielding "Read(//…)"
}

// settingsJSON builds the compact Claude Code settings JSON injected via
// `claude --settings`. In hook mode it registers the PreToolUse hook for Bash
// and Monitor (routing through `agent-sandbox hook`, adding --policy-file when
// policyFile is non-empty). denyRules (capability-derived permission deny
// rules) are merged into permissions.deny. When mcpConfigPath is non-empty
// (the github MCP is active) it also adds the deny rule that blocks reading the
// generated MCP config file (it embeds the token) plus the github repos
// write-tool deny rules. It returns "" when nothing applies, signaling the
// caller to inject no --settings flag.
func settingsJSON(policyFile, mcpConfigPath string, hookMode bool, denyRules []string) (string, error) {
	settings := map[string]any{}
	if hookMode {
		command := hookCommand
		if policyFile != "" {
			command += " --policy-file " + shellquote.Quote(policyFile)
		}
		entry := func(matcher string) map[string]any {
			return map[string]any{
				"matcher": matcher,
				"hooks":   []any{map[string]any{"type": "command", "command": command}},
			}
		}
		settings["hooks"] = map[string]any{
			"PreToolUse": []any{entry("Bash"), entry("Monitor")},
		}
	}

	deny := append([]string{}, denyRules...)
	if mcpConfigPath != "" {
		deny = append(deny, denyReadRule(mcpConfigPath))
		// The github MCP is active: block its repos write tools so the agent
		// cannot mutate GitHub via the API, bypassing routed local git.
		deny = append(deny, githubMCPWriteDenyRules...)
	}
	if len(deny) > 0 {
		settings["permissions"] = map[string]any{"deny": deny}
	}

	if len(settings) == 0 {
		return "", nil
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("encode settings: %w", err)
	}
	return string(data), nil
}
