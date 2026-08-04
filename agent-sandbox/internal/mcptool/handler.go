package mcptool

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/output"
)

// ContainerRunner is the engine's container-execution interface, re-exported so
// existing callers (serve.go, tests) keep their import.
type ContainerRunner = router.ContainerRunner

// DropRule is the router's drop rule (pattern + optional message), re-exported
// so callers can build a HandlerConfig without importing router directly.
type DropRule = router.DropRule

type HandlerConfig struct {
	OutputDir               string
	AllowPatterns           []string
	DropRules               []DropRule
	ContainerRunner         ContainerRunner
	ContainerEnvPassthrough []string
}

func HandleRunCommand(ctx context.Context, cmd string, cfg HandlerConfig) (*mcp.CallToolResult, any, error) {
	files, err := output.CreateFiles(cfg.OutputDir)
	if err != nil {
		return errorResult(fmt.Sprintf("output: %v", err)), nil, nil
	}

	exitCode, runErr := router.New(router.Config{
		AllowPatterns:           cfg.AllowPatterns,
		DropRules:               cfg.DropRules,
		ContainerRunner:         cfg.ContainerRunner,
		ContainerEnvPassthrough: cfg.ContainerEnvPassthrough,
	}).Run(ctx, cmd, files.Stdout, files.Stderr)

	closeErr := files.Close()
	if runErr != nil {
		return errorResult(runErr.Error()), nil, nil
	}
	if closeErr != nil {
		return errorResult(fmt.Sprintf("output close: %v", closeErr)), nil, nil
	}
	return BuildResponse(exitCode, files), nil, nil
}
