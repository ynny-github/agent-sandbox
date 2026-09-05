// agent-sandbox/cmd/doctor_test.go
package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// stubLookPath overrides only lookPath, leaving runCommand untouched — the
// counterpart to stubRunCommand, for checkCommandProfile's own use of
// lookPath to resolve the broker entrypoint's base name.
func stubLookPath(lp func(string) (string, error)) func() {
	orig := lookPath
	lookPath = lp
	return func() { lookPath = orig }
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
			// Anything else — specifically "agent-sandbox", the broker
			// entrypoint's base name that checkCommandProfile looks up —
			// resolves to whatever selfPath is stubbed to at call time,
			// matching what a real PATH lookup would find if this exact
			// binary's own directory were on it. A closure, not a fixed
			// value, because tests that use this helper stub selfPath
			// afterward.
			return selfPath()
		},
		func(context.Context, string, ...string) ([]byte, error) { return []byte("nono 0.4.2\n"), nil },
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

func TestCheckCommandProfileFailsWhenValidateRejects(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(profile, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restore := stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("JSON syntax invalid"), fmt.Errorf("exit status 1")
	})
	defer restore()

	got := checkCommandProfile(configWithProfile(t, dir, profile))
	if got.ok {
		t.Errorf("checkCommandProfile ok = true, want false for a profile nono rejects")
	}
	if got.hint == "" {
		t.Errorf("a failing check must carry a hint")
	}
}

func TestCheckCommandProfileFailsWhenTheBinaryIsWritable(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	// The broker binary sitting inside the directory the profile grants
	// read+write is exactly what nono refuses at startup, with a message the
	// operator will not see until every command has already failed.
	body := `{"filesystem":{"allow":["` + dir + `"]}}`
	if err := os.WriteFile(profile, []byte(body), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	// Stub validate to pass so this test fails for the reason it names (the
	// writable-binary check) rather than merely because no real nono is on
	// the test machine's PATH — both would report NG, but only one exercises
	// profileGrantsWrite.
	restoreRun := stubRunCommand(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("valid"), nil
	})
	defer restoreRun()
	restore := stubSelfPath(filepath.Join(dir, "agent-sandbox"))
	defer restore()

	got := checkCommandProfile(configWithProfile(t, dir, profile))
	if got.ok {
		t.Errorf("checkCommandProfile ok = true, want false when the binary is writable through the profile")
	}
	if got.hint == "" {
		t.Errorf("a failing check must carry a hint")
	}
}

// TestCheckCommandProfileFailsWhenACommandsFSWriteCoversTheBinary is the
// regression test for the shape both Criticals this task's review found
// actually had: not the top-level filesystem.allow (already covered above),
// but a command_policies command's own from.<caller>.sandbox.fs_write — a
// per-command child sandbox's grant the original version of
// profileGrantsWrite could not see at all.
func TestCheckCommandProfileFailsWhenACommandsFSWriteCoversTheBinary(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	selfBin := filepath.Join(dir, "agent-sandbox")
	body := `{
	  "filesystem": {"allow": ["$WORKDIR"]},
	  "command_policies": {
	    "commands": {
	      "go": {
	        "executable": "/usr/bin/go",
	        "from": {"agent-sandbox": {"sandbox": {"fs_write": ["` + dir + `"]}}}
	      }
	    }
	  }
	}`
	if err := os.WriteFile(profile, []byte(body), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restoreRun := stubRunCommand(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("valid"), nil
	})
	defer restoreRun()
	restore := stubSelfPath(selfBin)
	defer restore()

	got := checkCommandProfile(configWithProfile(t, dir, profile))
	if got.ok {
		t.Errorf("checkCommandProfile ok = true, want false: a command's own fs_write covers the broker binary")
	}
	if got.hint == "" {
		t.Errorf("a failing check must carry a hint")
	}
}

func TestCheckCommandProfileOK(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(profile, []byte(`{"filesystem":{"allow":["$WORKDIR"]}}`), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restore := stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("valid"), nil
	})
	defer restore()
	selfBin := filepath.Join(t.TempDir(), "agent-sandbox")
	restoreSelf := stubSelfPath(selfBin)
	defer restoreSelf()
	// PATH must resolve the same base name back to this exact binary — see
	// BrokerArgs's doc comment on why that is what actually matters, measured
	// directly against a real nono session (not command_policies.executable_dirs,
	// despite an earlier version of this check assuming so).
	restoreLookPath := stubLookPath(stubLookPathByName(map[string]string{"agent-sandbox": selfBin}))
	defer restoreLookPath()

	got := checkCommandProfile(configWithProfile(t, dir, profile))
	if !got.ok {
		t.Errorf("checkCommandProfile ok = false, want true: details=%v hint=%q", got.details, got.hint)
	}
}

func TestCheckCommandProfileFailsWhenEntrypointNotOnPATH(t *testing.T) {
	// BrokerArgs invokes the broker by base name, resolved through this
	// process's own PATH the same way an ordinary shell would (measured
	// directly; see BrokerArgs's doc comment) — not through
	// command_policies.executable_dirs. A binary that PATH cannot find at
	// all means the broker session fails to start.
	dir := t.TempDir()
	selfBin := filepath.Join(t.TempDir(), "agent-sandbox")

	profile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(profile, []byte(`{"filesystem":{"allow":["$WORKDIR"]}}`), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restore := stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("valid"), nil
	})
	defer restore()
	restoreSelf := stubSelfPath(selfBin)
	defer restoreSelf()
	restoreLookPath := stubLookPath(stubLookPathByName(nil))
	defer restoreLookPath()

	got := checkCommandProfile(configWithProfile(t, dir, profile))
	if got.ok {
		t.Fatal("checkCommandProfile ok = true, want false: the entrypoint's base name is not on PATH at all")
	}
	if !strings.Contains(got.hint, "PATH") {
		t.Errorf("hint = %q, want it to mention PATH", got.hint)
	}
}

func TestCheckCommandProfileFailsWhenEntrypointOnPATHResolvesElsewhere(t *testing.T) {
	// The failure mode this catches is not "not found" but "found the wrong
	// one": PATH resolving "agent-sandbox" to some *other* binary than the
	// one currently running this check means that other binary — a stale
	// install, or an unrelated program that happens to share the name —
	// would silently become the broker instead.
	dir := t.TempDir()
	selfBin := filepath.Join(t.TempDir(), "agent-sandbox")
	otherBin := filepath.Join(t.TempDir(), "agent-sandbox")

	profile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(profile, []byte(`{"filesystem":{"allow":["$WORKDIR"]}}`), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restore := stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("valid"), nil
	})
	defer restore()
	restoreSelf := stubSelfPath(selfBin)
	defer restoreSelf()
	restoreLookPath := stubLookPath(stubLookPathByName(map[string]string{"agent-sandbox": otherBin}))
	defer restoreLookPath()

	got := checkCommandProfile(configWithProfile(t, dir, profile))
	if got.ok {
		t.Fatal("checkCommandProfile ok = true, want false: PATH resolves the entrypoint to a different binary")
	}
	if !strings.Contains(got.hint, "different binary") {
		t.Errorf("hint = %q, want it to say PATH resolves to a different binary", got.hint)
	}
}

// TestCheckCommandProfileFailsWhenProfilePinsADifferentExecutable covers the
// case PATH resolution alone cannot see: the profile's own
// command_policies.commands[base].executable field naming a path other than
// the binary actually running this check, even though PATH resolves the
// same base name correctly. A profile and a binary disagreeing about which
// file the entrypoint is should not pass doctor.
func TestCheckCommandProfileFailsWhenProfilePinsADifferentExecutable(t *testing.T) {
	dir := t.TempDir()
	selfBin := filepath.Join(t.TempDir(), "agent-sandbox")
	pinnedBin := filepath.Join(t.TempDir(), "agent-sandbox")

	profile := filepath.Join(dir, "command-profile.json")
	body := `{
	  "filesystem": {"allow": ["$WORKDIR"]},
	  "command_policies": {"commands": {"agent-sandbox": {"executable": "` + pinnedBin + `"}}}
	}`
	if err := os.WriteFile(profile, []byte(body), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restore := stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("valid"), nil
	})
	defer restore()
	restoreSelf := stubSelfPath(selfBin)
	defer restoreSelf()
	// PATH correctly resolves "agent-sandbox" back to this exact binary —
	// isolating the pinned-executable mismatch as the only failing signal.
	restoreLookPath := stubLookPath(stubLookPathByName(map[string]string{"agent-sandbox": selfBin}))
	defer restoreLookPath()

	got := checkCommandProfile(configWithProfile(t, dir, profile))
	if got.ok {
		t.Fatal("checkCommandProfile ok = true, want false: the profile pins a different executable than the running binary")
	}
	if !strings.Contains(got.hint, "executable") {
		t.Errorf("hint = %q, want it to mention the pinned executable", got.hint)
	}
}

// TestCheckCommandProfileFailsWhenProfileDeclaresEntrypointWithoutExecutable
// covers a profile that declares the entrypoint's command_policies entry but
// omits "executable" entirely. profileEntrypointExecutable then returns "",
// which cleanAbs resolves to this process's own working directory — the
// omission must be reported as an omission, not as a path mismatch naming a
// directory that appears nowhere in the profile.
func TestCheckCommandProfileFailsWhenProfileDeclaresEntrypointWithoutExecutable(t *testing.T) {
	dir := t.TempDir()
	selfBin := filepath.Join(t.TempDir(), "agent-sandbox")

	profile := filepath.Join(dir, "command-profile.json")
	body := `{
	  "filesystem": {"allow": ["$WORKDIR"]},
	  "command_policies": {"commands": {"agent-sandbox": {"can_use": ["git"]}}}
	}`
	if err := os.WriteFile(profile, []byte(body), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restore := stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("valid"), nil
	})
	defer restore()
	restoreSelf := stubSelfPath(selfBin)
	defer restoreSelf()
	restoreLookPath := stubLookPath(stubLookPathByName(map[string]string{"agent-sandbox": selfBin}))
	defer restoreLookPath()

	got := checkCommandProfile(configWithProfile(t, dir, profile))
	if got.ok {
		t.Fatal("checkCommandProfile ok = true, want false: the entrypoint entry omits \"executable\"")
	}
	if !strings.Contains(got.hint, "does not pin") {
		t.Errorf("hint = %q, want it to name the omission, not a path mismatch", got.hint)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	if strings.Contains(got.hint, cwd) {
		t.Errorf("hint = %q, must not send the operator hunting for the process's own working directory", got.hint)
	}
}

// TestCheckCommandProfileOKWhenProfilePinsTheRunningExecutable is the control:
// a profile that declares the entrypoint and pins it correctly must still
// pass.
func TestCheckCommandProfileOKWhenProfilePinsTheRunningExecutable(t *testing.T) {
	dir := t.TempDir()
	selfBin := filepath.Join(t.TempDir(), "agent-sandbox")

	profile := filepath.Join(dir, "command-profile.json")
	body := `{
	  "filesystem": {"allow": ["$WORKDIR"]},
	  "command_policies": {"commands": {"agent-sandbox": {"executable": "` + selfBin + `"}}}
	}`
	if err := os.WriteFile(profile, []byte(body), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restore := stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("valid"), nil
	})
	defer restore()
	restoreSelf := stubSelfPath(selfBin)
	defer restoreSelf()
	restoreLookPath := stubLookPath(stubLookPathByName(map[string]string{"agent-sandbox": selfBin}))
	defer restoreLookPath()

	got := checkCommandProfile(configWithProfile(t, dir, profile))
	if !got.ok {
		t.Errorf("checkCommandProfile ok = false, want true: details=%v hint=%q", got.details, got.hint)
	}
}

func TestCheckCommandProfileFailsWhenTheFileIsMissing(t *testing.T) {
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

	got := checkCommandProfile(cfg)
	if got.ok {
		t.Errorf("checkCommandProfile ok = true, want false when the profile file is missing")
	}
	if got.hint == "" {
		t.Errorf("a failing check must carry a hint")
	}
}

// TestCheckCommandProfileFailsWhenSelfPathErrors pins ruling R20: not knowing
// the broker's own binary path means the writability check never ran, and
// that must be reported as NG with an explanatory detail, not silently
// treated as "the binary is not writable".
func TestCheckCommandProfileFailsWhenSelfPathErrors(t *testing.T) {
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

	got := checkCommandProfile(configWithProfile(t, dir, profile))
	if got.ok {
		t.Errorf("checkCommandProfile ok = true, want false when selfPath errors")
	}
	if got.hint == "" {
		t.Errorf("a failing check must carry a hint")
	}
	if !strings.Contains(strings.Join(got.details, "\n"), "could not determine the broker binary's own path") {
		t.Errorf("expected details to name what could not be determined, got %v", got.details)
	}
}

// TestCheckCommandProfileFailsWhenProfileGrantsWriteErrors pins the same
// ruling for profileGrantsWrite's own error return: an unreadable or
// unparseable profile must not be silently treated as "not writable".
func TestCheckCommandProfileFailsWhenProfileGrantsWriteErrors(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	// Malformed JSON: `nono profile validate` is stubbed to pass regardless
	// (checkCommandProfile only calls the real nono for that step), so this
	// exercises profileGrantsWrite's own json.Unmarshal failure specifically,
	// independent of validate.
	if err := os.WriteFile(profile, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restoreRun := stubRunCommand(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("valid"), nil
	})
	defer restoreRun()
	restoreSelf := stubSelfPath(filepath.Join(t.TempDir(), "agent-sandbox"))
	defer restoreSelf()

	got := checkCommandProfile(configWithProfile(t, dir, profile))
	if got.ok {
		t.Errorf("checkCommandProfile ok = true, want false when profileGrantsWrite errors")
	}
	if got.hint == "" {
		t.Errorf("a failing check must carry a hint")
	}
	if !strings.Contains(strings.Join(got.details, "\n"), "could not check whether the profile grants write access") {
		t.Errorf("expected details to name what could not be determined, got %v", got.details)
	}
}

// stubLookPathByName returns a lookPath stub that answers only the given
// names, so a test can distinguish checkToolSandbox's lookup of "nono" from
// its lookup of the probe program without one stub masking the other.
func stubLookPathByName(paths map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		if p, ok := paths[name]; ok {
			return p, nil
		}
		return "", fmt.Errorf("not found: %s", name)
	}
}

func TestCheckToolSandbox_NonoMissing(t *testing.T) {
	stubNonoSeams(t,
		stubLookPathByName(nil),
		func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("runCommand must not be called when nono is not on PATH")
			return nil, nil
		},
	)

	got := checkToolSandbox(context.Background())
	if got.ok {
		t.Error("checkToolSandbox ok = true, want false when nono is not on PATH")
	}
	if got.hint == "" {
		t.Error("a failing check must carry a hint")
	}
}

// TestCheckToolSandbox_ProbeBinaryMissing pins ruling R18's "fail loudly ...
// when the probe cannot run" requirement: finding no program to probe with
// must not be silently treated as success.
func TestCheckToolSandbox_ProbeBinaryMissing(t *testing.T) {
	stubNonoSeams(t,
		stubLookPathByName(map[string]string{"nono": "/usr/bin/nono"}),
		func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("runCommand must not be called when the probe binary cannot be found")
			return nil, nil
		},
	)

	got := checkToolSandbox(context.Background())
	if got.ok {
		t.Error("checkToolSandbox ok = true, want false when the probe binary is not on PATH")
	}
	if got.hint == "" {
		t.Error("a failing check must carry a hint")
	}
}

func TestCheckToolSandbox_NonoRunFails(t *testing.T) {
	stubNonoSeams(t,
		stubLookPathByName(map[string]string{"nono": "/usr/bin/nono", toolSandboxProbeCommand: "/usr/bin/true"}),
		func(context.Context, string, ...string) ([]byte, error) {
			return []byte("nono: Sandbox initialization failed: failed to resolve ELF dependency 'libc.so.6'"),
				fmt.Errorf("exit status 1")
		},
	)

	got := checkToolSandbox(context.Background())
	if got.ok {
		t.Error("checkToolSandbox ok = true, want false when nono cannot start tool-sandbox")
	}
	if got.hint == "" {
		t.Error("a failing check must carry a hint")
	}
	if !strings.Contains(strings.Join(got.details, "\n"), "ELF dependency") {
		t.Errorf("expected nono's own diagnostic in details, got %v", got.details)
	}
}

func TestCheckToolSandbox_OK(t *testing.T) {
	stubNonoSeams(t,
		stubLookPathByName(map[string]string{"nono": "/usr/bin/nono", toolSandboxProbeCommand: "/usr/bin/true"}),
		func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	)

	got := checkToolSandbox(context.Background())
	if !got.ok {
		t.Errorf("checkToolSandbox ok = false, want true: details=%v hint=%q", got.details, got.hint)
	}
}

// TestCheckToolSandbox_WritesPolicyProbeProfile inspects the actual profile
// checkToolSandbox hands to nono, rather than only its ok/NG verdict: this is
// what proves the probe truly exercises tool-sandbox (a policy-controlled
// command declared under command_policies) instead of merely running the
// probe binary unsandboxed and reporting success no matter what.
func TestCheckToolSandbox_WritesPolicyProbeProfile(t *testing.T) {
	const nonoPath = "/usr/bin/nono"
	const trueBin = "/usr/bin/true"
	var gotArgs []string
	var profilePath, workdir string
	var profileData []byte

	stubNonoSeams(t,
		stubLookPathByName(map[string]string{"nono": nonoPath, toolSandboxProbeCommand: trueBin}),
		func(ctx context.Context, name string, args ...string) ([]byte, error) {
			gotArgs = append([]string{name}, args...)
			for i, a := range args {
				switch a {
				case "--profile":
					if i+1 < len(args) {
						profilePath = args[i+1]
					}
				case "--workdir":
					if i+1 < len(args) {
						workdir = args[i+1]
					}
				}
			}
			// checkToolSandbox removes its temp dir (holding the profile) once
			// this call returns, so read it now, from inside the stub, while it
			// still exists.
			if profilePath != "" {
				data, err := os.ReadFile(profilePath)
				if err != nil {
					t.Fatalf("read generated profile: %v", err)
				}
				profileData = data
			}
			return nil, nil
		},
	)

	got := checkToolSandbox(context.Background())
	if !got.ok {
		t.Fatalf("checkToolSandbox ok = false, want true: details=%v hint=%q", got.details, got.hint)
	}
	if len(gotArgs) == 0 || gotArgs[0] != "nono" {
		t.Fatalf("expected runCommand to be called with nono, got %v", gotArgs)
	}
	if gotArgs[len(gotArgs)-1] != toolSandboxProbeCommand {
		t.Errorf("expected the probe command name as the final argv entry, got %q", gotArgs[len(gotArgs)-1])
	}
	if profilePath == "" || workdir == "" {
		t.Fatalf("expected --profile and --workdir to be passed, got args %v", gotArgs)
	}

	var parsed struct {
		Groups struct {
			Include []string `json:"include"`
		} `json:"groups"`
		Filesystem struct {
			Allow []string `json:"allow"`
		} `json:"filesystem"`
		CommandPolicies struct {
			Commands map[string]struct {
				Executable string `json:"executable"`
			} `json:"commands"`
		} `json:"command_policies"`
	}
	if err := json.Unmarshal(profileData, &parsed); err != nil {
		t.Fatalf("generated profile is not valid JSON: %v\n%s", err, profileData)
	}
	if !slices.Contains(parsed.Groups.Include, "nix_runtime") {
		t.Errorf("profile groups.include = %v, want it to contain %q (needed for /nix/store on NixOS)",
			parsed.Groups.Include, "nix_runtime")
	}
	if !slices.Contains(parsed.Filesystem.Allow, workdir) {
		t.Errorf("profile filesystem.allow = %v, want it to contain the --workdir value %q",
			parsed.Filesystem.Allow, workdir)
	}
	cmd, ok := parsed.CommandPolicies.Commands[toolSandboxProbeCommand]
	if !ok {
		t.Fatalf("profile command_policies.commands has no entry %q: %v",
			toolSandboxProbeCommand, parsed.CommandPolicies.Commands)
	}
	if cmd.Executable != trueBin {
		t.Errorf("policy command executable = %q, want %q", cmd.Executable, trueBin)
	}

	// The probe's own temp dir must not leak.
	if _, err := os.Stat(filepath.Dir(profilePath)); !os.IsNotExist(err) {
		t.Errorf("expected the probe's temp dir to be removed after the check, stat err = %v", err)
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
