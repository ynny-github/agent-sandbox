// Package claude holds what doctor checks about a Claude Code session under
// agent-sandbox: where execd is, whether the command profile can write a
// path, and whether the PreToolUse hook and context-mode selection work under
// the agent's profile. The launch itself is the user's script.
package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// ShellEnvVar names the variable Claude Code reads to pick the shell it runs
// every Bash tool command with.
const ShellEnvVar = "CLAUDE_CODE_SHELL"

// ResolveExecd finds the execd binary on PATH and resolves symlinks, because
// Landlock checks the target and a grant has to name it. startExecd calls it
// once per launch and run feeds the same path to both sessions, so they cannot
// disagree about which execd they run.
func ResolveExecd() (string, error) {
	p, err := exec.LookPath("execd")
	if err != nil {
		return "", fmt.Errorf("execd not found in PATH; install it with "+
			"`go install github.com/ynny-github/agent-sandbox/cmd/execd@latest`: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
		return resolved, nil
	}
	return p, nil
}

// hookProbePayload is a minimal PreToolUse payload; hookProbeWant is what the
// hook must rewrite it into. The command is `true` so that nothing runs even if
// the response were somehow acted on.
const (
	hookProbePayload = `{"tool_name":"Bash","tool_input":{"command":"true"}}`
	hookProbeWant    = "execd exec --"
)

// ProbeHook runs the PreToolUse hook (`execd hook claude`) inside the agent's own sandbox and checks
// that it rewrites a command, before the agent is launched.
//
// It exists because the failure it catches is invisible and unsafe. Claude Code
// blocks a tool call only when a hook exits 2; a hook that cannot start at all
// is a non-blocking error, and the original command then runs unwrapped, in the
// agent's own sandbox, under none of the command profile's limits. Nothing in
// the session says so. The launcher therefore proves the hook runs under this
// exact profile rather than assuming it, in the same "measure, do not parse"
// shape doctor uses for the execd socket variable.
//
// --allow-cwd is required because nono refuses working-directory access in
// non-interactive mode, which is how this runs.
func ProbeHook(profilePath, execdPath string) error {
	nonoPath, err := exec.LookPath("nono")
	if err != nil {
		return fmt.Errorf("nono not found in PATH: %w", err)
	}
	cmd := exec.Command(nonoPath, "wrap", "--silent", "--allow-cwd",
		"--profile", profilePath, "--read-file", execdPath, "--", execdPath, "hook", "claude")
	cmd.Stdin = strings.NewReader(hookProbePayload)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return fmt.Errorf("the PreToolUse hook cannot run under %s: %v: %s",
			profilePath, runErr, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), hookProbeWant) {
		return fmt.Errorf("the PreToolUse hook ran under %s but did not route the command "+
			"through execd: %s", profilePath, strings.TrimSpace(string(out)))
	}
	return nil
}

// ProfileAllowsWrite asks nono whether profilePath grants write access to
// binPath, running nono through run (which returns combined output). The answer comes from `nono why --json`, whose status field is
// "allowed" or "denied"; the command exits 0 either way, so the status is the
// only signal.
//
// nono writes warnings (a bypass_protection entry naming a path that does not
// exist on this host, for one) to stderr, and run is expected to combine the streams,
// so the JSON object is found rather than assumed to start at byte zero.
func ProfileAllowsWrite(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error),
	profilePath, binPath string) (bool, error) {
	out, err := run(ctx, "nono", "why", "--json",
		"--profile", profilePath, "--path", binPath, "--op", "write")
	if err != nil {
		return false, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	i := strings.IndexByte(string(out), '{')
	if i < 0 {
		return false, fmt.Errorf("no JSON in nono why output: %s", strings.TrimSpace(string(out)))
	}
	var answer struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(string(out)[i:]), &answer); err != nil {
		return false, err
	}
	switch answer.Status {
	case "allowed":
		return true, nil
	case "denied":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected nono why status %q", answer.Status)
	}
}
