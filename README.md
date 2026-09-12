# agent-sandbox

**English** | [日本語](README.ja.md)

Run an AI coding agent (Claude Code) inside a
[nono](https://github.com/tkancf/nono) sandbox, and mediate every shell
command it issues through a host-side command broker that runs in its own
sibling nono session. What each command may touch is decided once, by a nono
command profile the operator writes — not by `agent-sandbox.toml`.

The point is not to lock the agent out of your machine. It is to make the
boundary *explicit and inspectable*: `agent-sandbox ai explain` tells the
agent which commands run in their own sandbox, which run directly at the
broker's own grants, and why any refusal fired, so a policy denial reads as a
policy denial rather than an unexplained failure worth retrying.

```
launcher
├── nono wrap  --profile <agent profile>    -- claude …         no command control here
└── nono run   --profile <command profile>  -- agent-sandbox broker
                                               │
                                               ├─ exec git → shim → git, its own child sandbox
                                               │                     └─ exec ssh → shim → ssh, its own
                                               └─ exec rg  → runs directly in the broker's own sandbox
```

The two sessions are siblings, never nested. There is no shell in the loop:
the broker parses the agent's command line itself (pipelines, `&&`/`||`/`;`,
redirections, globbing, `$(…)`, `for`/`if`, `cd` and the other shell builtins
all work) and execs each simple command directly. `bash` and `sh` are
themselves declared commands with their own child sandbox, reachable from the
session — invoking a shell directly is mediated the same way `git` is, not
merely something a dispatched toolchain can still reach on its own; see
[The two profiles](#the-two-profiles) for what that toolchain caveat still
covers.

## Contents

- [Requirements](#requirements)
- [Install](#install)
- [Quick start](#quick-start)
- [How it works](#how-it-works)
- [Commands](#commands)
- [Configuration](#configuration)
  - [`tool_mode`](#tool_mode)
  - [The agent profile: `[agents.<name>].profile`](#the-agent-profile-agentsnameprofile)
  - [The two profiles](#the-two-profiles)
  - [User-scope config](#user-scope-config)
- [Environment variables (`--env`)](#environment-variables---env)
- [GitHub MCP](#github-mcp)
- [Development](#development)
- [License](#license)

## Requirements

- [nono](https://github.com/tkancf/nono) on `PATH` — the sandbox engine.
- Go 1.25 or later (to build from source).
- `claude` on `PATH` — for `agent-sandbox claude`.

Run `agent-sandbox doctor` to verify.

## Install

```bash
go install github.com/ynny-github/agent-sandbox@latest
```

Or with [mise](https://mise.jdx.dev/):

```toml
# .mise.toml
[tools]
"go:github.com/ynny-github/agent-sandbox" = "latest"
```

**The installed binary must live outside every path the command profile
grants write access to.** nono refuses to start a session otherwise
(`tool-sandbox policy command binary is replaceable through writable parent
directory`) — a build placed inside the project's own working directory
does not qualify once that directory is the agent's workspace. Install to a
stable, non-project path (what `go install` already does by default) before
writing a command profile that declares `agent-sandbox` as a policy command.

## Quick start

Write `agent-sandbox.toml` in your project root — it only names the two
profile files, it does not build them:

In hook mode the hook is the only thing standing between a Bash call and the
agent's own sandbox, so both of its failure modes are closed:

- **The hook runs but cannot rewrite the call** — unreadable payload, unparseable
  JSON, no command in it — and exits **2**, the one code Claude Code treats as
  blocking, with the reason on stderr. Any other non-zero exit would be a
  *non-blocking* error, after which Claude Code runs the original command
  unwrapped, in the agent's own sandbox, under none of the command profile's
  limits.
- **The hook cannot start at all** — a binary the agent profile does not reach,
  say. Nothing inside the hook can catch that, so the launcher proves it first:
  before handing control to the agent it runs the hook once inside the agent's
  own sandbox with a probe payload and refuses to launch unless the command
  comes back rewritten. `agent-sandbox debug` shows the same invocation.

mcp mode has neither failure: Bash and Monitor are disabled outright, so there
is nothing to bypass.

```toml
tool_mode = "hook"

[agents.claude]
profile = "claude-profile.json"   # the agent process's own nono profile, written by you
```

Then write both profiles yourself, directly in nono's own schema:
`claude-profile.json` (the agent process's own sandbox — see
[The agent profile](#the-agent-profile-agentsnameprofile)) and
`command-profile.json` (every brokered command's sandbox — see
[The two profiles](#the-two-profiles)). There is no default for either: a
missing file is a launch error, and agent-sandbox never generates, reads, or
validates either one beyond its path.

Check that both resolve, then launch:

```bash
agent-sandbox doctor            # nono, the broker socket, and both profiles all usable?
agent-sandbox ai config-check   # does agent-sandbox.toml resolve, and do both profiles validate?
agent-sandbox claude -- --model opus
```

There is no `sandbox up` step. `agent-sandbox claude` starts the host-side
command broker inside its own nono session, launches Claude under a second,
sibling session, and tears the broker down when Claude exits.

## How it works

### The broker is a sandboxed process, not a router

`agent-sandbox claude` starts two sibling nono sessions: one wraps Claude
Code under the *operator-written* agent profile named by
`[agents.<name>].profile`; the other runs `agent-sandbox broker` under the
*operator-written* command profile. Neither profile is generated — both are
files you write in nono's own schema, and agent-sandbox does no more than
resolve their paths and hand them to `nono`. The two sessions are siblings,
not nested — nono refuses to nest a sandbox inside a sandbox, which is
exactly why the broker does not run inside the agent's own session.

Every shell command the agent issues reaches the broker over a unix socket.
The broker is not a shell: it parses the line itself with an embedded
interpreter and calls `execve` directly for each simple command. Handing the
line to `bash -c` instead — the design this project used before — turns out
to be unfixable: a denial written for `git reset --hard` is trivially
defeated by invoking git through its `/nix/store` path instead of by name,
and nono's own documentation says plainly that pointing `exec` at a
general-purpose shell defeats a sandbox. There is no shell in this design to
defeat.

### Two tiers

The command profile sorts every runnable program into one of two tiers:

- **Policy commands** are declared in the profile with their own child
  sandbox — `git`, `ssh`, `bash` and `sh`, in this repository's own profile.
  None of them carries nono's `invocation_policy` argv rules; what bounds
  each is its own filesystem, network and environment grants, scoped per
  caller edge (see [The two profiles](#the-two-profiles) for the worked
  example). The broker dispatches to one of these only through nono's own
  generated shim — never by an absolute path, a symlink, or any other
  indirection that skips it. That guarantee is about how the broker itself
  dispatches; it is not a guarantee about what a *different* command's own
  sandbox can still reach and run — see below.
- **Everything else** runs directly in the broker's own sandbox, at
  whatever that sandbox's filesystem, network and execute grants allow.
  There is no enumerated list of these commands: the broker is not itself a
  policy command, so nothing in the profile bounds which program names it
  may dispatch. Once Tool Sandbox activates, the broker's own execute grant
  is built from the trusted directories on `PATH` — this profile's
  `/nix/store`, `/run/current-system/sw` and mise-installed toolchains among
  them — and that grant set, not a named list, is the boundary for
  everything not in the first tier.

A refusal from a policy command always explains itself: the denial
carries the `reason` its profile entry's `from` edge wrote. A command that
fails at the floor instead fails at plain `execve` permission — `Permission
denied` or "no such file", the same as an unrecognized command would
produce, with no `reason` field to give.

One thing neither tier covers: the broker's own shell builtins (`echo`,
`cd`, `test`, `read`, and the others its embedded interpreter implements)
run inside the broker process itself, at the broker's own filesystem
grants — never through either tier. A redirect or a glob you write is
bounded the same way.

**Nor does either tier bound what a compiler or interpreter does once it
runs.** Nothing here enumerates every program capable of executing code —
only which four commands get their own sandbox. A toolchain that compiles
and executes code (this profile runs `go` at the floor, for instance, not as
a policy command) is bounded only by what *its own* process can reach —
the broker's own sandbox, in `go`'s case — not by argv rules and not by
which other tools are or are not declared elsewhere in the profile. See
[The two profiles](#the-two-profiles) below for what that costs in
practice.

### The filesystem is not virtualized

There is no container and no bind mount. A command runs directly on the host
filesystem at the *same absolute path*, restricted to whatever its own
sandbox (policy command) or the broker's sandbox (floor command) grants.
`HOME` keeps its real value. Nothing needs translating between what the
agent sees and what a command actually touches.

Paths outside a command's own grants are reachable only where its profile
entry says so.

### Neither profile is agent-sandbox's

agent-sandbox generates no nono profile at all. Both — the launched agent's
own, named by `[agents.<name>].profile`, and the command profile every
brokered command runs under — are files the operator writes directly in
nono's schema; agent-sandbox does no more than resolve their paths and hand
them to `nono`. See
[The agent profile](#the-agent-profile-agentsnameprofile) and
[The two profiles](#the-two-profiles).

## Commands

| Command | What it does |
|---|---|
| `agent-sandbox claude -- [claude args...]` | Launch Claude under nono, with the command broker running as a sibling session |
| `agent-sandbox exec -- <command>` | Send one command to the broker and stream its output |
| `agent-sandbox doctor` | Check that `nono` works, that Tool Sandbox can actually start on this host, the broker socket can bind, both profiles exist and validate, every path the command profile pins still exists, the agent profile forwards `AGENT_SANDBOX_BROKER_SOCKET`, and the command profile does not grant write access to the broker's own binary. Exit 0 / 1 |
| `agent-sandbox debug -- [claude args...]` | Print the `nono` invocations for both sessions and the GitHub MCP config (token redacted) — without running anything |
| `agent-sandbox ai explain` | Agent-facing description of the sandbox: how commands run, both tiers, and every denial's reason |
| `agent-sandbox ai config-check` | Validate `agent-sandbox.toml` and both nono profiles the way launch reads them |
| `agent-sandbox command-router` | Start the MCP server (`tool_mode = "mcp"`) |
| `agent-sandbox hook` | PreToolUse adapter (`tool_mode = "hook"`; invoked by Claude, not by you) |

Global flags: `--config <path>` (default `agent-sandbox.toml`) and `--env <ref>`
(repeatable).

For `claude` and `debug`, only `--config` and `--env` may appear before `--`;
everything after `--` goes to `claude`. `agent-sandbox` does not forward
options to `nono` — both profiles come from the config file and the command
profile it points at.

`--settings` is reserved by `agent-sandbox` and rejected as a passthrough
option. `--mcp-config` / `--strict-mcp-config` are rejected too when the GitHub
MCP is enabled.

### `doctor`

`doctor` checks what a launch depends on:

- `nono` is on `PATH` and `nono --version` runs.
- **Tool Sandbox can actually start on this host.** Presence of the `nono`
  binary says nothing about this: nono starts and prints its version
  regardless, and only refuses once `command_policies` actually activates
  Tool Sandbox. doctor runs a real, throwaway `nono run` session under a
  minimal probe profile to answer the question directly — catching, for
  example, an unpatched nono on NixOS that cannot resolve its own ELF
  dependency chain, a failure that would otherwise stay invisible until the
  first real command inside a session fails the same way.
- The command broker can **bind a unix socket** in its socket directory
  (`$XDG_STATE_HOME/agent-sandbox`, or `~/.local/state/agent-sandbox`). A
  plain write check is not enough — binding also catches the ~104-byte
  `sun_path` limit.
- Both nono profiles — the agent profile (`[agents.<name>].profile`) and the
  command profile — exist and `nono profile validate` accepts them.
- The agent profile forwards `AGENT_SANDBOX_BROKER_SOCKET` into the sandbox.
  doctor sets the variable to a sentinel value and starts one throwaway
  `nono wrap --profile <agent profile>` session to confirm the sentinel
  survives. nono cannot be asked directly: `nono profile show` does not
  report `environment.allow_vars`, and `nono why` has no environment-variable
  query, so this is measured rather than read from the file. Without the
  variable the agent can never reach the broker, and every command fails for
  a reason nothing on screen explains. Skipped when doctor is itself running
  inside a session — nono refuses to nest a sandbox inside a sandbox, so the
  probe is only meaningful run from the host.
- The command profile does not grant write access to the broker's own
  binary. doctor asks `nono why --profile <command profile> --path <broker
  binary> --op write` and fails if the answer is allowed.
- **Every path the command profile pins under `command_policies` still
  exists.** doctor asks nono what the profile's `executable_dirs`, each
  policy command's `exec_paths`, and any `executable` resolve to, and
  `stat`s every one. nono handles a missing one differently depending on
  which kind it is, and only one of the two is silent: a missing
  `exec_paths` entry is skipped by design, with its warning suppressed by
  the `--silent` the launcher passes, so a multi-call tool quietly loses a
  helper with no diagnostic naming the profile; a missing `executable` pin
  disables mediation for that command outright — nono falls back to the
  first `PATH` match and runs it at the *session's* own grants, `nono
  profile validate` still passes, and the audit trail records only "tools:
  active, no invocations" (measured on nono 0.74.0). That one is not
  silent — nono prints a warning and `--silent` does not suppress it — but
  the warning is easy to miss in a wall of launch output, and the danger it
  names is real. On NixOS, where every one of these paths carries a store
  hash, a routine package upgrade is enough to trigger either failure mode
  — which is also why
  nothing in this repository's own profile pins an `executable` at all (see
  [The two profiles](#the-two-profiles)).

If any of these fail, `agent-sandbox claude` will not launch Claude at all.

## Configuration

### `tool_mode`

Selects how the agent's commands reach the broker.

| Mode | Behavior |
|---|---|
| `hook` | Bash and Monitor stay enabled. A PreToolUse hook is injected at launch via `claude --settings`, rewriting each command to `agent-sandbox exec -- <command>`. Nothing is written to `.claude/settings.json`. `agent-sandbox` must be on `PATH`. |
| `mcp` (default) | Bash and Monitor are disabled. The agent routes commands through the `run_command` MCP tool, and output is written to files under `mcp.command_output_dir` — the response carries paths and an exit code only. |

```toml
tool_mode = "hook"

[mcp]
command_output_dir = "/tmp/mcp-output"  # required in mcp mode; ignored in hook mode
```

The command profile is read once, when the broker starts — editing it, like
editing `agent-sandbox.toml`, takes effect at the next `agent-sandbox
claude`, never mid-session.

### The agent profile: `[agents.<name>].profile`

The nono profile the launched agent process itself runs under is a file the
operator writes in nono's own schema. agent-sandbox does not generate, read,
or validate its contents — it only resolves the path named by
`[agents.<name>].profile` and hands it to `nono wrap --profile`. The table's
key is the launch subcommand's name: `agent-sandbox claude` reads
`[agents.claude]`.

| written | resolves to |
|---|---|
| omitted | `<name>-profile.json` beside `agent-sandbox.toml` |
| relative path | joined onto the directory holding `agent-sandbox.toml` |
| absolute path | itself |

```toml
[agents.claude]
profile = "claude-profile.json"
```

A missing profile file is a launch error: there is no generated fallback,
matching how the command profile behaves. See
[The two profiles](#the-two-profiles) for what this file must grant, and
[User-scope config](#user-scope-config) for how the key behaves when set in
`~/.config/agent-sandbox/config.toml`.

Four grants do **not** belong in this file, because only the launcher knows
their values and it passes each on the `nono wrap` command line:

| grant | why it is dynamic |
|---|---|
| the `agent-sandbox` binary itself (`--read-file`) | the agent runs `agent-sandbox hook` and `agent-sandbox serve` as its own direct children — inside its own sandbox, not through the broker — and on a mise-managed toolchain the binary's path carries the Go version, so an upgrade renumbers it |
| the main git directory of a worktree (`--allow`) | detected per invocation |
| the generated GitHub MCP config (`--read-file`) | a temp file, new every launch |
| the command broker's socket (`--allow-unix-socket`) | its name is derived from the launcher's PID |

Without the first one nono refuses the execve and every command fails with
nothing on screen to explain it, so the launcher refuses to start at all if it
cannot locate its own binary.

Because it is hand-written, this file can name any path nono's schema
allows — including protected prefixes such as `~/.aws`, `~/.gnupg`,
`~/.config/gh`, and `~/.kube` via `bypass_protection` — where the deleted
capability catalog only ever offered a fixed set of bundles. Review both
profile files in `git diff` like any other code.

### The two profiles

Two files, both written by the operator in nono's own schema, decide all
host access — agent-sandbox generates neither, and hands each only a path:

- **The agent profile** (`[agents.<name>].profile`, see
  [above](#the-agent-profile-agentsnameprofile)) is the sandbox the launched
  agent process itself runs in: its own file tools, and any MCP server it
  spawns as a **direct child** — an MCP server the agent spawns is not
  brokered, so a Python- or Go-based MCP server needs its own runtime granted
  there. Shell commands never run in it.
- **The command profile**, described below, is the sandbox every *brokered*
  command runs in — everything reaching the broker via Bash/Monitor (hook
  mode) or the `run_command` MCP tool (mcp mode).

Nothing declared in one reaches the other. Because the agent process itself
never runs a shell command — hook mode routes Bash/Monitor into the broker
through a PreToolUse hook, and mcp mode disables Bash outright — **nothing
needed only to *run a command* belongs in the agent profile**: a toolchain
grant for `go`, `python`, or any other command-line tool belongs in the
command profile, never here. What is left for the agent profile is genuinely
narrow — this repository's own `claude-profile.json` is a worked example.

`AGENT_SANDBOX_BROKER_SOCKET` must be in the agent profile's
`environment.allow_vars`, or the agent cannot reach the broker at all: every
command then fails with an error that names nothing. `agent-sandbox doctor`
measures this directly (see [`doctor`](#doctor)), because nono cannot be
asked — `nono profile show` does not report `environment.allow_vars`, and
`nono why` has no environment-variable query.

`command-profile.json`, written in nono's own schema, resolved next to
`agent-sandbox.toml` — a top-level `command_profile = "<path>"` in the TOML
overrides the name, and a relative path resolves against the directory
holding the TOML. A missing file is a launch error: there is no generated
default, because a static one cannot absorb host differences (`/nix/store`
versus `/usr/bin`), and a profile that looks present but refuses every
command is the worst failure mode.

`agent-sandbox` reads only enough of it to help `ai explain` describe the
two tiers (see [How it works](#how-it-works)) and to help `doctor` check
that it validates and does not grant write access to its own binary. Every
other decision — which commands exist, what each may touch, which
invocations are refused — is the operator's, expressed directly in nono's
schema.

**`$WORKDIR`.** nono expands it inside `filesystem` and inside every
`command_policies` child sandbox. Write it wherever the working directory is
meant, never a literal path — that is what lets one profile serve multiple
git worktrees.

**Network.** The top-level `network` section is a ceiling. When it sets
`network_profile` or `allow_domain`, nono stands up a loopback proxy and
injects proxy env vars (`http_proxy`/`HTTP_PROXY`/`https_proxy`/
`HTTPS_PROXY`/`no_proxy`/`NO_PROXY`); `block: true` or
`network_profile: null` stands up no proxy at all. At the raw Landlock
level, a command_policies child's own `network` has exactly two effective
permission states, measured: omitting the key blocks it outright (a bare
`network: {}` behaves the same), and either `{"allow_all": true}` or
`{"allow_domain": [...]}` grants the same permission to open sockets at
all. The *domain* restriction `allow_domain` implies is not a Landlock
check — it is enforced entirely by nono's own proxy, which is why it
depends on whether the proxy env vars actually reach that child (below),
not on anything in the grant itself.

Whether a child's traffic is actually bounded the way its own `network`
grant implies depends on nothing in that grant alone — it depends on
whether the proxy env vars reach it. Each hop's own `environment.allow_vars`
filters what it received from its caller, and if a *single* hop in the
chain — including the session's own top-level `environment` section — omits
the proxy vars, they are gone for every hop below it, and the leaf falls
back to a direct, unmediated connection: an `allow_domain` grant without the
proxy vars reaching it is not bounded to that domain at all, and an
`allow_all` grant without them means genuinely unbounded, not "up to the
ceiling".

**`git`'s own network grant is scoped to GitHub, and governs the HTTPS
transport only.** Its child sandbox sets `network:
{"allow_domain": ["github.com", "*.githubusercontent.com"]}`, and the
top-level `network` (the ceiling) allows the same two, so the proxy is
actually standing up in front of it; both the session's own top-level
`environment.allow_vars` and `git`'s own carry the six proxy variable names
(`http_proxy`/`HTTP_PROXY`/`https_proxy`/`HTTPS_PROXY`/`no_proxy`/
`NO_PROXY`), measured sufficient with no `NONO_*` variable needed. Measured
through the real broker: `git ls-remote https://github.com/git/git.git HEAD`
returns a real ref, while `git ls-remote https://example.com/x`, a raw IP,
and a nonexistent domain all fail identically with `CONNECT tunnel failed,
response 403` — a genuine proxy denial, not a DNS or routing failure.
Omitting the proxy vars from either hop's `allow_vars` would silently reopen
this to the whole internet, so check both when changing this profile, not
just the one you touched. SSH remotes never go through this grant at all —
they go out through the separate `ssh` command below.

**`ssh`'s own network grant is `{"allow_all": true}`, and unlike `git`'s
grant, that really is unrestricted.** The SSH protocol cannot be tunnelled
through nono's HTTP(S) proxy, so an `allow_domain` grant there is rejected
outright, and there is no proxy for an `allow_all` grant to be bound
against either. `nono profile validate` warns `[allow_all_network]` on this
entry, and — unlike a warning on a grant that the proxy chain actually
bounds — this one should be taken literally: `ssh` can reach any host. What
actually bounds it is everything else about its entry: it is reachable only
from `git` (`session: "deny"` refuses a direct invocation from the session,
exit 126), and its filesystem grants are narrow — `~/.ssh/id_main`,
`~/.ssh/config` and `~/.ssh/known_hosts` read, `~/.ssh/known_hosts` write,
nothing else. There is no live `ssh-agent` credential to bind on this host
(`SSH_AUTH_SOCK` names a socket that no longer exists), so the identity file
itself is the grant nono's own `credentials` mechanism would otherwise
replace.

**`bash` and `sh` carry no `network` key at all, and that absence is itself
the denial** — not an oversight to "helpfully" fill in with `allow_domain`
or `allow_all`. Both are confined to `$WORKDIR`, the nix store,
`/run/current-system/sw` and the mise-installed toolchains, for both read
and exec, with no network reach of any kind; measured directly, both
`~/.ssh/id_main` and `~/.gitconfig` are denied from inside either sandbox.

**Git hooks do not execute inside `git`'s sandbox.** `git commit` fails
with `fatal: cannot exec '.git/hooks/commit-msg': Permission denied` unless
run as `git commit --no-verify` — and because of that, **commit-message
validation does not run inside this sandbox**: no hook here is going to
catch a malformed message, so get it right before committing (this
repository follows Conventional Commits; see
`.claude/rules/git-commit.md`). Making hooks execute would need `$WORKDIR`
and `/nix/store` added to `git`'s own `exec_paths` — a hook's shebang makes
the kernel exec the interpreter, under `/nix/store`, as part of git's own
`execve()` of the hook file itself. This profile deliberately does not do
that: `$WORKDIR` is agent-writable, and `git`'s sandbox already carries
network access and read access to the global gitconfig, so that widening
would let git execute arbitrary agent-written content with network the
moment it lands in the working tree — a write-then-execute path. It would
also not buy back the validation it costs so much to get: measured
separately, with hooks temporarily enabled, `lefthook` still did not run
and a commit message that violates this repository's own Conventional
Commits rule went through unvalidated. Net effect, with or without hooks
executing: commit-message validation does not run inside this sandbox
either way, so re-opening hook execution is not worth what it costs.

**A worked example** — this repository's own `command-profile.json` at the
repo root — declares four policy commands: `git`, `ssh`, `bash` and `sh`.
Everything else this repository's own workflows use (`go`, `rg`, `mise`,
`gofmt`, the coreutils, …) runs at the floor instead, granted through
`groups.include` and the top-level `filesystem`/`environment` sections, not
through `command_policies`.

None of the four pins an `executable`. A pin whose path does not exist
disables mediation for that command entirely: nono falls back to the first
`PATH` match and runs it at the *session's* own grants, `nono profile
validate` still passes, and the audit trail records only "tools: active, no
invocations" (measured on nono 0.74.0). This is not silent — nono prints a
warning and `--silent` does not suppress it — but the warning is easy to
miss in a wall of launch output, and the danger is real. `git`'s real
binary is a `/nix/store` path that changes on every nixpkgs update, so
pinning it here would un-sandbox `git` on the next upgrade — which is also
why `doctor`'s "profile paths" check (see [`doctor`](#doctor)) exists, verifying
instead the paths this profile *does* pin (`executable_dirs`, and each
command's own `exec_paths`).

- **`git`** is reachable from the session and runs as the real binary
  directly — there is no wrapper and no second name for it. Its child
  sandbox grants read access to `$WORKDIR`, the object store
  (`@git:common-dir`, `@git:worktree`, `@git:toplevel`, `@git:hooks-path`)
  and the global/system git config (`@git:config-files`, which nono
  resolves to the declared target of every `include.path`/
  `includeIf.*.path` in those scopes only — `local` and `worktree` scopes
  are excluded, so a repository cannot grant itself host access through its
  own `.git/config`), plus `libexec/git-core` under `exec_paths` — needed
  only for the HTTPS transport's separate `git-remote-https` binary; nono
  skips a missing `exec_paths` entry silently, so a nixpkgs git update that
  moves this path would break HTTPS transport with no diagnostic naming the
  profile, which is exactly what `doctor`'s "profile paths" check catches.
  `git`'s own `can_use: ["ssh"]` is the only reason `ssh` is reachable at
  all — see [Network](#the-two-profiles) above for both commands' network
  grants and the git-hooks trade-off.
- **`ssh`** is reachable only from `git`; a direct session invocation is
  refused (`session: "deny"`, exit 126).
- **`bash` and `sh`** are reachable only from the session, deliberately not
  from `git` — `git` cannot execute a hook at all (above), so a `git`-caller
  edge for either would be dead weight.

None of the four carries an `invocation_policy` argv rule of any kind. An
earlier revision of this project enforced git-specific rules — blocking an
unconditional `push --force`, `reset --hard`, and similar — through a
Go-based wrapper that parsed each invocation before deciding whether to
exec the real binary. That wrapper (`internal/safe/git`, invoked via the
`agent-sandbox` binary's own `safe` subcommand) still exists, is still
built, and is still tested, but this profile no longer wires it in: `git`'s
entry above execs
the real binary directly, under no argv-level policy at all. **What bounds
`git` in this profile is exclusively what its filesystem, network and
`can_use` grants reach — not which git subcommand or flag it was given.** An
operator who wants those argv-level rules back would need to wire the
wrapper in explicitly (pin `git`'s `executable` to the `agent-sandbox`
binary, add `argv_prepend: ["safe", "git"]`, and give the real binary a
second name reachable only from the wrapper) — a capability decision this
repository's own profile does not make.

None of these four edges is written with nono's `sandbox` shorthand
(`"from": {"session": "sandbox"}` rather than the longer
`{"session": {"sandbox": {...}}}`), even though nono runs both forms
identically. `nono why --command` does not implement the shorthand: on this
profile it answers DENIED, citing a missing `from.session`, for a command
that demonstrably runs (measured, nono 0.74.0) — and `agent-sandbox ai
explain` tells the agent to run exactly that query, so an edge written with
the shorthand would hand the agent a false denial with no way to see
through it. Every edge in this profile is written the long way for exactly
this reason.

**`docker` is not declared in this repository's profile at all — no
policy-command entry names it.** A `docker` wrapper exists
(`internal/safe/dockercompose` and `cmd/safe_docker.go`: `docker` →
wrapper → `realdocker` → the real binary — the same argv-parsing pattern
`git`'s own entry used before this profile moved `git` to the direct
sandbox described above) and is fully built and tested, but wiring it into
a command profile is a capability decision an operator makes deliberately,
not something to enable by copying this repository's profile. The reason is
the Docker socket, and it is not what it looks like:

> **The Docker socket is not filesystem-gated.** nono does not mediate
> pathname AF_UNIX sockets — only the Linux *abstract* socket namespace —
> so `/var/run/docker.sock` is reachable by any command that can execute
> the `docker` binary, regardless of what `fs_read`/`fs_write` grant it
> does or does not have. Leaving `docker` undeclared does not stop the
> broker from running it: an undeclared command gets no child sandbox, no
> `can_use` edges and no argv gate at all — it simply runs at the broker's
> own grants, like any other floor command, reachable the moment it sits in
> a trusted `PATH` directory (see [Two tiers](#two-tiers)). Measured
> directly against this repository's own profile: `docker --version` runs
> — the broker dispatches it — and exits 255 only because the nix
> `docker` package's own `bin/docker` stub fails its re-exec into
> `libexec`, not because of any tool-sandbox denial. The socket is already
> reachable from this profile's floor, whether or not `docker` is ever
> mentioned in it; declaring `docker` (with the wrapper below) adds an
> argv-level check that does not otherwise exist — it does not add a
> filesystem boundary that otherwise would exist, because there isn't one
> to add. Once declared, the wrapper's checks (below) are the *entire*
> defense, not a second layer behind a filesystem bound, because reaching
> the daemon at all is root-equivalent: the socket permits mounting `/`
> into a container. Gating the socket itself needs
> `linux.af_unix_mediation` plus a `filesystem.unix_socket` allowlist — a
> separate opt-in nono's profile guide documents under its `no-docker`
> example — which this repository's profile does not configure, because
> this repository's profile does not declare `docker` at all.

An operator who decides the wrapper's checks are sufficient can wire it in
with this shape:

```json
"docker": {
  "executable": "<agent-sandbox binary>",
  "can_use": ["realdocker"],
  "from": { "agent-sandbox": { "sandbox": {
    "argv_prepend": ["safe", "docker"],
    "...": "..."
  } } }
},
"realdocker": {
  "executable": "/nix/store/…-docker-…/libexec/docker/docker",
  "from": { "docker": { "sandbox": { "...": "..." } } }
}
```

Three things worth knowing before doing that. First, the `executable` above
is deliberately `libexec/docker/docker`, not the more obvious
`bin/docker`: on NixOS, `bin/docker` is a small stub that re-execs
`libexec/docker/docker` by absolute path, and nono's per-command Landlock
rule set (built from the pinned executable's own direct library
dependencies) does not cover that second, indirectly invoked path —
pinning the stub crashes every invocation, silently (`execve(...) = -1
EACCES`, reported only as "Command exited with code 255"). Second, the
wrapper's checks (`internal/safe/dockercompose` and `cmd/safe_docker.go`;
read the source for the exact, current rule set — `--help` passes straight
through to real docker and prints docker's own help, never the wrapper's
rule set: the plain `docker` path never intercepts it because the wrapper
disables its own flag parsing, and the `compose` path skips model
resolution outright once it sees `--help`, since help executes nothing and
needs no model) are argv/model-level,
not filesystem-level: a `compose` invocation is checked against its
*resolved* model (`docker compose config`) — host-path mounts, the Docker
socket, `privileged`, host `network`/`pid`/`ipc`, dangerous capabilities,
disabled seccomp/apparmor — and every other invocation is checked at the
argv level for `run`/`exec`, `--privileged`, and a host-path or
Docker-socket bind mount.

Third, and this is the one that matters most given the socket fact above:
the wrapper's checks have two known gaps, both accepted only because
docker was otherwise unreachable — a premise this opt-in block removes the
moment it is pasted in.
- `docker create` followed by `docker start` reaches the same running
  state as `docker run` with none of the dangerous flags present on either
  individual invocation — `create` is not itself refused (nothing about
  creating a container without starting it is dangerous on its own), so
  this is a structural gap across two calls, not a parsing defect the
  argv check could close in one.
- `--mount type=volume,volume-opt=device=...,volume-opt=o=bind` is a bind
  mount in substance (a `local`-driver volume with `o=bind` behaves as a
  bind mount of `device`'s path) that the mount check does not catch,
  since it keys on `type=bind` specifically and this spec's `type` is
  `volume`.

Neither is closed by anything in this repository. An operator enabling
docker is accepting both until someone closes them.

Three facts this design turns on, each measured on nono 0.74.0:

- **`executable_dirs` is required once Tool Sandbox activates, or the Go
  toolchain cannot exec its own tools.** Activating Tool Sandbox at all
  rebuilds the broker's own execute grant from the trusted directories on
  `PATH`, and the Go toolchain's internal tools (`compile`, `link`, …) do
  not live on `PATH` — without `command_policies.executable_dirs` naming
  that mise-managed directory directly, `go build ./...` fails with
  `fork/exec .../pkg/tool/linux_amd64/compile: permission denied`. Neither
  `~` nor `$HOME` is expanded there, so the path is written literal, and it
  is pinned to a minor Go version (`.mise.toml` pins `go = "1.25"`): a patch
  upgrade does not break it, a minor bump does, loudly.
- **A missing `executable` pin disables mediation for that command
  entirely, which is why nothing in this profile pins one** (see the
  worked example above). It is not silent — nono prints a warning and
  `--silent` does not suppress it — but the warning is easy to miss in a
  wall of launch output, and the danger is real. `agent-sandbox doctor`'s
  "profile paths" check exists to catch the paths that *are* pinned
  (`exec_paths`, `executable_dirs`) going stale, not to catch a pin that
  was never made.
- **`nono why --command` does not implement the `sandbox` shorthand, which
  is why every edge in this profile is written `from.session`** rather than
  the shorter form (see the worked example above) — the shorthand answers a
  false DENIED for a command that demonstrably runs, and `ai explain` sends
  the agent to ask exactly that question.

Two more properties worth knowing:

- **A toolchain that compiles and runs code is bounded only by its own
  sandbox's reach, not by which other commands are declared.** `go` is not
  one of this profile's four policy commands — `go build`, `go test` and
  `go run` all run directly in the broker's own sandbox, the same one every
  other floor command shares. Whatever that sandbox's filesystem and
  network grants reach, a program compiled and immediately executed there
  can reach too — in principle including a copy of a program this profile
  does sandbox elsewhere, made and exec'd through `/tmp` or `$WORKDIR`. The
  two-tier model was never positioned to close this: it decides what the
  broker itself dispatches by name, and a binary a compiled program execs
  directly is not something the broker dispatches at all. If you enumerate
  a compiler or interpreter's own toolchain in a profile you write, treat
  its own sandbox's reach as the honest boundary, not the two-tier model's
  absoluteness.
- **`nono profile validate` checks JSON syntax and group references only**
  — it does not catch every schema mistake (`exec_paths` itself is not in
  the published JSON Schema, though the runtime honours it). Verify a real
  workflow against a real `nono run` session, not just a passing
  `validate`.

### User-scope config

An optional `~/.config/agent-sandbox/config.toml` is composed with the
project config: every field is a scalar or a map of scalars, and **the
project file wins for anything it sets** — an omitted key falls back to the
user-scope value.

`[agents.<name>]` tables merge by key, not by union: an agent declared only
in the user-scope file (say `[agents.codex]`) still applies even when the
project file declares only `[agents.claude]`. A table present in *both*
files is replaced wholesale by the project one rather than merged field by
field — today that means a project `[agents.claude]` table always fully
determines that agent's profile path, since `AgentConfig` has a single field.

This is the same scalar-override behavior `command_profile` already has:
both it and every `[agents.<name>].profile` are project-overrides-user
values, so "every project has a `claude-profile.json` beside its config" is
declarable once, in the user-scope file, and a project that needs a
different path just sets it.

## Environment variables (`--env`)

`--env` loads variables from a file into the launcher's own process before
running Claude or a command. It is repeatable and uses a scheme-based
reference; only `file:` exists today.

```bash
agent-sandbox claude --env file:.env -- --model opus
agent-sandbox exec --env file:.env -- go test ./...
```

The format is a minimal dotenv subset: `KEY=VALUE` per line, `#` comments and
blank lines ignored, an optional `export ` prefix stripped, surrounding quotes
removed. There is **no variable interpolation**. Values **override** any
same-named host variable; with multiple files, later files win.

**`--env` no longer grants anything.** Loading a variable into the
launcher's own process is not the same as the sandboxed agent seeing it:
nono forwards only what a profile's `environment.allow_vars` lists,
hand-written by the operator. A variable loaded by `--env` reaches the
launched agent only if the agent profile's `environment.allow_vars` names it
— a glob such as `MISE*` covers a family in one line. Exposing the same
variable to a brokered command is a separate, explicit edit to the command
profile's `environment.allow_vars` — with one exception:
`AGENT_SANDBOX_BROKER_SOCKET` must **never** appear in the command profile's
`environment.allow_vars`, under any name or wildcard that would match it. A
command that can reach the broker socket can recurse into the broker, which
spawns handlers with no concurrency cap — a host-side fork bomb. A value
silently not reaching the agent is exactly the failure this paragraph exists
to pre-empt.

## GitHub MCP

The built-in GitHub MCP server is enabled when `GITHUB_MCP_TOKEN` is non-empty;
otherwise it is not configured at all. Its value is passed to the MCP server as
`GITHUB_PERSONAL_ACCESS_TOKEN`.

```bash
agent-sandbox claude --env file:.secrets.env -- --model opus
# .secrets.env: GITHUB_MCP_TOKEN=ghp_...
```

`agent-sandbox debug` prints the resulting MCP config with the token redacted.

## Development

```bash
mise install          # Go + lefthook
go test ./...         # unit + integration tests
go build ./...
mise run build         # install a working-tree build via `go install`
```

**Building this project's own binary no longer puts it on `PATH`.**
`agent-sandbox claude` resolves its own broker entrypoint by base name
through the launcher's PATH (see [the two profiles](#the-two-profiles)), and
a build that landed in this working tree would sit inside the same directory
the command profile grants `fs_write` — the writable-and-executable
combination nono refuses an entrypoint binary for. `mise run build` runs
`go install` instead, which installs to `$(go env GOBIN)` when it is set and
to `$(go env GOPATH)/bin` otherwise — outside `$WORKDIR` either way, and
already on the developer's `PATH` (this repository's own mise-managed Go sets
`GOBIN` to its own version-scoped `bin/`). Run `mise run build` after every
change you want to exercise, then launch as usual (`agent-sandbox claude`).
`go run .` cannot stand in for this: its output binary is staged under
`$TMPDIR` at run time, on no reliable footing with the profile at all.

End-to-end suites live in `e2e` (Python/pytest, MCP stdio).

Commits follow [Conventional Commits](https://www.conventionalcommits.org/);
`lefthook` validates the title on `commit-msg`.

## License

[MIT](LICENSE) © Yuya Nagai
