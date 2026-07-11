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
	Use:                "git [args...]",
	Short:              "Run git, refusing known-dangerous invocations",
	DisableFlagParsing: true, // pass every token straight through to the validator
	RunE:               runSafeGit,
}

func init() {
	safeCmd.AddCommand(safeGitCmd)
}

// execGit runs the real git binary with stdio inherited and returns its exit
// code. It is a package var so tests can replace it.
var execGit = func(ctx context.Context, args []string) int {
	c := exec.CommandContext(ctx, "git", args...)
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
