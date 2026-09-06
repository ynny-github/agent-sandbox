# The broker runs commands, from inside the sandbox

Date: 2026-09-05
Status: agreed design, not yet implemented
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
reached `github.com`. Only `allow_all` (on/off) has any effect on a child, and
domain filtering happens at the session layer, through `network_profile` /
`allow_domain` in the profile's top-level `network` section.

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

**Network.** The top-level `network` section is the ceiling and the only place
where domain filtering works. A command entry's `network` is on/off:
`{"allow_all": true}` lets that command reach the ceiling's allowed domains,
and omitting the key gives it no network at all (both measured, including that
a command with `allow_all` still could not reach a domain outside the session's
`developer` profile).

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
  "network": { "network_profile": "developer" },

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
real binary, reachable only from it. It matters only when an operator grants the
Docker socket, which the profile does not do by default — but the shape is
there so that granting it does not also mean giving up the compose checks.
One host-packaging trap measured while wiring this: Nix's `docker` package
installs `bin/docker` as a small stub that re-execs `libexec/docker/docker`,
the actual CLI binary — the same "multi-call host" shape this document's
Measured Constraints section already warns about for `ls` and `mise`. nono's
per-command Landlock rule set is built from the pinned executable's own
direct library dependencies, so pinning `realdocker` at the stub does not
grant execute on the second, indirectly invoked path: every invocation
crashed with `execve(...) = -1 EACCES`, reported as "Command exited with code
255" with no other output. `realdocker` must be pinned at
`libexec/docker/docker` directly.

## Measured constraints

**The broker binary must live outside every path the sandbox can write.** nono
refuses to start otherwise:
`tool-sandbox policy command binary is replaceable through writable parent
directory`. The current `bin/agent-sandbox` sits inside the working directory
and would be refused. Installation has to move.

**Every runnable command must be named.** A program in neither tier cannot be
executed. This is the intended allowlist, and it is also the operator's
recurring cost.

**A pinned `executable` must be the program, not a multi-call host.** Pointing
an `ls` entry at coreutils' combined binary silently produced no policy
enforcement. Version-manager shims have the same shape: a `mise` entry turns
every mise-managed tool into an attempted direct exec of `mise`.

**The NixOS patch is still a prerequisite.** nono cannot start tool-sandbox on
NixOS unpatched (unfixed in 0.75.0; see the prerequisite document).

## Follow-on work

**`doctor`** should verify, before a session starts, that the command profile
exists, that `nono profile validate` passes on it, that the nono on PATH can
actually start tool-sandbox, and that the broker binary is not writable through
the profile's own grants. Each of these otherwise produces a session that
refuses every command with an error the agent cannot act on.

**`agent-sandbox ai explain`** currently describes `allow_commands`,
`drop_commands` and the resolved shell grants, all of which are gone. It should
instead state the two tiers, name the commands in each, and reproduce the
`invocation_policy` denials with their reasons — the agent needs to know a
refusal is a policy, not a bug.

**Upstream reports.** Two items, each with a reproducer: `exec_paths` missing
from the published schema, and `invocation_policy` denials bypassable by
absolute path when the caller holds a broad `exec_paths`. A third is worth
raising as a question rather than a bug — a shim keeping a duplicate of an
inherited pipe fd is ordinary fd inheritance, but it means every parent that
pipes two policy commands together has to interpose, and nothing says so.

**A worked example profile** for this repository, covering the commands the
agent actually uses here, belongs with the implementation — it is the artifact
that shows whether the enumeration cost is tolerable in practice.
