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

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/execd"
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

// contextModeProbeExpr is evaluated by node inside the agent's sandbox. The
// separator is "|" rather than a space because Array.prototype.join renders an
// absent variable as the empty string: space-joined, a stripped backend
// variable and a socket-only line are the same text, and the error could not
// say which value is missing.
var contextModeProbeExpr = "[process.env." + ContextModeEnvVar +
	", process.env." + execd.SocketEnvVar + "].join(\"|\")"

// probeContextMode proves, before the agent is launched, that a context-mode
// MCP server born in this sandbox would see the backend selection.
//
// It runs node — the binary the plugin manifest names — under the agent's own
// profile, so it measures the environment the server is actually about to get:
// same profile, same env filter, same parent. This is probeHook's rationale
// applied to a second silent failure. Claude Code reports an MCP server that
// cannot start as nothing but an absent tool, and context-mode reads an absent
// variable as "local", so neither failure announces itself in a session.
//
// --allow-cwd is required because nono refuses working-directory access in
// non-interactive mode, which is how this runs.
func probeContextMode(profilePath string) error {
	nonoPath, err := exec.LookPath("nono")
	if err != nil {
		return fmt.Errorf("nono not found in PATH: %w", err)
	}
	cmd := exec.Command(nonoPath, "wrap", "--silent", "--allow-cwd",
		"--profile", profilePath, "--", "node", "-p", contextModeProbeExpr)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()
	if runErr != nil {
		return fmt.Errorf("node could not run under %s: %v: %s\n"+
			"context-mode's MCP server is `node <plugin>/start.mjs`, so the agent "+
			"profile has to be able to execute node", profilePath, runErr,
			strings.TrimSpace(stderr.String()))
	}
	return parseContextModeProbe(string(out))
}

// parseContextModeProbe interprets what the probe printed.
func parseContextModeProbe(out string) error {
	line := strings.TrimSpace(out)
	backend, socket, found := strings.Cut(line, "|")
	if !found {
		return fmt.Errorf("the probe printed unexpected output %q, which is not the expected "+
			"\"<backend>|<socket>\" pair", line)
	}
	if backend == "" {
		return fmt.Errorf("%s does not reach the sandbox; add it to the agent "+
			"profile's environment.allow_vars, or context-mode will run commands "+
			"in the agent's own sandbox instead of under the command profile",
			ContextModeEnvVar)
	}
	if backend != ContextModeExecd {
		return fmt.Errorf("%s reached the sandbox as %q, want %q",
			ContextModeEnvVar, backend, ContextModeExecd)
	}
	if socket == "" {
		return fmt.Errorf("%s does not reach the sandbox; context-mode refuses to "+
			"start with the execd backend and no socket", execd.SocketEnvVar)
	}
	return nil
}
