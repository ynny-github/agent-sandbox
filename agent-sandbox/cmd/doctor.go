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
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxhost"
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
		checkBrokerSocketDir(),
	}
	switch {
	case cfgErr == nil, errors.Is(cfgErr, config.ErrCommandProfileMissing) && cfg != nil:
		// config.Load returns cfg alongside ErrCommandProfileMissing
		// specifically (unlike every other validate failure, which leaves cfg
		// nil), so checkCommandProfile can still run and report its own
		// dedicated, actionable hint ("write the profile, or point
		// command_profile at it") instead of the generic one below, which
		// config.go:204 made otherwise unreachable for this, the headline
		// case doctor exists to catch.
		results = append(results, checkProfiles(cfg))
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

// checkProfiles reports whether the two nono profiles a launch depends on are
// present and accepted by nono, and whether the command profile leaves the
// broker's own binary replaceable.
//
// Both profiles are handed to `nono profile validate` rather than parsed here.
// An earlier version read the command profile's JSON directly, which was both
// fragile and incomplete: it could not see a grant that arrives through a
// policy group (whose paths live in nono's policy.json, not in the profile), it
// skipped every entry containing a "$" because expansion is nono's job, and it
// broke outright once the profile grew comments — nono accepts JSONC, Go's
// encoding/json does not. Asking nono is the only way to get answers in nono's
// own semantics.
//
// What validate covers is narrow, measured: JSON syntax and group references,
// nothing else. A grant over a protected path, a malformed domain and a
// nonexistent path all pass it. That is why the writable-binary question is
// asked separately, through `nono why`, which resolves groups, "~"/"$XDG_*"
// expansion and bypass_protection before answering.
//
// The agent profile is included because nothing else validates it: `ai
// config-check` resolves the config through sandboxhost but never hands the
// JSON it generates to nono.
//
// Each failure here otherwise produces a session that refuses every command
// with an error the agent cannot act on: nono's own failure arrives on the
// broker's stderr long after the launcher has returned.
func checkProfiles(cfg *config.Config) checkResult {
	r := checkResult{name: "profiles"}
	path := cfg.CommandProfilePath()
	r.details = append(r.details, "command profile: "+path)

	if _, err := os.Stat(path); err != nil {
		r.hint = "write the profile, or point command_profile at it"
		return r
	}
	if out, err := runCommand(context.Background(), "nono", "profile", "validate", path); err != nil {
		r.details = append(r.details, "nono profile validate: "+strings.TrimSpace(string(out)))
		r.hint = "fix the command profile until `nono profile validate` passes"
		return r
	}

	resolved, err := sandboxhost.Resolve(cfg, "claude")
	if err != nil {
		r.details = append(r.details, fmt.Sprintf("error: could not resolve the agent profile: %v", err))
		r.hint = "fix [sandbox.agent] until `agent-sandbox ai config-check` passes"
		return r
	}
	agentPath, cleanup, err := resolved.WriteProfile()
	if err != nil {
		r.details = append(r.details, fmt.Sprintf("error: could not write the agent profile: %v", err))
		r.hint = "could not validate the agent profile; fix the error above and re-run doctor"
		return r
	}
	defer cleanup()
	if out, err := runCommand(context.Background(), "nono", "profile", "validate", agentPath); err != nil {
		r.details = append(r.details, "agent profile: "+strings.TrimSpace(string(out)))
		r.hint = "the profile generated from [sandbox.agent] is not accepted by nono; " +
			"see `agent-sandbox debug` for the JSON it produced"
		return r
	}
	r.details = append(r.details, "agent profile: generated from [sandbox.agent], validated")

	self, err := selfPath()
	if err != nil {
		// Fail loudly rather than silently reporting OK: not knowing the
		// broker's own binary path means the writability check below never
		// ran, and that must not look like it passed.
		r.details = append(r.details, fmt.Sprintf("error: could not determine the broker binary's own path: %v", err))
		r.hint = "could not verify the broker binary is not writable through the command profile; " +
			"investigate why os.Executable() failed and re-run doctor"
		return r
	}
	writable, werr := profileAllowsWrite(path, self)
	if werr != nil {
		r.details = append(r.details, fmt.Sprintf("error: could not ask nono whether %s is writable: %v", self, werr))
		r.hint = "could not verify the broker binary is not writable through the command profile; " +
			"fix the error above and re-run doctor"
		return r
	}
	if writable {
		r.details = append(r.details, "broker binary: "+self)
		r.hint = "the command profile grants write access to the broker's own binary, so a command could " +
			"replace what the next launch runs; narrow the grant that covers it (`nono why --profile " +
			path + " --path " + self + " --op write` names it)"
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
func profileAllowsWrite(profilePath, binPath string) (bool, error) {
	out, err := runCommand(context.Background(), "nono", "why", "--json",
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
