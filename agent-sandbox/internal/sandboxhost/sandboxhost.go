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
	Network     *profileNetwork    `json:"network,omitempty"`
}

type profileNetwork struct {
	NetworkProfile string   `json:"network_profile,omitempty"`
	AllowDomain    []string `json:"allow_domain,omitempty"`
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

	// Baseline (per agent). agentOnlyEnv (e.g. the broker socket path) is
	// granted here but deliberately not in ResolveCommand's profile — see
	// baselineEnv's comment in catalog.go.
	allowVars = append(allowVars, baselineEnv...)
	allowVars = append(allowVars, agentOnlyEnv...)
	allowFile = append(allowFile, baselineAllowFile...)

	// Capabilities.
	for _, name := range h.Capabilities {
		c, ok := catalog[name]
		if !ok {
			return nil, fmt.Errorf("sandboxhost: unknown capability %q (valid: %s)", name, validCapabilities())
		}
		groups = append(groups, c.groups...)
		read = append(read, c.read...)
		readFile = append(readFile, c.readFile...)
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

// ResolveCommand builds the profile for a single brokered command: the working
// directory read+write, the non-credential capabilities from cfg.Sandbox.Host,
// the env allowlist from cfg.Sandbox.Command.EnvPassthrough, and the fixed
// developer network profile plus cfg.Sandbox.Network.AllowDomains.
//
// It deliberately does not reuse Resolve: the command sandbox is a different
// policy, not a variation of the agent's, and sharing the expansion would make
// it easy to leak a credential grant into it by accident.
func ResolveCommand(cfg *config.Config, workdir string) (*Resolved, error) {
	if strings.TrimSpace(workdir) == "" {
		return nil, fmt.Errorf("sandboxhost: empty workdir for command profile")
	}

	var groups, read, readFile, allowVars []string
	allowVars = append(allowVars, baselineEnv...)
	allowVars = append(allowVars, cfg.Sandbox.Command.EnvPassthrough...)

	for _, name := range cfg.Sandbox.Host.Capabilities {
		if credentialCapabilities[name] {
			continue
		}
		c, ok := catalog[name]
		if !ok {
			return nil, fmt.Errorf("sandboxhost: unknown capability %q (valid: %s)", name, validCapabilities())
		}
		groups = append(groups, c.groups...)
		read = append(read, c.read...)
		readFile = append(readFile, c.readFile...)
		allowVars = append(allowVars, c.allowVars...)
	}

	r := &Resolved{
		profile: nonoProfile{
			Meta: profileMeta{Name: "agent-sandbox command"},
			Filesystem: profileFilesystem{
				Allow:     sortDedup([]string{workdir}),
				Read:      sortDedup(read),
				AllowFile: sortDedup(baselineAllowFile),
				ReadFile:  sortDedup(readFile),
			},
			Environment: profileEnvironment{AllowVars: sortDedup(allowVars)},
			Network: &profileNetwork{
				NetworkProfile: commandNetworkProfile,
				AllowDomain:    sortDedup(cfg.Sandbox.Network.AllowDomains),
			},
		},
	}
	if g := sortDedup(groups); len(g) > 0 {
		r.profile.Groups = &profileGroups{Include: g}
	}
	return r, nil
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
