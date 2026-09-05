package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	ToolMode string `toml:"tool_mode"`
	// CommandProfile names the nono profile the command broker runs under. It
	// is written by the operator in nono's own schema, not generated: every
	// decision about commands — which may run, what each may touch, which
	// invocations are refused — lives there. An empty value means the default
	// name beside this config file.
	CommandProfile string        `toml:"command_profile"`
	MCP            MCPConfig     `toml:"mcp"`
	Sandbox        SandboxConfig `toml:"sandbox"`

	// dir is the directory the project config was loaded from. A relative
	// command_profile resolves against it rather than the process working
	// directory, so the same config behaves identically however it is invoked.
	dir string
}

// defaultCommandProfileName is the file the broker's profile is read from when
// command_profile is not set.
const defaultCommandProfileName = "command-profile.json"

// CommandProfilePath is the absolute path of the nono profile the command
// broker runs under.
func (c *Config) CommandProfilePath() string {
	name := strings.TrimSpace(c.CommandProfile)
	if name == "" {
		name = defaultCommandProfileName
	}
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(c.dir, name)
}

// absPath makes p absolute, falling back to p when the working directory
// cannot be read — a path that stays relative is still better than none.
func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

type MCPConfig struct {
	CommandOutputDir string `toml:"command_output_dir"`
}

// SandboxConfig is the host access agent-sandbox generates a profile for: the
// launched agent, and nothing else. Commands are governed by the operator's
// command profile, which agent-sandbox does not generate and does not read.
type SandboxConfig struct {
	Agent HostConfig `toml:"agent"`
}

// HostConfig declares, in nono-agnostic terms, host-side access for the
// launched agent's sandbox. Capabilities are named bundles expanded by
// internal/sandboxhost; the remaining lists are raw grants.
type HostConfig struct {
	Capabilities []string `toml:"capabilities"`
	Allow        []string `toml:"allow"`
	Read         []string `toml:"read"`
	AllowFile    []string `toml:"allow_file"`
	ReadFile     []string `toml:"read_file"`
	AllowEnv     []string `toml:"allow_env"`
}

// Load composes the optional user-scope config
// (~/.config/agent-sandbox/config.toml) with the project-scope config at path,
// then validates the merged result. Scalars: project overrides user. Lists (every
// list in [sandbox.agent]): de-duplicated union.
func Load(path string) (*Config, error) {
	var cfg Config

	// 1. User config is the base (optional). Snapshot its list fields before the
	//    project decode can replace them. The snapshot must be a *clone*: TOML
	//    decode reuses an existing slice's backing array in place when its cap is
	//    large enough, so a plain header copy would be corrupted by the project
	//    decode below.
	var userAgent HostConfig
	if up, err := userConfigPath(); err == nil {
		if _, statErr := os.Stat(up); statErr == nil {
			md, derr := decodeInto(up, &cfg)
			if derr != nil {
				return nil, derr
			}
			if derr := checkDeprecated(md); derr != nil {
				return nil, derr
			}
			userAgent = cloneHost(cfg.Sandbox.Agent)
		}
	}

	// 2. Project config overrides the scalars it defines; fields it omits keep the
	//    user values. Lists it defines replace the user's (unioned back in step 3).
	md, derr := decodeInto(path, &cfg)
	if derr != nil {
		return nil, derr
	}
	if derr := checkDeprecated(md); derr != nil {
		return nil, derr
	}
	cfg.dir = filepath.Dir(absPath(path))

	// 3. Union the list fields. When the project omits a list, cfg still holds the
	//    user's, so the union de-dupes back to the user's list (no change).
	cfg.Sandbox.Agent = unionHost(userAgent, cfg.Sandbox.Agent)

	// 4. Validate the merged config.
	return validate(&cfg)
}

// decodeInto decodes the TOML file at path into cfg, wrapping errors so callers
// can still match os.ErrNotExist. It returns the decode metadata for the
// deprecated-key check.
func decodeInto(path string, cfg *Config) (toml.MetaData, error) {
	md, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return md, fmt.Errorf("config: %w", err)
	}
	return md, nil
}

// checkDeprecated rejects keys that were removed or moved, naming the new
// location so a stale config fails loudly instead of being half-ignored. It is
// applied per-file because the keys are not representable in Config, and must
// fire even when the file merely mentions one with no value that validate would
// otherwise check.
//
// Order matters: the sandbox.command checks run most-specific first, since
// IsDefined("sandbox", "command") is also true for its sub-tables.
func checkDeprecated(md toml.MetaData) error {
	if md.IsDefined("sandbox", "network", "allow_cidrs") || md.IsDefined("sandbox", "network", "allow_hosts") {
		return ErrDeprecatedNetworkKeys
	}
	if md.IsDefined("sandbox", "network", "allow_external") {
		return ErrRemovedAllowExternal
	}
	if md.IsDefined("sandbox", "network") {
		return ErrMovedNetworkSection
	}
	if md.IsDefined("sandbox", "agent", "allow_commands") || md.IsDefined("sandbox", "agent", "drop_commands") {
		return ErrMovedCommandTiers
	}
	if md.IsDefined("sandbox", "shared") {
		return ErrMovedSharedToAgent
	}
	if md.IsDefined("sandbox", "shell") {
		return ErrMovedShellToProfile
	}
	if md.IsDefined("sandbox", "command", "env_passthrough") {
		return ErrMovedEnvPassthrough
	}
	if md.IsDefined("sandbox", "command", "network") {
		return ErrMovedCommandNetwork
	}
	if md.IsDefined("sandbox", "command", "host") {
		return ErrMovedCommandHost
	}
	if md.IsDefined("sandbox", "command") {
		// Bare [sandbox.command] predates even allow_commands/drop_commands: it
		// was the routing table before routing was split into those two keys.
		// Both are gone now too, so it points at the same sentinel they do.
		return ErrMovedCommandTiers
	}
	if md.IsDefined("sandbox", "agent", "host") {
		return ErrMovedAgentHost
	}
	if md.IsDefined("sandbox", "host") {
		// [sandbox.host] predates [sandbox.shared], which has itself since been
		// folded into [sandbox.agent]; point straight at today's destination
		// rather than a name that no longer exists either.
		return ErrMovedSharedToAgent
	}
	if md.IsDefined("sandbox", "container") {
		return ErrRemovedContainerSection
	}
	return nil
}

// validate applies the tool_mode default and all required-field checks to the
// merged config, returning it unchanged on success.
func validate(cfg *Config) (*Config, error) {
	switch cfg.ToolMode {
	case "":
		cfg.ToolMode = "mcp"
	case "mcp", "hook":
		// valid
	default:
		return nil, fmt.Errorf("%w: %q", ErrInvalidToolMode, cfg.ToolMode)
	}

	if _, err := os.Stat(cfg.CommandProfilePath()); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrCommandProfileMissing, cfg.CommandProfilePath())
	}

	// command_output_dir is only consumed by the MCP server path, so require it
	// only in mcp mode. In hook mode it is optional and, if set, ignored.
	if cfg.ToolMode == "mcp" && strings.TrimSpace(cfg.MCP.CommandOutputDir) == "" {
		return nil, ErrMissingMCPCommandOutputDir
	}
	// NONO_* is rejected in the agent's allow_env: the agent's own nono profile
	// is not the only one it can influence — it is what starts the command
	// broker, which runs its own nono session — so a forwarded NONO_* variable
	// could reconfigure that session from inside the sandbox meant to contain it.
	for _, name := range cfg.Sandbox.Agent.AllowEnv {
		if strings.HasPrefix(strings.TrimSpace(name), "NONO_") {
			return nil, fmt.Errorf("%w: %q", ErrAllowEnvNonoVar, name)
		}
	}
	return cfg, nil
}

// cloneHost deep-copies a host section's lists. See Load step 1 for why a
// header copy is not enough.
func cloneHost(h HostConfig) HostConfig {
	return HostConfig{
		Capabilities: slices.Clone(h.Capabilities),
		Allow:        slices.Clone(h.Allow),
		Read:         slices.Clone(h.Read),
		AllowFile:    slices.Clone(h.AllowFile),
		ReadFile:     slices.Clone(h.ReadFile),
		AllowEnv:     slices.Clone(h.AllowEnv),
	}
}

// unionHost de-duplicates the union of two host sections field by field, a's
// entries first.
func unionHost(a, b HostConfig) HostConfig {
	return HostConfig{
		Capabilities: dedupUnion(a.Capabilities, b.Capabilities),
		Allow:        dedupUnion(a.Allow, b.Allow),
		Read:         dedupUnion(a.Read, b.Read),
		AllowFile:    dedupUnion(a.AllowFile, b.AllowFile),
		ReadFile:     dedupUnion(a.ReadFile, b.ReadFile),
		AllowEnv:     dedupUnion(a.AllowEnv, b.AllowEnv),
	}
}

// dedupUnion returns the concatenation of a and b with duplicates removed,
// preserving first-occurrence order (a's items first). It returns nil when both
// inputs are empty so an omitted list stays nil, matching prior behavior.
func dedupUnion(a, b []string) []string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, v := range list {
			if _, ok := seen[v]; ok {
				continue
			}
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// userConfigPath returns the fixed user-scope config location,
// ~/.config/agent-sandbox/config.toml. It errors only when the home directory
// cannot be resolved (e.g. $HOME unset); callers treat that as "no user config".
func userConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "agent-sandbox", "config.toml"), nil
}
