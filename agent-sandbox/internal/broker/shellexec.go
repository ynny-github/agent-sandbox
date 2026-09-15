package broker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

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
type ShellExecutor struct {
	// pacer is shared by every request this executor serves, which is what
	// makes it process-wide: cmd/broker.go builds exactly one ShellExecutor
	// and hands it to the server, whose Serve spawns a goroutine per
	// connection. The budget being paced belongs to the one nono session all
	// of those goroutines launch into, so a per-request limiter would not
	// bound anything.
	pacer *launchPacer
}

// PolicyLaunchBurst and PolicyLaunchInterval pace how fast policy-controlled
// commands may be launched: PolicyLaunchBurst of them may start with no wait
// at all, and the budget refills at one launch per PolicyLaunchInterval. See
// launchPacer for what is being rationed and how the numbers were arrived at.
const (
	PolicyLaunchBurst    = 4
	PolicyLaunchInterval = 500 * time.Millisecond
)

// NewShellExecutor returns a ShellExecutor. Its only state is the launch pacer
// below, which is deliberately per-executor rather than per-request: see the
// pacer field.
func NewShellExecutor() *ShellExecutor { return &ShellExecutor{pacer: &launchPacer{}} }

// launchPacer rations how fast policy-controlled commands are launched.
//
// The defect it exists for, measured on nono 0.77.0 / Ubuntu 24.04 against
// this repository's command-profile.json: policy-controlled commands launched
// back-to-back inside ONE nono session exhaust something the session reclaims
// only over time, and every launch past that point dies before the command
// itself runs, with "Sandbox initialization failed: tool-sandbox shim failed
// to connect to <TMPDIR>/nono-tool-sandbox-<id>/supervisor.sock: Operation
// not permitted". Measured with `git --version` run 12 times through xargs
// inside one session:
//
//	~50ms apart   ooooooxxxxxx / ooooooxxxxxx / oooooxoxxxxx   (3 runs)
//	250ms apart   oooooxoooooo
//	500ms apart   oooooooooooo / oooooooooooo / oooooooooooo   (3 runs)
//
// The same 12 commands run as 12 SEPARATE `nono run` sessions are 12/12, so
// what is exhausted belongs to the session, not to the host — and the broker
// is one long-lived session by construction (it must be the session
// entrypoint for the profile to govern everything it executes, and nono
// refuses to nest), so spacing the launches is the lever this side of the
// boundary actually has.
//
// The numbers: PolicyLaunchInterval is the smallest spacing measured clean,
// and PolicyLaunchBurst is set below the 5-6 launches measured free from a
// cold start, so a burst cannot spend the whole budget. They are not read
// from nono, which reports nothing about this budget; re-measure before
// changing them, and re-measure on a nono upgrade.
//
// Only the broker's own launches are paced, and only those need to be.
// Measured against this profile with a synthetic `bash -> git` edge (the real
// profile's own nesting edges are `bash`/`sh` -> `go` and `git` -> `ssh`,
// neither cheap to drive here): a policy command launching 12 more policy
// commands from inside its own child sandbox is 12/12, and session-level
// launches issued immediately afterwards are 3/3 — 3 runs of both. What is
// exhausted is spent by launches made from the session, which is exactly the
// set execHandler makes. A different nesting shape was not measured, so treat
// this as "the shape this repository can produce", not as a law.
//
// Not the trigger, despite an earlier note in this repository saying so: the
// session's top-level "network" block. With every network key removed from
// the profile — top-level and child alike — the same burst still fails
// identically (3/3 runs). That earlier finding was measured on nono 0.74.0
// and does not hold on 0.77.0.
type launchPacer struct {
	mu sync.Mutex
	// ready is when the next launch may start. It runs ahead of the clock
	// while launches are being spent and is clamped back to at most a full
	// burst behind it, which is what "the bucket refills up to
	// PolicyLaunchBurst" means expressed as a single instant rather than a
	// token count and a refill timer.
	ready time.Time
}

// wait blocks until this launch's turn, or until ctx is done. It reports
// whether the launch may proceed; false means ctx ended the wait and the
// caller must not start the command.
//
// The turn is claimed under the lock and waited for outside it, so concurrent
// callers queue in the order they arrived rather than all racing for the same
// instant.
func (p *launchPacer) wait(ctx context.Context) bool {
	p.mu.Lock()
	now := time.Now()
	if floor := now.Add(-(PolicyLaunchBurst - 1) * PolicyLaunchInterval); p.ready.Before(floor) {
		p.ready = floor
	}
	at := p.ready
	p.ready = p.ready.Add(PolicyLaunchInterval)
	p.mu.Unlock()

	d := time.Until(at)
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

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

// isPolicyControlledPath reports whether path is a nono-generated shim for a
// policy-controlled command, rather than a floor command's real binary,
// judging only by the resolved path's own shape. This is a heuristic over
// nono's own directory naming ("<TMPDIR>/nono-tool-sandbox-<id>/shims/<name>",
// measured directly), not a documented API: nono gives this process no other
// signal that distinguishes the two tiers from a resolved path alone. A
// future nono that renames this directory would make this heuristic stop
// matching — the failure mode is a return to the hang refusePolicyPipeChains
// exists to prevent, not a false refusal, since that function only acts when
// this matches.
//
// command_policies is back in the profile this repository ships: git, ssh,
// bash and sh each carry a command_policies entry, so nono generates a real
// shim for each of the four and PATH resolves them there, not to their real
// binaries. This function and refusePolicyPipeChains below are therefore
// live against real shims, not inert — a refusal from either is the guard
// doing its job, not a false positive to investigate away.
func isPolicyControlledPath(path string) bool {
	shimsDir := filepath.Dir(path)
	return filepath.Base(shimsDir) == "shims" &&
		strings.Contains(filepath.Base(filepath.Dir(shimsDir)), "nono-tool-sandbox-")
}

// refusePolicyPipeChains reports whether file's parsed command line contains
// a pipe (`|` or `|&`) with two or more stages that resolve, before anything
// runs, to a policy-controlled command. names lists which resolved command
// names triggered the refusal, for the caller's error message.
//
// The exact two-stage shape this exists for — a policy-controlled reader
// that blocks on stdin, fed by a policy-controlled writer — is measured,
// against a real nono session, to hang and strand a process
// (task-8-report.md's Finding B). A three-or-more-stage chain with a
// policy-controlled command at two non-adjacent ends (`policy | floor |
// policy`) is refused by the same check but was not independently measured
// to hang — it is reasoned to be exposed to the identical hazard, since all
// stages of one pipe still run concurrently regardless of how many there
// are, not confirmed to reproduce it. Two policy commands piped together
// where neither blocks on stdin (`git --version | git --version`) was
// measured *not* to hang, and would still be refused here — this check
// cannot tell that case apart from the one it exists for, since doing so
// would require knowing whether a stage reads its stdin to EOF, which is a
// runtime property a static, parse-time pass cannot see.
//
// This is a static, parse-time check: LookPathDir is used only to learn
// where a literal command name would resolve, never to run anything, so
// there is no race and no side effect to get wrong — the two properties a
// runtime detection attempt (tried and abandoned; see the doc comment on the
// old execHandler ordering this replaced) could not deliver.
//
// Deliberately narrow, not a general "two policy commands running
// concurrently in one request" detector: it inspects only a literal pipe
// chain's own immediate stages (`a | b | c`, flattened through nested
// Pipe/PipeAll operators), each stage's command name only when it is a
// simple, unexpanded literal. It does not follow into a stage that is itself
// a compound command (a `{ }` group, `if`, `while`, a subshell, ...) to find
// a pipe buried inside it, and it has no way to see a policy command reached
// through backgrounding (`policy & policy`) or command substitution
// (`$(policy) | policy`) — both are exposed to the same underlying hazard,
// by the same reasoning, but neither is a literal pipe stage this function
// can resolve without simulating expansion or execution, which is exactly
// what staying static rules out. A profile author relying on this as a
// complete guarantee against the hang, rather than the specific shape it
// covers, would be relying on more than it delivers.
func refusePolicyPipeChains(file *syntax.File, cwd string, env expand.Environ) (names []string, refuse bool) {
	var found []string
	syntax.Walk(file, func(n syntax.Node) bool {
		bc, ok := n.(*syntax.BinaryCmd)
		if !ok || (bc.Op != syntax.Pipe && bc.Op != syntax.PipeAll) {
			return true
		}
		var policyNames []string
		for _, stage := range flattenPipeStages(bc) {
			call, ok := stage.Cmd.(*syntax.CallExpr)
			if !ok || len(call.Args) == 0 {
				continue
			}
			name := call.Args[0].Lit()
			if name == "" {
				continue // not a simple literal; cannot resolve statically
			}
			path, err := interp.LookPathDir(cwd, env, name)
			if err != nil {
				continue
			}
			if isPolicyControlledPath(path) {
				policyNames = append(policyNames, name)
			}
		}
		if len(policyNames) >= 2 {
			found = append(found, policyNames...)
		}
		return false // this chain is fully inspected; do not also revisit its nested Pipe/PipeAll nodes
	})
	return found, len(found) > 0
}

// flattenPipeStages returns bc's pipe chain as an ordered list of stages,
// flattening through any nested Pipe/PipeAll operator (`a | b | c` parses as
// nested BinaryCmd nodes) so a 3-or-more-stage pipe is inspected as a whole
// rather than as two independent 2-stage checks that would each see only
// half of it.
func flattenPipeStages(bc *syntax.BinaryCmd) []*syntax.Stmt {
	var stages []*syntax.Stmt
	var collect func(s *syntax.Stmt)
	collect = func(s *syntax.Stmt) {
		if inner, ok := s.Cmd.(*syntax.BinaryCmd); ok && (inner.Op == syntax.Pipe || inner.Op == syntax.PipeAll) {
			collect(inner.X)
			collect(inner.Y)
			return
		}
		stages = append(stages, s)
	}
	collect(bc.X)
	collect(bc.Y)
	return stages
}

// execHandler is the only place this process performs an execve. Everything the
// interpreter treats as a simple command — and nothing else — arrives here.
func (e *ShellExecutor) execHandler(ctx context.Context, args []string) error {
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

	// Pace only the policy-controlled commands: they are the ones whose
	// launch spends the session budget launchPacer rations, and delaying a
	// floor command would buy nothing.
	if isPolicyControlledPath(path) && !e.pacer.wait(ctx) {
		return interp.NewExitStatus(126)
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
	// finished risks truncating output that was still in flight. A prior
	// version of this function tried waiting for process exit concurrently
	// with draining, to unblock a drain left waiting on a leaked fd it has no
	// other way to detect (see ShellExecutor.Run's pipe pre-check for the
	// hazard this refers to). Measured against a real nono session, that did
	// not reliably work: racing cmd.Wait() itself against an in-flight drain
	// truncates output that had not been read yet (Go's own Cmd.Wait
	// unconditionally closes every tracked pipe the instant it reaps the
	// process), and reading process state from /proc instead to avoid that
	// still left the hang reproducing in most runs, for reasons inside nono's
	// own process-spawning machinery that a read ordering fix on this side of
	// the boundary cannot address. See task-8-report.md's Finding B fix
	// report for what was tried and measured.
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
//
// req.Cwd is client-controlled and flows straight into interp.Dir, and from
// there into every command's own working directory. The old router-based
// design (NonoExecutor.checkCwd) rejected a relative path or one outside the
// granted root itself; this executor does not reproduce that check, because
// the bound it enforced now comes from the broker's own nono session instead:
// the broker runs under --profile with no --allow-cwd (see BrokerArgs), so
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
		return 0, fmt.Errorf("broker: empty command")
	}
	if !filepath.IsAbs(req.Cwd) {
		return 0, fmt.Errorf("broker: cwd %q is not an absolute path", req.Cwd)
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
