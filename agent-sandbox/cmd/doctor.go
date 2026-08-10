// agent-sandbox/cmd/doctor.go
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/policysnapshot"
)

var errDoctorChecksFailed = errors.New("doctor: checks failed")

var doctorCmd = &cobra.Command{
	Use:           "doctor",
	Short:         "Check whether agent-sandbox's external dependencies are usable",
	SilenceErrors: true, // suppress cobra's automatic "Error: ..." print; rootCmd already silences usage
	RunE:          runDoctor,
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

func runDoctor(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	results := []checkResult{
		checkNono(ctx),
		checkBrokerSocketDir(),
	}
	renderResults(cmd.OutOrStdout(), results)
	for _, r := range results {
		if !r.ok {
			return errDoctorChecksFailed
		}
	}
	return nil
}

type checkResult struct {
	name    string
	ok      bool
	details []string // each entry is a "key: value" line, no leading indent
	hint    string   // only meaningful when ok == false
}

var (
	lookPath   = exec.LookPath
	runCommand = defaultRunCommand
)

func defaultRunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
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

// checkBrokerSocketDir verifies the launcher can create the broker socket. A
// failure here means `agent-sandbox claude` cannot start the command broker,
// so every sandboxed command would fail.
//
// os.MkdirAll alone is not sufficient: it returns nil for a directory that
// already exists, regardless of its permission bits, so an existing
// unwritable state dir (e.g. left at mode 0500 by something else) would pass
// even though the launcher cannot actually create a socket file inside it.
// Binding a throwaway unix socket the same way the launcher does also catches
// the ~104-byte sun_path limit a plain write wouldn't.
func checkBrokerSocketDir() checkResult {
	const name = "command broker"
	dir, err := policysnapshot.StateDir()
	if err != nil {
		return checkResult{name: name, ok: false,
			details: []string{fmt.Sprintf("error: %v", err)},
			hint:    "set HOME or XDG_STATE_HOME"}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return checkResult{name: name, ok: false,
			details: []string{fmt.Sprintf("error: %v", err)},
			hint:    fmt.Sprintf("make %s writable", dir)}
	}

	sockPath := filepath.Join(dir, fmt.Sprintf("doctor-%d.sock", os.Getpid()))
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return checkResult{name: name, ok: false,
			details: []string{fmt.Sprintf("error: %v", err)},
			hint: fmt.Sprintf(
				"make %s writable, or shorten XDG_STATE_HOME (unix socket paths are limited to ~104 bytes)", dir),
		}
	}
	l.Close()
	os.Remove(sockPath)

	return checkResult{name: name, ok: true,
		details: []string{fmt.Sprintf("socket dir: %s", dir)}}
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
