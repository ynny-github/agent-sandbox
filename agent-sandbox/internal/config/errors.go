package config

import "errors"

var ErrMissingMCPCommandOutputDir = errors.New("missing required field: mcp.command_output_dir")
var ErrMissingContainerBuildContext = errors.New("missing required field: sandbox.container.build_context")
var ErrMissingContainerDockerfile = errors.New("missing required field: sandbox.container.dockerfile")
var ErrMissingContainerImage = errors.New("missing required field: sandbox.container.image")
var ErrInvalidToolMode = errors.New(`invalid tool_mode (must be "mcp" or "hook")`)
