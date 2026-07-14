package cmd

import "github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"

// builtinAllowPatterns are always-allowed self-invocations: `ai` so the agent
// can read `ai explain`, and `safe` because the safe wrappers validate the
// underlying tool call and refuse dangerous invocations before running. They
// are never written to the TOML file.
var builtinAllowPatterns = []string{
	"agent-sandbox ai", "agent-sandbox ai *",
	"agent-sandbox safe", "agent-sandbox safe *",
}

// allowPatterns returns the effective host-allow patterns: the built-in
// self-allow patterns followed by the operator-configured allow list. It never
// mutates cfg.
func allowPatterns(cfg *config.Config) []string {
	out := make([]string, 0, len(builtinAllowPatterns)+len(cfg.Sandbox.Command.Allow))
	out = append(out, builtinAllowPatterns...)
	out = append(out, cfg.Sandbox.Command.Allow...)
	return out
}
