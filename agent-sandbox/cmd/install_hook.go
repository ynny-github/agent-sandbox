package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const hookCommand = "agent-sandbox hook"

var installHookCmd = &cobra.Command{
	Use:   "install-hook",
	Short: "Install the PreToolUse hook into .claude/settings.json",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
		return runInstallHookIn(wd, cmd.OutOrStdout())
	},
}

func init() {
	rootCmd.AddCommand(installHookCmd)
}

func runInstallHookIn(dir string, out io.Writer) error {
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return fmt.Errorf("create .claude dir: %w", err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")

	settings, err := readSettings(settingsPath)
	if err != nil {
		return err
	}

	changed := false
	for _, matcher := range []string{"Bash", "Monitor"} {
		if ensurePreToolUseHook(settings, matcher, hookCommand) {
			changed = true
		}
	}

	if !changed {
		fmt.Fprintln(out, "PreToolUse hook already installed")
		return nil
	}
	if err := writeSettings(settingsPath, settings); err != nil {
		return err
	}
	fmt.Fprintln(out, "installed PreToolUse hook into .claude/settings.json")
	return nil
}

func readSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("read settings: %w", err)
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parse settings: %w", err)
	}
	return settings, nil
}

func writeSettings(path string, settings map[string]any) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
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
