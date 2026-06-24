package mcptool

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/executor"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/output"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/validator"
)

type ContainerRunner interface {
	RunContainer(ctx context.Context, serviceName, cmd string, env []string, stdout, stderr io.Writer) (int, error)
}

type HandlerConfig struct {
	OutputDir               string
	AllowPatterns           []string
	DenyPatterns            []string
	DropPatterns            []string
	ContainerRunner         ContainerRunner
	ContainerEnvPassthrough []string
}

func HandleRunCommand(ctx context.Context, cmd string, cfg HandlerConfig) (*mcp.CallToolResult, any, error) {
	files, err := output.CreateFiles(cfg.OutputDir)
	if err != nil {
		return errorResult(fmt.Sprintf("output: %v", err)), nil, nil
	}
	closed := false
	closeFiles := func() error {
		if closed {
			return nil
		}
		closed = true
		return files.Close()
	}
	defer closeFiles()

	decision, matched := router.Route(cmd, cfg.AllowPatterns, cfg.DenyPatterns, cfg.DropPatterns)
	switch decision {
	case "drop":
		fmt.Fprintf(files.Stderr, "dropped: command matches drop pattern %q\n", matched)
		if closeErr := closeFiles(); closeErr != nil {
			return errorResult(fmt.Sprintf("output close: %v", closeErr)), nil, nil
		}
		return BuildResponse(1, files), nil, nil

	case "container":
		if cfg.ContainerRunner == nil {
			fmt.Fprintln(files.Stderr, "no container configured: cannot route command to container")
			if closeErr := closeFiles(); closeErr != nil {
				return errorResult(fmt.Sprintf("output close: %v", closeErr)), nil, nil
			}
			return BuildResponse(1, files), nil, nil
		}

		env := resolveEnv(cfg.ContainerEnvPassthrough)
		exitCode, runErr := cfg.ContainerRunner.RunContainer(ctx, executor.SandboxServiceName, cmd, env, files.Stdout, files.Stderr)
		if runErr != nil {
			fmt.Fprintf(files.Stderr, "container exec: %v\n", runErr)
		}
		closeErr := closeFiles()
		if runErr != nil {
			if closeErr != nil {
				return errorResult(fmt.Sprintf("output close: %v", closeErr)), nil, nil
			}
			if exitCode == 0 {
				exitCode = 1
			}
			return BuildResponse(exitCode, files), nil, nil
		}
		if closeErr != nil {
			return errorResult(fmt.Sprintf("output close: %v", closeErr)), nil, nil
		}
		return BuildResponse(exitCode, files), nil, nil

	default:
		if err := validator.Validate(cmd); err != nil {
			fmt.Fprintf(files.Stderr, "rejected: %v\n", err)
			if closeErr := closeFiles(); closeErr != nil {
				return errorResult(fmt.Sprintf("output close: %v", closeErr)), nil, nil
			}
			return BuildResponse(1, files), nil, nil
		}

		exitCode, runErr := executor.RunHost(ctx, cmd, files.Stdout, files.Stderr)
		closeErr := closeFiles()
		if runErr != nil {
			return errorResult(fmt.Sprintf("executor: %v", runErr)), nil, nil
		}
		if closeErr != nil {
			return errorResult(fmt.Sprintf("output close: %v", closeErr)), nil, nil
		}
		return BuildResponse(exitCode, files), nil, nil
	}
}

func resolveEnv(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}
