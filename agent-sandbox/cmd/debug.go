// agent-sandbox/cmd/debug.go
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/claude"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/envflag"
)

var debugCmd = &cobra.Command{
	Use:                "debug [--config <path>] -- [claude-args...]",
	Short:              "Show the command that would be used to run Claude",
	DisableFlagParsing: true,
	RunE:               runDebug,
}

func init() {
	rootCmd.AddCommand(debugCmd)
}

func runDebug(cmd *cobra.Command, args []string) error {
	configFile, opts, err := claude.ParseArgs(args, configPath)
	if err != nil {
		return err
	}
	// --env loads the referenced file's variables into this process, which is
	// all it does now: nono forwards only what the agent profile's
	// environment.allow_vars lists, and that list is hand-written.
	if _, err := envflag.Load(opts.EnvRefs); err != nil {
		return err
	}
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	profilePath := cfg.AgentProfilePath("claude")

	// debug exists to show the exact invocation the launcher builds, so it must
	// include the broker socket grant; passing "" here would hide the only thing
	// the command broker adds to the wrap invocation.
	brokerSocket, err := claude.BrokerSocketPath()
	if err != nil {
		return err
	}

	// Same reason as the broker socket: the generated shell wrapper is granted
	// on the command line, so a debug invocation that omitted it would print an
	// argv the launcher never builds.
	shellWrapper, err := claude.ShellWrapperPath()
	if err != nil {
		return err
	}

	_, nonoArgs, err := claude.BuildArgs(cfg, opts, profilePath, brokerSocket, shellWrapper)
	if err != nil {
		return err
	}
	fmt.Println(strings.Join(nonoArgs, " "))

	selfPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate agent-sandbox: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "command broker:")
	fmt.Fprintln(cmd.OutOrStdout(), "  "+strings.Join(
		claude.BrokerArgs(cfg, nonoPathForDisplay(), selfPath, brokerSocket, cwd), " "))

	return nil
}

// nonoPathForDisplay resolves nono the same way the launcher does, so debug
// prints the binary that will actually run rather than an unresolved literal.
// It falls back to "nono" when lookup fails: debug must still print something
// useful when nono is missing, which is itself worth being able to see here.
func nonoPathForDisplay() string {
	if path, err := exec.LookPath("nono"); err == nil {
		return path
	}
	return "nono"
}
