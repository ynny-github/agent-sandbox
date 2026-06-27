package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/shellquote"
)

var hookCmd = &cobra.Command{
	Use:   "hook",
	Short: "PreToolUse adapter: rewrite Bash/Monitor commands to route through the sandbox",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookCore(os.Stdin, os.Stdout)
	},
}

func init() {
	rootCmd.AddCommand(hookCmd)
}

type hookInput struct {
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

// runHookCore reads a PreToolUse hook payload from in and, if it carries a
// command, writes a hook response to out that rewrites the command to run
// through `agent-sandbox exec`. If there is no command, it writes nothing so
// the tool call proceeds unchanged.
func runHookCore(in io.Reader, out io.Writer) error {
	data, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("read hook input: %w", err)
	}
	var input hookInput
	if err := json.Unmarshal(data, &input); err != nil {
		return fmt.Errorf("parse hook input: %w", err)
	}
	if input.ToolInput.Command == "" {
		return nil
	}

	wrapped := "agent-sandbox exec -- " + shellquote.Quote(input.ToolInput.Command)
	response := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":      "PreToolUse",
			"permissionDecision": "allow",
			"updatedInput": map[string]any{
				"command": wrapped,
			},
		},
	}
	return json.NewEncoder(out).Encode(response)
}
