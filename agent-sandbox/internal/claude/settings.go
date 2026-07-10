package claude

import (
	"encoding/json"
	"fmt"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/shellquote"
)

const hookCommand = "agent-sandbox hook"

// hookSettingsJSON returns a compact Claude Code settings JSON string that
// registers the PreToolUse hook for the Bash and Monitor tools, routing each
// command through `agent-sandbox hook`. It is injected via `claude --settings`
// in hook mode, so no persisted .claude/settings.json entry is required. When
// policyFile is non-empty, the hook command is given `--policy-file` so it
// routes from the frozen snapshot rather than the mutable config on disk.
func hookSettingsJSON(policyFile string) (string, error) {
	command := hookCommand
	if policyFile != "" {
		command += " --policy-file " + shellquote.Quote(policyFile)
	}
	entry := func(matcher string) map[string]any {
		return map[string]any{
			"matcher": matcher,
			"hooks": []any{
				map[string]any{"type": "command", "command": command},
			},
		}
	}
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{entry("Bash"), entry("Monitor")},
		},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("encode hook settings: %w", err)
	}
	return string(data), nil
}
