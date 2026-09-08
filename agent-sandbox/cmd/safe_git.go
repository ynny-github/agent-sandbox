package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe/git"
)

var safeGitCmd = &cobra.Command{
	Use:   "git [args...]",
	Short: "Run git, refusing known-dangerous invocations and unresolvable subcommands",
	Long: `Run git, refusing an invocation that git.Check flags before it runs.

This refuses more than just the semantic denylist (force push, hard reset,
history rewrites, and the rest of git.Rules()): when the first non-global
token is not one of git's own command names, it is resolved as a configured
alias and the expansion is re-checked the same way — this is what catches an
alias installed by a direct write to .git/config, not only one set through
"git config" or "-c alias.x=...".

One consequence: a third-party git subcommand backed by its own "git-<name>"
executable on PATH — "git lfs", "git town", and similar plugins — is not a
git builtin and is not a configured alias either, so it is refused as an
unresolvable name. This is a behavior change from the wrapper as it existed
before alias-expansion was added, when such a subcommand simply passed
through unrecognized. There is currently no mechanism to allow a specific
third-party subcommand.`,
	DisableFlagParsing: true, // pass every token straight through to the validator
	RunE:               runSafeGit,
}

func init() {
	safeCmd.AddCommand(safeGitCmd)
}

// execGit runs the real git binary with stdio inherited and returns its exit
// code. It is a package var so tests can replace it.
//
// It resolves git.RealBinary ("realgit"), never "git": this wrapper is
// itself bound to the name "git" in the command profile, so looking up "git"
// from inside it would resolve back to this wrapper's own shim and recurse
// without bound. See git.RealBinary for the measurement and for why the
// resolved name must not start with "git-" either.
var execGit = func(ctx context.Context, args []string) int {
	path, err := exec.LookPath(git.RealBinary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "safe git: %q not found in PATH; the command profile must grant this wrapper the %q command\n", git.RealBinary, git.RealBinary)
		return 1
	}
	c := exec.CommandContext(ctx, path, args...)
	// argv[0] is forced to "git", not the resolved RealBinary path: belt-and-
	// braces against git's own argv[0] dispatch (see git.RealBinary) even if
	// RealBinary is ever misconfigured to a colliding name again, and it
	// keeps anything git does derive from its own program name (some of its
	// plumbing, and its multi-call entry points, do) from naming an internal
	// command the agent cannot run itself. Do not remove this as
	// apparently-redundant.
	c.Args[0] = "git"
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "safe git: %v\n", err)
		return 1
	}
	return 0
}

func runSafeGit(cmd *cobra.Command, args []string) error {
	os.Exit(runSafeGitCore(cmd.Context(), args, cmd.ErrOrStderr()))
	return nil
}

// runSafeGitCore validates args and either refuses (exit 1, git not run) or
// passes through to git, returning git's exit code.
func runSafeGitCore(ctx context.Context, args []string, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "safe git: no git command given")
		return 1
	}
	if vs := git.Check(args); len(vs) > 0 {
		for _, v := range vs {
			fmt.Fprintln(stderr, "blocked: "+v.Setting)
		}
		return 1
	}
	return execGit(ctx, args)
}
