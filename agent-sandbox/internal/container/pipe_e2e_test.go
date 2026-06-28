//go:build integration

package container_test

import (
	"context"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/container"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
)

func TestPipe_E2E(t *testing.T) {
	cli := newIntegrationDockerCli(t)
	startComposeService(t)
	exec := container.NewComposeExecutor(cli, testProject(testProjectName))

	s := router.New(router.Config{
		AllowPatterns:   []string{"echo *"}, // echo on host, tr in container
		ContainerRunner: exec,
	})
	res, err := s.RunBuffered(context.Background(), "echo hello | tr a-z A-Z")
	if err != nil {
		t.Fatalf("RunBuffered error: %v", err)
	}
	if res.ExitCode != 0 || string(res.Stdout) != "HELLO\n" {
		t.Fatalf("code=%d stdout=%q, want 0 / %q", res.ExitCode, res.Stdout, "HELLO\n")
	}
}
