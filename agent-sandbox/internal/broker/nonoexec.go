package broker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// NonoExecutor runs each command in its own nono sandbox, using a profile the
// launcher generated once at startup.
type NonoExecutor struct {
	nonoPath    string
	profilePath string
}

// NewNonoExecutor returns an Executor that shells out to nono at nonoPath with
// the command profile at profilePath.
func NewNonoExecutor(nonoPath, profilePath string) *NonoExecutor {
	return &NonoExecutor{nonoPath: nonoPath, profilePath: profilePath}
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

// ProcessEnv builds the environment for the nono process itself: the
// launcher's own environment plus the request's variables, with every NONO_*
// entry from the request removed.
//
// This filter is the second line of defence. Config validation already rejects
// NONO_* in env_passthrough, but nono reads its own policy from the
// environment (NONO_ALLOW_DOMAIN, NONO_NETWORK_PROFILE, NONO_BLOCK_NET,
// NONO_PROFILE, NONO_TRUST_OVERRIDE, ...), so a single leaked variable would
// let the sandboxed side rewrite the policy meant to contain it.
func (e *NonoExecutor) ProcessEnv(req Request) []string {
	env := os.Environ()
	for _, v := range req.Env {
		if strings.HasPrefix(v, "NONO_") {
			continue
		}
		env = append(env, v)
	}
	return env
}

// Execute runs one command and streams its output to stdout/stderr.
func (e *NonoExecutor) Execute(ctx context.Context, req Request, stdin io.Reader,
	stdout, stderr io.Writer) (int, error) {
	if len(req.Argv) == 0 {
		return 0, fmt.Errorf("broker: empty command")
	}

	cmd := exec.CommandContext(ctx, e.nonoPath, e.Args(req)...)
	cmd.Dir = req.Cwd
	cmd.Env = e.ProcessEnv(req)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if stdin != nil {
		cmd.Stdin = stdin
	}

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 0, fmt.Errorf("broker: run nono: %w", err)
	}
	return 0, nil
}
