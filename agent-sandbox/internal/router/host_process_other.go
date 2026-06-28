//go:build !unix

package router

import "os/exec"

func configureProcessGroup(c *exec.Cmd) {}

func commandProcessGroupID(c *exec.Cmd) int {
	return 0
}

func terminateProcessGroup(c *exec.Cmd, _ int) error {
	if c.Process == nil {
		return nil
	}
	return c.Process.Kill()
}
