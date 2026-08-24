package config

import "errors"

var ErrMissingMCPCommandOutputDir = errors.New("missing required field: mcp.command_output_dir")
var ErrInvalidToolMode = errors.New(`invalid tool_mode (must be "mcp" or "hook")`)
var ErrDeprecatedNetworkKeys = errors.New("sandbox.network.allow_cidrs / allow_hosts are no longer supported; use sandbox.command.network.allow_domains")
var ErrDropRuleMissingPattern = errors.New(`each sandbox.command.drop entry requires a non-empty "pattern"`)
var ErrAllowEnvNonoVar = errors.New(`allow_env must not contain NONO_* variables: they reconfigure the command sandbox itself`)
var ErrRemovedContainerSection = errors.New("sandbox.container is no longer supported: commands now run under nono, not Docker; remove the section")
var ErrRemovedAllowExternal = errors.New("sandbox.network.allow_external is no longer supported: use sandbox.command.network.allow_domains")
var ErrMovedNetworkSection = errors.New("sandbox.network has moved: it only ever configured brokered commands, so use [sandbox.command.network]")
var ErrMovedEnvPassthrough = errors.New("sandbox.command.env_passthrough has moved: use allow_env in [sandbox.command.host]")
