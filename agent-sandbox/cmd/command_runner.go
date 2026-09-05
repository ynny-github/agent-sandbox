package cmd

import (
	"context"
	"io"
)

// unavailableRunner stands in for a broker client that could not be built. It
// lets the MCP server start (as it did before the broker existed) while making
// every sandbox-routed command fail with ErrBrokerUnavailable, which the caller
// renders as broker.SandboxNotRunningHint. A nil runner would instead produce
// a nil-pointer panic.
type unavailableRunner struct{ err error }

func (u unavailableRunner) RunCommand(_ context.Context, _ string,
	_ io.Reader, _, _ io.Writer) (int, error) {
	return 0, u.err
}
