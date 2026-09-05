package broker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// execWaitDelay bounds how long Wait keeps draining a finished command's output
// pipes. It is a backstop for output inherited by a grandchild that outlives the
// command: without it, such a process turns a finished command into an
// unbounded hang.
const execWaitDelay = 5 * time.Second

// ShellExecutor runs one command line. It parses and evaluates the shell
// language in this process with mvdan.cc/sh and executes every simple command
// itself, which is what keeps each execution mediated: the broker runs inside a
// nono session whose command policies decide what may be executed at all, and
// handing the line to a real shell would hand that decision to the shell.
//
// Everything the shell language does with the filesystem — globbing, redirects,
// command substitution — is performed here and is therefore bounded by the
// broker's own sandbox, not by the agent's.
type ShellExecutor struct{}

// NewShellExecutor returns a ShellExecutor. It holds no state; one value serves
// every request.
func NewShellExecutor() *ShellExecutor { return &ShellExecutor{} }

// Run evaluates command with cwd as the working directory, streaming output to
// stdout and stderr, and returns the exit status of the last command.
//
// The error is non-nil only for a failure of Run itself. A syntax error, a
// command that does not exist, and a command that fails are all reported
// through the exit status with a message on stderr, because they are outcomes
// of the agent's line rather than faults of the broker.
func (e *ShellExecutor) Run(ctx context.Context, command, cwd string,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "command")
	if err != nil {
		fmt.Fprintf(stderr, "agent-sandbox: %v\n", err)
		return 2, nil
	}

	runner, err := interp.New(
		interp.Dir(cwd),
		interp.StdIO(stdin, stdout, stderr),
		interp.ExecHandler(execHandler),
	)
	if err != nil {
		return 0, fmt.Errorf("broker: build interpreter: %w", err)
	}

	if err := runner.Run(ctx, file); err != nil {
		if status, ok := interp.IsExitStatus(err); ok {
			return int(status), nil
		}
		fmt.Fprintf(stderr, "agent-sandbox: %v\n", err)
		return 1, nil
	}
	return 0, nil
}

// execHandler is the only place this process performs an execve. Everything the
// interpreter treats as a simple command — and nothing else — arrives here.
func execHandler(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	path, err := exec.LookPath(args[0])
	if err != nil {
		fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: command not found\n", args[0])
		return interp.NewExitStatus(127)
	}

	cmd := exec.CommandContext(ctx, path, args[1:]...)
	cmd.Dir = hc.Dir
	cmd.Env = execEnv(hc)
	cmd.WaitDelay = execWaitDelay

	// Never hand a child the interpreter's own pipe ends. A policy-controlled
	// command is reached through nono's shim, and the shim duplicates every fd
	// it is given and keeps the copy: pass the interpreter's pipe writer
	// straight through and it stays open after the command exits, so the next
	// stage of the pipeline never sees EOF and the pipeline hangs after
	// producing its complete output. Interposing an os/exec pipe means the shim
	// only ever duplicates that pipe, leaving the interpreter's end held by this
	// process alone — so closing it here is what ends the stage.
	waits, err := interposeOutputs(cmd, hc)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: %v\n", args[0], err)
		return interp.NewExitStatus(126)
	}
	defer func() {
		for _, wait := range waits {
			wait()
		}
	}()

	// stdin is pumped through StdinPipe rather than assigned to cmd.Stdin: with
	// cmd.Stdin set, Wait blocks until os/exec's own copier finishes, and that
	// copier sits in Read(), which nothing interrupts while the upstream end of
	// the pipeline is still open.
	if hc.Stdin != nil {
		w, perr := cmd.StdinPipe()
		if perr != nil {
			fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: %v\n", args[0], perr)
			return interp.NewExitStatus(126)
		}
		go func() {
			io.Copy(w, hc.Stdin)
			w.Close()
		}()
	}

	err = cmd.Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return interp.NewExitStatus(uint8(exitStatusOf(exitErr.ProcessState)))
	}
	fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: %v\n", args[0], err)
	return interp.NewExitStatus(126)
}

// interposeOutputs wires stdout and stderr through os/exec pipes, returning the
// functions that must run after the command finishes: each waits for its copy
// to drain and then closes the interpreter's end, which is what signals EOF to
// the next stage.
func interposeOutputs(cmd *exec.Cmd, hc interp.HandlerContext) ([]func(), error) {
	var waits []func()
	attach := func(w io.Writer, set func(io.Writer), pipe func() (io.ReadCloser, error)) error {
		// A real file needs no interposition: closing it is not ours to do, and
		// the shim holding a duplicate of it is harmless.
		if w == nil || w == os.Stdout || w == os.Stderr {
			set(w)
			return nil
		}
		rc, err := pipe()
		if err != nil {
			return err
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			// Closing the read end when the copy fails is what delivers EPIPE to
			// a writer whose reader has already exited (`seq … | head -n 1`).
			if _, err := io.Copy(w, rc); err != nil {
				rc.Close()
			}
		}()
		waits = append(waits, func() {
			<-done
			if c, ok := w.(io.Closer); ok {
				c.Close()
			}
		})
		return nil
	}
	if err := attach(hc.Stdout, func(w io.Writer) { cmd.Stdout = w }, cmd.StdoutPipe); err != nil {
		return nil, err
	}
	if err := attach(hc.Stderr, func(w io.Writer) { cmd.Stderr = w }, cmd.StderrPipe); err != nil {
		return nil, err
	}
	return waits, nil
}

// execEnv renders the interpreter's environment for the child. The broker's own
// environment is already filtered by nono before it starts, and each command's
// is decided by its entry in the command profile, so nothing is filtered here.
func execEnv(hc interp.HandlerContext) []string {
	var env []string
	hc.Env.Each(func(name string, v expand.Variable) bool {
		if v.IsSet() {
			env = append(env, name+"="+v.String())
		}
		return true
	})
	return env
}

// exitStatusOf maps a finished process to the status a shell user expects.
// ExitCode() is -1 for a signal death, which would surface as 255; report the
// conventional 128+signum instead.
func exitStatusOf(ps *os.ProcessState) int {
	if ps == nil {
		return 0
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}
