package cmd

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/execd"
)

var execdSocket string

// execdCmd is the process the launcher starts inside `nono run`. It is not
// meant to be run by hand: outside that session it has no command policies
// above it, so it would execute commands with whatever the caller can reach.
var execdCmd = &cobra.Command{
	Use:    "execd",
	Short:  "Serve commands for a sandboxed agent (started by `agent-sandbox claude`)",
	Args:   cobra.NoArgs,
	Hidden: true,
	RunE:   runExecd,
}

func init() {
	execdCmd.Flags().StringVar(&execdSocket, "socket", "",
		"unix socket path to serve on (required)")
	// Cobra enforces this before RunE runs, so runExecd itself can assume
	// execdSocket is set rather than re-checking it by hand.
	if err := execdCmd.MarkFlagRequired("socket"); err != nil {
		panic(err)
	}
	rootCmd.AddCommand(execdCmd)
}

// startExecdServer opens the socket and returns a server that runs each
// request through the in-process shell interpreter.
func startExecdServer(sockPath string) (*execd.Server, error) {
	return execd.NewServer(sockPath, execd.NewShellExecutor())
}

func runExecd(cmd *cobra.Command, args []string) error {
	srv, err := startExecdServer(execdSocket)
	if err != nil {
		return err
	}
	defer srv.Close()

	// The launcher tears the session down by sending this process SIGTERM;
	// handling the signal is what lets the socket be removed instead of left
	// behind. signal.Stop pairs with Notify so the channel is deregistered
	// once Serve returns, rather than leaving the runtime holding a reference
	// to it: harmless when this call is the last thing the process does before
	// exiting, but Notify without a matching Stop is the kind of imbalance that
	// bites the day this command stops being the last thing in main.
	//
	// A command still in flight when the signal arrives does not get to finish:
	// Close only stops accepting new connections, it does not wait for
	// in-flight handle goroutines, so this process exiting immediately after
	// can truncate that command's output. This is reachable more often than it
	// sounds: this process is started with no SysProcAttr.Setpgid, so it sits
	// in the launcher's own foreground process group, and an ordinary terminal
	// Ctrl-C reaches it directly, not only launch.go's own SIGTERM on teardown.
	// What makes that acceptable rather than a bug to fix here is harm, not
	// rarity: a Ctrl-C also aborts the agent in the same instant, so the
	// truncated output belongs to a command the user just cancelled, not one
	// they were waiting on. Draining in-flight connections with a bounded
	// timeout would close the gap properly but is a bigger change than this
	// task's mandate, so it is left as a known follow-up rather than papered
	// over here.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		<-sigs
		srv.Close()
	}()

	srv.Serve()
	return nil
}
