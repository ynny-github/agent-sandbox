package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/internal/claude"
	"github.com/ynny-github/agent-sandbox/internal/launch"
)

var launchCmd = &cobra.Command{
	Use:   "launch <name> [args...]",
	Short: "Run .agent-sandbox/bin/<name> after approving any change to .agent-sandbox/",
	Long: `Run .agent-sandbox/bin/<name> with args.

Everything in .agent-sandbox/ is compared with the copy you last approved,
kept outside every path the sandbox can write. Any difference is shown and
must be approved on the terminal; then the approved copy's script runs, so a
change made later takes effect only once you approve it.`,
	Args:               cobra.MinimumNArgs(1),
	DisableFlagParsing: true,
	RunE:               runLaunch,
}

func init() { rootCmd.AddCommand(launchCmd) }

func runLaunch(cmd *cobra.Command, args []string) error {
	err := launch.Run(args[0], args[1:], launch.Options{
		Prompt: promptTTY,
		Writable: func(profile, path string) (bool, error) {
			return claude.ProfileAllowsWrite(context.Background(), combinedOutput, profile, path)
		},
		Exec: syscall.Exec,
		Out:  cmd.ErrOrStderr(),
	})
	if errors.Is(err, launch.ErrUsage) {
		fmt.Fprintln(cmd.ErrOrStderr(), "agent-sandbox launch:", err)
		os.Exit(2)
	}
	return err
}

func combinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// promptTTY asks on the controlling terminal, never stdin: the launch's own
// stdin belongs to the agent.
func promptTTY(diff string) (bool, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, fmt.Errorf("no terminal to approve on (%v); run it from a terminal", err)
	}
	defer tty.Close()
	fmt.Fprintf(tty, "%s/ differs from the copy you approved:\n\n%s\nRun with these changes? [y/N] ", launch.Dir, diff)
	line, _ := bufio.NewReader(tty).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}
