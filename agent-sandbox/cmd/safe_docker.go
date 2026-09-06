package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe/dockercompose"
)

var safeDockerCmd = &cobra.Command{
	Use:   "docker [args...]",
	Short: "Run docker, validating a compose invocation before running it",
	Long: `Run "docker" verbatim, except that when the first non-global argument
is "compose", the remainder is validated before anything runs.

The wrapper resolves the project with "docker compose config" and refuses the
invocation (exit 1, running nothing) when the configuration would:
  - mount a host path outside the current working directory, or the Docker socket;
  - set privileged, host network/pid/ipc, userns_mode host, or expose devices;
  - add a dangerous Linux capability, or disable seccomp/apparmor confinement;
  - use the "run" or "exec" subcommand.

Named volumes, tmpfs mounts, and every other compose subcommand pass through.
Every non-compose docker invocation passes through unchanged: this wrapper's
argv-level checks apply to it, but there is no resolved model to read for a
plain docker command the way there is for compose.`,
	Args:               cobra.ArbitraryArgs,
	DisableFlagParsing: true, // pass every token through to docker verbatim
	RunE:               runSafeDocker,
}

func init() {
	safeCmd.AddCommand(safeDockerCmd)
}

// dockerGlobalValueFlags lists docker's own global flags ("docker --help",
// the "Global Options" section) that take a value, so a leading
// "docker --context foo compose up" is still recognized as a compose
// invocation instead of being mistaken for a plain passthrough.
var dockerGlobalValueFlags = map[string]bool{
	"--config": true, "-c": true, "--context": true,
	"-H": true, "--host": true,
	"-l": true, "--log-level": true,
	"--tlscacert": true, "--tlscert": true, "--tlskey": true,
}

// dockerGlobalBoolFlags lists docker's own global flags that take no value.
var dockerGlobalBoolFlags = map[string]bool{
	"-D": true, "--debug": true,
	"--tls": true, "--tlsverify": true,
	"-v": true, "--version": true,
	"-h": true, "--help": true,
}

// splitDockerGlobal walks args' leading docker global flags and returns
// docker's own subcommand (the first non-flag token) plus everything from
// there on. An unrecognized leading flag is reported instead of guessed
// past, so the caller can fail closed rather than risk missing a "compose"
// subcommand hidden behind a flag this list does not know — the same
// fail-closed shape as dockercompose.ParseArgs's own Unrecognized field.
func splitDockerGlobal(args []string) (subcommand string, rest []string, unrecognized string) {
	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return a, args[i:], ""
		}
		key := a
		attached := false
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			key = a[:eq]
			attached = true
		}
		switch {
		case dockerGlobalValueFlags[key]:
			i++
			if !attached && i < len(args) {
				i++
			}
		case dockerGlobalBoolFlags[key]:
			i++
		default:
			return "", nil, a
		}
	}
	return "", nil, ""
}

func runSafeDocker(cmd *cobra.Command, args []string) error {
	sub, rest, unrecognized := splitDockerGlobal(args)
	if unrecognized != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "refused: unrecognized global docker flag %q is not allowed\n", unrecognized)
		os.Exit(1)
	}
	if sub == "compose" {
		return runSafeDockerCompose(cmd, rest[1:])
	}
	return execDockerReal(args)
}

// runSafeDockerCompose validates a "docker compose" invocation (args is
// everything after the "compose" token) before running it.
func runSafeDockerCompose(cmd *cobra.Command, args []string) error {
	// A global --help/-h describes this wrapper, not docker compose: an agent
	// wants this command's usage, so print it and stop.
	if dockercompose.WantsGlobalHelp(args) {
		return cmd.Help()
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}

	violations, err := dockercompose.Prepare(context.Background(), args, cwd, dockercompose.NewResolver())
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		for _, v := range violations {
			fmt.Fprintf(cmd.ErrOrStderr(), "refused: %s\n", v)
		}
		os.Exit(1)
	}

	return execDockerReal(append([]string{"compose"}, args...))
}

// execDockerReal execs the real docker binary (dockercompose.RealBinary,
// "docker-real") with args appended after this wrapper's own argv[0].
//
// It resolves RealBinary, never "docker": this wrapper is itself bound to
// the name "docker" in the command profile, so looking up "docker" from
// inside it would resolve back to this wrapper's own shim and recurse
// without bound. See dockercompose.RealBinary for the measurement.
func execDockerReal(args []string) error {
	dockerPath, err := exec.LookPath(dockercompose.RealBinary)
	if err != nil {
		return fmt.Errorf("%s not found in PATH; the command profile must grant this wrapper the %q command", dockercompose.RealBinary, dockercompose.RealBinary)
	}
	argv := append([]string{dockercompose.RealBinary}, args...)
	if err := syscall.Exec(dockerPath, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec docker: %w", err)
	}
	return nil
}
