---
name: growing-a-nono-profile
description: Use when adding or changing a command in a nono profile's command_policies, moving a secret or a path off the shared floor, wiring a new tool so it runs under the broker, or fixing a policy path that a package upgrade broke.
---

# Growing a nono profile

## Overview

A nono profile is never finished — tools get added, secrets get noticed, upgrades
break pinned paths. Each change is the same shape, and it has one failure mode that
nobody catches by default.

**Core principle: declaring a command is a trade, not a narrowing.** The command
loses the floor's grants and gains whatever its entry declares. The second half can
be *wider* than the first in some dimension you were not thinking about.

**REQUIRED BACKGROUND:** Use `verifying-nono-sandbox-claims` for how to measure any of
this — nono's own checks answer narrower questions than they appear to.

## The loop

1. **State the property you want**, as something falsifiable. "Nothing but `ssh`
   can read `~/.ssh/id_main`" — not "tighten ssh".
2. **Measure it today.** If it already holds, stop. If it does not, you now have the
   "before" half of your evidence.
3. **Find what actually grants it.** Check `groups.include` as well as explicit
   `filesystem` entries — a group can grant far more than the lines you are reading.
   (A `~/.cargo` leak turned out to come from `rust_runtime`, not from the two
   `~/.cargo/*` lines next to it.)
4. **Make the change.**
5. **Prove the close.** The thing you named in step 1 is now refused. Repeat the probe;
   one run is a hypothesis.
6. **Prove what you opened.** See below. This is the step that gets skipped.
7. **Prove nothing else broke** — the other declared commands, and the broker itself.
8. **Write the residual down** where the next editor will read it: in the profile,
   next to the entry. Say what the change does *not* bound.

## Step 6: what did I open?

Compare the new entry against what that command had at the floor, in every dimension —
not just the one you were narrowing:

| Dimension | Ask |
|---|---|
| filesystem | Does the entry grant a path the floor did not? |
| exec | Does `exec_paths` reach binaries the floor could not run? `/nix/store`, `/tmp`, and `$WORKDIR` are the ones that matter — `$WORKDIR` is agent-writable, so exec there is write-then-execute. |
| network | Does it have a `network` key at all? Presence is on/off; the domain list is the session's, not the entry's. |
| **unix sockets** | **Always widened.** Child sandboxes carry no AF_UNIX mediation, so a declared command reaches host sockets the floor refuses — including any root-equivalent daemon socket present on the host. |
| chains | Does `can_use` (or another command's `can_use` naming this one) create a path to something the floor blocked? |

If a dimension widened, that is a finding to state, not a detail to omit. It may still
be the right trade — say so and say why.

## Worked example

Declaring `bash` and `sh`, to confine scripts to the working directory with no network:

- closed: filesystem down to `$WORKDIR` plus the toolchains; network removed entirely
- **opened: AF_UNIX reach.** At the floor a shell could not connect to
  `/var/run/docker.sock`; as declared commands they can, and that daemon is
  root-equivalent. Measured only much later, by accident.

The change was still worth making. Shipping it as "we narrowed the shells" was not.

## Sources

- **nono's own documentation** — <https://nono.sh/docs/cli/features/tool-sandbox> for the
  shape of a `command_policies` entry, the field tables, worked examples
  (`eti-git-ssh`, `eti-make-cc`) and the troubleshooting messages. Read it for *what a
  field means*; measure for *what it does on your host*.
- **The profile you are editing.** Existing entries are the best worked examples you
  have, and their comments record decisions and measurements that are not in any
  document. Read the neighbours of the entry you are adding before you write it.
- **The source**, when behaviour and documentation disagree — see
  `verifying-nono-sandbox-claims` for which file settles which question.
- `nono profile schema` for the fields the installed version accepts;
  `nono profile groups <name>` for what a group grants, which is where leaks hide.

## Common mistakes

- **Measuring only the close.** Step 5 without step 6 is how the example above shipped.
- **Reading the explicit grants and stopping.** Groups grant more than they look like.
- **Assuming a declared command still works.** Declaring it moves it to a hand-written
  grant set; it may lose an exec path it silently depended on. Run it.
- **Pinning `executable` to a content-addressed path.** On NixOS it goes stale on every
  package update and silently disables mediation for that command.
- **Leaving the profile's comments behind.** They are the only documentation the next
  editor is guaranteed to see. A stale comment there is worse than no comment.
