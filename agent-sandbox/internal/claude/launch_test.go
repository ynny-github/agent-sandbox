package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/agentconfig"
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
	if _, _, err := BuildArgs(cfg, Options{}, "", "", "", nil); err == nil {
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
	_, args, err := BuildArgs(cfg, Options{}, "", "", "", nil)
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
	_, args, err := BuildArgs(cfg, Options{}, "", "", "", nil)
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
	_, args, err := BuildArgs(cfg, Options{}, "/state/policy-1.json", "", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if argsContain(args, "--disallowed-tools") {
		t.Errorf("hook mode should not disable tools; got %v", args)
	}

	ci := argsIndex(args, "claude")

	ri := argsIndex(args, "--read-file")
	if ri < 0 || ri+1 >= len(args) || args[ri+1] != "/state/policy-1.json" {
		t.Fatalf("hook mode should grant --read-file for the snapshot; got %v", args)
	}
	if ri > ci {
		t.Errorf("--read-file must appear before claude; got %v", args)
	}

	si := argsIndex(args, "--settings")
	if si < 0 || si+1 >= len(args) {
		t.Fatalf("hook mode should inject --settings with a value; got %v", args)
	}
	val := args[si+1]
	if !strings.Contains(val, `"PreToolUse"`) ||
		!strings.Contains(val, "agent-sandbox hook --policy-file '/state/policy-1.json'") {
		t.Errorf("--settings value missing policy-file hook config; got %q", val)
	}
	if si < ci {
		t.Errorf("--settings must appear after claude; got %v", args)
	}
}

func TestBuildArgs_McpMode_NoReadFile(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{ToolMode: "mcp"}
	_, args, err := BuildArgs(cfg, Options{}, "", "", "", nil)
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
	_, args, err := BuildArgs(cfg, Options{}, "", "", "/tmp/asb-profile-1.json", nil)
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

func TestBuildArgs_InjectsCapabilityDeny(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{ToolMode: "mcp"}
	_, args, err := BuildArgs(cfg, Options{}, "", "", "/tmp/p.json", []string{"Read(~/.ssh/**)"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	si := argsIndex(args, "--settings")
	if si < 0 || !strings.Contains(args[si+1], "Read(~/.ssh/**)") {
		t.Errorf("expected --settings with capability deny rule; got %v", args)
	}
}

func TestBuildArgs_ClaudeOptsAfterClaude(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{ToolMode: "mcp"}
	_, args, err := BuildArgs(cfg, Options{ClaudeOpts: []string{"--model", "opus"}}, "", "", "", nil)
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
	_, args, err := BuildArgs(cfg, Options{}, "", "", "", nil)
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
	_, args, err := BuildArgs(cfg, Options{}, "", "/tmp/asb-mcp-1.json", "", nil)
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
	_, args, err := BuildArgs(cfg, Options{}, "/state/p.json", "/tmp/asb-mcp-1.json", "", nil)
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
	_, args, err := BuildArgs(cfg, Options{}, "", "", "", nil)
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

func TestEnvKeys_ReachProfileAllowVars(t *testing.T) {
	cfg := &config.Config{ToolMode: "hook"}
	cfg.Sandbox.Host.AllowEnv = append(cfg.Sandbox.Host.AllowEnv, "MY_SECRET_KEY")
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

type fakeHandle struct {
	started   bool
	downCalls int
	downErr   error
	closed    bool
}

func (h *fakeHandle) Started() bool              { return h.started }
func (h *fakeHandle) Down(context.Context) error { h.downCalls++; return h.downErr }
func (h *fakeHandle) Close()                     { h.closed = true }

func TestRun_EnsureUpFailure_DoesNotLaunch(t *testing.T) {
	makeFakeNono(t)
	superviseCalls := 0
	exitCalls := 0
	err := run(&config.Config{ToolMode: "mcp"}, Options{}, runDeps{
		writeProfile: func(*config.Config) (string, []string, func(), error) {
			return "/tmp/asb-profile-1.json", nil, func() {}, nil
		},
		ensureUp: func(context.Context, *config.Config) (sandboxHandle, error) {
			return nil, errors.New("docker daemon error")
		},
		supervise: func(string, []string) int { superviseCalls++; return 0 },
		exit:      func(int) { exitCalls++ },
	})
	if err == nil {
		t.Fatal("expected error when EnsureUp fails, got nil")
	}
	if superviseCalls != 0 {
		t.Errorf("claude was launched despite sandbox failure (supervise called %d times)", superviseCalls)
	}
	if exitCalls != 0 {
		t.Errorf("exit called %d times, want 0 on hard-fail", exitCalls)
	}
}

func TestRun_StartedByUs_CallsDown(t *testing.T) {
	makeFakeNono(t)
	h := &fakeHandle{started: true}
	gotExit := -1
	err := run(&config.Config{ToolMode: "mcp"}, Options{}, runDeps{
		writeProfile: func(*config.Config) (string, []string, func(), error) {
			return "/tmp/asb-profile-1.json", nil, func() {}, nil
		},
		ensureUp:  func(context.Context, *config.Config) (sandboxHandle, error) { return h, nil },
		supervise: func(string, []string) int { return 3 },
		exit:      func(code int) { gotExit = code },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.downCalls != 1 {
		t.Errorf("Down called %d times, want 1 when we started the sandbox", h.downCalls)
	}
	if !h.closed {
		t.Error("handle not closed")
	}
	if gotExit != 3 {
		t.Errorf("exit code = %d, want 3 (claude's code)", gotExit)
	}
}

func TestRun_NotStartedByUs_SkipsDown(t *testing.T) {
	makeFakeNono(t)
	h := &fakeHandle{started: false}
	err := run(&config.Config{ToolMode: "mcp"}, Options{}, runDeps{
		writeProfile: func(*config.Config) (string, []string, func(), error) {
			return "/tmp/asb-profile-1.json", nil, func() {}, nil
		},
		ensureUp:  func(context.Context, *config.Config) (sandboxHandle, error) { return h, nil },
		supervise: func(string, []string) int { return 0 },
		exit:      func(int) {},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.downCalls != 0 {
		t.Errorf("Down called %d times, want 0 when sandbox was already running", h.downCalls)
	}
}

func TestRun_DownError_StillPropagatesExitCode(t *testing.T) {
	makeFakeNono(t)
	h := &fakeHandle{started: true, downErr: errors.New("down failed")}
	gotExit := -1
	err := run(&config.Config{ToolMode: "mcp"}, Options{}, runDeps{
		writeProfile: func(*config.Config) (string, []string, func(), error) {
			return "/tmp/asb-profile-1.json", nil, func() {}, nil
		},
		ensureUp:  func(context.Context, *config.Config) (sandboxHandle, error) { return h, nil },
		supervise: func(string, []string) int { return 5 },
		exit:      func(code int) { gotExit = code },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotExit != 5 {
		t.Errorf("exit code = %d, want 5 even when Down errors", gotExit)
	}
	if h.downCalls != 1 {
		t.Errorf("Down attempted %d times, want 1 in the down-error case", h.downCalls)
	}
}

func TestRun_HookMode_WritesAndCleansSnapshot(t *testing.T) {
	makeFakeNono(t)
	wrote, cleaned := 0, 0
	h := &fakeHandle{started: false}
	err := run(&config.Config{ToolMode: "hook"}, Options{}, runDeps{
		writeSnapshot: func(*config.Config) (string, func(), error) {
			wrote++
			return "/state/policy-1.json", func() { cleaned++ }, nil
		},
		writeProfile: func(*config.Config) (string, []string, func(), error) {
			return "/tmp/asb-profile-1.json", nil, func() {}, nil
		},
		ensureUp:  func(context.Context, *config.Config) (sandboxHandle, error) { return h, nil },
		supervise: func(string, []string) int { return 0 },
		exit:      func(int) {},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wrote != 1 {
		t.Errorf("writeSnapshot called %d times, want 1", wrote)
	}
	if cleaned != 1 {
		t.Errorf("cleanup called %d times, want 1", cleaned)
	}
}

func TestRun_HookMode_CleansSnapshotBeforeExit(t *testing.T) {
	makeFakeNono(t)
	cleaned := 0
	cleanedBeforeExit := false
	h := &fakeHandle{started: false}
	err := run(&config.Config{ToolMode: "hook"}, Options{}, runDeps{
		writeSnapshot: func(*config.Config) (string, func(), error) {
			return "/state/policy-1.json", func() { cleaned++ }, nil
		},
		writeProfile: func(*config.Config) (string, []string, func(), error) {
			return "/tmp/asb-profile-1.json", nil, func() {}, nil
		},
		ensureUp:  func(context.Context, *config.Config) (sandboxHandle, error) { return h, nil },
		supervise: func(string, []string) int { return 0 },
		exit:      func(int) { cleanedBeforeExit = cleaned == 1 },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cleanedBeforeExit {
		t.Error("snapshot cleanup must run before exit; os.Exit skips deferred cleanup in production")
	}
}

func TestRun_GithubMCPEnabled_WritesAndCleansConfig(t *testing.T) {
	makeFakeNono(t)
	t.Setenv("GITHUB_MCP_TOKEN", "ghp_x")
	wrote, cleaned := 0, 0
	h := &fakeHandle{started: false}
	cfg := &config.Config{ToolMode: "mcp"}
	err := run(cfg, Options{}, runDeps{
		writeMCPConfig: func(*config.Config) (string, func(), error) {
			wrote++
			return "/tmp/asb-mcp-1.json", func() { cleaned++ }, nil
		},
		writeProfile: func(*config.Config) (string, []string, func(), error) {
			return "/tmp/asb-profile-1.json", nil, func() {}, nil
		},
		ensureUp:  func(context.Context, *config.Config) (sandboxHandle, error) { return h, nil },
		supervise: func(string, []string) int { return 0 },
		exit:      func(int) {},
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
	h := &fakeHandle{started: false}
	err := run(&config.Config{ToolMode: "mcp"}, Options{}, runDeps{
		writeMCPConfig: func(*config.Config) (string, func(), error) { wrote++; return "", func() {}, nil },
		writeProfile: func(*config.Config) (string, []string, func(), error) {
			return "/tmp/asb-profile-1.json", nil, func() {}, nil
		},
		ensureUp:  func(context.Context, *config.Config) (sandboxHandle, error) { return h, nil },
		supervise: func(string, []string) int { return 0 },
		exit:      func(int) {},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wrote != 0 {
		t.Errorf("writeMCPConfig called %d times, want 0 when disabled", wrote)
	}
}
