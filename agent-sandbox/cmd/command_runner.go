package cmd

import (
	"context"
	"io"

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

// unavailableRunner stands in for a broker client that could not be built. It
// lets the MCP server start (as it did before the broker existed) while making
// every sandbox-routed command fail with ErrBrokerUnavailable, which the router
// renders as router.SandboxNotRunningHint. A nil runner would instead produce
// the much less useful "no command broker configured" line.
type unavailableRunner struct{ err error }

func (u unavailableRunner) RunSandboxed(_ context.Context, _ []string,
	_ io.Reader, _, _ io.Writer) (int, error) {
	return 0, u.err
}
