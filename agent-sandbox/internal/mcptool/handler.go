package mcptool

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/engine"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/output"
)

// ContainerRunner is the engine's container-execution interface, re-exported so
// existing callers (serve.go, tests) keep their import.
type ContainerRunner = engine.ContainerRunner

type HandlerConfig struct {
	OutputDir               string
	AllowPatterns           []string
	DropPatterns            []string
	ContainerRunner         ContainerRunner
	ContainerEnvPassthrough []string
}

func HandleRunCommand(ctx context.Context, cmd string, cfg HandlerConfig) (*mcp.CallToolResult, any, error) {
	files, err := output.CreateFiles(cfg.OutputDir)
	if err != nil {
		return errorResult(fmt.Sprintf("output: %v", err)), nil, nil
	}

	exitCode, runErr := engine.Run(ctx, engine.Request{
		Command:                 cmd,
		AllowPatterns:           cfg.AllowPatterns,
		DropPatterns:            cfg.DropPatterns,
		ContainerRunner:         cfg.ContainerRunner,
		ContainerEnvPassthrough: cfg.ContainerEnvPassthrough,
		Stdout:                  files.Stdout,
		Stderr:                  files.Stderr,
	})

	closeErr := files.Close()
	if runErr != nil {
		return errorResult(runErr.Error()), nil, nil
	}
	if closeErr != nil {
		return errorResult(fmt.Sprintf("output close: %v", closeErr)), nil, nil
	}
	return BuildResponse(exitCode, files), nil, nil
}
