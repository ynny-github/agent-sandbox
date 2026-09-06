package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe/dockercompose"
)

var safeDockerCmd = &cobra.Command{
	Use:   "docker [args...]",
	Short: "Run docker, checking a compose invocation against its resolved model and every other invocation at the argv level",
	Long: `Run "docker", except for two kinds of checks that run first.

When the first non-global argument is "compose", the remainder is validated
against the resolved model ("docker compose config"). The invocation is
refused (exit 1, running nothing) when the configuration would:
  - mount a host path outside the current working directory, or the Docker socket;
  - set privileged, host network/pid/ipc, userns_mode host, or expose devices;
  - add a dangerous Linux capability, or disable seccomp/apparmor confinement;
  - use the "run" or "exec" subcommand.
Named volumes, tmpfs mounts, and every other compose subcommand pass through.

Every other docker invocation is checked at the argv level only — there is no
resolved model to read the way "docker compose config" gives one for compose:
  - the "run" and "exec" subcommands are refused outright;
  - "--privileged" is refused wherever it appears;
  - a "-v"/"--volume"/"--mount" bind of a host path outside the current
    working directory, or of the Docker socket, is refused wherever it appears.
Nothing else about a plain docker invocation is inspected: capabilities,
network mode, and the rest of what the compose model check reads have no
equivalent check here.

A global docker flag (--context, -H, --config, --tls*, ...) before "compose"
is refused rather than silently validating and running the resolved compose
model against a different daemon than the one actually checked.`,
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
		// A docker-level global (--context, -H, --config, --tls*, ...) before
		// "compose" would retarget which daemon/context "docker compose
		// config" resolves against and which one the final exec runs on,
		// while dockercompose.Prepare only ever sees compose's own global
		// flags (rest[1:]). Validation and exec would still agree with each
		// other — both would silently use the wrong daemon — so this is not
		// a policy bypass, but retargeting the daemon without a diagnostic is
		// wrong on its own. Refuse rather than carry it through unchecked.
		if leading := args[:len(args)-len(rest)]; len(leading) > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"refused: a global docker flag (%s) before \"compose\" is not allowed: it would validate and run the resolved compose model against a different daemon than the default one\n",
				strings.Join(leading, " "))
			os.Exit(1)
		}
		return runSafeDockerCompose(cmd, rest[1:])
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	if vs := dockerCLIViolations(sub, rest, cwd); len(vs) > 0 {
		for _, v := range vs {
			fmt.Fprintf(cmd.ErrOrStderr(), "refused: %s\n", v)
		}
		os.Exit(1)
	}
	return execDockerReal(args)
}

// dockerDangerousSubcommands mirrors, at the plain "docker" level, the
// dockercompose.CheckCLI denial of subcommands that are entrypoints for
// arbitrary command execution.
var dockerDangerousSubcommands = map[string]bool{"run": true, "exec": true}

// dockerCLIViolations applies argv-only checks to a non-compose docker
// invocation. sub is docker's own subcommand (as identified by
// splitDockerGlobal); rest is sub and everything from there on; cwd bounds
// an allowed bind mount the same way it does for compose.
//
// This is deliberately narrow: there is no resolved model to check the way
// dockercompose.CheckModel reads what "docker compose config" resolves to,
// so this only catches what is visible directly in argv, for the same two
// categories CheckModel would refuse for compose. A container created some
// other way — a Swarm service, a Dockerfile ONBUILD, an image's own
// ENTRYPOINT — is out of reach of an argv-only check and this does not claim
// to catch it.
func dockerCLIViolations(sub string, rest []string, cwd string) []string {
	var out []string
	if dockerDangerousSubcommands[sub] {
		out = append(out, fmt.Sprintf("%q subcommand is not allowed", sub))
	}
	for i, a := range rest {
		switch {
		case a == "--privileged":
			out = append(out, "--privileged is not allowed")
		case a == "-v" || a == "--volume":
			if i+1 < len(rest) {
				if v := checkVolumeSpec(rest[i+1], cwd); v != "" {
					out = append(out, v)
				}
			}
		case strings.HasPrefix(a, "--volume="):
			if v := checkVolumeSpec(strings.TrimPrefix(a, "--volume="), cwd); v != "" {
				out = append(out, v)
			}
		case len(a) > 2 && strings.HasPrefix(a, "-v") && a[1] != '-':
			if v := checkVolumeSpec(a[2:], cwd); v != "" {
				out = append(out, v)
			}
		case a == "--mount":
			if i+1 < len(rest) {
				if v := checkMountSpec(rest[i+1], cwd); v != "" {
					out = append(out, v)
				}
			}
		case strings.HasPrefix(a, "--mount="):
			if v := checkMountSpec(strings.TrimPrefix(a, "--mount="), cwd); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// checkVolumeSpec applies the same rule dockercompose.CheckModel applies to
// a bind mount's source to a "-v"/"--volume" spec ("SRC:DST[:OPTS]", or a
// bare "SRC" for an anonymous volume): refuse the Docker socket, refuse a
// host path outside cwd, and let a named volume (a source with no leading
// "/", which docker never treats as a host path) through untouched.
func checkVolumeSpec(spec, cwd string) string {
	src := spec
	if idx := strings.IndexByte(spec, ':'); idx >= 0 {
		src = spec[:idx]
	}
	return checkBindSource(src, cwd)
}

// checkMountSpec applies the same rule to a "--mount" spec
// ("type=bind,src=/host,dst=/x", "source=" is an accepted alias for "src=").
// A non-bind mount (type=volume, type=tmpfs) is not a host path and passes.
func checkMountSpec(spec, cwd string) string {
	var typ, src string
	for _, kv := range strings.Split(spec, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "type":
			typ = strings.TrimSpace(v)
		case "src", "source":
			src = strings.TrimSpace(v)
		}
	}
	if typ != "bind" || src == "" {
		return ""
	}
	return checkBindSource(src, cwd)
}

// checkBindSource is the shared rule: a source with no leading "/" is not a
// host path (docker requires bind sources to be absolute; anything else is a
// named volume) and passes untouched; the Docker socket and any host path
// outside cwd are refused, matching dockercompose.CheckModel's own bind
// checks exactly.
func checkBindSource(src, cwd string) string {
	if !strings.HasPrefix(src, "/") {
		return ""
	}
	if filepath.Base(filepath.Clean(src)) == "docker.sock" {
		return fmt.Sprintf("bind mount of the docker socket %q is not allowed", src)
	}
	if !safe.PathWithin(safe.RealPath(cwd), safe.RealPath(src)) {
		return fmt.Sprintf("bind mount %q escapes the work directory", src)
	}
	return ""
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
// "realdocker") with args as everything after argv[0].
//
// It resolves RealBinary, never "docker": this wrapper is itself bound to
// the name "docker" in the command profile, so looking up "docker" from
// inside it would resolve back to this wrapper's own shim and recurse
// without bound. See dockercompose.RealBinary for the measurement and for
// why the resolved name must not start with "docker-" either.
func execDockerReal(args []string) error {
	dockerPath, err := exec.LookPath(dockercompose.RealBinary)
	if err != nil {
		return fmt.Errorf("%s not found in PATH; the command profile must grant this wrapper the %q command", dockercompose.RealBinary, dockercompose.RealBinary)
	}
	// argv[0] is forced to "docker", not RealBinary: belt-and-braces against
	// docker ever growing a git-style argv[0] dispatch (see
	// dockercompose.RealBinary), and independently right for any usage/error
	// text docker derives from its own program name — without this the agent
	// would see output naming an internal command it cannot run itself. Do
	// not remove this as apparently-redundant.
	argv := append([]string{"docker"}, args...)
	if err := syscall.Exec(dockerPath, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec docker: %w", err)
	}
	return nil
}
