// Package sandboxlifecycle decides whether the Docker sandbox needs starting
// and, when it does, brings it up — reporting whether the caller now owns it.
// The ownership signal lets callers tear down only the sandbox they started.
package sandboxlifecycle

import (
	"context"
	"fmt"
	"time"
)

// Sandbox is the subset of the compose executor the lifecycle logic needs.
// *container.ComposeExecutor satisfies it.
type Sandbox interface {
	IsRunning(context.Context) (bool, error)
	Up(context.Context) error
	ApplyNetworkPolicy(context.Context) error
	Down(context.Context) error
}

// Ensure makes sb running. If it is already running, Ensure does nothing and
// reports startedByUs=false (the caller must not tear it down). If it was not
// running, Ensure brings it up and applies the network policy, reporting
// startedByUs=true. Any failure is returned and startedByUs is false.
func Ensure(ctx context.Context, sb Sandbox) (startedByUs bool, err error) {
	checkCtx, checkCancel := context.WithTimeout(ctx, 10*time.Second)
	defer checkCancel()
	running, err := sb.IsRunning(checkCtx)
	if err != nil {
		return false, fmt.Errorf("sandbox status: %w", err)
	}
	if running {
		return false, nil
	}

	upCtx, upCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer upCancel()
	if err := sb.Up(upCtx); err != nil {
		return false, fmt.Errorf("sandbox up: %w", err)
	}

	policyCtx, policyCancel := context.WithTimeout(ctx, 30*time.Second)
	defer policyCancel()
	if err := sb.ApplyNetworkPolicy(policyCtx); err != nil {
		return false, fmt.Errorf("network policy: %w", err)
	}
	return true, nil
}
