package container

import (
	"context"
	"fmt"

	"github.com/docker/cli/cli/command"
	dockernetwork "github.com/docker/docker/api/types/network"
)

// egressConflict reports whether attaching an egress-restricted sandbox
// (allowExternal=false) to a network that can reach outside
// (networkInternal=false) would silently defeat isolation.
func egressConflict(allowExternal, networkInternal bool) bool {
	return !allowExternal && !networkInternal
}

// CheckNetworkConsistency inspects projectNetwork and refuses attachment when it
// would leak egress. A blank projectNetwork is a no-op (nil).
func CheckNetworkConsistency(ctx context.Context, dockerCLI command.Cli, allowExternal bool, projectNetwork string) error {
	if projectNetwork == "" {
		return nil
	}
	inspect, err := dockerCLI.Client().NetworkInspect(ctx, projectNetwork, dockernetwork.InspectOptions{})
	if err != nil {
		return fmt.Errorf("sandbox: inspect project network %q: %w", projectNetwork, err)
	}
	if egressConflict(allowExternal, inspect.Internal) {
		return fmt.Errorf("sandbox: allow_external=false but project network %q is externally reachable (Internal=false); refusing to attach and leak egress. Make the project network internal, or set allow_external=true", projectNetwork)
	}
	return nil
}
