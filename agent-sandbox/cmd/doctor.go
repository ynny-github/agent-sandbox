// agent-sandbox/cmd/doctor.go
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/policysnapshot"
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
		checkBrokerSocketDir(),
	}
	if cfgErr != nil {
		results = append(results, checkResult{
			name: "command profile",
			hint: "fix the config first: " + cfgErr.Error(),
		})
	} else {
		results = append(results, checkCommandProfile(cfg))
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
	lookPath   = exec.LookPath
	runCommand = defaultRunCommand
)

func defaultRunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
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

// checkBrokerSocketDir verifies the launcher can create the broker socket. A
// failure here means `agent-sandbox claude` cannot start the command broker,
// so every sandboxed command would fail.
//
// os.MkdirAll alone is not sufficient: it returns nil for a directory that
// already exists, regardless of its permission bits, so an existing
// unwritable state dir (e.g. left at mode 0500 by something else) would pass
// even though the launcher cannot actually create a socket file inside it.
// Binding a throwaway unix socket the same way the launcher does also catches
// the ~104-byte sun_path limit a plain write wouldn't.
func checkBrokerSocketDir() checkResult {
	const name = "command broker"
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

// selfPath is os.Executable, indirected so tests can place the broker binary
// anywhere without moving a real file.
var selfPath = os.Executable

// checkCommandProfile reports whether the profile the broker will run under is
// present, accepted by nono, and does not grant write access to the broker's own
// binary. Each of these otherwise produces a session that refuses every command
// with an error the agent cannot act on: nono's own failure arrives on the
// broker's stderr long after the launcher has returned.
func checkCommandProfile(cfg *config.Config) checkResult {
	r := checkResult{name: "command profile"}
	path := cfg.CommandProfilePath()
	r.details = append(r.details, "path: "+path)

	if _, err := os.Stat(path); err != nil {
		r.hint = "write the profile, or point command_profile at it"
		return r
	}
	if out, err := runCommand(context.Background(), "nono", "profile", "validate", path); err != nil {
		r.details = append(r.details, "nono profile validate: "+strings.TrimSpace(string(out)))
		r.hint = "fix the profile until `nono profile validate` passes"
		return r
	}
	self, err := selfPath()
	if err != nil {
		// Fail loudly rather than silently reporting OK: not knowing the
		// broker's own binary path means the writability check below never
		// ran, and that must not look like it passed.
		r.details = append(r.details, fmt.Sprintf("error: could not determine the broker binary's own path: %v", err))
		r.hint = "could not verify the broker binary is not writable through the profile; " +
			"investigate why os.Executable() failed and re-run doctor"
		return r
	}
	writable, werr := profileGrantsWrite(path, self)
	if werr != nil {
		r.details = append(r.details, fmt.Sprintf("error: could not check whether the profile grants write access to %s: %v", self, werr))
		r.hint = "could not verify the broker binary is not writable through the profile; " +
			"fix the error above and re-run doctor"
		return r
	}
	if writable {
		r.details = append(r.details, "broker binary: "+self)
		r.hint = "move the agent-sandbox binary outside every path the profile grants write access to; " +
			"nono refuses a policy command binary it considers replaceable"
		return r
	}

	base := filepath.Base(self)
	resolved, rerr := lookPath(base)
	if rerr != nil {
		r.details = append(r.details, fmt.Sprintf("entrypoint: %q not found on PATH", base))
		r.hint = "the launcher invokes the broker by base name (\"" + base + "\"), resolved through this " +
			"process's own PATH the same way an ordinary shell would, before any sandbox exists — nono's " +
			"command_policies.executable_dirs plays no part in resolving it (measured; see BrokerArgs's doc " +
			"comment); put the directory holding the installed agent-sandbox binary on PATH, or the session " +
			"will fail to start"
		return r
	}
	if cleanAbs(resolved) != cleanAbs(self) {
		r.details = append(r.details, fmt.Sprintf("entrypoint: %q on PATH resolves to %s, not the running binary %s", base, resolved, self))
		r.hint = "PATH resolves \"" + base + "\" to a different binary than the one running this check — " +
			"that other one would silently become the broker instead, whatever it is; put this binary's own " +
			"directory ahead of it on PATH, or remove the other one"
		return r
	}

	r.ok = true
	return r
}

// cleanAbs is filepath.Clean for two paths being compared for identity, one
// of which (lookPath's result) may not be absolute if PATH itself contains a
// relative entry. Comparing raw strings would treat "./agent-sandbox" and its
// absolute equivalent as different binaries when they are the same file.
func cleanAbs(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}

// profileGrantsWrite reports whether binPath falls under one of the profile's
// filesystem.allow entries. It reads only that list: it is the grant that made
// nono refuse to start during the design measurements, and a fuller model of
// nono's own trust check belongs in nono, not here.
//
// Matching is by filepath.Clean, not symlink resolution: a granted directory
// that is itself a symlink (e.g. macOS's /tmp -> /private/tmp) could hide a
// binary this check should have flagged. That gap is a deliberate choice, in
// keeping with reading only filesystem.allow at all (see above) rather than
// building a fuller model of nono's own trust check — not an oversight.
func profileGrantsWrite(profilePath, binPath string) (bool, error) {
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return false, err
	}
	var p struct {
		Filesystem struct {
			Allow []string `json:"allow"`
		} `json:"filesystem"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return false, err
	}
	bin := filepath.Clean(binPath)
	for _, dir := range p.Filesystem.Allow {
		dir = filepath.Clean(strings.TrimSpace(dir))
		if dir == "" || strings.Contains(dir, "$") {
			continue // $WORKDIR and friends are resolved by nono, not here
		}
		if rel, rerr := filepath.Rel(dir, bin); rerr == nil &&
			rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true, nil
		}
	}
	return false, nil
}

// toolSandboxProbeCommand is the trivial, argument-free program checkToolSandbox
// declares as a policy-controlled command to find out whether nono's
// tool-sandbox can actually start on this host. "true" always exits 0 and needs
// nothing from its environment, so if the probe fails, the failure can only be
// tool-sandbox's own startup — never the probed program's behaviour.
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
			"every command the broker runs will fail the same way once a session starts " +
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
