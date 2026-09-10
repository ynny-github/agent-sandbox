package config

import (
	"fmt"
	"os"
	"path/filepath"
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
	CommandProfile string `toml:"command_profile"`
	// Agents maps a launch subcommand's name ("claude") to that agent's
	// configuration. An absent table is not an error: every key has a default.
	Agents map[string]AgentConfig `toml:"agents"`
	MCP    MCPConfig              `toml:"mcp"`

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

// AgentConfig is one launchable agent's configuration. It holds only the nono
// profile that agent runs under — a file the operator writes in nono's own
// schema, which agent-sandbox hands to nono without generating, reading, or
// validating it.
//
// A second field would need care. Load decodes the user config and then the
// project config into the same map, and a key present in both is replaced
// wholesale rather than merged field by field, so a project [agents.claude]
// setting only one field would silently drop the user config's others.
type AgentConfig struct {
	Profile string `toml:"profile"`
}

// AgentProfilePath is the absolute path of the nono profile the named agent
// runs under. It resolves exactly like CommandProfilePath — one rule for both
// profiles — so an absent table or empty value means "<agent>-profile.json"
// beside the project config, and a relative path joins onto that same
// directory however agent-sandbox was invoked.
func (c *Config) AgentProfilePath(agent string) string {
	name := strings.TrimSpace(c.Agents[agent].Profile)
	if name == "" {
		name = agent + "-profile.json"
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

// Load composes the optional user-scope config
// (~/.config/agent-sandbox/config.toml) with the project-scope config at path,
// then validates the merged result. Scalars: project overrides user.
func Load(path string) (*Config, error) {
	var cfg Config

	// 1. User config is the base (optional). Every field is a scalar or a map
	//    of scalars, so the project decode below simply overrides what it
	//    declares — a map key present in both is replaced whole, not merged.
	if up, err := userConfigPath(); err == nil {
		if _, statErr := os.Stat(up); statErr == nil {
			md, derr := decodeInto(up, &cfg)
			if derr != nil {
				return nil, derr
			}
			if derr := checkDeprecated(md); derr != nil {
				return nil, derr
			}
		}
	}

	// 2. Project config overrides the scalars it defines; fields it omits keep the
	//    user values.
	md, derr := decodeInto(path, &cfg)
	if derr != nil {
		return nil, derr
	}
	if derr := checkDeprecated(md); derr != nil {
		return nil, derr
	}
	cfg.dir = filepath.Dir(absPath(path))

	// 3. Validate the merged config.
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
	if md.IsDefined("sandbox") {
		// Last, so every sentinel above still wins for the key it names.
		return ErrMovedAgentSectionToProfile
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
		// Unlike the other validate failures below, cfg itself is returned
		// alongside this error: doctor's checkProfiles needs
		// cfg.CommandProfilePath() to report the dedicated, actionable
		// "write the profile, or point command_profile at it" hint instead of
		// the generic "fix the config first" one — see cmd/doctor.go.
		return cfg, fmt.Errorf("%w: %s", ErrCommandProfileMissing, cfg.CommandProfilePath())
	}

	// command_output_dir is only consumed by the MCP server path, so require it
	// only in mcp mode. In hook mode it is optional and, if set, ignored.
	if cfg.ToolMode == "mcp" && strings.TrimSpace(cfg.MCP.CommandOutputDir) == "" {
		return nil, ErrMissingMCPCommandOutputDir
	}
	return cfg, nil
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
