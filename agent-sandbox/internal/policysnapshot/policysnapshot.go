// Package policysnapshot persists the sandbox policy (a *config.Config) to a
// per-session JSON file so hook-mode `agent-sandbox exec` can route from a
// frozen copy the agent cannot edit, rather than re-reading the mutable
// agent-sandbox.toml on every command.
package policysnapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

// stateDir returns the directory that holds policy snapshots:
// $XDG_STATE_HOME/agent-sandbox, falling back to ~/.local/state/agent-sandbox.
func stateDir() (string, error) {
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

// Write serializes cfg as JSON to policy-<pid>.json under the state dir and
// returns the path plus a cleanup function that removes the file. The file is
// written on the host before the sandbox starts; callers grant nono read-only
// access to it (--read-file) so the agent cannot tamper with it.
func Write(cfg *config.Config) (string, func(), error) {
	dir, err := stateDir()
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("encode policy snapshot: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("policy-%d.json", os.Getpid()))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", nil, fmt.Errorf("write policy snapshot: %w", err)
	}
	cleanup := func() { _ = os.Remove(path) }
	return path, cleanup, nil
}

// Load reads and decodes a policy snapshot written by Write. A missing or
// corrupt file is an error: hook-mode exec must fail closed rather than fall
// back to the mutable config.
func Load(path string) (*config.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy snapshot: %w", err)
	}
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse policy snapshot: %w", err)
	}
	return &cfg, nil
}
