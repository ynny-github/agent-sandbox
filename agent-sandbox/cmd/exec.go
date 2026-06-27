package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/engine"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
)

var execCmd = &cobra.Command{
	Use:   "exec -- <command>",
	Short: "Route and run a command through the sandbox router, streaming output",
	Args:  cobra.ArbitraryArgs,
	RunE:  runExec,
}

func init() {
	rootCmd.AddCommand(execCmd)
}

func runExec(cmd *cobra.Command, args []string) error {
	command := commandFromArgs(cmd, args)
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("no command given after --")
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	os.Exit(runExecCore(context.Background(), cfg, command, os.Stdout, os.Stderr))
	return nil
}

// commandFromArgs returns the command string: everything after `--` if present,
// otherwise all positional args, joined with spaces.
func commandFromArgs(cmd *cobra.Command, args []string) string {
	if dashIdx := cmd.ArgsLenAtDash(); dashIdx >= 0 {
		return strings.Join(args[dashIdx:], " ")
	}
	return strings.Join(args, " ")
}

// runExecCore routes command and runs it, writing to stdout/stderr. It returns
// the exit code. A container runner is built lazily, only when the routing
// decision is "container", so host/drop commands never touch Docker.
func runExecCore(ctx context.Context, cfg *config.Config, command string, stdout, stderr io.Writer) int {
	req := engine.Request{
		Command:                 command,
		AllowPatterns:           cfg.Sandbox.Command.Allow,
		DropPatterns:            cfg.Sandbox.Command.Drop,
		ContainerEnvPassthrough: cfg.Sandbox.Container.EnvPassthrough,
		Stdout:                  stdout,
		Stderr:                  stderr,
	}

	decision, _ := router.Route(command, cfg.Sandbox.Command.Allow, cfg.Sandbox.Command.Drop)
	if decision == "container" {
		runner, cleanup, err := newComposeContainerRunner(ctx, cfg)
		if err != nil {
			fmt.Fprintf(stderr, "container setup: %v\n", err)
			return 1
		}
		defer cleanup()
		req.ContainerRunner = runner
	}

	exitCode, runErr := engine.Run(ctx, req)
	if runErr != nil {
		fmt.Fprintf(stderr, "%v\n", runErr)
		return 1
	}
	return exitCode
}
