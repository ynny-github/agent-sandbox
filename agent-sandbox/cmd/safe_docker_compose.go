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
	Use:                "docker-compose [docker compose args...]",
	Short:              "Run docker compose only after safety validation",
	Args:               cobra.ArbitraryArgs,
	DisableFlagParsing: true, // pass every token through to docker compose verbatim
	RunE:               runSafeDockerCompose,
}

func init() {
	safeCmd.AddCommand(safeDockerComposeCmd)
}

func runSafeDockerCompose(cmd *cobra.Command, args []string) error {
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
