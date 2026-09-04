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
[`mvdan.cc/sh/v3`](https://pkg.go.dev/mvdan.cc/sh/v3) (BSD-3, the engine behind
`shfmt`). `interp.ExecHandler` is called for every simple command that is
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

The stdin pump stays, and for the same reason the current code documents:
assigning `cmd.Stdin` makes `Wait` block on os/exec's copier, which sits in
`Read()`. Measured: without `StdinPipe`, a pipeline hangs.

The `Request` gains nothing. Redirections are handled by the interpreter inside
the broker, so they never reach the wire.

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
- `internal/safe/git` and `cmd/safe_git.go`. Its rules move to
  `invocation_policy` on git's edge, where they are stronger — the current
  wrapper is evaded by `sh -c 'git push --force'`, and the replacement cannot be
  evaded at all. Two of its sixteen rules do not translate: a refspec beginning
  with `:` or `+` (token-prefix inspection) and `-c key=value` inspection. Deny
  `-c` wholesale; accept the refspec gap.

`internal/shellquote` stays — `cmd/hook.go` and `internal/claude/settings.go`
use it to build the rewritten command line.

`internal/safe/dockercompose` is undecided; see the open items.

## Measured constraints

**A policy command cannot be the reader in a pipeline.** stdin EOF is not
propagated to a command reached through its shim, so the reader never finishes.

| pipeline | result |
| --- | --- |
| policy command → floor command (`git log \| rg x`) | works |
| floor command → floor command | works |
| policy command → shell builtin | works |
| shell builtin → policy command | works |
| policy command → policy command, reader consumes stdin | **hangs** |
| policy command → policy command, reader ignores stdin | works |

Two policy commands running concurrently without a pipe (`a & b & wait`) both
complete, so this is not serialization — it is the pipe.

The design must pick one: refuse a line that makes a policy command a pipeline
reader, buffer the upstream to completion first (losing streaming, and unsafe
against an unbounded producer), or carry a patch. Report it upstream either way.

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

**`internal/safe/dockercompose`** cannot move to `invocation_policy`: its rules
read the resolved compose YAML, which no argv matcher can see. Either keep it as
a floor command that the operator lists explicitly, or drop it — the docker
socket is not granted to commands today, so it only matters if an operator adds
`docker` as a policy command.

**Upstream reports.** Three items, each with a reproducer: `exec_paths` missing
from the published schema; `invocation_policy` denials bypassable by absolute
path when the caller holds a broad `exec_paths`; stdin EOF not propagated
through a shim.

## Open decisions

1. The pipeline-reader limitation: refuse, buffer, or patch.
2. `safe docker-compose`: keep as a floor command, or drop.
3. Whether `agent-sandbox exec` keeps its `--policy-file` snapshot. The snapshot
   froze `allow_commands` / `drop_commands` at launch; with routing gone there is
   nothing left to freeze, and the command profile is already read once by nono
   at broker startup.

## Evidence

Every *measured* claim above has a profile and a recorded result in
[2026-09-05-broker-probes.md](2026-09-05-broker-probes.md). The probes the
design rests on are `p7`, `p8`, `b1`, `b2`, `s1`, `n12`, `sh1` and `sh2`.
