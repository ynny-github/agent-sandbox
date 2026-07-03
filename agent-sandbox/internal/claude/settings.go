// Package claude generates Claude Code-facing configuration that agent-sandbox
// hands to the `claude` CLI at launch.
package claude

import (
	"encoding/json"
	"fmt"
)

const hookCommand = "agent-sandbox hook"

// HookSettingsJSON returns a compact Claude Code settings JSON string that
// registers the PreToolUse hook for the Bash and Monitor tools, routing each
// command through `agent-sandbox hook`. It is injected via `claude --settings`
// in hook mode, so no persisted .claude/settings.json entry is required.
func HookSettingsJSON() (string, error) {
	settings := map[string]any{}
	for _, matcher := range []string{"Bash", "Monitor"} {
		ensurePreToolUseHook(settings, matcher, hookCommand)
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("encode hook settings: %w", err)
	}
	return string(data), nil
}

// ensurePreToolUseHook adds a PreToolUse entry for matcher running command if
// one is not already present. Returns true if it added an entry.
func ensurePreToolUseHook(settings map[string]any, matcher, command string) bool {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
		settings["hooks"] = hooks
	}
	preToolUse, _ := hooks["PreToolUse"].([]any)

	for _, entry := range preToolUse {
		e, ok := entry.(map[string]any)
		if !ok || e["matcher"] != matcher {
			continue
		}
		inner, _ := e["hooks"].([]any)
		for _, h := range inner {
			if hm, ok := h.(map[string]any); ok && hm["command"] == command {
				return false
			}
		}
	}

	preToolUse = append(preToolUse, map[string]any{
		"matcher": matcher,
		"hooks": []any{
			map[string]any{"type": "command", "command": command},
		},
	})
	hooks["PreToolUse"] = preToolUse
	return true
}
