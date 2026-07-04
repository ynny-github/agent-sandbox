// agent-sandbox/cmd/sandbox_up.go
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxlifecycle"
)

var sandboxUpCmd = &cobra.Command{
	Use:   "up",
	Short: "Start the sandbox container and wait until stopped",
	RunE:  runSandboxUp,
}

var sandboxUpDetach bool

func init() {
	sandboxUpCmd.Flags().BoolVarP(&sandboxUpDetach, "detach", "d", false, "start the sandbox and exit without stopping it")
	sandboxCmd.AddCommand(sandboxUpCmd)
}

func runSandboxUp(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	ctx := context.Background()
	executor, projectName, cleanup, err := sandboxlifecycle.NewExecutor(ctx, cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	started, err := sandboxlifecycle.Ensure(ctx, executor, sandboxlifecycle.DefaultExternalAccessConfirm(cfg))
	if err != nil {
		return err
	}

	if !started {
		fmt.Fprintf(cmd.ErrOrStderr(), "sandbox %s already running; skipping startup\n", projectName)
	}

	if sandboxUpDetach {
		if started {
			fmt.Fprintf(cmd.ErrOrStderr(), "sandbox %s is running\n", projectName)
		}
		return nil
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintln(os.Stderr, "sandbox is running. press Ctrl+C to stop.")
	<-sigCtx.Done()

	fmt.Fprintln(os.Stderr, "\nstopping sandbox...")
	downCtx, downCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer downCancel()
	return executor.Down(downCtx)
}
