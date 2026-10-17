package claude

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/internal/agentconfig"
	"github.com/ynny-github/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/internal/execd"
)

func TestValidatePassthrough_SettingsBlocked(t *testing.T) {
	if err := ValidatePassthrough([]string{"--settings", "foo.json"}); err == nil {
		t.Fatal("expected error for --settings, got nil")
	}
}

func TestValidatePassthrough_SettingsEqualBlocked(t *testing.T) {
	if err := ValidatePassthrough([]string{"--settings=foo.json"}); err == nil {
		t.Fatal("expected error for --settings=..., got nil")
	}
}

func TestValidatePassthrough_AllowsOtherArgs(t *testing.T) {
	if err := ValidatePassthrough([]string{"--print", "--model", "opus"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePassthrough_Empty(t *testing.T) {
	if err := ValidatePassthrough(nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestValidatePassthrough_AllowsMCPConfig guards the passthrough that opened
// up when agent-sandbox stopped generating an MCP config of its own: these
// two flags were reserved while it did, and nothing reserves them now.
func TestValidatePassthrough_AllowsMCPConfig(t *testing.T) {
	if err := ValidatePassthrough([]string{"--mcp-config", "x.json", "--strict-mcp-config"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildArgs_NonoNotInPath(t *testing.T) {
	t.Setenv("PATH", "")
	cfg := &config.Config{}
	if _, _, err := BuildArgs(cfg, Options{}, "", "", "", ""); err == nil {
		t.Fatal("expected error when nono not in PATH, got nil")
	}
}

func makeFakeNono(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "nono")
	// Answers `nono why` with denied, so startExecd's writability check on
	// the fake execd beside it passes.
	if err := os.WriteFile(path, []byte("#!/bin/sh\nif [ \"$1\" = why ]; then echo '{\"status\":\"denied\"}'; exit 0; fi\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	execdScript := "#!/bin/sh\nd=${0%/*}\n" +
		"case \"$1\" in\n" +
		"up) echo \"$d/fake.sock\" ;;\n" +
		"down) echo \"$2\" > \"$d/down-arg\" ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "execd"), []byte(execdScript), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return path
}

func argsContain(args []string, target string) bool {
	for _, a := range args {
		if a == target {
			return true
		}
	}
	return false
}

func argsIndex(args []string, target string) int {
	for i, a := range args {
		if a == target {
			return i
		}
	}
	return -1
}

// The agent runs under supervised `nono run`, not `nono wrap`. On nono 0.78.0
// wrap applies the sandbox to nono itself before running the profile's
// session_hooks.before, so the nolabs-ai/claude pack's before-hook fails with
// "Failed to create session env directory ...: Permission denied".
func TestBuildArgs_AlwaysUsesRun(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{}
	_, args, err := BuildArgs(cfg, Options{}, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(args) < 2 {
		t.Fatalf("expected at least 2 args, got %v", args)
	}
	if args[0] != "nono" {
		t.Errorf("args[0] = %q, want \"nono\"; full args: %v", args[0], args)
	}
	if args[1] != "run" {
		t.Errorf("args[1] = %q, want \"run\"; full args: %v", args[1], args)
	}
}

// Every session injects the PreToolUse hook: it is the only thing that routes
// the agent's Bash and Monitor commands to execd.
func TestBuildArgs_InjectsHookSettings(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{}
	_, args, err := BuildArgs(cfg, Options{}, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if argsContain(args, "--disallowed-tools") {
		t.Errorf("Bash and Monitor stay enabled; got %v", args)
	}

	ci := argsIndex(args, "claude")

	si := argsIndex(args, "--settings")
	if si < 0 || si+1 >= len(args) {
		t.Fatalf("--settings must be injected with a value; got %v", args)
	}
	val := args[si+1]
	if !strings.Contains(val, `"PreToolUse"`) ||
		!strings.Contains(val, "agent-sandbox hook") {
		t.Errorf("--settings value missing the hook config; got %q", val)
	}
	if si < ci {
		t.Errorf("--settings must appear after claude; got %v", args)
	}
	// The settings JSON carries the hook and nothing else — in particular no
	// "permissions" key: agent-sandbox decides no permissions itself, the two
	// nono profiles do.
	if strings.Contains(val, `"permissions"`) {
		t.Errorf("--settings value should have no permissions key without an MCP config path; got %q", val)
	}
}

// The only --read-file BuildArgs adds on its own is the launcher's own binary,
// which the agent must be able to exec to run the hook. Nothing else is
// granted here; everything else is the profile's.
func TestBuildArgs_GrantsOnlyTheLauncherBinary(t *testing.T) {
	makeFakeNono(t)
	self := pinExecutablePath(t)
	cfg := &config.Config{}
	_, args, err := BuildArgs(cfg, Options{}, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var granted []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--read-file" {
			granted = append(granted, args[i+1])
		}
	}
	if len(granted) != 1 || granted[0] != self {
		t.Errorf("BuildArgs must grant only the launcher binary %q; got %v", self, granted)
	}
}

func TestBuildArgs_InjectsProfileBeforeClaude(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{}
	_, args, err := BuildArgs(cfg, Options{}, "/tmp/asb-profile-1.json", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pi := argsIndex(args, "--profile")
	ci := argsIndex(args, "claude")
	if pi < 0 || ci < 0 || pi > ci {
		t.Errorf("--profile must appear before claude; got %v", args)
	}
	if args[pi+1] != "/tmp/asb-profile-1.json" {
		t.Errorf("--profile value misplaced; got %v", args)
	}
}

func TestBuildArgs_ClaudeOptsAfterClaude(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{}
	_, args, err := BuildArgs(cfg, Options{ClaudeOpts: []string{"--model", "opus"}}, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ci := argsIndex(args, "claude")
	mi := argsIndex(args, "--model")
	if ci < 0 || mi < 0 || mi < ci {
		t.Errorf("--model must appear after claude; got %v", args)
	}
	if args[mi+1] != "opus" {
		t.Errorf("--model value misplaced; got %v", args)
	}
}

func TestBuildArgs_InjectsSystemPrompt(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{}
	_, args, err := BuildArgs(cfg, Options{}, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	i := argsIndex(args, "--append-system-prompt")
	if i < 0 {
		t.Fatalf("--append-system-prompt not injected; got %v", args)
	}
	if args[i+1] != agentconfig.Pointer() {
		t.Errorf("system prompt arg = %q, want Pointer()", args[i+1])
	}
	if ci := argsIndex(args, "claude"); ci < 0 || i < ci {
		t.Errorf("--append-system-prompt must appear after claude; got %v", args)
	}
}

// The launcher configures no MCP server of its own — it never generated one,
// and the run_command tool it used to serve is gone — so no mcp flag should
// reach claude.
func TestBuildArgs_InjectsNoMCPFlags(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{}
	_, args, err := BuildArgs(cfg, Options{}, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if argsContain(args, "--mcp-config") || argsContain(args, "--strict-mcp-config") {
		t.Errorf("no mcp flags expected; got %v", args)
	}
}

func TestParseArgs_ClaudeOptsAfterDash(t *testing.T) {
	cfgFile, opts, err := ParseArgs([]string{"--config", "custom.toml", "--", "--model", "opus"}, "default.toml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfgFile != "custom.toml" {
		t.Errorf("configFile = %q, want custom.toml", cfgFile)
	}
	if strings.Join(opts.ClaudeOpts, " ") != "--model opus" {
		t.Errorf("ClaudeOpts = %v", opts.ClaudeOpts)
	}
}

func TestParseArgs_ConfigEqualsForm(t *testing.T) {
	cfgFile, _, err := ParseArgs([]string{"--config=custom.toml", "--", "--print"}, "default.toml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfgFile != "custom.toml" {
		t.Errorf("configFile = %q, want custom.toml", cfgFile)
	}
}

func TestParseArgs_Empty(t *testing.T) {
	cfgFile, opts, err := ParseArgs(nil, "default.toml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfgFile != "default.toml" {
		t.Errorf("configFile = %q, want default", cfgFile)
	}
	if len(opts.ClaudeOpts) != 0 {
		t.Errorf("ClaudeOpts = %v, want empty", opts.ClaudeOpts)
	}
}

func TestParseArgs_ProfileRejected(t *testing.T) {
	_, _, err := ParseArgs([]string{"--profile", "nono.jsonc"}, "default.toml")
	if err == nil || !strings.Contains(err.Error(), "[agents.") {
		t.Fatalf("expected --profile rejection pointing to [agents.<name>].profile, got %v", err)
	}
}

func TestParseArgs_UnknownNonoOptRejected(t *testing.T) {
	_, _, err := ParseArgs([]string{"--allow", "/repo"}, "default.toml")
	if err == nil {
		t.Fatal("expected error for a pre-'--' nono option, got nil")
	}
}

func TestParseArgs_EnvRefs(t *testing.T) {
	cfgFile, opts, err := ParseArgs(
		[]string{"--config", "custom.toml", "--env", "file:.env", "--env=file:b.env", "--", "--print"},
		"default.toml")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfgFile != "custom.toml" {
		t.Errorf("cfgFile = %q, want custom.toml", cfgFile)
	}
	wantEnv := []string{"file:.env", "file:b.env"}
	if !reflect.DeepEqual(opts.EnvRefs, wantEnv) {
		t.Errorf("EnvRefs = %v, want %v", opts.EnvRefs, wantEnv)
	}
	if !reflect.DeepEqual(opts.ClaudeOpts, []string{"--print"}) {
		t.Errorf("ClaudeOpts = %v, want [--print]", opts.ClaudeOpts)
	}
}

// Without --allow-cwd, nono asks "Share <cwd> with read+write access? [y/N]"
// on every launch, because the nolabs-ai/claude base profile requests the
// working directory. The operator has chosen to share it unprompted.
func TestBuildArgs_AllowsCwdWithoutPrompt(t *testing.T) {
	makeFakeNono(t)
	_, args, err := BuildArgs(&config.Config{}, Options{}, "", "", "", "")
	if err != nil {
		t.Fatalf("BuildArgs() error = %v", err)
	}
	fi := argsIndex(args, "--allow-cwd")
	ci := argsIndex(args, "claude")
	if fi < 0 || ci < 0 || fi > ci {
		t.Errorf("--allow-cwd must appear before claude; got %v", args)
	}
}

func TestBuildArgs_GrantsExecdSocket(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{}
	_, args, err := BuildArgs(cfg, Options{}, "", "/tmp/b.sock", "", "")
	if err != nil {
		t.Fatalf("BuildArgs() error = %v", err)
	}
	if !hasFlagValue(args, "--allow-unix-socket", "/tmp/b.sock") {
		t.Errorf("args = %v, want --allow-unix-socket /tmp/b.sock", args)
	}
	si := argsIndex(args, "--allow-unix-socket")
	ci := argsIndex(args, "claude")
	if si < 0 || ci < 0 || si > ci {
		t.Errorf("--allow-unix-socket must appear before claude; got %v", args)
	}
}

func hasFlagValue(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// --- lifecycle orchestration (Task 4) ---
//
// The Docker-lifecycle tests that used to live here (fakeHandle / ensureUp /
// Started / Down) no longer apply: run() no longer owns a sandbox lifecycle,
// it starts and tears down execd instead. They are replaced by
// the execd-focused tests below; startExecd replaces the deleted ensureUp
// field everywhere else.

const testExecdPath = "/opt/bin/execd"

func testExecdStart(sock string, cleaned *int) func(*config.Config) (string, string, func(), error) {
	return func(*config.Config) (string, string, func(), error) {
		return sock, testExecdPath, func() {
			if cleaned != nil {
				*cleaned++
			}
		}, nil
	}
}

func TestRun_StartExecdFailure_DoesNotLaunch(t *testing.T) {
	makeFakeNono(t)
	superviseCalls := 0
	exitCalls := 0
	err := run(&config.Config{}, Options{}, runDeps{
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		verifyHook:        func(string, string) error { return nil },
		startShellWrapper: testWrapperStart("/tmp/test-norc-bash-1", nil),
		startExecd: func(*config.Config) (string, string, func(), error) {
			return "", "", nil, errors.New("execd start error")
		},
		supervise: func(string, []string) int { superviseCalls++; return 0 },
		exit:      func(int) { exitCalls++ },
	})
	if err == nil {
		t.Fatal("expected error when startExecd fails, got nil")
	}
	if superviseCalls != 0 {
		t.Errorf("claude was launched despite execd failure (supervise called %d times)", superviseCalls)
	}
	if exitCalls != 0 {
		t.Errorf("exit called %d times, want 0 on hard-fail", exitCalls)
	}
}

func TestRun_ExitReceivesSuperviseCode(t *testing.T) {
	makeFakeNono(t)
	gotExit := -1
	err := run(&config.Config{}, Options{}, runDeps{
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		verifyHook:        func(string, string) error { return nil },
		startExecd:        testExecdStart("/tmp/test.sock", nil),
		startShellWrapper: testWrapperStart("/tmp/test-norc-bash-1", nil),
		supervise:         func(string, []string) int { return 3 },
		exit:              func(code int) { gotExit = code },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotExit != 3 {
		t.Errorf("exit code = %d, want 3 (claude's code)", gotExit)
	}
}

func TestRun_ExecdCleanupBeforeExit(t *testing.T) {
	makeFakeNono(t)
	cleaned := 0
	cleanedBeforeExit := false
	err := run(&config.Config{}, Options{}, runDeps{
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		verifyHook:        func(string, string) error { return nil },
		startExecd:        testExecdStart("/tmp/test.sock", &cleaned),
		startShellWrapper: testWrapperStart("/tmp/test-norc-bash-1", nil),
		supervise:         func(string, []string) int { return 0 },
		exit:              func(int) { cleanedBeforeExit = cleaned == 1 },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cleanedBeforeExit {
		t.Error("execd cleanup must run before exit; os.Exit skips deferred cleanup in production")
	}
}

func TestRun_SetsExecdSocketEnvBeforeSupervise(t *testing.T) {
	makeFakeNono(t)
	// t.Setenv restores whatever this variable held before the test once the
	// test finishes, so run()'s own os.Setenv call below doesn't leak into
	// the rest of the binary.
	t.Setenv(execd.SocketEnvVar, "")
	const wantSocket = "/tmp/test-env-handoff.sock"
	var gotEnv string
	err := run(&config.Config{}, Options{}, runDeps{
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		verifyHook:        func(string, string) error { return nil },
		startExecd:        testExecdStart(wantSocket, nil),
		startShellWrapper: testWrapperStart("/tmp/test-norc-bash-1", nil),
		supervise: func(string, []string) int {
			gotEnv = os.Getenv(execd.SocketEnvVar)
			return 0
		},
		exit: func(int) {},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotEnv != wantSocket {
		t.Errorf("%s at supervise time = %q, want %q (the execd socket, set before supervise runs)",
			execd.SocketEnvVar, gotEnv, wantSocket)
	}
}

// startExecd now launches a real `nono run` session (see ExecdArgs),
// so it can no longer be exercised end-to-end without a real nono binary and
// a real sandbox — out of scope for this package's tests (see task-4-report.md
// for why). What stays testable without spawning anything is its plumbing:
// like BuildArgs, it must fail fast when nono is not on PATH rather than
// attempting to start a session it cannot run.
func TestStartExecd_NonoNotInPath(t *testing.T) {
	t.Setenv("PATH", "")
	sock, _, cleanup, err := startExecd(&config.Config{})
	if err == nil {
		t.Fatal("startExecd() error = nil, want error when nono is not in PATH")
	}
	if !strings.Contains(err.Error(), "nono not found in PATH") {
		t.Errorf("error = %q, want it to explain nono is missing", err.Error())
	}
	if sock != "" {
		t.Errorf("socket = %q, want empty on error", sock)
	}
	if cleanup != nil {
		t.Error("cleanup should be nil on error")
	}
}

// loadConfigWithCommandProfile writes a minimal project config in dir pointing
// at profile and loads it, so the test exercises the same resolution the
// launcher uses rather than a hand-built Config.
func loadConfigWithCommandProfile(t *testing.T, dir, profile string) *config.Config {
	t.Helper()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	body := "command_profile = " + strconv.Quote(profile) + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// writeLaunchFixture writes a loadable config plus the two profiles beside it
// and returns the directory and the loaded config.
func writeLaunchFixture(t *testing.T, withAgentProfile bool) (string, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	body := ""
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "command-profile.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write command profile: %v", err)
	}
	if withAgentProfile {
		if err := os.WriteFile(filepath.Join(dir, "claude-profile.json"), []byte("{}"), 0o600); err != nil {
			t.Fatalf("write agent profile: %v", err)
		}
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return dir, cfg
}

func TestRun_PassesTheConfiguredAgentProfileToNono(t *testing.T) {
	makeFakeNono(t)
	dir, cfg := writeLaunchFixture(t, true)

	var gotArgs []string
	err := run(cfg, Options{}, runDeps{
		agentProfile:      defaultAgentProfile,
		verifyHook:        func(string, string) error { return nil },
		startExecd:        testExecdStart("/tmp/test.sock", nil),
		startShellWrapper: testWrapperStart("/tmp/test-norc-bash-1", nil),
		supervise:         func(_ string, args []string) int { gotArgs = args; return 0 },
		exit:              func(int) {},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := filepath.Join(dir, "claude-profile.json")
	i := argsIndex(gotArgs, "--profile")
	if i < 0 || i+1 >= len(gotArgs) || gotArgs[i+1] != want {
		t.Errorf("--profile must name the configured agent profile %q; got %v", want, gotArgs)
	}
}

func TestRun_MissingAgentProfileFailsBeforeLaunch(t *testing.T) {
	makeFakeNono(t)
	_, cfg := writeLaunchFixture(t, false)

	supervised := 0
	err := run(cfg, Options{}, runDeps{
		agentProfile:      defaultAgentProfile,
		verifyHook:        func(string, string) error { return nil },
		startExecd:        testExecdStart("/tmp/test.sock", nil),
		startShellWrapper: testWrapperStart("/tmp/test-norc-bash-1", nil),
		supervise:         func(string, []string) int { supervised++; return 0 },
		exit:              func(int) {},
	})
	if !errors.Is(err, config.ErrAgentProfileMissing) {
		t.Fatalf("run error = %v, want ErrAgentProfileMissing", err)
	}
	if supervised != 0 {
		t.Errorf("claude must not be launched when the agent profile is missing")
	}
}

// The PreToolUse hook runs `agent-sandbox hook` as a direct child of the
// agent, so it runs inside the agent's own sandbox rather than through
// execd: without a grant for the binary itself, nono refuses the execve and
// every command fails with nothing on screen to explain it. The path is the
// launcher's own, so it is passed as a flag rather than written into the
// hand-written profile, where a mise toolchain upgrade would silently
// invalidate it.
func TestBuildArgs_GrantsTheLauncherBinaryToTheAgent(t *testing.T) {
	makeFakeNono(t)
	self := pinExecutablePath(t)

	cfg := &config.Config{}
	_, args, err := BuildArgs(cfg, Options{}, "/tmp/p.json", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasFlagValue(args, "--read-file", self) {
		t.Errorf("expected --read-file %s; got %v", self, args)
	}
	i := argsIndex(args, "--read-file")
	if c := argsIndex(args, "claude"); i < 0 || c < 0 || i > c {
		t.Errorf("the grant must precede the wrapped command; got %v", args)
	}
}

// Launching without the grant produces a session in which every command fails
// for a reason nothing reports, so an unknown binary path stops the launch
// instead of starting a broken one.
func TestBuildArgs_FailsWhenItsOwnPathIsUnknown(t *testing.T) {
	makeFakeNono(t)
	prev := executablePath
	executablePath = func() (string, error) { return "", errors.New("os.Executable: not implemented") }
	t.Cleanup(func() { executablePath = prev })

	cfg := &config.Config{}
	if _, _, err := BuildArgs(cfg, Options{}, "/tmp/p.json", "", "", ""); err == nil {
		t.Fatal("expected an error when the launcher cannot locate its own binary, got nil")
	}
}

// pinExecutablePath fixes the launcher's own binary path for a test and returns
// it. BuildArgs grants that path unconditionally, so a test asserting on the
// grant list needs it to be a known value rather than the test binary's.
func pinExecutablePath(t *testing.T) string {
	t.Helper()
	const self = "/home/tester/.local/share/mise/installs/go/1.25.14/bin/agent-sandbox"
	prev := executablePath
	executablePath = func() (string, error) { return self, nil }
	t.Cleanup(func() { executablePath = prev })
	return self
}

// If the hook cannot run, Claude Code reports a non-blocking error and then
// runs the command anyway — unwrapped, in the agent's own sandbox, under none
// of the command profile's limits. That is a bypass, not a degraded mode, so
// the launcher proves the hook works before handing control to the agent.
func TestRun_RefusesToLaunchWhenTheHookCannotRun(t *testing.T) {
	makeFakeNono(t)
	pinExecutablePath(t)
	dir, cfg := writeLaunchFixture(t, true)
	_ = dir

	supervised := 0
	err := run(cfg, Options{}, runDeps{
		agentProfile:      defaultAgentProfile,
		verifyHook:        func(string, string) error { return errors.New("execve refused") },
		startExecd:        testExecdStart("/tmp/test.sock", nil),
		startShellWrapper: testWrapperStart("/tmp/test-norc-bash-1", nil),
		supervise:         func(string, []string) int { supervised++; return 0 },
		exit:              func(int) {},
	})
	if err == nil {
		t.Fatal("expected an error when the hook probe fails, got nil")
	}
	if supervised != 0 {
		t.Error("claude must not be launched when its hook cannot run")
	}
}

// The hook is the only route from the agent's tools to execd, so the
// probe is unconditional: every launch proves it before handing over control.
func TestRun_AlwaysProbesTheHook(t *testing.T) {
	makeFakeNono(t)
	pinExecutablePath(t)
	_, cfg := writeLaunchFixture(t, true)

	probed := 0
	err := run(cfg, Options{}, runDeps{
		agentProfile:      defaultAgentProfile,
		verifyHook:        func(string, string) error { probed++; return nil },
		startExecd:        testExecdStart("/tmp/test.sock", nil),
		startShellWrapper: testWrapperStart("/tmp/test-norc-bash-1", nil),
		supervise:         func(string, []string) int { return 0 },
		exit:              func(int) {},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if probed != 1 {
		t.Errorf("the hook probe ran %d time(s); want exactly 1", probed)
	}
}

// Every collaborator Run hands to run must be wired. A nil one is not a failing
// test but a panic at launch, and the tests that drive run() supply their own
// stubs, so nothing else would notice a field added to runDeps and forgotten
// here.
func TestDefaultDeps_EveryDependencyIsWired(t *testing.T) {
	v := reflect.ValueOf(defaultDeps())
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Kind() != reflect.Func {
			continue
		}
		if v.Field(i).IsNil() {
			t.Errorf("runDeps.%s is nil in defaultDeps()", v.Type().Field(i).Name)
		}
	}
}

func TestParseArgs_ContextModeFlag(t *testing.T) {
	_, opts, err := ParseArgs([]string{"--context-mode", "--", "-p", "hi"}, "cfg.toml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opts.ContextMode {
		t.Error("--context-mode before \"--\" must set Options.ContextMode")
	}
	if len(opts.ClaudeOpts) != 2 || opts.ClaudeOpts[0] != "-p" {
		t.Errorf("claude opts = %v, want [-p hi]", opts.ClaudeOpts)
	}
}

func TestParseArgs_ContextModeDefaultsOff(t *testing.T) {
	_, opts, err := ParseArgs([]string{"--", "-p", "hi"}, "cfg.toml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.ContextMode {
		t.Error("Options.ContextMode must default to false")
	}
}

// After "--" the flag belongs to claude, not to agent-sandbox: the launcher
// must pass it through untouched rather than claim it.
func TestParseArgs_ContextModeAfterDashIsPassthrough(t *testing.T) {
	_, opts, err := ParseArgs([]string{"--", "--context-mode"}, "cfg.toml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.ContextMode {
		t.Error("--context-mode after \"--\" must not set Options.ContextMode")
	}
	if len(opts.ClaudeOpts) != 1 || opts.ClaudeOpts[0] != "--context-mode" {
		t.Errorf("claude opts = %v, want [--context-mode]", opts.ClaudeOpts)
	}
}

// Only the bare form is accepted. A value form would suggest the flag can be
// switched off with "=false", which it cannot.
func TestParseArgs_ContextModeValueFormRejected(t *testing.T) {
	if _, _, err := ParseArgs([]string{"--context-mode=true", "--"}, "cfg.toml"); err == nil {
		t.Error("--context-mode=true must be rejected")
	}
}

// The launcher hands the backend selection to the agent the same way it hands
// over the execd socket: by setting it in its own environment before
// supervise, which the child inherits.
func TestRun_SetsContextModeEnvBeforeSupervise(t *testing.T) {
	makeFakeNono(t)
	t.Setenv(ContextModeEnvVar, "")
	var got string
	err := run(&config.Config{}, Options{ContextMode: true}, runDeps{
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		verifyHook:         func(string, string) error { return nil },
		ContextModeEnabled: func() (bool, error) { return true, nil },
		verifyContextMode:  func(string) error { return nil },
		startExecd:         testExecdStart("/tmp/test.sock", nil),
		startShellWrapper:  testWrapperStart("/tmp/test-norc-bash-1", nil),
		supervise: func(string, []string) int {
			got = os.Getenv(ContextModeEnvVar)
			return 0
		},
		exit: func(int) {},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "execd" {
		t.Errorf("%s at supervise time = %q, want %q", ContextModeEnvVar, got, "execd")
	}
}

// Without the flag the variable must not appear at all: an empty value and an
// absent one mean the same thing to context-mode, but publishing one anyway
// would claim a selection this session never made.
func TestRun_LeavesContextModeEnvUnsetWithoutTheFlag(t *testing.T) {
	makeFakeNono(t)
	t.Setenv(ContextModeEnvVar, "")
	var got string
	err := run(&config.Config{}, Options{}, runDeps{
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		verifyHook:         func(string, string) error { return nil },
		ContextModeEnabled: func() (bool, error) { return false, nil },
		startExecd:         testExecdStart("/tmp/test.sock", nil),
		startShellWrapper:  testWrapperStart("/tmp/test-norc-bash-1", nil),
		supervise: func(string, []string) int {
			got = os.Getenv(ContextModeEnvVar)
			return 0
		},
		exit: func(int) {},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("%s at supervise time = %q, want empty without the flag", ContextModeEnvVar, got)
	}
}

func TestRun_ContextModeNotEnabled_DoesNotStartExecd(t *testing.T) {
	makeFakeNono(t)
	execdCalls, superviseCalls := 0, 0
	err := run(&config.Config{}, Options{ContextMode: true}, runDeps{
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		verifyHook:         func(string, string) error { return nil },
		ContextModeEnabled: func() (bool, error) { return false, nil },
		verifyContextMode:  func(string) error { return nil },
		startShellWrapper:  testWrapperStart("/tmp/test-norc-bash-1", nil),
		startExecd: func(*config.Config) (string, string, func(), error) {
			execdCalls++
			return "/tmp/test.sock", testExecdPath, func() {}, nil
		},
		supervise: func(string, []string) int { superviseCalls++; return 0 },
		exit:      func(int) {},
	})
	if err == nil {
		t.Fatal("expected an error when context-mode is not enabled")
	}
	if !strings.Contains(err.Error(), "context-mode") {
		t.Errorf("error must name context-mode: %v", err)
	}
	if execdCalls != 0 {
		t.Errorf("execd started %d times; the check must run before it", execdCalls)
	}
	if superviseCalls != 0 {
		t.Errorf("claude was launched %d times, want 0", superviseCalls)
	}
}

func TestRun_ContextModeCheckSkippedWithoutTheFlag(t *testing.T) {
	makeFakeNono(t)
	checked := false
	err := run(&config.Config{}, Options{}, runDeps{
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		verifyHook:         func(string, string) error { return nil },
		ContextModeEnabled: func() (bool, error) { checked = true; return false, nil },
		startExecd:         testExecdStart("/tmp/test.sock", nil),
		startShellWrapper:  testWrapperStart("/tmp/test-norc-bash-1", nil),
		supervise:          func(string, []string) int { return 0 },
		exit:               func(int) {},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if checked {
		t.Error("a session without the flag must not ask about context-mode at all")
	}
}

func TestRun_ContextModeProbeFailure_DoesNotLaunch(t *testing.T) {
	makeFakeNono(t)
	superviseCalls := 0
	err := run(&config.Config{}, Options{ContextMode: true}, runDeps{
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		verifyHook:         func(string, string) error { return nil },
		ContextModeEnabled: func() (bool, error) { return true, nil },
		verifyContextMode: func(string) error {
			return errors.New("the profile does not forward CONTEXT_MODE_EXEC_BACKEND")
		},
		startExecd:        testExecdStart("/tmp/test.sock", nil),
		startShellWrapper: testWrapperStart("/tmp/test-norc-bash-1", nil),
		supervise:         func(string, []string) int { superviseCalls++; return 0 },
		exit:              func(int) {},
	})
	if err == nil {
		t.Fatal("expected an error when the probe fails")
	}
	if superviseCalls != 0 {
		t.Errorf("claude was launched %d times despite a failed probe, want 0", superviseCalls)
	}
}

// The probe reads the variables out of the sandbox, so they must already be
// published when it runs.
func TestRun_ContextModeProbeRunsAfterTheEnvIsSet(t *testing.T) {
	makeFakeNono(t)
	t.Setenv(ContextModeEnvVar, "")
	var seen string
	err := run(&config.Config{}, Options{ContextMode: true}, runDeps{
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		verifyHook:         func(string, string) error { return nil },
		ContextModeEnabled: func() (bool, error) { return true, nil },
		verifyContextMode: func(string) error {
			seen = os.Getenv(ContextModeEnvVar)
			return nil
		},
		startExecd:        testExecdStart("/tmp/test.sock", nil),
		startShellWrapper: testWrapperStart("/tmp/test-norc-bash-1", nil),
		supervise:         func(string, []string) int { return 0 },
		exit:              func(int) {},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seen != "execd" {
		t.Errorf("%s at probe time = %q, want %q", ContextModeEnvVar, seen, "execd")
	}
}

// herdrRun runs the launcher with every dependency stubbed. It reports
// whether run re-exec'd, the environment it re-exec'd with, and whether it
// went on to start execd.
func herdrRun(t *testing.T) (reexeced bool, env []string, started bool) {
	t.Helper()
	makeFakeNono(t)
	err := run(&config.Config{}, Options{}, runDeps{
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		verifyHook:         func(string, string) error { return nil },
		ContextModeEnabled: func() (bool, error) { return false, nil },
		startExecd: func(*config.Config) (string, string, func(), error) {
			started = true
			return "/tmp/test.sock", testExecdPath, func() {}, nil
		},
		startShellWrapper: testWrapperStart("/tmp/test-norc-bash-1", nil),
		reexec: func(_ string, _ []string, e []string) error {
			reexeced, env = true, e
			return nil
		},
		supervise: func(string, []string) int { return 0 },
		exit:      func(int) {},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return reexeced, env, started
}

// Under `nono run` claude lives on nono's own pty, so herdr sees only the
// launcher and nono in the pane's foreground job. herdr reads HERDR_AGENT from
// /proc/<pid>/environ, which shows a process's environment as of its execve:
// nono soon makes its own unreadable, and an os.Setenv in the launcher never
// appears there. So inside herdr the launcher re-execs itself with the hint,
// before it starts anything.
func TestRun_ReexecsWithHerdrHintInsideHerdr(t *testing.T) {
	t.Setenv(herdrPaneEnvVar, "w1:p1")
	t.Setenv(HerdrAgentEnvVar, "")
	os.Unsetenv(HerdrAgentEnvVar)
	reexeced, env, started := herdrRun(t)
	if !reexeced {
		t.Fatal("run did not re-exec inside herdr without HERDR_AGENT")
	}
	if !slices.Contains(env, HerdrAgentEnvVar+"=claude") {
		t.Errorf("re-exec env lacks %s=claude: %v", HerdrAgentEnvVar, env)
	}
	if started {
		t.Error("run started execd before re-exec'ing; the re-exec'd launcher would start a second one")
	}
}

// An operator-set hint wins, and it also ends the re-exec: the re-exec'd
// launcher sees the hint and carries on.
func TestRun_NoReexecWhenHerdrHintPresent(t *testing.T) {
	t.Setenv(herdrPaneEnvVar, "w1:p1")
	t.Setenv(HerdrAgentEnvVar, "custom")
	reexeced, _, started := herdrRun(t)
	if reexeced {
		t.Error("run re-exec'd although HERDR_AGENT was already set")
	}
	if !started {
		t.Error("run did not go on to launch")
	}
}

// Outside herdr nothing reads the hint, so the launcher neither re-execs nor
// publishes it.
func TestRun_NoReexecOutsideHerdr(t *testing.T) {
	t.Setenv(herdrPaneEnvVar, "")
	os.Unsetenv(herdrPaneEnvVar)
	t.Setenv(HerdrAgentEnvVar, "")
	os.Unsetenv(HerdrAgentEnvVar)
	reexeced, _, started := herdrRun(t)
	if reexeced {
		t.Error("run re-exec'd outside herdr")
	}
	if !started {
		t.Error("run did not go on to launch")
	}
	if v, ok := os.LookupEnv(HerdrAgentEnvVar); ok {
		t.Errorf("%s = %q outside herdr, want unset", HerdrAgentEnvVar, v)
	}
}

func TestBuildArgs_GrantsTheExecdBinary(t *testing.T) {
	makeFakeNono(t)
	_, args, err := BuildArgs(&config.Config{}, Options{}, "", "", "", "/opt/bin/execd")
	if err != nil {
		t.Fatalf("BuildArgs() error = %v", err)
	}
	if !slices.Contains(argPairs(args, "--read-file"), "/opt/bin/execd") {
		t.Errorf("BuildArgs() = %v, want --read-file /opt/bin/execd so the agent can run execd exec", args)
	}
}

// argPairs returns every value that directly follows flag in args.
func argPairs(args []string, flag string) []string {
	var out []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			out = append(out, args[i+1])
		}
	}
	return out
}

func TestResolveExecd_NotOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := ResolveExecd()
	if err == nil || !strings.Contains(err.Error(), "go install github.com/ynny-github/agent-sandbox/cmd/execd@latest") {
		t.Fatalf("ResolveExecd() error = %v, want the install hint", err)
	}
}

func TestResolveExecd_ResolvesSymlinks(t *testing.T) {
	real := filepath.Join(t.TempDir(), "execd-real")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(real, filepath.Join(bin, "execd")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	got, err := ResolveExecd()
	if err != nil {
		t.Fatalf("ResolveExecd() error = %v", err)
	}
	want, _ := filepath.EvalSymlinks(real)
	if got != want {
		t.Errorf("ResolveExecd() = %q, want the symlink target %q (Landlock checks the target)", got, want)
	}
}

func TestStartExecd_ExecdNotInPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nono"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, _, cleanup, err := startExecd(&config.Config{})
	if err == nil || !strings.Contains(err.Error(), "cmd/execd@latest") {
		t.Fatalf("startExecd() error = %v, want the execd install hint", err)
	}
	if cleanup != nil {
		t.Error("cleanup should be nil on error")
	}
}

// A missing execd must stop the launch before claude starts, and must still
// remove the shell wrapper written earlier in the launch.
func TestRun_ExecdMissing_DoesNotLaunch(t *testing.T) {
	makeFakeNono(t)
	superviseCalls, wrapperCleaned := 0, 0
	err := run(&config.Config{}, Options{}, runDeps{
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		verifyHook: func(string, string) error { return nil },
		startShellWrapper: func() (string, func(), error) {
			return "/tmp/w", func() { wrapperCleaned++ }, nil
		},
		startExecd: func(*config.Config) (string, string, func(), error) {
			return "", "", nil, errors.New("execd not found in PATH; install it with " +
				"`go install github.com/ynny-github/agent-sandbox/cmd/execd@latest`")
		},
		supervise: func(string, []string) int { superviseCalls++; return 0 },
		exit:      func(int) {},
	})
	if err == nil || !strings.Contains(err.Error(), "cmd/execd@latest") {
		t.Fatalf("run() error = %v, want the execd install hint", err)
	}
	if superviseCalls != 0 {
		t.Errorf("claude was launched without execd (supervise called %d times)", superviseCalls)
	}
	if wrapperCleaned != 1 {
		t.Errorf("shell wrapper cleanup ran %d times, want 1", wrapperCleaned)
	}
}

// A command running under the command profile must not be able to choose the
// execd the next launch grants to the agent and runs as the daemon — by
// rewriting the installed one, or by planting an `execd` in a writable
// directory earlier on PATH. startExecd refuses either before starting
// anything.
func TestStartExecd_RefusesAnExecdTheCommandProfileCanWrite(t *testing.T) {
	dir := t.TempDir()
	nono := "#!/bin/sh\nif [ \"$1\" = why ]; then echo '{\"status\":\"allowed\"}'; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "nono"), []byte(nono), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "execd"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	_, _, cleanup, err := startExecd(&config.Config{})
	if err == nil || !strings.Contains(err.Error(), "command profile can write") {
		t.Fatalf("startExecd() error = %v, want a refusal naming the writable execd", err)
	}
	if cleanup != nil {
		t.Error("cleanup should be nil on error")
	}
}

// The session socket's bind grant reaches its parent directory, so that
// directory must not be the one holding the agent's shell wrapper.
func TestExecdSessionSocket_IsNotBesideTheShellWrapper(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	sock, err := execd.SessionSocket("/p/command-profile.json", "/w")
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := ShellWrapperPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(sock) == filepath.Dir(wrapper) || filepath.Dir(filepath.Dir(sock)) == filepath.Dir(wrapper) {
		t.Errorf("socket %s sits in or beside the shell wrapper's directory %s", sock, filepath.Dir(wrapper))
	}
}

func TestStartExecd_RunsUpAndDown(t *testing.T) {
	nono := makeFakeNono(t)
	dir := filepath.Dir(nono)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	sock, execdPath, cleanup, err := startExecd(&config.Config{})
	if err != nil {
		t.Fatalf("startExecd() error = %v", err)
	}
	if want := filepath.Join(dir, "fake.sock"); sock != want {
		t.Errorf("socket = %q, want what execd up printed, %q", sock, want)
	}
	if execdPath != filepath.Join(dir, "execd") {
		t.Errorf("execd path = %q", execdPath)
	}
	cleanup()
	got, err := os.ReadFile(filepath.Join(dir, "down-arg"))
	if err != nil || strings.TrimSpace(string(got)) != sock {
		t.Errorf("execd down got %q (err %v), want %q", got, err, sock)
	}
}

func TestStartExecd_UpFailureFailsTheLaunch(t *testing.T) {
	nono := makeFakeNono(t)
	failing := "#!/bin/sh\necho 'profile rejected' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(nono), "execd"), []byte(failing), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, _, cleanup, err := startExecd(&config.Config{})
	if err == nil || !strings.Contains(err.Error(), "execd up") {
		t.Fatalf("startExecd() error = %v, want an execd up failure", err)
	}
	if cleanup != nil {
		t.Error("cleanup should be nil on error")
	}
}
