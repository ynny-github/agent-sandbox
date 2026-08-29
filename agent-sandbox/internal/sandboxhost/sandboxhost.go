// Package sandboxhost is the single source of truth for host-access
// capabilities. It expands the config's host sections into the two nono
// profiles agent-sandbox needs — one for the launched agent (Resolve), one for
// the shell sandbox each brokered command runs in (ResolveShell) — plus the
// coordinated permission-deny rules for the agent's own file tools. Both profiles come from
// the same expansion and differ only in which config sections feed them, so a
// grant's scope is decided by where it is written, not by a rule in here. The
// nono-specific JSON rendering lives here; nothing about nono leaks into the
// user-facing config.
package sandboxhost

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
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
	Deny             []string `json:"deny,omitempty"`
	BypassProtection []string `json:"bypass_protection,omitempty"`
}

type profileEnvironment struct {
	AllowVars []string `json:"allow_vars,omitempty"`
}

// sideOptions carries what differs between the two profiles agent-sandbox
// generates. The grants themselves never differ by side — those come from the
// config sections expand is handed — so everything here is structural: how the
// profile is framed, and which built-ins only one side may have.
type sideOptions struct {
	extends  string
	metaName string
	// extraEnv is granted on top of baselineEnv. Only the agent side passes
	// anything (agentOnlyEnv, the broker socket); see catalog.go for why.
	extraEnv []string
	// workdir, when set, is granted read+write. Only the shell side sets it:
	// the agent's own working directory comes from its nono base profile.
	workdir string
	// network, when set, is the profile's network section. Only the shell side
	// sets one, so it is also the only side where a capability's domains land;
	// see catalog.go's capability comment.
	network *profileNetwork
	// emitDeny renders the capabilities' Claude permission-deny rules. They
	// constrain the agent's own file tools, so only the agent side wants them.
	emitDeny bool
	// emitDenyPath renders the capabilities' nono filesystem denials. Unlike
	// the two fields above, this side-split is a policy call rather than a
	// structural one: both profiles have a filesystem section to put it in. It
	// is the shell side's only way to refuse a credential a group hands out,
	// while the agent — which is where an operator's `cargo publish` runs —
	// keeps the file.
	emitDenyPath bool
}

// expand turns host sections into one nono profile. Sections are unioned in
// order, so a grant reaches the profile if any of them declares it — the shared
// [sandbox.shared] base plus that side's own section. Nothing is subtracted:
// whatever a side must not have is simply not among the sections it is given.
//
// All output lists are sorted and de-duplicated so the result is deterministic.
func expand(sections []config.HostConfig, opts sideOptions) (*Resolved, error) {
	var groups, read, bypass, allowFile, allowVars, allow, readFile, domains, denyPath, deny []string

	allowVars = append(allowVars, baselineEnv...)
	allowVars = append(allowVars, opts.extraEnv...)
	allowFile = append(allowFile, baselineAllowFile...)
	if opts.workdir != "" {
		allow = append(allow, opts.workdir)
	}

	for _, h := range sections {
		for _, name := range h.Capabilities {
			c, ok := catalog[name]
			if !ok {
				return nil, fmt.Errorf("sandboxhost: unknown capability %q (valid: %s)", name, validCapabilities())
			}
			groups = append(groups, c.groups...)
			allow = append(allow, c.allow...)
			allow = append(allow, c.perOSAllow[hostOS]...)
			denyPath = append(denyPath, c.denyPath...)
			read = append(read, c.read...)
			readFile = append(readFile, c.readFile...)
			bypass = append(bypass, c.bypass...)
			allowFile = append(allowFile, c.allowFile...)
			allowVars = append(allowVars, c.allowVars...)
			domains = append(domains, c.domains...)
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
				Deny:             denyPathFor(opts, denyPath),
				BypassProtection: sortDedup(bypass),
			},
			Environment: profileEnvironment{AllowVars: sortDedup(allowVars)},
			Network:     withDomains(opts.network, domains),
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

// denyPathFor returns the nono filesystem denials for a side, or nil when that
// side does not take them. Kept as a function so the gate reads as one thing
// next to the other list fields, which are all unconditional.
func denyPathFor(opts sideOptions, denyPath []string) []string {
	if !opts.emitDenyPath {
		return nil
	}
	return sortDedup(denyPath)
}

// Resolve builds the profile for the launched agent: the shared
// [sandbox.shared] base plus [sandbox.agent], on the given agent's nono base
// profile. agentOnlyEnv (the broker socket path) is granted here and never in
// ResolveShell's profile — see baselineEnv's comment in catalog.go.
func Resolve(cfg *config.Config, agent string) (*Resolved, error) {
	base, ok := agentBases[agent]
	if !ok {
		return nil, fmt.Errorf("sandboxhost: unknown agent %q", agent)
	}
	return expand(
		[]config.HostConfig{cfg.Sandbox.Shared, cfg.Sandbox.Agent.HostConfig},
		sideOptions{
			extends:  base.extends,
			metaName: base.metaName,
			extraEnv: agentOnlyEnv,
			emitDeny: true,
		},
	)
}

// ResolveShell builds the profile for the shell sandbox a single brokered
// command runs in: the working directory read+write, the shared
// [sandbox.shared] base plus [sandbox.shell], and the fixed developer network
// profile plus the extra domains ShellAllowDomains resolves.
//
// It shares expand with Resolve because the two profiles differ only in which
// config sections feed them: a grant the shell sandbox must not have belongs
// under [sandbox.agent], where this call never looks.
func ResolveShell(cfg *config.Config, workdir string) (*Resolved, error) {
	if strings.TrimSpace(workdir) == "" {
		return nil, fmt.Errorf("sandboxhost: empty workdir for shell profile")
	}
	return expand(
		[]config.HostConfig{cfg.Sandbox.Shared, cfg.Sandbox.Shell.HostConfig},
		sideOptions{
			metaName:     "agent-sandbox shell",
			workdir:      workdir,
			network:      shellNetwork(cfg),
			emitDenyPath: true,
		},
	)
}

// shellNetwork is the network section every shell profile starts from: the
// fixed developer profile plus the domains written in [sandbox.shell]. expand
// adds whatever the declared capabilities bring on top.
func shellNetwork(cfg *config.Config) *profileNetwork {
	return &profileNetwork{
		NetworkProfile: shellNetworkProfile,
		AllowDomain:    cfg.Sandbox.Shell.AllowDomains,
	}
}

// withDomains returns n with domains merged into its allow list, leaving the
// caller's struct untouched. A nil n stays nil: a side with no network section
// has nowhere to put them, and inventing one would silently reconfigure the
// network of a profile that means to inherit it.
func withDomains(n *profileNetwork, domains []string) *profileNetwork {
	if n == nil {
		return nil
	}
	merged := *n
	merged.AllowDomain = sortDedup(concat(n.AllowDomain, domains))
	return &merged
}

// ShellGrants are the filesystem paths the shell sandbox can reach outside its
// working directory — the shared [sandbox.shared] base plus [sandbox.shell],
// expanded. The working directory and the built-in baseline files are excluded:
// they are true of every command and say nothing about this config.
type ShellGrants struct {
	Write []string // read+write
	Read  []string // read-only
}

// ShellFilesystemGrants resolves ShellGrants for cfg. It exists so agent-facing
// documentation can state what a sandboxed command actually reaches instead of
// describing the config sections and leaving the agent to work it out — the
// answer depends entirely on where grants were written.
func ShellFilesystemGrants(cfg *config.Config) (ShellGrants, error) {
	r, err := expand([]config.HostConfig{cfg.Sandbox.Shared, cfg.Sandbox.Shell.HostConfig}, sideOptions{})
	if err != nil {
		return ShellGrants{}, err
	}
	fs := r.profile.Filesystem
	return ShellGrants{
		Write: sortDedup(exclude(concat(fs.Allow, fs.AllowFile), baselineAllowFile)),
		Read:  sortDedup(concat(fs.Read, fs.ReadFile)),
	}, nil
}

// ShellAllowDomains are the domains a brokered command may reach on top of the
// fixed developer network profile: what [sandbox.shell] writes as
// allow_domains, plus what the capabilities declared for that side bring with
// them. Like ShellFilesystemGrants it exists so agent-facing documentation can
// state the resolved answer instead of the config sections it came from.
func ShellAllowDomains(cfg *config.Config) ([]string, error) {
	r, err := expand(
		[]config.HostConfig{cfg.Sandbox.Shared, cfg.Sandbox.Shell.HostConfig},
		sideOptions{network: shellNetwork(cfg)},
	)
	if err != nil {
		return nil, err
	}
	return slices.Clone(r.profile.Network.AllowDomain), nil
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

// ProtectedGrants returns the profile's filesystem grants that fall under
// protectedPrefixes, sorted. Raw grants can never produce one (expand rejects
// them), so a non-empty result means a capability carrying host credentials was
// declared for this side — worth surfacing on the shell profile, where it is
// usually a mistake.
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
