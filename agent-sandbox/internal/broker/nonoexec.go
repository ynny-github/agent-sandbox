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
	"syscall"
	"time"
)

// waitDelay bounds how long Wait keeps draining the child's I/O pipes after
// the child itself has exited. The stdin deadlock is fixed structurally (see
// Execute), so this is only a backstop for output pipes inherited by a
// grandchild that outlives `nono run`: without it such a process turns a
// finished command into an unbounded hang. Five seconds is far more than
// draining already-buffered output needs.
const waitDelay = 5 * time.Second

// NonoExecutor runs each command in its own nono sandbox, using a profile the
// launcher generated once at startup.
type NonoExecutor struct {
	nonoPath    string
	profilePath string
	workdir     string
	envAllow    []string
}

// NewNonoExecutor returns an Executor that shells out to nono at nonoPath with
// the command profile at profilePath.
//
// workdir is the directory the command profile grants; a request may only run
// inside it.
//
// envAllow is the command profile's own environment allow_vars list — pass
// sandboxhost.Resolved.EnvAllowVars() for the same profile written to
// profilePath. It is the single source of truth for the process environment:
// the executor forwards exactly those of the LAUNCHER's variables that the
// profile already declares, so the supervisor's environment can never grant
// more than the sandbox itself would allow. Passing an empty list yields an
// empty environment, which is almost never what a caller wants.
func NewNonoExecutor(nonoPath, profilePath, workdir string, envAllow []string) *NonoExecutor {
	return &NonoExecutor{
		nonoPath:    nonoPath,
		profilePath: profilePath,
		workdir:     workdir,
		envAllow:    envAllow,
	}
}

// Args builds the nono argv for req. Exported so the argv shape is testable
// without spawning anything.
func (e *NonoExecutor) Args(req Request) []string {
	args := []string{
		"run", "--silent",
		"--profile", e.profilePath,
		"--workdir", req.Cwd,
		"--",
	}
	return append(args, req.Argv...)
}

// envAllowlist decides which of the launcher's environment variables are handed
// to the nono supervisor. It is built from the command profile's allow_vars, so
// the supervisor's environment can never exceed what the profile declares.
//
// The supported pattern syntax is deliberately narrow — the two forms this
// repo's profiles use, matched the way nono matches them:
//
//	NAME    exact variable name
//	NAME*   prefix match (the mise capability contributes "MISE*" and "__MISE*")
//
// A "*" anywhere other than at the end, and a bare "*", are NOT supported: such
// a pattern matches nothing, so an unrecognised form fails closed rather than
// over-granting. This is not a glob engine and must not become one.
type envAllowlist struct {
	exact    map[string]struct{}
	prefixes []string
}

func newEnvAllowlist(patterns []string) envAllowlist {
	a := envAllowlist{exact: make(map[string]struct{}, len(patterns))}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if i := strings.IndexByte(p, '*'); i >= 0 {
			if i == len(p)-1 && i > 0 {
				a.prefixes = append(a.prefixes, p[:i])
			}
			continue
		}
		a.exact[p] = struct{}{}
	}
	return a
}

func (a envAllowlist) allows(name string) bool {
	if _, ok := a.exact[name]; ok {
		return true
	}
	for _, p := range a.prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// ProcessEnv builds the environment for the nono process itself: the LAUNCHER's
// own values for exactly those names the command profile's allow_vars permits.
//
// The source matters as much as the filter. The broker runs outside the
// sandbox, so anything the sandboxed side could supply is untrusted input — a
// request-supplied XDG_STATE_HOME would redirect nono's own audit ledger and
// session state, letting the sandboxed side suppress its audit trail and making
// the unsandboxed supervisor write files anywhere the user can write. Reading
// from the launcher removes that class entirely; nothing here is
// request-controlled. It is also the only source that works: the agent's own
// nono profile does not allow-list sandbox.command.env_passthrough, so those
// variables are stripped before the sandboxed router could ever observe them.
//
// The NONO_* strip is kept as defence in depth: nono reads its own policy from
// the environment (NONO_ALLOW_DOMAIN, NONO_NETWORK_PROFILE, NONO_BLOCK_NET,
// NONO_PROFILE, NONO_TRUST_OVERRIDE, ...), so a single leaked variable would let
// the sandboxed side rewrite the policy meant to contain it. Config validation
// already rejects NONO_* in env_passthrough.
func (e *NonoExecutor) ProcessEnv() []string {
	allow := newEnvAllowlist(e.envAllow)
	env := make([]string, 0, len(e.envAllow)+len(allow.prefixes))
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || strings.HasPrefix(name, "NONO_") || !allow.allows(name) {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// checkCwd rejects a request working directory that is not an absolute path
// inside the directory the command profile grants. req.Cwd is client-controlled
// and flows into `nono run --workdir`, so it is validated before it reaches any
// policy machinery.
func (e *NonoExecutor) checkCwd(cwd string) error {
	if !filepath.IsAbs(cwd) {
		return fmt.Errorf("broker: working directory %q is not an absolute path", cwd)
	}
	root := resolvePath(e.workdir)
	if root == "" {
		return fmt.Errorf("broker: no sandboxed working directory is configured")
	}
	rel, err := filepath.Rel(root, resolvePath(cwd))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("broker: working directory %q is outside the sandboxed directory %q",
			cwd, e.workdir)
	}
	return nil
}

// resolvePath canonicalizes p so a symlinked path cannot masquerade as a path
// outside (or inside) the granted directory. A path that does not exist yet
// falls back to lexical cleaning.
func resolvePath(p string) string {
	if p == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// Execute runs one command and streams its output to stdout/stderr.
//
// stdin is pumped through cmd.StdinPipe() by a goroutine this method owns,
// rather than being handed to os/exec as cmd.Stdin. That is deliberate: with
// cmd.Stdin set, Wait blocks until os/exec's own copier finishes, and that
// copier sits in stdin.Read() — which nothing interrupts when the peer feeding
// stdin neither writes nor closes (`tail -f x | grep -m1 y`, with grep in the
// sandbox). Wait closes a StdinPipe as soon as the process exits, so the exit
// status is always reported; the pump goroutine unblocks separately when the
// server closes its end.
func (e *NonoExecutor) Execute(ctx context.Context, req Request, stdin io.Reader,
	stdout, stderr io.Writer) (int, error) {
	if len(req.Argv) == 0 {
		return 0, fmt.Errorf("broker: empty command")
	}
	if err := e.checkCwd(req.Cwd); err != nil {
		return 0, err
	}

	cmd := exec.CommandContext(ctx, e.nonoPath, e.Args(req)...)
	cmd.Dir = req.Cwd
	cmd.Env = e.ProcessEnv()
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = waitDelay

	if stdin != nil {
		w, err := cmd.StdinPipe()
		if err != nil {
			return 0, fmt.Errorf("broker: stdin pipe: %w", err)
		}
		go func() {
			io.Copy(w, stdin)
			w.Close()
		}()
	}

	err := cmd.Run()
	switch {
	case err == nil:
		return 0, nil
	case errors.Is(err, exec.ErrWaitDelay):
		// The child exited on its own; only its I/O pipes stayed open. Report
		// the real status rather than a broker failure.
		return exitStatus(cmd.ProcessState), nil
	default:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitStatus(exitErr.ProcessState), nil
		}
		return 0, fmt.Errorf("broker: run nono: %w", err)
	}
}

// exitStatus maps a finished process to the status a shell user expects.
// ProcessState.ExitCode() is -1 for a signal death, which the CLI would turn
// into exit 255; report the conventional 128+signum instead.
func exitStatus(ps *os.ProcessState) int {
	if ps == nil {
		return 0
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}
