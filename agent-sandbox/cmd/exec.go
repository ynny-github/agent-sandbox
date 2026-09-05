package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
)

var execCmd = &cobra.Command{
	Use:   "exec -- <command>",
	Short: "Send a command to the broker and stream its output",
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
	os.Exit(runExecCore(context.Background(), command, os.Stdout, os.Stderr))
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

// runExecCore sends command to the broker and streams its output, returning the
// exit code. There is no routing left to do: the command profile decides what
// may run, and the broker's interpreter decides how the line is executed.
func runExecCore(ctx context.Context, command string, stdout, stderr io.Writer) int {
	client, err := broker.NewClientFromEnv()
	if err != nil {
		// The overwhelmingly common cause is running `agent-sandbox exec`
		// outside a `claude` session, so the socket variable is unset. Print the
		// actionable hint instead of the raw dial/lookup error.
		if errors.Is(err, broker.ErrBrokerUnavailable) {
			fmt.Fprintln(stderr, broker.SandboxNotRunningHint)
		} else {
			fmt.Fprintf(stderr, "command broker: %v\n", err)
		}
		return 1
	}
	code, runErr := client.RunCommand(ctx, command, nil, stdout, stderr)
	if runErr != nil {
		if errors.Is(runErr, broker.ErrBrokerUnavailable) {
			fmt.Fprintln(stderr, broker.SandboxNotRunningHint)
		} else {
			fmt.Fprintf(stderr, "%v\n", runErr)
		}
		return 1
	}
	return code
}
