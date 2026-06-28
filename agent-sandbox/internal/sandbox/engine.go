// Package sandbox routes a command to drop/host/container and executes it,
// independent of any transport (MCP, CLI). Output is written to the caller's
// io.Writers.
package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
)

// ContainerRunner executes an argv inside the sandbox container.
type ContainerRunner interface {
	RunContainer(ctx context.Context, argv []string, env []string, stdout, stderr io.Writer) (int, error)
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
func Run(ctx context.Context, req Request) (int, error) {
	cmd, parseErr := Parse(req.Command)
	if parseErr != nil {
		fmt.Fprintf(req.Stderr, "rejected: %v\n", parseErr)
		return 1, nil
	}
	decision, matched := Route(req.Command, req.AllowPatterns, req.DropPatterns)
	switch decision {
	case "drop":
		fmt.Fprintf(req.Stderr, "dropped: command matches drop pattern %q\n", matched)
		return 1, nil

	case "container":
		if req.ContainerRunner == nil {
			fmt.Fprintln(req.Stderr, "no container configured: cannot route command to container")
			return 1, nil
		}
		argv := cmd.Args
		if cmd.HasOperator {
			argv = []string{"bash", "-c", cmd.Raw}
		}
		env := resolveEnv(req.ContainerEnvPassthrough)
		exitCode, runErr := req.ContainerRunner.RunContainer(ctx, argv, env, req.Stdout, req.Stderr)
		if runErr != nil {
			fmt.Fprintf(req.Stderr, "container exec: %v\n", runErr)
			if exitCode == 0 {
				exitCode = 1
			}
			return exitCode, nil
		}
		return exitCode, nil

	default:
		if cmd.HasOperator {
			fmt.Fprintln(req.Stderr, "rejected: shell operator not allowed on host")
			return 1, nil
		}
		if len(cmd.Args) == 0 {
			fmt.Fprintln(req.Stderr, "rejected: empty command")
			return 1, nil
		}
		exitCode, runErr := RunHost(ctx, cmd.Args, req.Stdout, req.Stderr)
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
