package execd_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/internal/execd"
)

func decodeHookOutput(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("output is not valid JSON: %v (raw=%q)", err, string(raw))
	}
	return out
}

func updatedCommand(t *testing.T, out map[string]any) string {
	t.Helper()
	hso, ok := out["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("missing hookSpecificOutput in %v", out)
	}
	ui, ok := hso["updatedInput"].(map[string]any)
	if !ok {
		t.Fatalf("missing updatedInput in %v", hso)
	}
	cmd, _ := ui["command"].(string)
	return cmd
}

func TestClaudeHook_WrapsBashCommand(t *testing.T) {
	in := strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"git status"}}`)
	var out bytes.Buffer
	if err := execd.ClaudeHook(in, &out); err != nil {
		t.Fatalf("ClaudeHook error: %v", err)
	}
	parsed := decodeHookOutput(t, out.Bytes())

	hso := parsed["hookSpecificOutput"].(map[string]any)
	if hso["hookEventName"] != "PreToolUse" {
		t.Errorf("hookEventName = %v, want PreToolUse", hso["hookEventName"])
	}
	if hso["permissionDecision"] != "allow" {
		t.Errorf("permissionDecision = %v, want allow", hso["permissionDecision"])
	}
	if got, want := updatedCommand(t, parsed), `execd exec -- 'git status'`; got != want {
		t.Errorf("updatedInput.command = %q, want %q", got, want)
	}
}

func TestClaudeHook_EscapesEmbeddedQuotes(t *testing.T) {
	in := strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"git commit -m 'hi there'"}}`)
	var out bytes.Buffer
	if err := execd.ClaudeHook(in, &out); err != nil {
		t.Fatalf("ClaudeHook error: %v", err)
	}
	parsed := decodeHookOutput(t, out.Bytes())
	want := `execd exec -- 'git commit -m '\''hi there'\'''`
	if got := updatedCommand(t, parsed); got != want {
		t.Errorf("updatedInput.command = %q, want %q", got, want)
	}
}

// A Bash or Monitor call this hook cannot rewrite must not proceed: the hook
// exists to route every command through execd, and letting an unrewritten one
// through runs it in the agent's own sandbox instead.
func TestClaudeHook_NoCommand_IsAnError(t *testing.T) {
	for _, payload := range []string{
		`{"tool_name":"Bash","tool_input":{"command":""}}`,
		`{"tool_name":"Monitor","tool_input":{}}`,
	} {
		var out bytes.Buffer
		err := execd.ClaudeHook(strings.NewReader(payload), &out)
		if err == nil {
			t.Fatalf("execd.ClaudeHook(%s) error = nil, want an error", payload)
		}
		if strings.TrimSpace(out.String()) != "" {
			t.Errorf("a refused payload must emit no decision; got %q", out.String())
		}
	}
}
