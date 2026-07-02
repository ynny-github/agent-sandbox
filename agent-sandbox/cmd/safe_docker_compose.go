package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe/dockercompose"
)

var safeDockerComposeCmd = &cobra.Command{
	Use:   "docker-compose [docker compose args...]",
	Short: "Run docker compose only after safety validation",
	Long: `Run "docker compose" only after validating that the invocation is safe.

The wrapper resolves the project with "docker compose config" and refuses the
invocation (exit 1, running nothing) when the configuration would:
  - mount a host path outside the current working directory, or the Docker socket;
  - set privileged, host network/pid/ipc, userns_mode host, or expose devices;
  - add a dangerous Linux capability, or disable seccomp/apparmor confinement;
  - use the "run" or "exec" subcommand.

Named volumes, tmpfs mounts, and every other subcommand pass through. All
arguments are forwarded verbatim to "docker compose".`,
	Args:               cobra.ArbitraryArgs,
	DisableFlagParsing: true, // pass every token through to docker compose verbatim
	RunE:               runSafeDockerCompose,
}

func init() {
	safeCmd.AddCommand(safeDockerComposeCmd)
}

func runSafeDockerCompose(cmd *cobra.Command, args []string) error {
	// A global --help/-h describes this wrapper, not docker compose: an agent
	// wants this command's usage, so print it and stop.
	if dockercompose.WantsGlobalHelp(args) {
		return cmd.Help()
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}

	violations, err := dockercompose.Prepare(context.Background(), args, cwd, dockercompose.NewResolver())
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		for _, v := range violations {
			fmt.Fprintf(cmd.ErrOrStderr(), "refused: %s\n", v)
		}
		os.Exit(1)
	}

	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("docker not found in PATH: %w", err)
	}
	argv := append([]string{"docker", "compose"}, args...)
	if err := syscall.Exec(dockerPath, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec docker compose: %w", err)
	}
	return nil
}
