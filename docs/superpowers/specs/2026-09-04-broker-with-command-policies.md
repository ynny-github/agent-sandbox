# The broker, with command policies

Date: 2026-09-04
Status: SUPERSEDED
Superseded by: [2026-09-05-broker-runs-commands-inside-the-sandbox.md](2026-09-05-broker-runs-commands-inside-the-sandbox.md)
Prerequisite reading: [2026-09-04-tool-sandbox-blocked-by-claude.md](2026-09-04-tool-sandbox-blocked-by-claude.md)

Three of this document's claims were measured false on 2026-09-05: that an
`invocation_policy` denial cannot be bypassed, that network can be granted per
command, and that one `bash` entry is a sound whole policy. Read the superseding
document instead; it carries forward what survived.

## The idea

Keep `internal/broker`. Put `command_policies` in the profile it already
generates for brokered commands, and hand each command to a shell inside that
sandbox instead of routing an argv.

This works because of *where* the broker runs. It lives in the launcher process,
outside the agent's sandbox, and starts a fresh `nono run` per command. Claude
Code is never in that session, so the incompatibility that blocked every other
arrangement — Claude Code aborts as soon as `command_policies` is present — does
not apply here.

## Verified

Measured 2026-09-04 with a profile shaped like the one the broker generates,
invoked exactly the way the broker invokes it
(`nono run --silent --profile <shell profile> --workdir <cwd> --`, no
`--allow-cwd`; the workdir is granted by the profile):

| Probe | Result |
| --- | --- |
| `bash -c 'ls'` | runs |
| `bash -c 'wc -c ~/.ssh/id_main'` | permission denied |
| `bash -c 'curl … https://pypi.org/simple/'` | `000` — no network |
| `bash -c 'echo x > tmp/p'` | writes |

One `bash` entry is the whole policy. Everything the shell starts inherits its
sandbox, so no command has to be enumerated to *run*; entries are only added to
give a command *more* than the floor.

## What changes

**The broker sends a command line, not an argv.** Today
`NonoExecutor.Args` appends `req.Argv` after `--`; it becomes
`bash -c "<command line>"`. The shell then does the splitting, and every
`execve` it performs meets nono's shim.

**The generated shell profile grows a `command_policies` section.** The floor
lives on the `bash` entry: the working directory read+write, the runtime paths
read, `exec_paths` so the shell can start anything, and no network. Commands
that need more get their own entries.

**`internal/router` stops deciding where anything runs.** Its parsing survives
only for whatever steering remains (see the open decision below). A parser gap
there can no longer let a command escape into a wider sandbox — which is the
class of bug `5c6dceb` was.

**What this buys, all measured on 2026-09-04:**

- Per-command sandboxes, so `allow_commands` entries stop inheriting the agent's
  grants. `go run x.go` reading `~/.ssh` was measured on the current design.
- Network off by default, granted per command.
- `git push --force` refused by `invocation_policy` at execve, so
  `sh -c 'git push --force'` and `python3 -c "os.system(…)"` are refused too.
  The current `safe git` wrapper is evaded by both.

## Open decisions

**1. `drop_commands` and the `safe` wrappers.** The agreed direction was to drop
`drop_commands` and replace refusal with substitution. With `command_policies`,
git's dangerous forms can be denied directly by `invocation_policy`, which makes
`internal/safe/git` unnecessary. Measured translation of its 16 rules:

- 13–14 translate as token denials (`--force`, `--hard`, `--no-verify`,
  `reflog expire`, `stash drop`, …) and are *stronger* than today because they
  cannot be evaded by wrapping.
- Argv matchers are token-based, so `--force-with-lease` correctly survives a
  `--force` denial (measured).
- Two rules cannot be expressed: a refspec starting with `:` or `+`
  (`git push origin +main` — token-prefix inspection), and `-c key=value`
  inspection. The recommendation was to deny `-c` wholesale (it is an arbitrary
  code-execution vector) and accept the refspec gap.
- `git config` write-only and `git restore` worktree-only are negations; leaving
  them unexpressed is better than denying the whole subcommand.

`safe docker-compose` is different and does **not** move: its rules read the
compose YAML, which no argv matcher can see. Note that the docker socket is not
granted to sandboxed commands today, so that wrapper only matters if an operator
puts docker in `allow_commands`.

**2. The NixOS patch dependency.** nono cannot start tool-sandbox on NixOS
(unfixed in 0.75.0; see the prerequisite document). Until that lands upstream,
this design needs a patched nono. `doctor` should check that the nono on PATH
can actually start tool-sandbox and fail loudly if not, rather than leaving a
session that silently refuses every command.

## Traps that will bite

These were each hit during the investigation; the prerequisite document has the
detail.

- **Every command that is a session *entrypoint* must be in
  `command_policies.commands`**, or it is refused with
  `command_not_policy_controlled`. Here the entrypoint is always `bash`, so one
  entry covers it — but that is why it must exist.
- **`exec_paths` is mandatory on the floor.** Without it a policy-controlled
  command may exec only its own binary and ELF closure — not even a shell.
- **A broad `exec_paths` defeats a `deny`.** A denied command can be reached by
  absolute path. The bypass runs in the *caller's* sandbox, so it can only lose
  privilege — safe for widening entries, unsafe for denials. Do not put a `deny`
  in `command_policies` while `exec_paths` is broad; deny via
  `invocation_policy` on an allowed edge instead, which is argv-level and not
  bypassable this way.
- **Version-manager shims collide with policy commands.** `mise` shims are all
  the `mise` binary, so a `mise` policy entry turns every mise-managed tool into
  an attempted direct exec of `mise` and breaks them all. Either leave `mise`
  out of `allow_commands` or pin `executable` to the real binary.
- **Write the entries the way the shipped examples do**: pin `executable`, grant
  the command's own binary with `fs_read_file`, grant the directories its binary
  and libraries live in, set `environment.allow_vars` explicitly. Verify with
  `nono why --profile <file> --command <name>`, which answers `ALLOWED` /
  `DENIED` with the policy path — it is the authoritative check and was the
  thing that finally located the entrypoint rule.
- `sandbox.resources` is parsed but **not enforced** by this runtime.

## Environment as of this writing

- `~/.local/bin/nono` is a **patched 0.74.0** (NixOS ELF fix) and shadows the
  system nono for every project. The 0.75.0 build with the same patch is in the
  session scratchpad and will not survive.
- The patch is at `tmp/nono-nixos-elf-fix.patch` (gitignored), against upstream
  `bda1e7e`; it applies cleanly to `v0.75.0`.
- Building it needs the rustup 1.98.0 toolchain and a nix gcc wrapper on PATH,
  plus `RUSTFLAGS` adding rpaths for glibc and libgcc — a plain `cargo build`
  fails to find `cc`, and an unpatched-rpath build then fails its own ELF
  closure.
- `~/.local/bin/asb-safe-git` and `~/.local/bin/agent-sandbox` are leftovers from
  the investigation and can be deleted.

## Reference profile

The shape that was measured working. `<cwd>` is the working directory the broker
passes; `<bash>` is the canonical bash path.

```json
{
  "meta": { "name": "agent-sandbox shell" },
  "filesystem": {
    "allow": ["<cwd>", "/tmp"],
    "read":  ["/nix/store", "/run/current-system/sw"]
  },
  "environment": { "allow_vars": ["PATH", "HOME", "USER", "LANG", "LC_*", "TERM"] },
  "command_policies": {
    "commands": {
      "bash": {
        "executable": "<bash>",
        "from": { "session": { "sandbox": {
          "fs_read_file": ["<bash>"],
          "fs_read":  ["/nix/store", "/run/current-system/sw", "<cwd>"],
          "fs_write": ["<cwd>", "/tmp"],
          "exec_paths": ["/nix/store", "/run/current-system/sw"],
          "environment": { "allow_vars": ["PATH", "HOME", "USER", "LANG", "LC_*", "TERM"] }
        }}}
      }
    }
  }
}
```

A command that needs more is added alongside, reachable from `bash`:

```json
"go": {
  "from": { "bash": { "sandbox": {
    "fs_read": ["…"], "fs_write": ["…"], "exec_paths": ["…"],
    "network": { "allow_domain": ["proxy.golang.org", "sum.golang.org"] }
  }}}
}
```

and `bash` lists it in `can_use`. Both sides are required: a caller listing a
callee whose entry has no matching `from.<caller>` is an error, and so is
listing a callee whose `from` is `deny`.
