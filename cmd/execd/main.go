// Command execd serves shell command lines inside a sandbox session and sends
// them to it.
//
//	execd up --profile <p> [--workdir d] start or reuse this shell's session; print its socket
//	execd down <socket>                  stop that session and remove its directory
//	execd gc [--idle d] [--dry-run]      clean up dead, orphaned and idle sessions
//	execd serve --socket <path>          serve until SIGTERM/SIGINT
//	execd exec [--timeout d] -- <cmd>    send one command line, exit with its status
//
// serve is not meant to run outside a sandbox: it executes whatever it is sent
// at its own grants. exec finds the socket through AGENT_SANDBOX_EXECD_SOCKET.
// There is no config file.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ynny-github/agent-sandbox/internal/execd"
)

const usage = `usage:
  execd up --profile <path> [--workdir <dir>]
  execd down <socket>
  execd gc [--idle <duration>] [--dry-run]
  execd serve --socket <path>
  execd exec [--timeout d] -- <command>`

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "exec":
		return execCommand(args[1:])
	case "up":
		return up(args[1:])
	case "down":
		return down(args[1:])
	case "gc":
		return gc(args[1:])
	}
	fmt.Fprintln(os.Stderr, usage)
	return 2
}

func serve(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	socket := fs.String("socket", "", "unix socket path to serve on (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *socket == "" {
		fmt.Fprintln(os.Stderr, "execd serve: --socket is required")
		return 2
	}

	sessionDeclaresDumbTerm()
	srv, err := execd.NewServer(*socket, execd.NewShellExecutor())
	if err != nil {
		fmt.Fprintf(os.Stderr, "execd serve: %v\n", err)
		return 1
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
	return 0
}

// sessionDeclaresDumbTerm sets TERM=dumb in execd's own process environment.
// No command execd runs has a terminal, but the launcher's own
// TERM=xterm-256color is inherited all the way down unless something
// overrides it, inviting colour and cursor escapes into output that is read
// as text. This makes the session's environment state what is true. A
// command line that sets TERM itself still wins over this default, because
// that assignment reaches the child through the interpreter's own
// environment, layered on top of the process environment this sets.
func sessionDeclaresDumbTerm() {
	os.Setenv("TERM", "dumb")
}

func execCommand(args []string) int {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 0,
		"give up on the command after this duration, exiting 124 (0 = no limit)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	command := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(command) == "" {
		fmt.Fprintln(os.Stderr, "execd exec: no command given after --")
		return 2
	}
	return execd.RunCLI(context.Background(), command, *timeout,
		execd.Stdio{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})
}

func up(args []string) int {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	profile := fs.String("profile", "", "nono profile the session runs under (required)")
	workdir := fs.String("workdir", "", "directory $WORKDIR expands to (default: current directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *profile == "" || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "execd up: --profile is required and takes no arguments")
		return 2
	}
	sock, err := execd.Up(*profile, *workdir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "execd up: %v\n", err)
		return 1
	}
	fmt.Println(sock)
	return 0
}

func down(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "execd down: takes exactly one socket path")
		return 2
	}
	if err := execd.Down(args[0]); err != nil {
		fmt.Fprintf(os.Stderr, "execd down: %v\n", err)
		return 1
	}
	return 0
}

func gc(args []string) int {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	idle := fs.Duration("idle", 168*time.Hour, "down sessions unused for longer than this")
	dryRun := fs.Bool("dry-run", false, "only report what would be cleaned up")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := execd.GC(*idle, *dryRun, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "execd gc: %v\n", err)
		return 1
	}
	return 0
}
