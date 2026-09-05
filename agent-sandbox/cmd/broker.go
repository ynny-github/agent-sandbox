package cmd

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
)

var brokerSocket string

// brokerCmd is the process the launcher starts inside `nono run`. It is not
// meant to be run by hand: outside that session it has no command policies
// above it, so it would execute commands with whatever the caller can reach.
var brokerCmd = &cobra.Command{
	Use:    "broker",
	Short:  "Serve commands for a sandboxed agent (started by `agent-sandbox claude`)",
	Args:   cobra.NoArgs,
	Hidden: true,
	RunE:   runBroker,
}

func init() {
	brokerCmd.Flags().StringVar(&brokerSocket, "socket", "",
		"unix socket path to serve on (required)")
	// Cobra enforces this before RunE runs, so runBroker itself can assume
	// brokerSocket is set rather than re-checking it by hand.
	if err := brokerCmd.MarkFlagRequired("socket"); err != nil {
		panic(err)
	}
	rootCmd.AddCommand(brokerCmd)
}

// startBrokerServer opens the socket and returns a server that runs each
// request through the in-process shell interpreter.
func startBrokerServer(sockPath string) (*broker.Server, error) {
	return broker.NewServer(sockPath, broker.NewShellExecutor())
}

func runBroker(cmd *cobra.Command, args []string) error {
	srv, err := startBrokerServer(brokerSocket)
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
	// can truncate that command's output. Under `agent-sandbox claude`'s own
	// teardown this is not reachable — launch.go's cleanup runs only after the
	// supervised Claude process has already exited, so no client can be
	// mid-command when the signal is sent. It is reachable if this session is
	// signaled directly (SIGINT reaching the whole foreground process group,
	// say) while a command is still running; draining in-flight connections
	// with a bounded timeout would close that gap but is a bigger change than
	// this task's mandate, so it is left as a known follow-up rather than
	// papered over here.
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
