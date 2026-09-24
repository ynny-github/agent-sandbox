package agentconfig

import (
	"bytes"
	_ "embed"
	"strings"
	"text/template"

	"github.com/ynny-github/agent-sandbox/internal/config"
)

// Pointer returns the short guidance injected into the agent's system prompt at
// launch. It intentionally carries no detail about the command profile itself
// — only a pointer to the live `agent-sandbox ai explain` command — to keep
// the system prompt small.
func Pointer() string {
	return "## agent-sandbox environment\n\n" +
		"This project routes your shell commands through an exec daemon. " +
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
	// ConfigPath is the config file actually loaded, not the default name: the
	// agent is being told which file to edit, and --config can move it.
	ConfigPath string
	// ProfilePath is the command profile execd runs commands under
	// (cfg.CommandProfilePath()). agent-sandbox neither generates nor reads its
	// contents; this is a pointer, not a description.
	ProfilePath string
	// AgentProfilePath is the nono profile the launched agent itself runs
	// under (cfg.AgentProfilePath). Like ProfilePath it is a pointer, not a
	// description: agent-sandbox neither generates nor reads it.
	AgentProfilePath string
}

// Explain renders a Markdown description of the sandbox environment from cfg,
// for the AI agent to read on demand via `agent-sandbox ai explain`. The prose
// lives in explain.tmpl; this function only prepares the view data. The caller
// passes the config path it loaded cfg from, so the editing section names the
// file the agent must actually edit.
//
// It does not read either profile. What the profiles grant is nono's to answer,
// and the template points the agent at `nono why` and `nono profile show`
// instead of restating it — a restatement drifts, and drifts into a confident
// lie. See docs/superpowers/specs/2026-09-12-git-goes-to-command-policy.md.
func Explain(cfg *config.Config, configPath string) string {
	view := explainView{
		ConfigPath:       configPath,
		ProfilePath:      cfg.CommandProfilePath(),
		AgentProfilePath: cfg.AgentProfilePath("claude"),
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
