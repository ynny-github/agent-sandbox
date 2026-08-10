package cmd

import (
	"context"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
)

// newBrokerCommandRunner returns a runner that sends commands to the host-side
// broker started by `agent-sandbox claude`. The cleanup is a no-op: the client
// opens a connection per command and owns nothing long-lived.
func newBrokerCommandRunner(_ context.Context, _ *config.Config) (router.CommandRunner, func(), error) {
	client, err := broker.NewClientFromEnv()
	if err != nil {
		return nil, nil, err
	}
	return client, func() {}, nil
}
