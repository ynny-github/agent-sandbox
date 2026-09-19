package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/execd"
)

var execCmd = &cobra.Command{
	Use:   "exec -- <command>",
	Short: "Send a command to execd and stream its output",
	Args:  cobra.ArbitraryArgs,
	RunE:  runExec,
}

var execTimeout time.Duration

func init() {
	execCmd.Flags().DurationVar(&execTimeout, "timeout", 0,
		"give up on the command after this duration, exiting 124 (0 = no limit). "+
			"Teardown starts at the deadline but the exit can lag it by up to a "+
			"couple of seconds while output drains, and a sandboxed command's own "+
			"children may survive it")
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

// runExecCore sends command to execd and streams its output, returning the
// exit code. There is no routing left to do: the command profile decides what
// may run, and execd's interpreter decides how the line is executed.
func runExecCore(ctx context.Context, command string, stdout, stderr io.Writer) int {
	client, err := execd.NewClientFromEnv()
	if err != nil {
		// The overwhelmingly common cause is running `agent-sandbox exec`
		// outside a `claude` session, so the socket variable is unset. Print the
		// actionable hint instead of the raw dial/lookup error.
		if errors.Is(err, execd.ErrExecdUnavailable) {
			fmt.Fprintln(stderr, execd.SandboxNotRunningHint)
		} else {
			fmt.Fprintf(stderr, "exec daemon: %v\n", err)
		}
		return 1
	}
	// os.Stdin is wired unconditionally rather than behind a flag. The hook
	// rewrites every Bash tool call to `agent-sandbox exec -- <command>`, so a
	// flag the hook did not pass would leave the agent unable to send input at
	// all, and a flag the hook always passed would not be a flag. The harness
	// gives this process /dev/null on fd 0, so an ordinary command sees an
	// immediate EOF, exactly as it does today.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	// relayDone stops the translator goroutine below when this call returns.
	// signal.Stop only unregisters delivery to sigs; it does not close the
	// channel, so a goroutine ranging over sigs would never see it end. In
	// production that goroutine outlives the call harmlessly because os.Exit
	// follows immediately, but runExecCore is also called directly by tests,
	// where nothing else would ever unblock it.
	relayDone := make(chan struct{})
	defer close(relayDone)
	relay := make(chan syscall.Signal, 1)
	go func() {
		for {
			select {
			case s := <-sigs:
				if us, ok := s.(syscall.Signal); ok {
					select {
					case relay <- us:
					default:
					}
				}
			case <-relayDone:
				return
			}
		}
	}()

	code, runErr := client.RunCommand(ctx, command, os.Stdin, stdout, stderr, execd.RunOptions{
		TimeoutMs: int(execTimeout.Milliseconds()),
		Signals:   relay,
	})
	if runErr != nil {
		if errors.Is(runErr, execd.ErrExecdUnavailable) {
			fmt.Fprintln(stderr, execd.SandboxNotRunningHint)
		} else {
			fmt.Fprintf(stderr, "%v\n", runErr)
		}
		return 1
	}
	return code
}
