---
name: growing-a-nono-profile
description: Use when deciding whether a command belongs in a nono profile's command_policies at all, adding or changing an entry there, moving a secret or a path off the shared floor, wiring a new tool so it runs through execd, or fixing a policy path that a package upgrade broke.
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

## Should this be an entry at all?

**Default: no.** An undeclared command runs at the floor and gets the session's
grants and nothing more. Keep this block as short as the properties you need allow —
not because entries are dangerous, but because every entry that grants something is a
set of paths you maintain from then on. `exec_paths` and `executable_dirs` go stale on
package upgrades, and `doctor` has to check them.

The first entry is the expensive one. A non-empty `commands` object activates Tool
Sandbox, which rebuilds the SESSION's own execute grant from PATH's trusted
directories alone — so off-PATH helper binaries stop running until `executable_dirs`
names their directory exactly. That field is not recursive, does not expand `~` or
`$HOME`, and refuses to start the session when one of its paths is missing.

Three reasons to declare a command. Nothing else is one:

| Reason | Shape | Cost |
|---|---|---|
| **Grant** — the command needs a resource you do not want on the floor (a key, a secret) | a `from.session.sandbox` edge | paths, maintained from then on |
| **Deny** — the command should not be reachable from the session at all | `"from": {"session": "deny"}` | none |
| **Restrict** — only a named parent may invoke it | `"session": "deny"` plus a `from.<parent>` edge, and `can_use` on the parent | paths, twice over |

Restricting pulls the parent in. If the parent is not declared yet, declare it — that
is part of this reason, not a separate one to justify. It does mean one property
bought two entries to maintain.

**Not reasons:**

- **To confine a command.** Confinement is the floor's job. Declaring is a trade, not
  a narrowing, so an entry written to tighten a command can widen it in a dimension
  you were not looking at — see Step 6.
- **To grant a command that can run arbitrary code** — a shell, an interpreter, a
  compiler that also runs what it built. Child sandboxes carry no AF_UNIX mediation,
  so granting one to arbitrary code opens a route to every host socket the floor
  refuses. **Denying such a command is fine**: a deny edge builds no child sandbox.
  Measured on nono 0.74.0, 3/3 each — with a `{"from": {"session": "deny"}}` entry for
  `bash` added to this repo's profile, `bash -c` is refused (exit 126, "direct session
  invocation is explicitly denied") and a floor AF_UNIX connect to a host daemon
  socket is still refused ("no matching unix_socket capability"); `nono profile
  validate` passes with no new warning.

**Before writing the entry**, check the resource is actually off the floor. If the
floor grants it, the entry buys nothing. And if one `groups.include` is what puts it
on the floor, removing that group is the cheaper change — it leaves no paths behind to
maintain.

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
7. **Prove nothing else broke** — the other declared commands, and execd itself.
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

- **Declaring a command in order to confine it.** Confinement is the floor's job; a
  declared command's edge is not clamped by the floor.
- **Declaring before checking the floor.** If the floor already grants the resource the
  entry changes nothing, and if a group is what grants it, dropping the group is the
  cheaper fix.
- **Measuring only the close.** Step 5 without step 6 is how the example above shipped.
- **Reading the explicit grants and stopping.** Groups grant more than they look like.
- **Assuming a declared command still works.** Declaring it moves it to a hand-written
  grant set; it may lose an exec path it silently depended on. Run it.
- **Pinning `executable` to a content-addressed path.** On NixOS it goes stale on every
  package update and silently disables mediation for that command.
- **Leaving the profile's comments behind.** They are the only documentation the next
  editor is guaranteed to see. A stale comment there is worse than no comment.
