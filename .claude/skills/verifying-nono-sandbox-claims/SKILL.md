---
name: verifying-nono-sandbox-claims
description: Use when editing or auditing a nono profile's command_policies, interpreting nono why / profile validate / profile show output, adding an executable pin or exec_paths or executable_dirs or an @git: token, or when a nono sandbox behaves differently from what the profile appears to say.
---

# Verifying nono sandbox claims

## Overview

nono's diagnostics answer narrower questions than they appear to, and several of its
policy features degrade silently. A profile can validate cleanly, run without error,
and still not have the property you wrote it for.

**Core principle: a reassuring answer from `nono why` or `nono profile validate` is
not evidence. Only a probe run under the profile is.**

Everything below was measured on nono 0.74.0, Linux/Landlock. Re-measure on a
different version before trusting it.

## Quick reference: what looks true vs what is true

| Looks like | Actually |
|---|---|
| `nono why --command C --path F --op read` answers "may C read F?" | **`--path` is silently ignored when `--command` is present.** The answer is about the caller edge only. A command whose sandbox denies F still returns ALLOWED. |
| `nono why --path F --op read` answers for command C | It answers for the **session / floor**, never a command's child sandbox. There is no `nono why` route into a child's filesystem grants — use `nono profile show --json`. |
| `nono why --command C` → DENIED means C cannot run | If C declares the `sandbox` shorthand instead of `from.session`, `nono why` reports **DENIED / missing from.session** for a command that runs fine. Write `from.session` explicitly. |
| `nono profile validate` passing means the paths are good | validate **never checks path existence**. A pin at a deleted path validates. |
| A stale `executable` pin fails loudly | **Mediation for that command is silently disabled.** nono falls back to the first `PATH` match and runs it at *session* grants — an explicit `from.session: "deny"` is bypassed. Audit records `tools: active, no invocations`. |
| A missing `exec_paths` entry is an error | Silently skipped — which is why candidate lists (`/usr/lib/git-core`, `/usr/libexec/git-core`, a `/nix/store` path) work and cost nothing. |
| `executable_dirs` behaves like `exec_paths` | Opposite: a missing entry makes nono **refuse to start the session**, and it expands neither `~` nor `$HOME` nor `$XDG_*`. It also rejects group/world-writable dirs (`/tmp`). |
| `@git:` tokens resolve wherever declared | `@git:config-files` **spawns `git` host-side** and returns empty **silently** on any failure. Observed empty on non-session caller edges; a literal `~/.gitconfig` works there instead. `@git:toplevel` and friends never spawn git (filesystem walk) and do resolve. |
| A child sandbox with no `network` key is isolated | For IP, yes. For **AF_UNIX, child sandboxes carry no mediation at all** — they reach host unix sockets (e.g. a root-equivalent `/var/run/docker.sock`) that the floor refuses. `linux.af_unix_mediation` is a session-level switch; a child has no connect-side field. |
| A child's `network.allow_domain` narrows that command | It is an **on/off switch**. Domain filtering happens at nono's proxy against the **session's** top-level list. Removing a host from the session list blocks it for every child regardless of what the child declares. |
| The session/floor is a ceiling over every child | **For filesystem, no.** A file the floor refuses (`nono why --path … → DENIED / path_not_granted`) is readable by a declared command whose own edge grants it — that is how a secret can come off the floor and stay reachable by one command. **For network domains, yes** (previous row). The two behave oppositely; do not reason from one to the other. |

## How to actually check something

1. **Ask the question nono can answer.**
   - a child's grants → `nono profile show --json <profile>` (the plain form lists command names only)
   - a caller edge → `nono why --profile <p> --command <c> [--caller <x>]`, and read it as an edge answer
   - the floor's filesystem → `nono why --profile <p> --path <f> --op read|write`
   - anything else → run a probe under the profile
2. **Probe with an explicit marker.** Tool startup warnings print *before* results, so a
   bare `grep … | head -1` over mixed output catches the warning, not the answer:
   ```bash
   nono run --profile ./p.json -- bash -c 'v=$(cmd 2>&1); echo "MARK=[$v]"' 2>&1 | grep -oE 'MARK=\[[^]]*\]'
   ```
3. **Repeat.** Treat a single run as a hypothesis. Two or three agreeing runs is the floor.
4. **Probe the configuration the system actually runs in.** A component started by hand
   may lack flags its real launcher always passes; a failure there is not a fact about
   the system.
5. **Read the source when behaviour surprises you.** See Sources below. Twice in the
   work that produced this table, the source overturned a conclusion three separate
   probes had seemed to support.

## Sources

Cite one of these when you add a row; a row without a source is a rumour.

**Official documentation** — <https://nono.sh/docs/cli/features/tool-sandbox>
covers `command_policies` fields, the `@git:` dynamic tokens, credentials, intercepts
and the troubleshooting table. It is accurate on caller-edge independence ("Each child
command starts from a minimal runtime baseline. It does not inherit outer `--allow` …");
it does not document the silent-degradation behaviours in the table above.

**Source** — the crate unpacks under `~/.cargo/registry/src/*/nono-cli-<version>/`
after any `cargo install nono-cli` or `cargo fetch`, so it is readable without building.
Files that settle specific questions:

| Question | Where |
|---|---|
| which sandbox a callee actually gets | `src/tool-sandbox/platform/linux.rs`, `select_effective_policy` |
| how `@git:` tokens resolve, and why some never spawn git | `src/tool-sandbox/dynamic_providers.rs` (`run_with_path`, `find_git_toplevel`) |
| which fields a child sandbox has at all | `src/command_policy.rs`, `CommandSandboxConfig` / `CommandEdgeConfig` / `CommandFromConfig` |
| which fields the session profile has | `src/profile/mod.rs` (`unix_socket`, `unix_socket_dir`, `unix_socket_subtree` and the `_bind` forms) |
| where child paths are expanded and clamped | `src/tool-sandbox/platform/linux.rs`, `add_policy_paths` / `add_policy_unix_sockets` |

**Version history that matters:** child sandboxes gained `unix_socket_bind` in
**0.76.0** (PR #1780, "support Git fsmonitor socket via unix_socket_bind"). There is
still no connect-side field for a child as of 0.77.0 — check before assuming an upgrade
closes a socket gap.

`nono profile schema` prints the accepted fields for the installed version, and
`nono profile groups <name>` prints what a group actually grants — faster than reading
either the docs or the source when the question is "what does this name expand to".

## Common mistakes

- **Reading a tool's warning as the result.** git prints `warning: unable to access
  '~/.gitconfig'` before its output; docker's nix stub exits 255 with no message at all.
- **Generalising from one measurement.** Several of these entries contradict an
  obvious-looking first result.
- **Believing a comment over a probe** — including a comment you wrote. Profile comments
  in a repo may encode a past version's behaviour.

## Real-world impact

Every row above was found by a probe contradicting a check that had already passed.
The `executable`-pin row is the sharpest: nono's own documentation recommends pinning so
that PATH order cannot decide the boundary, and on a content-addressed store (NixOS) the
pin goes stale on every package update and silently removes the mediation it was added
to guarantee.
