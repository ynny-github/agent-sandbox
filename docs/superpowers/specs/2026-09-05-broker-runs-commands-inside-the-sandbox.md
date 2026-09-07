# The broker runs commands, from inside the sandbox

Date: 2026-09-05
Status: implemented
Supersedes: [2026-09-04-broker-with-command-policies.md](2026-09-04-broker-with-command-policies.md)
Prerequisite reading: [2026-09-04-tool-sandbox-blocked-by-claude.md](2026-09-04-tool-sandbox-blocked-by-claude.md)

Everything below marked *measured* was measured on 2026-09-05 with nono 0.74.0
(carrying the NixOS ELF patch), Linux 6.18.42, NixOS. The profiles and results
are in [2026-09-05-broker-probes.md](2026-09-05-broker-probes.md).

## The shape

Put the command broker *inside* a `nono run` session and let it be a
policy-controlled command. It interprets the agent's shell line in its own
process and execs each simple command itself. It never starts `nono run` again.

```
launcher
├── nono wrap  --profile <agent profile>    -- claude …        no command_policies
└── nono run   --profile <command profile>  -- agent-sandbox broker
                                               │
                                               ├─ exec git   → shim → child sandbox + argv policy
                                               └─ exec rg    → runs in the broker's own sandbox
```

The two sessions are siblings, not nested, so nono's refusal to nest a sandbox
inside a sandbox does not apply.

The command profile is written by the operator, in nono's own schema. Everything
about commands — which may run, what each may touch, which invocations are
refused — is expressed there. agent-sandbox generates only the agent profile.

## Why the previous design does not work

The superseded design put the agent's whole command line into `bash -c` and ran
it as the session entrypoint under a generated profile. Three of its load-bearing
claims are false.

**`invocation_policy` denials are bypassable when a shell is the entrypoint.**
The old document states they "cannot be evaded by wrapping" and are "not
bypassable this way". Measured, with the exact profile shape it recommends:

| invocation | result |
| --- | --- |
| `bash -c 'git reset --hard'` | denied, with the rule's `reason` |
| `bash -c '/nix/store/…/git reset --hard'` | **runs** — real git, no policy check |
| `bash -c '/run/current-system/sw/bin/git reset --hard'` | **runs** |

No configuration closes this while a shell is the entrypoint. Narrowing
`exec_paths` to git's own binary makes it worse (the PATH form bypasses too);
narrowing it to the shim directory stops everything from running; adding git to
`command_policies.deny_direct_exec_bypass` disables the shim as well.

This is not a nono bug. nono's documentation says directly:

> Do not point `exec` at a general-purpose shell (`/bin/sh`, `/bin/bash`, etc.):
> it would let the caller run arbitrary commands with those credentials,
> defeating the sandbox.

and describes `exec_paths` as a narrow escape hatch —

> Linux-only. Extra paths the child may execute, for multi-call tools (e.g.
> `git`) that re-exec their own helpers by absolute path from a compiled-in
> exec-path.

— not as the way to let a shell run arbitrary programs. There is no documented
profile anywhere that makes a shell a policy command. Opening that hatch over
`/nix/store` is what produced the bypass.

**Per-command network grants do not exist.** The old design grants `go` the Go
module proxy through `network.allow_domain` on its command entry. Measured, a
child's `allow_domain` is ignored: a `curl` entry restricted to `pypi.org`
reached `github.com`. Only `allow_all` (on/off) has any effect on a child.
Domain filtering (`network_profile` / `allow_domain` in the profile's
top-level `network` section) governs the session's own direct sandbox; a
command_policies child with `network: {"allow_all": true}` is not bounded by
it — measured 2026-09-07 against this repository's own profile, `realgit`
with `allow_all` reached a domain outside the session's `network_profile`
allowlist, and reached one even with the top-level `network` set to
`{"block": true}`. A bare `network: {}` on a child (present, no `allow_all`)
behaves the same as omitting the key: nothing is reachable. So a
command_policies child's network is not "on, bounded by the session
ceiling" — it is either fully open (`allow_all`) or fully closed (anything
else), with no state in between and nothing that narrows the open state.

**`exec_paths` is not in the published JSON Schema.** `nono profile schema`
emits `CommandSandboxConfig` with `additionalProperties: false` and no
`exec_paths` key, although the runtime honours it and the prose documents it.
`nono profile validate` does not catch the discrepancy — it checks JSON syntax
and group references only. Do not treat a passing `validate` as schema
conformance.

One claim of the old document survives intact and is worth restating: a command
reached by bypassing its shim runs in the *caller's* sandbox, so it can only
lose privilege. Widening entries stay sound; denials are what break.

## Architecture

### Two tiers of command

The command profile sorts every runnable program into one of two tiers. A
program in neither cannot be executed at all — that is the allowlist.

**Policy commands** are declared under `command_policies.commands` with a
`from.<broker>` edge and are deliberately *absent* from the broker's
`exec_paths`. Each gets its own child sandbox with its own filesystem and
network grants, plus an `invocation_policy` carrying argv rules. Because the
broker holds no execute right on their real binaries, the shim is the only way
to reach them and the rules cannot be bypassed. This tier is for commands that
need argv-level control or their own grants: `git`, `docker`.

**Floor commands** are named in the broker's `exec_paths` (a directory or an
individual file) and are *not* policy commands. They run in the broker's own
sandbox. They carry no argv rules, which is why listing their binaries is not a
hole: there is no denial to bypass. This tier is for ordinary tools: `rg`, `go`,
`cat`, `agent-sandbox` itself.

Measured, in one profile: `git reset --hard` is denied by its rule;
`/nix/store/…/git reset --hard` is refused at `execve`; `rg` and
`git --version | rg version` both run; `bash -c '…'` is refused at `execve`
because no shell is in either tier.

### The broker interprets the shell language itself

The agent's Bash tool emits a command *line*, and `nono run -- <argv>` takes an
argv. Measured, passing the line as a single argument fails with
`cannot find binary path` — the splitting is unavoidable. Handing the line to a
shell is what the previous section rules out.

So the broker parses and evaluates the line in-process with
[`mvdan.cc/sh/v3`](https://pkg.go.dev/mvdan.cc/sh/v3) at v3.13.1 (BSD-3, the
engine behind `shfmt`; v3.14 raises the Go floor to 1.26 and is not taken). `interp.ExecHandler` is called for every simple command that is
neither a builtin nor a shell function, and that handler is the only place an
`execve` happens.

This means the shell *language* is supported — pipelines, `&&`/`||`/`;`,
redirections, globbing, `$(…)`, parameter expansion, `for`/`if`/`while`,
heredocs, `cd` and the other builtins — while every command execution remains
mediated. Measured inside the sandbox: `echo *.go` expands, `for f in *.go`
iterates, `$(git --version)` routes through the handler, `git --version > out`
writes through the interpreter, `git reset --hard || echo …` reports the denial
and continues.

Two consequences worth stating plainly. Globbing, redirection and command
substitution are performed by the interpreter, so they are bounded by the
*broker's* sandbox rather than the agent's — a redirect cannot write where the
command profile does not allow. And there is no list of refused shell
constructs to maintain, which is what the previous design would have needed.

### Where each piece runs

| | process | sandbox |
| --- | --- | --- |
| shell parsing and evaluation | broker | the broker's own |
| glob / redirect / `$(…)` file access | broker | the broker's own |
| a floor command | child of broker | the broker's own |
| a policy command | child of broker, via shim | its own, per the profile |

## The command profile

The operator writes it; agent-sandbox only points nono at it.

**Location.** `command-profile.json`, resolved next to `agent-sandbox.toml`.
A top-level `command_profile = "<path>"` in the TOML overrides the name;
relative paths resolve against the directory holding the TOML. A missing file is
a launch error — agent-sandbox does not fall back to a generated default,
because a static default cannot absorb the host differences the capability
catalog handles today (`/nix/store` versus `/usr/bin`), and a profile that looks
present but refuses every command is the worst failure mode.

**`$WORKDIR`.** nono expands it inside `filesystem` and inside every
`command_policies` child sandbox (measured), and `nono run --workdir` sets it.
The operator writes `$WORKDIR`; agent-sandbox never rewrites the file. This is
also what makes one profile work across git worktrees.

**Network.** The top-level `network` section is a ceiling for the session's
own direct sandbox, and the only place domain filtering works — for that
sandbox. A command_policies child's `network` is not bounded by it: measured
2026-09-07 against this repository's own profile, holding the top-level
`network` fixed and varying only the child, `realgit` with
`{"allow_all": true}` reached a destination the ceiling's own
`network_profile` allowlist excluded, and reached one even with the ceiling
set to `{"block": true}`. A bare `network: {}` on a child (present, no
`allow_all`) reaches nothing, the same as omitting the key. So a
command_policies child's network is effectively binary — fully open
(`allow_all`) or fully closed — and the top-level ceiling does not narrow
the open state; there is no per-command domain limiting either way. This
repository's own ceiling is separately set to `{"network_profile": null}`
(unbounded, not a named profile) as a matter of declared policy, but that
setting is not what makes `realgit`'s reach unbounded — nothing in this
profile bounds it. See "Accepted residual" below for the consequence.

**Shape.**

```json
{
  "meta": { "name": "agent-sandbox commands" },
  "filesystem": {
    "allow": ["$WORKDIR"],
    "read":  ["/nix/store", "/run/current-system/sw"],
    "read_file": ["/opt/agent-sandbox/bin/agent-sandbox"]
  },
  "environment": { "allow_vars": ["PATH", "HOME", "USER", "LANG", "TERM"] },
  "network": { "network_profile": null },

  "command_policies": {
    "executable_dirs": ["/opt/agent-sandbox/bin"],
    "commands": {
      "agent-sandbox": {
        "executable": "/opt/agent-sandbox/bin/agent-sandbox",
        "can_use": ["git"],
        "from": { "session": { "sandbox": {
          "fs_read_file": ["/opt/agent-sandbox/bin/agent-sandbox"],
          "fs_read":  ["/nix/store", "/run/current-system/sw", "$WORKDIR"],
          "fs_write": ["$WORKDIR"],
          "exec_paths": ["/nix/store/…-ripgrep-15.2.0/bin", "…"],
          "environment": { "allow_vars": ["PATH", "HOME", "USER", "LANG", "TERM"] }
        }}}
      },
      "git": {
        "executable": "/nix/store/…-git-2.54.0/bin/git",
        "from": { "agent-sandbox": {
          "sandbox": {
            "fs_read_file": ["/nix/store/…-git-2.54.0/bin/git"],
            "fs_read":  ["/nix/store", "/run/current-system/sw"],
            "fs_write": ["$WORKDIR"],
            "network": { "allow_all": true },
            "environment": { "allow_vars": ["PATH", "HOME", "USER", "LANG", "TERM"] }
          },
          "invocation_policy": {
            "default": "allow",
            "deny": [
              { "argv": { "contains": ["--force"] },
                "reason": "force push is disabled in this sandbox" },
              { "argv": { "prefix": ["reset", "--hard"] },
                "reason": "hard reset is disabled in this sandbox" }
            ]
          }
        }}
      }
    }
  }
}
```

The session entrypoint is `agent-sandbox broker`, so `agent-sandbox` is the
policy command's name and every other command hangs off `from.agent-sandbox`.
`agent-sandbox ai explain` needs no entry of its own: nono keeps a command's
effective policy across self-invocation unless an explicit self edge says
otherwise.

**The launcher invokes this entrypoint by base name, never by absolute path,
and that name resolves through the launcher's own `PATH`, not through
`executable_dirs`.** nono treats an absolute-path invocation of a declared
policy command as a direct exec bypass and refuses it outright — the same
refusal `git`'s own shim produces for an absolute-path bypass attempt,
measured here against the entrypoint itself. Measured separately, holding
one variable at a time: a profile whose `command_policies.executable_dirs`
names the installed binary's directory, invoked with that directory absent
from the launcher's own `PATH`, fails outright (`cannot find binary path`);
the same profile with `executable_dirs` pointed at an unrelated, empty
directory instead, but with the right directory on `PATH`, starts the named
binary successfully. `executable_dirs` plays no measured part in resolving
the session entrypoint — nono resolves the bare name the same way an
ordinary shell would, searching the launcher process's own `PATH` before any
sandbox exists at all.

This means the directory holding the installed `agent-sandbox` binary must
be on the *launcher's* `PATH` — and that PATH resolution finds whichever
`agent-sandbox` comes first on it, not necessarily the one currently running:
a stale copy or an unrelated program sharing the name, earlier on that same
`PATH`, would silently become the broker instead. `agent-sandbox doctor`
checks that resolving the entrypoint's base name through this process's own
`PATH` lands back on this exact binary.

`reason` reaches the agent verbatim:
`nono: tool-sandbox denied git: Command 'git' is blocked: force push is disabled in this sandbox`
(exit 126).

**Argv matching is by token, not substring** (measured): a `--force` denial does
not catch `--force-with-lease`, and `prefix: ["reset","--hard"]` leaves
`reset --soft` alone. `invocation_policy` fails closed — with the block present
and no `default`, the default is `deny`.

## What the broker becomes

`internal/broker` keeps its wire protocol and its server. What changes is the
executor.

`NonoExecutor` dissolves. It exists to spawn `nono run` per command, and with
the broker inside the sandbox there is nothing to spawn:

| today | after |
| --- | --- |
| `exec.CommandContext(nono, "run","--silent","--profile",P,"--workdir",cwd,"--", argv…)` | `exec.CommandContext(argv[0], argv[1:]…)`, resolved to a shim or a floor binary |
| `ProcessEnv` filters the launcher's environment through the profile's `allow_vars` | gone — the broker's own environment is already filtered by nono, and each command's is decided by its entry |
| `checkCwd` validates the request's working directory | gone — a directory outside the grant simply fails |
| `--workdir` passed per request | passed once, at broker startup |
| `waitDelay` backstop | kept; a grandchild holding the output pipes is still possible |

The `Request` gains nothing. Redirections are handled by the interpreter inside
the broker, so they never reach the wire.

### Stream ownership is the one delicate part

The exec handler must not hand the interpreter's own pipe ends to a child. A
policy command reaches its target through a shim, and the shim duplicates every
fd it is given and keeps the copy. Pass the interpreter's pipe writer straight
through and it stays open after the command exits, so the next stage never sees
EOF: measured, `rg --version | rg ripgrep` with both commands policy-controlled
produced its complete, correct output and then hung 5/5, stranding the reader's
shim in `anon_pipe_read` — a leaked process that never exits. Closing the
broker's own copy of that writer does not help; the shim holds another.

The handler interposes an os/exec pipe on every stream the interpreter supplies
that is not already a real file, and hands the child *that* pipe:

- `cmd.StdoutPipe()` / `cmd.StderrPipe()`, copied into the interpreter's writer.
  The shim then only ever duplicates the os/exec pipe, so the child's exit
  closes every copy of it, the copy drains, and the interpreter closes its own
  writer when the *stage* ends. The handler must **not** close the
  interpreter's writer itself: the interpreter owns it and closes it at the
  right time, and closing it per command silently truncates any stage holding
  more than one command — `{ a; b; } | c` loses `b`'s output entirely — as well
  as closing a caller-supplied stream after the first command.
- When the interpreter hands the same writer as both stdout and stderr — what
  `2>&1` produces — a **single** pipe, with the same `*os.File` write end
  assigned to both. Two pipes means two unsynchronized copy goroutines writing
  one `io.Writer`, which loses output; os/exec makes the same guarantee
  internally for exactly this reason.
- The os/exec end closed when a copy fails, which is what delivers `EPIPE` to a
  writer whose reader has already exited (`seq … | head -n 1`).
- `cmd.Start()`, then drain, then `cmd.Wait()`. `Wait` closes the parent pipes
  as soon as the process exits, so draining after it is the wrong order.
- `cmd.StdinPipe()` with a pump goroutine rather than `cmd.Stdin`, for the
  reason the current `NonoExecutor` documents: with `cmd.Stdin` set, `Wait`
  blocks on os/exec's copier, which sits in `Read()`.

Command lookup goes through `interp.LookPathDir(hc.Dir, hc.Env, args[0])`, not
`exec.LookPath`: the latter resolves against the broker process's own `PATH`
and working directory, so an agent could not run a script in the directory it
is working in.

Measured with all of this in place: policy-to-policy pipelines, three-stage
pipelines, an early-exiting reader, `2>&1 |`, multiple commands in one stage,
and exit-status propagation all behave, with no process left behind over
repeated runs. This is the same pipe-ownership problem
`router.runMixedPipeline` documents today, in a new place.

### The interpreter's builtins run at the floor

`mvdan.cc/sh` dispatches its own builtins — `echo`, `printf`, `test`, `cd`,
`read`, `eval`, `source` among them — inside the interpreter, so they never
reach the exec handler and never reach a command policy. They execute with the
broker's own grants, which is the same place globbing, redirection and command
substitution already run, and the floor bounds them exactly as it bounds those.
It is not an escape, but it does mean the floor's filesystem grants govern more
than the two tiers alone suggest: a profile that expects `command_policies` to
mediate *everything* is mistaken about these.

## What is deleted

- `internal/router`, entirely. Its routing decision (`Route`, `AllowPatterns`,
  `DropRules`), host execution path (`host.go`, `RunHost`, `RunHostShell`),
  pipeline assembly (`runMixedPipeline`, `runUniformHost`,
  `runSandboxedWhole`), line parsing (`ParseLine`) and `NeedsSandbox` all have
  no caller once the broker owns the shell language. `cmd/exec.go` and
  `internal/mcptool` call the broker client directly; the `CommandRunner`
  interface and `SandboxNotRunningHint` move to `internal/broker`.
- `internal/broker/nonoexec.go`, replaced by the interpreter's exec handler.
- `cmd/allow.go` — `allowPatterns`, `dropRules`, `builtinAllowPatterns`.
- Config: `sandbox.agent.allow_commands`, `sandbox.agent.drop_commands`,
  `[sandbox.shared]`, `[sandbox.shell]`. Whatever `[sandbox.shared]` granted the
  agent moves to `[sandbox.agent]`; whatever it granted commands moves to the
  command profile.
- `internal/sandboxhost`: `ResolveShell`, `shellNetwork`,
  `ShellFilesystemGrants`, `ShellAllowDomains`, `sideOptions.workdir`,
  `sideOptions.network`, and the capability struct's `domains` field, which only
  ever fed the shell side. `Resolve` and the catalog stay: the agent profile is
  still generated.
- Nothing from `internal/safe`. **This reverses an earlier decision in this same
  document**, and the reversal is what the "Wrappers, reinstated" section below
  is about. The wrappers stay, and the name an agent types is bound to them.

`internal/shellquote` stays — `cmd/hook.go` and `internal/claude/settings.go`
still quote the agent's command line into `agent-sandbox exec -- …`.

### Wrappers, reinstated — and why the earlier decision was wrong

An earlier revision of this document deleted `internal/safe/git` and
`internal/safe/dockercompose`, on the reasoning that command control should
narrow to what nono itself can express. That reasoning rested on
`invocation_policy` being sufficient. It is not, and the evidence is specific.

**Measured 2026-09-06:** nono anchors an `argv.prefix` matcher at the command's
*first argument* (`crates/nono-cli/src/tool-sandbox/policy.rs`: `invocation_args`
is `argv.iter().skip(1)`, and the prefix arm zips from index 0). git accepts its
global options *before* the subcommand, so one leading token walks past every
prefix rule:

```
git --no-pager config alias.h "reset --hard"    # matches nothing
git h                                           # → hard reset
```

`--hard` is inside a single quoted token, so `contains` misses it too. And the
alias need not be installed through git at all: `.git/config` is under
`$WORKDIR`, the broker's own sandbox can write there, and `echo '[alias] …' >>
.git/config` is an *interpreter builtin* — no `execve`, no shim, nothing to
mediate. No argv matcher can see either route.

A wrapper can. It parses git's grammar, so global options do not hide the
subcommand; it can inspect `-c key=value`; it can distinguish a `config` read
from a write; and it can resolve the invocation's first non-global token against
the repository's configured aliases and re-check the expansion. That last one is
the only defence against the direct-write route, and it is structurally
impossible in a matcher.

**The wrapper's `-c`/`--config-env` inspection is a denylist, not a boundary,
and what actually closes the rest of it lives one layer down.**
`execCapableConfigKeys` in `internal/safe/git/rules.go` names nine config keys
that run a program as a value. git has more of them —
`diff.external`, `filter.*.clean`/`smudge`, `merge.*.driver`, `pager.*`,
`protocol.*.command`, `uploadpack.packObjectsHook`, `trailer.*.command`,
`core.gitProxy`, `gpg.<fmt>.program` among them — and every one is settable by
the same direct-write route the alias check exists to close:
`echo '[diff] external = …' >> .git/config` is a broker builtin, not an
`execve`, so neither the parser nor the shim ever sees it. What stops these
is not the wrapper at all: `realgit`'s own `exec_paths` in the command
profile names only `libexec/git-core`, so every shell-out one of those keys
would need — `sh -c`, a bare program name, `rebase -x`, `bisect run`,
`submodule foreach`, `difftool --extcmd` — fails at `execve` under nono's
Landlock execute restriction, independent of anything the parser did or did
not catch. That layer is load-bearing for all of those, and belongs to the
profile, not to this wrapper: widening `realgit`'s `exec_paths` toward
`/nix/store` — the natural reflex when some git subcommand's shell-out fails
for an unrelated reason — reopens every one of those config keys at once,
silently, because nothing in the wrapper's own rule set changed. It is not,
however, a complete backstop for `diff.external` specifically: see "Accepted
residual: `diff.external` reaches `ld-linux` directly" below, measured on
this branch, for one config key on this list that reaches execution through
this same narrow `exec_paths` regardless. The parser stops what it can see in
argv; a narrow `exec_paths` stops what it cannot; a git config key that runs
a program is the thing the second layer is holding back.

Not every rule in `internal/safe/git/rules.go` is that kind of boundary,
and it is worth being precise about which is which. `hard-reset`,
`clean-force`, `discard-changes`, `stash-destroy` and `tag-delete` are
guardrails against an accidental invocation, not defenses against a
deliberate one: `rm`, `mv` and `cp` are floor commands with write access to
`$WORKDIR` in this repository's own profile, so `rm -rf .git` needs no git
at all, and none of those five rules sits anywhere in that path. Presenting
the full rule set as one undifferentiated policy overstates that second
class; `force-push`, `branch-force-delete`, `filter-history`,
`update-ref-delete`, `gc-prune`, `bypass-hooks`, `alias-injection`,
`config-exec-injection`, `remote-tamper`, `config-write` and
`exec-path-injection` are the ones doing boundary work against what git
itself, or a config value it reads, can be made to do.

The same argument restores the compose wrapper. Its rules read the *resolved*
Compose model — host-path mounts, the Docker socket, `privileged`, host
`network`/`pid`/`ipc`, `userns_mode`, devices, dangerous capabilities, seccomp
and apparmor. None of that is in argv, and none of it was recoverable by the two
subcommand denials that replaced it.

### How a wrapper is bound to a command name

The profile binds the name the agent types to the wrapper, and gives the real
binary a second name reachable only from it. Measured 2026-09-06:

```json
"git":     { "executable": "<wrapper>",   "can_use": ["realgit"],
             "from": { "<broker>": { "sandbox": { "argv_prepend": ["safe", "git"], … } } } },
"realgit": { "executable": "<real git>",
             "from": { "git": { "sandbox": { … } } } }
```

- `argv_prepend` inserts after the synthesised `argv[0]`, so `git status --short`
  reaches the wrapper as `["safe","git","status","--short"]` — what the CLI
  already parses. `argv[0]` is the shim's own path.
- The same executable may be pinned under two command names.
- **The wrapper must never `LookPath` the tool it wraps.** nono puts its shim
  directory first on `PATH`, so `LookPath("git")` resolves back to the wrapper,
  `argv_prepend` fires again, and it recurses without bound — measured four
  levels deep before the probe was killed. The deleted `cmd/safe_git.go` used
  `LookPath`, so restoring it unchanged would ship an infinite loop.
- Resolving a *second* name instead avoids that, needs no absolute path, and
  keeps the real binary behind a shim of its own. **That second name must not
  be of the form `git-<word>`.** git treats `argv[0]`'s basename that way as an
  attempt to run `<word>` as one of its own multi-call builtins, ignoring the
  rest of argv. Measured: naming the real binary `git-real` and invoking it as
  `git-real config --get alias.h` produced `fatal: cannot handle real as a
  builtin`, silently dropping `config --get alias.h` entirely. The name used
  here and in `command-profile.json` is `realgit` (and `realdocker` for
  docker, for the same reason on general principle, though docker has no
  equivalent multi-call dispatch today).
- Reachability is enforced by nono, not by convention. Invoking `realgit`
  directly from the floor is refused: *"'realgit' is blocked because tool
  '<broker>' is not allowed to invoke it … `can_use` must include 'realgit'"*.

`docker` takes the identical shape: `docker` → the wrapper, `realdocker` → the
real binary, reachable only from it.

**The Docker socket is not filesystem-gated, at all, by any of this.**
An earlier revision of this document claimed granting the socket was a
separate, later step — "it matters only when an operator grants the Docker
socket, which the profile does not do by default." Measured 2026-09-06, that
claim is false: `docker ps` against a profile with zero filesystem grants
beyond the pinned binary and `/nix/store` — no `/var/run`, no socket path
anywhere in `fs_read`/`fs_write` — still reached the real daemon and
returned a real container list. nono does not mediate pathname AF_UNIX
sockets: its Landlock `scoped=LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET` covers
only the Linux *abstract* socket namespace, and `/var/run/docker.sock` is an
ordinary pathname socket. Gating a pathname socket needs
`linux.af_unix_mediation` plus a `filesystem.unix_socket` allowlist (see the
profile guide's `no-docker` example) — a separate, deliberate opt-in this
document never specifies and no profile in this repository configures.

The consequence is not cosmetic: **the only gate on the Docker daemon is
whether the profile makes the `docker` binary reachable at all.** Once it
is reachable — by any command, through any path, wrapped or not — the
daemon is reachable, socket grant or no socket grant, and the wrapper's
argv/compose checks become the *sole* defense rather than a second layer
sitting behind a filesystem bound. Reaching the daemon this way is
root-equivalent: the socket permits mounting `/` into a container. This is
why command-profile.json in this repository does not declare `docker` or
`realdocker` at all — wiring the wrapper in is a capability decision for an
operator to make deliberately, not a step that happens to be inert until a
second, unrelated grant is added.

One host-packaging trap measured while wiring this, recorded in Measured
Constraints below alongside the other three `executable`-pinning traps this
branch found: Nix's `docker` package installs `bin/docker` as a small stub
that re-execs `libexec/docker/docker`, the actual CLI binary. `realdocker`
must be pinned at `libexec/docker/docker` directly, or every invocation
crashes.

### Accepted residual: `diff.external` reaches `ld-linux` directly, bypassing `exec_paths`

**Measured 2026-09-06, against this repository's own `command-profile.json`,
in a real broker session** (a real `nono run` under the profile, driving the
broker over its wire protocol, not a bench test of the Go packages in
isolation): the `-c`/`--config-env` denylist and the narrow `exec_paths`
layer described above do not stop everything. Writing a single-token
`diff.external` straight into `.git/config` — the same direct-write route
`alias-injection` exists to catch, which the wrapper's parser never sees —
reaches git's own dynamic linker directly, and from there reaches arbitrary
execution of anything readable under the sandbox, with no shell involved:

1. `echo -e '[diff]\n\texternal = <path-to-ld-linux-x86-64.so.2>' >>
   .git/config` (the exact path is `realgit`'s own ELF interpreter, i.e. the
   `PT_INTERP` of the pinned git binary — readable and, it turns out,
   executable regardless of `exec_paths`).
2. `git diff --no-index <fileA> <fileB>` through the real wrapper in a live
   broker session. git's `prepare_shell_cmd` skips `$SHELL -c` for a
   config-supplied command with no shell metacharacters (a bare path has
   none), so it calls `execve` on the `diff.external` value directly.
3. **`execve` succeeds.** nono's Landlock execute restriction on `realgit`'s
   sandbox (`exec_paths: ["…/libexec/git-core"]`) does not cover this path,
   confirming the review's hypothesis: the pinned binary's own `PT_INTERP`
   is exec-allowed independent of `exec_paths`. With `<fileA>` a plain text
   file, the observed result was `ld.so`'s own load failure, relayed by git
   as `fatal: external diff died, stopping at <fileA>` — proof `execve`
   itself was not refused, but not yet proof of arbitrary execution.
4. **Arbitrary execution, confirmed.** Run the same `git diff --no-index`
   with `<fileA>` set to the real path of a *different* ELF binary readable
   under the sandbox (measured with `ripgrep`'s own binary, readable via
   `realgit`'s `fs_read` of `/nix/store`) and `<fileB>` any file with
   different content (so git does not skip the callout as a no-op diff).
   `ld-linux-x86-64.so.2`, invoked as documented in its own man page for
   direct invocation (`ld.so <path> <args>...`, `argv[0]` set to `<path>`),
   loaded and ran that second binary for real: the command's own output
   appeared on stderr —
   ```
   rg: 0000000000000000000000000000000000000000: No such file or directory (os error 2)
   rg: 100755: No such file or directory (os error 2)
   ...
   fatal: external diff died, stopping at /nix/store/…-ripgrep-15.2.0/bin/rg
   ```
   — `rg` genuinely executed, treating the hex/mode strings git appends as
   its own positional arguments (it errored on each because they are not
   real paths, not because `rg` failed to run). The invoking `agent` needs
   only **read** access to whatever binary it names this way — the write
   needed is only to `.git/config` under `$WORKDIR`, which the sandbox
   already grants for entirely legitimate reasons.

This is a real, working bypass of the Landlock execute layer this document
elsewhere calls load-bearing, not a theoretical one — it was driven through
a live broker session over the actual wire protocol, the same path an
agent's shell line takes. It is accepted as a residual for this branch, not
fixed here: closing it needs either an upstream fix (narrowing what a
pinned command's own `PT_INTERP` is allowed to do, or nono exposing a way to
deny it), or a wrapper-side rule that recognizes `diff.external` (and the
other `execCapableConfigKeys`) as refused however they reach the config —
including a route with no `execve` and no shim for the wrapper to see at
all, which is exactly the shape "Wrappers, reinstated" above says a matcher
structurally cannot close. Whoever picks this up next should not assume
`exec_paths` is a complete backstop for `execCapableConfigKeys`: it is not,
for at least this one interpreter-path route, and probably for any other
config key on that list pointed at a program instead of a shell command.

**The reach through this residual includes the network, and that reach is
unbounded.** The arbitrary execution above runs as `realgit`'s own child,
whose `network` grant is `{"allow_all": true}` — measured (see "Network"
above) unrestricted independent of the top-level ceiling, reaching a
destination even with that ceiling set to `{"block": true}`. This
repository's own session ceiling — `command-profile.json`'s top-level
`network` — is separately set to `{"network_profile": null}` (unbounded,
not a named profile) as a matter of declared policy, but the ceiling was
never what bounded this path: an attacker who reaches arbitrary execution
through this residual has unrestricted network reach regardless of what
the top-level ceiling is set to. This is the accepted consequence of the
operator's deliberate choice to manage network per-command by on/off rather
than by a narrower, domain-filtered ceiling — a choice this profile's own
mechanics make absolute for any command holding `allow_all`, not merely
a matter of degree the ceiling's value could still soften.

## Measured constraints

**The broker binary must live outside every path the sandbox can write.** nono
refuses to start otherwise:
`tool-sandbox policy command binary is replaceable through writable parent
directory`. The current `bin/agent-sandbox` sits inside the working directory
and would be refused. Installation has to move.

**Every runnable command must be named.** A program in neither tier cannot be
executed. This is the intended allowlist, and it is also the operator's
recurring cost.

**A pinned `executable` must be the program, not a multi-call host — four
measured variants, so far, on this branch alone.** Pointing an `ls` entry at
coreutils' combined binary silently produced no policy enforcement.
Version-manager shims have the same shape: a `mise` entry turns every
mise-managed tool into an attempted direct exec of `mise`. git's own
multi-call dispatch is a third variant, argv-shaped rather than
directory-shaped: naming the real git binary's second command `git-real`
(instead of `realgit`) made git itself treat `argv[0]`'s `git-<word>`
basename as an attempt to run `<word>` as one of its own builtins, silently
dropping the rest of argv (`fatal: cannot handle real as a builtin`) — see
"How a wrapper is bound to a command name" above. A fourth, host-packaging
variant: Nix's `docker` package installs `bin/docker` as a small stub that
re-execs `libexec/docker/docker`, the actual CLI binary, by absolute path.
nono's per-command Landlock rule set is built from the pinned executable's
own direct library dependencies (its ELF interpreter, its linked `.so`s, a
handful of fixed system files) — not from whatever paths it re-execs into —
so pinning an entry at `bin/docker` does not grant execute on the second,
indirectly invoked path: every invocation crashed with
`execve(".../libexec/docker/docker", ...) = -1 EACCES`, reported only as
"Command exited with code 255" with no other output. Confirmed by `strace
-f`, and confirmed specific to this one binary's packaging shape by a
control run pinning the real `git` binary the same way, which did not
crash. The fix in every case is the same: pin the actual program, whatever
directory or argv-derived indirection the package puts in front of it.

**The NixOS patch is still a prerequisite.** nono cannot start tool-sandbox on
NixOS unpatched (unfixed in 0.75.0; see the prerequisite document).

## Follow-on work

**`doctor` — done.** It verifies, before a session starts, that the command
profile exists, that `nono profile validate` passes on it, that the broker
binary is not writable through the profile's own grants, that resolving the
broker's own name through this process's `PATH` lands back on that exact
binary, and — when the profile pins the entrypoint's `executable` — that the
pinned path also names that exact binary. Each of these otherwise produces a
session that refuses every command with an error the agent cannot act on.

**`agent-sandbox ai explain` — done, with one known gap.** It states the two
tiers, names the commands in each, and — for a command bound to a `safe
<tool>` wrapper rather than to `invocation_policy` directly — renders that
wrapper's own rule messages, read live from the Go package that implements
it (`wrapperRuleMessages` in `internal/agentconfig/agentconfig.go`), plus
two behaviours a plain rule list would omit: alias expansion re-checking the
expanded form, and the refusal a git subcommand this package cannot resolve
as a real subcommand or a configured alias now gets. The known gap is
`docker`: `internal/safe/dockercompose` has no `Rules()`-shaped export — its
checks are inline literals inside `CheckModel`/`CheckCLI` — so
`wrapperRuleMessages` cannot read a rule list out of it the way it does for
git. A profile that wires the README's docker opt-in block gets a `docker`
entry in `ai explain` with no `WrapperDenials` at all: correct about the
wrapper's `blocked: <reason>` shape, silent about what the wrapper actually
refuses. Closing this needs a `Rules()`-shaped export from
`internal/safe/dockercompose` first; it is not attempted here.

**Upstream reports.** Two items, each with a reproducer: `exec_paths` missing
from the published schema, and `invocation_policy` denials bypassable by
absolute path when the caller holds a broad `exec_paths`. A third is worth
raising as a question rather than a bug — a shim keeping a duplicate of an
inherited pipe fd is ordinary fd inheritance, but it means every parent that
pipes two policy commands together has to interpose, and nothing says so.

**A worked example profile** for this repository, covering the commands the
agent actually uses here, belongs with the implementation — it is the artifact
that shows whether the enumeration cost is tolerable in practice.

**The profile cannot run the repository it ships in, and the obvious fix does
not work.** `go`'s `command_policies` entry has no `can_use` at all, so
`go test ./...` run *through the broker* (not on the bare host) fails in
`internal/safe/git`, `internal/broker`, and `internal/claude` — anything whose
tests shell out to `git` or another subprocess `go`'s own sandbox cannot
reach. This is pre-existing, not something this branch's git/docker wiring
introduced, but the wiring makes it *harder* to close rather than incidental:
adding `"git"` to `go`'s `can_use` does not make these tests pass, it changes
how they fail. `git` is now a wrapper-bound name (`argv_prepend: ["safe",
"git"]`, real binary behind `realgit`), so `exec.LookPath("git")` from inside
a sandboxed `go test` binary would resolve to nono's `git` shim, not to a
real git binary — exactly the wrapper-recursion hazard "How a wrapper is
bound to a command name" describes, except now hit by test fixtures rather
than the wrapper's own code.
`internal/safe/git/alias_test.go`'s `withFakeGitReal` fixture does precisely
this: `exec.LookPath("git")` to find a real git binary, then symlinks it to a
temporary `realgit` and prepends that directory to `PATH` so production code's
own `exec.LookPath(git.RealBinary)` finds it. Granted `can_use: ["git"]`, the
fixture's own `LookPath("git")` would find the wrapper shim instead of the
real binary and symlink *that*, so the "realgit" production code resolves
would recurse into `agent-sandbox safe git` rather than reach git at all — a
straight parser bypass of the fixture's own intent, not a fix. Separately,
even a fixture that somehow got past that would still fail: these tests
create their fixture repository under `t.TempDir()`, which lives under `/tmp`,
and `realgit`'s own `fs_write` is `["$WORKDIR"]` only — `git init` there would
be refused regardless of what invoked it. Closing this gap needs either
restructuring these specific tests to not shell out to a real git process
from inside a sandboxed `go`, or a profile mechanism this document does not
yet have for granting a test-only, non-recursive path to a real binary. Do
not "fix" it by widening `go`'s `can_use` to `git` — that is not this gap
closing, it is a parser bypass wearing this gap's clothes.
