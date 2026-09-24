// agent-sandbox/cmd/doctor.go
package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/internal/claude"
	"github.com/ynny-github/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/internal/execd"
	"github.com/ynny-github/agent-sandbox/internal/policysnapshot"
)

var errDoctorChecksFailed = errors.New("doctor: checks failed")

var doctorCmd = &cobra.Command{
	Use:           "doctor",
	Short:         "Check whether agent-sandbox's external dependencies are usable",
	SilenceErrors: true, // suppress cobra's automatic "Error: ..." print; rootCmd already silences usage
	RunE:          runDoctor,
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

func runDoctor(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	cfg, cfgErr := config.Load(configPath)
	results := []checkResult{
		checkNono(ctx),
		checkToolSandbox(ctx),
		checkExecdSocketDir(),
	}
	switch {
	case cfgErr == nil, errors.Is(cfgErr, config.ErrCommandProfileMissing) && cfg != nil:
		// config.Load returns cfg alongside ErrCommandProfileMissing
		// specifically (unlike every other validate failure, which leaves cfg
		// nil), so checkProfiles can still run and report its own
		// dedicated, actionable hint ("write the profile, or point
		// command_profile at it") instead of the generic one below, which the
		// os.Stat check in validate (internal/config/config.go) made otherwise
		// unreachable for this, the headline case doctor exists to catch.
		results = append(results, checkProfiles(ctx, cfg), checkProfilePaths(ctx, cfg))
	default:
		results = append(results, checkResult{
			name: "profiles",
			hint: "fix the config first: " + cfgErr.Error(),
		})
	}
	renderResults(cmd.OutOrStdout(), results)
	for _, r := range results {
		if !r.ok {
			return errDoctorChecksFailed
		}
	}
	return nil
}

type checkResult struct {
	name    string
	ok      bool
	details []string // each entry is a "key: value" line, no leading indent
	hint    string   // only meaningful when ok == false
}

var (
	lookPath      = exec.LookPath
	runCommand    = defaultRunCommand
	runCommandEnv = defaultRunCommandEnv
)

func defaultRunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// defaultRunCommandEnv runs a command with extra environment on top of this
// process's own. It is separate from defaultRunCommand because its only
// caller starts a sandbox, which is slower than the profile queries the
// five-second budget was sized for.
func defaultRunCommandEnv(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	return cmd.CombinedOutput()
}

// envProbeValue is the sentinel the env-forwarding probes look for. Any
// value would do; an obviously synthetic one keeps a failure legible.
const envProbeValue = "agent-sandbox-doctor-probe"

// checkExecdSocketVar measures whether the agent profile forwards
// AGENT_SANDBOX_EXECD_SOCKET into the sandbox. nono cannot be asked: `nono
// profile show` does not report environment.allow_vars, and `nono why` has no
// env query. Without the variable the agent never reaches execd and every
// command fails for a reason nothing on screen explains, so this is measured
// rather than assumed.
//
// --allow-cwd is required because nono refuses working-directory access in
// non-interactive mode, which is how doctor runs.
func checkExecdSocketVar(ctx context.Context, profilePath string) error {
	out, err := runCommandEnv(ctx,
		[]string{execd.SocketEnvVar + "=" + envProbeValue},
		"nono", "wrap", "--silent", "--allow-cwd", "--profile", profilePath,
		"--", "sh", "-c", "echo $"+execd.SocketEnvVar)
	if err != nil {
		return fmt.Errorf("could not run the probe: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), envProbeValue) {
		return fmt.Errorf("the profile does not forward %s into the sandbox", execd.SocketEnvVar)
	}
	return nil
}

// checkShellVar measures whether the agent profile forwards CLAUDE_CODE_SHELL
// into the sandbox. It is measured for the same reason as the execd socket
// variable: nono reports nothing about env grants, and the failure is silent.
//
// What it costs when it is stripped is noise, not access — Claude falls back to
// the host's bash, which sources ~/.bashrc, which the agent profile no longer
// grants — but the noise lands on every tool result the agent reads, and
// nothing in the session points at the profile as the cause.
func checkShellVar(ctx context.Context, profilePath string) error {
	out, err := runCommandEnv(ctx,
		[]string{claude.ShellEnvVar + "=" + envProbeValue},
		"nono", "wrap", "--silent", "--allow-cwd", "--profile", profilePath,
		"--", "sh", "-c", "echo $"+claude.ShellEnvVar)
	if err != nil {
		return fmt.Errorf("could not run the probe: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), envProbeValue) {
		return fmt.Errorf("the profile does not forward %s into the sandbox", claude.ShellEnvVar)
	}
	return nil
}

// checkContextModeVar measures whether the agent profile forwards
// CONTEXT_MODE_EXEC_BACKEND. It is the same probe shape as the two variables
// above, for the same reason: nono reports nothing about env grants.
//
// Unlike those two, a stripped value here is not a defect for every session —
// only for one launched with --context-mode — so the caller reports it without
// failing the check.
func checkContextModeVar(ctx context.Context, profilePath string) error {
	out, err := runCommandEnv(ctx,
		[]string{claude.ContextModeEnvVar + "=" + envProbeValue},
		"nono", "wrap", "--silent", "--allow-cwd", "--profile", profilePath,
		"--", "sh", "-c", "echo $"+claude.ContextModeEnvVar)
	if err != nil {
		return fmt.Errorf("could not run the probe: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), envProbeValue) {
		return fmt.Errorf("the profile does not forward %s into the sandbox", claude.ContextModeEnvVar)
	}
	return nil
}

// contextModePluginState renders what Claude Code reports about the plugin.
func contextModePluginState(ctx context.Context) string {
	out, err := runCommand(ctx, "claude", "plugin", "list", "--json")
	if err != nil {
		return "plugin state unknown (" + firstLine(err.Error()) + ")"
	}
	enabled, perr := claude.ContextModeEnabledIn(out)
	if perr != nil {
		return "plugin state unknown (" + firstLine(perr.Error()) + ")"
	}
	if enabled {
		return "plugin enabled"
	}
	return "plugin not enabled"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func checkNono(ctx context.Context) checkResult {
	const name = "nono"
	path, err := lookPath("nono")
	if err != nil {
		return checkResult{
			name:    name,
			ok:      false,
			details: []string{fmt.Sprintf("error: %v", err)},
			hint:    "install nono and make sure it is on PATH",
		}
	}
	out, err := runCommand(ctx, "nono", "--version")
	if err != nil {
		return checkResult{
			name:    name,
			ok:      false,
			details: []string{fmt.Sprintf("path: %s", path), fmt.Sprintf("error: \"nono --version\" failed: %v", err)},
			hint:    "verify the nono binary is functional (try running \"nono --version\" manually)",
		}
	}
	return checkResult{
		name:    name,
		ok:      true,
		details: []string{fmt.Sprintf("path: %s", path), fmt.Sprintf("version: %s", firstLine(string(out)))},
	}
}

// checkExecdSocketDir verifies the launcher can create the execd socket. A
// failure here means `agent-sandbox claude` cannot start execd,
// so every sandboxed command would fail.
//
// os.MkdirAll alone is not sufficient: it returns nil for a directory that
// already exists, regardless of its permission bits, so an existing
// unwritable state dir (e.g. left at mode 0500 by something else) would pass
// even though the launcher cannot actually create a socket file inside it.
// Binding a throwaway unix socket the same way the launcher does also catches
// the ~104-byte sun_path limit a plain write wouldn't.
func checkExecdSocketDir() checkResult {
	const name = "exec daemon"
	dir, err := policysnapshot.StateDir()
	if err != nil {
		return checkResult{name: name, ok: false,
			details: []string{fmt.Sprintf("error: %v", err)},
			hint:    "set HOME or XDG_STATE_HOME"}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return checkResult{name: name, ok: false,
			details: []string{fmt.Sprintf("error: %v", err)},
			hint:    fmt.Sprintf("make %s writable", dir)}
	}

	sockPath := filepath.Join(dir, fmt.Sprintf("doctor-%d.sock", os.Getpid()))
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return checkResult{name: name, ok: false,
			details: []string{fmt.Sprintf("error: %v", err)},
			hint: fmt.Sprintf(
				"make %s writable, or shorten XDG_STATE_HOME (unix socket paths are limited to ~104 bytes)", dir),
		}
	}
	l.Close()
	os.Remove(sockPath)

	return checkResult{name: name, ok: true,
		details: []string{fmt.Sprintf("socket dir: %s", dir)}}
}

// toolSandboxProbeCommand is the program checkToolSandbox asks nono to run to
// measure whether tool-sandbox can actually start on this host. "true" always
// exits 0 and needs nothing from its environment, so if the probe fails, the
// failure can only be tool-sandbox's own startup — never the probed program's
// behaviour.
const toolSandboxProbeCommand = "true"

// checkToolSandbox answers the question the design spec assigns to doctor:
// "that the nono on PATH can actually start tool-sandbox". That is not
// hypothetical — an unpatched nono cannot start tool-sandbox on NixOS at all,
// because it fails to resolve an ELF dependency chain that runs through a
// symlinked libgcc_s.so.1. See, under docs/superpowers/specs, the documents
// dated 2026-09-04 ("tool-sandbox blocked by claude") and 2026-09-05 ("broker
// probes"). Presence of the nono binary (checkNono) says nothing about this:
// nono starts, prints its version, and only refuses once command_policies
// actually activates tool-sandbox.
//
// The probe runs a real, short-lived nono session — the only way to answer the
// question, and cheap enough for an operator command that is not a hot path.
// It declares toolSandboxProbeCommand as a session-entrypoint policy command
// under a minimal profile and asks nono to run it. The profile includes the
// "nix_runtime" policy group (one of nono's own built-in groups, cross-platform
// and a no-op where Nix is not installed) so the probe also succeeds on a
// working NixOS host rather than only ever failing for lack of /nix/store
// access; without it, tool-sandbox's outer-session PATH scan cannot even load
// the shim nono generates for the probe command, regardless of how the probe
// command's own sandbox is configured. See
// docs/superpowers/specs/2026-09-05-broker-probes.md's "dp1" entry for this
// exact profile shape (as committed) measured against a working nono, a
// separately patched nono, and a build carrying the ELF-closure bug -- and
// for what happens without each piece: no groups.include -> the shim itself
// cannot execute (exit 127, "execution still failed"); a raw "/" filesystem
// grant -> nono refuses it outright as overlapping its own protected state
// root.
func checkToolSandbox(ctx context.Context) checkResult {
	r := checkResult{name: "tool-sandbox"}

	if _, err := lookPath("nono"); err != nil {
		r.details = append(r.details, fmt.Sprintf("error: %v", err))
		r.hint = "install nono and make sure it is on PATH (see the nono check above)"
		return r
	}
	probeBin, err := lookPath(toolSandboxProbeCommand)
	if err != nil {
		// Fail loudly rather than silently reporting OK: a check that could not
		// run at all must never look like a check that ran and passed.
		r.details = append(r.details, fmt.Sprintf("error: %v", err))
		r.hint = fmt.Sprintf("could not find %q on PATH to probe with; this check did not run", toolSandboxProbeCommand)
		return r
	}

	dir, err := os.MkdirTemp("", "agent-sandbox-doctor-toolsandbox")
	if err != nil {
		r.details = append(r.details, fmt.Sprintf("error: %v", err))
		r.hint = "could not create a temp directory to probe tool-sandbox in"
		return r
	}
	defer os.RemoveAll(dir)

	profilePath, err := writeToolSandboxProbeProfile(dir, probeBin)
	if err != nil {
		r.details = append(r.details, fmt.Sprintf("error: %v", err))
		r.hint = "could not write the probe profile"
		return r
	}

	out, err := runCommand(ctx, "nono", "run", "--silent",
		"--profile", profilePath, "--workdir", dir, "--", toolSandboxProbeCommand)
	if err != nil {
		r.details = append(r.details, "nono run: "+strings.TrimSpace(string(out)))
		r.hint = "the nono on PATH cannot start tool-sandbox on this host; " +
			"every command execd runs will fail the same way once a session starts " +
			"(a common cause is an unpatched nono on NixOS, which cannot resolve its ELF dependency layout)"
		return r
	}

	r.ok = true
	r.details = append(r.details, "probed with: "+toolSandboxProbeCommand)
	return r
}

// toolSandboxProbeProfile is the minimal nono profile checkToolSandbox runs
// under. Its field shapes are the "dp1" entry in
// docs/superpowers/specs/2026-09-05-broker-probes.md, which records this exact
// shape measured to succeed against a working nono and fail distinctly
// (nono's own ELF-resolution error) against a build that cannot resolve its
// dependency closure.
type toolSandboxProbeProfile struct {
	Meta struct {
		Name string `json:"name"`
	} `json:"meta"`
	Groups struct {
		Include []string `json:"include"`
	} `json:"groups"`
	Filesystem struct {
		Allow []string `json:"allow"`
	} `json:"filesystem"`
	Environment struct {
		AllowVars []string `json:"allow_vars"`
	} `json:"environment"`
	CommandPolicies struct {
		Commands map[string]toolSandboxProbeCommandPolicy `json:"commands"`
	} `json:"command_policies"`
}

type toolSandboxProbeCommandPolicy struct {
	Executable string `json:"executable"`
	From       struct {
		Session struct {
			Sandbox struct {
				FSReadFile  []string `json:"fs_read_file"`
				Environment struct {
					AllowVars []string `json:"allow_vars"`
				} `json:"environment"`
			} `json:"sandbox"`
		} `json:"session"`
	} `json:"from"`
}

// writeToolSandboxProbeProfile writes the probe profile into dir and returns
// its path. probeBin is granted read access under its own command policy (so
// the shim nono generates for it can be loaded and re-executed) and declared
// as the session's sole policy command, so the session entrypoint is exactly
// the thing checkToolSandbox asks nono to run.
func writeToolSandboxProbeProfile(dir, probeBin string) (string, error) {
	var p toolSandboxProbeProfile
	p.Meta.Name = "agent-sandbox doctor tool-sandbox probe"
	p.Groups.Include = []string{"nix_runtime"}
	p.Filesystem.Allow = []string{dir}
	p.Environment.AllowVars = []string{"PATH"}

	var cmd toolSandboxProbeCommandPolicy
	cmd.Executable = probeBin
	cmd.From.Session.Sandbox.FSReadFile = []string{probeBin}
	cmd.From.Session.Sandbox.Environment.AllowVars = []string{"PATH"}
	p.CommandPolicies.Commands = map[string]toolSandboxProbeCommandPolicy{
		toolSandboxProbeCommand: cmd,
	}

	data, err := json.MarshalIndent(&p, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "probe-profile.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// selfPath is os.Executable, indirected so tests can place the execd binary
// anywhere without moving a real file.
var selfPath = os.Executable

// checkProfiles validates both nono profiles and measures the one thing nono
// cannot report. agent-sandbox generates neither file, so every check here is
// either nono's own answer or a direct measurement.
//
// Each failure otherwise produces a session that refuses every command with an
// error the agent cannot act on: nono's own failure arrives on execd's
// stderr long after the launcher has returned.
func checkProfiles(ctx context.Context, cfg *config.Config) checkResult {
	r := checkResult{name: "profiles"}
	agentPath := cfg.AgentProfilePath("claude")
	cmdPath := cfg.CommandProfilePath()
	r.details = append(r.details, "agent profile: "+agentPath, "command profile: "+cmdPath)

	if err := validateProfile(agentPath); err != nil {
		r.details = append(r.details, "agent profile: "+err.Error())
		r.hint = "write the agent profile, or point [agents.claude].profile at it, " +
			"until `nono profile validate` passes"
		return r
	}
	if err := validateProfile(cmdPath); err != nil {
		r.details = append(r.details, "command profile: "+err.Error())
		r.hint = "write the profile, or point command_profile at it"
		return r
	}

	switch {
	case os.Getenv(execd.SocketEnvVar) != "":
		// Running inside a session: the probe would nest one nono sandbox in
		// another. Say so rather than report a failure nobody can act on.
		r.details = append(r.details,
			"execd socket variable: skipped (running inside a session; run doctor on the host)",
			"agent shell variable: skipped (running inside a session; run doctor on the host)",
			"context-mode: skipped (running inside a session; run doctor on the host)")
	default:
		if err := checkExecdSocketVar(ctx, agentPath); err != nil {
			r.details = append(r.details, "execd socket variable: "+err.Error())
			r.hint = "add " + execd.SocketEnvVar + " to the agent profile's " +
				"environment.allow_vars; without it the agent cannot reach execd " +
				"and every command fails"
			return r
		}
		r.details = append(r.details, "execd socket variable: forwarded")

		if err := checkShellVar(ctx, agentPath); err != nil {
			r.details = append(r.details, "agent shell variable: "+err.Error())
			r.hint = "add " + claude.ShellEnvVar + " to the agent profile's " +
				"environment.allow_vars; without it Claude runs tool commands with the host's " +
				"bash, which sources a ~/.bashrc the profile does not grant, and every tool " +
				"result carries the permission error"
			return r
		}
		r.details = append(r.details, "agent shell variable: forwarded")

		// Neither fact fails the check: a session that never passes
		// --context-mode needs neither of them.
		plugin := contextModePluginState(ctx)
		variable := "backend var not forwarded"
		if err := checkContextModeVar(ctx, agentPath); err == nil {
			variable = "backend var forwarded"
		}
		line := "context-mode: " + plugin + ", " + variable
		if plugin != "plugin enabled" || variable != "backend var forwarded" {
			line += " (needed only for --context-mode)"
		}
		r.details = append(r.details, line)
	}

	self, err := selfPath()
	if err != nil {
		// Fail loudly rather than silently reporting OK: not knowing
		// execd's own binary path means the writability check below never
		// ran, and that must not look like it passed.
		r.details = append(r.details, fmt.Sprintf("error: could not determine execd binary's own path: %v", err))
		r.hint = "could not verify execd's binary is not writable through the command profile; " +
			"investigate why os.Executable() failed and re-run doctor"
		return r
	}
	writable, werr := profileAllowsWrite(ctx, cmdPath, self)
	if werr != nil {
		r.details = append(r.details, fmt.Sprintf("error: could not ask nono whether %s is writable: %v", self, werr))
		r.hint = "could not verify execd's binary is not writable through the command profile; " +
			"fix the error above and re-run doctor"
		return r
	}
	if writable {
		r.details = append(r.details, "execd binary: "+self)
		r.hint = "the command profile grants write access to execd's own binary, so a command could " +
			"replace what the next launch runs; narrow the grant that covers it (`nono why --profile " +
			cmdPath + " --path " + self + " --op write` names it)"
		return r
	}

	r.ok = true
	return r
}

// profileAllowsWrite asks nono whether profilePath grants write access to
// binPath. The answer comes from `nono why --json`, whose status field is
// "allowed" or "denied"; the command exits 0 either way, so the status is the
// only signal.
//
// nono writes warnings (a bypass_protection entry naming a path that does not
// exist on this host, for one) to stderr, and runCommand combines the streams,
// so the JSON object is found rather than assumed to start at byte zero.
func profileAllowsWrite(ctx context.Context, profilePath, binPath string) (bool, error) {
	out, err := runCommand(ctx, "nono", "why", "--json",
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

// commandPolicySandbox is the subset of a `nono profile show --json` sandbox
// object this package reads.
type commandPolicySandbox struct {
	ExecPaths []string `json:"exec_paths"`
}

// commandPolicyEdge is one entry of a command's "from" map. nono's own type
// (CommandFromConfig, an untagged three-variant enum) lets this be any of:
//
//   - Deny(String): a bare policy string, e.g. `"session": "deny"` for a
//     command reachable only through another command, not the session
//     directly. Carries no sandbox and so no exec_paths.
//   - Edge(CommandEdgeConfig): an object wrapping the sandbox under a nested
//     "sandbox" key, alongside sibling fields like "invocation_policy":
//     `"session": {"sandbox": {...}, "invocation_policy": {...}}`.
//   - Policy(CommandSandboxConfig): the sandbox fields used directly as the
//     edge value, with no "sandbox" wrapper at all:
//     `"session": {"fs_read": [...], "exec_paths": [...]}`.
//
// UnmarshalJSON must tell Edge and Policy apart by the presence of the
// "sandbox" key, not by whether decoding into {Sandbox commandPolicySandbox
// `json:"sandbox"`} succeeds: a Policy object has no "sandbox" key, so that
// decode always succeeds anyway, quietly producing a zero-value sandbox and
// throwing away every field — including exec_paths — with no error. That
// silent loss is exactly what this whole check exists to prevent, so it must
// not happen here of all places.
type commandPolicyEdge struct {
	Sandbox commandPolicySandbox
}

func (e *commandPolicyEdge) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		// Deny(String): ignore the policy string, no sandbox to read.
		*e = commandPolicyEdge{}
		return nil
	}

	// Object cases only from here. Probe for the "sandbox" wrapper key rather
	// than guessing from decode success, per the type's doc comment above.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("command-policy edge is neither a string nor an object: %w", err)
	}

	if raw, ok := probe["sandbox"]; ok {
		// Edge(CommandEdgeConfig): decode the nested sandbox object.
		var sb commandPolicySandbox
		if err := json.Unmarshal(raw, &sb); err != nil {
			return fmt.Errorf("command-policy edge's \"sandbox\" field: %w", err)
		}
		e.Sandbox = sb
		return nil
	}

	// Policy(CommandSandboxConfig): no "sandbox" wrapper, so the object
	// itself is the sandbox.
	var sb commandPolicySandbox
	if err := json.Unmarshal(data, &sb); err != nil {
		return fmt.Errorf("command-policy edge: %w", err)
	}
	e.Sandbox = sb
	return nil
}

// pinnedPath is a single "executable" pin, tagged with the command that owns
// it so a failure message can name it.
type pinnedPath struct {
	Command string
	Path    string
}

// execPathSet is one edge's exec_paths list — one per (command, caller edge),
// or per command for the rare top-level "sandbox" shape with no "from"
// wrapper. nono skips a missing entry in this list silently, so the set
// (not any single path in it) is the unit that can fail: the command only
// loses its helper directory entirely once every entry in its set is gone.
type execPathSet struct {
	Label string // e.g. "git" (top-level sandbox) or "git (from.session)" (a caller edge)
	Paths []string
}

// pinnedPaths groups every host path a command profile pins by how nono
// fails when one goes missing, because that difference decides what doctor
// should report:
//
//   - Executables: a single "executable" pin. A missing one disables
//     mediation for that command entirely — nono falls back to the first
//     PATH match and runs it at the session's own grants.
//   - ExecutableDirs: command_policies.executable_dirs entries. nono refuses
//     to start the session at all if any single one of these is missing.
//   - ExecPathSets: exec_paths lists. nono skips a missing entry in one of
//     these silently; a set is only broken when every entry in it is gone.
type pinnedPaths struct {
	Executables    []pinnedPath
	ExecutableDirs []string
	ExecPathSets   []execPathSet
}

// commandPolicyPaths returns every host path the command profile pins,
// grouped by pinnedPaths' three failure modes: the profile's own
// "command_policies.executable_dirs", each command's "executable", and every
// "exec_paths" entry in every caller edge's sandbox (or a command's
// top-level "sandbox", for the rare shape with no "from" wrapper at all).
//
// The profile is read through `nono profile show --json`, never parsed from the
// file: nono's own parser handles JSONC, fills defaults, and is the authority on
// the schema. What this function adds is the part nono does not do — checking
// the paths against this host. Asking nono and measuring ourselves is the
// delegation rule; reading the file in Go would break it.
//
// nono writes warnings to stderr and runCommand combines the streams, so the
// JSON object is found rather than assumed to start at byte zero.
//
// Command and caller names are sorted before use so the result — and
// anything checkProfilePaths reports from it — does not vary with Go's
// randomized map iteration order.
func commandPolicyPaths(ctx context.Context, profilePath string) (pinnedPaths, error) {
	out, err := runCommand(ctx, "nono", "profile", "show", profilePath, "--json")
	if err != nil {
		return pinnedPaths{}, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	i := strings.IndexByte(string(out), '{')
	if i < 0 {
		return pinnedPaths{}, fmt.Errorf("no JSON in nono profile show output: %s", strings.TrimSpace(string(out)))
	}

	var shown struct {
		CommandPolicies struct {
			ExecutableDirs []string `json:"executable_dirs"`
			Commands       map[string]struct {
				Executable *string                      `json:"executable"`
				Sandbox    *commandPolicySandbox        `json:"sandbox"`
				From       map[string]commandPolicyEdge `json:"from"`
			} `json:"commands"`
		} `json:"command_policies"`
	}
	if err := json.Unmarshal([]byte(string(out)[i:]), &shown); err != nil {
		return pinnedPaths{}, err
	}

	var pp pinnedPaths
	pp.ExecutableDirs = append(pp.ExecutableDirs, shown.CommandPolicies.ExecutableDirs...)

	names := make([]string, 0, len(shown.CommandPolicies.Commands))
	for name := range shown.CommandPolicies.Commands {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		cmd := shown.CommandPolicies.Commands[name]
		if cmd.Executable != nil && *cmd.Executable != "" {
			pp.Executables = append(pp.Executables, pinnedPath{Command: name, Path: *cmd.Executable})
		}
		if cmd.Sandbox != nil && len(cmd.Sandbox.ExecPaths) > 0 {
			pp.ExecPathSets = append(pp.ExecPathSets, execPathSet{Label: name, Paths: cmd.Sandbox.ExecPaths})
		}

		callers := make([]string, 0, len(cmd.From))
		for caller := range cmd.From {
			callers = append(callers, caller)
		}
		sort.Strings(callers)
		for _, caller := range callers {
			if paths := cmd.From[caller].Sandbox.ExecPaths; len(paths) > 0 {
				pp.ExecPathSets = append(pp.ExecPathSets, execPathSet{
					Label: fmt.Sprintf("%s (from.%s)", name, caller),
					Paths: paths,
				})
			}
		}
	}
	return pp, nil
}

// expandHome resolves a leading "~" (bare, or "~/...") against the current
// user's home directory, the same way nono itself expands one in
// fs_read/fs_write/exec_paths when it applies the sandbox. `nono profile show
// --json` — what commandPolicyPaths reads — does NOT perform this expansion;
// it prints the profile's own literal strings. Without this, a perfectly
// valid "~/..." exec_paths entry reads as "missing" here even though the
// directory exists and nono itself grants it fine at runtime (measured, nono
// 0.74.0). $WORKDIR and other $VAR-style tokens are not handled here: none of
// this profile's own exec_paths/executable/executable_dirs entries use one.
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// checkProfilePaths reports command-policy paths that no longer exist on
// this host, judging each of pinnedPaths' three groups by how nono itself
// fails when an entry in it goes missing — the three modes do not fail the
// same way, so treating them alike would make this check either too loud
// (NG on a healthy host, for the exec_paths candidates a profile lists on
// purpose so it can run on more than one host) or too quiet (missing a
// severe case):
//
//   - a missing "executable" pin disables mediation for that command
//     entirely: nono falls back to the first PATH match and runs it at the
//     session's own grants, `nono profile validate` still passes, and the
//     audit records "tools: active, no invocations" (measured, nono 0.74.0).
//     This is not silent — nono prints a warning and --silent does not
//     suppress it — but the warning is easy to miss in a wall of launch
//     output, and the danger it names is real. NG on any single miss.
//   - a missing command_policies.executable_dirs entry makes nono refuse to
//     start the session at all (measured, nono 0.74.0). NG on any single
//     miss, same as "executable" above.
//   - a missing exec_paths entry is skipped by design, with its warning
//     suppressed by the --silent the launcher passes — this is what makes a
//     candidate list (several plausible locations for the same helper
//     directory, so the same profile works on more than one kind of host)
//     a legitimate thing to write. NG only when every entry in a given
//     command's exec_paths set is gone, because only then does that command
//     have no reachable helper directory left at all.
//
// On NixOS every such path carries a store hash, so any package update can
// produce either state. This check is what makes that loud — without
// drowning out the signal on a healthy host that happens to carry
// non-NixOS candidates it will never use.
func checkProfilePaths(ctx context.Context, cfg *config.Config) checkResult {
	r := checkResult{name: "profile paths"}
	profilePath := cfg.CommandProfilePath()

	pinned, err := commandPolicyPaths(ctx, profilePath)
	if err != nil {
		r.details = append(r.details, fmt.Sprintf("error: could not ask nono what %s pins: %v", profilePath, err))
		r.hint = "could not verify the command profile's pinned paths; fix the error above and re-run doctor"
		return r
	}
	if len(pinned.Executables) == 0 && len(pinned.ExecutableDirs) == 0 && len(pinned.ExecPathSets) == 0 {
		r.ok = true
		r.details = append(r.details, "no command-policy paths pinned")
		return r
	}

	exists := func(p string) bool {
		_, err := os.Stat(expandHome(p))
		return err == nil
	}

	var problems []string
	for _, e := range pinned.Executables {
		if !exists(e.Path) {
			problems = append(problems, fmt.Sprintf("%s: executable pin missing: %s", e.Command, e.Path))
		}
	}
	for _, p := range pinned.ExecutableDirs {
		if !exists(p) {
			problems = append(problems, "executable_dirs entry missing: "+p)
		}
	}
	resolvedSets := 0
	for _, set := range pinned.ExecPathSets {
		anyPresent := false
		for _, p := range set.Paths {
			if exists(p) {
				anyPresent = true
				break
			}
		}
		if anyPresent {
			resolvedSets++
			continue
		}
		problems = append(problems, fmt.Sprintf("%s: every exec_paths entry is missing (%s)",
			set.Label, strings.Join(set.Paths, ", ")))
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		r.details = problems
		r.hint = "the command profile pins paths that no longer exist on this host, and nono does " +
			"not fail the same way for every kind: a missing `executable` pin disables mediation " +
			"for that command entirely, and a missing `executable_dirs` entry stops nono from " +
			"starting the session at all — both reported here on any single miss. nono skips a " +
			"missing `exec_paths` entry silently instead, so that one is only reported once every " +
			"entry in a command's own exec_paths set is gone (a single missing entry among several " +
			"is expected — that is what a portable candidate list is for, and is not reported here " +
			"on its own). Update " + profilePath +
			" to the current paths (a package upgrade is the usual cause)"
		return r
	}

	r.ok = true
	var parts []string
	if n := len(pinned.Executables); n > 0 {
		parts = append(parts, fmt.Sprintf("%d executable pin(s) present", n))
	}
	if n := len(pinned.ExecutableDirs); n > 0 {
		parts = append(parts, fmt.Sprintf("%d executable_dirs entry(ies) present", n))
	}
	if resolvedSets > 0 {
		// resolvedSets counts exec_paths SETS, one per (command, caller edge) —
		// not distinct commands. A single command with several caller edges
		// (this repository's own "go" has session/bash/sh edges, each with its
		// own exec_paths) contributes one set per edge, so this number can
		// exceed the number of commands it came from.
		parts = append(parts, fmt.Sprintf("%d exec_paths set(s) resolve", resolvedSets))
	}
	r.details = append(r.details, strings.Join(parts, "; "))
	return r
}
func renderResults(w io.Writer, results []checkResult) {
	failed := 0
	for _, r := range results {
		label := "[OK]"
		if !r.ok {
			label = "[NG]"
			failed++
		}
		fmt.Fprintf(w, "%s %s\n", label, r.name)
		for _, d := range r.details {
			fmt.Fprintf(w, "     %s\n", d)
		}
		if !r.ok && r.hint != "" {
			fmt.Fprintf(w, "     hint: %s\n", r.hint)
		}
		fmt.Fprintln(w)
	}
	if failed == 0 {
		fmt.Fprintln(w, "doctor: all checks passed")
	} else {
		fmt.Fprintf(w, "doctor: %d of %d checks failed\n", failed, len(results))
	}
}
