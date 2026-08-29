# agent-sandbox

**English** | [日本語](README.ja.md)

Run an AI coding agent (Claude Code) under a sandbox, and route every shell
command it issues to one of three destinations — the **host**, a **per-command
[nono](https://github.com/tkancf/nono) sandbox**, or **nowhere at all** —
according to a policy you write in TOML.

The point is not to lock the agent out of your machine. It is to make the
boundary *explicit and inspectable*: `agent-sandbox ai config-check` prints
exactly which paths a sandboxed command can reach and which domains it can talk
to, resolved from the config that will actually be used at launch.

```
                       agent-sandbox.toml
                              │
          ┌───────────────────┼───────────────────┐
          ▼                   ▼                   ▼
    allow_commands      drop_commands       (everything else)
          │                   │                   │
          ▼                   ▼                   ▼
   ┌─────────────┐     ┌─────────────┐     ┌──────────────────┐
   │    HOST     │     │   REFUSED   │     │  nono run        │
   │  (direct)   │     │  (exit 1)   │     │  scoped to CWD   │
   └─────────────┘     └─────────────┘     └──────────────────┘
```

## Contents

- [Requirements](#requirements)
- [Install](#install)
- [Quick start](#quick-start)
- [How it works](#how-it-works)
- [Commands](#commands)
- [Configuration](#configuration)
  - [`tool_mode`](#tool_mode)
  - [Host access: the three sections](#host-access-the-three-sections)
  - [Capabilities](#capabilities)
  - [Command routing](#command-routing)
  - [Network](#network)
  - [User-scope config](#user-scope-config)
- [Environment variables (`--env`)](#environment-variables---env)
- [GitHub MCP](#github-mcp)
- [Safe wrappers](#safe-wrappers)
- [Development](#development)
- [License](#license)

## Requirements

- [nono](https://github.com/tkancf/nono) on `PATH` — the sandbox engine.
- Go 1.25 or later (to build from source).
- `claude` on `PATH` — for `agent-sandbox claude`.

Run `agent-sandbox doctor` to verify.

## Install

```bash
go install github.com/ynny-github/agent-sandbox/agent-sandbox@latest
```

Or with [mise](https://mise.jdx.dev/):

```toml
# .mise.toml
[tools]
"go:github.com/ynny-github/agent-sandbox/agent-sandbox" = "latest"
```

## Quick start

Write `agent-sandbox.toml` in your project root:

```toml
tool_mode = "hook"

[sandbox.shared]
capabilities = ["go", "python"]

[sandbox.agent]
capabilities = ["ssh"]          # host credentials — agent only, never sandboxed commands
allow_commands = ["go *"]       # run directly on the host
drop_commands = [
  { pattern = "gh *", message = "gh is disabled; use the GitHub MCP tools." },
]
```

Check that it resolves, then launch:

```bash
agent-sandbox doctor            # are nono and the broker socket usable?
agent-sandbox ai config-check   # does the config resolve, and what does it grant?
agent-sandbox claude -- --model opus
```

There is no `sandbox up` step. `agent-sandbox claude` starts the host-side
command broker, launches Claude under nono, and tears the broker down when
Claude exits.

## How it works

### Routing

Every command the agent runs is matched against the policy, in this order:

1. **allow** — matches `sandbox.agent.allow_commands` → runs **on the host**,
   after shell-safety validation.
2. **drop** — matches `sandbox.agent.drop_commands` → **refused**. Neither the
   host nor the sandbox runs it; exit code 1 with a stderr line (the rule's
   `message`, or a default `dropped: command matches drop pattern "<pattern>"`).
3. **sandbox** — everything else → sent to the command broker, which runs it
   under its own `nono run` invocation scoped to the current working directory.

**Allow wins over drop.** A command matching both patterns runs on the host.
Patterns are globs where `*` matches any run of characters, anchored at both
ends (`go *` matches `go test ./...`, not `cd x && go test`).

Two patterns are always host-allowed and never written to the TOML file:
`agent-sandbox ai *` (so the agent can read its own environment docs) and
`agent-sandbox safe *` (the safe wrappers validate before running).

### The filesystem is not virtualized

There is no container and no bind mount. A sandboxed command runs directly on
the host filesystem at the *same absolute path*, restricted to the current
working directory (read + write). `HOME` keeps its real value. Nothing needs
translating between a host command and a sandboxed one.

Paths outside the working directory are reachable only where the config grants
them.

### Two profiles, nothing inherited between them

`agent-sandbox` generates two nono profiles: one for the launched agent, one for
the shell sandbox each brokered command runs in.

**Nothing is inherited between the two sides.** A grant reaches a sandbox only
if it is written where that sandbox can see it. This is why there is no way to
*subtract* a grant: "keep this away from sandboxed commands" is expressed by
declaring it under `[sandbox.agent]` instead of the shared base. Forgetting to
put a grant in `[sandbox.shared]` leaves a command sandbox without it; a command
sandbox can never silently *gain* one.

## Commands

| Command | What it does |
|---|---|
| `agent-sandbox claude -- [claude args...]` | Launch Claude under nono, with the command broker running |
| `agent-sandbox exec -- <command>` | Route and run one command, streaming output |
| `agent-sandbox doctor` | Check that `nono` works and the broker socket can bind. Exit 0 / 1 |
| `agent-sandbox debug -- [claude args...]` | Print the `nono` invocation, both generated profiles, and the GitHub MCP config (token redacted) — without running anything |
| `agent-sandbox ai explain` | Agent-facing description of the current sandbox environment |
| `agent-sandbox ai config-check` | Validate the config the way launch reads it, and print what a sandboxed command reaches |
| `agent-sandbox safe git [args...]` | Run git, refusing known-dangerous invocations |
| `agent-sandbox safe docker-compose [args...]` | Run docker compose after validating the resolved project |
| `agent-sandbox command-router` | Start the MCP server (`tool_mode = "mcp"`) |
| `agent-sandbox hook` | PreToolUse adapter (`tool_mode = "hook"`; invoked by Claude, not by you) |

Global flags: `--config <path>` (default `agent-sandbox.toml`) and `--env <ref>`
(repeatable).

For `claude` and `debug`, only `--config` and `--env` may appear before `--`;
everything after `--` goes to `claude`. `agent-sandbox` does not forward options
to `nono` — the profile comes from the config file.

`--settings` is reserved by `agent-sandbox` and rejected as a passthrough
option. `--mcp-config` / `--strict-mcp-config` are rejected too when the GitHub
MCP is enabled.

### `doctor`

`doctor` checks the two things a launch depends on:

- `nono` is on `PATH` and `nono --version` runs.
- The command broker can actually **bind a unix socket** in its socket directory
  (`$XDG_STATE_HOME/agent-sandbox`, or `~/.local/state/agent-sandbox`). A plain
  write check is not enough here — binding also catches the ~104-byte
  `sun_path` limit.

If either fails, `agent-sandbox claude` will not launch Claude at all.

## Configuration

### `tool_mode`

Selects how the agent's commands reach the router.

| Mode | Behavior |
|---|---|
| `hook` | Bash and Monitor stay enabled. A PreToolUse hook is injected at launch via `claude --settings`, rewriting each command to `agent-sandbox exec -- <command>`. Nothing is written to `.claude/settings.json`. `agent-sandbox` must be on `PATH`. |
| `mcp` (default) | Bash and Monitor are disabled. The agent routes commands through the `run_command` MCP tool, and output is written to files under `mcp.command_output_dir` — the response carries paths and an exit code only. |

In `hook` mode, commands route through a **policy snapshot frozen at launch**,
so editing `agent-sandbox.toml` mid-session does not change the running
session's policy. The edit takes effect at the next `agent-sandbox claude`.

```toml
tool_mode = "hook"

[mcp]
command_output_dir = "/tmp/mcp-output"  # required in mcp mode; ignored in hook mode
```

### Host access: the three sections

| Section | Applies to |
|---|---|
| `[sandbox.shared]` | Both the launched agent and the shell sandbox |
| `[sandbox.agent]` | The launched agent only |
| `[sandbox.shell]` | The shell sandbox (brokered commands) only |

All three take the same six host-access fields:

| Field | Grants |
|---|---|
| `capabilities` | Named bundles — see below |
| `allow` | Directories, read + write |
| `read` | Directories, read-only |
| `allow_file` | Single files, read + write |
| `read_file` | Single files, read-only |
| `allow_env` | Environment variable names |

`PATH`, `HOME`, `TERM`, `LANG`, `LC_ALL`, `USER` and `/dev/null` are always
granted from a built-in baseline.

`NONO_*` is rejected in every `allow_env` list — those variables reconfigure the
sandbox itself.

A raw `allow` / `read` targeting a protected prefix (`~/.ssh`, `~/.aws`,
`~/.docker`, `~/.gnupg`, `~/.config/gh`, `~/.kube`) is rejected; use the
matching capability instead.

### Capabilities

Named bundles that expand into directories, files, env vars, network domains,
and — for credential bundles — the matching Claude permission denies.

| Capability | Grants | Domains added to the shell sandbox |
|---|---|---|
| `go` | Go runtime group | `proxy.golang.org`, `sum.golang.org` |
| `python` | Python runtime group | `pypi.org`, `files.pythonhosted.org` |
| `node` | Node runtime group | `registry.npmjs.org` |
| `rust` | Rust runtime group | `crates.io`, `index.crates.io`, `static.crates.io` |
| `docker` | `~/.docker`, `~/.orbstack` (read-only) | `auth.docker.io`, `index.docker.io`, `registry-1.docker.io`, `production.cloudflare.docker.com` |
| `ssh` | `~/.ssh` (read-only), `~/.ssh/known_hosts` (read+write) | — |
| `mise` | `~/.local/share/mise`, `~/.config/mise` (read-only), `MISE*` env | `mise.jdx.dev`, `mise-versions.jdx.dev` |
| `bashrc` | `~/.bashrc`, `/etc/bashrc`, `/etc/bash.bashrc` (read-only) | — |

Each toolchain brings its own registry, so a config never has to restate the Go
module proxy or PyPI.

> **`docker` and `ssh` expose host credentials.** They are otherwise ordinary
> capabilities — they apply to whichever side declares them. Put them in
> `[sandbox.agent]`, not `[sandbox.shared]`, unless a sandboxed command really
> needs the keys. `agent-sandbox debug` warns when the shell profile ends up
> granting a credential path.

### Command routing

```toml
[sandbox.agent]
allow_commands = ["go *", "mise use *", "mise install *"]
drop_commands = [
  { pattern = "git *" },
  { pattern = "gh *", message = "gh is disabled in this sandbox. Use the GitHub MCP server's tools instead." },
]
```

Each `drop_commands` entry is a `{ pattern, message }` table. `message` is
optional; omitted, the default refusal line is printed.

Patterns have no negation, and allow is evaluated first — so an allow cannot
carve out a narrower case of itself. `"mise use *"` covers `mise use -g` too. To
constrain that, rely on the profile: the `mise` capability grants `~/.config/mise`
read-only, so the global write is refused by the sandbox rather than by the list.

### Network

Sandboxed commands run under nono's `developer` network profile (LLM APIs,
package registries, GitHub, sigstore, documentation), plus the domains the
declared capabilities bring.

```toml
[sandbox.shell]
allow_domains = ["internal.example.com"]   # only what neither covers
```

Domains apply to whichever side has a network to widen — that is the shell
sandbox only. The launched agent keeps the network of its nono base profile, so
a capability declared under `[sandbox.agent]` contributes no domains.

### User-scope config

An optional `~/.config/agent-sandbox/config.toml` is composed with the project
config:

- **Scalars**: the project file wins.
- **Lists**: a de-duplicated union of both, user entries first.

So a project file can *add* to a list but cannot *remove* what the user-scope
file contributes. If a grant you did not write shows up in
`agent-sandbox ai config-check`, that is where it comes from.

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
Exposing a variable to sandboxed commands stays an explicit config edit.

## GitHub MCP

The built-in GitHub MCP server is enabled when `GITHUB_MCP_TOKEN` is non-empty;
otherwise it is not configured at all. Its value is passed to the MCP server as
`GITHUB_PERSONAL_ACCESS_TOKEN`.

```bash
agent-sandbox claude --env file:.secrets.env -- --model opus
# .secrets.env: GITHUB_MCP_TOKEN=ghp_...
```

`agent-sandbox debug` prints the resulting MCP config with the token redacted.

## Safe wrappers

`agent-sandbox safe <tool> ...` validates an invocation, then passes it through
unchanged. On a refusal it exits 1 and runs nothing.

Because `agent-sandbox safe *` is always host-allowed, the usual pattern is to
drop the bare tool and let the agent reach it only through the wrapper:

```toml
[sandbox.agent]
drop_commands = [{ pattern = "git *" }]   # all git goes through `safe git`
```

### `safe git`

```bash
agent-sandbox safe git push --force-with-lease
```

Refused invocations:

| Rule | Refuses |
|---|---|
| force-push | `push --force` / `-f`, `--delete` / `-d`, `--mirror`, `--prune`, and `:`/`+` refspecs. `--force-with-lease` and `--force-if-includes` are allowed — unless a bare `--force` is also present |
| hard-reset | `reset --hard` |
| clean-force | `clean -f` / `--force` |
| branch-force-delete | `branch -D`, or `-d` with `--force` |
| filter-history | `filter-branch`, `filter-repo` |
| update-ref-delete | `update-ref -d` |
| reflog-expire | `reflog expire` |
| gc-prune | `gc --prune=now` / `--prune=all` |
| bypass-hooks | `--no-verify`, `--no-gpg-sign`, `commit -n`, and `-c` / `--config-env` setting `core.hooksPath` or disabling `commit.gpgsign` |
| alias-injection | `-c alias.*=...` |
| config-exec-injection | `-c` setting an exec-capable key (`core.sshCommand`, `core.pager`, `core.editor`, `credential.helper`, `gpg.program`, `diff.external`, …) |
| stash-destroy | `stash drop`, `stash clear` |
| remote-tamper | `remote remove` / `rm` / `set-url` |
| tag-delete | `tag -d` |
| discard-changes | `checkout -- <path>` / `checkout .`, and `restore` targeting the working tree |
| config-write | `git config` unless it is a read (`--get*`, `--list`, `-l`) |

### `safe docker-compose`

```bash
agent-sandbox safe docker-compose up -d
```

The project is resolved with `docker compose config` and the invocation is
refused when:

- a `bind` mount resolves outside the current working directory;
- a `bind` mount targets the Docker socket (`docker.sock`);
- a service sets `privileged: true`, `network_mode: host`, `pid: host`,
  `ipc: host`, or `userns_mode: host`;
- a service exposes host `devices`;
- `cap_add` contains a dangerous capability (`ALL`, `SYS_ADMIN`, `SYS_PTRACE`,
  `SYS_MODULE`, `SYS_RAWIO`, `SYS_BOOT`, `SYS_TIME`, `NET_ADMIN`, `NET_RAW`,
  `DAC_READ_SEARCH`, `DAC_OVERRIDE`, `MKNOD`);
- `security_opt` disables confinement (`*:unconfined`, `label:disable`);
- the subcommand is `run` or `exec`;
- a leading global flag cannot be classified (it fails closed).

Named volumes and `tmpfs` mounts are allowed. Every other subcommand (`up`,
`build`, `down`, `ps`, `logs`, …) passes through. The rules are fixed and
built-in.

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
