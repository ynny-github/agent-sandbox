package config

import "errors"

var ErrMissingMCPCommandOutputDir = errors.New("missing required field: mcp.command_output_dir")
var ErrInvalidToolMode = errors.New(`invalid tool_mode (must be "mcp" or "hook")`)
var ErrDeprecatedNetworkKeys = errors.New("sandbox.network.allow_cidrs / allow_hosts are no longer supported; use allow_domains in [sandbox.shell]")
var ErrDropRuleMissingPattern = errors.New(`each sandbox.agent.drop_commands entry requires a non-empty "pattern"`)
var ErrAllowEnvNonoVar = errors.New(`allow_env must not contain NONO_* variables: they reconfigure the shell sandbox itself`)
var ErrRemovedContainerSection = errors.New("sandbox.container is no longer supported: commands now run under nono, not Docker; remove the section")
var ErrRemovedAllowExternal = errors.New("sandbox.network.allow_external is no longer supported: use allow_domains in [sandbox.shell]")
var ErrMovedNetworkSection = errors.New("sandbox.network has moved: it only ever configured brokered commands, so use allow_domains in [sandbox.shell]")
var ErrMovedEnvPassthrough = errors.New("sandbox.command.env_passthrough has moved: use allow_env in [sandbox.shell]")
var ErrMovedCommandNetwork = errors.New("sandbox.command.network has moved: use allow_domains in [sandbox.shell]")
var ErrMovedCommandHost = errors.New("sandbox.command.host has moved: the shell sandbox's host access is now [sandbox.shell]")
var ErrMovedCommandRouting = errors.New("sandbox.command has moved: command routing is now allow_commands / drop_commands in [sandbox.agent]")
var ErrMovedAgentHost = errors.New("sandbox.agent.host has moved: write its keys directly under [sandbox.agent]")
var ErrMovedSharedSection = errors.New("sandbox.host has moved: the base shared by both sandboxes is now [sandbox.shared]")
