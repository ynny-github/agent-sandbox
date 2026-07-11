package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe/gh"
)

var safeGhCmd = &cobra.Command{
	Use:                "gh [args...]",
	Short:              "Run gh for issues/PRs/projects, refusing destructive or out-of-scope operations",
	DisableFlagParsing: true, // pass every token straight through to the validator
	RunE:               runSafeGh,
}

func init() {
	safeCmd.AddCommand(safeGhCmd)
}

// execGh runs the real gh binary with stdio inherited and returns its exit code.
// It is a package var so tests can replace it.
var execGh = func(ctx context.Context, args []string) int {
	c := exec.CommandContext(ctx, "gh", args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "safe gh: %v\n", err)
		return 1
	}
	return 0
}

func runSafeGh(cmd *cobra.Command, args []string) error {
	os.Exit(runSafeGhCore(cmd.Context(), args, cmd.ErrOrStderr()))
	return nil
}

// runSafeGhCore validates args and either refuses (exit 1, gh not run) or passes
// through to gh, returning gh's exit code.
func runSafeGhCore(ctx context.Context, args []string, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "safe gh: no gh command given")
		return 1
	}
	if vs := gh.Check(args); len(vs) > 0 {
		for _, v := range vs {
			fmt.Fprintln(stderr, "blocked: "+v.Setting)
		}
		return 1
	}
	return execGh(ctx, args)
}
