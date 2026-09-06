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

An unrecognized global docker flag (one not in this wrapper's own list of
docker's "--config"/"-c"/"-H"/"-l"/"--tls*"/"-D"/"-v"/"-h" family) is refused
for every invocation, before either check below runs, rather than risking a
subcommand hidden behind a flag this wrapper does not know.

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
  - "run" and "exec" are refused outright, including as "docker container run"
    / "docker container exec" (the equivalent management-command form);
  - "--privileged" is refused, including its attached "=<bool>" form (e.g.
    "--privileged=true"); an explicit "--privileged=false" is allowed, since
    it turns privileged mode off, wherever it appears — not only with "run";
  - a "-v"/"--volume"/"--mount" bind of a host path outside the current
    working directory, or of the Docker socket, is refused wherever it
    appears. A relative source is resolved against the working directory
    first, the same as docker itself does for "--mount". "-v" is also
    checked inside a short-flag cluster (e.g. "-tv /host:/x") and its
    attached "=" form (e.g. "-v=/host:/x", "-tv=/host:/x"), by looking for a
    "v" in the cluster; a cluster where an earlier flag also consumes a
    value of its own (e.g. "-ev", where "v" is "-e"'s one-character value)
    can be misread as "-v" instead — this is a known, narrow gap, and such
    an invocation may be refused (or, rarely, checked against the wrong
    value) rather than silently passed.
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
		fmt.Fprintf(cmd.ErrOrStderr(), "blocked: unrecognized global docker flag %q is not allowed\n", unrecognized)
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
				"blocked: a global docker flag (%s) before \"compose\" is not allowed: it would validate and run the resolved compose model against a different daemon than the default one\n",
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
			fmt.Fprintf(cmd.ErrOrStderr(), "blocked: %s\n", v)
		}
		os.Exit(1)
	}
	return execDockerReal(args)
}

// dockerDangerousSubcommands mirrors, at the plain "docker" level, the
// dockercompose.CheckCLI denial of subcommands that are entrypoints for
// arbitrary command execution.
var dockerDangerousSubcommands = map[string]bool{"run": true, "exec": true}

// effectiveSubcommand collapses docker's "container <verb>" management-
// command aliases to their top-level equivalents, so "docker container run"
// and "docker container exec" are recognized the same as "docker run" and
// "docker exec" (measured (review): both are live aliases for the same
// entrypoints). Only these two verbs are collapsed — they are the ones this
// package's checks must refuse outright; other "docker container <verb>"
// forms (ls, stop, rm, ...) are not entrypoints for arbitrary execution and
// are left alone.
func effectiveSubcommand(sub string, rest []string) string {
	if sub == "container" && len(rest) > 1 && (rest[1] == "run" || rest[1] == "exec") {
		return rest[1]
	}
	return sub
}

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
// to catch it. See the command's Long help text for the short-flag-cluster
// caveat on "-v".
func dockerCLIViolations(sub string, rest []string, cwd string) []string {
	var out []string
	if eff := effectiveSubcommand(sub, rest); dockerDangerousSubcommands[eff] {
		out = append(out, fmt.Sprintf("%q subcommand is not allowed", eff))
	}
	for i, a := range rest {
		switch {
		case a == "--privileged", strings.HasPrefix(a, "--privileged="):
			if privilegedFlagIsTrue(a) {
				out = append(out, "--privileged is not allowed")
			}
		case a == "--volume":
			if i+1 < len(rest) {
				if v := checkVolumeSpec(rest[i+1], cwd); v != "" {
					out = append(out, v)
				}
			}
		case strings.HasPrefix(a, "--volume="):
			if v := checkVolumeSpec(strings.TrimPrefix(a, "--volume="), cwd); v != "" {
				out = append(out, v)
			}
		case len(a) >= 2 && a[0] == '-' && a[1] != '-':
			if val, ok := shortFlagVolumeValue(a, rest, i); ok && val != "" {
				if v := checkVolumeSpec(val, cwd); v != "" {
					out = append(out, v)
				}
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

// privilegedFlagIsTrue reports whether a is "--privileged" or an attached
// "--privileged=<value>" whose value is not one of pflag's recognized false
// spellings. Docker's --privileged is a pflag bool, which accepts the
// attached form (measured (review): "--privileged=true" was not previously
// recognized at all). An unparseable value defaults to true — fail closed,
// since docker's own parser would refuse it before ever reaching this far,
// and "unparseable" is not evidence that privileged mode is off.
func privilegedFlagIsTrue(a string) bool {
	val, ok := strings.CutPrefix(a, "--privileged=")
	if !ok {
		return a == "--privileged"
	}
	switch val {
	case "false", "False", "FALSE", "0", "f", "F":
		return false
	default:
		return true
	}
}

// shortFlagVolumeValue reports whether short-flag token a (e.g. "-v",
// "-tv", "-v/host:/x", "-itv", "-v=/host:/x") includes docker's
// "-v"/--volume short flag, and returns its value: whatever follows the
// first "v" within the same token — with a leading "=" stripped, since
// pflag (docker's flag library) accepts "-f=value" for a short flag exactly
// as it accepts "--flag=value" for a long one — or the next argv token when
// nothing follows there.
//
// Without stripping the "=", "-v=/:/host" and "-tv=/:/host" both extracted
// the value as the literal string "=/:/host": checkVolumeSpec's own ":"
// split then produced a source of "=", which looksLikeHostPath accepted
// (it contains no "/", but the check at the time keyed only on absence of
// "/" — the bug predates this method's own leading-"/" check too) and
// checkBindSource joined onto cwd, so the join landed *inside* cwd and
// nothing was ever flagged as escaping — while the intended target, "/",
// was never inspected at all. Reachable via "docker create -v=/:/host
// alpine" followed by "docker start", since "create" is not itself a
// refused subcommand.
//
// This is still a heuristic, not a full short-flag-cluster parser: pflag's
// real rule is that once a value-taking flag is reached in a cluster,
// everything remaining in that token (or the next token, if nothing
// remains) is its value, and telling the cluster's real "-v" apart from an
// earlier value-taking flag whose own attached value happens to contain the
// letter "v" (e.g. "-ev", where "v" is "-e"'s value, not a second flag)
// requires knowing every relevant flag's type, which this package does not
// enumerate. Finding the first "v" cannot miss a real "-v" that is
// present — an earlier value flag would have consumed the token before a
// later "-v" could appear at all — so misattribution, when it happens,
// inspects the wrong string as a mount spec rather than skipping a real one
// (see the command's Long help text). That property is about which flag a
// found value is attributed to; it says nothing about whether a value this
// function does correctly attribute to "-v" is then checked correctly —
// that is checkVolumeSpec/checkBindSource's job, and the leading-"=" bug
// above was a defect in exactly that half, not in attribution.
func shortFlagVolumeValue(a string, rest []string, i int) (value string, ok bool) {
	idx := strings.IndexByte(a, 'v')
	if idx < 1 {
		return "", false
	}
	if idx+1 < len(a) {
		return strings.TrimPrefix(a[idx+1:], "="), true
	}
	if i+1 < len(rest) {
		return rest[i+1], true
	}
	return "", true
}

// checkVolumeSpec applies the same rule dockercompose.CheckModel applies to
// a bind mount's source to a "-v"/"--volume" spec ("SRC:DST[:OPTS]"): refuse
// the Docker socket, refuse a host path outside cwd (resolving a relative
// one against it first), and let a named volume through untouched.
//
// A bare "SRC" with no ":" at all is not this: docker treats a single path
// with no destination as an anonymous volume's *container* path, not a host
// source — there is nothing to check.
func checkVolumeSpec(spec, cwd string) string {
	idx := strings.IndexByte(spec, ':')
	if idx < 0 {
		return ""
	}
	return checkBindSource(spec[:idx], cwd)
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

// looksLikeHostPath reports whether a bind source names a filesystem path
// rather than a named-volume identifier: docker treats a bare name (no "/",
// and not "." or "..") as a named volume, and anything else — absolute, or
// relative and resolved against the caller's own cwd — as a host path.
// Measured (review): "--mount type=bind,src=./relx,dst=/x" is accepted and
// resolved relative to cwd, not rejected for lacking a leading "/" as this
// package previously assumed. The same resolution is applied to "-v" here
// too, out of caution, though only the "--mount" case was itself measured.
func looksLikeHostPath(src string) bool {
	return strings.ContainsRune(src, '/') || src == "." || src == ".."
}

// checkBindSource is the shared rule: a bare name is a named volume and
// passes untouched (see looksLikeHostPath); a relative path is resolved
// against cwd first; the Docker socket and any host path outside cwd are
// refused, matching dockercompose.CheckModel's own bind checks.
func checkBindSource(src, cwd string) string {
	if !looksLikeHostPath(src) {
		return ""
	}
	abs := src
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, abs)
	}
	if filepath.Base(filepath.Clean(abs)) == "docker.sock" {
		return fmt.Sprintf("bind mount of the docker socket %q is not allowed", src)
	}
	if !safe.PathWithin(safe.RealPath(cwd), safe.RealPath(abs)) {
		return fmt.Sprintf("bind mount %q escapes the work directory", src)
	}
	return ""
}

// runSafeDockerCompose validates a "docker compose" invocation (args is
// everything after the "compose" token) before running it.
func runSafeDockerCompose(cmd *cobra.Command, args []string) error {
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
			fmt.Fprintf(cmd.ErrOrStderr(), "blocked: %s\n", v.Setting)
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
