// Package sandboxhost is the single source of truth for host-access
// capabilities. It expands the config's [sandbox.agent] section into the one
// nono profile agent-sandbox generates — the launched agent's (Resolve) —
// plus the coordinated permission-deny rules for the agent's own file tools.
// The sandbox each brokered command runs in is the operator-written command
// profile; this package neither generates nor reads it. The nono-specific
// JSON rendering lives here; nothing about nono leaks into the user-facing
// config.
package sandboxhost

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

// Resolved is the outcome of expanding a HostConfig: the nono profile to write
// and the deduped Claude permission-deny rules to inject via --settings.
type Resolved struct {
	profile   nonoProfile
	DenyRules []string
}

type nonoProfile struct {
	Extends     string             `json:"extends,omitempty"`
	Meta        profileMeta        `json:"meta"`
	Groups      *profileGroups     `json:"groups,omitempty"`
	Filesystem  profileFilesystem  `json:"filesystem"`
	Environment profileEnvironment `json:"environment"`
}

type profileMeta struct {
	Name string `json:"name"`
}

type profileGroups struct {
	Include []string `json:"include,omitempty"`
}

type profileFilesystem struct {
	Allow            []string `json:"allow,omitempty"`
	Read             []string `json:"read,omitempty"`
	AllowFile        []string `json:"allow_file,omitempty"`
	ReadFile         []string `json:"read_file,omitempty"`
	BypassProtection []string `json:"bypass_protection,omitempty"`
}

type profileEnvironment struct {
	AllowVars []string `json:"allow_vars,omitempty"`
}

// sideOptions carries how the agent's profile is framed: its nono base
// profile and any env granted on top of the baseline. There used to be a
// second side (the shell sandbox's) with its own structural differences —
// a working directory grant, a network section, whether deny rules were
// emitted — but that profile is gone, so only what the agent side ever
// used remains.
type sideOptions struct {
	extends  string
	metaName string
	// extraEnv is granted on top of baselineEnv (agentOnlyEnv, the broker
	// socket).
	extraEnv []string
	// emitDeny renders the capabilities' Claude permission-deny rules. They
	// constrain the agent's own file tools.
	emitDeny bool
}

// expand turns host sections into one nono profile. Sections are unioned in
// order, so a grant reaches the profile if any of them declares it. Nothing is
// subtracted: whatever must not be granted is simply not among the sections
// handed in.
//
// All output lists are sorted and de-duplicated so the result is deterministic.
func expand(sections []config.HostConfig, opts sideOptions) (*Resolved, error) {
	var groups, read, bypass, allowFile, allowVars, allow, readFile, deny []string

	groups = append(groups, baselineGroups...)
	allowVars = append(allowVars, baselineEnv...)
	allowVars = append(allowVars, opts.extraEnv...)
	allowFile = append(allowFile, baselineAllowFile...)

	for _, h := range sections {
		for _, name := range h.Capabilities {
			c, ok := catalog[name]
			if !ok {
				return nil, fmt.Errorf("sandboxhost: unknown capability %q (valid: %s)", name, validCapabilities())
			}
			groups = append(groups, c.groups...)
			allow = append(allow, c.allow...)
			allow = append(allow, c.perOSAllow[hostOS]...)
			read = append(read, c.read...)
			readFile = append(readFile, c.readFile...)
			bypass = append(bypass, c.bypass...)
			allowFile = append(allowFile, c.allowFile...)
			allowVars = append(allowVars, c.allowVars...)
			for tool, paths := range map[string][]string{"Read": c.denyRead, "Edit": c.denyEdit} {
				for _, p := range paths {
					rule, rerr := denyRule(tool, p)
					if rerr != nil {
						return nil, fmt.Errorf("sandboxhost: capability %q: %w", name, rerr)
					}
					deny = append(deny, rule)
				}
			}
		}

		// Raw grants (no bypass). Guard protected paths on the read/allow lists.
		for _, p := range h.Allow {
			if isProtected(p) {
				return nil, protectedErr(p)
			}
		}
		for _, p := range h.Read {
			if isProtected(p) {
				return nil, protectedErr(p)
			}
		}
		allow = append(allow, h.Allow...)
		read = append(read, h.Read...)
		allowFile = append(allowFile, h.AllowFile...)
		readFile = append(readFile, h.ReadFile...)
		allowVars = append(allowVars, h.AllowEnv...)
	}

	r := &Resolved{
		profile: nonoProfile{
			Extends: opts.extends,
			Meta:    profileMeta{Name: opts.metaName},
			Filesystem: profileFilesystem{
				Allow:            sortDedup(allow),
				Read:             sortDedup(read),
				AllowFile:        sortDedup(allowFile),
				ReadFile:         sortDedup(readFile),
				BypassProtection: sortDedup(bypass),
			},
			Environment: profileEnvironment{AllowVars: sortDedup(allowVars)},
		},
	}
	if opts.emitDeny {
		r.DenyRules = sortDedup(deny)
	}
	if g := sortDedup(groups); len(g) > 0 {
		r.profile.Groups = &profileGroups{Include: g}
	}
	return r, nil
}

// denyRule renders one Claude Code permission-deny rule for a catalog path.
//
// The "//" prefix is what makes the pattern absolute. Claude Code reads the
// path as a gitignore pattern in which a single leading slash anchors at the
// settings source, so "Read(/etc/bashrc)" resolves under the working directory
// and matches nothing — silently, which is the worst way for a deny rule to
// fail. "~" is expanded here for the same reason: a rule is only worth emitting
// if it names the file the tool will actually be asked for.
func denyRule(tool, path string) (string, error) {
	switch {
	case strings.HasPrefix(path, "~/"):
		if hostHome == "" {
			return "", fmt.Errorf("cannot expand %q: home directory is unknown", path)
		}
		path = hostHome + path[1:]
	case strings.HasPrefix(path, "/"):
	default:
		return "", fmt.Errorf("deny path %q must start with ~/ or /", path)
	}
	return tool + "(/" + path + ")", nil
}

// Resolve builds the profile for the launched agent from [sandbox.agent], on
// the given agent's nono base profile. It is the only profile agent-sandbox
// generates: the sandbox commands run in is the operator's command profile.
func Resolve(cfg *config.Config, agent string) (*Resolved, error) {
	base, ok := agentBases[agent]
	if !ok {
		return nil, fmt.Errorf("sandboxhost: unknown agent %q", agent)
	}
	return expand(
		[]config.HostConfig{cfg.Sandbox.Agent},
		sideOptions{
			extends:  base.extends,
			metaName: base.metaName,
			extraEnv: agentOnlyEnv,
			emitDeny: true,
		},
	)
}

// ProtectedGrants returns the profile's filesystem grants that fall under
// protectedPrefixes, sorted. Raw grants can never produce one (expand rejects
// them), so a non-empty result means a credential capability (docker, ssh, ...)
// was declared in one of the sections handed to expand.
func (r *Resolved) ProtectedGrants() []string {
	var out []string
	fs := r.profile.Filesystem
	for _, list := range [][]string{fs.Allow, fs.Read, fs.AllowFile, fs.ReadFile} {
		for _, p := range list {
			if isProtected(p) {
				out = append(out, p)
			}
		}
	}
	return sortDedup(out)
}

// EnvAllowVars returns the profile's environment allow_vars patterns.
//
// The command broker uses it to build the nono supervisor's own environment:
// it forwards exactly those of the launcher's variables that this list already
// permits inside the sandbox. Sharing the list keeps the two in step — in
// particular baselineEnv and the capability allowVars (the mise capability's
// "MISE*" / "__MISE*") are declared in exactly one place, this package, rather
// than being restated by the broker where they could silently drift.
//
// Entries are patterns, not plain names; see broker's envAllowlist for the
// supported syntax.
func (r *Resolved) EnvAllowVars() []string {
	out := make([]string, len(r.profile.Environment.AllowVars))
	copy(out, r.profile.Environment.AllowVars)
	return out
}

// ProfileJSON marshals the resolved nono profile.
func (r *Resolved) ProfileJSON() ([]byte, error) {
	data, err := json.Marshal(r.profile)
	if err != nil {
		return nil, fmt.Errorf("sandboxhost: encode profile: %w", err)
	}
	return data, nil
}

func isProtected(p string) bool {
	p = strings.TrimSpace(p)
	for _, pre := range protectedPrefixes {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	return false
}

func protectedErr(p string) error {
	return fmt.Errorf("sandboxhost: %q is a protected path; grant it through a capability, not a raw read/allow", p)
}

func validCapabilities() string {
	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// sortDedup returns a sorted, de-duplicated copy, or nil when empty (so
// omitempty drops the field).
func sortDedup(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// WriteProfile marshals the profile to a 0600 temp file and returns its path
// plus a cleanup func that removes it. The file is read by nono itself on the
// host before the sandbox applies, so callers do NOT grant --read-file for it.
func (r *Resolved) WriteProfile() (string, func(), error) {
	data, err := r.ProfileJSON()
	if err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp("", "agent-sandbox-profile-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("sandboxhost: temp file: %w", err)
	}
	path := f.Name()
	cleanup := func() { os.Remove(path) }
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		cleanup()
		return "", nil, fmt.Errorf("sandboxhost: chmod: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		cleanup()
		return "", nil, fmt.Errorf("sandboxhost: write: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("sandboxhost: close: %w", err)
	}
	return path, cleanup, nil
}
