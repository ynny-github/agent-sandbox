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

func TestContextModeEnabledIn(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want bool
	}{
		{
			name: "enabled",
			out:  `[{"id":"context-mode@context-mode","enabled":true}]`,
			want: true,
		},
		{
			name: "installed but disabled",
			out:  `[{"id":"context-mode@context-mode","enabled":false}]`,
			want: false,
		},
		{
			name: "other plugins only",
			out:  `[{"id":"superpowers@claude-plugins-official","enabled":true}]`,
			want: false,
		},
		{
			name: "no plugins at all",
			out:  `[]`,
			want: false,
		},
		{
			// Any marketplace may carry it; the id's left half is what identifies
			// the plugin.
			name: "another marketplace",
			out:  `[{"id":"context-mode@local-dir","enabled":true}]`,
			want: true,
		},
		{
			// runCommand-style callers combine stdout and stderr, so the array is
			// found rather than assumed to start at byte zero.
			name: "warning before the array",
			out:  "warning: something\n[{\"id\":\"context-mode@context-mode\",\"enabled\":true}]",
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ContextModeEnabledIn([]byte(tc.out))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("ContextModeEnabledIn(%s) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}

func TestContextModeEnabledIn_NotJSON(t *testing.T) {
	if _, err := ContextModeEnabledIn([]byte("command not found")); err == nil {
		t.Error("unparseable output must be an error, not a quiet false: " +
			"a false would read as \"context-mode is off\" and stop the launch " +
			"for the wrong reason")
	}
}
