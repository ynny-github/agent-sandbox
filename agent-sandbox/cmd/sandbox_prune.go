// agent-sandbox/cmd/sandbox_prune.go
package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/docker/cli/cli/command"
	cliflags "github.com/docker/cli/cli/flags"
	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/container"
)

var sandboxPruneCmd = &cobra.Command{
	Use:   "sandbox-prune",
	Short: "Remove all agent-sandbox managed containers and networks",
	RunE:  runSandboxPrune,
}

func init() {
	rootCmd.AddCommand(sandboxPruneCmd)
}

func runSandboxPrune(cmd *cobra.Command, args []string) error {
	dockerCli, err := command.NewDockerCli()
	if err != nil {
		return fmt.Errorf("docker cli error: %w", err)
	}
	if err := dockerCli.Initialize(cliflags.NewClientOptions()); err != nil {
		return fmt.Errorf("docker cli initialize: %w", err)
	}
	defer dockerCli.Client().Close()

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if _, err := dockerCli.Client().Ping(pingCtx); err != nil {
		return fmt.Errorf("docker daemon error: %w", err)
	}

	// nil project: CleanStale only uses dockerCLI, not the project.
	ex := container.NewComposeExecutor(dockerCli, nil)

	cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cleanCancel()
	result, err := ex.CleanStale(cleanCtx)
	if err != nil {
		return fmt.Errorf("prune: %w", err)
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "removed %d container(s), %d network(s)\n", result.Containers, result.Networks)
	return nil
}
