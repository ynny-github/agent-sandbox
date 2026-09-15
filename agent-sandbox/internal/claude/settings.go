package claude

import (
	"encoding/json"
	"fmt"
)

const hookCommand = "agent-sandbox hook"

// settingsJSON builds the compact Claude Code settings JSON injected via
// `claude --settings`. In hook mode it registers the PreToolUse hook for Bash
// and Monitor, routing through `agent-sandbox hook`. It returns "" when
// nothing applies — mcp mode injects no hook and there is nothing else to
// carry — signaling the caller to inject no --settings flag.
func settingsJSON(hookMode bool) (string, error) {
	settings := map[string]any{}
	if hookMode {
		entry := func(matcher string) map[string]any {
			return map[string]any{
				"matcher": matcher,
				"hooks":   []any{map[string]any{"type": "command", "command": hookCommand}},
			}
		}
		settings["hooks"] = map[string]any{
			"PreToolUse": []any{entry("Bash"), entry("Monitor")},
		}
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
