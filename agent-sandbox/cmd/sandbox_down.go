package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/docker/cli/cli/command"
	cliflags "github.com/docker/cli/cli/flags"
	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/container"
)

var sandboxDownCmd = &cobra.Command{
	Use:   "down",
	Short: "Stop and remove a project sandbox",
	RunE:  runSandboxDown,
}

var sandboxDownPath string

func init() {
	sandboxDownCmd.Flags().StringVar(&sandboxDownPath, "path", "", "directory whose sandbox to stop (default: current directory)")
	sandboxCmd.AddCommand(sandboxDownCmd)
}

func runSandboxDown(cmd *cobra.Command, args []string) error {
	targetDir := sandboxDownPath
	if !cmd.Flags().Changed("path") {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
		targetDir = cwd
	} else if targetDir == "" {
		return fmt.Errorf("--path must not be empty")
	}

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

	projectName := container.ProjectSandboxName(targetDir)

	downCtx, downCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer downCancel()
	if err := container.DownProject(downCtx, dockerCli, projectName); err != nil {
		return fmt.Errorf("sandbox down: %w", err)
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "sandbox %s stopped\n", projectName)
	return nil
}
