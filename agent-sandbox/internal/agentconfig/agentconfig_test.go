package agentconfig_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/agentconfig"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxhost"
)

// writeProfile writes contents (a command-profile.json body) to path.
func writeProfile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
}

// configWithProfile returns a *config.Config whose CommandProfilePath()
// resolves to profile, loaded the same way config.Load resolves a relative
// command_profile: against the directory holding agent-sandbox.toml.
func configWithProfile(t *testing.T, dir, profile string) *config.Config {
	t.Helper()
	rel, err := filepath.Rel(dir, profile)
	if err != nil {
		t.Fatalf("relative profile path: %v", err)
	}
	data := "tool_mode = \"hook\"\ncommand_profile = " + `"` + rel + `"` + "\n"
	tomlPath := filepath.Join(dir, "agent-sandbox.toml")
	if err := os.WriteFile(tomlPath, []byte(data), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(tomlPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// Explain names the command profile's path, both tiers it declares, and the
// reason behind each invocation_policy denial — an agent that cannot tell a
// policy refusal from a bug will retry it.
func TestExplainNamesTheCommandProfileAndBothTiers(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	writeProfile(t, profile, `{
	  "command_policies": {
	    "commands": {
	      "agent-sandbox": { "can_use": ["git"],
	        "from": { "session": { "sandbox": { "exec_paths": ["/usr/bin"] } } } },
	      "git": { "from": { "agent-sandbox": { "invocation_policy": { "deny": [
	        { "argv": { "contains": ["--force"] }, "reason": "force push is disabled in this sandbox" }
	      ] } } } }
	    }
	  }
	}`)
	got := agentconfig.Explain(configWithProfile(t, dir, profile), filepath.Join(dir, "agent-sandbox.toml"))

	for _, want := range []string{
		profile,
		"git",
		"force push is disabled in this sandbox",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() is missing %q\n---\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"allow_commands", "drop_commands", "[sandbox.shell]"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("Explain() still mentions %q", unwanted)
		}
	}
}

// A command bound to a "safe <tool>" wrapper (argv_prepend, no
// invocation_policy of its own — the shape "git" uses in this repository's
// own command-profile.json) must be described as routing through that
// wrapper, not reported as carrying no denials: its rule set lives in Go,
// not in this profile.
func TestExplain_WrapperBoundCommand_NamesTheWrapper(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	writeProfile(t, profile, `{
	  "command_policies": {
	    "commands": {
	      "agent-sandbox": { "can_use": ["git"],
	        "from": { "session": { "sandbox": { "exec_paths": ["/usr/bin"] } } } },
	      "git": { "can_use": ["realgit"],
	        "from": { "agent-sandbox": { "sandbox": { "argv_prepend": ["safe", "git"] } } } },
	      "realgit": { "from": { "git": { "sandbox": {} } } }
	    }
	  }
	}`)
	got := agentconfig.Explain(configWithProfile(t, dir, profile), filepath.Join(dir, "agent-sandbox.toml"))

	for _, want := range []string{
		"agent-sandbox safe git",
		"reachable only from this wrapper",
		// The wrapper's own rule set, read from internal/safe/git, not
		// this synthetic fixture's (nonexistent) invocation_policy.
		"git reset --hard is not allowed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() is missing %q for a wrapper-bound command\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "`git` (no invocations refused)") {
		t.Errorf("Explain() reports the wrapper-bound `git` entry as refusing nothing:\n%s", got)
	}
	// realgit is named only in git's own "from" (git → realgit), never the
	// broker's ("agent-sandbox" → realgit is absent from this fixture): it
	// is not directly invocable at all, and nono refuses reaching it with
	// "tool 'agent-sandbox' is not allowed to invoke it". Listing it as a
	// policy command the agent could type is what shipped a false "no
	// invocations refused" line for it once git's own invocation_policy was
	// deleted — regression coverage for that, not just a documentation nit.
	if strings.Contains(got, "`realgit`") {
		t.Errorf("Explain() lists `realgit`, which the broker cannot reach directly:\n%s", got)
	}
}

func TestPointer_MentionsExplainCommand(t *testing.T) {
	got := agentconfig.Pointer()
	if !strings.Contains(got, "agent-sandbox ai explain") {
		t.Errorf("Pointer() missing command reference:\n%s", got)
	}
}

func TestExplain_HookMode(t *testing.T) {
	cfg := &config.Config{ToolMode: "hook"}
	got := agentconfig.Explain(cfg, "agent-sandbox.toml")
	for _, want := range []string{
		"# agent-sandbox environment",
		"Bash/Monitor tools",
		"PreToolUse hook",
		"returned inline in the tool result",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() missing %q\nfull output:\n%s", want, got)
		}
	}
	if strings.Contains(got, "run_command") {
		t.Errorf("Explain() hook branch should not mention the mcp run_command tool:\n%s", got)
	}
}

func TestExplain_McpMode(t *testing.T) {
	cfg := &config.Config{ToolMode: "mcp"}
	got := agentconfig.Explain(cfg, "agent-sandbox.toml")
	for _, want := range []string{
		"run_command",
		"written to files",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() mcp branch missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "PreToolUse hook") {
		t.Errorf("Explain() mcp branch should not mention the hook flow:\n%s", got)
	}
}

// Explain names the command profile the broker runs under, resolved the same
// way config.Config.CommandProfilePath() does, without describing what it
// allows: agent-sandbox neither generates nor reads its contents.
func TestExplain_NamesTheCommandProfilePath(t *testing.T) {
	cfg := &config.Config{ToolMode: "hook"}
	got := agentconfig.Explain(cfg, "agent-sandbox.toml")
	if !strings.Contains(got, cfg.CommandProfilePath()) {
		t.Errorf("Explain() missing the command profile path %q\nfull output:\n%s", cfg.CommandProfilePath(), got)
	}
}

func TestExplain_ConfigEditingSection(t *testing.T) {
	cfg := &config.Config{ToolMode: "hook"}
	got := agentconfig.Explain(cfg, "/work/proj/agent-sandbox.toml")

	for _, want := range []string{
		"## Changing the config",
		"/work/proj/agent-sandbox.toml",
		"[sandbox.agent]",
		"tool_mode",
		"capabilities",
		"agent-sandbox ai config-check",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() config section missing %q\nfull output:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"[sandbox.shared]", "[sandbox.shell]", "allow_commands", "drop_commands", "allow_domains"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("Explain() config section still mentions removed key %q\nfull output:\n%s", unwanted, got)
		}
	}
}

func TestExplain_ListsCapabilityNames(t *testing.T) {
	got := agentconfig.Explain(&config.Config{ToolMode: "hook"}, "agent-sandbox.toml")
	for _, name := range sandboxhost.CapabilityNames() {
		if !strings.Contains(got, "`"+name+"`") {
			t.Errorf("Explain() does not list capability %q\nfull output:\n%s", name, got)
		}
	}
}

func TestExplain_SaysEditsApplyAtNextLaunch(t *testing.T) {
	got := agentconfig.Explain(&config.Config{ToolMode: "hook"}, "agent-sandbox.toml")
	for _, want := range []string{
		"does not affect the current session",
		"agent-sandbox claude",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() missing %q about when edits take effect\nfull output:\n%s", want, got)
		}
	}
}

func TestExplain_MentionsUserScopeConfigMerge(t *testing.T) {
	got := agentconfig.Explain(&config.Config{ToolMode: "hook"}, "agent-sandbox.toml")
	for _, want := range []string{
		"~/.config/agent-sandbox/config.toml",
		"union",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() missing %q about the user-scope config merge\nfull output:\n%s", want, got)
		}
	}
}
