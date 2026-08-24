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
	// OutsideWrite / OutsideRead are the paths a sandboxed command reaches
	// beyond its working directory, resolved from the config rather than
	// described in the abstract: which grants apply depends on which section
	// they were written in, so only the resolved lists answer the question an
	// agent actually has.
	OutsideWrite []string
	OutsideRead  []string
	// Resolved reports whether the two lists above were resolved at all, so the
	// template can distinguish "nothing outside the working directory" from
	// "could not tell".
	Resolved bool
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
		AllowDomains: cfg.Sandbox.Command.Network.AllowDomains,
	}
	// A resolve error means an unknown capability name, which stops
	// `agent-sandbox claude` at launch — no session can reach this code with
	// such a config. Should one ever get here, fall back to the prose above the
	// list rather than printing a wrong (empty) list as if it were resolved.
	if grants, err := sandboxhost.CommandFilesystemGrants(cfg); err == nil {
		view.OutsideWrite = grants.Write
		view.OutsideRead = grants.Read
		view.Resolved = true
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
