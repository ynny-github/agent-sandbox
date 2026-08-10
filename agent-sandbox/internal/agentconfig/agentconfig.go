package agentconfig

import (
	"bytes"
	_ "embed"
	"strings"
	"text/template"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

// Pointer returns the short guidance injected into the agent's system prompt at
// launch. It intentionally carries no allow/drop detail — only a pointer to the
// live `agent-sandbox ai explain` command — to keep the system prompt small.
func Pointer() string {
	return "## agent-sandbox environment\n\n" +
		"This project routes your shell commands through the agent-sandbox " +
		"sandbox. Run `agent-sandbox ai explain` to learn how to run commands, " +
		"which commands run on the host, which are refused, and the sandbox " +
		"network constraints.\n"
}

//go:embed explain.tmpl
var explainTmplText string

// explainTmpl renders the environment explanation. Parsing a static, embedded
// template cannot fail, so template.Must is safe.
var explainTmpl = template.Must(template.New("explain").Parse(explainTmplText))

// SafeCommand describes one `agent-sandbox safe <tool>` wrapper for the explain
// output. Use is the subcommand's usage (e.g. "git [args...]"); Short is its
// one-line description.
type SafeCommand struct {
	Use   string
	Short string
}

// explainView is the data handed to explain.tmpl.
type explainView struct {
	Hook         bool
	Allow        []string
	Drop         []config.DropRule
	Safe         []SafeCommand
	AllowDomains []string
}

// Explain renders a Markdown description of the sandbox environment from cfg,
// for the AI agent to read on demand via `agent-sandbox ai explain`. The prose
// lives in explain.tmpl; this function only prepares the view data. The caller
// passes the available `safe` wrappers (from the live command tree) so the
// explain output points agents at them.
func Explain(cfg *config.Config, safe ...SafeCommand) string {
	view := explainView{
		Hook:         cfg.ToolMode == "hook",
		Allow:        cfg.Sandbox.Command.Allow,
		Drop:         cfg.Sandbox.Command.Drop,
		Safe:         safe,
		AllowDomains: cfg.Sandbox.Network.AllowDomains,
	}

	var buf bytes.Buffer
	if err := explainTmpl.Execute(&buf, view); err != nil {
		// The template and its data are static; execution cannot fail. A panic
		// here would mean the embedded template was edited into an invalid state,
		// which the package tests catch immediately.
		panic("agentconfig: render explain template: " + err.Error())
	}
	return strings.TrimRight(buf.String(), "\n") + "\n"
}
