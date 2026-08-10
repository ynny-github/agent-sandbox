package config

import "errors"

var ErrMissingMCPCommandOutputDir = errors.New("missing required field: mcp.command_output_dir")
var ErrMissingContainerBuildContext = errors.New("missing required field: sandbox.container.build_context")
var ErrMissingContainerDockerfile = errors.New("missing required field: sandbox.container.dockerfile")
var ErrMissingContainerImage = errors.New("missing required field: sandbox.container.image")
var ErrInvalidToolMode = errors.New(`invalid tool_mode (must be "mcp" or "hook")`)
var ErrDeprecatedNetworkKeys = errors.New("sandbox.network.allow_cidrs / allow_hosts are no longer supported; use allow_external (bool)")
var ErrDropRuleMissingPattern = errors.New(`each sandbox.command.drop entry requires a non-empty "pattern"`)
var ErrEnvPassthroughNonoVar = errors.New(`sandbox.command.env_passthrough must not contain NONO_* variables: they reconfigure the command sandbox itself`)
