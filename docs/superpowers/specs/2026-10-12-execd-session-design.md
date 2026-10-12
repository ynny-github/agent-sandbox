# execd owns its sandbox session (design)

Date: 2026-10-12
Status: designed, not implemented
Builds on: `2026-10-12-execd-binary-design.md` (implemented)

## Context

execd is its own binary (`serve`, `exec`). Starting it is still the
launcher's job: `internal/claude/launch.go` picks the socket path, builds the
`nono run … -- execd serve --socket` argv, creates the socket directory,
waits for the socket, and on exit stops the process (SIGTERM, then SIGKILL)
and removes the directory.

That split already produced one security bug. A bind grant for a socket that
does not exist yet makes nono allow the socket's whole parent directory, and
the socket sat in agent-sandbox's state dir next to the agent's shell
wrapper; a command run through execd could rewrite the wrapper (fixed in
`7b87a3d` by giving the socket a directory of its own). The rules for placing
and granting the socket belong in one place, next to the server that binds it.

## Goals

- execd starts, reuses, stops and cleans up its own sandboxed session.
- `execd up` is idempotent: run again for the same session, it keeps the
  running one and prints its socket.
- The launcher no longer builds nono argv for execd, picks socket paths, or
  stops processes. It calls `execd up` and `execd down`.
- Long-unused sessions can be cleaned up together, on demand.

## Non-goals

- A sandbox-backend abstraction. nono is the only backend; no interface is
  introduced until a second one exists.
- Automatic cleanup. Nothing cleans up other sessions as a side effect of
  another command; only `execd gc` does.
- Sharing one session between concurrent launchers in the same shell.
- A user-chosen session name (`--session`). Add it when a caller needs one.
- Changing `serve`'s wire protocol, `exec`, or `AGENT_SANDBOX_EXECD_SOCKET`.

## Decisions

**The session is a daemon with separate `up` and `down`** (not a foreground
supervisor). `up` returns once the server is listening; `down` stops it.

**A session is identified by (caller session id, workdir, profile).**
- The caller session id is `getsid(0)` of the process running `execd up`. An
  interactive shell is its session's leader, so this is the shell's pid, and
  everything started from that shell (`agent-sandbox claude` → `execd up`)
  inherits it. It is POSIX, needs no tty, and depends on no terminal tool.
- workdir and profile are absolute paths (symlinks not resolved; they are
  identity, not grants).
- The key is the first 12 hex digits of SHA-256 over
  `sid "\x00" workdir "\x00" profile`.
- Consequence: repeated `up` in one shell reuses; another terminal or tmux
  pane in the same workdir gets its own session, so one launcher's `down`
  cannot stop another's. Two launchers run at once from one shell share a
  session, and the first to exit stops it; that case is not supported.
- A recycled sid can match a leftover session from a dead shell. It is reused
  only if it is alive and matches workdir, profile path and profile content,
  so it is the same sandbox either way.

**Sessions live under execd's own root, `$XDG_STATE_HOME/execd/`**
(`~/.local/state/execd/` when unset), not agent-sandbox's state dir. Each
session is `<root>/<key>/`, mode 0700:

| File | Written by | Content |
| --- | --- | --- |
| `sock/execd.sock` | serve | the socket |
| `sock/last-used` | serve | mtime touched on every request |
| `pid` | up | pid of the `nono run` process |
| `owner` | up | the caller session id |
| `profile.sha256` | up | SHA-256 of the profile's content at start |
| `log` | nono, serve | the session's stdout and stderr |

The socket's bind grant makes its directory (`sock/`) writable to the
sandbox, so that directory holds only what serve itself writes. The files up,
down and gc trust from outside the sandbox sit one level up: beside the
socket, a command could write another process's pid into `pid` and have the
next `execd down` SIGKILL it (found in final review; measured on nono 0.78.0
that `<key>/pid` is not writable from the session).

**`--workdir` is optional** and defaults to the current directory, made
absolute. It is always passed to nono explicitly, never left to nono's
default. **`--profile` is required**: a default would be a second
profile-location rule beside `agent-sandbox.toml`'s.

**The launcher keeps deciding which execd to trust.** `ResolveExecd` and the
check that the command profile can write neither the execd binary nor its
directory stay in the launcher: an execd planted on PATH would not check
itself.

## CLI

| Command | Behaviour |
| --- | --- |
| `execd up --profile <p> [--workdir <d>]` | Start or reuse the session; print its socket path on one stdout line |
| `execd down <socket>` | Stop that session and remove its directory; success if already gone |
| `execd gc [--idle <d>] [--dry-run]` | Clean up sessions (below); `--idle` defaults to 168h |
| `execd serve --socket <path>` | Unchanged; what `up` runs under nono, still usable directly |
| `execd exec [--timeout d] -- <cmd>` | Unchanged |

Exit status: 0 success, 1 runtime failure, 2 usage error. No config file.

## `up`

All of `up` and `down` for one key run under an exclusive `flock` on
`<root>/<key>.lock`.

1. Validate: the profile exists and is readable; `nono` is on PATH.
2. If `<root>/<key>/` exists:
   - alive (the `pid` process exists **and** the socket accepts a
     connection): if `profile.sha256` matches the profile now, print the
     socket and exit 0; otherwise fail, telling the caller to
     `execd down <socket>` and `up` again. Silently serving a stale profile
     would leave a tightened profile without effect.
   - not alive: remove the directory (leftover of this key only).
3. Create the directory (0700) and write `owner` and `profile.sha256`.
4. Start, in a new session (`setsid`), stdin `/dev/null`, stdout and stderr to
   `log`:
   `nono run --silent --profile <p> --workdir <d> --read-file <execd>
   --allow-unix-socket-bind <dir>/sock/execd.sock -- <execd> serve --socket
   <dir>/sock/execd.sock`, where `<execd>` is this binary's own path with symlinks
   resolved. Write `pid`.
5. Wait until the socket appears or the child exits, at most 15 s (the
   launcher's `waitForSocketOrExit` and `execdStartTimeout`, moved).
6. On failure: kill and reap the child, then fail with the last 20 lines of
   `log` in the message, and remove the directory.
7. Print the socket path; exit 0.

## `down <socket>`

1. Refuse unless `<socket>` is `<root>/<key>/sock/execd.sock` for a key
   directory directly under the root. `down` removes a directory tree; it must not be
   aimed at an arbitrary path.
2. If the socket does not accept a connection, the session is already dead:
   remove the directory and succeed, without signalling `pid` (a dead
   session's pid may belong to an unrelated process by now). A wedged server
   still accepts connections at the kernel level, so it takes step 3.
3. SIGTERM `pid`; wait up to the stop timeout (the launcher's
   `execdStopTimeout`, moved); then SIGKILL. Remove the directory.

## `gc`

For every key directory under the root (each under its key's lock):

| Condition | Action |
| --- | --- |
| not alive (as in `up`) | remove the directory |
| the `owner` session id no longer exists (`kill(owner, 0)` fails with ESRCH) | `down` |
| `last-used` older than `--idle` | `down` |
| otherwise | keep |

`--dry-run` prints what would happen and changes nothing; it doubles as the
listing. A failure on one session is reported and the rest continue; the exit
status is then 1.

## `serve`

On every request, `serve` touches `last-used` in its socket's directory
(`filepath.Dir(socket)`, i.e. `sock/`). It already has write access there through the bind
grant. A failed touch is ignored: it only affects `gc`'s idle judgement.

## agent-sandbox

- **launcher**: `startExecd` resolves and checks execd as today, runs
  `execd up --profile <cfg.CommandProfilePath()> --workdir <cwd>`, takes the
  trimmed stdout as the socket, and passes `up`'s stderr through. Its cleanup
  runs `execd down <socket>`. `ExecdArgs`, `ExecdSocketPath`, the socket
  directory, `waitForSocketOrExit`, the stop timeout and their tests move to
  `internal/execd`. The agent session's `--allow-unix-socket <socket>` and
  `--read-file <execd>` grants and `AGENT_SANDBOX_EXECD_SOCKET` are unchanged.
- **debug**: the socket path is a pure function of (sid, workdir, profile),
  so `internal/execd` exports one that computes it without starting anything;
  debug uses it for the agent argv, and prints the `execd up --profile …
  --workdir …` line in place of the nono argv.
- **doctor**: the exec-daemon check binds its probe socket under
  `$XDG_STATE_HOME/execd/` instead of agent-sandbox's state dir. The execd
  PATH and writability checks are unchanged.
- **A launcher killed before cleanup** leaves its session running; `execd gc`
  collects it (owner gone, or idle).

## Code layout

`internal/execd/session.go` holds `Up`, `Down`, `GC`, the root and key
functions, and the moved start/stop machinery; `cmd/execd` parses flags and
calls them. Nothing in `internal/execd` imports agent-sandbox packages.

## Testing

Fake `nono` scripts (moved from `internal/claude/launch_test.go`) stand in
for the sandbox; the caller session id is read through a package variable
tests replace.

- `up`: starts and prints the socket; a second `up` prints the same path
  without starting nono again (the fake counts its invocations in a file); a
  changed profile fails; a dead leftover is replaced; a different session id
  gives a different path; a child exiting before binding fails with the log
  tail and leaves no directory.
- `down`: stops a server that ignores SIGTERM via SIGKILL within the timeout;
  succeeds on an already-dead session without signalling; refuses a path
  outside the root.
- `gc`: on fabricated session directories, removes the dead one, downs the
  one whose owner does not exist and the one with `last-used` eight days old
  (`os.Chtimes`), keeps a live recent one; `--dry-run` changes nothing.
- `serve`: a request updates `last-used`.
- launcher: a fake `execd` on PATH prints a socket for `up` and records that
  `down` received the same path.
- e2e (`-tags e2e`, real nono): `up` → `exec` → `down`; two `up`s print the
  same path.
