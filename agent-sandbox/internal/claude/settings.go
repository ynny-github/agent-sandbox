package claude

import (
	"encoding/json"
	"fmt"
)

const hookCommand = "agent-sandbox hook"

// settingsJSON builds the compact Claude Code settings JSON injected via
// `claude --settings`. It registers the PreToolUse hook for Bash and Monitor,
// routing each command through `agent-sandbox hook` and from there to the
// broker. That hook is the whole of what the settings carry; the profiles
// contribute nothing to them.
func settingsJSON() (string, error) {
	entry := func(matcher string) map[string]any {
		return map[string]any{
			"matcher": matcher,
			"hooks":   []any{map[string]any{"type": "command", "command": hookCommand}},
		}
	}
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{entry("Bash"), entry("Monitor")},
		},
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("encode settings: %w", err)
	}
	return string(data), nil
}
