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
	os.Exit(runExecCore(context.Background(), command,
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

// runExecCore sends command to execd and returns the exit code. There is no
// routing left to do: the command profile decides what may run, and execd's
// interpreter decides how the line is executed.
//
// stdio is this process's own stdin, stdout and stderr. They are passed to
// execd as descriptors, so the command writes to this process's terminal (or
// pipe, or file) itself and nothing copies its bytes through here.
func runExecCore(ctx context.Context, command string, stdio execd.Stdio) int {
	client, err := execd.NewClientFromEnv()
	if err != nil {
		// The overwhelmingly common cause is running `agent-sandbox exec`
		// outside a `claude` session, so the socket variable is unset. Print the
		// actionable hint instead of the raw dial/lookup error.
		if errors.Is(err, execd.ErrExecdUnavailable) {
			fmt.Fprintln(stdio.Err, execd.SandboxNotRunningHint)
		} else {
			fmt.Fprintf(stdio.Err, "exec daemon: %v\n", err)
		}
		return 1
	}
	// stdin is wired unconditionally rather than behind a flag. The hook
	// rewrites every Bash tool call to `agent-sandbox exec -- <command>`, so a
	// flag the hook did not pass would leave the agent unable to send input at
	// all, and a flag the hook always passed would not be a flag. The harness
	// gives this process /dev/null on fd 0, so an ordinary command sees an
	// immediate EOF, exactly as it does today.

	// Interrupting is two-stage, and the second stage is the floor.
	//
	// signal.Notify disables this process's default termination on SIGINT and
	// SIGTERM, so from here on a signal no longer kills the client by itself.
	// The first one is relayed over the wire and delivered to the command's
	// process groups, which is the point of the relay: the command sees a real
	// signal and may handle it.
	//
	// But that delivery can land on nothing. Job.Signal reaches only groups
	// that still have a live member, so a line the interpreter runs entirely
	// itself (`while :; do :; done`), the gap between two external commands,
	// and a command that traps or ignores INT all swallow the signal with no
	// diagnostic, and the request has no other deadline unless --timeout was
	// given. Without a second stage the user at a terminal would be stuck.
	//
	// So the second signal does what pressing Ctrl-C did before the relay
	// existed: it closes the connection. execd's watchConn sees the read fail,
	// cancels the request, and tears the process tree down — the same teardown
	// path as every other way a request ends. No timer is involved; the escape
	// is a second press, not a wait.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Buffered by two: os/signal drops a signal rather than blocking when the
	// channel is full, and a double Ctrl-C is exactly the case that must not be
	// dropped.
	sigs := make(chan os.Signal, 2)
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
	// forced carries the second signal back here, so the exit status can name
	// the signal the user actually sent instead of a generic failure. It is
	// written once, before the cancel that unblocks RunCommand, so the value is
	// always in the buffer by the time it is read below.
	forced := make(chan syscall.Signal, 1)
	go twoStageSignals(sigs, relay, relayDone, stdio.Err, forced, func() {
		// Hand this process its default disposition back before closing the
		// connection: a third signal then terminates the client outright, which
		// is the floor under the floor and costs nothing to keep.
		signal.Reset(syscall.SIGINT, syscall.SIGTERM)
		cancel()
	})

	code, runErr := client.RunCommand(ctx, command, stdio, execd.RunOptions{
		TimeoutMs: int(execTimeout.Milliseconds()),
		Signals:   relay,
	})
	if runErr != nil {
		select {
		case sig := <-forced:
			// The error is the connection this process closed on purpose, and
			// twoStageSignals already said so on stderr; reprinting the read
			// failure underneath it would only obscure the reason. The status
			// is the one a shell reports for a process killed by that signal,
			// which is what the pre-relay behaviour produced.
			return 128 + int(sig)
		default:
		}
		if errors.Is(runErr, execd.ErrExecdUnavailable) {
			fmt.Fprintln(stdio.Err, execd.SandboxNotRunningHint)
		} else {
			fmt.Fprintf(stdio.Err, "%v\n", runErr)
		}
		return 1
	}
	return code
}

// twoStageSignals applies the interrupt policy documented in runExecCore: the
// first signal goes on relay, to be forwarded to the command, and any signal
// after that runs escape and stops. It returns when stop is closed.
//
// It is a function rather than an inline goroutine so the policy can be driven
// directly in a test, without sending real signals to the test binary — escape
// resets this process's signal disposition, which a test must not do casually.
func twoStageSignals(sigs <-chan os.Signal, relay chan<- syscall.Signal,
	stop <-chan struct{}, stderr io.Writer, forced chan<- syscall.Signal, escape func()) {
	relayed := false
	for {
		select {
		case s := <-sigs:
			us, ok := s.(syscall.Signal)
			if !ok {
				continue
			}
			if relayed {
				fmt.Fprintf(stderr, "agent-sandbox exec: second signal (%s): "+
					"closing the connection; execd cancels the request and tears "+
					"down what it started\n", us)
				forced <- us
				escape()
				return
			}
			// Marked relayed even when the send below is dropped: the buffer is
			// full only because a first signal is already on its way, and what
			// this records is that the user has pressed once, not that a frame
			// left the process.
			relayed = true
			select {
			case relay <- us:
			default:
			}
		case <-stop:
			return
		}
	}
}
