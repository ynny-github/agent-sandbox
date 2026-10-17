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
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/internal/execd"
)

// shortStateDir returns a fresh, short-named temp directory suitable for
// XDG_STATE_HOME in tests that actually bind a unix socket underneath it.
// t.TempDir() embeds the (potentially long) test name in the path, which on
// macOS can push a unix socket path past the ~104-byte sun_path limit and
// make bind(2) fail with "invalid argument" — see
// internal/execd/server_test.go's startTestServer for the same workaround.
func shortStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "asdoc")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func stubNonoSeams(t *testing.T, lp func(string) (string, error), rc func(context.Context, string, ...string) ([]byte, error)) {
	t.Helper()
	origLookPath := lookPath
	origRun := runCommand
	lookPath = lp
	runCommand = rc
	t.Cleanup(func() {
		lookPath = origLookPath
		runCommand = origRun
	})
}

// stubRunCommand overrides only runCommand, leaving lookPath untouched. It
// returns a restore function rather than registering a t.Cleanup so it can
// match the brief's `defer restore()` call sites.
func stubRunCommand(rc func(context.Context, string, ...string) ([]byte, error)) func() {
	orig := runCommand
	runCommand = rc
	return func() { runCommand = orig }
}

func stubRunCommandEnv(rc func(context.Context, []string, string, ...string) ([]byte, error)) func() {
	orig := runCommandEnv
	runCommandEnv = rc
	return func() { runCommandEnv = orig }
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

func TestCheckNono_NotInPath(t *testing.T) {
	stubNonoSeams(t,
		func(string) (string, error) { return "", errors.New("not found") },
		func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	)

	r := checkNono(context.Background())
	if r.ok {
		t.Fatal("expected NG, got OK")
	}
	if r.hint == "" {
		t.Error("expected hint to be set on NG")
	}
}

func TestCheckNono_VersionFails(t *testing.T) {
	stubNonoSeams(t,
		func(string) (string, error) { return "/usr/bin/nono", nil },
		func(context.Context, string, ...string) ([]byte, error) {
			return []byte("boom"), errors.New("exit status 1")
		},
	)

	r := checkNono(context.Background())
	if r.ok {
		t.Fatal("expected NG when --version fails")
	}
}

func stubAllSeamsOK(t *testing.T) {
	t.Helper()
	stubNonoSeams(t,
		func(name string) (string, error) {
			if name == "nono" {
				return "/usr/bin/nono", nil
			}
			// Anything else — specifically toolSandboxProbeCommand ("true"),
			// which checkToolSandbox looks up to probe with — resolves as if
			// it were on PATH too, so this helper still stubs every seam a
			// fully-OK doctor run touches.
			return "/bin/" + name, nil
		},
		nonoAnswers(nil, "denied"),
	)
	t.Setenv("XDG_STATE_HOME", shortStateDir(t))
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

func TestRunDoctor_AllOK(t *testing.T) {
	stubAllSeamsOK(t)

	dir := t.TempDir()
	configWithBothProfiles(t, dir)
	useProfiles(t, dir)
	// The execd binary in a `go test` run is a temp binary outside dir, so it
	// is never covered by the profile's filesystem.allow above.
	restoreSelf := stubSelfPath(filepath.Join(t.TempDir(), "agent-sandbox"))
	defer restoreSelf()
	restoreEnv := stubRunCommandEnv(func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte(envProbeValue), nil
	})
	defer restoreEnv()

	var buf bytes.Buffer
	doctorCmd.SetOut(&buf)
	t.Cleanup(func() { doctorCmd.SetOut(nil) })

	if err := runDoctor(doctorCmd, nil); err != nil {
		t.Fatalf("expected nil error, got %v: output:\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "doctor: all checks passed") {
		t.Errorf("missing summary in output:\n%s", buf.String())
	}
}

func TestRunDoctor_NonoNG(t *testing.T) {
	stubNonoSeams(t,
		func(string) (string, error) { return "", errors.New("not found") },
		func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	dir := t.TempDir()
	configWithBothProfiles(t, dir)
	useProfiles(t, dir)
	restoreSelf := stubSelfPath(filepath.Join(t.TempDir(), "agent-sandbox"))
	defer restoreSelf()
	restoreEnv := stubRunCommandEnv(func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte(envProbeValue), nil
	})
	defer restoreEnv()

	var buf bytes.Buffer
	doctorCmd.SetOut(&buf)
	t.Cleanup(func() { doctorCmd.SetOut(nil) })

	err := runDoctor(doctorCmd, nil)
	if !errors.Is(err, errDoctorChecksFailed) {
		t.Fatalf("expected errDoctorChecksFailed, got %v", err)
	}
	if !strings.Contains(buf.String(), "checks failed") {
		t.Errorf("missing failure summary:\n%s", buf.String())
	}
}

func TestRunDoctor_RunsAllChecksEvenOnEarlyFailure(t *testing.T) {
	stubNonoSeams(t,
		func(string) (string, error) { return "", errors.New("not found") },
		func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	dir := t.TempDir()
	configWithBothProfiles(t, dir)
	useProfiles(t, dir)
	restoreSelf := stubSelfPath(filepath.Join(t.TempDir(), "agent-sandbox"))
	defer restoreSelf()
	restoreEnv := stubRunCommandEnv(func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte(envProbeValue), nil
	})
	defer restoreEnv()

	var buf bytes.Buffer
	doctorCmd.SetOut(&buf)
	t.Cleanup(func() { doctorCmd.SetOut(nil) })

	_ = runDoctor(doctorCmd, nil)
	for _, want := range []string{"nono", "exec daemon"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing section %q:\n%s", want, buf.String())
		}
	}
}

func TestCheckNono_OK(t *testing.T) {
	stubNonoSeams(t,
		func(string) (string, error) { return "/usr/bin/nono", nil },
		func(context.Context, string, ...string) ([]byte, error) {
			return []byte("nono 0.4.2\n"), nil
		},
	)

	r := checkNono(context.Background())
	if !r.ok {
		t.Fatal("expected OK")
	}
	joined := strings.Join(r.details, "\n")
	if !strings.Contains(joined, "path: /usr/bin/nono") {
		t.Errorf("details missing path: %v", r.details)
	}
	if !strings.Contains(joined, "version: nono 0.4.2") {
		t.Errorf("details missing version: %v", r.details)
	}
}

func TestCheckExecdSocketDir_OK(t *testing.T) {
	base := shortStateDir(t)
	t.Setenv("XDG_STATE_HOME", base)

	r := checkExecdSocketDir()
	if !r.ok {
		t.Fatalf("expected OK, got NG: %v", r.details)
	}
	want := filepath.Join(base, "execd")
	joined := strings.Join(r.details, "\n")
	if !strings.Contains(joined, want) {
		t.Errorf("details missing socket dir %q: %v", want, r.details)
	}
}

func TestCheckExecdSocketDir_UnwritableFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	base := t.TempDir()
	// Make base read-only so MkdirAll underneath it fails.
	if err := os.Chmod(base, 0o500); err != nil {
		t.Skipf("cannot make dir read-only in this environment: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(base, 0o700) })
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))

	r := checkExecdSocketDir()
	if r.ok {
		t.Fatal("expected NG when the socket dir cannot be created")
	}
	if r.hint == "" {
		t.Error("expected hint on NG")
	}
}

func TestCheckExecdSocketDir_ExistingDirUnwritableFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	base := t.TempDir()
	dir := filepath.Join(base, "execd")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// os.MkdirAll returns nil for a directory that already exists, whatever
	// its mode, so this pins that checkExecdSocketDir does not stop there:
	// it must also fail to bind a socket inside dir and report NG.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("cannot make dir read-only in this environment: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	t.Setenv("XDG_STATE_HOME", base)

	r := checkExecdSocketDir()
	if r.ok {
		t.Fatal("expected NG when the existing socket dir cannot bind a socket")
	}
	if r.hint == "" {
		t.Error("expected hint on NG")
	}
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

func TestRenderResults_Mixed(t *testing.T) {
	var buf bytes.Buffer
	renderResults(&buf, []checkResult{
		{name: "alpha", ok: true, details: []string{"path: /usr/bin/alpha"}},
		{name: "beta", ok: false, details: []string{"error: something went wrong"}, hint: "try X"},
		{name: "gamma", ok: false, details: []string{"error: daemon down"}, hint: "start daemon"},
	})
	want := "[OK] alpha\n" +
		"     path: /usr/bin/alpha\n" +
		"\n" +
		"[NG] beta\n" +
		"     error: something went wrong\n" +
		"     hint: try X\n" +
		"\n" +
		"[NG] gamma\n" +
		"     error: daemon down\n" +
		"     hint: start daemon\n" +
		"\n" +
		"doctor: 2 of 3 checks failed\n"
	if got := buf.String(); got != want {
		t.Errorf("output mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func configWithProfile(t *testing.T, dir, profile string) profiles {
	t.Helper()
	return profiles{agent: filepath.Join(dir, "claude-profile.json"), command: profile}
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
func TestCheckProfiles_ValidatesTheAgentProfile(t *testing.T) {
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	var validated []string
	restoreRun := stubRunCommand(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "nono" && len(args) == 3 && args[1] == "validate" {
			validated = append(validated, args[2])
		}
		return []byte(`{"status":"denied"}`), nil
	})
	defer restoreRun()
	restoreEnv := stubRunCommandEnv(func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte(envProbeValue), nil
	})
	defer restoreEnv()
	defer stubSelfPath("/usr/local/bin/agent-sandbox")()

	got := checkProfiles(context.Background(), cfg)
	if !got.ok {
		t.Fatalf("checkProfiles ok = false, want true; details %v hint %q", got.details, got.hint)
	}
	if !slices.Contains(validated, filepath.Join(dir, "claude-profile.json")) {
		t.Errorf("the agent profile was not validated; validated %v", validated)
	}
}

// A profile that does not forward the execd socket variable produces a
// session where every command fails for a reason nothing on screen explains.
// nono cannot report env grants, so doctor measures instead.
func TestCheckProfiles_FailsWhenTheSocketVarIsNotForwarded(t *testing.T) {
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	restoreRun := stubRunCommand(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"status":"denied"}`), nil
	})
	defer restoreRun()
	restoreEnv := stubRunCommandEnv(func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte("\n"), nil
	})
	defer restoreEnv()
	defer stubSelfPath("/usr/local/bin/agent-sandbox")()

	got := checkProfiles(context.Background(), cfg)
	if got.ok {
		t.Error("checkProfiles ok = true, want false when the socket variable is stripped")
	}
	if !strings.Contains(strings.Join(got.details, "\n"), execd.SocketEnvVar) {
		t.Errorf("details must name the variable: %v", got.details)
	}
	if got.hint == "" {
		t.Error("a failing check must carry a hint")
	}
}

// Inside a session the probe would nest one nono sandbox in another. Skip it
// and say so, rather than reporting a failure the operator cannot act on.
func TestCheckProfiles_SkipsTheProbeInsideASession(t *testing.T) {
	t.Setenv(execd.SocketEnvVar, "/tmp/some-execd.sock")
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	restoreRun := stubRunCommand(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"status":"denied"}`), nil
	})
	defer restoreRun()
	probed := false
	restoreEnv := stubRunCommandEnv(func(context.Context, []string, string, ...string) ([]byte, error) {
		probed = true
		return nil, nil
	})
	defer restoreEnv()
	defer stubSelfPath("/usr/local/bin/agent-sandbox")()

	got := checkProfiles(context.Background(), cfg)
	if probed {
		t.Error("the probe must not run inside a session")
	}
	if !got.ok {
		t.Errorf("a skipped probe is not a failure; details %v hint %q", got.details, got.hint)
	}
	if !strings.Contains(strings.Join(got.details, "\n"), "skipped") {
		t.Errorf("details must say the probe was skipped: %v", got.details)
	}
}

func TestCheckProfiles_FailsWhenValidateRejects(t *testing.T) {
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	restore := stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("JSON syntax invalid"), fmt.Errorf("exit status 1")
	})
	defer restore()
	restoreEnv := stubRunCommandEnv(func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte(envProbeValue), nil
	})
	defer restoreEnv()

	got := checkProfiles(context.Background(), cfg)
	if got.ok {
		t.Errorf("checkProfiles ok = true, want false for a profile nono rejects")
	}
	if got.hint == "" {
		t.Errorf("a failing check must carry a hint")
	}
}

func TestCheckProfiles_FailsWhenTheFileIsMissing(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(present, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	agentProfile := filepath.Join(dir, "claude-profile.json")
	if err := os.WriteFile(agentProfile, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write claude profile: %v", err)
	}
	// The agent profile exists and must validate cleanly so the failure this
	// test pins is unambiguously the command profile's. Stub runCommand so no
	// real nono process is invoked for that agent-profile validate call: the
	// checkProfiles path here (execd socket probe, self path, writability
	// query) never runs, because command profile validation fails on its
	// missing-file os.Stat before any of that, but the agent-profile validate
	// call above it does reach runCommand.
	var validated []string
	restoreRun := stubRunCommand(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) == 3 && args[1] == "validate" {
			validated = append(validated, args[2])
		}
		return []byte("Result: valid"), nil
	})
	defer restoreRun()

	// runDoctor stops earlier on a missing profile (profileFilesExist); this
	// drives checkProfiles directly to pin its own defensive os.Stat.
	cfg := configWithProfile(t, dir, present)
	if err := os.Remove(present); err != nil {
		t.Fatalf("remove profile: %v", err)
	}

	got := checkProfiles(context.Background(), cfg)
	if got.ok {
		t.Errorf("checkProfiles ok = true, want false when the profile file is missing")
	}
	if got.hint == "" {
		t.Errorf("a failing check must carry a hint")
	}
	joined := strings.Join(got.details, "\n")
	if !strings.Contains(joined, "command profile: not found: "+present) {
		t.Errorf("expected the failure to name the missing command profile %q, got details %v", present, got.details)
	}
	if strings.Contains(joined, "agent profile: not found") {
		t.Errorf("the agent profile exists and should validate cleanly, got details %v", got.details)
	}
	if !slices.Contains(validated, agentProfile) {
		t.Errorf("expected the agent profile to be validated through the stub, got %v", validated)
	}
	if slices.Contains(validated, present) {
		t.Errorf("the command profile is missing, so its os.Stat failure must short-circuit before any nono call: got %v", validated)
	}
}

// TestCheckProfiles_FailsWhenSelfPathErrors pins ruling R20: not knowing
// execd's own binary path means the writability check never ran, and
// that must be reported as NG with an explanatory detail, not silently
// treated as "the binary is not writable".
func TestCheckProfiles_FailsWhenSelfPathErrors(t *testing.T) {
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	restoreRun := stubRunCommand(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("valid"), nil
	})
	defer restoreRun()
	restoreEnv := stubRunCommandEnv(func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte(envProbeValue), nil
	})
	defer restoreEnv()
	prevSelf := selfPath
	selfPath = func() (string, error) { return "", fmt.Errorf("os.Executable: not implemented on this platform") }
	defer func() { selfPath = prevSelf }()

	got := checkProfiles(context.Background(), cfg)
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
func TestCheckProfiles_FailsWhenTheWriteQueryErrors(t *testing.T) {
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	// Every nono call answers with something unparseable as a why result, so
	// validate still passes and this exercises the query step alone.
	restoreRun := stubRunCommand(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Result: valid"), nil
	})
	defer restoreRun()
	restoreEnv := stubRunCommandEnv(func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte(envProbeValue), nil
	})
	defer restoreEnv()
	restoreSelf := stubSelfPath(filepath.Join(t.TempDir(), "agent-sandbox"))
	defer restoreSelf()

	got := checkProfiles(context.Background(), cfg)
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

func TestCheckProfiles_FailsWhenTheAgentProfileIsRejected(t *testing.T) {
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	commandProfile := filepath.Join(dir, "command-profile.json")
	restore := stubRunCommand(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 2 && args[0] == "profile" && args[1] == "validate" && args[2] != commandProfile {
			return []byte("Group 'nope' not found in policy.json"), fmt.Errorf("exit status 1")
		}
		return []byte("Result: valid"), nil
	})
	defer restore()
	restoreEnv := stubRunCommandEnv(func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte(envProbeValue), nil
	})
	defer restoreEnv()
	restoreSelf := stubSelfPath(filepath.Join(t.TempDir(), "agent-sandbox"))
	defer restoreSelf()

	got := checkProfiles(context.Background(), cfg)
	if got.ok {
		t.Fatal("checkProfiles ok = true, want false when nono rejects the agent profile")
	}
	if !strings.Contains(got.hint, cfg.agent) {
		t.Errorf("hint = %q, want it to name the rejected profile %s", got.hint, cfg.agent)
	}
}

func TestCheckProfiles_FailsWhenNonoSaysTheBinaryIsWritable(t *testing.T) {
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	commandProfile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(commandProfile, []byte(`{"filesystem":{"allow":["`+dir+`"]}}`), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restore := stubRunCommand(nonoAnswers(nil, "allowed"))
	defer restore()
	restoreEnv := stubRunCommandEnv(func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte(envProbeValue), nil
	})
	defer restoreEnv()
	restoreSelf := stubSelfPath(filepath.Join(dir, "agent-sandbox"))
	defer restoreSelf()

	got := checkProfiles(context.Background(), cfg)
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

func TestCheckToolSandbox_NonoMissingFails(t *testing.T) {
	stubNonoSeams(t,
		func(name string) (string, error) {
			if name == "nono" {
				return "", errors.New(`exec: "nono": executable file not found in $PATH`)
			}
			return "/bin/" + name, nil
		},
		func(ctx context.Context, name string, args ...string) ([]byte, error) {
			t.Fatalf("runCommand called with nono missing: %s %v", name, args)
			return nil, nil
		},
	)

	r := checkToolSandbox(context.Background())
	if r.ok {
		t.Fatal("checkToolSandbox reported ok with nono missing")
	}
	if r.hint == "" {
		t.Error("checkToolSandbox gave no hint on failure")
	}
}

func TestCheckToolSandbox_NonoRunFailureIsReportedWithAHint(t *testing.T) {
	stubNonoSeams(t,
		func(name string) (string, error) { return "/bin/" + name, nil },
		func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte("nono: resolve_shared_library failed"), errors.New("exit status 1")
		},
	)

	r := checkToolSandbox(context.Background())
	if r.ok {
		t.Fatal("checkToolSandbox reported ok when nono run failed")
	}
	if !strings.Contains(r.hint, "tool-sandbox") {
		t.Errorf("hint does not name tool-sandbox: %q", r.hint)
	}
}

const profileShowJSON = `{
  "name": "test",
  "command_policies": {
    "commands": {
      "git": {
        "executable": null,
        "from": { "session": { "sandbox": {
          "exec_paths": ["/does/not/exist/libexec/git-core", "%s"]
        } } }
      },
      "ssh": { "executable": "/also/missing/bin/ssh", "from": {} }
    }
  }
}`

func TestCommandPolicyPaths_CollectsExecutablesAndExecPaths(t *testing.T) {
	defer stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(fmt.Sprintf(profileShowJSON, "/tmp")), nil
	})()

	got, err := commandPolicyPaths(context.Background(), "irrelevant.json")
	if err != nil {
		t.Fatal(err)
	}
	want := pinnedPaths{
		Executables: []pinnedPath{{Command: "ssh", Path: "/also/missing/bin/ssh"}},
		ExecPathSets: []execPathSet{
			{Label: "git (from.session)", Paths: []string{"/does/not/exist/libexec/git-core", "/tmp"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commandPolicyPaths = %+v, want %+v", got, want)
	}
}

const profileShowExecutableDirsJSON = `{
  "command_policies": {
    "executable_dirs": ["/from/executable_dirs"],
    "commands": {
      "git": {
        "executable": null,
        "from": { "session": { "sandbox": {
          "exec_paths": ["/does/not/exist/libexec/git-core"]
        } } }
      }
    }
  }
}`

func TestCommandPolicyPaths_IncludesExecutableDirs(t *testing.T) {
	defer stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(profileShowExecutableDirsJSON), nil
	})()

	got, err := commandPolicyPaths(context.Background(), "irrelevant.json")
	if err != nil {
		t.Fatal(err)
	}
	want := pinnedPaths{
		ExecutableDirs: []string{"/from/executable_dirs"},
		ExecPathSets: []execPathSet{
			{Label: "git (from.session)", Paths: []string{"/does/not/exist/libexec/git-core"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commandPolicyPaths = %+v, want %+v (command_policies.executable_dirs must not be dropped)", got, want)
	}
}

// nono's CommandFromConfig is an untagged three-variant enum: a "from" edge's
// value may be a bare policy string (Deny), an object wrapping the sandbox
// under a nested "sandbox" key (Edge), or a sandbox object used directly as
// the edge value with no wrapper at all (Policy). This profile carries all
// three at once, on the same command, so the test pins the whole contract
// rather than only whichever shape a prior fix happened to add.
//
// The Policy shape is the one that regressed silently: decoding it into
// {Sandbox commandPolicySandbox `json:"sandbox"`} finds no "sandbox" key,
// succeeds anyway with a zero-value sandbox, and drops exec_paths with no
// error — the exact failure this check exists to prevent, reproduced in the
// parser itself.
const profileShowThreeEdgeShapesJSON = `{
  "command_policies": {
    "commands": {
      "widget": {
        "executable": null,
        "from": {
          "session": "deny",
          "wrapped_caller": { "sandbox": { "exec_paths": ["/from/wrapped/edge"] } },
          "bare_caller": { "exec_paths": ["/from/bare/policy"] }
        }
      }
    }
  }
}`

func TestCommandPolicyPaths_ReadsAllThreeFromEdgeShapes(t *testing.T) {
	defer stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(profileShowThreeEdgeShapesJSON), nil
	})()

	got, err := commandPolicyPaths(context.Background(), "irrelevant.json")
	if err != nil {
		t.Fatal(err)
	}
	// "session": "deny" carries no sandbox and so no exec_paths — it must not
	// appear as a (spurious, empty) set. The other two callers are sorted
	// alphabetically ("bare_caller" before "wrapped_caller"), same as
	// commandPolicyPaths sorts every caller name before use.
	want := pinnedPaths{
		ExecPathSets: []execPathSet{
			{Label: "widget (from.bare_caller)", Paths: []string{"/from/bare/policy"}},
			{Label: "widget (from.wrapped_caller)", Paths: []string{"/from/wrapped/edge"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commandPolicyPaths = %+v, want %+v (the bare-sandbox \"from\" shape must not be dropped)", got, want)
	}
}

// A missing "executable" pin is the severe case nono itself does not soften:
// mediation for that command is disabled entirely, silently as far as
// execd's own behavior goes (it just runs the first PATH match at the
// session's grants), so this must be reported on any single miss.
func TestCheckProfilePaths_MissingExecutableIsNG(t *testing.T) {
	defer stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(`{"command_policies":{"commands":{
		  "ssh": {"executable": "/also/missing/bin/ssh", "from": {}}
		}}}`), nil
	})()

	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	r := checkProfilePaths(context.Background(), cfg)

	if r.ok {
		t.Fatal("checkProfilePaths reported ok with a missing executable pin")
	}
	joined := strings.Join(r.details, "\n")
	if !strings.Contains(joined, "/also/missing/bin/ssh") {
		t.Errorf("details do not name the missing executable pin:\n%s", joined)
	}
}

// command_policies.executable_dirs is the other single-entry field: nono
// refuses to start the session at all if any one entry in it is missing, so
// — like "executable" above, and unlike exec_paths below — a single miss is
// enough to report NG.
func TestCheckProfilePaths_MissingExecutableDirsEntryIsNG(t *testing.T) {
	defer stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(`{"command_policies":{
		  "executable_dirs": ["/does/not/exist/from/executable_dirs"],
		  "commands": {}
		}}`), nil
	})()

	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	r := checkProfilePaths(context.Background(), cfg)

	if r.ok {
		t.Fatal("checkProfilePaths reported ok with a missing executable_dirs entry")
	}
	joined := strings.Join(r.details, "\n")
	if !strings.Contains(joined, "/does/not/exist/from/executable_dirs") {
		t.Errorf("details do not name the missing executable_dirs entry:\n%s", joined)
	}
}

// The whole point of a portable exec_paths candidate list (this repository's
// own git entry lists a /nix/store path and two non-NixOS candidates
// alongside it) is that nono skips a missing entry silently and the command
// still works as long as *one* entry in the set resolves. A doctor that goes
// NG here would be red on every healthy host that isn't NixOS, or isn't the
// exact host the profile was written on — exactly the false alarm this test
// guards against.
func TestCheckProfilePaths_OKWhenOneOfSeveralExecPathsResolves(t *testing.T) {
	present := t.TempDir()
	defer stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"command_policies":{"commands":{"git":{"executable":null,
		  "from":{"session":{"sandbox":{"exec_paths":[
		    "/does/not/exist/a", %q, "/does/not/exist/b"
		  ]}}}}}}}`, present)), nil
	})()

	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	r := checkProfilePaths(context.Background(), cfg)
	if !r.ok {
		t.Errorf("checkProfilePaths not ok with one of three exec_paths entries present: %+v", r)
	}
}

// The other half of the same rule: once every entry in a command's own
// exec_paths set is missing, that command genuinely has no reachable helper
// directory left, and this must be reported — naming the command, since
// "every one of git's exec_paths is gone" is a different, more useful
// sentence than "this one path is gone."
func TestCheckProfilePaths_NGWhenEveryExecPathIsMissing(t *testing.T) {
	defer stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(`{"command_policies":{"commands":{"git":{"executable":null,
		  "from":{"session":{"sandbox":{"exec_paths":[
		    "/does/not/exist/a", "/does/not/exist/b"
		  ]}}}}}}}`), nil
	})()

	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	r := checkProfilePaths(context.Background(), cfg)

	if r.ok {
		t.Fatal("checkProfilePaths reported ok with every exec_paths entry missing")
	}
	joined := strings.Join(r.details, "\n")
	if !strings.Contains(joined, "git (from.session)") {
		t.Errorf("details do not name the command whose exec_paths are all missing:\n%s", joined)
	}
}

func TestCheckProfilePaths_OKWhenEveryPathExists(t *testing.T) {
	dir := t.TempDir()
	defer stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"command_policies":{"commands":{"git":{"executable":null,
		  "from":{"session":{"sandbox":{"exec_paths":[%q]}}}}}}}`, dir)), nil
	})()

	cfg := configWithBothProfiles(t, dir)
	if r := checkProfilePaths(context.Background(), cfg); !r.ok {
		t.Errorf("checkProfilePaths not ok with every path present: %+v", r)
	}
}

// `nono profile show --json` prints a "~/..." exec_paths entry back exactly
// as written in the profile, unexpanded — but nono itself expands "~" when it
// actually applies fs_read/fs_write/exec_paths at sandbox-apply time
// (measured, nono 0.74.0). Without expandHome, checkProfilePaths would stat
// the literal string "~/toolchain" and report a directory that genuinely
// exists as missing.
func TestCheckProfilePaths_ExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, "toolchain"), 0o755); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	defer stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(`{"command_policies":{"commands":{"git":{"executable":null,
		  "from":{"session":{"sandbox":{"exec_paths":["~/toolchain"]}}}}}}}`), nil
	})()

	cfg := configWithBothProfiles(t, dir)
	if r := checkProfilePaths(context.Background(), cfg); !r.ok {
		t.Errorf("checkProfilePaths not ok with a \"~\"-prefixed path that exists: %+v", r)
	}
}

// The agent's shell is a wrapper the launcher generates and names in
// CLAUDE_CODE_SHELL. A profile that strips the variable leaves Claude on the
// host's bash, which sources a ~/.bashrc the profile no longer grants and
// prefixes every tool result with a permission error. Nothing on screen says
// the profile is the cause, so doctor measures it the same way it measures the
// execd socket variable.
func TestCheckProfiles_FailsWhenTheShellVarIsNotForwarded(t *testing.T) {
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	restoreRun := stubRunCommand(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"status":"denied"}`), nil
	})
	defer restoreRun()
	restoreEnv := stubRunCommandEnv(func(_ context.Context, env []string, _ string, _ ...string) ([]byte, error) {
		for _, kv := range env {
			if strings.HasPrefix(kv, claude.ShellEnvVar+"=") {
				return []byte("\n"), nil // stripped by the profile
			}
		}
		return []byte(envProbeValue), nil
	})
	defer restoreEnv()
	defer stubSelfPath("/usr/local/bin/agent-sandbox")()

	got := checkProfiles(context.Background(), cfg)

	if got.ok {
		t.Error("checkProfiles ok = true, want false when the shell variable is stripped")
	}
	if !strings.Contains(strings.Join(got.details, "\n"), claude.ShellEnvVar) {
		t.Errorf("details must name the variable: %v", got.details)
	}
	if got.hint == "" {
		t.Error("a stripped shell variable must come with a hint")
	}
}

func TestCheckProfiles_ReportsContextModeReady(t *testing.T) {
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	restoreRun := stubRunCommand(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "claude" {
			return []byte(`[{"id":"context-mode@context-mode","enabled":true}]`), nil
		}
		return []byte(`{"status":"denied"}`), nil
	})
	defer restoreRun()
	restoreEnv := stubRunCommandEnv(func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte(envProbeValue), nil
	})
	defer restoreEnv()
	defer stubSelfPath("/usr/local/bin/agent-sandbox")()

	got := checkProfiles(context.Background(), cfg)
	if !got.ok {
		t.Fatalf("ok = false, want true; details %v hint %q", got.details, got.hint)
	}
	joined := strings.Join(got.details, "\n")
	if !strings.Contains(joined, "context-mode: plugin enabled, backend var forwarded") {
		t.Errorf("details must report context-mode readiness: %v", got.details)
	}
	if strings.Contains(joined, "needed only for --context-mode") {
		t.Errorf("the fully-ready case must not carry the optional-gap suffix: %v", got.details)
	}
}

// Neither fact is a defect for a session that never passes --context-mode, so
// the check must not fail on them.
func TestCheckProfiles_ContextModeNotReadyDoesNotFail(t *testing.T) {
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	restoreRun := stubRunCommand(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "claude" {
			return []byte(`[]`), nil
		}
		return []byte(`{"status":"denied"}`), nil
	})
	defer restoreRun()
	restoreEnv := stubRunCommandEnv(func(_ context.Context, env []string, _ string, _ ...string) ([]byte, error) {
		// Forward everything except the context-mode variable.
		for _, e := range env {
			if strings.HasPrefix(e, claude.ContextModeEnvVar+"=") {
				return []byte("\n"), nil
			}
		}
		return []byte(envProbeValue), nil
	})
	defer restoreEnv()
	defer stubSelfPath("/usr/local/bin/agent-sandbox")()

	got := checkProfiles(context.Background(), cfg)
	if !got.ok {
		t.Fatalf("ok = false, want true: context-mode readiness is optional; details %v", got.details)
	}
	joined := strings.Join(got.details, "\n")
	if !strings.Contains(joined, "context-mode: plugin not enabled, backend var not forwarded") {
		t.Errorf("details must report both gaps: %v", got.details)
	}
	if !strings.Contains(joined, "needed only for --context-mode") {
		t.Errorf("details must say the gaps are optional: %v", got.details)
	}
}

func TestCheckProfiles_SkipsContextModeInsideASession(t *testing.T) {
	t.Setenv(execd.SocketEnvVar, "/tmp/some-execd.sock")
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	restoreRun := stubRunCommand(func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "claude" {
			t.Error("doctor must not query plugins from inside a session")
		}
		return []byte(`{"status":"denied"}`), nil
	})
	defer restoreRun()
	defer stubSelfPath("/usr/local/bin/agent-sandbox")()

	got := checkProfiles(context.Background(), cfg)
	if !strings.Contains(strings.Join(got.details, "\n"), "context-mode: skipped") {
		t.Errorf("details must say the context-mode facts were skipped: %v", got.details)
	}
}

// checkProfilesWith runs checkProfiles against a fresh pair of profiles with
// agent-sandbox at dir/agent-sandbox (and execd beside it), answering nono
// with the fake runFor builds for that dir.
func checkProfilesWith(t *testing.T, runFor func(dir string) func(context.Context, string, ...string) ([]byte, error)) (checkResult, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	t.Cleanup(stubRunCommand(runFor(dir)))
	t.Cleanup(stubRunCommandEnv(func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte(envProbeValue), nil
	}))
	t.Cleanup(stubSelfPath(filepath.Join(dir, "agent-sandbox")))
	return checkProfiles(context.Background(), cfg), dir
}

func TestCheckProfiles_FailsWhenExecdIsWritable(t *testing.T) {
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

func TestCheckProfiles_FailsWhenAgentSandboxIsWritable(t *testing.T) {
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

func TestCheckProfiles_FailsWhenExecdIsNotOnPath(t *testing.T) {
	dir := t.TempDir()
	cfg := configWithBothProfiles(t, dir)
	t.Cleanup(stubRunCommand(nonoAnswers(nil, "denied")))
	t.Cleanup(stubRunCommandEnv(func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte(envProbeValue), nil
	}))
	t.Cleanup(stubSelfPath(filepath.Join(dir, "agent-sandbox")))
	execdPath = func() (string, error) {
		return "", errors.New("execd not found in PATH; install it with " +
			"`go install github.com/ynny-github/agent-sandbox/cmd/execd@latest`")
	}

	got := checkProfiles(context.Background(), cfg)
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

func TestCheckContextMode_SkippedWithoutTheVariable(t *testing.T) {
	t.Setenv(claude.ContextModeEnvVar, "")
	if r := checkContextMode(profiles{agent: "a.json"}); !r.ok {
		t.Errorf("checkContextMode() without %s = %+v, want ok", claude.ContextModeEnvVar, r)
	}
}
