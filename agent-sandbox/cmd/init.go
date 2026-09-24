// agent-sandbox/cmd/init.go
package cmd

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/scaffold"
)

// Seams for tests: one points the fetch at a local server, the other removes
// the dependency on nono being installed.
var (
	initBaseURL  = scaffold.BaseURL
	initValidate = func(fetched []scaffold.Fetched) ([]string, error) {
		return scaffold.Validate(context.Background(), fetched)
	}
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Download a minimal config, both nono profiles, and the profile skills into this project",
	Long: `Download a starting point from this repository's main branch and write it
beside the config path: agent-sandbox.toml, a minimal command profile and
agent profile, and the two skills that explain how to grow a profile.

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

func init() {
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	// Files land beside the config path, which --config can move.
	dir := filepath.Dir(configPath)

	// cobra only sets a context when the command runs through Execute; a
	// direct call leaves it nil, which WithTimeout would panic on.
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()

	fmt.Fprintf(out, "fetching from %s\n", initBaseURL)
	fetched, err := scaffold.Fetch(ctx, &http.Client{Timeout: 30 * time.Second}, initBaseURL)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	for _, f := range fetched {
		fmt.Fprintf(out, "  %s  %d bytes  %s\n", f.URL, len(f.Body), f.ETag)
	}

	warnings, err := initValidate(fetched)
	if err != nil {
		return fmt.Errorf("profile validation failed, nothing written: %w", err)
	}
	for _, w := range warnings {
		fmt.Fprintf(out, "warning: %s\n", w)
	}

	written, skipped, err := scaffold.Write(dir, fetched)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	for _, p := range written {
		fmt.Fprintf(out, "wrote %s\n", p)
	}
	for _, p := range skipped {
		fmt.Fprintf(out, "skip  %s (already exists)\n", p)
	}

	if len(written) == 0 {
		fmt.Fprint(out, "\nAlready initialized; nothing to do.\n")
		return nil
	}
	fmt.Fprint(out, "\nNext: agent-sandbox doctor, then agent-sandbox claude\n"+
		"These profiles are a starting point. Read the seeded\n"+
		"growing-a-nono-profile skill before widening them.\n")
	return nil
}
