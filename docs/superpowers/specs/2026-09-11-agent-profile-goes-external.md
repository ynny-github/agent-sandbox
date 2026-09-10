# The agent profile goes external, one per agent

Date: 2026-09-11
Status: designed
Prerequisite reading: [2026-09-05-broker-runs-commands-inside-the-sandbox.md](2026-09-05-broker-runs-commands-inside-the-sandbox.md)

Everything below marked *measured* was measured on 2026-09-11 with nono 0.74.0,
Linux 6.18.42, NixOS.

## The shape

agent-sandbox stops generating nono profiles. Both profiles become files the
operator writes in nono's own schema, named by path from `agent-sandbox.toml`,
and agent-sandbox hands each path to `nono` without reading it.

```toml
tool_mode = "hook"
command_profile = "command-profile.json"   # shared by every agent

[agents.claude]
profile = "claude-profile.json"
```

The agent profile is per agent, because the profile *is* the difference between
one agent and another: a launch subcommand's name selects the table, and the
table names the sandbox that agent runs in. The command profile stays shared —
what a command may touch in this repository does not depend on who asked for it.

After this change the answer to "where is nono configured?" has exactly one
form: in a file, by path, next to the project config. Today it has two, because
the agent's half is still generated from `[sandbox.agent]`.

## What agent-sandbox stops knowing

Three things go with the generator, each deliberately.

**The capability catalog.** `go`, `python`, `node`, `rust`, `dart`, `flutter`,
`docker`, `ssh`, `mise`, `bashrc` — the named bundles, their per-OS branches,
and the reasons written beside them — are deleted. The operator writes nono
groups and paths directly, the way `command-profile.json` already does. This is
the same trade accepted for the command profile on 2026-09-10: per-bundle
curation for a boundary one file can state, with the drift understood.

**The Claude permission-deny rules.** agent-sandbox no longer injects
`Read(...)`/`Edit(...)` denies into `claude --settings`. Those rules existed to
keep the agent's own file tools away from credentials the profile had to grant
anyway. They are largely moot once the agent profile grants no credential (see
"The agent needs no command permissions" below); the one residual is recorded
at the end of this document.

**Every profile-shaped validation.** `isProtected` (raw grants that name
`~/.ssh`, `~/.aws`, …), the `allow_env` guard against `NONO_*`, the
capability-name check — all of them re-expressed something nono validates
itself. `nono profile validate` is the check that remains.

## The agent needs no command permissions

The agent process never runs a shell command: in hook mode a PreToolUse hook
routes Bash and Monitor into the broker, and in mcp mode Bash is disabled
outright. Every toolchain grant that reached the agent profile through
`[sandbox.agent].capabilities` was therefore paying for something that happens
in the *other* sandbox.

What is left is what the agent process and its **direct children** — an MCP
server it spawns is not brokered — actually touch.

This repository's `claude-profile.json`:

```jsonc
{
  "extends": "claude",
  "meta": { "name": "custom claude" },

  // NixOS: every executable lives under /nix/store, reached through the
  // /run/current-system/sw symlink farm. nono's base profile grants that tree
  // read but not execute, so without this the agent itself cannot start:
  // execve is refused and the process exits 127 with no output.
  "groups": { "include": ["nix_runtime"] },

  "filesystem": {
    // The agent's own file tools write scratch files here and read them back;
    // the claude base grants /tmp write-only, which fails on the read-back.
    // In mcp mode this also covers mcp.command_output_dir (/tmp/mcp-output).
    //
    // Measured: the claude base already grants /tmp/claude-$UID read+write, so
    // a hook-mode session that only ever touches the scratchpad can drop this
    // line. It is kept because mcp mode's output directory is not under it.
    "allow": ["/tmp"],

    "read": [
      "/nix/store", "/run/current-system/sw",
      // GitHub MCP runs `docker run -i --rm ghcr.io/github/github-mcp-server`
      // as a direct child of the agent, so its config is read here rather than
      // in the command profile. Drop these two lines (and the bypass below)
      // when GITHUB_MCP_TOKEN is not used.
      "~/.docker", "~/.orbstack"
    ],

    "allow_file": ["/dev/null"],
    "bypass_protection": ["~/.docker"]
  },

  "environment": {
    // AGENT_SANDBOX_BROKER_SOCKET is load-bearing: without it nono strips the
    // variable, the agent cannot reach the broker, and *every* command fails
    // with an error that names nothing.
    "allow_vars": ["PATH", "HOME", "TERM", "LANG", "LC_ALL", "USER",
                   "AGENT_SANDBOX_BROKER_SOCKET"]
  }
}
```

Compared with what `[sandbox.agent]` generates today, this drops
`go_runtime`/`python_runtime` and their caches, `~/.local/share/mise`,
`~/.config/mise`, `MISE*`, `~/.bashrc` and `/etc/bashrc`, `~/.ssh` with its
`known_hosts` and bypass, and the `git_config` group. The working directory and
`~/.claude` are the `claude` base profile's, and the worktree's main git
directory is still added by the launcher as a `--allow` flag, not in the file.

Three notes for whoever edits this file next:

- **No `$WORKDIR` entry.** The base profile covers the working directory. The
  command profile names `$WORKDIR` because it extends a different base.
- **Linux paths are written literally.** `perOSAllow`'s linux/darwin branch is
  gone with the catalog; a profile that must run on both writes nono's
  `platform_overrides`.
- **`git_config` was dropped on purpose.** If Claude Code turns out to run
  `git` directly rather than through the Bash tool, the symptom is a git error
  naming the config it could not read, and the fix is to add the group back.

## Configuration

### `[agents.<name>].profile`

The key resolves exactly like `command_profile`, so there is one rule rather
than two:

| written | resolves to |
| --- | --- |
| omitted | `<name>-profile.json` beside `agent-sandbox.toml` |
| relative path | joined onto the directory of `agent-sandbox.toml` |
| absolute path | itself |

The table's key is the launch subcommand's name (`agent-sandbox claude` →
`[agents.claude]`), which replaces `sandboxhost.agentBases` as the place an
agent name is recognised. A missing profile file is a launch error; there is no
generated fallback, matching the command profile.

A user-scope config (`~/.config/agent-sandbox/config.toml`) may set the key,
and a relative value there still resolves against the *project* config's
directory — the same behaviour `command_profile` has today. That makes "every
project has a `claude-profile.json` beside its config" declarable once.

### Removed keys

`[sandbox]` and everything under it (`[sandbox.agent]`, its `capabilities`,
`allow`, `read`, `allow_file`, `read_file`, `allow_env`) are rejected by
`checkDeprecated` with a sentinel naming `[agents.<name>].profile`. It is a
catch-all on `IsDefined("sandbox")` and therefore goes *last*, after every
existing `sandbox.*` sentinel: the file's most-specific-first ordering is what
lets the older, more precise messages still win for the keys they name.

### `--env` changes meaning

`--env <ref>` keeps loading a file's variables into the launcher's own process
environment. It no longer grants them: nono forwards only what the profile's
`environment.allow_vars` lists, and that list is now hand-written. A glob
(`MISE*`) covers a family in one line. The README section documents the change
in one paragraph — a value silently not reaching the agent is exactly the
failure this must pre-empt.

## What each command becomes

**`agent-sandbox claude`** passes `cfg.AgentProfilePath(agentName)` straight to
`nono wrap --profile`. No temp file, no cleanup, no deny rules folded into
`--settings` (which survives only to inject the PreToolUse hook).

**`agent-sandbox doctor`** stats and `nono profile validate`s both profiles,
keeps the `nono why` check that the broker binary is not writable through the
command profile, and gains one probe:

```
AGENT_SANDBOX_BROKER_SOCKET=<sentinel> \
  nono wrap --profile <agent profile> -- sh -c 'echo $AGENT_SANDBOX_BROKER_SOCKET'
```

doctor sets the variable itself and checks whether the sentinel survives into
the sandbox, so the probe answers the same question at doctor time that the
launcher will ask at launch time.

A hand-written profile that omits that variable produces a session where every
command fails for a reason nothing on screen explains. The probe measures the
answer instead of parsing the file, which is the rule this repository settled on
in `e69e92b` ("ask nono instead of parsing profiles"). It costs one sandbox
start.

**`agent-sandbox ai config-check`** reports that the config loads and that both
profiles exist and validate. The "The launched agent's own sandbox additionally
reaches: …" listing is deleted along with `FilesystemGrants`.

**`agent-sandbox ai explain`** (what the sandboxed agent reads) stops listing
capabilities. It names the two profile paths and hands the agent the two
commands that answer any question about them:

- `nono profile show <path>` — the fully resolved grants, groups expanded
- `nono why --profile <path> --path <p> --op <op>` — why one access was refused

and states that editing a profile takes effect at the next launch.

*Measured*: both commands work from inside a sandbox —
`nono wrap --profile agent.json -- nono profile show agent.json` and
`-- nono why --profile agent.json --path /nix/store --op read` both answer
normally, because neither builds a sandbox of its own and so neither trips
nono's refusal to nest. The agent can therefore run what `explain` tells it to.

### What nono cannot answer

Delegation is not total, and the gap falls exactly where this design puts its
weight. *Measured* on nono 0.74.0:

- **`nono profile show` does not print `environment.allow_vars` at all.** The
  76-line output has no environment section. `nono why` has no env-var query
  either — its four query forms are `--command`, `--path`, `--host`, `--scope`.
  So "why is this variable not reaching the agent?" cannot be asked of nono.
  With `--env` no longer granting and `AGENT_SANDBOX_BROKER_SOCKET` load-bearing,
  this is the one question that matters most: doctor's probe above is the only
  way to answer it, and `explain` must say plainly that env grants are invisible
  to `profile show` and live in the file's `environment.allow_vars`.
- **Meaning is agent-sandbox's.** Which profile governs which sandbox, that the
  broker socket variable is required, that an edit takes effect at the next
  launch — nono knows none of it. That is all `explain` has left to say.
- **`validate` is syntax and group references.** A path that does not exist is
  a launch-time warning, not a validation failure (*measured*: `~/.docker` is
  absent on this host, so its `bypass_protection` entry is skipped with a
  warning and the deny rule stays in force).

**`agent-sandbox debug`** is unchanged except that `--profile` now names a real
file, so its output can be pasted into `nono profile show` directly.

## What is deleted

| what | why it goes |
| --- | --- |
| `internal/sandboxhost/**` (~1,080 lines with tests) | the generator, the catalog, `WriteProfile`, `isProtected`, deny-rule rendering |
| `config.SandboxConfig`, `config.HostConfig` | no section left to decode |
| `unionHost`, `cloneHost`, `dedupUnion` and the TOML slice-aliasing workaround in `Load` | the merged config has no list fields; user→project merging is scalars only |
| `ErrAllowEnvNonoVar` and its check | no `allow_env` key to guard |
| `runDeps.writeProfile` and its temp-file lifecycle | the path comes from config |
| the `denyRules` parameter of `BuildArgs`/`settingsJSON` | no rules to inject |
| `cfg.Sandbox.Agent.AllowEnv = append(...)` in `cmd/claude.go`, `cmd/debug.go` | `--env` no longer grants |
| `Resolved.FilesystemGrants`, `printList` in `cmd/ai.go` | nothing resolved to report |
| `explainView.Capabilities` and the capability section of `explain.tmpl` | no catalog |

`internal/safe/**` is not touched: it stays unwired, per the 2026-09-10 parking
decision.

## Tests

Deleted: the `internal/sandboxhost` suites, and the config tests covering list
union, capability names, and `allow_env`.

Added:

- `AgentProfilePath`: default name, relative, absolute, `[agents]` table absent,
  and a name with no table.
- `checkDeprecated`: `[sandbox]`, `[sandbox.agent]`, and each removed list key
  fail with the new sentinel, while the older `sandbox.*` sentinels still fire
  for the keys they name.
- `BuildArgs`: the configured path reaches `--profile` verbatim; `--settings`
  carries the hook and nothing else.
- `doctor`: both profiles are validated; a profile missing
  `AGENT_SANDBOX_BROKER_SOCKET` fails the probe with an actionable hint.

`e2e/test_mcp_stdio.py` must still pass unchanged.

## Migration inside this repository

1. Write `claude-profile.json` as above.
2. Rewrite `agent-sandbox.toml`: drop `[sandbox.agent]`, add `[agents.claude]`.
3. Rewrite `agent-sandbox/config.example.toml` the same way.
4. Rewrite the README sections `### Host access: [sandbox.agent]`,
   `### Capabilities` (deleted), `### The command profile` (now "The two
   profiles"), `## Environment variables (--env)`, `### User-scope config`, and
   `### doctor` — and the matching sections of `README.ja.md`.

## Accepted residual: `~/.docker` is readable by the agent's file tools

With the deny rules gone, `~/.docker/config.json` — which holds registry
credentials — is readable through the agent's own Read tool, because GitHub MCP
needs the directory and the profile must grant it. This is the only credential
path that survives the trim; `~/.ssh`, `~/.cargo`, and `~/.dart-tool` are no
longer granted at all, so their deny rules had nothing left to protect.

An operator who does not use GitHub MCP removes the two `read` entries and the
`bypass_protection` line, and the residual is gone.

## Follow-on work

- A second agent (`agent-sandbox codex`, say) is now a subcommand plus an
  `[agents.<name>]` table; nothing else in the config has to move.
- If hand-written agent profiles drift into repetition across projects, nono's
  own named profiles (`~/.config/nono/profiles/`) are the place to factor them —
  `extends` accepts a profile name, and *measured*, it does not accept a path.
