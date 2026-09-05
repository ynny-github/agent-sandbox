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

// expand turns host sections into one nono profile. Sections are unioned in
// order, so a grant reaches the profile if any of them declares it. Nothing is
// subtracted: whatever must not be granted is simply not among the sections
// handed in.
//
// extends and metaName frame the profile (its nono base profile and its
// meta.name); extraEnv is granted on top of baselineEnv (agentOnlyEnv, the
// broker socket, for Resolve's one caller). expand always emits the
// capabilities' Claude permission-deny rules — there used to be a second
// caller (the shell sandbox's ResolveShell) that suppressed them, but that
// profile is gone, so nothing suppresses them anymore.
//
// All output lists are sorted and de-duplicated so the result is deterministic.
func expand(sections []config.HostConfig, extends, metaName string, extraEnv []string) (*Resolved, error) {
	var groups, read, bypass, allowFile, allowVars, allow, readFile, deny []string

	groups = append(groups, baselineGroups...)
	allowVars = append(allowVars, baselineEnv...)
	allowVars = append(allowVars, extraEnv...)
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
			Extends: extends,
			Meta:    profileMeta{Name: metaName},
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
		base.extends, base.metaName, agentOnlyEnv,
	)
}

// Grants describes what a resolved profile reaches beyond its baseline: the
// read+write filesystem paths and the read-only ones.
type Grants struct {
	Write []string
	Read  []string
}

// FilesystemGrants resolves the profile's filesystem grants into read+write and
// read-only lists, excluding baselineAllowFile (granted to every profile
// regardless of what was declared, so listing it would crowd out what an
// operator actually wrote). It exists so agent-facing documentation — and
// `agent-sandbox ai config-check` — can state what the launched agent's own
// sandbox reaches instead of describing config sections and leaving the reader
// to work it out.
func (r *Resolved) FilesystemGrants() Grants {
	fs := r.profile.Filesystem
	return Grants{
		Write: sortDedup(exclude(concat(fs.Allow, fs.AllowFile), baselineAllowFile)),
		Read:  sortDedup(concat(fs.Read, fs.ReadFile)),
	}
}

func concat(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// exclude drops every entry of in that appears in drop.
func exclude(in, drop []string) []string {
	skip := make(map[string]struct{}, len(drop))
	for _, d := range drop {
		skip[d] = struct{}{}
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, ok := skip[v]; ok {
			continue
		}
		out = append(out, v)
	}
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
