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
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe/git"
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
	// BrokerIssue is set when the profile parsed but no entry in it has a
	// "session" caller — the broker's own entrypoint could not be identified,
	// so PolicyCommands and FloorPaths are both necessarily empty for a
	// reason that has nothing to do with the profile declaring no commands.
	// Rendering this distinctly matters: "(none declared in the current
	// profile)" reads to the agent as an affirmative statement that nothing
	// is policy-controlled, which is false here — the profile could not be
	// read the way this package expects, not read and found empty.
	BrokerIssue string
	// Capabilities is the catalog's capability names, so the editing section
	// lists what may actually be written rather than a prose sample that goes
	// stale when a bundle is added.
	Capabilities []string
}

// policyCommandView is one command_policies.commands entry that is not the
// broker's own (session) entry: a policy command, reachable only through its
// nono-generated shim.
type policyCommandView struct {
	Name string
	// Denials are this entry's own invocation_policy denials, if any: each
	// is "<matcher>: <reason>", or just "<matcher>" when the profile
	// carries no reason. Empty for a wrapper-bound entry with no
	// invocation_policy of its own (WrapperDenials covers that case).
	Denials []string
	// Wrapper is the "safe <tool>" subcommand this entry's argv_prepend
	// inserts (e.g. "safe git"), when the profile binds this name to the
	// agent-sandbox binary itself rather than to the real tool. Empty for a
	// command pinned directly at its own binary.
	Wrapper string
	// WrapperDenials are the wrapper's own rule messages, read directly
	// from the Go package that implements it (see wrapperRuleMessages) —
	// not from this profile. A wrapper-bound command's rule set lives in
	// Go, not in invocation_policy, so Denials alone would understate — or,
	// now that git's invocation_policy is gone, entirely miss — what it
	// refuses. Populated only when Wrapper is non-empty and this package
	// knows the wrapper's rule source.
	WrapperDenials []string
}

// wrapperRuleMessages returns the human-readable rule messages for a
// wrapper subcommand (e.g. "safe git", the value of policyCommandView.
// Wrapper), read from the same Go source the wrapper itself enforces, or nil
// if this package does not know that wrapper's source.
//
// This switches on the wrapper, not the profile's command name: the wrapper
// is what identifies the rule source, and a profile is free to bind the same
// wrapper under a different command name (or to bind a name this package has
// no rule source for at all, e.g. "safe docker" — see the spec's Follow-on
// work for why that one is not covered here).
//
// This exists instead of pointing the agent at "agent-sandbox safe <tool>
// --help": "safe git"/"safe docker" both set DisableFlagParsing: true, so
// "--help" passes straight through to the real tool and prints *its* help
// instead of a rule set. Reading the rule messages directly, the way this
// function does, needs neither that nor a self-invocation of agent-sandbox.
func wrapperRuleMessages(wrapper string) []string {
	switch wrapper {
	case "safe git":
		rules := git.Rules()
		msgs := make([]string, 0, len(rules))
		for _, r := range rules {
			if r.Message == "" {
				continue
			}
			msgs = append(msgs, r.Message)
		}
		return msgs
	default:
		return nil
	}
}

// Explain renders a Markdown description of the sandbox environment from cfg,
// for the AI agent to read on demand via `agent-sandbox ai explain`. The prose
// lives in explain.tmpl; this function only prepares the view data. The caller
// passes the config path it loaded cfg from, so the editing section names the
// file the agent must actually edit.
func Explain(cfg *config.Config, configPath string) string {
	profilePath := cfg.CommandProfilePath()
	policyCommands, floorPaths, brokerFound := readCommandProfile(profilePath)

	var brokerIssue string
	if !brokerFound {
		// The profile read and parsed fine, but no entry in it had a "session"
		// caller, so this package could not tell which command is the
		// broker's own entrypoint. A missing or unparseable file returns
		// brokerFound true instead: that case already has its own launch-time
		// diagnostic (doctor, config-check) and stays silent here.
		brokerIssue = fmt.Sprintf("could not identify the broker entry in %s", profilePath)
	}

	view := explainView{
		Hook:           cfg.ToolMode == "hook",
		ConfigPath:     configPath,
		ProfilePath:    profilePath,
		PolicyCommands: policyCommands,
		FloorPaths:     floorPaths,
		BrokerIssue:    brokerIssue,
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
// policy commands (sorted by name), the broker's own floor paths, and
// whether an entry with a "session" caller (the broker's own entrypoint)
// was found at all. A missing or unparseable profile is not an error here:
// Explain always returns a string, and a launch-time check (doctor,
// config-check) is where a broken profile is actually reported — for that
// case this returns brokerFound true, since it is not this function's
// diagnostic to give. brokerFound false means the file read and parsed, but
// no entry in it had a "session" caller, so the caller should say so rather
// than let an empty PolicyCommands/FloorPaths read as "nothing declared".
func readCommandProfile(path string) (policyCommands []policyCommandView, floorPaths []string, brokerFound bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, true
	}
	var profile commandProfileSchema
	if err := json.Unmarshal(data, &profile); err != nil {
		return nil, nil, true
	}

	names := make([]string, 0, len(profile.CommandPolicies.Commands))
	for name := range profile.CommandPolicies.Commands {
		names = append(names, name)
	}
	sort.Strings(names)

	// brokerName is the command whose own entry has a "session" caller: the
	// broker itself (e.g. "agent-sandbox"). Only an entry the broker can
	// reach directly — one naming brokerName among its own callers — is
	// something the agent can actually invoke and belongs in PolicyCommands.
	// An entry reachable only from another *policy* command (e.g. "realgit",
	// named solely in "git"'s own from, not the broker's) is not directly
	// invocable at all: nono refuses it with "tool 'agent-sandbox' is not
	// allowed to invoke it". Listing it as though it were a live command the
	// agent could type is what shipped a false "no invocations refused" line
	// for it once git's own invocation_policy was removed.
	//
	// A well-formed profile has exactly one such entry; this does not stop
	// scanning after the first because a second one's floor paths must not
	// vanish silently — but reachability below is still checked against only
	// the last name seen, which is the one real profile shape this was
	// written for.
	var brokerName string
	var found bool
	for _, name := range names {
		if session, ok := profile.CommandPolicies.Commands[name].From["session"]; ok {
			brokerName = name
			found = true
			// The session's own callee is the broker: what it can exec directly
			// (exec_paths) is the floor, not a policy command. Keep scanning
			// rather than stop at the first match, so a second such entry's
			// own floor paths are not silently dropped.
			floorPaths = append(floorPaths, session.Sandbox.ExecPaths...)
		}
	}
	if !found {
		return nil, nil, false
	}

	for _, name := range names {
		if name == brokerName {
			continue
		}
		entry := profile.CommandPolicies.Commands[name]
		if _, reachable := entry.From[brokerName]; !reachable {
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
		var wrapperDenials []string
		if wrapper != "" {
			wrapperDenials = wrapperRuleMessages(wrapper)
		}
		policyCommands = append(policyCommands, policyCommandView{
			Name: name, Denials: denials, Wrapper: wrapper, WrapperDenials: wrapperDenials,
		})
	}
	return policyCommands, floorPaths, true
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
