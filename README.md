# agent-sandbox

**English** | [日本語](README.ja.md)

Run an AI coding agent (Claude Code) inside a
[nono](https://github.com/tkancf/nono) sandbox, and mediate every shell
command it issues through a host-side command broker that runs in its own
sibling nono session. Which commands may run, what each may touch, and which
invocations are refused is decided once, by a nono command profile the
operator writes — not by `agent-sandbox.toml`.

The point is not to lock the agent out of your machine. It is to make the
boundary *explicit and inspectable*: `agent-sandbox ai explain` tells the
agent which commands are policy-controlled, which run at the floor with no
argv rules, and why any refusal fired, so a policy denial reads as a policy
denial rather than an unexplained failure worth retrying.

```
launcher
├── nono wrap  --profile <agent profile>    -- claude …         no command control here
└── nono run   --profile <command profile>  -- agent-sandbox broker
                                               │
                                               ├─ exec git   → shim → its own child sandbox + argv rules
                                               └─ exec rg    → runs directly in the broker's own sandbox
```

The two sessions are siblings, never nested. There is no shell in the loop:
the broker parses the agent's command line itself (pipelines, `&&`/`||`/`;`,
redirections, globbing, `$(…)`, `for`/`if`, `cd` and the other shell builtins
all work) and execs each simple command directly. `bash` and `sh` are not
runnable in this sandbox at all.

## Contents

- [Requirements](#requirements)
- [Install](#install)
- [Quick start](#quick-start)
- [How it works](#how-it-works)
- [Commands](#commands)
- [Configuration](#configuration)
  - [`tool_mode`](#tool_mode)
  - [Host access: `[sandbox.agent]`](#host-access-sandboxagent)
  - [Capabilities](#capabilities)
  - [The command profile](#the-command-profile)
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

Write `agent-sandbox.toml` in your project root — this covers the launched
agent's own host access only:

```toml
tool_mode = "hook"

[sandbox.agent]
capabilities = ["go", "ssh"]   # host access for the agent process itself
```

Then write `command-profile.json` beside it, in nono's own schema, declaring
which commands the broker may run and how (see
[The command profile](#the-command-profile)). There is no default: a missing
file is a launch error, and agent-sandbox never generates or reads its
contents beyond a path.

Check that both resolve, then launch:

```bash
agent-sandbox doctor            # nono, the broker socket, and the command profile all usable?
agent-sandbox ai config-check   # does agent-sandbox.toml resolve, and what does it grant the agent?
agent-sandbox claude -- --model opus
```

There is no `sandbox up` step. `agent-sandbox claude` starts the host-side
command broker inside its own nono session, launches Claude under a second,
sibling session, and tears the broker down when Claude exits.

## How it works

### The broker is a sandboxed process, not a router

`agent-sandbox claude` starts two sibling nono sessions: one wraps Claude
Code under the profile agent-sandbox generates from `[sandbox.agent]`; the
other runs `agent-sandbox broker` under the *operator-written* command
profile. They are siblings, not nested — nono refuses to nest a sandbox
inside a sandbox, which is exactly why the broker does not run inside the
agent's own session.

Every shell command the agent issues reaches the broker over a unix socket.
The broker is not a shell: it parses the line itself with an embedded
interpreter and calls `execve` directly for each simple command. Handing the
line to `bash -c` instead — the design this project used before — turns out
to be unfixable: a denial written for `git reset --hard` is trivially
defeated by invoking git through its `/nix/store` path instead of by name,
and nono's own documentation says plainly that pointing `exec` at a
general-purpose shell defeats a sandbox. There is no shell in this design to
defeat.

### Two tiers, and nothing else runs

The command profile sorts every runnable program into one of two tiers. A
program named in neither cannot execute at all — that is the allowlist, and
there is nothing else to check:

- **Policy commands** are declared in the profile with their own child
  sandbox and an `invocation_policy` of argv rules. They are reachable only
  through nono's own generated shim, so a rule cannot be evaded by an
  absolute path, a symlink, or any other indirection.
- **Floor commands** are named in the broker's own `exec_paths` and run
  directly in the broker's sandbox, with no argv rules of their own — there
  is no shim to bypass because there is nothing being enforced.

A refusal always explains itself: a policy command's denial carries the
`reason` its profile entry wrote; a command absent from both tiers is
refused at `execve`, the same as an unrecognized command would be, with no
further detail to give.

One thing the two tiers do not cover: the broker's own shell builtins
(`echo`, `cd`, `test`, `read`, and the others its embedded interpreter
implements) run inside the broker process itself, at the broker's own
filesystem grants — never through either tier. A redirect or a glob you
write is bounded the same way.

**Nor do the two tiers bound what a compiler or interpreter does once it
runs.** The allowlist above is absolute about *which programs exist to run
at all* — nothing bypasses that. It says nothing about what a command that
is itself a compiler or interpreter can do with code you hand it: that
command is bounded only by what *its own* sandbox can reach, not by argv
rules and not by which other tools are or are not enumerated elsewhere in
the profile. This repository's own profile enumerates `go` for exactly this
reason — see [The command profile](#the-command-profile) below for what
that costs and how far the containment actually reaches once you look
closely at what a Go program can do from inside `go`'s own grants.

### The filesystem is not virtualized

There is no container and no bind mount. A command runs directly on the host
filesystem at the *same absolute path*, restricted to whatever its own
sandbox (policy command) or the broker's sandbox (floor command) grants.
`HOME` keeps its real value. Nothing needs translating between what the
agent sees and what a command actually touches.

Paths outside a command's own grants are reachable only where its profile
entry says so.

### The command profile is the operator's, not agent-sandbox's

agent-sandbox generates exactly one nono profile: the launched agent's own,
from `[sandbox.agent]`. Everything about *commands* is expressed separately,
directly in nono's schema, in a file agent-sandbox neither generates nor
inspects beyond its path. See [The command profile](#the-command-profile).

## Commands

| Command | What it does |
|---|---|
| `agent-sandbox claude -- [claude args...]` | Launch Claude under nono, with the command broker running as a sibling session |
| `agent-sandbox exec -- <command>` | Send one command to the broker and stream its output |
| `agent-sandbox doctor` | Check that `nono` works, the broker socket can bind, and the command profile exists, validates, does not grant write access to the broker's own binary, and resolves its own name back to itself. Exit 0 / 1 |
| `agent-sandbox debug -- [claude args...]` | Print the `nono` invocations for both sessions, the generated agent profile, and the GitHub MCP config (token redacted) — without running anything |
| `agent-sandbox ai explain` | Agent-facing description of the sandbox: how commands run, both tiers, and every denial's reason |
| `agent-sandbox ai config-check` | Validate `agent-sandbox.toml` the way launch reads it, and print what the launched agent's own profile grants |
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
- `nono` can actually start tool-sandbox on this host — a real, short-lived
  probe session, not just a version check. An unpatched nono on NixOS cannot
  start tool-sandbox at all, so presence of the binary says nothing about
  this.
- The command broker can **bind a unix socket** in its socket directory
  (`$XDG_STATE_HOME/agent-sandbox`, or `~/.local/state/agent-sandbox`). A
  plain write check is not enough — binding also catches the ~104-byte
  `sun_path` limit.
- The command profile exists, `nono profile validate` accepts it, and it
  does not grant write access to the broker's own binary — checking both the
  top-level `filesystem.allow` and every `command_policies` command's own
  `fs_write` (both Critical findings a review of this profile found lived
  in a command's own `fs_write`, not the top-level list). A grant expressed
  purely through `$WORKDIR` is still not something this check can see —
  nono itself is the final word on that at launch.
- Resolving the broker's own base name through this process's own `PATH` —
  the same lookup the launcher's `BrokerArgs` relies on — lands back on this
  exact binary. A different `agent-sandbox` earlier on `PATH` would silently
  become the broker instead.

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

### Host access: `[sandbox.agent]`

`[sandbox.agent]` is the only section `agent-sandbox` reads for host access,
and it is the only nono profile agent-sandbox generates: the launched
agent's own process (its file tools, and any MCP server it spawns as a
direct child — those are not brokered). The sandbox a shell *command* runs
in is a completely separate concern, decided entirely by the command
profile below; nothing declared here reaches a brokered command, and nothing
in the command profile reaches the agent.

| Field | Grants |
|---|---|
| `capabilities` | Named bundles — see below |
| `allow` | Directories, read + write |
| `read` | Directories, read-only |
| `allow_file` | Single files, read + write |
| `read_file` | Single files, read-only |
| `allow_env` | Environment variable names |

`PATH`, `HOME`, `TERM`, `LANG`, `LC_ALL`, `USER` and `/dev/null` are always
granted from a built-in baseline, as are nono's `nix_runtime` and `git_config`
groups.

`nix_runtime` is baseline rather than a capability because on a NixOS host it is
not a toolchain but the precondition for running anything: every executable
lives under `/nix/store`, nono's base profile grants that tree read but not
execute, and the agent's own process would exit 127 with no output to explain
itself. On a host without Nix its paths simply do not exist.

`git_config` is baseline because toolchains shell out to git without saying so —
flutter's launcher reads the SDK revision that way and exits 128 without it, `go
build` stamps a version, npm and cargo resolve git dependencies. The group is
configuration only; `~/.git-credentials` is not in it, so it hands out no
credential.

`NONO_*` is rejected in `allow_env` — those variables would reconfigure the
nono session the command broker runs in, not just the agent's own.

A raw `allow` / `read` targeting a protected prefix (`~/.ssh`, `~/.aws`,
`~/.docker`, `~/.gnupg`, `~/.config/gh`, `~/.kube`) is rejected; use the
matching capability instead.

### Capabilities

Named bundles that expand into directories, files, env vars, and — for
credential bundles — the matching Claude permission denies. They apply only
to the agent's own profile: a capability declared here reaches the launched
agent's process, never a brokered command. Give a command network or
filesystem access explicitly, in the command profile, on its own entry.

| Capability | Grants |
|---|---|
| `go` | Go runtime group, plus `GOCACHE` and `~/go/pkg/mod` (read+write) |
| `python` | Python runtime group, plus the uv and pip caches and `~/.local/share/uv/python` (read+write) |
| `node` | Node runtime group, plus `~/.npm` and the pnpm store (read+write) |
| `rust` | Rust runtime group, plus `~/.cargo/registry` and `~/.cargo/git` (read+write); `~/.cargo/credentials*` hidden from Claude's file tools |
| `dart` | `~/.pub-cache`, `~/.dart`, `~/.dart-tool` (read+write), `PUB_CACHE` / `PUB_HOSTED_URL` env; `~/.dart-tool/pub-tokens.json` hidden from Claude's file tools |
| `flutter` | `~/.config/flutter`, `~/.local/share/mise/http-tarballs` (read+write), `FLUTTER_ROOT` / `FLUTTER_STORAGE_BASE_URL` env |
| `docker` | `~/.docker`, `~/.orbstack` (read-only) |
| `ssh` | `~/.ssh` (read-only), `~/.ssh/known_hosts` (read+write) |
| `mise` | `~/.local/share/mise`, `~/.config/mise` (read-only), `MISE*` env |
| `bashrc` | `~/.bashrc`, `/etc/bashrc`, `/etc/bash.bashrc` (read-only) |

Every nono runtime group is read-only, so on its own none of the four could
build anything — the toolchain fails on its own cache before it reaches a
package. Each bundle therefore adds what its tool writes during an ordinary
build and leaves the read surface to the group, which curates the version
manager layouts for us. A cache whose location differs by platform is resolved
when the profile is generated, since that always happens on the machine that
will run under it.

None of them grants the directory that puts an executable on the host's PATH —
`~/go/bin`, `~/.cargo/bin`, uv's tools directory. `go install` and its siblings
stay a deliberate raw `allow`.

A group cannot be narrowed. `groups.exclude` removes one whole, never a path
inside it, and a profile-level denial under a granted parent is worse than
useless: Landlock has no deny-overlap on Linux, so nono refuses to start at all
rather than pretend to enforce it. `rust` runs into this — `rust_runtime` grants
all of `~/.cargo`, crates.io publish token included — and the only levers left
are the two that don't need carve-outs:

- **Claude's own file tools** are denied `Read(//…/.cargo/credentials*)`, the way
  `docker` and `ssh` deny theirs. This binds a tool call and nothing else: a
  `cat` in a sandboxed command still reads the file.
- **Whether you declare the capability at all** decides whether a brokered
  command could ever reach it — capabilities never apply to commands, so this
  is really just "does the launched agent need `rust`", nothing more.

Deny rule paths are absolute, `Read(//etc/bashrc)` rather than `Read(/etc/bashrc)`:
Claude Code reads the path as a gitignore pattern where a single leading slash
anchors at the settings source, so the one-slash form silently matches nothing.
The catalog names paths and the prefix is added once, so no bundle can get it
wrong.

`flutter` is the Dart *delta*, not a superset: a Flutter project declares
`["dart", "flutter"]`. It has to make the SDK writable, because flutter
populates its own `bin/cache` on first run. Under `mise` that means the tarball
tree it unpacks tools into — a write grant over *every* mise-installed binary,
which is why it rides on `flutter` rather than on `mise`, so only the projects
that need it open it. A git checkout of the SDK is not covered at all: no fixed
path is right for everyone, so wherever it lives needs a raw `allow`.

`dart` grants `~/.dart-tool` even though pub keeps its private-repository
credentials there, because dartdev's analytics writes to it on startup and dart
does not run without it. The token is denied to Claude's own file tools instead
— the same arrangement `rust` uses, for the same reason: a carve-out inside a
granted directory is not enforceable.

`flutter doctor` does not work in the sandbox. It lists `$HOME` looking for
Android and browser toolchains, and granting that would let the agent's own
process enumerate the home directory. `flutter --version`, `pub get` and builds
are unaffected.

> **`docker` and `ssh` expose host credentials.** They are otherwise ordinary
> capabilities. Since `[sandbox.agent]` is the only section left, declaring
> either one here is what puts the credential in reach of the agent's own
> process — never of a brokered command, which would need the same access
> granted separately, deliberately, in the command profile.

### The command profile

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

**Network.** The top-level `network` section is the ceiling and the only
place domain filtering works. A command's own `network` is on/off:
`{"allow_all": true}` lets it reach whatever the ceiling allows, and
omitting the key gives it no network at all. A per-command `allow_domain` is
not enforced — narrowing what a command reaches requires narrowing the
top-level ceiling instead.

**A worked example** — this repository's own `command-profile.json` at the
repo root — declares `agent-sandbox` itself as the session's policy command
(`can_use: ["git", "go"]`, `exec_paths` covering `rg`, `mise`, `gofmt` (as a
single file, not its whole directory — see the note on multi-call binaries
below), and the coreutils this repo's own workflows use). `git` and `go` are
its two policy commands, for different reasons: `git` carries argv rules;
`go` carries none — a compiler is not something argv-level rules can usefully
bound — but still needs its own child sandbox, because `go test` compiles
and immediately executes a test binary, and that write-then-execute
directory has to stay out of reach of every other command. More on what a
compiler being enumerable at all actually costs below.

```json
"git": {
  "executable": "/nix/store/…-git-2.54.0/bin/git",
  "from": { "agent-sandbox": {
    "sandbox": { "...": "..." },
    "invocation_policy": {
      "default": "allow",
      "deny": [
        { "argv": { "contains": ["--force"] },
          "reason": "force push is disabled in this sandbox; use --force-with-lease..." },
        { "argv": { "contains": ["--hard"] },
          "reason": "hard reset is disabled in this sandbox; it discards uncommitted work." }
      ]
    }
  }}
}
```

`reason` reaches the agent verbatim on stderr, at exit code 126 — see
`agent-sandbox ai explain` for the full, current list.

Four properties worth knowing before writing your own:

- **A toolchain that compiles and runs code is bounded only by its own
  sandbox, not by which other commands are enumerated.** This repository's
  own `go` entry has no `invocation_policy` at all — a compiler is not
  something argv-level rules can usefully bound — and its own child sandbox
  originally granted `/nix/store` read access, the same NixOS execute path
  every other command needs. That combination is a real, measured bypass:
  `go run` on a program that copies `git`'s (or `bash`'s) real binary into
  `/tmp` and `exec`s it directly reaches the real binary, unmediated by any
  shim or `invocation_policy`, exactly as if the copy-then-exec had been
  attempted at the floor. Fixed here by removing `/nix/store` and
  `/run/current-system/sw` from `go`'s own `fs_read`: the toolchain (built
  with `CGO_ENABLED=0`, confirmed with `ldd` reporting "not a dynamic
  executable") and everything it compiles here need neither for their own
  linking, so the fix costs nothing `go test`/`go build` need — but it does
  not make the underlying risk disappear. A dynamically linked binary staged
  through `$WORKDIR` instead (readable to both the floor and to `go`) and
  then copied to `/tmp` and exec'd now fails at the shared-library-loading
  step, since `/tmp` is not `/nix/store` and the copy carries no working
  runtime with it — measured directly, both for `git` and for `bash`. A
  sufficiently deliberate attack that stages an entire dependency closure
  (a copy of the dynamic linker plus every `.so` it needs) into `$WORKDIR`
  and invokes the copied linker directly was not attempted and is not
  claimed to be closed. If you enumerate a compiler or interpreter in your
  own profile, treat this as the honest boundary: its own sandbox's reach,
  not the two-tier model's absoluteness, is what actually bounds it.
- **The installed binary's directory must be on the launcher's own `PATH`.**
  The launcher invokes `agent-sandbox broker` by base name, never by an
  absolute path: nono treats an absolute-path invocation of a declared
  policy command as a direct exec bypass and refuses it. That name then
  resolves the same way an ordinary shell would — through the *launcher
  process's own* `PATH`, before any sandbox exists — not through
  `command_policies.executable_dirs`, which measurably plays no part in
  resolving the session entrypoint at all. This also means PATH resolution
  finds whichever `agent-sandbox` comes first on it, not necessarily the one
  you meant: a stale copy or an unrelated program sharing the name, earlier
  on that PATH, would silently become the broker instead. `agent-sandbox
  doctor` checks that resolving the entrypoint through this process's own
  PATH lands back on this exact binary.
- **Enumerating every runnable command is the real cost of this design.** A
  program absent from both tiers cannot run at all, which is the allowlist
  working as intended — and also the profile's recurring maintenance
  burden. On a NixOS host, coreutils applets (`cat`, `ls`, `rm`, `cp`, …) are
  symlinks into one combined multi-call binary: pinning each as its own
  policy command silently disables enforcement, so they belong at the floor,
  granted as one directory. A pinned `executable` must be the real program,
  never a multi-call host or a version-manager shim — pointing an entry at
  `mise` turns every mise-managed tool into an attempted direct exec of
  `mise` itself.
- **`nono profile validate` checks JSON syntax and group references only** —
  it does not catch every schema mistake (`exec_paths` itself is not in the
  published JSON Schema, though the runtime honours it). Verify a real
  workflow against a real `nono run` session, not just a passing `validate`.

### User-scope config

An optional `~/.config/agent-sandbox/config.toml` is composed with the project
config:

- **Scalars**: the project file wins.
- **Lists**: a de-duplicated union of both, user entries first.

So a project file can *add* to a list but cannot *remove* what the user-scope
file contributes. If a grant you did not write shows up in
`agent-sandbox ai config-check`, that is where it comes from. This affects only
`[sandbox.agent]` — the command profile has no user-scope counterpart.

## Environment variables (`--env`)

`--env` loads variables from a file into the process before launching Claude or
running a command. It is repeatable and uses a scheme-based reference; only
`file:` exists today.

```bash
agent-sandbox claude --env file:.env -- --model opus
agent-sandbox exec --env file:.env -- go test ./...
```

The format is a minimal dotenv subset: `KEY=VALUE` per line, `#` comments and
blank lines ignored, an optional `export ` prefix stripped, surrounding quotes
removed. There is **no variable interpolation**. Values **override** any
same-named host variable; with multiple files, later files win.

For `agent-sandbox claude`, the loaded keys are appended to
`[sandbox.agent].allow_env` — so `--env` grants the launched agent alone.
Exposing a variable to a brokered command is a separate, explicit edit to the
command profile's `environment.allow_vars`.

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
```

End-to-end suites live in `tests/e2e` (Go/Ginkgo) and `e2e` (Python/pytest,
MCP stdio).

Commits follow [Conventional Commits](https://www.conventionalcommits.org/);
`lefthook` validates the title on `commit-msg`.

## License

[MIT](LICENSE) © Yuya Nagai
