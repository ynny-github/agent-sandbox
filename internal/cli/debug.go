// internal/cli/debug.go
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/internal/claude"
	"github.com/ynny-github/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/internal/envflag"
	"github.com/ynny-github/agent-sandbox/internal/execd"
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
	// include the execd socket grant; passing "" here would hide the only thing
	// execd adds to the agent's nono invocation.
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	execdSocket, err := execd.SessionSocket(cfg.CommandProfilePath(), cwd)
	if err != nil {
		return err
	}

	// Same reason as the execd socket: the generated shell wrapper is granted
	// on the command line, so a debug invocation that omitted it would print an
	// argv the launcher never builds.
	shellWrapper, err := claude.ShellWrapperPath()
	if err != nil {
		return err
	}

	execdPath, execdErr := claude.ResolveExecd()
	grant := execdPath // "" when unresolved: the launch would refuse anyway

	_, nonoArgs, err := claude.BuildArgs(cfg, opts, profilePath, execdSocket, shellWrapper, grant)
	if err != nil {
		return err
	}
	fmt.Println(strings.Join(nonoArgs, " "))

	if execdErr != nil {
		execdPath = "execd"
	}
	fmt.Fprintln(cmd.OutOrStdout(), "exec daemon:")
	fmt.Fprintln(cmd.OutOrStdout(), "  "+strings.Join([]string{
		execdPath, "up", "--profile", cfg.CommandProfilePath(), "--workdir", cwd}, " "))
	if execdErr != nil {
		fmt.Fprintln(cmd.OutOrStdout(), "  ("+execdErr.Error()+")")
	}

	if opts.ContextMode {
		fmt.Fprintln(cmd.OutOrStdout(),
			"environment: "+claude.ContextModeEnvVar+"="+claude.ContextModeExecd)
	}

	return nil
}
