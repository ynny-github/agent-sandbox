// Package sandboxhost is the single source of truth for host-access
// capabilities. It expands a config.HostConfig for a launch agent into a nono
// profile (what the host process may touch) and the coordinated Claude
// permission-deny rules (what the agent's own file tools may not read). The
// nono-specific JSON rendering lives here; nothing about nono leaks into the
// user-facing config.
package sandboxhost

import (
	"encoding/json"
	"fmt"
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

// Resolve expands cfg.Sandbox.Host for the given launch agent. All output lists
// are sorted and de-duplicated so the result is deterministic.
func Resolve(cfg *config.Config, agent string) (*Resolved, error) {
	base, ok := agentBases[agent]
	if !ok {
		return nil, fmt.Errorf("sandboxhost: unknown agent %q", agent)
	}
	h := cfg.Sandbox.Host

	var groups, read, bypass, allowFile, allowVars, allow, readFile, deny []string

	// Baseline (per agent).
	allowVars = append(allowVars, baselineEnv...)
	allowFile = append(allowFile, baselineAllowFile...)

	// Capabilities.
	for _, name := range h.Capabilities {
		c, ok := catalog[name]
		if !ok {
			return nil, fmt.Errorf("sandboxhost: unknown capability %q (valid: %s)", name, validCapabilities())
		}
		groups = append(groups, c.groups...)
		read = append(read, c.read...)
		bypass = append(bypass, c.bypass...)
		allowFile = append(allowFile, c.allowFile...)
		allowVars = append(allowVars, c.allowVars...)
		deny = append(deny, c.deny...)
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

	r := &Resolved{
		profile: nonoProfile{
			Extends: base.extends,
			Meta:    profileMeta{Name: base.metaName},
			Filesystem: profileFilesystem{
				Allow:            sortDedup(allow),
				Read:             sortDedup(read),
				AllowFile:        sortDedup(allowFile),
				ReadFile:         sortDedup(readFile),
				BypassProtection: sortDedup(bypass),
			},
			Environment: profileEnvironment{AllowVars: sortDedup(allowVars)},
		},
		DenyRules: sortDedup(deny),
	}
	if g := sortDedup(groups); len(g) > 0 {
		r.profile.Groups = &profileGroups{Include: g}
	}
	return r, nil
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
