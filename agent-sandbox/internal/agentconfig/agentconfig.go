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
		"which commands run on the host, which are refused, and the container " +
		"constraints.\n"
}

//go:embed explain.tmpl
var explainTmplText string

// explainTmpl renders the environment explanation. Parsing a static, embedded
// template cannot fail, so template.Must is safe.
var explainTmpl = template.Must(template.New("explain").Parse(explainTmplText))

// explainView is the data handed to explain.tmpl. Network host/CIDR lists are
// pre-joined so the template stays free of helper functions.
type explainView struct {
	Hook         bool
	Allow        []string
	Drop         []string
	Image        string
	NetworkHosts string
	NetworkCIDRs string
}

// Explain renders a Markdown description of the sandbox environment from cfg,
// for the AI agent to read on demand via `agent-sandbox ai explain`. The prose
// lives in explain.tmpl; this function only prepares the view data.
func Explain(cfg *config.Config) string {
	image := strings.TrimSpace(cfg.Sandbox.Container.Image)
	if image == "" {
		image = "(none)"
	}

	view := explainView{
		Hook:         cfg.ToolMode == "hook",
		Allow:        cfg.Sandbox.Command.Allow,
		Drop:         cfg.Sandbox.Command.Drop,
		Image:        image,
		NetworkHosts: strings.Join(cfg.Sandbox.Network.AllowHosts, ", "),
		NetworkCIDRs: strings.Join(cfg.Sandbox.Network.AllowCIDRs, ", "),
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
