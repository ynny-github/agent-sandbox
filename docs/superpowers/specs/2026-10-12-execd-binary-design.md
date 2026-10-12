# execd becomes its own binary (design)

Date: 2026-10-12
Status: designed, not implemented

## Context

execd ships inside `agent-sandbox`. The launcher re-executes its own binary
as `agent-sandbox execd --socket <path>` under the command profile, and the
agent reaches it through `agent-sandbox exec -- <cmd>`, which the PreToolUse
hook rewrites every Bash command into.

The goal is distribution and reuse: execd should be installable and usable
without the rest of agent-sandbox. The intended reuser is someone who builds
their own `nono run` line (or equivalent) and needs a server plus a client —
not a turnkey launcher. Turnkey launch stays agent-sandbox's job.

`internal/execd` already depends only on the standard library and
`mvdan.cc/sh`. What ties execd to agent-sandbox is the cobra entrypoints
(`internal/cli/execd.go`, `internal/cli/exec.go`) and the launcher
re-executing `os.Executable()`.

## Goals

- `go install github.com/ynny-github/agent-sandbox/cmd/execd@latest` yields a
  binary that serves and that sends commands, with no agent-sandbox code in it
  beyond `internal/execd`.
- agent-sandbox stops carrying execd: no `execd` subcommand, no `exec`
  subcommand. It launches and talks to the `execd` binary found on PATH.
- One client implementation, used by everything that sends commands.

## Non-goals

- Changing the wire protocol, the transport (unix socket path), or how the
  agent's commands reach execd. A transport built on inherited descriptors and
  a PTY is being evaluated separately as a spike; if it is adopted it gets its
  own spec on top of this one.
- Renaming `AGENT_SANDBOX_EXECD_SOCKET`. context-mode reads it.
- A protocol version handshake. See "Version skew" below.
- Updating context-mode. Its `src/exec-backend.ts` spawns `agent-sandbox exec`
  and must switch to `execd exec` in the same release window; that change lives
  in the context-mode repository.

## Decisions

**Every binary lives at `cmd/<name>/`** (per the 2026-09-24 multi-binary
layout). The new one is `cmd/execd/main.go`, so the installed binary is named
`execd`.

**The `execd` CLI uses the standard library `flag` package, not cobra.** Two
subcommands do not justify the dependency in a binary meant to be small.

| Command | What it does | Replaces |
| --- | --- | --- |
| `execd serve --socket <path>` | Serve commands on the socket until SIGTERM/SIGINT; sets `TERM=dumb`; removes the socket on exit | `agent-sandbox execd` |
| `execd exec [--timeout d] -- <cmd>` | Send one command to the socket named by `AGENT_SANDBOX_EXECD_SOCKET`, stream its output, exit with its status; two-stage interrupt | `agent-sandbox exec` |

`serve` is not meant to run outside a sandbox: it executes whatever it is sent
at its own grants. Its usage text says so, as the hidden cobra command's
comment does today.

**The client core moves into `internal/execd`.** `runExecCore` and
`twoStageSignals` (today in `internal/cli/exec.go`, cobra-free apart from
argument handling) become exported functions there; `cmd/execd` calls them.
Their tests move with them.

**agent-sandbox deletes `execd` and `exec`.** `internal/cli/execd.go` and
`internal/cli/exec.go` go away.

**The launcher finds execd with `exec.LookPath("execd")`**, then resolves
symlinks (Landlock checks the target, so grants must name it). Not found →
the launch fails with an error naming
`go install github.com/ynny-github/agent-sandbox/cmd/execd@latest`. The
resolved path is used in two places:

- execd's session (`ExecdArgs`): `--read-file <execd>` and
  `-- <execd> serve --socket <sock>`.
- the agent's session (`BuildArgs`): `--read-file <execd>`, added beside the
  existing grant for the launcher's own binary, so the agent can run
  `execd exec`.

Resolution happens once per launch and the same path feeds both, so the two
sessions cannot disagree about which execd they run.

**The hook rewrites to `execd exec -- <quoted cmd>`.** `hookProbeWant` becomes
`execd exec --`.

**doctor checks both binaries.** Today one `profileAllowsWrite` call covers
execd and the launcher because they are the same file. After the split they
are two files, and the command profile must not be able to write either: a
writable execd replaces what the next session serves with; a writable
agent-sandbox replaces the launcher and the hook. doctor also reports a
missing `execd` on PATH as a failure.

**debug prints the resolved execd path** in the exec-daemon line, or the
lookup error when execd is not on PATH.

## Version skew

agent-sandbox and execd are now installed separately, so PATH can hold an
execd from a different commit. Both halves of the wire still come from one
binary per session (`execd serve` and `execd exec` are the same file), so a
skew only matters between agent-sandbox's launch flags and execd's CLI. That
surface is `serve --socket` and nothing else. No handshake is added; add one
when the protocol or that surface changes incompatibly.

## Affected files

| File | Change |
| --- | --- |
| `cmd/execd/main.go` | new |
| `internal/execd/` | gains the client core and its tests |
| `internal/cli/execd.go`, `internal/cli/exec.go`, `exec_test.go` | deleted (tests moved) |
| `internal/cli/hook.go` + test | rewrite target |
| `internal/claude/launch.go` + test | LookPath, `ExecdArgs`, `BuildArgs` grant, `hookProbeWant` |
| `internal/cli/doctor.go` + test | execd on PATH, writability of both binaries |
| `internal/cli/debug.go` | resolved execd path |
| `internal/claude/execd_e2e_test.go` | build both binaries into one temp dir and put it on PATH |
| `README.md`, `README.ja.md` | install line, command table, "How it works" |
| `.mise.toml` | build task: `go install -buildvcs=false ./cmd/...` |
| `agent-sandbox.toml` | comment naming `agent-sandbox exec` |

Historical documents under `docs/superpowers/` are not rewritten.

## Testing

The existing suite, adjusted for the moved tests and the new names, is the
specification. Additionally:

- launch: `ExecdArgs`/`BuildArgs` carry the resolved execd path, and a missing
  execd fails the launch with the install hint.
- doctor: a profile that grants write to execd fails; one that grants write to
  agent-sandbox fails; neither passes.
- e2e (`//go:build e2e`): a real session serves and answers `execd exec` with
  both binaries resolved from PATH.
