package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/internal/execd"
)

var execCmd = &cobra.Command{
	Use:   "exec -- <command>",
	Short: "Send a command to execd and stream its output",
	Long: `Send a command to execd and stream its output.

Interrupting is two-stage. The first SIGINT or SIGTERM (Ctrl-C at a terminal) is
relayed over the wire and delivered to the command's process groups, so the
command sees a real signal and may handle it. The second one closes the
connection instead: execd notices, cancels the request and tears down what it
started. The second stage exists because the first can land on nothing — a line
the interpreter runs by itself, the gap between two commands, or a command that
traps the signal — and there has to be a way out that does not depend on the
command cooperating.`,
	Args: cobra.ArbitraryArgs,
	RunE: runExec,
}

var execTimeout time.Duration

func init() {
	execCmd.Flags().DurationVar(&execTimeout, "timeout", 0,
		"give up on the command after this duration, exiting 124 (0 = no limit). "+
			"Teardown starts at the deadline but the exit can lag it by up to a "+
			"couple of seconds while output drains, and a sandboxed command's own "+
			"children may survive it. This is the only time limit; interrupting by "+
			"hand is two-stage instead — the first signal is relayed to the "+
			"command, the second closes the connection (see the description above)")
	rootCmd.AddCommand(execCmd)
}

func runExec(cmd *cobra.Command, args []string) error {
	command := commandFromArgs(cmd, args)
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("no command given after --")
	}
	os.Exit(execd.RunCLI(context.Background(), command, execTimeout,
		execd.Stdio{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}))
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
