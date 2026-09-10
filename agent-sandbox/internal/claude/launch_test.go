package claude

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/agentconfig"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxhost"
)

// TestMain clears GITHUB_MCP_TOKEN so the package's tests are hermetic: run()
// gates the github MCP path on GithubMCPEnabled(), which reads this variable,
// and an ambient token would otherwise drive tests that don't inject a
// writeMCPConfig dep into that path. Tests exercising the MCP path set the
// variable explicitly with t.Setenv.
func TestMain(m *testing.M) {
	os.Unsetenv("GITHUB_MCP_TOKEN")
	os.Exit(m.Run())
}

func TestValidatePassthrough_SettingsBlocked(t *testing.T) {
	if err := ValidatePassthrough([]string{"--settings", "foo.json"}, false); err == nil {
		t.Fatal("expected error for --settings, got nil")
	}
}

func TestValidatePassthrough_SettingsEqualBlocked(t *testing.T) {
	if err := ValidatePassthrough([]string{"--settings=foo.json"}, false); err == nil {
		t.Fatal("expected error for --settings=..., got nil")
	}
}

func TestValidatePassthrough_AllowsOtherArgs(t *testing.T) {
	if err := ValidatePassthrough([]string{"--print", "--model", "opus"}, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePassthrough_Empty(t *testing.T) {
	if err := ValidatePassthrough(nil, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePassthrough_MCPConfigBlockedWhenEnabled(t *testing.T) {
	if err := ValidatePassthrough([]string{"--mcp-config", "x.json"}, true); err == nil {
		t.Fatal("expected error for --mcp-config when github mcp enabled (GITHUB_MCP_TOKEN set), got nil")
	}
	if err := ValidatePassthrough([]string{"--strict-mcp-config"}, true); err == nil {
		t.Fatal("expected error for --strict-mcp-config when enabled, got nil")
	}
}

func TestValidatePassthrough_MCPConfigAllowedWhenDisabled(t *testing.T) {
	if err := ValidatePassthrough([]string{"--mcp-config", "x.json"}, false); err != nil {
		t.Fatalf("unexpected error when github mcp disabled (GITHUB_MCP_TOKEN unset): %v", err)
	}
}

func TestBuildArgs_NonoNotInPath(t *testing.T) {
	t.Setenv("PATH", "")
	cfg := &config.Config{}
	if _, _, err := BuildArgs(cfg, Options{}, "", "", ""); err == nil {
		t.Fatal("expected error when nono not in PATH, got nil")
	}
}

func makeFakeNono(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "nono")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return path
}

// makeFakeNonoWedged writes a "nono" that creates the socket named after
// "--allow-unix-socket-bind" (standing in for a broker session that started
// fine), then ignores SIGTERM and sleeps, standing in for a broker that never
// exits on its own. It is what TestStartCommandBroker_CleanupKillsAWedgedBroker
// uses to prove teardown escalates to SIGKILL rather than waiting forever.
func makeFakeNonoWedged(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "nono")
	// touch and sleep are external binaries and this script's own PATH is
	// deliberately just this fake nono's directory (so it, not any real nono,
	// is what gets resolved), so both the socket creation and the "never
	// exits" wait use only sh builtins: redirection and a busy loop.
	script := `#!/bin/sh
sock=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--allow-unix-socket-bind" ]; then
    sock="$arg"
  fi
  prev="$arg"
done
: > "$sock"
trap '' TERM
while :; do :; done
`
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
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

func TestBuildArgs_AlwaysUsesWrap(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{}
	_, args, err := BuildArgs(cfg, Options{}, "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(args) < 2 {
		t.Fatalf("expected at least 2 args, got %v", args)
	}
	if args[0] != "nono" {
		t.Errorf("args[0] = %q, want \"nono\"; full args: %v", args[0], args)
	}
	if args[1] != "wrap" {
		t.Errorf("args[1] = %q, want \"wrap\"; full args: %v", args[1], args)
	}
}

func TestBuildArgs_McpMode_DisablesTools(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{ToolMode: "mcp"}
	_, args, err := BuildArgs(cfg, Options{}, "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !argsContain(args, "--disallowed-tools") || !argsContain(args, "Bash,Monitor") {
		t.Errorf("mcp mode should disable Bash,Monitor; got %v", args)
	}
}

func TestBuildArgs_HookMode_InjectsSettings(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{ToolMode: "hook"}
	_, args, err := BuildArgs(cfg, Options{}, "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if argsContain(args, "--disallowed-tools") {
		t.Errorf("hook mode should not disable tools; got %v", args)
	}

	ci := argsIndex(args, "claude")

	si := argsIndex(args, "--settings")
	if si < 0 || si+1 >= len(args) {
		t.Fatalf("hook mode should inject --settings with a value; got %v", args)
	}
	val := args[si+1]
	if !strings.Contains(val, `"PreToolUse"`) ||
		!strings.Contains(val, "agent-sandbox hook") {
		t.Errorf("--settings value missing the hook config; got %q", val)
	}
	if si < ci {
		t.Errorf("--settings must appear after claude; got %v", args)
	}
}

func TestBuildArgs_McpMode_NoReadFile(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{ToolMode: "mcp"}
	_, args, err := BuildArgs(cfg, Options{}, "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if argsContain(args, "--read-file") {
		t.Errorf("mcp mode should not grant --read-file; got %v", args)
	}
}

func TestBuildArgs_InjectsProfileBeforeClaude(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{ToolMode: "mcp"}
	_, args, err := BuildArgs(cfg, Options{}, "", "/tmp/asb-profile-1.json", "")
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
	cfg := &config.Config{ToolMode: "mcp"}
	_, args, err := BuildArgs(cfg, Options{ClaudeOpts: []string{"--model", "opus"}}, "", "", "")
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
	cfg := &config.Config{ToolMode: "mcp"}
	_, args, err := BuildArgs(cfg, Options{}, "", "", "")
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

func TestBuildArgs_InjectsMCPConfig(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{ToolMode: "mcp"}
	_, args, err := BuildArgs(cfg, Options{}, "/tmp/asb-mcp-1.json", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ci := argsIndex(args, "claude")

	ri := argsIndex(args, "--read-file")
	if ri < 0 || args[ri+1] != "/tmp/asb-mcp-1.json" || ri > ci {
		t.Fatalf("expected --read-file <mcp path> before claude; got %v", args)
	}
	if !argsContain(args, "--strict-mcp-config") {
		t.Errorf("missing --strict-mcp-config; got %v", args)
	}
	mi := argsIndex(args, "--mcp-config")
	if mi < 0 || args[mi+1] != "/tmp/asb-mcp-1.json" || mi < ci {
		t.Errorf("expected --mcp-config <path> after claude; got %v", args)
	}
	si := argsIndex(args, "--settings")
	if si < 0 || !strings.Contains(args[si+1], "Read(//tmp/asb-mcp-1.json)") {
		t.Errorf("expected --settings with a deny rule for the mcp path; got %v", args)
	}
}

func TestBuildArgs_HookMode_MCPConfig_DenyAndHooks(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{ToolMode: "hook"}
	_, args, err := BuildArgs(cfg, Options{}, "/tmp/asb-mcp-1.json", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	si := argsIndex(args, "--settings")
	if si < 0 {
		t.Fatal("expected --settings")
	}
	val := args[si+1]
	if !strings.Contains(val, "PreToolUse") || !strings.Contains(val, "Read(//tmp/asb-mcp-1.json)") {
		t.Errorf("hook+mcp settings should contain both hooks and deny; got %q", val)
	}
}

func TestBuildArgs_NoMCPConfig_Unchanged(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{ToolMode: "mcp"}
	_, args, err := BuildArgs(cfg, Options{}, "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if argsContain(args, "--mcp-config") || argsContain(args, "--strict-mcp-config") {
		t.Errorf("no mcp flags expected when path empty; got %v", args)
	}
	if argsContain(args, "--settings") {
		t.Errorf("mcp mode with no mcp path should have no --settings; got %v", args)
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
	if err == nil || !strings.Contains(err.Error(), "sandbox.host") {
		t.Fatalf("expected --profile rejection pointing to [sandbox.host], got %v", err)
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

func TestBuildArgs_GrantsBrokerSocket(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{ToolMode: "hook"}
	_, args, err := BuildArgs(cfg, Options{}, "", "", "/tmp/b.sock")
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

func TestEnvKeys_ReachProfileAllowVars(t *testing.T) {
	cfg := &config.Config{ToolMode: "hook"}
	cfg.Sandbox.Agent.AllowEnv = append(cfg.Sandbox.Agent.AllowEnv, "MY_SECRET_KEY")
	r, err := sandboxhost.Resolve(cfg, "claude")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	data, err := r.ProfileJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "MY_SECRET_KEY") {
		t.Errorf("profile allow_vars missing MY_SECRET_KEY: %s", data)
	}
}

// --- lifecycle orchestration (Task 4) ---
//
// The Docker-lifecycle tests that used to live here (fakeHandle / ensureUp /
// Started / Down) no longer apply: run() no longer owns a sandbox lifecycle,
// it starts and tears down the command broker instead. They are replaced by
// the broker-focused tests below; startBroker replaces the deleted ensureUp
// field everywhere else.

func testBrokerStart(sock string, cleaned *int) func(*config.Config) (string, func(), error) {
	return func(*config.Config) (string, func(), error) {
		return sock, func() {
			if cleaned != nil {
				*cleaned++
			}
		}, nil
	}
}

func TestRun_StartBrokerFailure_DoesNotLaunch(t *testing.T) {
	makeFakeNono(t)
	superviseCalls := 0
	exitCalls := 0
	err := run(&config.Config{ToolMode: "mcp"}, Options{}, runDeps{
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		startBroker: func(*config.Config) (string, func(), error) {
			return "", nil, errors.New("broker start error")
		},
		supervise: func(string, []string) int { superviseCalls++; return 0 },
		exit:      func(int) { exitCalls++ },
	})
	if err == nil {
		t.Fatal("expected error when startBroker fails, got nil")
	}
	if superviseCalls != 0 {
		t.Errorf("claude was launched despite broker failure (supervise called %d times)", superviseCalls)
	}
	if exitCalls != 0 {
		t.Errorf("exit called %d times, want 0 on hard-fail", exitCalls)
	}
}

func TestRun_ExitReceivesSuperviseCode(t *testing.T) {
	makeFakeNono(t)
	gotExit := -1
	err := run(&config.Config{ToolMode: "mcp"}, Options{}, runDeps{
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		startBroker: testBrokerStart("/tmp/test.sock", nil),
		supervise:   func(string, []string) int { return 3 },
		exit:        func(code int) { gotExit = code },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotExit != 3 {
		t.Errorf("exit code = %d, want 3 (claude's code)", gotExit)
	}
}

func TestRun_BrokerCleanupBeforeExit(t *testing.T) {
	makeFakeNono(t)
	cleaned := 0
	cleanedBeforeExit := false
	err := run(&config.Config{ToolMode: "mcp"}, Options{}, runDeps{
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		startBroker: testBrokerStart("/tmp/test.sock", &cleaned),
		supervise:   func(string, []string) int { return 0 },
		exit:        func(int) { cleanedBeforeExit = cleaned == 1 },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cleanedBeforeExit {
		t.Error("broker cleanup must run before exit; os.Exit skips deferred cleanup in production")
	}
}

func TestRun_SetsBrokerSocketEnvBeforeSupervise(t *testing.T) {
	makeFakeNono(t)
	// t.Setenv restores whatever this variable held before the test once the
	// test finishes, so run()'s own os.Setenv call below doesn't leak into
	// the rest of the binary.
	t.Setenv(broker.SocketEnvVar, "")
	const wantSocket = "/tmp/test-env-handoff.sock"
	var gotEnv string
	err := run(&config.Config{ToolMode: "mcp"}, Options{}, runDeps{
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		startBroker: testBrokerStart(wantSocket, nil),
		supervise: func(string, []string) int {
			gotEnv = os.Getenv(broker.SocketEnvVar)
			return 0
		},
		exit: func(int) {},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotEnv != wantSocket {
		t.Errorf("%s at supervise time = %q, want %q (the broker socket, set before supervise runs)",
			broker.SocketEnvVar, gotEnv, wantSocket)
	}
}

func TestRun_GithubMCPEnabled_WritesAndCleansConfig(t *testing.T) {
	makeFakeNono(t)
	t.Setenv("GITHUB_MCP_TOKEN", "ghp_x")
	wrote, cleaned := 0, 0
	cfg := &config.Config{ToolMode: "mcp"}
	err := run(cfg, Options{}, runDeps{
		writeMCPConfig: func(*config.Config) (string, func(), error) {
			wrote++
			return "/tmp/asb-mcp-1.json", func() { cleaned++ }, nil
		},
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		startBroker: testBrokerStart("/tmp/test.sock", nil),
		supervise:   func(string, []string) int { return 0 },
		exit:        func(int) {},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wrote != 1 {
		t.Errorf("writeMCPConfig called %d times, want 1", wrote)
	}
	if cleaned != 1 {
		t.Errorf("mcp config cleanup called %d times, want 1", cleaned)
	}
}

func TestRun_GithubMCPDisabled_SkipsConfig(t *testing.T) {
	makeFakeNono(t)
	t.Setenv("GITHUB_MCP_TOKEN", "")
	wrote := 0
	err := run(&config.Config{ToolMode: "mcp"}, Options{}, runDeps{
		writeMCPConfig: func(*config.Config) (string, func(), error) { wrote++; return "", func() {}, nil },
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
		startBroker: testBrokerStart("/tmp/test.sock", nil),
		supervise:   func(string, []string) int { return 0 },
		exit:        func(int) {},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wrote != 0 {
		t.Errorf("writeMCPConfig called %d times, want 0 when disabled", wrote)
	}
}

// startCommandBroker now launches a real `nono run` session (see BrokerArgs),
// so it can no longer be exercised end-to-end without a real nono binary and
// a real sandbox — out of scope for this package's tests (see task-4-report.md
// for why). What stays testable without spawning anything is its plumbing:
// like BuildArgs, it must fail fast when nono is not on PATH rather than
// attempting to start a session it cannot run.
func TestStartCommandBroker_NonoNotInPath(t *testing.T) {
	t.Setenv("PATH", "")
	sock, cleanup, err := startCommandBroker(&config.Config{})
	if err == nil {
		t.Fatal("startCommandBroker() error = nil, want error when nono is not in PATH")
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

// TestStartCommandBroker_ReportsChildExitBeforeBinding is the integration-level
// version of TestWaitForSocketOrExit_ChildExitsFirst: with a fake "nono" that
// exits 0 without ever binding a socket (standing in for nono rejecting the
// command profile), startCommandBroker must fail fast with a specific error
// rather than blocking for the full brokerStartTimeout and reporting a bare
// "did not start" message.
func TestStartCommandBroker_ReportsChildExitBeforeBinding(t *testing.T) {
	makeFakeNono(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	start := time.Now()
	sock, cleanup, err := startCommandBroker(&config.Config{})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("startCommandBroker() took %s, want it to fail fast once the child exits", elapsed)
	}
	if err == nil {
		t.Fatal("startCommandBroker() error = nil, want an error when the child exits before binding")
	}
	if !strings.Contains(err.Error(), "exited") {
		t.Errorf("err = %v, want it to mention the child exiting", err)
	}
	if sock != "" {
		t.Errorf("socket = %q, want empty on error", sock)
	}
	if cleanup != nil {
		t.Error("cleanup should be nil on error")
	}
}

// TestStartCommandBroker_CleanupKillsAWedgedBroker is the regression test for
// Important Finding 2: teardown must not trust a signaled broker to exit and
// wait on it forever. With a fake "nono" that ignores SIGTERM entirely,
// cleanup must still return — bounded by brokerStopTimeout, shrunk here so the
// test does not spend real seconds proving it — rather than hang
// agent-sandbox claude after the agent has already exited.
func TestStartCommandBroker_CleanupKillsAWedgedBroker(t *testing.T) {
	old := brokerStopTimeout
	brokerStopTimeout = 200 * time.Millisecond
	t.Cleanup(func() { brokerStopTimeout = old })

	makeFakeNonoWedged(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	sock, cleanup, err := startCommandBroker(&config.Config{})
	if err != nil {
		t.Fatalf("startCommandBroker() error = %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup = nil, want a cleanup function")
	}
	if _, statErr := os.Stat(sock); statErr != nil {
		t.Fatalf("socket %s missing before cleanup: %v", sock, statErr)
	}

	start := time.Now()
	cleanup()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("cleanup() took %s, want it bounded by brokerStopTimeout instead of hanging", elapsed)
	}
	if _, statErr := os.Stat(sock); !os.IsNotExist(statErr) {
		t.Errorf("socket %s still exists after cleanup (stat err = %v), want it removed", sock, statErr)
	}
}

func TestBrokerArgsRunsTheBrokerUnderTheCommandProfile(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(profile, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	cfg := loadConfigWithCommandProfile(t, dir, profile)

	args := BrokerArgs(cfg, "/usr/bin/nono", "/opt/agent-sandbox/bin/agent-sandbox",
		"/run/b.sock", "/work/project")

	joined := strings.Join(args, " ")
	for _, want := range []string{
		"run", "--silent",
		"--profile " + profile,
		"--workdir /work/project",
		"--allow-unix-socket-bind /run/b.sock",
		"--read-file /opt/agent-sandbox/bin/agent-sandbox",
		"-- /opt/agent-sandbox/bin/agent-sandbox broker --socket /run/b.sock",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("BrokerArgs() = %q\nmissing %q", joined, want)
		}
	}
	if strings.Contains(joined, "--allow-cwd") {
		t.Errorf("BrokerArgs() grants --allow-cwd; the working directory comes from the profile's $WORKDIR")
	}
}

// TestBrokerArgsGrantsItsOwnBinaryAndInvokesItAbsolutely pins the two halves
// of one ruling: the broker's ability to start is the launcher's to guarantee,
// not the operator profile's to remember.
//
// --read-file covers the binary (a read grant carries the execute right).
// Measured: under a profile granting neither, an absolute-path invocation of
// the binary exits 127 with no output; --read-file alone makes it run.
//
// The entrypoint is then invoked by that same absolute path. It was invoked by
// base name only because tool-sandbox refuses an absolute-path invocation of a
// policy-controlled command as a direct exec bypass; with command_policies gone
// that constraint is gone, and base-name resolution would leave its own hazard —
// a stale copy earlier on the launcher's PATH silently becoming the broker.
func TestBrokerArgsGrantsItsOwnBinaryAndInvokesItAbsolutely(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(profile, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	cfg := loadConfigWithCommandProfile(t, dir, profile)
	const self = "/opt/agent-sandbox/bin/agent-sandbox"

	args := BrokerArgs(cfg, "/usr/bin/nono", self, "/run/b.sock", "/work/project")

	readFile := -1
	dashIdx := -1
	for i, a := range args {
		switch a {
		case "--read-file":
			readFile = i
		case "--":
			if dashIdx < 0 {
				dashIdx = i
			}
		}
	}
	if readFile < 0 || readFile+1 >= len(args) || args[readFile+1] != self {
		t.Errorf("BrokerArgs() = %v, want --read-file %s so the profile need not grant the broker's own binary", args, self)
	}
	if readFile > dashIdx {
		t.Errorf("BrokerArgs() puts --read-file after --, where nono would pass it to the broker instead of reading it")
	}
	if dashIdx < 0 || dashIdx+1 >= len(args) {
		t.Fatalf("BrokerArgs() = %v, no entrypoint after --", args)
	}
	if got := args[dashIdx+1]; got != self {
		t.Errorf("entrypoint = %q, want selfPath's absolute form %q", got, self)
	}
}

// TestBrokerArgsUsesTheResolvedNonoPath guards against BrokerArgs silently
// discarding nonoPath: `agent-sandbox debug` exists specifically to print the
// invocation the launcher really builds, and a hardcoded "nono" in argv[0]
// would defeat that the moment the resolved binary isn't the first "nono" on
// PATH.
func TestBrokerArgsUsesTheResolvedNonoPath(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(profile, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	cfg := loadConfigWithCommandProfile(t, dir, profile)

	args := BrokerArgs(cfg, "/opt/nono/bin/nono", "/opt/agent-sandbox/bin/agent-sandbox",
		"/run/b.sock", "/work/project")

	if len(args) == 0 || args[0] != "/opt/nono/bin/nono" {
		t.Errorf("args[0] = %v, want the resolved nono path %q", args, "/opt/nono/bin/nono")
	}
}

func TestWaitForSocketOrExit_SocketAppears(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "s.sock")
	exited := make(chan error, 1) // never sent to: the child stays "running"
	go func() {
		time.Sleep(20 * time.Millisecond)
		if err := os.WriteFile(sock, nil, 0o600); err != nil {
			t.Error(err)
		}
	}()

	childExited, err := waitForSocketOrExit(sock, 2*time.Second, exited)
	if err != nil {
		t.Fatalf("waitForSocketOrExit() error = %v, want nil", err)
	}
	if childExited {
		t.Error("childExited = true, want false: the child never exited on this path")
	}
}

// TestWaitForSocketOrExit_ChildExitsFirst is the case Important Finding 1
// exists to fix: a broker nono rejects (a bad profile, say) exits almost
// immediately, and that must surface as a fast, specific error instead of the
// launcher blocking for the full startup timeout and then reporting a
// generic "did not start" message.
func TestWaitForSocketOrExit_ChildExitsFirst(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "s.sock") // never created
	exited := make(chan error, 1)
	exited <- errors.New("exit status 2")

	start := time.Now()
	childExited, err := waitForSocketOrExit(sock, 2*time.Second, exited)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("waitForSocketOrExit() took %s, want it to return promptly on child exit", elapsed)
	}
	if !childExited {
		t.Error("childExited = false, want true")
	}
	if err == nil || !strings.Contains(err.Error(), "exited before binding its socket") {
		t.Errorf("err = %v, want it to say the child exited before binding", err)
	}
	if err == nil || !strings.Contains(err.Error(), "exit status 2") {
		t.Errorf("err = %v, want it to wrap the underlying exit error", err)
	}
}

// TestWaitForSocketOrExit_ChildExitsCleanly guards the nil-error edge case: a
// nil error from cmd.Wait means the process exited with status 0, which %w
// would otherwise render as a broken "%!w(<nil>)" message.
func TestWaitForSocketOrExit_ChildExitsCleanly(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "s.sock")
	exited := make(chan error, 1)
	exited <- nil

	childExited, err := waitForSocketOrExit(sock, 2*time.Second, exited)
	if !childExited {
		t.Error("childExited = false, want true")
	}
	if err == nil || !strings.Contains(err.Error(), "status 0") {
		t.Errorf("err = %v, want it to mention exit status 0", err)
	}
}

func TestWaitForSocketOrExit_Timeout(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "s.sock")
	exited := make(chan error, 1) // never sent to

	childExited, err := waitForSocketOrExit(sock, 50*time.Millisecond, exited)
	if childExited {
		t.Error("childExited = true, want false: nothing was ever sent on exited")
	}
	if err == nil || !strings.Contains(err.Error(), "did not start within") {
		t.Errorf("err = %v, want the timeout message", err)
	}
}

// loadConfigWithCommandProfile writes a minimal project config in dir pointing
// at profile and loads it, so the test exercises the same resolution the
// launcher uses rather than a hand-built Config.
func loadConfigWithCommandProfile(t *testing.T, dir, profile string) *config.Config {
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

// writeLaunchFixture writes a loadable config plus the two profiles beside it
// and returns the directory and the loaded config.
func writeLaunchFixture(t *testing.T, withAgentProfile bool) (string, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	body := "tool_mode = \"mcp\"\n\n[mcp]\ncommand_output_dir = \"/tmp/asb-out\"\n"
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
		agentProfile: defaultAgentProfile,
		startBroker:  testBrokerStart("/tmp/test.sock", nil),
		supervise:    func(_ string, args []string) int { gotArgs = args; return 0 },
		exit:         func(int) {},
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
		agentProfile: defaultAgentProfile,
		startBroker:  testBrokerStart("/tmp/test.sock", nil),
		supervise:    func(string, []string) int { supervised++; return 0 },
		exit:         func(int) {},
	})
	if !errors.Is(err, config.ErrAgentProfileMissing) {
		t.Fatalf("run error = %v, want ErrAgentProfileMissing", err)
	}
	if supervised != 0 {
		t.Errorf("claude must not be launched when the agent profile is missing")
	}
}
