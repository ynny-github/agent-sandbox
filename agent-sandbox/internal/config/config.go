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
	ToolMode string        `toml:"tool_mode"`
	MCP      MCPConfig     `toml:"mcp"`
	Sandbox  SandboxConfig `toml:"sandbox"`
}

type MCPConfig struct {
	CommandOutputDir string `toml:"command_output_dir"`
}

// SandboxConfig spans the two sandboxes agent-sandbox generates a profile for:
// the launched agent and each brokered command. Host is the shared base,
// expanded into both; Agent.Host and Command.Host add grants to exactly one of
// them.
//
// Nothing is inherited between the two sides. A grant reaches a profile only if
// it is written in that side's section or in the shared base, which is why
// there is no subtraction axis: anything you do not want in a profile is simply
// not written where that profile can see it.
type SandboxConfig struct {
	Host    HostConfig    `toml:"host"`
	Agent   SideConfig    `toml:"agent"`
	Command CommandConfig `toml:"command"`
}

// SideConfig is one side's host-access section. Only the agent side uses this
// shape on its own; the command side carries the same Host field alongside its
// routing and network settings in CommandConfig.
type SideConfig struct {
	Host HostConfig `toml:"host"`
}

type NetworkConfig struct {
	AllowDomains []string `toml:"allow_domains"`
}

// CommandConfig holds everything scoped to brokered commands: how a command is
// routed (Allow/Drop), the host access its sandbox gets (Host), and the network
// it may reach (Network).
type CommandConfig struct {
	Allow   []string      `toml:"allow"`
	Drop    []DropRule    `toml:"drop"`
	Host    HostConfig    `toml:"host"`
	Network NetworkConfig `toml:"network"`
}

// DropRule is one drop pattern with an optional custom refusal message. Every
// drop entry is written as a table so the shape is the same with or without a
// message:
//
//	drop = [
//	  { pattern = "git *" },
//	  { pattern = "gh *", message = "gh is disabled" },
//	]
//
// An omitted message leaves Message empty, and the router falls back to the
// default `dropped: command matches drop pattern "<pattern>"` line.
type DropRule struct {
	Pattern string `toml:"pattern"`
	Message string `toml:"message"`
}

// HostConfig declares, in nono-agnostic terms, host-side access for one
// sandbox. Capabilities are named bundles expanded by internal/sandboxhost; the
// remaining lists are raw grants.
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
// then validates the merged result. Scalars: project overrides user. Lists
// (command.allow, command.drop, command.network.allow_domains, and every list
// in the three host sections): de-duplicated union.
func Load(path string) (*Config, error) {
	var cfg Config

	// 1. User config is the base (optional). Snapshot its list fields before the
	//    project decode can replace them. The snapshots must be *clones*: TOML
	//    decode reuses an existing slice's backing array in place when its cap is
	//    large enough, so a plain header copy would be corrupted by the project
	//    decode below.
	var userAllow []string
	var userDrop []DropRule
	var userAllowDomains []string
	var userHost, userAgentHost, userCommandHost HostConfig
	if up, err := userConfigPath(); err == nil {
		if _, statErr := os.Stat(up); statErr == nil {
			md, derr := decodeInto(up, &cfg)
			if derr != nil {
				return nil, derr
			}
			if derr := checkDeprecated(md); derr != nil {
				return nil, derr
			}
			userAllow = slices.Clone(cfg.Sandbox.Command.Allow)
			userDrop = slices.Clone(cfg.Sandbox.Command.Drop)
			userAllowDomains = slices.Clone(cfg.Sandbox.Command.Network.AllowDomains)
			userHost = cloneHost(cfg.Sandbox.Host)
			userAgentHost = cloneHost(cfg.Sandbox.Agent.Host)
			userCommandHost = cloneHost(cfg.Sandbox.Command.Host)
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

	// 3. Union the list fields. When the project omits a list, cfg still holds the
	//    user's, so the union de-dupes back to the user's list (no change).
	cfg.Sandbox.Command.Allow = dedupUnion(userAllow, cfg.Sandbox.Command.Allow)
	cfg.Sandbox.Command.Drop = dedupUnionDrop(userDrop, cfg.Sandbox.Command.Drop)
	cfg.Sandbox.Command.Network.AllowDomains = dedupUnion(userAllowDomains, cfg.Sandbox.Command.Network.AllowDomains)
	cfg.Sandbox.Host = unionHost(userHost, cfg.Sandbox.Host)
	cfg.Sandbox.Agent.Host = unionHost(userAgentHost, cfg.Sandbox.Agent.Host)
	cfg.Sandbox.Command.Host = unionHost(userCommandHost, cfg.Sandbox.Command.Host)

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

// checkDeprecated rejects keys that were removed or moved: the old
// sandbox.network section (now sandbox.command.network), the
// sandbox.command.env_passthrough list (now sandbox.command.host.allow_env),
// and the sandbox.container section. It is applied per-file because the keys
// are not representable in Config, and must fire even when the file merely
// mentions a removed key with no value that validate would otherwise check.
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
	if md.IsDefined("sandbox", "command", "env_passthrough") {
		return ErrMovedEnvPassthrough
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

	// command_output_dir is only consumed by the MCP server path, so require it
	// only in mcp mode. In hook mode it is optional and, if set, ignored.
	if cfg.ToolMode == "mcp" && strings.TrimSpace(cfg.MCP.CommandOutputDir) == "" {
		return nil, ErrMissingMCPCommandOutputDir
	}
	for _, r := range cfg.Sandbox.Command.Drop {
		if strings.TrimSpace(r.Pattern) == "" {
			return nil, ErrDropRuleMissingPattern
		}
	}
	// NONO_* is rejected in every allow_env list, not just the command side: the
	// launched agent spawns the commands, so a NONO_* variable reaching either
	// profile can reconfigure the command sandbox.
	for _, h := range []HostConfig{cfg.Sandbox.Host, cfg.Sandbox.Agent.Host, cfg.Sandbox.Command.Host} {
		for _, name := range h.AllowEnv {
			if strings.HasPrefix(strings.TrimSpace(name), "NONO_") {
				return nil, fmt.Errorf("%w: %q", ErrAllowEnvNonoVar, name)
			}
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

// dedupUnionDrop is dedupUnion for drop rules, de-duplicating by pattern with
// first-occurrence order (a's rules first). When a pattern appears in both a and
// b, a's rule — including its message — wins, matching the user-first union used
// for the other list fields.
func dedupUnionDrop(a, b []DropRule) []DropRule {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]DropRule, 0, len(a)+len(b))
	for _, list := range [][]DropRule{a, b} {
		for _, r := range list {
			if _, ok := seen[r.Pattern]; ok {
				continue
			}
			seen[r.Pattern] = struct{}{}
			out = append(out, r)
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
