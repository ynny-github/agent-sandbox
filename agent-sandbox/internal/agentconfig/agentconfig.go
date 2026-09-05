package agentconfig

import (
	"bytes"
	_ "embed"
	"strings"
	"text/template"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxhost"
)

// Pointer returns the short guidance injected into the agent's system prompt at
// launch. It intentionally carries no detail about the command profile itself
// — only a pointer to the live `agent-sandbox ai explain` command — to keep
// the system prompt small.
func Pointer() string {
	return "## agent-sandbox environment\n\n" +
		"This project routes your shell commands through a command broker. " +
		"Run `agent-sandbox ai explain` to learn how commands run and where " +
		"the sandbox is configured.\n"
}

//go:embed explain.tmpl
var explainTmplText string

// explainTmpl renders the environment explanation. Parsing a static, embedded
// template cannot fail, so template.Must is safe.
var explainTmpl = template.Must(template.New("explain").Parse(explainTmplText))

// explainView is the data handed to explain.tmpl.
type explainView struct {
	Hook bool
	// ConfigPath is the config file actually loaded, not the default name: the
	// agent is being told which file to edit, and --config can move it.
	ConfigPath string
	// ProfilePath is the command profile the broker runs commands under
	// (cfg.CommandProfilePath()). agent-sandbox does not generate or read its
	// contents, so this is a pointer, not a description of what it allows.
	ProfilePath string
	// Capabilities is the catalog's capability names, so the editing section
	// lists what may actually be written rather than a prose sample that goes
	// stale when a bundle is added.
	Capabilities []string
}

// Explain renders a Markdown description of the sandbox environment from cfg,
// for the AI agent to read on demand via `agent-sandbox ai explain`. The prose
// lives in explain.tmpl; this function only prepares the view data. The caller
// passes the config path it loaded cfg from, so the editing section names the
// file the agent must actually edit.
func Explain(cfg *config.Config, configPath string) string {
	view := explainView{
		Hook:         cfg.ToolMode == "hook",
		ConfigPath:   configPath,
		ProfilePath:  cfg.CommandProfilePath(),
		Capabilities: sandboxhost.CapabilityNames(),
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
