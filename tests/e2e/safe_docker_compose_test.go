//go:build e2e

package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("safe docker-compose wrapper", func() {
	Context("help", func() {
		It("shows the wrapper usage on --help", func() {
			dir := GinkgoT().TempDir()
			stdout, stderr, code := runSafe(dir, "--help")
			Expect(code).To(Equal(0))
			Expect(stdout + stderr).To(ContainSubstring("validating that the invocation is safe"))
		})
	})
})
