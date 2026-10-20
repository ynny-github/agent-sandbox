// internal/cli/doctor.go
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/internal/claude"
	"github.com/ynny-github/agent-sandbox/internal/launch"
)

var errDoctorChecksFailed = errors.New("doctor: checks failed")

var doctorCmd = &cobra.Command{
	Use:           "doctor",
	Short:         "Check that a launch is safe: the hook runs, and nothing the sandbox can write gets run",
	SilenceErrors: true, // suppress cobra's automatic "Error: ..." print; rootCmd already silences usage
	RunE:          runDoctor,
}

// profiles are the two nono profiles doctor checks.
type profiles struct{ agent, command string }

var doctorCommandProfile, doctorAgentProfile string

// probeHook is a var so tests can stand in for the real nono.
var probeHook = claude.ProbeHook

func init() {
	doctorCmd.Flags().StringVar(&doctorCommandProfile, "command-profile",
		filepath.Join(launch.Dir, "command-profile.json"), "execd's nono profile")
	doctorCmd.Flags().StringVar(&doctorAgentProfile, "agent-profile",
		filepath.Join(launch.Dir, "claude-profile.json"), "the agent's nono profile")
	rootCmd.AddCommand(doctorCmd)
}

func runDoctor(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	p := profiles{agent: doctorAgentProfile, command: doctorCommandProfile}
	results := []checkResult{profileFilesExist(p)}
	if results[0].ok {
		results = append(results, checkHook(p), checkBinaries(ctx, p), checkApprovedCopy(ctx, p))
	}
	// The launch script runs doctor on every launch, so a passing run is
	// silent; a failing one prints every check for context.
	for _, r := range results {
		if !r.ok {
			renderResults(cmd.OutOrStdout(), results)
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

var runCommand = defaultRunCommand

func defaultRunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// selfPath is os.Executable, indirected so tests can place agent-sandbox's
// own binary anywhere without moving a real file.
var selfPath = os.Executable

// execdPath is claude.ResolveExecd, indirected so tests can place the execd
// binary anywhere without installing one.
var execdPath = claude.ResolveExecd

// checkBinaries fails when execd is not on PATH, or when the command profile
// can write execd, its directory, or agent-sandbox: a command could then
// replace what the next launch runs.
func checkBinaries(ctx context.Context, p profiles) checkResult {
	r := checkResult{name: "binaries"}
	self, err := selfPath()
	if err != nil {
		// Fail loudly rather than silently reporting OK: not knowing the
		// binary's path means the writability check below never ran, and
		// that must not look like it passed.
		r.details = append(r.details, fmt.Sprintf("error: could not determine agent-sandbox's own path: %v", err))
		r.hint = "could not verify agent-sandbox's binary is not writable through the command profile; " +
			"investigate why os.Executable() failed and re-run doctor"
		return r
	}
	execdBin, err := execdPath()
	if err != nil {
		r.details = append(r.details, "execd binary: "+err.Error())
		r.hint = "install execd: go install github.com/ynny-github/agent-sandbox/cmd/execd@latest"
		return r
	}
	r.details = append(r.details, "execd binary: "+execdBin)

	// Two files the command profile must not be able to write: a writable
	// execd replaces what the next session serves with, a writable
	// agent-sandbox replaces the launcher and the hook.
	for _, bin := range []string{execdBin, self} {
		writable, werr := claude.ProfileAllowsWrite(ctx, runCommand, p.command, bin)
		if werr != nil {
			r.details = append(r.details, fmt.Sprintf("error: could not ask nono whether %s is writable: %v", bin, werr))
			r.hint = "could not verify " + bin + " is not writable through the command profile; " +
				"fix the error above and re-run doctor"
			return r
		}
		if writable {
			r.hint = "the command profile grants write access to " + bin + ", so a command could " +
				"replace what the next launch runs; narrow the grant that covers it (`nono why --profile " +
				p.command + " --path " + bin + " --op write` names it)"
			return r
		}
	}

	r.ok = true
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

// profileFilesExist fails with the init hint when either profile is missing.
func profileFilesExist(p profiles) checkResult {
	r := checkResult{name: "profiles"}
	for _, path := range []string{p.agent, p.command} {
		if _, err := os.Stat(path); err != nil {
			r.details = append(r.details, err.Error())
			r.hint = "run `agent-sandbox init`, or pass --agent-profile/--command-profile"
			return r
		}
	}
	r.ok = true
	return r
}

// checkHook proves the PreToolUse hook runs under the agent profile. A hook
// that cannot start is a non-blocking error to Claude Code, and the command
// then runs unwrapped, outside execd, with nothing on screen.
func checkHook(p profiles) checkResult {
	r := checkResult{name: "hook"}
	execdBin, err := execdPath()
	if err != nil {
		r.details = append(r.details, err.Error())
		r.hint = "install execd: go install github.com/ynny-github/agent-sandbox/cmd/execd@latest"
		return r
	}
	if err := probeHook(p.agent, execdBin); err != nil {
		r.details = append(r.details, err.Error())
		r.hint = "grant execd to the agent (the launch script's --read-file does) and keep the profile able to run it"
		return r
	}
	r.ok = true
	return r
}

// checkApprovedCopy fails when the agent profile can write the copy of
// .agent-sandbox/ that `agent-sandbox launch` runs. Claude's own file tools run
// under the agent profile, not through the hook, so such a grant would let the
// agent change what the next launch runs with no prompt. launch itself checks
// the command profile; only doctor knows the agent profile.
func checkApprovedCopy(ctx context.Context, p profiles) checkResult {
	r := checkResult{name: "approved copy"}
	wd, err := os.Getwd()
	if err != nil {
		r.details = append(r.details, err.Error())
		r.hint = "run doctor from the project directory"
		return r
	}
	dir, err := launch.TrustedDir(wd)
	if err != nil {
		r.details = append(r.details, err.Error())
		r.hint = "set HOME or XDG_CONFIG_HOME"
		return r
	}
	r.details = append(r.details, "copy: "+dir)
	writable, err := claude.ProfileAllowsWrite(ctx, runCommand, p.agent, dir)
	if err != nil {
		r.details = append(r.details, err.Error())
		r.hint = "could not ask nono whether the agent profile can write " + dir
		return r
	}
	if writable {
		r.hint = "the agent profile grants write access to " + dir + ", which launch runs from; " +
			"narrow the grant (`nono why --profile " + p.agent + " --path " + dir + " --op write` names it)"
		return r
	}
	r.ok = true
	return r
}
