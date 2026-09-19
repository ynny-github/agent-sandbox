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
	"syscall"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
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
		return 2, nil
	}

	// See refusePolicyPipeChains: a specific shape of this — a two-stage
	// pipe with a policy-controlled reader that blocks on stdin, fed by a
	// policy-controlled writer (`git log ... | git cat-file --batch-check`,
	// the case this exists for) — is measured, against a real nono session,
	// to hang and strand a process, for a reason entirely inside nono's own
	// process-spawning machinery, outside anything this package controls
	// (task-8-report.md's Finding B). Detecting and refusing it statically,
	// before any command in the line has run, is deterministic and
	// side-effect-free in a way that trying to detect and recover from the
	// hang at runtime was not. The refusal is wider than what was directly
	// measured — see refusePolicyPipeChains's own doc comment for exactly
	// which shapes were measured to hang, which were only reasoned to be
	// exposed to the same hazard, and which are not caught at all — so the
	// message below says "a combination measured to hang or exposed to the
	// same underlying hazard", not that every refused case was itself
	// observed hanging.
	if names, refuse := refusePolicyPipeChains(file, cwd, expand.ListEnviron(os.Environ()...)); refuse {
		fmt.Fprintf(stderr,
			"agent-sandbox: refused: this pipeline pipes two or more policy-controlled commands together (%s) — a combination measured to hang and strand a process in at least one shape, and reasoned to be exposed to the same underlying hazard in general. Run them as separate commands instead of piping them directly together.\n",
			strings.Join(names, ", "))
		return 126, nil
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
	// pipeline stage, belongs to it, and it is torn down on every path out of
	// Run — normal completion, a parse/exec error, or the context being
	// cancelled — so nothing this request started outlives Run itself.
	job := NewJob()
	defer job.Terminate()
	ctx = withJob(ctx, job)

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
		return interp.NewExitStatus(127)
	}

	// Pace only the policy-controlled commands: they are the ones whose
	// launch spends the session budget launchPacer rations, and delaying a
	// floor command would buy nothing.
	if isPolicyControlledPath(path) && !e.pacer.wait(ctx) {
		return interp.NewExitStatus(126)
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
	// producing its complete output. Interposing a pipe this side owns avoids
	// this because the shim only ever duplicates that pipe's fd; the
	// interpreter's own writer is never touched by the child (or the shim) at
	// all, so it stays exactly as open or closed as the interpreter itself left
	// it. What ends a pipeline stage is every copy of the interposed pipe's
	// write end being closed — the child's on exit, this side's in
	// closeJobEnds. A real file needs none of that and goes straight to the
	// child: see wiring.
	w, err := wireOutputs(cmd, hc)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: %v\n", args[0], err)
		return interp.NewExitStatus(126)
	}

	// stdin is an *os.File either way — the redirect's own file, or the read
	// end of a pipe this side owns and pumps hc.Stdin into. That is what keeps
	// Wait from blocking: with an io.Reader in cmd.Stdin, os/exec runs its own
	// copier and Wait waits for it, and that copier sits in Read(), which
	// nothing interrupts while the upstream end of the pipeline is still open.
	if err := w.wireStdin(cmd, hc); err != nil {
		w.abort()
		fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: %v\n", args[0], err)
		return interp.NewExitStatus(126)
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
		return interp.NewExitStatus(126)
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
	return interp.NewExitStatus(126)
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
// server owns the transport; ShellExecutor owns the shell language.
//
// req.Cwd is client-controlled and flows straight into interp.Dir, and from
// there into every command's own working directory. The old router-based
// design (NonoExecutor.checkCwd) rejected a relative path or one outside the
// granted root itself; this executor does not reproduce that check, because
// the bound it enforced now comes from execd's own nono session instead:
// execd runs under --profile with no --allow-cwd (see ExecdArgs), so
// every filesystem access the interpreter or a child process makes — cwd
// included — is already confined to whatever that profile grants, whatever
// req.Cwd claims. What is checked here is only the request's shape, not its
// reach: a non-absolute Cwd (including the empty string a client sends when
// its own os.Getwd fails) is refused rather than silently resolved against
// this process's own working directory, which would not be the directory the
// agent thinks it is running commands in.
func (e *ShellExecutor) Execute(ctx context.Context, req Request,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if strings.TrimSpace(req.Command) == "" {
		return 0, fmt.Errorf("execd: empty command")
	}
	if !filepath.IsAbs(req.Cwd) {
		return 0, fmt.Errorf("execd: cwd %q is not an absolute path", req.Cwd)
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
