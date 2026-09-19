# agent-sandbox

**English** | [日本語](README.ja.md)

Run an AI coding agent (Claude Code) inside a
[nono](https://github.com/tkancf/nono) sandbox, and mediate every shell
command it issues through execd, a host-side exec daemon that runs in its own
sibling nono session. What each command may touch is decided by a nono
command profile the operator writes — not by `agent-sandbox.toml`.

The point is not to lock the agent out of your machine. It is to make the
boundary *explicit and inspectable*: `agent-sandbox ai explain` tells the
agent which commands run in their own sandbox, which run at execd's own
grants, and why any refusal fired, so a policy denial reads as a policy
denial rather than an unexplained failure worth retrying.

```
launcher
├── nono wrap  --profile <agent profile>    -- claude …         no command control here
└── nono run   --profile <command profile>  -- agent-sandbox execd
                                               │
                                               ├─ exec git → shim → git, its own child sandbox
                                               │                     └─ exec ssh → shim → ssh, its own
                                               └─ exec rg  → runs directly in execd's own sandbox
```

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

**Install outside every path the command profile grants write access to** —
a session will not start otherwise. `go install` already does this.

## Quick start

Write `agent-sandbox.toml` in your project root. It only names the profile
files; it does not build them.

```toml
[agents.claude]
profile = "claude-profile.json"
```

Then write both profiles yourself, directly in nono's own schema:
`claude-profile.json` for the agent process's own sandbox, and
`command-profile.json` for the sandbox every command execd runs gets. There is no
default for either — a missing file is a launch error. This repository's own
two files are worked examples, and each entry carries its reasoning in a
comment.

```bash
agent-sandbox doctor            # nono, the execd socket, and both profiles all usable?
agent-sandbox ai config-check   # does agent-sandbox.toml resolve, and do both profiles validate?
agent-sandbox claude -- --model opus
```

There is no `sandbox up` step. `agent-sandbox claude` starts execd in
its own nono session, launches Claude under a second, sibling session, and
tears execd down when Claude exits.

## How it works

**Two sibling sessions, never nested.** One wraps Claude Code under the agent
profile; the other runs `agent-sandbox execd` under the command profile.
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
| `agent-sandbox claude -- [claude args...]` | Launch Claude under nono, with execd running as a sibling session |
| `agent-sandbox exec -- <command>` | Send one command to execd and stream its output |
| `agent-sandbox doctor` | Check everything a launch depends on: the sandbox engine, the execd socket, both profiles and the paths they pin, and that the command profile does not leave execd's own binary writable. Exit 0 / 1 |
| `agent-sandbox debug -- [claude args...]` | Print the `nono` invocations for both sessions — without running anything |
| `agent-sandbox ai explain` | Agent-facing description of the sandbox: how commands run, both tiers, and every denial's reason |
| `agent-sandbox ai config-check` | Validate `agent-sandbox.toml` and both nono profiles the way launch reads them |
| `agent-sandbox hook` | PreToolUse adapter, injected at launch and invoked by Claude, not by you |

Global flags: `--config <path>` (default `agent-sandbox.toml`) and `--env <ref>`
(repeatable). For `claude` and `debug`, only those two may appear before `--`;
everything after `--` goes to `claude`. `--settings` is reserved — it carries
the PreToolUse hook — and is rejected as a passthrough option.

## Configuration

```toml
command_profile = "command-profile.json" # default name; shared by every agent

[agents.claude]
profile = "claude-profile.json"          # default name: "<agent>-profile.json"
```

Bash and Monitor stay enabled, and a PreToolUse hook injected at launch via
`claude --settings` rewrites each command to `agent-sandbox exec -- <command>`.
Nothing is written to `.claude/settings.json`, and `agent-sandbox` must be on
`PATH`. Before handing over control the launcher runs the hook once with a
probe payload and refuses to launch unless the command comes back rewritten —
Claude Code treats a hook that cannot start as a non-blocking error and runs
the command anyway, which would be a bypass rather than a degraded mode.

Profile paths resolve beside `agent-sandbox.toml` unless written absolute. An
optional `~/.config/agent-sandbox/config.toml` is composed with the project
config, and the project file wins for anything it sets — so "every project has
a `claude-profile.json` beside its config" can be declared once, user-wide.

Both profiles are read once, at session start. Editing one takes effect at the
next `agent-sandbox claude`, never mid-session.

The agent's shell is a wrapper the launcher generates, not the host's bash.
Claude Code spawns tool commands with a socket on stdin, and non-interactive
bash reads a socket on stdin as an rshd/sshd session and sources `~/.bashrc` —
a file no profile here grants, so without the wrapper every tool result is
prefixed with a permission error. The launcher writes `norc-bash-<pid>` beside
the execd socket (`bash --norc --noprofile`, nothing else), grants it with
`--read-file`, names it in `CLAUDE_CODE_SHELL`, and removes it when the session
ends. The agent profile's `environment.allow_vars` must list that variable or
nono strips it and Claude falls back to the host's bash; the session still
works, it just gets noisy, so `agent-sandbox doctor` measures it.

`--env <ref>` (only `file:` exists today) loads a dotenv-subset file into the
launcher's own process. **It grants nothing.** Only what a profile's
`environment.allow_vars` lists is forwarded, so a variable reaches the agent
only if the agent profile names it. Exposing one to a command execd runs is a
separate edit to the command profile — with one exception:
`AGENT_SANDBOX_EXECD_SOCKET` must never appear there, under any name or
wildcard. A command that can reach the execd socket can recurse into
execd, which spawns handlers with no concurrency cap.

## Development

```bash
mise install          # Go + lefthook
go test ./...         # unit + integration tests
go build ./...
mise run build        # install a working-tree build via `go install`
```


**Use `mise run build`, not `go build` or `go run .`.** Only `go install`
puts the binary outside this working tree and on `PATH`, which is where it
has to be for a launch to work.
Commits follow [Conventional Commits](https://www.conventionalcommits.org/);
`lefthook` validates the title on `commit-msg`.

## License

[MIT](LICENSE) © Yuya Nagai
