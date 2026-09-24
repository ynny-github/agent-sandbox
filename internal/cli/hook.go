package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/internal/shellquote"
)

var hookCmd = &cobra.Command{
	Use:   "hook",
	Short: "PreToolUse adapter: rewrite Bash/Monitor commands to route through the sandbox",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		os.Exit(runHook(os.Stdin, os.Stdout, os.Stderr))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(hookCmd)
}

type hookInput struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

// runHook adapts runHookCore to a process exit code, and every failure it can
// have is a 2.
//
// Claude Code blocks a tool call only on exit 2, feeding the hook's stderr back
// to the agent as the reason. Every other non-zero exit is a *non-blocking*
// error: the message reaches the user, and then the original tool call runs
// anyway. For this hook that is not a degraded mode, it is a bypass — the
// command runs unwrapped, in the agent's own sandbox, under none of the command
// profile's limits. So there is no failure here worth reporting with any other
// code.
func runHook(in io.Reader, out, errOut io.Writer) int {
	if err := runHookCore(in, out); err != nil {
		fmt.Fprintf(errOut, "agent-sandbox hook: %v\n", err)
		return 2
	}
	return 0
}

// runHookCore reads a PreToolUse hook payload from in and writes a hook
// response to out that rewrites the command to run through `agent-sandbox
// exec`. A payload it cannot rewrite is an error, never a pass-through: this
// hook is registered for Bash and Monitor only, both of which exist to run a
// command, so a payload with none is either a shape this code does not know or
// a call that must not proceed. Letting it through would run the command in the
// agent's own sandbox instead of execd's.
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
		name := input.ToolName
		if name == "" {
			name = "(unnamed tool)"
		}
		return fmt.Errorf("no command found in the %s payload, so it cannot be routed "+
			"through execd; refusing rather than letting it run outside execd", name)
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
