package claude

import "testing"

// The two strings are a contract with context-mode's resolveBackendConfig
// (src/exec-backend.ts): an unset or empty variable resolves to the "local"
// backend, which runs agent-authored code in the agent's own sandbox instead
// of under the command profile. A typo here fails nothing and is invisible in
// a session, so the literals are pinned here rather than trusted.
func TestContextModeConstants(t *testing.T) {
	if ContextModeEnvVar != "CONTEXT_MODE_EXEC_BACKEND" {
		t.Errorf("ContextModeEnvVar = %q, want CONTEXT_MODE_EXEC_BACKEND", ContextModeEnvVar)
	}
	if ContextModeExecd != "execd" {
		t.Errorf("ContextModeExecd = %q, want execd", ContextModeExecd)
	}
}
