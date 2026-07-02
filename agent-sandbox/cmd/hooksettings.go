package cmd

import (
	"encoding/json"
	"fmt"
)

// buildHookSettingsJSON returns a compact Claude Code settings JSON string that
// registers the PreToolUse hook for the Bash and Monitor tools, routing each
// command through `agent-sandbox hook`. It is injected via `claude --settings`
// in hook mode, so no persisted .claude/settings.json entry is required.
func buildHookSettingsJSON() (string, error) {
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
