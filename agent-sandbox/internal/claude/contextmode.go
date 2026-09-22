// agent-sandbox/internal/claude/contextmode.go
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ContextModeEnvVar names the variable context-mode reads to choose where
// ctx_execute and its siblings actually run. agent-sandbox publishes it; the
// meaning of its values belongs to context-mode's resolveBackendConfig
// (src/exec-backend.ts in github.com/ynny-github/context-mode).
//
// Unset or empty resolves to the "local" backend over there — agent-authored
// code spawned as a child of the MCP server, inside the *agent* profile, under
// none of the command profile's policies. That is why every failure on this
// side of the boundary stops the launch instead of warning: a variable that
// does not arrive produces a working-looking session with the boundary quietly
// absent.
const ContextModeEnvVar = "CONTEXT_MODE_EXEC_BACKEND"

// ContextModeExecd is the only value agent-sandbox ever publishes. context-mode
// also accepts "local", which is its default and needs no help from here. It is
// exported because `agent-sandbox debug` prints the pair.
const ContextModeExecd = "execd"

// contextModePluginPrefix is what a plugin id looks like before its
// marketplace. The right half varies with where it was installed from, so only
// the left half identifies the plugin.
const contextModePluginPrefix = "context-mode@"

// contextModeListTimeout bounds the plugin query. Measured at 0.29s on the
// author's machine; the bound exists so a wedged `claude` cannot hang a launch
// indefinitely.
const contextModeListTimeout = 30 * time.Second

// pluginListEntry is the subset of `claude plugin list --json` this package
// reads. Claude Code owns the rest of the shape.
type pluginListEntry struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

// ContextModeEnabledIn reports whether out — what `claude plugin list --json`
// printed — carries an enabled context-mode plugin.
//
// Claude Code is asked rather than ~/.claude/settings.json read, because the
// settings file records an intent, not a fact: measured on the author's host,
// enabledPlugins lists "context-mode@context-mode" while `claude plugin list`
// reports no such plugin at all (it is not installed).
//
// Output is scanned for the opening bracket rather than parsed from byte zero:
// callers that combine stdout and stderr (doctor's runCommand seam) can prefix
// it with nono or claude warnings.
func ContextModeEnabledIn(out []byte) (bool, error) {
	i := bytes.IndexByte(out, '[')
	if i < 0 {
		return false, fmt.Errorf("no JSON array in `claude plugin list --json` output: %s",
			strings.TrimSpace(string(out)))
	}
	var entries []pluginListEntry
	if err := json.Unmarshal(out[i:], &entries); err != nil {
		return false, fmt.Errorf("parse `claude plugin list --json`: %w", err)
	}
	for _, e := range entries {
		if e.Enabled && strings.HasPrefix(e.ID, contextModePluginPrefix) {
			return true, nil
		}
	}
	return false, nil
}

// contextModeEnabled asks the installed Claude Code whether context-mode is
// enabled for this user.
func contextModeEnabled() (bool, error) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return false, fmt.Errorf("claude not found in PATH: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), contextModeListTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, claudePath, "plugin", "list", "--json").CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("run `claude plugin list --json`: %v: %s",
			err, strings.TrimSpace(string(out)))
	}
	return ContextModeEnabledIn(out)
}
