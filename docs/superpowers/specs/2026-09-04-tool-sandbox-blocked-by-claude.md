# Claude Code cannot run under nono's Tool Sandbox

Date: 2026-09-04
Investigated on `design/tool-sandbox`, which was not merged: the command broker
this document explains stays in place.
Measured with: nono 0.74.0 and 0.75.0 (both patched for the NixOS ELF bug below),
Claude Code 2.1.245, Linux 6.18.42, NixOS

## The blocker

Activating `command_policies` — by any means, with any content — stops Claude
Code from starting. It aborts about 2 ms in, before printing anything of its
own.

```
$ nono run -s --allow-cwd --profile without-policies.json -- claude --version
2.1.245 (Claude Code)

$ nono run -s --allow-cwd --profile with-policies.json -- claude --version
============================================================
Bun v1.4.0 (fdb5e06cc) Linux x64
...
Features: jsc standalone_executable claude_code
panic(main thread): abort() called
```

The two profiles differ only in the presence of a `command_policies` key. One
entry is enough; the entry does not have to name `claude`.

Claude Code is a Bun standalone executable. On NixOS it arrives as a 20 KB
nixpkgs wrapper next to a 392 MB `.claude-wrapped` that carries the embedded
runtime and payload.

## What the logs show

The two runs are identical through every enforcement step, including supervised
execution and the seccomp baseline. They diverge on one line.

```
--- works (no command_policies) --------------------------------
INFO Executing with strategy: Supervised, threading: CryptoExpected
INFO Executing (supervised): "claude" ["--version"]
INFO Using Landlock ABI V6
INFO Landlock sandbox fully enforced
INFO Seccomp TCP-only network baseline enforced
2.1.245 (Claude Code)

--- aborts (command_policies present) --------------------------
INFO Executing with strategy: Supervised, threading: CryptoExpected
INFO Executing (supervised): "claude" ["--version"]
INFO Using Landlock ABI V6
INFO Landlock sandbox fully enforced
INFO Seccomp TCP-only network baseline enforced
INFO Using Landlock ABI V6              <-- second application
INFO Landlock sandbox fully enforced    <-- the child sandbox
<Bun crash report>
```

Supervised mode is not the difference: both runs are supervised. The seccomp
baseline is not the difference: both enforce it. The only difference is that a
policy-controlled command is launched inside a *second* Landlock domain, and Bun
aborts immediately after that domain is applied.

## Ruled out

Each of these was tested and is not the cause.

| Hypothesis | How it was ruled out |
| --- | --- |
| The profile was written wrongly | Corrected against the shipped examples and verified with `nono why --command claude` → `ALLOWED`. Still aborts. |
| argv[0] is the shim path | With `allow_direct_exec_bypass` and an absolute-path invocation, argv[0] is the real binary. Still aborts. |
| Environment filtering | `environment.allow_vars: ["*"]`. Still aborts. |
| A missing filesystem grant | `$HOME` readable, `/nix/store` readable, the 392 MB payload readable (`wc -c` succeeds from inside). Still aborts. |
| A process or thread cap | `sandbox.resources` is "parsed by tool-sandbox Schema 2 but not yet enforced by this runtime", and this host has no delegated cgroup v2 subtree, so `--max-processes` cannot be enforced at all. |
| The `bun_runtime` group was missing | Added. That group grants `~/.bun` read and nothing else. Still aborts. |
| A nono bug fixed in a later release | 0.75.0 behaves identically. |

Widening permissions never helped, which is itself evidence: this is not a
missing grant.

## Why "run it with the original permissions" does not work

`allow_direct_exec_bypass` makes a policy-controlled command run with the outer
session's capabilities rather than its child sandbox's. It does not exempt the
command from *being launched as a policy-controlled command*: the second
Landlock domain is still applied, because Landlock domains only ever nest and
narrow — there is no way to re-enter the outer domain.

So the bypass changes what is inside the second domain, never that there is one.
There is no mechanism in nono to run a command outside tool-sandbox mediation
once `command_policies` is present, and that is consistent by design: an exemption
would be a hole in the guarantee.

## What this means for agent-sandbox

Tool Sandbox and Claude Code cannot share a session today.

- `nono wrap` runs Claude Code fine but refuses `command_policies`
  ("command_policies require supervised execution").
- `nono run` accepts `command_policies` but Claude Code aborts.
- Nesting is refused: a `nono run` inside a `nono wrap` (or inside another
  `nono run`) fails with *"Refusing to grant '~/.local/state/nono' because it
  overlaps protected nono state root"*. Its audit/session directory cannot be
  granted from inside a sandbox.

That closes every arrangement in which Claude Code is sandboxed by nono *and*
its commands are governed by `command_policies`.

It also explains why `internal/broker` exists. The broker runs outside the
agent's sandbox and starts a fresh `nono` per command, which is the only
remaining way to put a command under a policy nono can enforce while the agent
itself runs under a sandbox nono can host. Removing it is not currently
possible.

## Worth reporting upstream

No matching issue exists in `always-further/nono` (searched for bun, standalone,
claude, abort). The vendor's own `claude` pack carries no `command_policies`,
which is consistent with the incompatibility.

The report needs: the two-line profile diff, the log divergence above, the Bun
crash report, and the ruled-out table.

## Secondary findings, kept because they were expensive to establish

**How to write `command_policies`.** Verified against the shipped examples in
`tool-sandbox-examples/` and with `nono why --command <name>`, which is the
authoritative check — it answers `ALLOWED` / `DENIED` with the policy path.

- Every command that is invoked *as the session entrypoint* must appear under
  `command_policies.commands`, or it is refused with
  `command_not_policy_controlled`.
- Real examples always pin `executable` to a canonical path, grant the
  command's own binary via `fs_read_file`, grant the directories its binary and
  libraries live in, and set `environment.allow_vars` explicitly.
- `from.<caller>` accepts `"deny"`, a bare sandbox object, or
  `{"sandbox": …, "invocation_policy": …}`. The shipped examples use the bare
  form; both parse to the same thing.

**The session and a child are asymmetric.** A child sandbox has an explicit
Landlock execute allowlist holding only its own binary and ELF closure, widened
by `exec_paths`. The session has no such list: whatever it can read, it can
execute. So "only these tools may run" is expressible for a child and not for a
session, and a shim cannot load at all if the session cannot read the store.

**`exec_paths` is what makes a child able to run anything.** Without it a
policy-controlled command cannot even start a shell. It accepts directories or
individual files, so it doubles as the whitelist.

**A broad `exec_paths` defeats a `deny`.** If it covers the directory holding a
policy-controlled command's real binary, that command can be reached by absolute
path, bypassing its shim — measured with `git` declared and denied. The bypassed
process runs in the *caller's* sandbox, so it can only lose privilege, never
gain it. Widening entries stay safe; a `deny` does not.

**Version managers collide with policy commands.** `mise` shims are all the
`mise` binary, so making `mise` a policy command turns every mise-managed tool
into an attempted direct exec of `mise`:
`tool-sandbox direct exec bypass denied for policy-controlled command 'mise'`.
This broke `node`, and through it Claude Code, before the deeper blocker was
found.

**NixOS: nono cannot resolve ELF dependencies.** `resolve_shared_library` in
`crates/nono-cli/src/tool-sandbox/platform/linux.rs` searches only an object's
own RPATH/RUNPATH plus hardcoded FHS defaults, and search directories are not
inherited down the closure. nixpkgs' `gcc-lib/libgcc_s.so.1` is a symlink whose
canonical target has an empty RUNPATH, so its `libc.so.6` is unresolvable and
tool-sandbox refuses to start. Still unfixed in 0.75.0. A patch that adds the
entry binary's PT_INTERP directory to the search path — a no-op on FHS — is at
`tmp/nono-nixos-elf-fix.patch`; every measurement above was taken with it
applied.
