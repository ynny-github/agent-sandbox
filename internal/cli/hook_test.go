package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
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

func TestRunHookCore_WrapsBashCommand(t *testing.T) {
	in := strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"git status"}}`)
	var out bytes.Buffer
	if err := runHookCore(in, &out); err != nil {
		t.Fatalf("runHookCore error: %v", err)
	}
	parsed := decodeHookOutput(t, out.Bytes())

	hso := parsed["hookSpecificOutput"].(map[string]any)
	if hso["hookEventName"] != "PreToolUse" {
		t.Errorf("hookEventName = %v, want PreToolUse", hso["hookEventName"])
	}
	if hso["permissionDecision"] != "allow" {
		t.Errorf("permissionDecision = %v, want allow", hso["permissionDecision"])
	}
	if got, want := updatedCommand(t, parsed), `agent-sandbox exec -- 'git status'`; got != want {
		t.Errorf("updatedInput.command = %q, want %q", got, want)
	}
}

func TestRunHookCore_EscapesEmbeddedQuotes(t *testing.T) {
	in := strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"git commit -m 'hi there'"}}`)
	var out bytes.Buffer
	if err := runHookCore(in, &out); err != nil {
		t.Fatalf("runHookCore error: %v", err)
	}
	parsed := decodeHookOutput(t, out.Bytes())
	want := `agent-sandbox exec -- 'git commit -m '\''hi there'\'''`
	if got := updatedCommand(t, parsed); got != want {
		t.Errorf("updatedInput.command = %q, want %q", got, want)
	}
}

// A Bash or Monitor call this hook cannot rewrite must not proceed: the hook
// exists to route every command through execd, and letting an unrewritten one
// through runs it in the agent's own sandbox instead.
func TestRunHookCore_NoCommand_IsAnError(t *testing.T) {
	for _, payload := range []string{
		`{"tool_name":"Bash","tool_input":{"command":""}}`,
		`{"tool_name":"Monitor","tool_input":{}}`,
	} {
		var out bytes.Buffer
		err := runHookCore(strings.NewReader(payload), &out)
		if err == nil {
			t.Fatalf("runHookCore(%s) error = nil, want an error", payload)
		}
		if strings.TrimSpace(out.String()) != "" {
			t.Errorf("a refused payload must emit no decision; got %q", out.String())
		}
	}
}

// Claude Code blocks a tool call only on exit 2. Every other non-zero exit is a
// non-blocking error, after which the ORIGINAL command runs — unwrapped, in the
// agent's own sandbox. So every failure in this hook has to be a 2.
func TestRunHook_FailsClosedWithExitCode2(t *testing.T) {
	for name, payload := range map[string]string{
		"unparseable": `not json at all`,
		"no command":  `{"tool_name":"Bash","tool_input":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := runHook(strings.NewReader(payload), &out, &errOut); code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if errOut.Len() == 0 {
				t.Error("a refusal must say why on stderr; Claude Code feeds it back to the agent")
			}
		})
	}
}

func TestRunHook_SucceedsWithExitCode0(t *testing.T) {
	var out, errOut bytes.Buffer
	in := strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"git status"}}`)
	if code := runHook(in, &out, &errOut); code != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "agent-sandbox exec --") {
		t.Errorf("expected the rewritten command; got %q", out.String())
	}
}
