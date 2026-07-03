package claude

import (
	"encoding/json"
	"fmt"
)

const hookCommand = "agent-sandbox hook"

// hookSettingsJSON returns a compact Claude Code settings JSON string that
// registers the PreToolUse hook for the Bash and Monitor tools, routing each
// command through `agent-sandbox hook`. It is injected via `claude --settings`
// in hook mode, so no persisted .claude/settings.json entry is required.
func hookSettingsJSON() (string, error) {
	entry := func(matcher string) map[string]any {
		return map[string]any{
			"matcher": matcher,
			"hooks": []any{
				map[string]any{"type": "command", "command": hookCommand},
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
