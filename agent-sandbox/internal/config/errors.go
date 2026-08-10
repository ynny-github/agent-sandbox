package config

import "errors"

var ErrMissingMCPCommandOutputDir = errors.New("missing required field: mcp.command_output_dir")
var ErrInvalidToolMode = errors.New(`invalid tool_mode (must be "mcp" or "hook")`)
var ErrDeprecatedNetworkKeys = errors.New("sandbox.network.allow_cidrs / allow_hosts are no longer supported; use allow_domains")
var ErrDropRuleMissingPattern = errors.New(`each sandbox.command.drop entry requires a non-empty "pattern"`)
var ErrEnvPassthroughNonoVar = errors.New(`sandbox.command.env_passthrough must not contain NONO_* variables: they reconfigure the command sandbox itself`)
var ErrRemovedContainerSection = errors.New("sandbox.container is no longer supported: commands now run under nono, not Docker; remove the section")
var ErrRemovedAllowExternal = errors.New("sandbox.network.allow_external is no longer supported: use sandbox.network.allow_domains")
