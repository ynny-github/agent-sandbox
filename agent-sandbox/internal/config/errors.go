package config

import "errors"

var ErrMissingOutputDir = errors.New("missing required field: server.output_dir")
var ErrMissingSandboxBuildContext = errors.New("missing required field: sandbox.build_context")
var ErrMissingSandboxDockerfile = errors.New("missing required field: sandbox.dockerfile")
var ErrMissingSandboxImage = errors.New("missing required field: sandbox.image")
var ErrInvalidNonoSubcommand = errors.New("nono.subcommand must be \"run\" or \"wrap\"")
