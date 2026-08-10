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

// baseEnvKeys are the variables the nono process always inherits from the
// launcher's own (trusted) environment. Request-supplied values never override
// them: the request comes from inside the sandbox.
var baseEnvKeys = []string{"PATH", "HOME", "TERM", "LANG", "LC_ALL", "USER"}

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
// inside it. envAllow is the set of environment variable names the config
// permits a request to forward (cfg.Sandbox.Command.EnvPassthrough); every
// other request variable is discarded.
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

// ProcessEnv builds the environment for the nono process itself. The broker
// runs outside the sandbox, so req.Env is untrusted input and the filter is an
// allowlist: the launcher's own values for baseEnvKeys, plus the request's
// values for exactly those names the config permits to be forwarded.
//
// An allowlist is the only safe shape here. A denylist let a request set, for
// example, XDG_STATE_HOME and so redirect nono's own audit ledger and session
// state — the sandboxed side suppressing its own audit trail, and the
// unsandboxed supervisor writing files anywhere the user can write.
//
// The NONO_* strip is kept as defence in depth: nono reads its own policy from
// the environment (NONO_ALLOW_DOMAIN, NONO_NETWORK_PROFILE, NONO_BLOCK_NET,
// NONO_PROFILE, NONO_TRUST_OVERRIDE, ...), so a single leaked variable would
// let the sandboxed side rewrite the policy meant to contain it. Config
// validation already rejects NONO_* in env_passthrough.
func (e *NonoExecutor) ProcessEnv(req Request) []string {
	env := make([]string, 0, len(baseEnvKeys)+len(e.envAllow))
	base := make(map[string]bool, len(baseEnvKeys))
	for _, k := range baseEnvKeys {
		base[k] = true
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}

	allowed := make(map[string]bool, len(e.envAllow))
	for _, k := range e.envAllow {
		k = strings.TrimSpace(k)
		if k == "" || base[k] || strings.HasPrefix(k, "NONO_") {
			continue
		}
		allowed[k] = true
	}
	if len(allowed) == 0 {
		return env
	}
	for _, v := range req.Env {
		k, _, ok := strings.Cut(v, "=")
		if !ok || !allowed[k] {
			continue
		}
		env = append(env, v)
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
	cmd.Env = e.ProcessEnv(req)
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
