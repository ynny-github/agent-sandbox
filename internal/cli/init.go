// internal/cli/init.go
package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	agentsandbox "github.com/ynny-github/agent-sandbox"
	"github.com/ynny-github/agent-sandbox/internal/scaffold"
)

// Seam for tests: removes the dependency on nono being installed. It takes the command's
// own context so a cancellation (Ctrl-C, the 60-second timeout below) reaches
// the nono subprocess instead of stopping short at this seam.
var (
	initValidate = func(ctx context.Context, fetched []scaffold.Fetched) ([]string, error) {
		return scaffold.Validate(ctx, fetched)
	}
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Write the .agent-sandbox/ launch template and the profile skills into this project",
	Long: `Write a starting point, embedded in this binary, into the current directory: .agent-sandbox/ (the launch script bin/claude, the
hook settings, the shell wrapper, a minimal command profile and agent profile)
and the two skills that explain how to grow a profile.

Nothing is ever overwritten -- a file that already exists is reported and
left alone, so re-running init is safe and skills you have edited are yours.
The profiles are handed to ` + "`nono profile validate`" + ` before anything is
written; if either is rejected, no file is created at all.

The profiles are a starting point, not a finished boundary. Run
` + "`agent-sandbox ai explain`" + ` and read the seeded growing-a-nono-profile skill
before widening them.`,
	Args: cobra.NoArgs,
	RunE: runInit,
}

// initDir is where init writes; a var so tests can point it at a temp dir.
var initDir = "."

func init() {
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	dir := initDir

	// cobra only sets a context when the command runs through Execute; a
	// direct call leaves it nil, which WithTimeout would panic on.
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()

	fetched, err := scaffold.Load(agentsandbox.Assets)
	if err != nil {
		return fmt.Errorf("read the embedded templates: %w", err)
	}

	warnings, err := initValidate(ctx, fetched)
	if err != nil {
		return fmt.Errorf("profile validation failed, nothing written: %w", err)
	}
	for _, w := range warnings {
		fmt.Fprintf(out, "warning: %s\n", w)
	}

	// written and skipped are printed even on failure: scaffold.Write returns
	// what it accumulated before the error, and a user whose run failed on
	// file 4 of 5 needs to know the other three already landed on disk.
	written, skipped, err := scaffold.Write(dir, fetched)
	for _, p := range written {
		fmt.Fprintf(out, "wrote %s\n", p)
	}
	for _, p := range skipped {
		fmt.Fprintf(out, "skip  %s (already exists)\n", p)
	}
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}

	if len(written) == 0 {
		fmt.Fprint(out, "\nAlready initialized; nothing to do.\n")
		return nil
	}
	fmt.Fprint(out, "\nNext: agent-sandbox launch claude\n"+
		"These profiles are a starting point. Read the seeded\n"+
		"growing-a-nono-profile skill before widening them.\n")
	return nil
}
