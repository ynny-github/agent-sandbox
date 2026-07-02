//go:build e2e

package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("safe docker-compose wrapper", func() {
	Context("CLI-level policy", func() {
		It("refuses the run subcommand before touching docker", func() {
			dir := GinkgoT().TempDir()
			stdout, stderr, code := runSafe(dir, "run", "web", "echo", "hi")
			Expect(code).To(Equal(1))
			Expect(stderr).To(ContainSubstring("refused:"))
			Expect(stderr).To(ContainSubstring(`"run" subcommand is not allowed`))
			Expect(stdout).To(BeEmpty())
		})
	})

	Context("model-level policy", func() {
		BeforeEach(func() {
			if !dockerComposeUp {
				Skip("docker compose is not available")
			}
		})

		It("refuses a bind mount that escapes the working directory", func() {
			dir := GinkgoT().TempDir()
			writeCompose(dir, `
services:
  web:
    image: busybox
    volumes:
      - /etc:/host-etc
`)
			_, stderr, code := runSafe(dir, "config")
			Expect(code).To(Equal(1))
			Expect(stderr).To(ContainSubstring("refused:"))
			Expect(stderr).To(ContainSubstring("escapes the work directory"))
		})

		It("passes a safe compose through to real docker compose", func() {
			dir := GinkgoT().TempDir()
			writeCompose(dir, `
services:
  web:
    image: busybox
    volumes:
      - ./data:/data
`)
			stdout, stderr, code := runSafe(dir, "config")
			Expect(code).To(Equal(0), "stderr:\n%s", stderr)
			Expect(stdout).To(ContainSubstring("busybox"))
		})
	})

	Context("help", func() {
		It("shows the wrapper usage on --help", func() {
			dir := GinkgoT().TempDir()
			stdout, stderr, code := runSafe(dir, "--help")
			Expect(code).To(Equal(0))
			Expect(stdout + stderr).To(ContainSubstring("validating that the invocation is safe"))
		})
	})
})
