package agentconfig

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"sort"
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
	// (cfg.CommandProfilePath()). agent-sandbox does not generate its contents;
	// this is a pointer, plus what could be read out of it, not a description
	// of everything nono's own schema can express.
	ProfilePath string
	// PolicyCommands are the commands with their own child sandbox: either
	// nono's own invocation_policy argv rules, or (see policyCommandView.
	// Wrapper) a "safe <tool>" wrapper that parses the real invocation in Go.
	// FloorCommands run in the broker's own sandbox. An agent that knows
	// which is which can tell a refusal from a bug.
	PolicyCommands []policyCommandView
	FloorPaths     []string
	// Capabilities is the catalog's capability names, so the editing section
	// lists what may actually be written rather than a prose sample that goes
	// stale when a bundle is added.
	Capabilities []string
}

// policyCommandView is one command_policies.commands entry that is not the
// broker's own (session) entry: a policy command, reachable only through its
// nono-generated shim.
type policyCommandView struct {
	Name    string
	Denials []string // each is "<matcher>: <reason>", or just "<matcher>" when the profile carries no reason
	// Wrapper is the "safe <tool>" subcommand this entry's argv_prepend
	// inserts (e.g. "safe git"), when the profile binds this name to the
	// agent-sandbox binary itself rather than to the real tool. Empty for a
	// command pinned directly at its own binary. A wrapper-bound command's
	// rule set lives in Go, not in this profile's (possibly absent)
	// invocation_policy, so Denials alone would understate — or, now that
	// git's invocation_policy is gone, entirely miss — what it refuses.
	Wrapper string
}

// Explain renders a Markdown description of the sandbox environment from cfg,
// for the AI agent to read on demand via `agent-sandbox ai explain`. The prose
// lives in explain.tmpl; this function only prepares the view data. The caller
// passes the config path it loaded cfg from, so the editing section names the
// file the agent must actually edit.
func Explain(cfg *config.Config, configPath string) string {
	profilePath := cfg.CommandProfilePath()
	policyCommands, floorPaths := readCommandProfile(profilePath)

	view := explainView{
		Hook:           cfg.ToolMode == "hook",
		ConfigPath:     configPath,
		ProfilePath:    profilePath,
		PolicyCommands: policyCommands,
		FloorPaths:     floorPaths,
		Capabilities:   sandboxhost.CapabilityNames(),
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

// commandProfileSchema is the slice of nono's command-profile JSON this
// package reads. It is deliberately narrow: agent-sandbox does not generate or
// validate the profile, and every field here exists only so an agent reading
// `ai explain` can tell a policy command from a floor command and see a
// denial's reason. Any profile field outside this shape is simply not shown.
type commandProfileSchema struct {
	CommandPolicies struct {
		Commands map[string]struct {
			From map[string]struct {
				Sandbox struct {
					ExecPaths []string `json:"exec_paths"`
					// ArgvPrepend marks a wrapper-bound command: the profile
					// pins this name's executable back to the agent-sandbox
					// binary itself and inserts these tokens (e.g.
					// ["safe","git"]) after the shim's own argv[0], so the
					// invocation reaches a "safe <tool>" subcommand that
					// parses it in Go instead of the real tool.
					ArgvPrepend []string `json:"argv_prepend"`
				} `json:"sandbox"`
				InvocationPolicy struct {
					Deny []struct {
						Argv   json.RawMessage `json:"argv"`
						Reason string          `json:"reason"`
					} `json:"deny"`
				} `json:"invocation_policy"`
			} `json:"from"`
		} `json:"commands"`
	} `json:"command_policies"`
}

// readCommandProfile parses the command profile at path and returns its
// policy commands (sorted by name) and the broker's own floor paths. A
// missing or unparseable profile is not an error here: Explain always returns
// a string, and a launch-time check (doctor, config-check) is where a broken
// profile is actually reported. Here it just means the two-tier section of the
// output is empty.
func readCommandProfile(path string) ([]policyCommandView, []string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var profile commandProfileSchema
	if err := json.Unmarshal(data, &profile); err != nil {
		return nil, nil
	}

	names := make([]string, 0, len(profile.CommandPolicies.Commands))
	for name := range profile.CommandPolicies.Commands {
		names = append(names, name)
	}
	sort.Strings(names)

	var floorPaths []string
	var policyCommands []policyCommandView
	for _, name := range names {
		entry := profile.CommandPolicies.Commands[name]
		if session, ok := entry.From["session"]; ok {
			// The session's own callee is the broker: what it can exec directly
			// (exec_paths) is the floor, not a policy command.
			floorPaths = append(floorPaths, session.Sandbox.ExecPaths...)
			continue
		}

		var denials []string
		var wrapper string
		callers := make([]string, 0, len(entry.From))
		for caller := range entry.From {
			callers = append(callers, caller)
		}
		sort.Strings(callers)
		for _, caller := range callers {
			if wrapper == "" && len(entry.From[caller].Sandbox.ArgvPrepend) > 0 {
				wrapper = strings.Join(entry.From[caller].Sandbox.ArgvPrepend, " ")
			}
			for _, rule := range entry.From[caller].InvocationPolicy.Deny {
				matcher := renderArgvMatcher(rule.Argv)
				if rule.Reason != "" {
					denials = append(denials, matcher+": "+rule.Reason)
				} else {
					denials = append(denials, matcher)
				}
			}
		}
		policyCommands = append(policyCommands, policyCommandView{Name: name, Denials: denials, Wrapper: wrapper})
	}
	return policyCommands, floorPaths
}

// renderArgvMatcher renders an invocation_policy rule's argv matcher (e.g.
// {"contains": ["--force"]} or {"prefix": ["reset", "--hard"]}) as a short,
// human-legible string. It never fails: a matcher shape it does not recognize
// still renders as its raw JSON, so an unusual profile is described rather
// than dropped silently.
func renderArgvMatcher(raw json.RawMessage) string {
	var m map[string][]string
	if err := json.Unmarshal(raw, &m); err == nil && len(m) > 0 {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("argv %s %s", k, strings.Join(m[k], " ")))
		}
		return strings.Join(parts, ", ")
	}
	return "argv " + string(raw)
}
