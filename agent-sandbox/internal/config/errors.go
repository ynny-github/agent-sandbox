package config

import "errors"

var ErrMissingMCPCommandOutputDir = errors.New("missing required field: mcp.command_output_dir")
var ErrInvalidToolMode = errors.New(`invalid tool_mode (must be "mcp" or "hook")`)
var ErrDeprecatedNetworkKeys = errors.New("sandbox.network.allow_cidrs / allow_hosts are no longer supported; network reach for a command is the command profile's top-level network section")
var ErrRemovedContainerSection = errors.New("sandbox.container is no longer supported: commands now run under nono, not Docker; remove the section")
var ErrRemovedAllowExternal = errors.New("sandbox.network.allow_external is no longer supported: network reach for a command is the command profile's top-level network section")
var ErrMovedNetworkSection = errors.New("sandbox.network has moved: it only ever configured brokered commands, so network reach is now the command profile's top-level network section")
var ErrMovedEnvPassthrough = errors.New("sandbox.command.env_passthrough has moved: env passthrough for a command is the command profile's own concern now")
var ErrMovedCommandNetwork = errors.New("sandbox.command.network has moved: network reach for a command is the command profile's top-level network section")
var ErrMovedCommandHost = errors.New("sandbox.command.host has moved: a command's host access is now expressed in the command profile, which agent-sandbox does not generate")
var ErrMovedAgentHost = errors.New("sandbox.agent.host is no longer supported: agent-sandbox does not generate nono profiles; write the grants in the profile named by [agents.<name>].profile")

// ErrMovedCommandTiers fires on the two keys that used to decide, per command,
// whether it ran on the host or in the shell sandbox. That decision is now
// made entirely by the operator's command profile: a command_policies entry
// either exists for a command or it does not, and invocation_policy rules
// refuse specific invocations of it. Neither concept survives as a config key.
var ErrMovedCommandTiers = errors.New("sandbox.agent.allow_commands / drop_commands are no longer supported: which commands may run, and which invocations are refused, are now command_policies entries in the command profile")

// ErrMovedSharedToAgent fires on [sandbox.shared], which existed only because
// agent-sandbox used to generate two profiles that both needed some of the
// same grants. It generates neither now — both are files the operator writes
// in nono's own schema — so there is nothing left for a shared base to feed.
var ErrMovedSharedToAgent = errors.New("sandbox.shared is no longer supported: agent-sandbox does not generate nono profiles; write the grants in the profile named by [agents.<name>].profile")

// ErrMovedShellToProfile fires on [sandbox.shell]. The sandbox it used to
// configure — the one each brokered command ran in — no longer exists as
// something agent-sandbox generates; it is the operator-written command
// profile instead.
var ErrMovedShellToProfile = errors.New("sandbox.shell is no longer supported: the sandbox brokered commands run in is the command profile, which you write")

// ErrCommandProfileMissing fires when the nono profile the broker runs under is
// not on disk. There is deliberately no built-in fallback: agent-sandbox does
// not generate profiles, a static default cannot absorb host differences
// (/nix/store versus /usr/bin), and a profile that looks present but refuses
// every command is the worst failure mode available.
var ErrCommandProfileMissing = errors.New("command profile not found; write it, or point command_profile at it")

// ErrAgentProfileMissing fires when the nono profile the launched agent runs
// under is not on disk. There is deliberately no generated fallback:
// agent-sandbox no longer builds profiles at all, and a default that looked
// present while granting the wrong thing is the worst failure available.
var ErrAgentProfileMissing = errors.New("agent profile not found; write it, or point [agents.<name>].profile at it")

// ErrMovedAgentSectionToProfile fires on [sandbox] and everything under it.
// The section described host access agent-sandbox turned into a nono profile;
// nothing generates profiles now, so the grants live in the file named by
// [agents.<name>].profile, written in nono's own schema.
var ErrMovedAgentSectionToProfile = errors.New("[sandbox] is no longer supported: agent-sandbox does not generate nono profiles; write the grants in the profile named by [agents.<name>].profile")
