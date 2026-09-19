package execd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// The statuses execd reports for outcomes that are not the command's own.
// They are collected here because a caller reads them as one vocabulary:
//
//	the command's own   the last command's exit status
//	128 + signum        died from a signal
//	127                 command not found
//	126                 could not be started (exec or wiring failure)
//	124                 timed out
//	2                   syntax error
//	1                   the interpreter failed for a reason that is not an exit status
//
// An error frame is never used for any of these: it reports a fault of execd
// itself, and a command that fails is not a fault of execd.
const (
	ExitSyntaxError = 2
	// ExitTimeout is the status a timed-out request reports. It follows GNU
	// timeout(1), so a caller that already knows that convention reads it
	// right.
	//
	// Two ways it is weaker than GNU timeout(1), both measured on nono 0.74.0,
	// 2026-09-20. It does not return AT the timeout: cancellation tears the
	// process groups down then, but the request still pays out whatever of
	// DrainGrace the drains have left, so it returns at the timeout plus up to
	// that grace. Measured with `git log --oneline | git cat-file
	// --batch-check` at a 1000ms timeout, 5 runs (probe log set bt1-5): the 4
	// that hit a stall reported 124 at 2017-2024ms, and the 1 that did not
	// finished 0 at 18ms.
	// And what it kills is what execd started: a policy-controlled command's
	// own children survive it — `sh -c 'sleep 60'` at a 3000ms timeout
	// reported 124 at 5004-5005ms and left the sleep running, 3/3 (log set
	// ss1-3). See Job and DrainGrace for both.
	ExitTimeout     = 124
	ExitCannotStart = 126
	ExitNotFound    = 127
)

// ShellExecutor runs one command line. It parses and evaluates the shell
// language in this process with mvdan.cc/sh and executes every simple command
// itself, which is what keeps each execution mediated: execd runs inside a
// nono session whose command policies decide what may be executed at all, and
// handing the line to a real shell would hand that decision to the shell.
//
// Everything the shell language does with the filesystem — globbing, redirects,
// command substitution — is performed here and is therefore bounded by
// execd's own sandbox, not by the agent's.
type ShellExecutor struct {
	// pacer is shared by every request this executor serves, which is what
	// makes it process-wide: cmd/execd.go builds exactly one ShellExecutor
	// and hands it to the server, whose Serve spawns a goroutine per
	// connection. The budget being paced belongs to the one nono session all
	// of those goroutines launch into, so a per-request limiter would not
	// bound anything.
	pacer *launchPacer
}

// NewShellExecutor returns a ShellExecutor. Its only state is the launch pacer
// below, which is deliberately per-executor rather than per-request: see the
// pacer field.
func NewShellExecutor() *ShellExecutor { return &ShellExecutor{pacer: &launchPacer{}} }

// Run evaluates command with cwd as the working directory, streaming output to
// stdout and stderr, and returns the exit status of the last command.
//
// The error is non-nil only for a failure of Run itself. A syntax error, a
// command that does not exist, and a command that fails are all reported
// through the exit status with a message on stderr, because they are outcomes
// of the agent's line rather than faults of execd itself.
func (e *ShellExecutor) Run(ctx context.Context, command, cwd string,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "command")
	if err != nil {
		fmt.Fprintf(stderr, "agent-sandbox: %v\n", err)
		return ExitSyntaxError, nil
	}

	runner, err := interp.New(
		interp.Dir(cwd),
		interp.StdIO(stdin, stdout, stderr),
		interp.ExecHandler(e.execHandler),
	)
	if err != nil {
		return 0, fmt.Errorf("execd: build interpreter: %w", err)
	}

	// One Job per request: every command the line starts, across every
	// pipeline stage, belongs to it. Ownership follows creation — whoever
	// makes the Job tears it down — so when the caller already put one on the
	// context (ExecuteWithSignals does, because it has to relay signals into
	// it) Run borrows it and leaves the teardown there. A caller that builds
	// an interpreter by hand, as tests do, gets a Job of Run's own, torn down
	// on every path out of Run — normal completion, a parse or exec error, or
	// the context being cancelled — so nothing execd started for that caller
	// outlives Run either. Not "nothing outlives Run": teardown is killpg over
	// the groups this side created, and a policy-controlled command's own
	// children sit inside nono's child sandbox, outside them. Measured; see
	// Job.
	job := jobFrom(ctx)
	if job == nil {
		job = NewJob()
		defer job.Terminate()
		ctx = withJob(ctx, job)
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
func (e *ShellExecutor) execHandler(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	// LookPathDir resolves against hc.Dir and hc.Env rather than this process's
	// own cwd and PATH: the whole premise of this executor is the caller's
	// explicitly supplied working directory, and exec.LookPath would silently
	// resolve "./script.sh" against execd's cwd instead of the command's.
	path, err := interp.LookPathDir(hc.Dir, hc.Env, args[0])
	if err != nil {
		fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: command not found\n", args[0])
		return interp.NewExitStatus(ExitNotFound)
	}

	// Pace only the policy-controlled commands: they are the ones whose
	// launch spends the session budget launchPacer rations, and delaying a
	// floor command would buy nothing.
	if isPolicyControlledPath(path) && !e.pacer.wait(ctx) {
		return interp.NewExitStatus(ExitCannotStart)
	}

	// exec.Command, not exec.CommandContext: cancellation is the Job's
	// responsibility now (see the watcher below), and CommandContext's own
	// kill would race Job.Terminate's killpg, two mechanisms killing the same
	// process by different means.
	cmd := exec.Command(path, args[1:]...)
	cmd.Dir = hc.Dir
	cmd.Env = execEnv(hc)

	// Never hand a child the interpreter's own pipe ends. A policy-controlled
	// command is reached through nono's shim, and the shim duplicates every fd
	// it is given and keeps the copy: pass the interpreter's pipe writer
	// straight through and it stays open after the command exits, so the next
	// stage of the pipeline never sees EOF and the pipeline hangs after
	// producing its complete output. Interposing a pipe this side owns confines
	// that leak instead of curing it. The interpreter's own writer is never
	// touched by the child or the shim, so it stays exactly as open or closed
	// as the interpreter left it — but nono retains a duplicate of the
	// interposed pipe's write end too, measured, so the leak lands on an fd
	// this side can close on a deadline rather than on one it must never touch.
	//
	// So a pipeline stage ends by one of three closes, not two: the child's on
	// exit, this side's in closeJobEnds, and — when a duplicate outlives both —
	// waitDrains force-closing the read end at DrainGrace, which is what ended
	// the stage in 14 of the 21 measured runs of a policy-to-policy pipe (probe
	// log set runB1-6, full, full2, bb1-10 plus three transcript-only runs).
	// See DrainGrace for the rest of the numbers. A real file needs none of
	// this and goes straight to the child: see wiring.
	w, err := wireOutputs(cmd, hc)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: %v\n", args[0], err)
		return interp.NewExitStatus(ExitCannotStart)
	}

	// stdin is an *os.File either way — the redirect's own file, or the read
	// end of a pipe this side owns and pumps hc.Stdin into. That is what keeps
	// Wait from blocking: with an io.Reader in cmd.Stdin, os/exec runs its own
	// copier and Wait waits for it, and that copier sits in Read(), which
	// nothing interrupts while the upstream end of the pipeline is still open.
	if err := w.wireStdin(cmd, hc); err != nil {
		w.abort()
		fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: %v\n", args[0], err)
		return interp.NewExitStatus(ExitCannotStart)
	}

	// Start, then close this side's copies of the fds the child was given, then
	// Wait, then drain. No ordering rule is being obeyed here: every fd belongs
	// to this side rather than to os/exec, so Wait has nothing to close and
	// cannot truncate output still in flight — the hazard the old
	// "Start, then drain, then Wait, never any other order" rule existed to
	// avoid is unreachable rather than avoided. The one thing that does matter
	// is closeJobEnds: while this side still holds a write end, the drain's EOF
	// can never arrive.
	job := jobFrom(ctx)
	if job == nil {
		job = NewJob() // a bare interpreter, in a test: the command still gets its own group
	}
	if err := job.Start(cmd); err != nil {
		w.abort()
		fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: %v\n", args[0], err)
		return interp.NewExitStatus(ExitCannotStart)
	}
	// Right after Start, and before anything that waits: exec.Cmd has now
	// duplicated these into the child, and while this side still holds a write
	// end the drain below can never see EOF.
	w.closeJobEnds()

	// Tear the whole group down the instant the context is cancelled, not
	// just when the interpreter next checks it between statements — this is
	// what makes a cancelled request return promptly even while this one
	// command is still running. stop stops the watcher once execHandler no
	// longer needs it — but only on the path where nothing has cancelled:
	// once the select below has taken the ctx.Done() branch, it is committed
	// to job.Terminate(), and closing stop afterwards does nothing. So the
	// goroutine can outlive execHandler, and possibly Run, by up to
	// TerminateGrace: that is how long Terminate may poll before it returns.
	// That is bounded and harmless — Terminate touches only the Job's own
	// state, nothing execHandler or Run still hold — but it is a real bound,
	// not "never outlives the command it watches".
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			job.Terminate()
		case <-stop:
		}
	}()

	err = cmd.Wait()

	if truncated := w.waitDrains(DrainGrace); truncated {
		// Not an exit code: inventing one would hide the command's real result.
		//
		// Nor, for a non-final pipeline stage, does it reach the caller at all:
		// mvdan.cc/sh drops such a stage's stderr entirely (measured, and
		// reproduced with the library's own default exec handler). That is the
		// common case for this particular note, since the stall that produces it
		// needs a downstream stage to be stalled by. See DrainGrace.
		fmt.Fprintf(hc.Stderr,
			"agent-sandbox: output truncated: %s left a process holding its output after %s\n",
			args[0], DrainGrace)
	}

	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return interp.NewExitStatus(uint8(exitStatusOf(exitErr.ProcessState)))
	}
	fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: %v\n", args[0], err)
	return interp.NewExitStatus(ExitCannotStart)
}

// execEnv renders the interpreter's environment for the child. execd's
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
// server owns the transport; ShellExecutor owns the shell language. It is
// ExecuteWithSignals with no signal channel, so the contract — the request
// shape it refuses, the timeout it honours, the Job it tears down — is that
// function's and executeWithJob's, and is documented there.
func (e *ShellExecutor) Execute(ctx context.Context, req Request,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	return e.ExecuteWithSignals(ctx, req, stdin, stdout, stderr, nil)
}

// ExecuteWithSignals is Execute plus in-flight signal delivery: every signal
// read from sigs is delivered to the request's process groups — not to one pid,
// because a pipeline has a group per stage and a command's own children are in
// its group. A nil channel means no signals, which is exactly Execute.
//
// The relay goroutine cannot outlive the request: its only exits are the stop
// channel and the caller closing sigs, and the deferred wait below blocks until
// it has actually returned. Because defers run last-registered-first, that wait
// completes before this function's own job.Terminate, so no relayed signal is
// ever in flight during or after the teardown that ends the request.
//
// That is the only teardown this ordering covers. execHandler starts a watcher
// that calls job.Terminate when the context is cancelled, and that watcher runs
// independently of this function and may outlive Run by up to TerminateGrace,
// so a relayed signal can run concurrently with that teardown. It is safe
// rather than ordered: Job guards its own state with a mutex, both paths reach
// the same groups through the same liveness probe, and the worst outcome is a
// SIGTERM landing beside the one Terminate is already sending. It does not
// widen the pid-recycling window liveGroups documents — the relay stops
// existing before this request stops tracking its groups.
func (e *ShellExecutor) ExecuteWithSignals(ctx context.Context, req Request,
	stdin io.Reader, stdout, stderr io.Writer, sigs <-chan syscall.Signal) (int, error) {
	// The Job is created here rather than in Run because the relay has to
	// have something to relay into before the first command exists: a signal
	// that arrives early finds a Job with no groups yet and delivers nothing,
	// which is the correct outcome for a command that has not started.
	job := NewJob()
	defer job.Terminate()

	if sigs != nil {
		stop := make(chan struct{})
		done := make(chan struct{})
		defer func() {
			close(stop)
			<-done
		}()
		go func() {
			defer close(done)
			for {
				select {
				case sig, ok := <-sigs:
					if !ok {
						return
					}
					job.Signal(sig)
				case <-stop:
					return
				}
			}
		}()
	}

	return e.executeWithJob(withJob(ctx, job), req, stdin, stdout, stderr)
}

// executeWithJob is Execute's body once the Job exists. It is separate so both
// entry points share one path: ExecuteWithSignals creates the Job because it
// has to relay into it, and Execute goes through ExecuteWithSignals with no
// signal channel.
//
// This is where a request's shape is checked. req.Cwd is client-controlled and
// flows straight into interp.Dir, and from there into every command's own
// working directory. The old router-based design (NonoExecutor.checkCwd)
// rejected a relative path or one outside the granted root itself; this
// executor does not reproduce that check, because the bound it enforced now
// comes from execd's own nono session instead: execd runs under --profile
// with no --allow-cwd (see ExecdArgs), so every filesystem access the
// interpreter or a child process makes — cwd included — is already confined to
// whatever that profile grants, whatever req.Cwd claims. What is checked here
// is only the request's shape, not its reach: a non-absolute Cwd (including
// the empty string a client sends when its own os.Getwd fails) is refused
// rather than silently resolved against this process's own working directory,
// which would not be the directory the agent thinks it is running commands in.
func (e *ShellExecutor) executeWithJob(ctx context.Context, req Request,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if strings.TrimSpace(req.Command) == "" {
		return 0, fmt.Errorf("execd: empty command")
	}
	if !filepath.IsAbs(req.Cwd) {
		return 0, fmt.Errorf("execd: cwd %q is not an absolute path", req.Cwd)
	}

	// req.TimeoutMs == 0 means no bound: leave ctx exactly as the caller gave
	// it, so a request with no timeout behaves exactly as it did before this
	// existed. The timer's write to timedOut and Run's read of it are
	// ordered by the cancellation the timer causes, but that happens-before
	// argument can't be checked here — the race detector needs cgo, which
	// this environment doesn't have — so timedOut is an atomic.Bool rather
	// than a plain bool.
	var timedOut atomic.Bool
	if req.TimeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		timer := time.AfterFunc(time.Duration(req.TimeoutMs)*time.Millisecond, func() {
			timedOut.Store(true)
			cancel()
		})
		defer timer.Stop()
	}

	code, err := e.Run(ctx, req.Command, req.Cwd, stdin, stdout, stderr)
	if timedOut.Load() {
		fmt.Fprintf(stderr, "agent-sandbox: command timed out after %dms\n", req.TimeoutMs)
		return ExitTimeout, nil
	}
	return code, err
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
