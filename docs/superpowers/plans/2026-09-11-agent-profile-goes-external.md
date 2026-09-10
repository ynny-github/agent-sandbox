# Agent Profile Goes External Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop generating the agent's nono profile; name it by path per agent in `agent-sandbox.toml`, and let nono be the only authority on what either profile grants.

**Architecture:** `[agents.<name>].profile` names a hand-written nono profile that resolves exactly like the existing `command_profile`. The launcher passes that path to `nono wrap --profile` unchanged. `internal/sandboxhost` — the capability catalog, the profile generator, and the Claude permission-deny rules it derived — is deleted, along with the `[sandbox]` config surface that fed it. `doctor` and `ai config-check` answer by calling `nono profile validate`, and the one question nono cannot answer (does the profile forward `AGENT_SANDBOX_BROKER_SOCKET`?) is answered by running a sandbox and looking.

**Tech Stack:** Go 1.25, cobra, BurntSushi/toml, nono 0.74.0 CLI

**Spec:** [docs/superpowers/specs/2026-09-11-agent-profile-goes-external.md](../specs/2026-09-11-agent-profile-goes-external.md)

## Global Constraints

- Module root is the repository root; all Go commands run from there. Package import prefix: `github.com/ynny-github/agent-sandbox/agent-sandbox/`.
- Full suite: `go test ./...`. It is green before Task 1 and must be green at the end of every task.
- No new Go dependencies. No new binary requirements beyond `nono`, which the project already requires.
- Commit messages follow Conventional Commits per `.claude/rules/git-commit.md`: `<type>(<scope>): <subject>`, imperative, under 72 chars, body explaining What and Why.
- All deliverable text (code comments, docs, commit messages) is English, per `.claude/CLAUDE.md`.
- Do not touch `agent-sandbox/internal/safe/**`. It is deliberately unwired and stays that way.
- The measured nono facts this plan relies on: a profile's `extends` accepts a profile *name*, never a path; unknown top-level profile keys are rejected by `nono profile validate`; `nono profile show` does not report `environment.allow_vars`; `nono why` queries are `--command`, `--path`, `--host`, `--scope` only; `nono wrap` in non-interactive mode needs `--allow-cwd` for working-directory access.

---

### Task 1: `[agents.<name>].profile` in the config

Additive only — nothing consumes it yet, so the suite stays green.

**Files:**
- Modify: `agent-sandbox/internal/config/config.go`
- Modify: `agent-sandbox/internal/config/errors.go`
- Test: `agent-sandbox/internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type AgentConfig struct { Profile string }` with TOML key `profile`
  - `Config.Agents map[string]AgentConfig` with TOML key `agents`
  - `func (c *Config) AgentProfilePath(agent string) string`
  - `var config.ErrAgentProfileMissing error`

- [ ] **Step 1: Write the failing tests**

Append to `agent-sandbox/internal/config/config_test.go`. `writeFile` and the
`TestMain` HOME isolation already exist in that file.

```go
func TestAgentProfilePathDefaultsBesideConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "tool_mode = \"hook\"\n")
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "claude-profile.json")
	if got := cfg.AgentProfilePath("claude"); got != want {
		t.Errorf("AgentProfilePath = %q, want %q", got, want)
	}
}

func TestAgentProfilePathRelativeResolvesAgainstConfigDir(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "tool_mode = \"hook\"\n\n[agents.claude]\nprofile = \"profiles/claude.json\"\n")
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "profiles", "claude.json")
	if got := cfg.AgentProfilePath("claude"); got != want {
		t.Errorf("AgentProfilePath = %q, want %q", got, want)
	}
}

func TestAgentProfilePathAbsoluteIsUsedAsIs(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "tool_mode = \"hook\"\n\n[agents.claude]\nprofile = \"/etc/agent-sandbox/claude.json\"\n")
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.AgentProfilePath("claude"); got != "/etc/agent-sandbox/claude.json" {
		t.Errorf("AgentProfilePath = %q, want the absolute path unchanged", got)
	}
}

// The project file wins for an agent both files declare, and an agent only the
// user-scope file declares survives. Both fall out of how BurntSushi/toml
// decodes into an existing map — pinned here because a second field on
// AgentConfig would silently break the first half (a key is replaced whole,
// not merged field by field).
func TestAgentProfileProjectOverridesUserScopeAndKeepsOtherAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, ".config", "agent-sandbox", "config.toml"),
		"[agents.claude]\nprofile = \"user-claude.json\"\n\n[agents.codex]\nprofile = \"user-codex.json\"\n")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "tool_mode = \"hook\"\n\n[agents.claude]\nprofile = \"project-claude.json\"\n")
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.AgentProfilePath("claude"), filepath.Join(dir, "project-claude.json"); got != want {
		t.Errorf("project must win for an agent both files declare: got %q, want %q", got, want)
	}
	if got, want := cfg.AgentProfilePath("codex"), filepath.Join(dir, "user-codex.json"); got != want {
		t.Errorf("an agent only the user config declares must survive: got %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./agent-sandbox/internal/config/`
Expected: build failure — `cfg.AgentProfilePath undefined (type *config.Config has no field or method AgentProfilePath)`

- [ ] **Step 3: Add the config surface**

In `agent-sandbox/internal/config/config.go`, add the field to `Config`
immediately after `CommandProfile`:

```go
	// Agents maps a launch subcommand's name ("claude") to that agent's
	// configuration. An absent table is not an error: every key has a default.
	Agents map[string]AgentConfig `toml:"agents"`
```

Add the type below `Config`:

```go
// AgentConfig is one launchable agent's configuration. It holds only the nono
// profile that agent runs under — a file the operator writes in nono's own
// schema, which agent-sandbox hands to nono without generating, reading, or
// validating it.
//
// A second field would need care. Load decodes the user config and then the
// project config into the same map, and a key present in both is replaced
// wholesale rather than merged field by field, so a project [agents.claude]
// setting only one field would silently drop the user config's others.
type AgentConfig struct {
	Profile string `toml:"profile"`
}
```

Add the resolver beside `CommandProfilePath`:

```go
// AgentProfilePath is the absolute path of the nono profile the named agent
// runs under. It resolves exactly like CommandProfilePath — one rule for both
// profiles — so an absent table or empty value means "<agent>-profile.json"
// beside the project config, and a relative path joins onto that same
// directory however agent-sandbox was invoked.
func (c *Config) AgentProfilePath(agent string) string {
	name := strings.TrimSpace(c.Agents[agent].Profile)
	if name == "" {
		name = agent + "-profile.json"
	}
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(c.dir, name)
}
```

In `agent-sandbox/internal/config/errors.go`, add:

```go
// ErrAgentProfileMissing fires when the nono profile the launched agent runs
// under is not on disk. There is deliberately no generated fallback:
// agent-sandbox no longer builds profiles at all, and a default that looked
// present while granting the wrong thing is the worst failure available.
var ErrAgentProfileMissing = errors.New("agent profile not found; write it, or point [agents.<name>].profile at it")
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./agent-sandbox/internal/config/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent-sandbox/internal/config/
git commit -m "feat(config): name each agent's nono profile by path

What: Adds [agents.<name>].profile and AgentProfilePath, resolving it the
same way command_profile resolves, plus ErrAgentProfileMissing.

Why: the agent profile stops being generated; it becomes a file the
operator writes and agent-sandbox only names."
```

---

### Task 2: The launcher uses the configured profile

After this task `agent-sandbox claude` runs entirely off the hand-written
profile, so this repository's own `claude-profile.json` lands here too.

**Files:**
- Modify: `agent-sandbox/internal/claude/launch.go` (imports, `runDeps`, `Run`, `run`, `BuildArgs`)
- Modify: `agent-sandbox/internal/claude/settings.go:25-48`
- Modify: `agent-sandbox/internal/claude/launch_test.go`
- Modify: `agent-sandbox/internal/claude/settings_test.go`
- Create: `claude-profile.json` (repository root)
- Modify: `agent-sandbox.toml` (repository root)

**Interfaces:**
- Consumes: `config.AgentProfilePath`, `config.ErrAgentProfileMissing` (Task 1).
- Produces:
  - `func BuildArgs(cfg *config.Config, opts Options, mcpConfigPath, profilePath string, brokerSocket string) (string, []string, error)` — the `denyRules []string` parameter is gone
  - `func settingsJSON(mcpConfigPath string, hookMode bool) (string, error)`
  - `runDeps.agentProfile func(*config.Config) (string, error)` replacing `writeProfile`
  - `func defaultAgentProfile(c *config.Config) (string, error)`

- [ ] **Step 1: Write the failing tests**

Append to `agent-sandbox/internal/claude/launch_test.go`. `makeFakeNono`,
`argsIndex` and `testBrokerStart` already exist there.

```go
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
```

Make sure `errors`, `os`, `path/filepath` and the `config` package are imported
in that test file; add whichever are missing.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./agent-sandbox/internal/claude/`
Expected: build failure — `unknown field agentProfile in struct literal of type runDeps` and `undefined: defaultAgentProfile`

- [ ] **Step 3: Rework the launcher**

In `agent-sandbox/internal/claude/launch.go`, delete the `sandboxhost` import.

Replace the `writeProfile` field of `runDeps` with:

```go
	// agentProfile resolves — and existence-checks — the nono profile the
	// launched agent runs under. It stays a dependency so tests can drive run
	// without touching the filesystem.
	agentProfile func(*config.Config) (string, error)
```

Add beside `runDeps`:

```go
// defaultAgentProfile resolves the launched agent's profile path from cfg and
// fails when it is not on disk. Nothing here opens the file: nono reads it at
// launch, and doctor asks nono to validate it. agent-sandbox only names it.
func defaultAgentProfile(c *config.Config) (string, error) {
	path := c.AgentProfilePath(agentName)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("%w: %s", config.ErrAgentProfileMissing, path)
	}
	return path, nil
}
```

In `Run`, replace the whole `writeProfile: func(...) {...},` literal with:

```go
		agentProfile: defaultAgentProfile,
```

In `run`, replace the profile block

```go
	profilePath, denyRules, cleanupProfile, err := d.writeProfile(cfg)
	if err != nil {
		return fmt.Errorf("sandbox host profile: %w", err)
	}
	defer func() {
		if cleanupProfile != nil {
			cleanupProfile()
		}
	}()
```

with

```go
	profilePath, err := d.agentProfile(cfg)
	if err != nil {
		return err
	}
```

and delete the two later `if cleanupProfile != nil { cleanupProfile(); cleanupProfile = nil }` blocks that follow `d.supervise`.

Update the `BuildArgs` call in `run` to drop the deny-rules argument:

```go
	nonoPath, nonoArgs, err := BuildArgs(cfg, opts, mcpConfigPath, profilePath, brokerSocket)
```

Change `BuildArgs`'s signature and its `settingsJSON` call:

```go
func BuildArgs(cfg *config.Config, opts Options, mcpConfigPath,
	profilePath string, brokerSocket string) (string, []string, error) {
```

```go
	settingsStr, err := settingsJSON(mcpConfigPath, cfg.ToolMode == "hook")
```

Update `BuildArgs`'s doc comment: replace "It injects the generated profile at
profilePath" with "It injects the operator's profile at profilePath", and
replace the trailing sentence "denyRules are folded into the injected settings
as additional capability denies." with:

```go
// The injected settings carry the hook and the GitHub MCP denies only; the
// profile contributes nothing to them.
```

In `agent-sandbox/internal/claude/settings.go`, drop the parameter and the
capability denies it seeded:

```go
func settingsJSON(mcpConfigPath string, hookMode bool) (string, error) {
```

```go
	var deny []string
	if mcpConfigPath != "" {
		deny = append(deny, denyReadRule(mcpConfigPath))
		// The github MCP is active: block its repos write tools so the agent
		// cannot mutate GitHub via the API, bypassing routed local git.
		deny = append(deny, githubMCPWriteDenyRules...)
	}
```

- [ ] **Step 4: Update the existing tests**

In `agent-sandbox/internal/claude/launch_test.go`:

- Drop the deny-rules argument from every `BuildArgs(...)` call (lines 71, 140,
  158, 170, 197, 209, 239, 256, 275 and any others): `BuildArgs(cfg, Options{}, "", "", nil, "")`
  becomes `BuildArgs(cfg, Options{}, "", "", "")`, and
  `BuildArgs(cfg, Options{}, "", "/tmp/asb-profile-1.json", nil, "")` becomes
  `BuildArgs(cfg, Options{}, "", "/tmp/asb-profile-1.json", "")`.
- Delete `TestBuildArgs_InjectsCapabilityDeny` entirely — the behaviour it
  pinned is gone.
- In every `runDeps{...}` literal, replace

  ```go
		writeProfile: func(*config.Config) (string, []string, func(), error) {
			return "/tmp/asb-profile-1.json", nil, func() {}, nil
		},
  ```

  with

  ```go
		agentProfile: func(*config.Config) (string, error) {
			return "/tmp/asb-profile-1.json", nil
		},
  ```

In `agent-sandbox/internal/claude/settings_test.go`, drop the third argument
from every `settingsJSON(...)` call, and delete any case whose only subject is
a capability deny rule reaching the settings.

- [ ] **Step 5: Run the package tests to verify they pass**

Run: `go test ./agent-sandbox/internal/claude/`
Expected: PASS

- [ ] **Step 6: Write this repository's agent profile**

Create `claude-profile.json` at the repository root:

```jsonc
{
  // The nono profile the launched agent itself runs under. agent-sandbox does
  // not generate, read, or validate this file — it hands the path to nono.
  //
  // Scope: the agent process and its DIRECT children (an MCP server it spawns
  // is not brokered). Shell commands do not run here at all: they go through
  // the broker, under command-profile.json. Nothing needed only to *run a
  // command* belongs in this file.
  "extends": "claude",
  "meta": { "name": "custom claude" },

  // NixOS: every executable lives under /nix/store, reached through the
  // /run/current-system/sw symlink farm. nono's base profile grants that tree
  // read but not execute, so without this the agent cannot start at all —
  // execve is refused and the process exits 127 with no output.
  "groups": { "include": ["nix_runtime"] },

  "filesystem": {
    // The agent's file tools write scratch files here and read them back; the
    // claude base grants /tmp write-only, which fails on the read-back. In mcp
    // mode this also covers mcp.command_output_dir.
    "allow": ["/tmp"],

    "read": [
      "/nix/store",
      "/run/current-system/sw",
      // GitHub MCP runs `docker run -i --rm ghcr.io/github/github-mcp-server`
      // as a direct child of the agent, so its config is read here rather than
      // in the command profile. Drop these two entries and the bypass below
      // when GITHUB_MCP_TOKEN is not used.
      "~/.docker",
      "~/.orbstack"
    ],

    "allow_file": ["/dev/null"],
    "bypass_protection": ["~/.docker"]
  },

  "environment": {
    // AGENT_SANDBOX_BROKER_SOCKET is load-bearing. Without it nono strips the
    // variable, the agent never reaches the broker, and EVERY command fails
    // with an error that names nothing. `agent-sandbox doctor` probes for it
    // because nono cannot report env grants: `nono profile show` does not
    // print allow_vars and `nono why` has no env query.
    //
    // --env no longer grants anything. A variable passed that way reaches the
    // agent only if it is listed here (globs like MISE* work).
    "allow_vars": [
      "PATH", "HOME", "TERM", "LANG", "LC_ALL", "USER",
      "AGENT_SANDBOX_BROKER_SOCKET"
    ]
  }
}
```

In `agent-sandbox.toml`, add below the `tool_mode` line:

```toml
  # The nono profile the launched agent runs under, resolved beside this file.
  # "claude-profile.json" is the default name for the "claude" agent, so this
  # key could be omitted; it is written out because the file it names is the
  # whole of the agent's host access.
  [agents.claude]
  profile = "claude-profile.json"
```

Leave `[sandbox.agent]` in place for now — nothing reads it after this task,
and Task 7 removes it together with the code that decoded it.

- [ ] **Step 7: Verify the profile and the whole suite**

Run: `nono profile validate claude-profile.json`
Expected: `Result: valid`

Run: `go test ./...`
Expected: all packages ok

- [ ] **Step 8: Commit**

```bash
git add agent-sandbox/internal/claude/ claude-profile.json agent-sandbox.toml
git commit -m "feat(launch): run the agent under the profile the operator wrote

What: run() resolves [agents.claude].profile and passes it to nono wrap
instead of generating and writing a temp profile; BuildArgs and
settingsJSON lose the capability deny rules. Adds this repository's own
claude-profile.json.

Why: the agent profile becomes a file, named by path, that nono alone
interprets."
```

---

### Task 3: `--env` stops granting

**Files:**
- Modify: `agent-sandbox/cmd/claude.go:44-50`
- Modify: `agent-sandbox/cmd/debug.go`
- Modify: `agent-sandbox/cmd/debug_test.go`

**Interfaces:**
- Consumes: `config.AgentProfilePath` (Task 1), `claude.BuildArgs` without deny rules (Task 2).
- Produces: `func formatGeneratedConfigs(mcpEnabled bool, mcpJSON []byte) string` — the profile parameters are gone.

- [ ] **Step 1: Write the failing test**

Append to `agent-sandbox/cmd/debug_test.go`:

```go
// debug must print the profile path the launcher will actually pass, so the
// value can be pasted straight into `nono profile show`.
func TestRunDebug_PrintsTheConfiguredAgentProfilePath(t *testing.T) {
	dir := t.TempDir()
	nono := filepath.Join(dir, "nono")
	if err := os.WriteFile(nono, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake nono: %v", err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))

	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	cfgBody := "[mcp]\ncommand_output_dir = " + toTOMLString(filepath.Join(dir, "out")) + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	for _, name := range []string{"command-profile.json", "claude-profile.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	orig := configPath
	configPath = cfgPath
	t.Cleanup(func() { configPath = orig })

	out := captureStdout(t, func() {
		if err := runDebug(debugCmd, nil); err != nil {
			t.Fatalf("runDebug() error = %v", err)
		}
	})
	want := "--profile " + filepath.Join(dir, "claude-profile.json")
	if !strings.Contains(out, want) {
		t.Errorf("debug output missing %q; got:\n%s", want, out)
	}
}
```

Also rewrite the two existing `formatGeneratedConfigs` tests in that file to the
new signature:

```go
func TestFormatGeneratedConfigs_EnabledMCP(t *testing.T) {
	mcp := []byte(`{"mcpServers":{"github":{"env":{"GITHUB_PERSONAL_ACCESS_TOKEN":"***redacted***"}}}}`)
	out := formatGeneratedConfigs(true, mcp)
	for _, want := range []string{
		"# github mcp config (enabled; token redacted):",
		"***redacted***",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

func TestFormatGeneratedConfigs_MCPDisabledLabel(t *testing.T) {
	out := formatGeneratedConfigs(false, []byte(`{"b":2}`))
	if !strings.Contains(out, "# github mcp config (disabled; token redacted):") {
		t.Errorf("expected disabled label; got:\n%s", out)
	}
}
```

Delete the old `TestFormatGeneratedConfigs_ProfileAndEnabledMCP`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./agent-sandbox/cmd/`
Expected: build failure — `too many arguments in call to formatGeneratedConfigs`

- [ ] **Step 3: Rework the two commands**

In `agent-sandbox/cmd/claude.go`, delete the comment block and the line that
appended the `--env` keys:

```go
	cfg.Sandbox.Agent.AllowEnv = append(cfg.Sandbox.Agent.AllowEnv, envKeys...)
```

Keep `envflag.Load` and add above it:

```go
	// --env loads the referenced file's variables into this process, which is
	// all it does now: nono forwards only what the agent profile's
	// environment.allow_vars lists, and that list is hand-written.
```

`envKeys` becomes unused — change the call to `if _, err := envflag.Load(opts.EnvRefs); err != nil { return err }`, matching `cmd/root.go:43`.

In `agent-sandbox/cmd/debug.go`: delete the `sandboxhost` import, the
`cfg.Sandbox.Agent.AllowEnv` append (same treatment as above), the
`sandboxhost.Resolve` / `r.WriteProfile` / `defer cleanupProfile()` block, and
the `r.ProfileJSON()` block. Resolve the path from config instead:

```go
	profilePath := cfg.AgentProfilePath("claude")
```

Update the `BuildArgs` call:

```go
	_, nonoArgs, err := claude.BuildArgs(cfg, opts, "", profilePath, brokerSocket)
```

Replace the final print with:

```go
	mcpJSON, err := claude.RedactedGithubMCPConfigJSON()
	if err != nil {
		return err
	}
	fmt.Print(formatGeneratedConfigs(claude.GithubMCPEnabled(), mcpJSON))
```

And narrow `formatGeneratedConfigs`:

```go
// formatGeneratedConfigs renders the token-redacted GitHub MCP config — the
// only file agent-sandbox still generates — for display under the debug
// command. The token is always redacted; it never reaches the terminal. The
// nono profiles are not shown here: they are files on disk, named in the
// invocation above, and `nono profile show <path>` is what resolves one.
func formatGeneratedConfigs(mcpEnabled bool, mcpJSON []byte) string {
	var b strings.Builder
	state := "disabled"
	if mcpEnabled {
		state = "enabled"
	}
	fmt.Fprintf(&b, "\n# github mcp config (%s; token redacted):\n", state)
	b.WriteString(indentJSON(mcpJSON))
	b.WriteString("\n")
	return b.String()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./agent-sandbox/cmd/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent-sandbox/cmd/claude.go agent-sandbox/cmd/debug.go agent-sandbox/cmd/debug_test.go
git commit -m "refactor(cmd): stop granting env vars from --env

What: --env now only loads a file's variables into the launcher process;
the agent profile's allow_vars decides what is forwarded. debug prints
the configured profile path instead of generated profile JSON.

Why: nono wrap has no flag for env grants, so the profile is the only
place they can live once it is hand-written."
```

---

### Task 4: `ai config-check` delegates to nono

**Files:**
- Create: `agent-sandbox/cmd/profiles.go`
- Modify: `agent-sandbox/cmd/ai.go:38-81`
- Modify: `agent-sandbox/cmd/ai_test.go`

**Interfaces:**
- Consumes: `config.AgentProfilePath` (Task 1), `runCommand` (existing seam in `cmd/doctor.go:78-86`).
- Produces: `func validateProfile(path string) error` in package `cmd`, used by Task 5.

- [ ] **Step 1: Write the failing tests**

In `agent-sandbox/cmd/ai_test.go`, replace `TestRunConfigCheck_PrintsFilesystemGrants`
and `TestRunConfigCheck_UnknownCapability` with:

```go
// config-check hands both profiles to nono rather than describing them: the
// only authority on what a profile grants is nono itself.
func TestRunConfigCheck_ValidatesBothProfilesWithNono(t *testing.T) {
	var validated []string
	restore := stubRunCommand(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "nono" && len(args) == 3 && args[0] == "profile" && args[1] == "validate" {
			validated = append(validated, args[2])
		}
		return []byte("valid"), nil
	})
	defer restore()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	if err := os.WriteFile(cfgPath, []byte("tool_mode = \"hook\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"command-profile.json", "claude-profile.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	orig := configPath
	configPath = cfgPath
	t.Cleanup(func() { configPath = orig })

	var buf bytes.Buffer
	configCheckCmd.SetOut(&buf)
	if err := runConfigCheck(configCheckCmd, nil); err != nil {
		t.Fatalf("runConfigCheck: %v", err)
	}
	for _, want := range []string{
		filepath.Join(dir, "claude-profile.json"),
		filepath.Join(dir, "command-profile.json"),
	} {
		if !slices.Contains(validated, want) {
			t.Errorf("nono profile validate was not called for %q; called for %v", want, validated)
		}
	}
	if !strings.Contains(buf.String(), "nono profile show") {
		t.Errorf("output must point at the command that resolves a profile:\n%s", buf.String())
	}
}

func TestRunConfigCheck_FailsWhenTheAgentProfileIsMissing(t *testing.T) {
	restore := stubRunCommand(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("valid"), nil
	})
	defer restore()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	if err := os.WriteFile(cfgPath, []byte("tool_mode = \"hook\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "command-profile.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := configPath
	configPath = cfgPath
	t.Cleanup(func() { configPath = orig })

	configCheckCmd.SetOut(&bytes.Buffer{})
	err := runConfigCheck(configCheckCmd, nil)
	if err == nil {
		t.Fatal("expected an error when the agent profile is missing, got nil")
	}
	if !strings.Contains(err.Error(), "claude-profile.json") {
		t.Errorf("error must name the missing file: %v", err)
	}
}
```

Two more edits in the same file:

- `TestRunConfigCheck_ValidConfig`: drop the `[sandbox.agent]` block from its
  fixture, wrap the call in a `stubRunCommand` returning `[]byte("valid"), nil`,
  and have `writeTempConfig` also write a `claude-profile.json` beside the
  config (add that write to the helper — every fixture in this file needs it now).
- `TestRunExplain_RendersConfig`: drop the `[sandbox.agent]` block and the
  ``"`go`"`` expectation from its `want` list, keeping the other two.

Add `context` and `slices` to the file's imports; `os` and `path/filepath` are
already there.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./agent-sandbox/cmd/ -run ConfigCheck`
Expected: FAIL — the current implementation neither validates profiles nor
prints `nono profile show`

- [ ] **Step 3: Add the shared validator and rewrite config-check**

Create `agent-sandbox/cmd/profiles.go`:

```go
// agent-sandbox/cmd/profiles.go
package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// validateProfile checks that a nono profile is on disk and that nono accepts
// it. Nothing here parses the file: nono owns the schema, and asking nono is
// the only answer that cannot drift from what a launch will actually accept.
func validateProfile(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("not found: %s", path)
	}
	out, err := runCommand(context.Background(), "nono", "profile", "validate", path)
	if err != nil {
		return fmt.Errorf("nono profile validate %s: %s", path, strings.TrimSpace(string(out)))
	}
	return nil
}

// profileHelp is the two commands that answer any question about a profile.
// agent-sandbox prints them rather than restating what a profile grants.
const profileHelp = `
agent-sandbox does not read either profile. To see what one grants:
  nono profile show <path>
To ask why an access was refused:
  nono why --profile <path> --path <p> --op read|write|readwrite
`
```

In `agent-sandbox/cmd/ai.go`, delete the `sandboxhost` and `io` imports, delete
`printList`, and replace `runConfigCheck` with:

```go
// runConfigCheck loads the config and asks nono to validate both profiles —
// the same two files a launch hands to nono. agent-sandbox does not interpret
// either one, so a passing check means the config and the profiles are not
// what breaks the next launch; what they *grant* is `nono profile show`.
func runConfigCheck(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "ok: %s loads\n", configPath)

	for _, p := range []struct{ label, path string }{
		{"agent profile", cfg.AgentProfilePath("claude")},
		{"command profile", cfg.CommandProfilePath()},
	} {
		if err := validateProfile(p.path); err != nil {
			return fmt.Errorf("%s: %w", p.label, err)
		}
		fmt.Fprintf(out, "ok: %s %s validates\n", p.label, p.path)
	}

	fmt.Fprint(out, profileHelp)
	return nil
}
```

Update the doc comment above `configCheckCmd`'s declaration if it promises a
grant listing.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./agent-sandbox/cmd/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent-sandbox/cmd/profiles.go agent-sandbox/cmd/ai.go agent-sandbox/cmd/ai_test.go
git commit -m "refactor(ai): validate both profiles instead of describing one

What: config-check now stats each profile and runs nono profile validate
on it, then points at nono profile show / nono why. The resolved-grant
listing and its FilesystemGrants source are gone.

Why: agent-sandbox no longer resolves profiles, so any listing it printed
would be a second copy of what nono already knows."
```

---

### Task 5: doctor validates both profiles and probes the socket variable

**Files:**
- Modify: `agent-sandbox/cmd/doctor.go` (imports, seams, `checkProfiles`)
- Modify: `agent-sandbox/cmd/doctor_test.go`

**Interfaces:**
- Consumes: `validateProfile` (Task 4), `config.AgentProfilePath` (Task 1).
- Produces: `var runCommandEnv func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)`; `func checkBrokerSocketVar(profilePath string) error`.

- [ ] **Step 1: Write the failing tests**

In `agent-sandbox/cmd/doctor_test.go`, add a stub helper beside `stubRunCommand`:

```go
func stubRunCommandEnv(rc func(context.Context, []string, string, ...string) ([]byte, error)) func() {
	orig := runCommandEnv
	runCommandEnv = rc
	return func() { runCommandEnv = orig }
}

// configWithBothProfiles writes a config plus both profiles beside it.
func configWithBothProfiles(t *testing.T, dir string) *config.Config {
	t.Helper()
	for _, name := range []string{"command-profile.json", "claude-profile.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	if err := os.WriteFile(cfgPath, []byte("tool_mode = \"hook\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}
```

Then add:

```go
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
		return []byte(brokerSocketProbeValue), nil
	})
	defer restoreEnv()
	defer stubSelfPath("/usr/local/bin/agent-sandbox")()

	got := checkProfiles(cfg)
	if !got.ok {
		t.Fatalf("checkProfiles ok = false, want true; details %v hint %q", got.details, got.hint)
	}
	if !slices.Contains(validated, filepath.Join(dir, "claude-profile.json")) {
		t.Errorf("the agent profile was not validated; validated %v", validated)
	}
}

// A profile that does not forward the broker socket variable produces a
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

	got := checkProfiles(cfg)
	if got.ok {
		t.Error("checkProfiles ok = true, want false when the socket variable is stripped")
	}
	if !strings.Contains(strings.Join(got.details, "\n"), broker.SocketEnvVar) {
		t.Errorf("details must name the variable: %v", got.details)
	}
	if got.hint == "" {
		t.Error("a failing check must carry a hint")
	}
}

// Inside a session the probe would nest one nono sandbox in another. Skip it
// and say so, rather than reporting a failure the operator cannot act on.
func TestCheckProfiles_SkipsTheProbeInsideASession(t *testing.T) {
	t.Setenv(broker.SocketEnvVar, "/tmp/some-broker.sock")
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

	got := checkProfiles(cfg)
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
```

Then update the existing tests, by name:

- Delete `TestCheckProfiles_ValidatesBothProfiles` — the new
  `TestCheckProfiles_ValidatesTheAgentProfile` above replaces it, and its
  "the agent profile is generated into a temp file, so its path is not
  predictable" premise is gone.
- `TestCheckProfiles_FailsWhenTheAgentProfileIsRejected`: keep the case, change
  its stub condition from `args[2] != profile` to `args[2] != commandProfile`
  built from `configWithBothProfiles`, and change the hint assertion from
  `"[sandbox.agent]"` to `"[agents.claude].profile"`.
- These call `checkProfiles` or `runDoctor` and expect to get past the agent
  profile, so each needs a `claude-profile.json` beside its config (switch to
  `configWithBothProfiles`) and a `stubRunCommandEnv` returning
  `[]byte(brokerSocketProbeValue)`:
  `TestCheckProfiles_FailsWhenValidateRejects`,
  `TestCheckProfiles_FailsWhenSelfPathErrors`,
  `TestCheckProfiles_FailsWhenTheWriteQueryErrors`,
  `TestCheckProfiles_FailsWhenNonoSaysTheBinaryIsWritable`,
  `TestRunDoctor_AllOK`, `TestRunDoctor_NonoNG`,
  `TestRunDoctor_RunsAllChecksEvenOnEarlyFailure`.
- `TestCheckProfiles_FailsWhenTheFileIsMissing` and
  `TestRunDoctor_MissingCommandProfileReportsActionableHint` keep their
  missing-file premise; they need only the `claude-profile.json` so the failure
  they assert is still the command profile's.

Add `slices` and the `broker` package to the test file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./agent-sandbox/cmd/ -run CheckProfiles`
Expected: build failure — `undefined: runCommandEnv`, `undefined: brokerSocketProbeValue`

- [ ] **Step 3: Implement the seam and the probe**

In `agent-sandbox/cmd/doctor.go`, delete the `sandboxhost` import, add the
`broker` import, and extend the seam block:

```go
var (
	lookPath      = exec.LookPath
	runCommand    = defaultRunCommand
	runCommandEnv = defaultRunCommandEnv
)

// defaultRunCommandEnv runs a command with extra environment on top of this
// process's own. It is separate from defaultRunCommand because its only
// caller starts a sandbox, which is slower than the profile queries the
// five-second budget was sized for.
func defaultRunCommandEnv(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	return cmd.CombinedOutput()
}

// brokerSocketProbeValue is the sentinel checkBrokerSocketVar looks for. Any
// value would do; an obviously synthetic one keeps a failure legible.
const brokerSocketProbeValue = "agent-sandbox-doctor-probe"

// checkBrokerSocketVar measures whether the agent profile forwards
// AGENT_SANDBOX_BROKER_SOCKET into the sandbox. nono cannot be asked: `nono
// profile show` does not report environment.allow_vars, and `nono why` has no
// env query. Without the variable the agent never reaches the broker and every
// command fails for a reason nothing on screen explains, so this is measured
// rather than assumed.
//
// --allow-cwd is required because nono refuses working-directory access in
// non-interactive mode, which is how doctor runs.
func checkBrokerSocketVar(profilePath string) error {
	out, err := runCommandEnv(context.Background(),
		[]string{broker.SocketEnvVar + "=" + brokerSocketProbeValue},
		"nono", "wrap", "--silent", "--allow-cwd", "--profile", profilePath,
		"--", "sh", "-c", "echo $"+broker.SocketEnvVar)
	if err != nil {
		return fmt.Errorf("could not run the probe: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), brokerSocketProbeValue) {
		return fmt.Errorf("the profile does not forward %s into the sandbox", broker.SocketEnvVar)
	}
	return nil
}
```

Rewrite `checkProfiles`:

```go
// checkProfiles validates both nono profiles and measures the one thing nono
// cannot report. agent-sandbox generates neither file, so every check here is
// either nono's own answer or a direct measurement.
//
// Each failure otherwise produces a session that refuses every command with an
// error the agent cannot act on: nono's own failure arrives on the broker's
// stderr long after the launcher has returned.
func checkProfiles(cfg *config.Config) checkResult {
	r := checkResult{name: "profiles"}
	agentPath := cfg.AgentProfilePath("claude")
	cmdPath := cfg.CommandProfilePath()
	r.details = append(r.details, "agent profile: "+agentPath, "command profile: "+cmdPath)

	if err := validateProfile(agentPath); err != nil {
		r.details = append(r.details, "agent profile: "+err.Error())
		r.hint = "write the agent profile, or point [agents.claude].profile at it, " +
			"until `nono profile validate` passes"
		return r
	}
	if err := validateProfile(cmdPath); err != nil {
		r.details = append(r.details, "command profile: "+err.Error())
		r.hint = "write the profile, or point command_profile at it"
		return r
	}

	switch {
	case os.Getenv(broker.SocketEnvVar) != "":
		// Running inside a session: the probe would nest one nono sandbox in
		// another. Say so rather than report a failure nobody can act on.
		r.details = append(r.details,
			"broker socket variable: skipped (running inside a session; run doctor on the host)")
	default:
		if err := checkBrokerSocketVar(agentPath); err != nil {
			r.details = append(r.details, "broker socket variable: "+err.Error())
			r.hint = "add " + broker.SocketEnvVar + " to the agent profile's " +
				"environment.allow_vars; without it the agent cannot reach the broker " +
				"and every command fails"
			return r
		}
		r.details = append(r.details, "broker socket variable: forwarded")
	}

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
	writable, werr := profileAllowsWrite(cmdPath, self)
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
			cmdPath + " --path " + self + " --op write` names it)"
		return r
	}

	r.ok = true
	return r
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./agent-sandbox/cmd/`
Expected: PASS

- [ ] **Step 5: Verify against the real nono**

Run: `go run . doctor`
Expected: `[OK] profiles` with details naming both profile paths and
`broker socket variable: forwarded`

- [ ] **Step 6: Commit**

```bash
git add agent-sandbox/cmd/doctor.go agent-sandbox/cmd/doctor_test.go
git commit -m "feat(doctor): validate both profiles and probe the socket var

What: checkProfiles validates the agent profile as well as the command
profile, then runs a one-off sandbox to confirm the profile forwards
AGENT_SANDBOX_BROKER_SOCKET. The probe is skipped inside a session,
where it would nest one sandbox in another.

Why: the agent profile is hand-written now, and nono reports nothing
about env grants -- a stripped socket variable would otherwise surface
as every command failing for no stated reason."
```

---

### Task 6: `ai explain` stops describing capabilities

**Files:**
- Modify: `agent-sandbox/internal/agentconfig/agentconfig.go` (imports, `explainView`, `Explain`)
- Modify: `agent-sandbox/internal/agentconfig/explain.tmpl` (the "Changing the config" section only)
- Modify: `agent-sandbox/internal/agentconfig/agentconfig_test.go`

**Interfaces:**
- Consumes: `config.AgentProfilePath` (Task 1).
- Produces: `explainView.AgentProfilePath string`; `explainView.Capabilities` is removed.

- [ ] **Step 1: Write the failing test**

Append to `agent-sandbox/internal/agentconfig/agentconfig_test.go`:

```go
// The agent is told where its own sandbox is defined and which nono commands
// answer questions about it — not a capability vocabulary that no longer
// exists.
func TestExplain_NamesBothProfilesAndTheNonoCommands(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	if err := os.WriteFile(cfgPath, []byte("tool_mode = \"hook\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"command-profile.json", "claude-profile.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	out := agentconfig.Explain(cfg, cfgPath)
	for _, want := range []string{
		filepath.Join(dir, "claude-profile.json"),
		filepath.Join(dir, "command-profile.json"),
		"nono profile show",
		"nono why",
		"[agents.claude]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output missing %q\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"capabilities = []", "[sandbox.agent]"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("explain output still mentions %q\n%s", unwanted, out)
		}
	}
}
```

The file already imports `os`, `path/filepath`, `strings`, `config` and
`agentconfig`; drop its `sandboxhost` import and delete every existing
assertion that the rendered output lists capability names (they are the only
uses of that import).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./agent-sandbox/internal/agentconfig/`
Expected: FAIL — the rendered output still carries `[sandbox.agent]` and the
capability list

- [ ] **Step 3: Update the view**

In `agent-sandbox/internal/agentconfig/agentconfig.go`, delete the
`sandboxhost` import, remove the `Capabilities` field from `explainView`, and
add:

```go
	// AgentProfilePath is the nono profile the launched agent itself runs
	// under (cfg.AgentProfilePath). Like ProfilePath it is a pointer, not a
	// description: agent-sandbox neither generates nor reads it.
	AgentProfilePath string
```

In `Explain`, replace `Capabilities: sandboxhost.CapabilityNames(),` with:

```go
		AgentProfilePath: cfg.AgentProfilePath("claude"),
```

- [ ] **Step 4: Rewrite the template's config section**

In `agent-sandbox/internal/agentconfig/explain.tmpl`, replace everything from
`## Changing the config` to the end of the file with:

```
## Changing the config
The sandbox is configured by `{{.ConfigPath}}`, which names two nono profiles.
You may edit any of the three files directly.

- Your own sandbox — this process, its file tools, and any MCP server it spawns
  as a direct child — is `{{.AgentProfilePath}}`.
- The sandbox every shell command runs in is `{{.ProfilePath}}`.

Both are written in nono's own schema. agent-sandbox does not generate, read,
or validate either one; it hands each path to nono. So this document cannot
tell you what they grant — nono can:

- `nono profile show <path>` resolves a profile, groups expanded and `~` and
  `$XDG_CACHE_HOME` filled in.
- `nono why --profile <path> --path <p> --op read|write|readwrite` says whether
  an access is allowed and which grant decides it. `--host`, `--command` and
  `--scope` ask the same question of network, command policy, and scope.

One thing neither command reports: environment variables. `nono profile show`
does not print `environment.allow_vars`, so if a variable is not reaching a
process, read that list in the profile file itself. `agent-sandbox doctor`
measures the one variable that matters most — `AGENT_SANDBOX_BROKER_SOCKET`,
without which nothing can reach the command broker at all.

Editing any of this **does not affect the current session**: both profiles are
read once, when the session starts. An edit takes effect the next time the
operator runs `agent-sandbox claude`.

After editing, run `agent-sandbox ai config-check` — it loads the config and
hands both profiles to `nono profile validate`, so a passing check means none
of the three files is what breaks the next launch. `agent-sandbox doctor` goes
further: it adds the broker-socket probe and asks nono whether the command
profile leaves the broker's own binary writable.

Schema:

```toml
tool_mode = "hook"                       # "hook" | "mcp"
command_profile = "command-profile.json" # default name; shared by every agent

[mcp]
command_output_dir = "..."               # mcp tool_mode only: where command output is written

[agents.claude]
profile = "claude-profile.json"          # default name: "<agent>-profile.json"
```

Both profile paths resolve beside `{{.ConfigPath}}` unless written absolute.

A user-scope config at `~/.config/agent-sandbox/config.toml` is composed with
this file: every value the project file sets wins, and an agent the user-scope
file declares alone still applies. If a profile path you did not write shows up
in `agent-sandbox ai config-check`, it comes from there.
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./agent-sandbox/internal/agentconfig/ ./agent-sandbox/cmd/`
Expected: PASS

- [ ] **Step 6: Read the rendered output once**

Run: `go run . ai explain`
Expected: the "Changing the config" section names both profile paths, the nono
commands, and the `[agents.claude]` schema; no capability list anywhere.

- [ ] **Step 7: Commit**

```bash
git add agent-sandbox/internal/agentconfig/
git commit -m "docs(explain): point the agent at nono, not at capabilities

What: explain names both profile paths and the nono commands that resolve
them, states that env grants are invisible to profile show, and drops the
capability vocabulary and the [sandbox.agent] schema.

Why: the agent's sandbox is now a file the operator writes; the only
authority on what it grants is nono."
```

---

### Task 7: Delete the generator and the `[sandbox]` surface

Nothing references `sandboxhost` after Task 6, so it comes out whole here,
together with the config surface that fed it.

**Files:**
- Delete: `agent-sandbox/internal/sandboxhost/` (all five files)
- Modify: `agent-sandbox/internal/config/config.go`
- Modify: `agent-sandbox/internal/config/errors.go`
- Modify: `agent-sandbox/internal/config/config_test.go`
- Modify: `agent-sandbox/internal/config/compose_test.go`
- Modify: `agent-sandbox.toml`, `agent-sandbox/config.example.toml`

**Interfaces:**
- Consumes: nothing new.
- Produces: `config.ErrMovedAgentSectionToProfile`; `Config.Sandbox`, `SandboxConfig`, `HostConfig`, `cloneHost`, `unionHost`, `dedupUnion` and `ErrAllowEnvNonoVar` no longer exist.

- [ ] **Step 1: Write the failing test**

Append to `agent-sandbox/internal/config/config_test.go`:

```go
// [sandbox] and everything under it are gone. A config that still carries one
// must fail loudly, naming where the grants moved — a half-ignored section is
// a sandbox that silently grants less than its author believes.
func TestLoadRejectsTheSandboxSection(t *testing.T) {
	for _, body := range []string{
		"[sandbox.agent]\ncapabilities = [\"go\"]\n",
		"[sandbox.agent]\nallow = [\"/opt\"]\n",
		"[sandbox.agent]\nallow_env = [\"FOO\"]\n",
		"[sandbox]\n",
	} {
		t.Run(body, func(t *testing.T) {
			_, err := config.Load(writeToml(t, "tool_mode = \"hook\"\n\n"+body))
			if err == nil {
				t.Fatal("expected an error for a config still declaring [sandbox], got nil")
			}
			if !strings.Contains(err.Error(), "[agents.") {
				t.Errorf("the error must name where the grants moved: %v", err)
			}
		})
	}
}

```

Add `strings` to the file's imports if it is not already there. No separate
ordering test is needed: the existing `TestLoad_RejectsMovedKeys` and its
siblings already fail if the catch-all is placed ahead of the sentinels that
name specific keys (Step 5 lists them).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./agent-sandbox/internal/config/ -run Sandbox`
Expected: FAIL — `[sandbox.agent]` still decodes without error

- [ ] **Step 3: Delete the generator**

```bash
git rm -r agent-sandbox/internal/sandboxhost
```

- [ ] **Step 4: Strip the config surface**

In `agent-sandbox/internal/config/config.go`:

- Delete the `Sandbox SandboxConfig` field from `Config`, the `SandboxConfig`
  and `HostConfig` types, and the `cloneHost`, `unionHost` and `dedupUnion`
  functions.
- In `Load`, delete the `userAgent` variable, the `userAgent = cloneHost(...)`
  line, and step 3's `cfg.Sandbox.Agent = unionHost(userAgent, cfg.Sandbox.Agent)`.
  Renumber the remaining comment steps and replace step 1's slice-aliasing
  paragraph with:

  ```go
	// 1. User config is the base (optional). Every field is a scalar or a map
	//    of scalars, so the project decode below simply overrides what it
	//    declares — a map key present in both is replaced whole, not merged.
  ```

- In `validate`, delete the `allow_env` NONO_* loop and its comment.
- In `checkDeprecated`, add as the **last** check, immediately before
  `return nil`:

  ```go
	if md.IsDefined("sandbox") {
		// Last, so every sentinel above still wins for the key it names.
		return ErrMovedAgentSectionToProfile
	}
  ```

  and delete the now-unreachable `md.IsDefined("sandbox", "host")` and
  `md.IsDefined("sandbox", "container")` checks only if their tests are also
  deleted — otherwise leave them where they are, above the new catch-all.
- Drop the `slices` import if nothing else uses it.

In `agent-sandbox/internal/config/errors.go`, delete `ErrAllowEnvNonoVar` and
add:

```go
// ErrMovedAgentSectionToProfile fires on [sandbox] and everything under it.
// The section described host access agent-sandbox turned into a nono profile;
// nothing generates profiles now, so the grants live in the file named by
// [agents.<name>].profile, written in nono's own schema.
var ErrMovedAgentSectionToProfile = errors.New("[sandbox] is no longer supported: agent-sandbox does not generate nono profiles; write the grants in the profile named by [agents.<name>].profile")
```

- [ ] **Step 5: Delete and adjust the orphaned tests**

In `agent-sandbox/internal/config/config_test.go`, delete these five tests
outright — every one of them exists to check a field that no longer exists:
`TestLoad_EmptyAllow`, `TestLoad_AllowOmitted`, `TestLoad_Compose_ListUnion`,
`TestLoad_AgentAllowEnv`, `TestLoad_RejectsNonoAllowEnv`.

Adjust three more:

- `TestLoad_ValidConfig`: drop the `[sandbox.agent] allow = [...]` block from
  the fixture and the `cfg.Sandbox.Agent.Allow` assertion, keeping the
  `CommandOutputDir` one.
- `TestLoad_MissingMCPCommandOutputDir`: replace the whole fixture body with
  `writeToml(t, "")` — an empty config still defaults `tool_mode` to `mcp` and
  so still fails on the missing output dir, which is the test's actual subject.
- `TestLoad_OldKeysRejected`: its fixture declares a bare `[sandbox]`, so the
  new catch-all now fires first. Change `want` to
  `config.ErrMovedAgentSectionToProfile` and rewrite the failure message to
  "old keys must be rejected by name, not silently ignored".

Leave every other deprecation test alone: `TestLoad_RejectsMovedKeys`,
`TestLoad_DeprecatedAllowCIDRs_Rejected`, `TestLoad_DeprecatedAllowHosts_Rejected`,
`TestLoad_RejectsRemovedContainerSection` (and its `_UserScope` twin),
`TestLoadRejectsAllowCommands`, `TestLoadRejectsDropCommands`,
`TestLoadRejectsSharedSection` and `TestLoadRejectsShellSection` all name keys
whose sentinels are checked before the catch-all. They are the ordering
regression test: put the catch-all first and every one of them fails.

In `agent-sandbox/internal/config/compose_test.go`, delete `TestDedupUnion` and
keep the two `userConfigPath` tests.

- [ ] **Step 6: Update this repository's config and the example**

In `agent-sandbox.toml`, delete the `[sandbox.agent]` block and the comment
paragraphs above it that describe capabilities, keeping `tool_mode`, the
`[agents.claude]` block from Task 2, and rewriting the intro comment to:

```toml
  # agent-sandbox generates no nono profile. Two files, both written in nono's
  # own schema, decide everything: claude-profile.json is the sandbox the agent
  # process itself runs in (its file tools, and any MCP server it spawns as a
  # direct child — those are not brokered), and command-profile.json is the
  # sandbox every brokered shell command runs in. agent-sandbox only names
  # them; `nono profile show <path>` is what resolves one.
```

Rewrite `agent-sandbox/config.example.toml` to match:

```toml
# Tool execution mode: "mcp" (agent uses the run_command MCP tool; Bash/Monitor
# disabled) or "hook" (agent uses Bash/Monitor, routed via a PreToolUse hook that
# `agent-sandbox claude` injects at launch). Default: "mcp".
tool_mode = "mcp"

# The nono profile every brokered command runs under. Default name, resolved
# beside this file. agent-sandbox does not generate or read it.
command_profile = "command-profile.json"

[mcp]
command_output_dir = "/tmp/mcp-output"

# The nono profile the launched agent itself runs under — its own file tools,
# and any MCP server it spawns as a direct child (those are not brokered, so a
# Python- or Go-based MCP server needs its runtime granted there). Shell
# commands do not run in it: they go to the broker, under command_profile.
#
# The default name is "<agent>-profile.json", so this key may be omitted.
# A missing file is a launch error; there is no generated default.
[agents.claude]
profile = "claude-profile.json"
```

- [ ] **Step 7: Run the whole suite**

Run: `go test ./...`
Expected: all packages ok, and `internal/sandboxhost` no longer listed

Run: `go run . ai config-check`
Expected: `ok:` lines for the config and both profiles

- [ ] **Step 8: Commit**

```bash
git add -A agent-sandbox/ agent-sandbox.toml
git commit -m "refactor(config): delete the profile generator and [sandbox]

What: removes internal/sandboxhost whole -- the capability catalog, the
profile generator, the protected-path guard and the deny-rule rendering
-- along with Config.Sandbox, HostConfig, the list-union merge and the
allow_env guard. [sandbox] now fails with a sentinel naming
[agents.<name>].profile.

Why: nothing generates profiles any more, so a second description of what
they grant could only drift from nono's."
```

---

### Task 8: Documentation

**Files:**
- Modify: `README.md` (sections at lines 262-403, 404-766, 767-779, 780-800, 225-261)
- Modify: `README.ja.md` (the matching sections)

**Interfaces:**
- Consumes: everything above. Produces: no code.

- [ ] **Step 1: Rewrite the configuration sections in `README.md`**

- `### Host access: [sandbox.agent]` → `### The agent profile: [agents.<name>].profile`.
  State the resolution table (omitted → `<agent>-profile.json` beside the
  config; relative → joined onto the config's directory; absolute → itself),
  that a missing file is a launch error, and that agent-sandbox does not
  generate, read, or validate it.
- `### Capabilities` → delete the section entirely, and remove its entry from
  the `## Contents` list at line 38.
- `### The command profile` → rename to `### The two profiles` and open with
  what each governs: the agent process and its direct children versus every
  brokered command. Carry over the existing command-profile content beneath.
  Add the paragraph that nothing needed only to *run a command* belongs in the
  agent profile, and that `AGENT_SANDBOX_BROKER_SOCKET` must be in the agent
  profile's `allow_vars`.
- `## Environment variables (--env)` → state that `--env` loads a file's
  variables into the launcher process and no longer grants them; a variable
  reaches the agent only if the agent profile's `environment.allow_vars` lists
  it (globs such as `MISE*` work). Delete the sentence about `[sandbox.agent].allow_env`.
- `### User-scope config` → replace the list-union paragraph: values are
  scalars now, the project file wins for anything it sets, and an agent
  declared only in the user-scope file still applies.
- `### doctor` → add the two new checks: the agent profile is validated too,
  and the broker-socket probe (with the note that it is skipped inside a
  session).

- [ ] **Step 2: Mirror the changes in `README.ja.md`**

Apply the same six edits to the matching Japanese sections, keeping the
existing translation's register.

- [ ] **Step 3: Verify the docs match the code**

Run: `grep -rn "sandbox.agent\|capabilities" README.md README.ja.md agent-sandbox/config.example.toml agent-sandbox.toml`
Expected: no hits describing the removed config surface (hits inside quoted
error text or historical spec references are fine; there should be none in
these four files)

Run: `go test ./...`
Expected: all packages ok

- [ ] **Step 4: Commit**

```bash
git add README.md README.ja.md
git commit -m "docs(readme): describe two hand-written profiles

What: replaces the [sandbox.agent] and Capabilities sections with the
agent profile's path resolution, documents that --env no longer grants,
and adds doctor's new checks.

Why: the capability vocabulary the docs taught no longer exists."
```

---

## Notes for the executor

- **Ordering matters.** Tasks 2 through 6 each remove one consumer of
  `internal/sandboxhost`; Task 7 deletes the package once nothing imports it.
  Running Task 7 early breaks the build.
- **The repository stays launchable throughout.** Task 2 adds
  `claude-profile.json` in the same commit that starts requiring it, and Task 7
  removes `[sandbox.agent]` only after the last reader of it is gone.
- **`nono profile validate` is the acceptance test for any profile you write.**
  It catches unknown keys and bad group references; it does not catch a path
  that does not exist on this host (that is a launch-time warning).
- **When a test needs a config**, build it through `config.Load` — `Config.dir`
  is unexported, so a struct literal resolves both profile paths against the
  process working directory instead of the config's.
