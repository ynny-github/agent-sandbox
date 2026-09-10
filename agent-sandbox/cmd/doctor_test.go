// agent-sandbox/cmd/doctor_test.go
package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

// shortStateDir returns a fresh, short-named temp directory suitable for
// XDG_STATE_HOME in tests that actually bind a unix socket underneath it.
// t.TempDir() embeds the (potentially long) test name in the path, which on
// macOS can push a unix socket path past the ~104-byte sun_path limit and
// make bind(2) fail with "invalid argument" — see
// internal/broker/server_test.go's startTestServer for the same workaround.
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
			return "", exec.ErrNotFound
		},
		nonoAnswers(nil, "denied"),
	)
	t.Setenv("XDG_STATE_HOME", shortStateDir(t))
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
	profile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(profile, []byte(`{"filesystem":{"allow":["$WORKDIR"]}}`), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	configWithProfile(t, dir, profile) // writes dir/agent-sandbox.toml
	origConfigPath := configPath
	configPath = filepath.Join(dir, "agent-sandbox.toml")
	t.Cleanup(func() { configPath = origConfigPath })
	// The broker binary in a `go test` run is a temp binary outside dir, so it
	// is never covered by the profile's filesystem.allow above.
	restoreSelf := stubSelfPath(filepath.Join(t.TempDir(), "agent-sandbox"))
	defer restoreSelf()

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

	var buf bytes.Buffer
	doctorCmd.SetOut(&buf)
	t.Cleanup(func() { doctorCmd.SetOut(nil) })

	_ = runDoctor(doctorCmd, nil)
	for _, want := range []string{"nono", "command broker"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing section %q:\n%s", want, buf.String())
		}
	}
}

// A missing command profile is config.Load's headline failure
// (config.go:204 requires the file to exist before anything else about the
// config can be trusted), so this is the case doctor most needs to get
// right — and until config.Load started returning cfg alongside
// ErrCommandProfileMissing, runDoctor's cfgErr branch could not reach
// checkCommandProfile at all here, so this exact scenario always fell back
// to the generic "fix the config first" hint instead of the dedicated one.
func TestRunDoctor_MissingCommandProfileReportsActionableHint(t *testing.T) {
	stubAllSeamsOK(t)

	dir := t.TempDir()
	missing := filepath.Join(dir, "command-profile.json") // never written
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	body := "tool_mode = \"hook\"\ncommand_profile = " + strconv.Quote(missing) + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	origConfigPath := configPath
	configPath = cfgPath
	t.Cleanup(func() { configPath = origConfigPath })

	var buf bytes.Buffer
	doctorCmd.SetOut(&buf)
	t.Cleanup(func() { doctorCmd.SetOut(nil) })

	err := runDoctor(doctorCmd, nil)
	if !errors.Is(err, errDoctorChecksFailed) {
		t.Fatalf("expected errDoctorChecksFailed, got %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "write the profile, or point command_profile at it") {
		t.Errorf("output missing checkCommandProfile's dedicated hint:\n%s", out)
	}
	if strings.Contains(out, "fix the config first") {
		t.Errorf("output still uses the generic cfgErr hint for the missing-profile case:\n%s", out)
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

func TestCheckBrokerSocketDir_OK(t *testing.T) {
	base := shortStateDir(t)
	t.Setenv("XDG_STATE_HOME", base)

	r := checkBrokerSocketDir()
	if !r.ok {
		t.Fatalf("expected OK, got NG: %v", r.details)
	}
	want := filepath.Join(base, "agent-sandbox")
	joined := strings.Join(r.details, "\n")
	if !strings.Contains(joined, want) {
		t.Errorf("details missing socket dir %q: %v", want, r.details)
	}
}

func TestCheckBrokerSocketDir_UnwritableFails(t *testing.T) {
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

	r := checkBrokerSocketDir()
	if r.ok {
		t.Fatal("expected NG when the socket dir cannot be created")
	}
	if r.hint == "" {
		t.Error("expected hint on NG")
	}
}

func TestCheckBrokerSocketDir_ExistingDirUnwritableFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	base := t.TempDir()
	dir := filepath.Join(base, "agent-sandbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// os.MkdirAll returns nil for a directory that already exists, whatever
	// its mode, so this pins that checkBrokerSocketDir does not stop there:
	// it must also fail to bind a socket inside dir and report NG.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("cannot make dir read-only in this environment: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	t.Setenv("XDG_STATE_HOME", base)

	r := checkBrokerSocketDir()
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

func configWithProfile(t *testing.T, dir, profile string) *config.Config {
	t.Helper()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	body := "tool_mode = \"hook\"\ncommand_profile = " + strconv.Quote(profile) + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func stubSelfPath(path string) func() {
	prev := selfPath
	selfPath = func() (string, error) { return path, nil }
	return func() { selfPath = prev }
}

func TestCheckProfiles_FailsWhenValidateRejects(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(profile, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restore := stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("JSON syntax invalid"), fmt.Errorf("exit status 1")
	})
	defer restore()

	got := checkProfiles(configWithProfile(t, dir, profile))
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
	// config.Load itself already refuses a missing profile (validate's
	// ErrCommandProfileMissing) — see TestRunDoctor_MissingCommandProfileReportsActionableHint
	// for that path through runDoctor. Build the *Config while the file still
	// exists, then remove it, to exercise checkCommandProfile's own
	// defensive os.Stat directly here — covering the window between Load
	// succeeding and the profile disappearing before launch.
	cfg := configWithProfile(t, dir, present)
	if err := os.Remove(present); err != nil {
		t.Fatalf("remove profile: %v", err)
	}

	got := checkProfiles(cfg)
	if got.ok {
		t.Errorf("checkProfiles ok = true, want false when the profile file is missing")
	}
	if got.hint == "" {
		t.Errorf("a failing check must carry a hint")
	}
}

// TestCheckProfiles_FailsWhenSelfPathErrors pins ruling R20: not knowing
// the broker's own binary path means the writability check never ran, and
// that must be reported as NG with an explanatory detail, not silently
// treated as "the binary is not writable".
func TestCheckProfiles_FailsWhenSelfPathErrors(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(profile, []byte(`{"filesystem":{"allow":["$WORKDIR"]}}`), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restoreRun := stubRunCommand(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("valid"), nil
	})
	defer restoreRun()
	prevSelf := selfPath
	selfPath = func() (string, error) { return "", fmt.Errorf("os.Executable: not implemented on this platform") }
	defer func() { selfPath = prevSelf }()

	got := checkProfiles(configWithProfile(t, dir, profile))
	if got.ok {
		t.Errorf("checkProfiles ok = true, want false when selfPath errors")
	}
	if got.hint == "" {
		t.Errorf("a failing check must carry a hint")
	}
	if !strings.Contains(strings.Join(got.details, "\n"), "could not determine the broker binary's own path") {
		t.Errorf("expected details to name what could not be determined, got %v", got.details)
	}
}

// TestCheckProfiles_FailsWhenTheWriteQueryErrors pins the ruling that an
// unanswerable writability query must not be silently treated as "not
// writable": doctor reports what it could not determine instead of passing.
func TestCheckProfiles_FailsWhenTheWriteQueryErrors(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(profile, []byte(`{"filesystem":{"allow":["$WORKDIR"]}}`), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	// Every nono call answers with something unparseable as a why result, so
	// validate still passes and this exercises the query step alone.
	restoreRun := stubRunCommand(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Result: valid"), nil
	})
	defer restoreRun()
	restoreSelf := stubSelfPath(filepath.Join(t.TempDir(), "agent-sandbox"))
	defer restoreSelf()

	got := checkProfiles(configWithProfile(t, dir, profile))
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

// nonoAnswers builds a runCommand stub covering the two nono subcommands
// checkProfiles drives: `nono profile validate` (both profiles) and `nono why
// --json` (the broker binary's writability). validateErr, when non-nil, fails
// every validate; whyStatus is the status field the why answer carries.
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

func TestCheckProfiles_ValidatesBothProfiles(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(profile, []byte(`{"filesystem":{"allow":["$WORKDIR"]}}`), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	var validated []string
	restore := stubRunCommand(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 2 && args[0] == "profile" && args[1] == "validate" {
			validated = append(validated, args[2])
			return []byte("Result: valid"), nil
		}
		return []byte("{\"status\":\"denied\"}"), nil
	})
	defer restore()
	restoreSelf := stubSelfPath(filepath.Join(t.TempDir(), "agent-sandbox"))
	defer restoreSelf()

	got := checkProfiles(configWithProfile(t, dir, profile))
	if !got.ok {
		t.Fatalf("checkProfiles ok = false: details=%v hint=%q", got.details, got.hint)
	}
	// The agent profile is generated into a temp file, so its path is not
	// predictable — what matters is that two distinct profiles were validated
	// and the operator's own is one of them.
	if len(validated) != 2 {
		t.Fatalf("validated %d profiles, want 2: %v", len(validated), validated)
	}
	if validated[0] != profile {
		t.Errorf("first validated profile = %q, want the command profile %q", validated[0], profile)
	}
	if validated[1] == profile {
		t.Errorf("second validated profile = %q, want the generated agent profile", validated[1])
	}
}

func TestCheckProfiles_FailsWhenTheAgentProfileIsRejected(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(profile, []byte(`{"filesystem":{"allow":["$WORKDIR"]}}`), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restore := stubRunCommand(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 2 && args[0] == "profile" && args[1] == "validate" && args[2] != profile {
			return []byte("Group 'nope' not found in policy.json"), fmt.Errorf("exit status 1")
		}
		return []byte("Result: valid"), nil
	})
	defer restore()
	restoreSelf := stubSelfPath(filepath.Join(t.TempDir(), "agent-sandbox"))
	defer restoreSelf()

	got := checkProfiles(configWithProfile(t, dir, profile))
	if got.ok {
		t.Fatal("checkProfiles ok = true, want false when nono rejects the generated agent profile")
	}
	if !strings.Contains(got.hint, "[sandbox.agent]") {
		t.Errorf("hint = %q, want it to point at the config section that produced the profile", got.hint)
	}
}

func TestCheckProfiles_FailsWhenNonoSaysTheBinaryIsWritable(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(profile, []byte(`{"filesystem":{"allow":["`+dir+`"]}}`), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restore := stubRunCommand(nonoAnswers(nil, "allowed"))
	defer restore()
	restoreSelf := stubSelfPath(filepath.Join(dir, "agent-sandbox"))
	defer restoreSelf()

	got := checkProfiles(configWithProfile(t, dir, profile))
	if got.ok {
		t.Fatal("checkProfiles ok = true, want false when nono reports the broker binary writable")
	}
	if !strings.Contains(got.hint, "nono why") {
		t.Errorf("hint = %q, want it to name the query that identifies the grant", got.hint)
	}
}

func TestProfileAllowsWrite_ReadsStatusPastWarnings(t *testing.T) {
	restore := stubRunCommand(nonoAnswers(nil, "allowed"))
	defer restore()
	allowed, err := profileAllowsWrite("/p.json", "/bin/agent-sandbox")
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
	if _, err := profileAllowsWrite("/p.json", "/bin/agent-sandbox"); err == nil {
		t.Error("profileAllowsWrite err = nil, want an error when nono prints no JSON")
	}
}
