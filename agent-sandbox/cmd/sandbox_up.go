// agent-sandbox/cmd/sandbox_up.go
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/docker/cli/cli/command"
	cliflags "github.com/docker/cli/cli/flags"
	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/executor"
)

var sandboxUpCmd = &cobra.Command{
	Use:   "sandbox-up",
	Short: "Start the sandbox container and wait until stopped",
	RunE:  runSandboxUp,
}

var sandboxUpDetach bool

func init() {
	sandboxUpCmd.Flags().BoolVarP(&sandboxUpDetach, "detach", "d", false, "start the sandbox and exit without stopping it")
	rootCmd.AddCommand(sandboxUpCmd)
}

func runSandboxUp(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
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

	detectCtx, detectCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer detectCancel()
	externalNetwork := executor.DetectProjectNetwork(detectCtx, dockerCli, cfg.Sandbox.ExternalNetwork)

	project, err := executor.NewSandboxProject(
		os.Getpid(),
		os.Getuid(),
		os.Getgid(),
		cfg.Sandbox.BuildContext,
		cfg.Sandbox.Dockerfile,
		cfg.Sandbox.Image,
		cfg.Sandbox.AllowCIDRs,
		cfg.Sandbox.AllowHosts,
		externalNetwork,
	)
	if err != nil {
		return fmt.Errorf("sandbox project: %w", err)
	}

	composeExecutor := executor.NewComposeExecutor(dockerCli, project)

	checkCtx, checkCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer checkCancel()
	running, err := composeExecutor.IsRunning(checkCtx)
	if err != nil {
		return fmt.Errorf("sandbox status: %w", err)
	}

	if running {
		fmt.Fprintf(cmd.ErrOrStderr(), "sandbox %s already running; skipping startup\n", project.Name)
		if sandboxUpDetach {
			return nil
		}
	} else {
		upCtx, upCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer upCancel()
		if err := composeExecutor.Up(upCtx); err != nil {
			return fmt.Errorf("sandbox up: %w", err)
		}

		policyCtx, policyCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer policyCancel()
		if err := composeExecutor.ApplyNetworkPolicy(policyCtx); err != nil {
			return fmt.Errorf("network policy: %w", err)
		}

		if sandboxUpDetach {
			fmt.Fprintf(cmd.ErrOrStderr(), "sandbox %s is running\n", project.Name)
			return nil
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintln(os.Stderr, "sandbox is running. press Ctrl+C to stop.")
	<-ctx.Done()

	fmt.Fprintln(os.Stderr, "\nstopping sandbox...")
	downCtx, downCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer downCancel()
	return composeExecutor.Down(downCtx)
}
