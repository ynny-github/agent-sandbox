// agent-sandbox/cmd/doctor.go
package cmd

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/docker/cli/cli/command"
	cliflags "github.com/docker/cli/cli/flags"
)

type checkResult struct {
	name    string
	ok      bool
	details []string // each entry is a "key: value" line, no leading indent
	hint    string   // only meaningful when ok == false
}

var (
	lookPath         = exec.LookPath
	runCommand       = defaultRunCommand
	pingDockerDaemon = defaultPingDockerDaemon
)

func defaultRunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func defaultPingDockerDaemon(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cli, err := command.NewDockerCli()
	if err != nil {
		return "", err
	}
	if err := cli.Initialize(cliflags.NewClientOptions()); err != nil {
		return "", err
	}
	defer cli.Client().Close()
	if _, err := cli.Client().Ping(ctx); err != nil {
		return "", err
	}
	return "reachable", nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func checkNono(ctx context.Context) checkResult {
	const name = "nono"
	path, err := lookPath("nono")
	if err != nil {
		return checkResult{
			name:    name,
			ok:      false,
			details: []string{fmt.Sprintf("error: %v", err)},
			hint:    "install nono and make sure it is on PATH",
		}
	}
	out, err := runCommand(ctx, "nono", "--version")
	if err != nil {
		return checkResult{
			name:    name,
			ok:      false,
			details: []string{fmt.Sprintf("path: %s", path), fmt.Sprintf("error: \"nono --version\" failed: %v", err)},
			hint:    "verify the nono binary is functional (try running \"nono --version\" manually)",
		}
	}
	return checkResult{
		name:    name,
		ok:      true,
		details: []string{fmt.Sprintf("path: %s", path), fmt.Sprintf("version: %s", firstLine(string(out)))},
	}
}

func checkDockerCompose(ctx context.Context) checkResult {
	const name = "docker compose"
	out, err := runCommand(ctx, "docker", "compose", "version")
	if err != nil {
		details := []string{fmt.Sprintf("error: \"docker compose version\" failed: %v", err)}
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			details = append(details, fmt.Sprintf("output: %s", firstLine(trimmed)))
		}
		return checkResult{
			name:    name,
			ok:      false,
			details: details,
			hint:    "install Docker (or a compatible CLI like colima/podman) so that \"docker compose version\" succeeds",
		}
	}
	return checkResult{
		name:    name,
		ok:      true,
		details: []string{firstLine(string(out))},
	}
}

func checkDockerDaemon(ctx context.Context) checkResult {
	const name = "docker daemon"
	detail, err := pingDockerDaemon(ctx)
	if err != nil {
		return checkResult{
			name:    name,
			ok:      false,
			details: []string{fmt.Sprintf("error: %v", err)},
			hint:    "start the Docker daemon (e.g. open Docker Desktop, or \"colima start\")",
		}
	}
	return checkResult{
		name:    name,
		ok:      true,
		details: []string{detail},
	}
}

func renderResults(w io.Writer, results []checkResult) {
	failed := 0
	for _, r := range results {
		label := "[OK]"
		if !r.ok {
			label = "[NG]"
			failed++
		}
		fmt.Fprintf(w, "%s %s\n", label, r.name)
		for _, d := range r.details {
			fmt.Fprintf(w, "     %s\n", d)
		}
		if !r.ok && r.hint != "" {
			fmt.Fprintf(w, "     hint: %s\n", r.hint)
		}
		fmt.Fprintln(w)
	}
	if failed == 0 {
		fmt.Fprintln(w, "doctor: all checks passed")
	} else {
		fmt.Fprintf(w, "doctor: %d of %d checks failed\n", failed, len(results))
	}
}
