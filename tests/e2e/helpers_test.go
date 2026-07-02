//go:build e2e

package e2e

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/gomega"
)

// runSafe runs "<binary> safe docker-compose <args...>" with its working
// directory set to dir (the mount boundary the wrapper enforces) and returns
// captured stdout, stderr, and the process exit code.
func runSafe(dir string, args ...string) (stdout, stderr string, exitCode int) {
	fullArgs := append([]string{"safe", "docker-compose"}, args...)
	cmd := exec.Command(binaryPath, fullArgs...)
	cmd.Dir = dir

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			// A non-exit error (e.g. binary missing) is a harness bug, not a
			// wrapper decision — fail the spec loudly.
			ExpectWithOffset(1, err).NotTo(HaveOccurred())
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// writeCompose writes body to dir/compose.yaml so "docker compose" auto-discovers it.
func writeCompose(dir, body string) {
	err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(body), 0o644)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}
