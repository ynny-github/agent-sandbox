package agentconfig

import (
	"fmt"
	"strings"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

// Pointer returns the short guidance injected into the agent's system prompt at
// launch. It intentionally carries no allow/drop detail — only a pointer to the
// live `agent-sandbox ai explain` command — to keep the system prompt small.
func Pointer() string {
	return "## agent-sandbox environment\n\n" +
		"This project routes your shell commands through the agent-sandbox " +
		"sandbox. Run `agent-sandbox ai explain` to learn how to run commands, " +
		"which commands run on the host, which are refused, and the container " +
		"constraints.\n"
}

// Explain renders a Markdown description of the sandbox environment from cfg,
// for the AI agent to read on demand via `agent-sandbox ai explain`.
func Explain(cfg *config.Config) string {
	var b strings.Builder
	b.WriteString("# agent-sandbox environment\n\n")

	b.WriteString("## Running commands\n")
	if cfg.ToolMode == "hook" {
		b.WriteString(`- tool_mode = "hook": run commands normally; a PreToolUse hook routes them through the sandbox.` + "\n")
	} else {
		b.WriteString(`- tool_mode = "mcp": use the ` + "`run_command`" + ` MCP tool to run commands.` + "\n")
	}
	b.WriteString("- Allowed commands run on the host; all others run in an isolated container.\n")
	b.WriteString("- Output is written to files; read the returned paths for stdout/stderr.\n\n")

	b.WriteString("## Commands that run on the host (allow)\n")
	writeList(&b, cfg.Sandbox.Command.Allow)
	b.WriteString("\n")

	b.WriteString("## Refused commands (drop)\n")
	b.WriteString("These run on neither host nor container:\n")
	writeList(&b, cfg.Sandbox.Command.Drop)
	b.WriteString("\n")

	b.WriteString("## Sandbox container\n")
	image := strings.TrimSpace(cfg.Sandbox.Container.Image)
	if image == "" {
		image = "(none)"
	}
	fmt.Fprintf(&b, "- image: %s\n", image)
	writeNetwork(&b, cfg.Sandbox.Network)

	return b.String()
}

func writeList(b *strings.Builder, items []string) {
	if len(items) == 0 {
		b.WriteString("- (none)\n")
		return
	}
	for _, it := range items {
		fmt.Fprintf(b, "- %s\n", it)
	}
}

func writeNetwork(b *strings.Builder, net config.NetworkConfig) {
	if len(net.AllowHosts) == 0 && len(net.AllowCIDRs) == 0 {
		b.WriteString("- network: none\n")
		return
	}
	if len(net.AllowHosts) > 0 {
		fmt.Fprintf(b, "- network allow_hosts: %s\n", strings.Join(net.AllowHosts, ", "))
	}
	if len(net.AllowCIDRs) > 0 {
		fmt.Fprintf(b, "- network allow_cidrs: %s\n", strings.Join(net.AllowCIDRs, ", "))
	}
}
