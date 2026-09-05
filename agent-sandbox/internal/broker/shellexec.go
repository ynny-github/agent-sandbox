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

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

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

	// LookPathDir resolves against hc.Dir and hc.Env rather than this process's
	// own cwd and PATH: the whole premise of this executor is the caller's
	// explicitly supplied working directory, and exec.LookPath would silently
	// resolve "./script.sh" against the broker's cwd instead of the command's.
	path, err := interp.LookPathDir(hc.Dir, hc.Env, args[0])
	if err != nil {
		fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: command not found\n", args[0])
		return interp.NewExitStatus(127)
	}

	cmd := exec.CommandContext(ctx, path, args[1:]...)
	cmd.Dir = hc.Dir
	cmd.Env = execEnv(hc)

	// Never hand a child the interpreter's own pipe ends. A policy-controlled
	// command is reached through nono's shim, and the shim duplicates every fd
	// it is given and keeps the copy: pass the interpreter's pipe writer
	// straight through and it stays open after the command exits, so the next
	// stage of the pipeline never sees EOF and the pipeline hangs after
	// producing its complete output. Interposing an os/exec pipe avoids this
	// because the shim only ever duplicates that pipe's fd; the interpreter's
	// own writer is never touched by the child (or the shim) at all, so it
	// stays exactly as open or closed as the interpreter itself left it. What
	// ends a pipeline stage is the child closing its copy of the os/exec pipe
	// on exit — nothing here closes the interpreter's writer.
	drains, err := interposeOutputs(cmd, hc)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: %v\n", args[0], err)
		return interp.NewExitStatus(126)
	}

	// stdin is pumped through StdinPipe rather than assigned to cmd.Stdin: with
	// cmd.Stdin set, Wait blocks until os/exec's own copier finishes, and that
	// copier sits in Read(), which nothing interrupts while the upstream end of
	// the pipeline is still open.
	if hc.Stdin != nil {
		w, perr := cmd.StdinPipe()
		if perr != nil {
			for _, d := range drains {
				d.abort()
			}
			fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: %v\n", args[0], perr)
			return interp.NewExitStatus(126)
		}
		go func() {
			io.Copy(w, hc.Stdin)
			w.Close()
		}()
	}

	// Start, drain, then Wait — never cmd.Run, and never in any other order.
	// Wait closes the parent's read end of every StdoutPipe/StderrPipe pipe as
	// soon as the process exits ("it is incorrect to call Wait before all reads
	// from the pipe have completed"); calling it before every drain has
	// finished risks truncating output that was still in flight.
	if err := cmd.Start(); err != nil {
		for _, d := range drains {
			d.abort()
		}
		fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: %v\n", args[0], err)
		return interp.NewExitStatus(126)
	}
	for _, d := range drains {
		d.wait()
	}

	err = cmd.Wait()
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

// outputDrain copies one command's output pipe into the interpreter's writer.
type outputDrain struct {
	rc   io.ReadCloser
	done chan struct{}
}

// startDrain launches the copy goroutine and returns immediately.
func startDrain(w io.Writer, rc io.ReadCloser) outputDrain {
	d := outputDrain{rc: rc, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		// Closing the read end when the copy fails is what delivers EPIPE to a
		// writer whose reader has already exited (`seq … | head -n 1`).
		if _, err := io.Copy(w, rc); err != nil {
			rc.Close()
		}
	}()
	return d
}

// wait blocks until the copy has drained to EOF. It must run after cmd.Start
// and before cmd.Wait (see execHandler), and it does not close w: the
// interpreter's writer may still be alive after this one command — a later
// command in the same pipeline stage writes to it too (`{ echo one; echo
// two; } | cat`), or the caller holds it open across the whole request — and
// only the interpreter, which knows when the whole script is done with it,
// may end its lifetime. What ends this copy is the child closing its own copy
// of the pipe's write end when it exits.
func (d outputDrain) wait() { <-d.done }

// abort is for a command that never gets to run at all: it closes the read
// end so a copy goroutine blocked on Read (or one that would never see data)
// unblocks with EOF or an error, then waits for it to exit. Without this, a
// later pipe's setup failing after an earlier one succeeded — or cmd.Start
// itself failing — would leak a goroutine parked on Read forever, since
// nothing would ever close its end of the pipe otherwise.
func (d outputDrain) abort() {
	d.rc.Close()
	d.wait()
}

// interposeOutputs wires stdout and stderr through os/exec pipes and returns
// the drains the caller must wait on after Start and before Wait.
func interposeOutputs(cmd *exec.Cmd, hc interp.HandlerContext) ([]outputDrain, error) {
	passthrough := func(w io.Writer) bool {
		// A real file needs no interposition: closing it is not ours to do, and
		// the shim holding a duplicate of it is harmless.
		return w == nil || w == os.Stdout || w == os.Stderr
	}

	if interfaceEqual(hc.Stdout, hc.Stderr) && !passthrough(hc.Stdout) {
		// `2>&1`, or a caller that hands one writer for both streams. Wire
		// exactly one pipe and one copy goroutine: os/exec makes the same
		// guarantee when a caller assigns Stdout and Stderr the same writer
		// directly — "if Stdout and Stderr are the same writer, at most one
		// goroutine at a time will call Write" — by reusing a single fd, and
		// this preserves that guarantee by construction (one goroutine only)
		// rather than by synchronizing two independent copies into one writer.
		rc, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		cmd.Stderr = cmd.Stdout
		return []outputDrain{startDrain(hc.Stdout, rc)}, nil
	}

	var drains []outputDrain
	if passthrough(hc.Stdout) {
		cmd.Stdout = hc.Stdout
	} else {
		rc, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		drains = append(drains, startDrain(hc.Stdout, rc))
	}
	if passthrough(hc.Stderr) {
		cmd.Stderr = hc.Stderr
	} else {
		rc, err := cmd.StderrPipe()
		if err != nil {
			// The stdout drain above, if any, is already running; abort it
			// rather than leaving its goroutine parked on Read forever, since
			// this command will now never start.
			for _, d := range drains {
				d.abort()
			}
			return nil, err
		}
		drains = append(drains, startDrain(hc.Stderr, rc))
	}
	return drains, nil
}

// interfaceEqual mirrors the unexported helper os/exec itself uses to detect
// when Stdout and Stderr are the same writer. Comparing two interface values
// with == panics only when they share a dynamic type that is not comparable;
// no writer this broker deals with does, but recovering keeps that fact from
// ever being load-bearing.
func interfaceEqual(a, b any) (eq bool) {
	defer func() {
		if recover() != nil {
			eq = false
		}
	}()
	return a == b
}

// execEnv renders the interpreter's environment for the child. The broker's
// own environment is already filtered by nono before it starts, and each
// command's is decided by its entry in the command profile, so nothing is
// filtered here.
//
// env starts as an empty (non-nil) slice rather than a nil one: cmd.Env == nil
// means "inherit this process's entire environment", which is exactly what a
// sandboxed child must never do, even when the interpreter's own environment
// happens to be empty.
func execEnv(hc interp.HandlerContext) []string {
	env := []string{}
	hc.Env.Each(func(name string, v expand.Variable) bool {
		if v.IsSet() {
			env = append(env, name+"="+v.String())
		}
		return true
	})
	return env
}

// Execute satisfies Executor so the server can run a request directly. The
// server owns the transport; ShellExecutor owns the shell language.
func (e *ShellExecutor) Execute(ctx context.Context, req Request,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if strings.TrimSpace(req.Command) == "" {
		return 0, fmt.Errorf("broker: empty command")
	}
	return e.Run(ctx, req.Command, req.Cwd, stdin, stdout, stderr)
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
