package cmd

import (
	"context"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxlifecycle"
)

// newBrokerCommandRunner builds a Docker-Compose-backed command runner.
// The returned cleanup closes the Docker client and must be called by the
// caller once the runner is no longer needed.
func newBrokerCommandRunner(ctx context.Context, cfg *config.Config) (router.CommandRunner, func(), error) {
	executor, _, cleanup, err := sandboxlifecycle.NewExecutor(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return executor, cleanup, nil
}
