package cmd

import (
	"context"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxlifecycle"
)

// newComposeContainerRunner builds a Docker-Compose-backed container runner.
// The returned cleanup closes the Docker client and must be called by the
// caller once the runner is no longer needed.
func newComposeContainerRunner(ctx context.Context, cfg *config.Config) (router.ContainerRunner, func(), error) {
	executor, _, cleanup, err := sandboxlifecycle.NewExecutor(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return executor, cleanup, nil
}
