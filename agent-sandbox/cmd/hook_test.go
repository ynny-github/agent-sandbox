package cmd

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

func TestRunHookCore_EmptyCommand_EmitsNothing(t *testing.T) {
	in := strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":""}}`)
	var out bytes.Buffer
	if err := runHookCore(in, &out); err != nil {
		t.Fatalf("runHookCore error: %v", err)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("output = %q, want empty (defensive passthrough)", out.String())
	}
}
