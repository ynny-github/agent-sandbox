//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// binaryPath is the freshly built agent-sandbox binary, set in BeforeSuite.
var binaryPath string

// dockerComposeUp reports whether "docker compose" is usable; model-level specs
// skip when it is false.
var dockerComposeUp bool

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "safe docker-compose e2e suite")
}

var _ = BeforeSuite(func() {
	// The module and the main package both live at the repo root; only the
	// implementation sits under agent-sandbox/. This test package is at
	// tests/e2e, so the source dir is two levels up.
	wd, err := os.Getwd()
	Expect(err).NotTo(HaveOccurred())
	sourceDir, err := filepath.Abs(filepath.Join(wd, "..", ".."))
	Expect(err).NotTo(HaveOccurred())

	tmpDir, err := os.MkdirTemp("", "agent-sandbox-e2e")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })

	binaryPath = filepath.Join(tmpDir, "agent-sandbox")
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Dir = sourceDir
	out, err := build.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "go build failed:\n%s", string(out))

	dockerComposeUp = exec.Command("docker", "compose", "version").Run() == nil
})
