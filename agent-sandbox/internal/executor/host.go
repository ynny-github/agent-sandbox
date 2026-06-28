package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

func RunHost(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("executor: context: %w", err)
	}
	if len(args) == 0 {
		return 0, fmt.Errorf("executor: empty command")
	}

	c := exec.Command(args[0], args[1:]...)
	configureProcessGroup(c)

	stdoutPipe, err := c.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("executor: stdout pipe: %w", err)
	}
	stderrPipe, err := c.StderrPipe()
	if err != nil {
		return 0, fmt.Errorf("executor: stderr pipe: %w", err)
	}

	if err := c.Start(); err != nil {
		return 0, fmt.Errorf("executor: start: %w", err)
	}
	processGroupID := commandProcessGroupID(c)

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = terminateProcessGroup(c, processGroupID)
		case <-done:
		}
	}()
	defer close(done)

	var wg sync.WaitGroup
	copyErrs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := io.Copy(stdout, stdoutPipe); err != nil {
			copyErrs <- fmt.Errorf("stdout: %w", err)
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := io.Copy(stderr, stderrPipe); err != nil {
			copyErrs <- fmt.Errorf("stderr: %w", err)
		}
	}()

	wg.Wait()
	close(copyErrs)

	waitErr := c.Wait()
	copyErr := joinCopyErrors(copyErrs)
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			if copyErr != nil {
				return 124, fmt.Errorf("executor: copy output: %w", copyErr)
			}
			return 124, nil
		}
		if waitErr != nil {
			return exitCode(waitErr), errors.Join(fmt.Errorf("executor: context: %w", ctxErr), copyErr)
		}
		return 0, errors.Join(fmt.Errorf("executor: context: %w", ctxErr), copyErr)
	}
	if copyErr != nil {
		return exitCode(waitErr), fmt.Errorf("executor: copy output: %w", copyErr)
	}
	if waitErr != nil {
		return exitCode(waitErr), waitError(waitErr)
	}
	return 0, nil
}

func joinCopyErrors(errs <-chan error) error {
	var joined error
	for err := range errs {
		joined = errors.Join(joined, err)
	}
	return joined
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return 0
}

func waitError(err error) error {
	if err == nil {
		return nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() >= 0 {
			return nil
		}
		return fmt.Errorf("executor: wait: %w", err)
	}
	return fmt.Errorf("executor: wait: %w", err)
}
