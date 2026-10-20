package execd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// The statuses execd reports for outcomes that are not the command's own.
// They are collected here because a caller reads them as one vocabulary:
//
//	the command's own   the last command's exit status
//	128 + signum        died from a signal
//	127                 command not found
//	126                 could not be started
//	124                 timed out
//	2                   syntax error
//
// An error frame is never used for any of these: it reports a fault of execd
// itself, and a command that fails is not a fault of execd.
const (
	ExitSyntaxError = 2
	// ExitTimeout is the status a timed-out request reports. It follows GNU
	// timeout(1). At the timeout the request's process group gets SIGTERM, a
	// grace, then SIGKILL; a policy-controlled command's own children run in
	// nono's child sandbox outside that group and can survive it (measured on
	// nono 0.74.0, `sh -c 'sleep 60'` under a 3000ms timeout, 3/3). See Job.
	ExitTimeout     = 124
	ExitCannotStart = 126
	ExitNotFound    = 127
)

// ShellExecutor runs one command line with bash, inside execd's own sandbox.
// nono mediates every command bash executes at execve — by name, by path,
// through env, sh -c or a function alike — so bash parsing the line costs no
// policy (measured on nono 0.78.0, 2026-10-20, against an in-process
// interpreter: identical denials, and a policy-to-policy pipe that the
// interpreter stalled on finished at once). What bash gives up is pacing:
// nono admits only about five policy-controlled launches in quick
// succession, and a line that launches more can fail with exit 126.
type ShellExecutor struct {
	bash string // resolved once; empty when bash is not on PATH
}

// NewShellExecutor resolves bash once, so every request runs the same one.
func NewShellExecutor() *ShellExecutor {
	bash, _ := exec.LookPath("bash")
	return &ShellExecutor{bash: bash}
}

// Run executes command with bash in cwd and returns its exit status. bash
// runs in the request's Job (its own process group), so its children share
// the group and the Job's signal relay and teardown reach them.
func (e *ShellExecutor) Run(ctx context.Context, command, cwd string, stdio Stdio) (int, error) {
	if e.bash == "" {
		fmt.Fprintln(stdio.Err, "execd: bash not found in PATH")
		return ExitNotFound, nil
	}
	cmd := exec.Command(e.bash, "--norc", "--noprofile", "-c", command)
	cmd.Dir = cwd
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdio.In, stdio.Out, stdio.Err
	cmd.Env = bashEnv(os.Environ())
	job := jobFrom(ctx)
	if job == nil {
		job = NewJob(stdio)
		defer job.Terminate()
	}
	if err := job.Start(cmd); err != nil {
		fmt.Fprintf(stdio.Err, "execd: start bash: %v\n", err)
		return ExitCannotStart, nil
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			job.Terminate()
		case <-done:
		}
	}()
	err := cmd.Wait()
	close(done)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitStatusOf(exitErr.ProcessState), nil
		}
		return ExitCannotStart, nil
	}
	return 0, nil
}

// bashEnv drops the variables a non-interactive bash would source a file
// from, so nothing in execd's environment injects a script into every line.
func bashEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		if strings.HasPrefix(kv, "BASH_ENV=") || strings.HasPrefix(kv, "ENV=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Execute satisfies Executor so the server can run a request directly. The
// server owns the transport; ShellExecutor owns the shell language. It is
// ExecuteWithSignals with no signal channel, so the contract — the request
// shape it refuses, the timeout it honours, the Job it tears down — is that
// function's and executeWithJob's, and is documented there.
func (e *ShellExecutor) Execute(ctx context.Context, req Request,
	stdio Stdio) (int, error) {
	return e.ExecuteWithSignals(ctx, req, stdio, nil)
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
	stdio Stdio, sigs <-chan syscall.Signal) (int, error) {
	// The Job is created here rather than in Run because the relay has to
	// have something to relay into before the first command exists: a signal
	// that arrives early finds a Job with no groups yet and delivers nothing,
	// which is the correct outcome for a command that has not started.
	job := NewJob(stdio)
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

	return e.executeWithJob(withJob(ctx, job), req, stdio)
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
// line's bash or a child process makes — cwd included — is already confined to
// whatever that profile grants, whatever req.Cwd claims. What is checked here
// is only the request's shape, not its reach: a non-absolute Cwd (including
// the empty string a client sends when its own os.Getwd fails) is refused
// rather than silently resolved against this process's own working directory,
// which would not be the directory the agent thinks it is running commands in.
func (e *ShellExecutor) executeWithJob(ctx context.Context, req Request,
	stdio Stdio) (int, error) {
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

	code, err := e.Run(ctx, req.Command, req.Cwd, stdio)
	if timedOut.Load() {
		fmt.Fprintf(stdio.Err, "agent-sandbox: command timed out after %dms\n", req.TimeoutMs)
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
