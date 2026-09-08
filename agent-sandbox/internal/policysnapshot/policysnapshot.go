// Package policysnapshot used to persist the sandbox policy to a per-session
// JSON file so hook-mode `agent-sandbox exec` could route from a frozen copy
// the agent could not edit. That routing is gone — the broker now interprets
// the agent's command line itself, under an operator-written nono profile,
// with no `allow_commands`/`drop_commands` left to freeze — so this package
// keeps only the one thing that outlived it: the shared state directory path.
package policysnapshot

import (
	"fmt"
	"os"
	"path/filepath"
)

// StateDir returns agent-sandbox's per-user state directory:
// $XDG_STATE_HOME/agent-sandbox, falling back to ~/.local/state/agent-sandbox.
// It is the single source of truth for this path; the command broker socket
// (internal/claude) and `agent-sandbox doctor`'s broker-readiness check both
// derive their paths from it, so the two stay in sync if it ever moves.
func StateDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "agent-sandbox"), nil
}
