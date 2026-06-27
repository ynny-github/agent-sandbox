// Package engine routes a command to drop/host/container and executes it,
// independent of any transport (MCP, CLI). Output is written to the caller's
// io.Writers.
package engine

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/executor"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/validator"
)

// ContainerRunner executes a command inside the sandbox container.
type ContainerRunner interface {
	RunContainer(ctx context.Context, serviceName, cmd string, env []string, stdout, stderr io.Writer) (int, error)
}

// Request carries everything Run needs for a single command.
type Request struct {
	Command                 string
	AllowPatterns           []string
	DropPatterns            []string
	ContainerRunner         ContainerRunner
	ContainerEnvPassthrough []string
	Stdout                  io.Writer
	Stderr                  io.Writer
}

// Run routes req.Command and executes it.
//
// err is non-nil only on host-execution infrastructure failure. All other
// outcomes (drop, container-not-configured, validation failure, container
// runner error) write a message to req.Stderr and return err == nil with a
// non-zero exit code.
func Run(ctx context.Context, req Request) (int, error) {
	decision, matched := router.Route(req.Command, req.AllowPatterns, req.DropPatterns)
	switch decision {
	case "drop":
		fmt.Fprintf(req.Stderr, "dropped: command matches drop pattern %q\n", matched)
		return 1, nil

	case "container":
		if req.ContainerRunner == nil {
			fmt.Fprintln(req.Stderr, "no container configured: cannot route command to container")
			return 1, nil
		}
		env := resolveEnv(req.ContainerEnvPassthrough)
		exitCode, runErr := req.ContainerRunner.RunContainer(ctx, executor.SandboxServiceName, req.Command, env, req.Stdout, req.Stderr)
		if runErr != nil {
			fmt.Fprintf(req.Stderr, "container exec: %v\n", runErr)
			if exitCode == 0 {
				exitCode = 1
			}
			return exitCode, nil
		}
		return exitCode, nil

	default:
		if err := validator.Validate(req.Command); err != nil {
			fmt.Fprintf(req.Stderr, "rejected: %v\n", err)
			return 1, nil
		}
		exitCode, runErr := executor.RunHost(ctx, req.Command, req.Stdout, req.Stderr)
		if runErr != nil {
			return exitCode, fmt.Errorf("executor: %w", runErr)
		}
		return exitCode, nil
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
