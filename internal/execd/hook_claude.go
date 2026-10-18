package execd

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/ynny-github/agent-sandbox/internal/shellquote"
)

type hookInput struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

// ClaudeHook reads a PreToolUse hook payload from in and writes a hook
// response to out that rewrites the command to run through `agent-sandbox
// exec`. A payload it cannot rewrite is an error, never a pass-through: this
// hook is registered for Bash and Monitor only, both of which exist to run a
// command, so a payload with none is either a shape this code does not know or
// a call that must not proceed. Letting it through would run the command in the
// agent's own sandbox instead of execd's.
func ClaudeHook(in io.Reader, out io.Writer) error {
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

	wrapped := "execd exec -- " + shellquote.Quote(input.ToolInput.Command)
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
