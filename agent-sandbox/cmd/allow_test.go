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
