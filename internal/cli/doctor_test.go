// internal/cli/doctor_test.go
package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/ynny-github/agent-sandbox/internal/claude"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/internal/launch"
)

// stubRunCommand overrides only runCommand, leaving lookPath untouched. It
// returns a restore function rather than registering a t.Cleanup so it can
// match the brief's `defer restore()` call sites.
func stubRunCommand(rc func(context.Context, string, ...string) ([]byte, error)) func() {
	orig := runCommand
	runCommand = rc
	return func() { runCommand = orig }
}

// configWithBothProfiles writes a config plus both profiles beside it.
func configWithBothProfiles(t *testing.T, dir string) profiles {
	t.Helper()
	for _, name := range []string{"command-profile.json", "claude-profile.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return profiles{agent: filepath.Join(dir, "claude-profile.json"), command: filepath.Join(dir, "command-profile.json")}
}

// useProfiles points doctor's profile flags at dir's two profiles.
func useProfiles(t *testing.T, dir string) {
	t.Helper()
	prevA, prevC := doctorAgentProfile, doctorCommandProfile
	doctorAgentProfile = filepath.Join(dir, "claude-profile.json")
	doctorCommandProfile = filepath.Join(dir, "command-profile.json")
	t.Cleanup(func() { doctorAgentProfile, doctorCommandProfile = prevA, prevC })
}

func stubAllSeamsOK(t *testing.T) {
	t.Helper()
	t.Cleanup(stubRunCommand(nonoAnswers(nil, "denied")))
	prev := probeHook
	probeHook = func(string, string) error { return nil }
	t.Cleanup(func() { probeHook = prev })
}

func TestDoctorCmd_Registered(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "doctor" {
			return
		}
	}
	t.Fatal("doctor command not registered on rootCmd")
}

func TestRenderResults_AllOK(t *testing.T) {
	var buf bytes.Buffer
	renderResults(&buf, []checkResult{
		{name: "alpha", ok: true, details: []string{"path: /usr/bin/alpha", "version: 1.0"}},
		{name: "beta", ok: true, details: []string{"info: hello"}},
	})
	want := "[OK] alpha\n" +
		"     path: /usr/bin/alpha\n" +
		"     version: 1.0\n" +
		"\n" +
		"[OK] beta\n" +
		"     info: hello\n" +
		"\n" +
		"doctor: all checks passed\n"
	if got := buf.String(); got != want {
		t.Errorf("output mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

// stubSelfPath pins agent-sandbox's own path, and execd's beside it: both
// binaries are checked, and a test environment has neither on PATH.
func stubSelfPath(path string) func() {
	prevSelf, prevExecd := selfPath, execdPath
	selfPath = func() (string, error) { return path, nil }
	execdPath = func() (string, error) { return filepath.Join(filepath.Dir(path), "execd"), nil }
	return func() { selfPath, execdPath = prevSelf, prevExecd }
}

// nonoWritable answers like nonoAnswers(nil, "denied") except that `nono why`
// reports "allowed" for exactly the path writable.
func nonoWritable(writable string) func(context.Context, string, ...string) ([]byte, error) {
	base := nonoAnswers(nil, "denied")
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "why" && slices.Contains(args, writable) {
			return []byte(`{"status":"allowed"}`), nil
		}
		return base(ctx, name, args...)
	}
}

// The agent profile must be validated too: it is now hand-written, so nono
// rejecting it is a launch failure doctor exists to catch first.
// A profile that does not forward the execd socket variable produces a
// session where every command fails for a reason nothing on screen explains.
// nono cannot report env grants, so doctor measures instead.
// Inside a session the probe would nest one nono sandbox in another. Skip it
// and say so, rather than reporting a failure the operator cannot act on.
// TestCheckProfiles_FailsWhenSelfPathErrors pins ruling R20: not knowing
// execd's own binary path means the writability check never ran, and
// that must be reported as NG with an explanatory detail, not silently
// treated as "the binary is not writable".
func TestCheckBinaries_FailsWhenSelfPathErrors(t *testing.T) {
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	restoreRun := stubRunCommand(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("valid"), nil
	})
	defer restoreRun()
	prevSelf := selfPath
	selfPath = func() (string, error) { return "", fmt.Errorf("os.Executable: not implemented on this platform") }
	defer func() { selfPath = prevSelf }()

	got := checkBinaries(context.Background(), cfg)
	if got.ok {
		t.Errorf("checkProfiles ok = true, want false when selfPath errors")
	}
	if got.hint == "" {
		t.Errorf("a failing check must carry a hint")
	}
	if !strings.Contains(strings.Join(got.details, "\n"), "could not determine agent-sandbox's own path") {
		t.Errorf("expected details to name what could not be determined, got %v", got.details)
	}
}

// TestCheckProfiles_FailsWhenTheWriteQueryErrors pins the ruling that an
// unanswerable writability query must not be silently treated as "not
// writable": doctor reports what it could not determine instead of passing.
func TestCheckBinaries_FailsWhenTheWriteQueryErrors(t *testing.T) {
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	// Every nono call answers with something unparseable as a why result, so
	// validate still passes and this exercises the query step alone.
	restoreRun := stubRunCommand(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Result: valid"), nil
	})
	defer restoreRun()
	restoreSelf := stubSelfPath(filepath.Join(t.TempDir(), "agent-sandbox"))
	defer restoreSelf()

	got := checkBinaries(context.Background(), cfg)
	if got.ok {
		t.Errorf("checkProfiles ok = true, want false when the writability query cannot be answered")
	}
	if got.hint == "" {
		t.Errorf("a failing check must carry a hint")
	}
	if !strings.Contains(strings.Join(got.details, "\n"), "could not ask nono whether") {
		t.Errorf("expected details to name what could not be determined, got %v", got.details)
	}
}

func TestRenderResults_AllNG(t *testing.T) {
	var buf bytes.Buffer
	renderResults(&buf, []checkResult{
		{name: "a", ok: false, details: []string{"error: x"}, hint: "fix x"},
		{name: "b", ok: false, details: []string{"error: y"}, hint: "fix y"},
	})
	want := "[NG] a\n" +
		"     error: x\n" +
		"     hint: fix x\n" +
		"\n" +
		"[NG] b\n" +
		"     error: y\n" +
		"     hint: fix y\n" +
		"\n" +
		"doctor: 2 of 2 checks failed\n"
	if got := buf.String(); got != want {
		t.Errorf("output mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

// nonoAnswers builds a runCommand stub covering the nono subcommands
// checkProfiles and checkProfilePaths drive: `nono profile validate` (both
// profiles), `nono profile show --json` (the command-policy paths query), and
// `nono why --json` (execd's binary's writability). validateErr, when
// non-nil, fails every validate; whyStatus is the status field the why answer
// carries.
//
// It answers `nono --version` too, so a test can hand this to stubNonoSeams and
// have checkNono pass on the same stub.
func nonoAnswers(validateErr error, whyStatus string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch {
		case len(args) > 0 && args[0] == "why":
			// The real nono prints warnings ahead of the JSON on the combined
			// stream; reproduce that so the parser's own tolerance is exercised
			// rather than assumed.
			return []byte("WARN something about a missing path\n{\"status\":\"" + whyStatus + "\"}\n"), nil
		case len(args) > 1 && args[0] == "profile" && args[1] == "show":
			// No command-policy paths pinned: checkProfilePaths should report ok
			// with nothing to check, not fail to parse a validate answer.
			return []byte(`{"command_policies":{"commands":{}}}`), nil
		case len(args) > 0 && args[0] == "profile":
			if validateErr != nil {
				return []byte("JSON syntax invalid"), validateErr
			}
			return []byte("Result: valid"), nil
		default:
			return []byte("nono 0.4.2\n"), nil
		}
	}
}

func TestCheckBinaries_FailsWhenNonoSaysTheBinaryIsWritable(t *testing.T) {
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	commandProfile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(commandProfile, []byte(`{"filesystem":{"allow":["`+dir+`"]}}`), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restore := stubRunCommand(nonoAnswers(nil, "allowed"))
	defer restore()
	restoreSelf := stubSelfPath(filepath.Join(dir, "agent-sandbox"))
	defer restoreSelf()

	got := checkBinaries(context.Background(), cfg)
	if got.ok {
		t.Fatal("checkProfiles ok = true, want false when nono reports execd's binary writable")
	}
	if !strings.Contains(got.hint, "nono why") {
		t.Errorf("hint = %q, want it to name the query that identifies the grant", got.hint)
	}
}

func TestProfileAllowsWrite_ReadsStatusPastWarnings(t *testing.T) {
	restore := stubRunCommand(nonoAnswers(nil, "allowed"))
	defer restore()
	allowed, err := claude.ProfileAllowsWrite(context.Background(), runCommand, "/p.json", "/bin/agent-sandbox")
	if err != nil {
		t.Fatalf("profileAllowsWrite: %v", err)
	}
	if !allowed {
		t.Error("allowed = false, want true: the JSON says allowed, behind a warning line")
	}
}

func TestProfileAllowsWrite_ErrorsOnUnparseableOutput(t *testing.T) {
	restore := stubRunCommand(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("no json here"), nil
	})
	defer restore()
	if _, err := claude.ProfileAllowsWrite(context.Background(), runCommand, "/p.json", "/bin/agent-sandbox"); err == nil {
		t.Error("profileAllowsWrite err = nil, want an error when nono prints no JSON")
	}
}

// A missing "executable" pin is the severe case nono itself does not soften:
// mediation for that command is disabled entirely, silently as far as
// execd's own behavior goes (it just runs the first PATH match at the
// session's grants), so this must be reported on any single miss.
// command_policies.executable_dirs is the other single-entry field: nono
// refuses to start the session at all if any one entry in it is missing, so
// — like "executable" above, and unlike exec_paths below — a single miss is
// enough to report NG.
// The whole point of a portable exec_paths candidate list (this repository's
// own git entry lists a /nix/store path and two non-NixOS candidates
// alongside it) is that nono skips a missing entry silently and the command
// still works as long as *one* entry in the set resolves. A doctor that goes
// NG here would be red on every healthy host that isn't NixOS, or isn't the
// exact host the profile was written on — exactly the false alarm this test
// guards against.
// The other half of the same rule: once every entry in a command's own
// exec_paths set is missing, that command genuinely has no reachable helper
// directory left, and this must be reported — naming the command, since
// "every one of git's exec_paths is gone" is a different, more useful
// sentence than "this one path is gone."
// `nono profile show --json` prints a "~/..." exec_paths entry back exactly
// as written in the profile, unexpanded — but nono itself expands "~" when it
// actually applies fs_read/fs_write/exec_paths at sandbox-apply time
// (measured, nono 0.74.0). Without expandHome, checkProfilePaths would stat
// the literal string "~/toolchain" and report a directory that genuinely
// exists as missing.
// The agent's shell is a wrapper the launcher generates and names in
// CLAUDE_CODE_SHELL. A profile that strips the variable leaves Claude on the
// host's bash, which sources a ~/.bashrc the profile no longer grants and
// prefixes every tool result with a permission error. Nothing on screen says
// the profile is the cause, so doctor measures it the same way it measures the
// execd socket variable.
// Neither fact is a defect for a session that never passes --context-mode, so
// the check must not fail on them.
// checkProfilesWith runs checkProfiles against a fresh pair of profiles with
// agent-sandbox at dir/agent-sandbox (and execd beside it), answering nono
// with the fake runFor builds for that dir.
func checkProfilesWith(t *testing.T, runFor func(dir string) func(context.Context, string, ...string) ([]byte, error)) (checkResult, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	t.Cleanup(stubRunCommand(runFor(dir)))
	t.Cleanup(stubSelfPath(filepath.Join(dir, "agent-sandbox")))
	return checkBinaries(context.Background(), cfg), dir
}

func TestCheckBinaries_FailsWhenExecdIsWritable(t *testing.T) {
	got, dir := checkProfilesWith(t, func(dir string) func(context.Context, string, ...string) ([]byte, error) {
		return nonoWritable(filepath.Join(dir, "execd"))
	})
	if got.ok {
		t.Fatal("checkProfiles ok = true, want false when the command profile can write execd")
	}
	if !strings.Contains(got.hint, filepath.Join(dir, "execd")) {
		t.Errorf("hint = %q, want it to name execd's path", got.hint)
	}
}

func TestCheckBinaries_FailsWhenAgentSandboxIsWritable(t *testing.T) {
	got, dir := checkProfilesWith(t, func(dir string) func(context.Context, string, ...string) ([]byte, error) {
		return nonoWritable(filepath.Join(dir, "agent-sandbox"))
	})
	if got.ok {
		t.Fatal("checkProfiles ok = true, want false when the command profile can write agent-sandbox")
	}
	if !strings.Contains(got.hint, filepath.Join(dir, "agent-sandbox")) {
		t.Errorf("hint = %q, want it to name agent-sandbox's path", got.hint)
	}
}

func TestCheckBinaries_FailsWhenExecdIsNotOnPath(t *testing.T) {
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	t.Cleanup(stubRunCommand(nonoAnswers(nil, "denied")))
	t.Cleanup(stubSelfPath(filepath.Join(dir, "agent-sandbox")))
	execdPath = func() (string, error) {
		return "", errors.New("execd not found in PATH; install it with " +
			"`go install github.com/ynny-github/agent-sandbox/cmd/execd@latest`")
	}

	got := checkBinaries(context.Background(), cfg)
	if got.ok {
		t.Fatal("checkProfiles ok = true, want false when execd is not on PATH")
	}
	if !strings.Contains(got.hint, "cmd/execd@latest") {
		t.Errorf("hint = %q, want the install command", got.hint)
	}
}

func TestDoctor_MissingProfilesPointAtInit(t *testing.T) {
	t.Chdir(t.TempDir())
	r := profileFilesExist(profiles{
		agent:   filepath.Join(".agent-sandbox", "claude-profile.json"),
		command: filepath.Join(".agent-sandbox", "command-profile.json"),
	})
	if r.ok || !strings.Contains(r.hint, "agent-sandbox init") {
		t.Errorf("result = %+v, want a failure pointing at agent-sandbox init", r)
	}
}

func TestCheckHook_ReportsAProbeFailure(t *testing.T) {
	prev := probeHook
	probeHook = func(string, string) error { return errors.New("hook did not run") }
	t.Cleanup(func() { probeHook = prev })
	defer stubSelfPath("/usr/local/bin/agent-sandbox")()
	if r := checkHook(profiles{agent: "a.json"}); r.ok || !strings.Contains(strings.Join(r.details, "\n"), "hook did not run") {
		t.Errorf("checkHook() = %+v, want the probe error", r)
	}
}

// Claude's own file tools run under the agent profile, not through the hook,
// so an agent profile that can write the approved copy could change what the
// next launch runs without any prompt.
func TestCheckApprovedCopy_FailsWhenTheAgentProfileCanWriteIt(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	wd, _ := os.Getwd()
	trusted, err := launch.TrustedDir(wd)
	if err != nil {
		t.Fatal(err)
	}
	defer stubRunCommand(nonoWritable(trusted))()
	r := checkApprovedCopy(context.Background(), profiles{agent: "a.json", command: "c.json"})
	if r.ok || !strings.Contains(r.hint, trusted) {
		t.Errorf("checkApprovedCopy() = %+v, want a failure naming %s", r, trusted)
	}
}

func TestCheckApprovedCopy_PassesWhenDenied(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer stubRunCommand(nonoAnswers(nil, "denied"))()
	if r := checkApprovedCopy(context.Background(), profiles{agent: "a.json", command: "c.json"}); !r.ok {
		t.Errorf("checkApprovedCopy() = %+v, want ok", r)
	}
}

// The hook is `execd hook claude`, so the probe must run the execd binary
// doctor resolved, not agent-sandbox.
func TestCheckHook_ProbesTheExecdBinary(t *testing.T) {
	defer stubSelfPath("/opt/bin/agent-sandbox")()
	var probed string
	prev := probeHook
	probeHook = func(_ string, bin string) error { probed = bin; return nil }
	t.Cleanup(func() { probeHook = prev })
	if r := checkHook(profiles{agent: "a.json"}); !r.ok {
		t.Fatalf("checkHook() = %+v", r)
	}
	if probed != "/opt/bin/execd" {
		t.Errorf("probed %q, want the execd binary /opt/bin/execd", probed)
	}
}

// doctor keeps only the checks that guard a launch; diagnostics went away.
func TestRunDoctor_RunsOnlyTheSafetyChecks(t *testing.T) {
	stubAllSeamsOK(t)
	dir := t.TempDir()
	configWithBothProfiles(t, dir)
	useProfiles(t, dir)
	defer stubSelfPath(filepath.Join(t.TempDir(), "agent-sandbox"))()
	t.Chdir(t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var buf bytes.Buffer
	doctorCmd.SetOut(&buf)
	t.Cleanup(func() { doctorCmd.SetOut(nil) })
	if err := runDoctor(doctorCmd, nil); err != nil {
		t.Fatalf("runDoctor: %v\n%s", err, buf.String())
	}
	out := buf.String()
	for _, want := range []string{"[OK] hook", "[OK] binaries", "[OK] approved copy"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	for _, gone := range []string{"] nono", "tool-sandbox", "exec daemon", "profile paths", "context-mode"} {
		if strings.Contains(out, gone) {
			t.Errorf("output still has the diagnostic %q:\n%s", gone, out)
		}
	}
}
