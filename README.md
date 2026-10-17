# agent-sandbox

**English** | [日本語](README.ja.md)

Run an AI coding agent (Claude Code) inside a
[nono](https://github.com/tkancf/nono) sandbox, and mediate every shell
command it issues through execd, a host-side exec daemon that runs in its own
sibling nono session. What each command may touch is decided by a nono
command profile the operator writes.

The point is not to lock the agent out of your machine. It is to make the
boundary *explicit and inspectable*: `agent-sandbox ai explain` tells the
agent which commands run in their own sandbox, which run at execd's own
grants, and why any refusal fired, so a policy denial reads as a policy
denial rather than an unexplained failure worth retrying.

```
agent-sandbox launch claude → .agent-sandbox/bin/claude (your script)
├── nono run   --profile <agent profile>    -- claude …         no command control here
└── execd up → nono run --profile <command profile> -- execd serve
                                               │
                                               ├─ exec git → shim → git, its own child sandbox
                                               │                     └─ exec ssh → shim → ssh, its own
                                               └─ exec rg  → runs directly in execd's own sandbox
```

## Requirements

- [nono](https://github.com/tkancf/nono) on `PATH` — the sandbox engine.
- Go 1.25 or later (to build from source).
- `claude` on `PATH` — for the `claude` launch script `init` writes.

Run `agent-sandbox doctor` to verify.

## Install

```bash
go install github.com/ynny-github/agent-sandbox/cmd/...@latest
```

This installs both `agent-sandbox` and `execd`; the launch script runs both from `PATH`.

Or with [mise](https://mise.jdx.dev/):

```toml
# .mise.toml
[tools]
"go:github.com/ynny-github/agent-sandbox/cmd/agent-sandbox" = "latest"
"go:github.com/ynny-github/agent-sandbox/cmd/execd" = "latest"
```

**Install outside every path the command profile grants write access to** —
a session will not start otherwise. `go install` already does this.

## Quick start

Run `agent-sandbox init` in your project root. It downloads a starting point
from this repository's `main` branch — `.agent-sandbox/` (a launch script, the
hook settings, a shell wrapper, a minimal command profile and agent profile)
and two skills that explain how to grow a profile. It never overwrites: a file
that already exists is reported and left alone.

```bash
agent-sandbox init
```

What it writes is a starting point, not a finished boundary. The command
profile gives commands `$WORKDIR` read/write, an `allow_domain` list covering
GitHub, six environment variables, and policies for `git` and `ssh` — with
`ssh` reachable only through `git`. Widen it as you need to; the seeded
`growing-a-nono-profile` skill walks through each change.

**On NixOS the command profile needs one edit.** Executables live in
`/nix/store`, which nono's default floor does not grant, so every command
fails with exit 127 until you add:

```json
"groups": { "include": ["nix_runtime"] },
"filesystem": { "read": ["/nix/store", "/run/current-system/sw"] },
```

and add the output of `git --exec-path` to both git's `exec_paths` and its
`fs_read` (a child sandbox needs both to run a helper). The agent
profile is unaffected — its `nolabs-ai/claude` base already carries
`nix_runtime`.

**The agent profile extends a registry pack.** Both the template and this
repository's own `.agent-sandbox/claude-profile.json` extend `nolabs-ai/claude`, which nono
0.78 no longer ships built in. Install it once before the first launch:

```bash
nono pull nolabs-ai/claude
```

Pulling it also enables the pack's `nono@nolabs-ai` Claude Code plugin in
`~/.claude/settings.json`; `nono remove nolabs-ai/claude` undoes both.

You can also write both profiles from scratch, directly in nono's own schema.
This repository's own `.agent-sandbox/` is a worked example of profiles grown
over time, and each entry carries its reasoning in a comment;
`templates/minimal/.agent-sandbox/` is the starting point `init` serves.

```bash
agent-sandbox launch claude            # start Claude; extra args go to the script
agent-sandbox launch claude --model opus
```

`.agent-sandbox/bin/claude` is a shell script you own: it runs
`agent-sandbox doctor`, starts execd with `execd up`, launches Claude under
`nono run` with the hook settings, and stops execd with `execd down` when Claude
exits. Edit it freely — add herdr or tmux, extra flags, or another script in
`bin/` for another agent.

Start it only through `agent-sandbox launch`. Every file under the working
directory — `.agent-sandbox/`, the profiles, even `.git` — is writable by the
commands the agent runs, so a script could be rewritten between launches.
`launch` compares `.agent-sandbox/` with the copy you last approved, kept in
`~/.config/agent-sandbox/trusted/`, shows any change as a diff and asks on the
terminal, then runs the approved copy. Do not put `.agent-sandbox/bin` on
`PATH`: that would run it unapproved, and a script named `claude` would call
itself. A shell alias in your own rc file is the shortcut:

```bash
alias claude-sb='agent-sandbox launch claude'
```

## How it works

**Two sibling sessions, never nested.** One wraps Claude Code under the agent
profile; the other is execd under the command profile, started by `execd up`.
Sandboxes cannot nest, which is why execd does not run inside the
agent's session.

**There is no shell in the loop.** Every command the agent issues reaches
execd over a unix socket, and execd parses the line itself with an
embedded interpreter — pipelines, `&&`/`||`/`;`, redirections, globbing,
`$(…)`, `for`/`if`, `cd` and the other builtins all work — then calls
`execve` directly for each simple command. Handing the line to `bash -c`
instead cannot be made safe: a denial written for `git reset --hard` is
defeated by invoking git through its store path instead of by name.

**Two tiers.** Commands declared in the command profile get their own child
sandbox, reached only through the generated shim; everything else runs
directly at execd's own grants. A refusal from the first tier explains
itself — the denial carries the `reason` its profile entry wrote. A failure
at the second is plain `execve` permission, with no reason to give.
execd's own builtins are a third case: they run inside the execd process,
so a redirect or a glob you write is bounded by execd's grants, not by
the command it is attached to.

**Neither profile is agent-sandbox's.** It generates no profile at all. Both
files are yours; agent-sandbox resolves their paths and hands them to `nono`,
which is what decides every grant — see
[nono's own documentation](https://github.com/tkancf/nono) for the schema and
for `nono profile show` / `nono why`.

## Commands

| Command | What it does |
|---|---|
| `agent-sandbox init` | Download `.agent-sandbox/` and the profile skills into this project. Never overwrites |
| `agent-sandbox launch <name> [args...]` | Run `.agent-sandbox/bin/<name>` after you approve any change to `.agent-sandbox/`; the approved copy is what runs |
| `agent-sandbox doctor` | Check everything a launch depends on: the sandbox engine, the execd socket, both profiles and the paths they pin, that the hook runs under the agent profile, that the command profile leaves neither `execd` nor `agent-sandbox` writable, and that the agent profile cannot write the approved copy `launch` runs. `--command-profile`/`--agent-profile` default to `.agent-sandbox/`. Exit 0 / 1 |
| `agent-sandbox ai explain` | Agent-facing description of the sandbox: how commands run, both tiers, and every denial's reason |
| `agent-sandbox ai pointer` | The system-prompt line the launch script passes to Claude |
| `agent-sandbox hook` | PreToolUse adapter, named in `.agent-sandbox/settings.json` and invoked by Claude, not by you |

`execd` is its own binary and takes no config file:

| Command | What it does |
|---|---|
| `execd up --profile <path> [--workdir <dir>]` | Start the session for this shell, workdir and profile under nono, or reuse the running one; print its socket. The launch script runs this |
| `execd down [<socket>]` | Stop that session and remove its directory; without a socket, stop every session this shell started. The launch script runs this on exit |
| `execd gc [--idle <duration>] [--dry-run]` | Clean up sessions that are dead, whose shell is gone, or unused for longer than `--idle` (default 168h) |
| `execd exec [--timeout d] -- <command>` | Send one command to the socket named by `AGENT_SANDBOX_EXECD_SOCKET` and stream its output |

Sessions live under `$XDG_STATE_HOME/execd/` (`~/.local/state/execd/`). One whose launch script never reached `execd down` — it was killed — keeps running until `execd gc` collects it.

[context-mode](https://github.com/ynny-github/context-mode) can run the code
`ctx_execute` executes through execd and the command profile instead of inside
the agent profile: uncomment `export CONTEXT_MODE_EXEC_BACKEND=execd` in the
launch script. `doctor` then checks that the plugin is enabled and that the
selection actually reaches the sandbox — an unforwarded variable is a silent
fall back to the agent profile, not an error. It requires
`CONTEXT_MODE_EXEC_BACKEND` in the agent profile's `environment.allow_vars` and
a `node` the agent profile can execute.

## Configuration

```
.agent-sandbox/
  bin/claude             launch script (yours; add more for other agents)
  settings.json          PreToolUse hook settings for `claude --settings`
  bash                   shell wrapper for CLAUDE_CODE_SHELL
  command-profile.json   execd's profile
  claude-profile.json    the agent's profile
```

There is no config file. Bash and Monitor stay enabled, and the hook in
`settings.json` rewrites each command to `execd exec -- <command>`. Nothing is
written to `.claude/settings.json`, and `agent-sandbox` and `execd` must be on
`PATH`. `doctor` (which the launch script runs first) proves the hook runs under
the agent profile — Claude Code treats a hook that cannot start as a
non-blocking error and runs the command anyway, which would be a bypass rather
than a degraded mode.

A session runs from the approved copy, so editing `.agent-sandbox/` never
changes a running session; the next `launch` shows the change and asks.

The agent's shell is `.agent-sandbox/bash`, not the host's bash. Claude Code
spawns tool commands with a socket on stdin, and non-interactive bash reads a
socket on stdin as an rshd/sshd session and sources `~/.bashrc` — a file no
profile here grants, so without the wrapper every tool result is prefixed with
a permission error. The launch script grants it with `--read-file` and names it
in `CLAUDE_CODE_SHELL`. The agent profile's `environment.allow_vars` must list
that variable or nono strips it and Claude falls back to the host's bash; the
session still works, it just gets noisy, so `agent-sandbox doctor` measures it.

Only what a profile's `environment.allow_vars` lists is forwarded, so a
variable reaches the agent only if the agent profile names it. Exposing one to a
command execd runs is a separate edit to the command profile — with one
exception: `AGENT_SANDBOX_EXECD_SOCKET` must never appear there, under any name
or wildcard. A command that can reach the execd socket can recurse into execd,
which spawns handlers with no concurrency cap.

## Development

```bash
mise install          # Go + lefthook
go test ./...         # unit + integration tests
go build ./...
mise run build        # install working-tree builds of both binaries via go install
```


**Use `mise run build`, not `go build` or `go run ./cmd/agent-sandbox`.** Only `go install`
puts the binary outside this working tree and on `PATH`, which is where it
has to be for a launch to work.

`templates/minimal/` holds what `agent-sandbox init` serves. It is fetched from
`main` at run time rather than embedded, so a change there reaches users
without a release — and `go test ./...` runs `nono profile validate` against
those files, which is the only thing guaranteeing `main` stays usable. Do not
confuse them with this repository's own `.agent-sandbox/`, whose profiles are
grown for this host and are not portable.

Commits follow [Conventional Commits](https://www.conventionalcommits.org/);
`lefthook` validates the title on `commit-msg`.

## License

[MIT](LICENSE) © Yuya Nagai
