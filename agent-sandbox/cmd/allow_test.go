package cmd

import (
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
)

func TestAllowPatterns_IncludesBuiltinSelfAllow(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Command.Allow = []string{"git *"}

	got := allowPatterns(cfg)
	for _, want := range []string{"agent-sandbox ai", "agent-sandbox ai *", "git *"} {
		if !argsContain(got, want) {
			t.Errorf("allowPatterns missing %q; got %v", want, got)
		}
	}
}

func TestAllowPatterns_DoesNotMutateConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Command.Allow = []string{"git *"}
	_ = allowPatterns(cfg)
	if len(cfg.Sandbox.Command.Allow) != 1 || cfg.Sandbox.Command.Allow[0] != "git *" {
		t.Errorf("config allow was mutated: %v", cfg.Sandbox.Command.Allow)
	}
}

func TestAllowPatterns_RoutesAiExplainToHost(t *testing.T) {
	cfg := &config.Config{}
	decision, _ := router.Route("agent-sandbox ai explain", allowPatterns(cfg), cfg.Sandbox.Command.Drop)
	if decision != "host" {
		t.Errorf("agent-sandbox ai explain routed to %q, want host", decision)
	}
}

func TestAllowPatterns_IncludesBuiltinSafeSelfAllow(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Command.Allow = []string{"git *"}

	got := allowPatterns(cfg)
	for _, want := range []string{"agent-sandbox safe", "agent-sandbox safe *"} {
		if !argsContain(got, want) {
			t.Errorf("allowPatterns missing %q; got %v", want, got)
		}
	}
}

func TestAllowPatterns_RoutesSafeWrappersToHost(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Command.Drop = []string{"git push --force*"}

	for _, cmd := range []string{
		"agent-sandbox safe git status",
		"agent-sandbox safe gh pr list",
		"agent-sandbox safe git push --force",
	} {
		decision, _ := router.Route(cmd, allowPatterns(cfg), cfg.Sandbox.Command.Drop)
		if decision != "host" {
			t.Errorf("%q routed to %q, want host", cmd, decision)
		}
	}
}

func argsContain(args []string, target string) bool {
	for _, a := range args {
		if a == target {
			return true
		}
	}
	return false
}
