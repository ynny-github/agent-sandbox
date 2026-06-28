//go:build unix

package router

import (
	"os/exec"
	"syscall"
)

func configureProcessGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func commandProcessGroupID(c *exec.Cmd) int {
	if c.Process == nil {
		return 0
	}
	return c.Process.Pid
}

func terminateProcessGroup(c *exec.Cmd, pgid int) error {
	if pgid > 0 {
		return syscall.Kill(-pgid, syscall.SIGKILL)
	}
	if c.Process == nil {
		return nil
	}
	return c.Process.Kill()
}
