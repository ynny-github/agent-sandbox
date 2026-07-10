package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/policysnapshot"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
)

var execCmd = &cobra.Command{
	Use:   "exec -- <command>",
	Short: "Route and run a command through the sandbox router, streaming output",
	Args:  cobra.ArbitraryArgs,
	RunE:  runExec,
}

var execPolicyFile string

func init() {
	execCmd.Flags().StringVar(&execPolicyFile, "policy-file", "",
		"read the frozen sandbox policy from this JSON snapshot instead of the config file")
	rootCmd.AddCommand(execCmd)
}

func runExec(cmd *cobra.Command, args []string) error {
	command := commandFromArgs(cmd, args)
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("no command given after --")
	}

	cfg, err := resolveExecConfig(execPolicyFile, configPath)
	if err != nil {
		return err
	}

	os.Exit(runExecCore(context.Background(), cfg, command, os.Stdout, os.Stderr))
	return nil
}

// resolveExecConfig picks the config source: the frozen snapshot when a
// policy file is given (hook mode), otherwise the on-disk TOML. A given but
// unreadable snapshot is a hard error — exec fails closed rather than routing
// against the mutable config.
func resolveExecConfig(policyFile, configPath string) (*config.Config, error) {
	if policyFile != "" {
		cfg, err := policysnapshot.Load(policyFile)
		if err != nil {
			return nil, fmt.Errorf("policy snapshot: %w", err)
		}
		return cfg, nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("config error: %w", err)
	}
	return cfg, nil
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
	s := router.New(router.Config{
		AllowPatterns:           allowPatterns(cfg),
		DropPatterns:            cfg.Sandbox.Command.Drop,
		ContainerEnvPassthrough: cfg.Sandbox.Container.EnvPassthrough,
	})

	needs, err := s.NeedsContainer(command)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	if needs {
		runner, cleanup, rerr := newComposeContainerRunner(ctx, cfg)
		if rerr != nil {
			fmt.Fprintf(stderr, "container setup: %v\n", rerr)
			return 1
		}
		defer cleanup()
		s = router.New(router.Config{
			AllowPatterns:           allowPatterns(cfg),
			DropPatterns:            cfg.Sandbox.Command.Drop,
			ContainerEnvPassthrough: cfg.Sandbox.Container.EnvPassthrough,
			ContainerRunner:         runner,
		})
	}

	exitCode, runErr := s.Run(ctx, command, stdout, stderr)
	if runErr != nil {
		fmt.Fprintf(stderr, "%v\n", runErr)
		return 1
	}
	return exitCode
}
