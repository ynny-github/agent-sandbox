package claude

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// superviseProcess runs path (with argv = args) as a child process that
// inherits the launcher's real terminal, so the child stays in the pane's
// foreground process group. It returns the child's exit code.
//
// Signal handling:
//   - SIGINT is caught and dropped by the launcher. Because the child shares
//     the foreground process group, the terminal delivers Ctrl+C to the child
//     directly; the launcher must not die first, or the post-exit teardown
//     would be skipped. We install a handler (rather than SIG_IGN, which is
//     inherited across exec and would stop the child from seeing SIGINT).
//   - SIGTERM/SIGHUP are forwarded to the child so it can shut down cleanly.
func superviseProcess(path string, args []string) int {
	cmd := &exec.Cmd{
		Path:   path,
		Args:   args,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Env:    os.Environ(),
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "claude: start failed: %v\n", err)
		return exitCode(err)
	}

	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-sigCh:
				// Drop SIGINT (child gets it via the terminal). Forward the rest.
				if sig == syscall.SIGINT {
					continue
				}
				_ = cmd.Process.Signal(sig)
			case <-done:
				return
			}
		}
	}()

	err := cmd.Wait()
	close(done)
	return exitCode(err)
}

// exitCode maps a (*exec.Cmd).Wait error to a process exit code: nil -> 0,
// a real exit status -> that status, any other error -> 1.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}
