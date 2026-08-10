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

type SandboxConfig struct {
	Network NetworkConfig `toml:"network"`
	Command CommandConfig `toml:"command"`
	Host    HostConfig    `toml:"host"`
}

type NetworkConfig struct {
	AllowDomains []string `toml:"allow_domains"`
}

type CommandConfig struct {
	Allow          []string   `toml:"allow"`
	Drop           []DropRule `toml:"drop"`
	EnvPassthrough []string   `toml:"env_passthrough"`
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

// HostConfig declares, in nono-agnostic terms, the host-side access the
// sandboxed agent process gets. Capabilities are named bundles expanded by
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
// then validates the merged result. Scalars: project overrides user. Lists
// (command.allow, command.drop, command.env_passthrough,
// container.env_passthrough, network.allow_domains, and the sandbox.host
// lists): de-duplicated union.
func Load(path string) (*Config, error) {
	var cfg Config

	// 1. User config is the base (optional). Snapshot its list fields before the
	//    project decode can replace them. The snapshots must be *clones*: TOML
	//    decode reuses an existing slice's backing array in place when its cap is
	//    large enough, so a plain header copy would be corrupted by the project
	//    decode below.
	var userAllow []string
	var userDrop []DropRule
	var userAllowDomains, userCmdEnv []string
	var hostSnap hostLists
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
			userAllowDomains = slices.Clone(cfg.Sandbox.Network.AllowDomains)
			userCmdEnv = slices.Clone(cfg.Sandbox.Command.EnvPassthrough)
			userHostCaps := slices.Clone(cfg.Sandbox.Host.Capabilities)
			userHostAllow := slices.Clone(cfg.Sandbox.Host.Allow)
			userHostRead := slices.Clone(cfg.Sandbox.Host.Read)
			userHostAllowFile := slices.Clone(cfg.Sandbox.Host.AllowFile)
			userHostReadFile := slices.Clone(cfg.Sandbox.Host.ReadFile)
			userHostAllowEnv := slices.Clone(cfg.Sandbox.Host.AllowEnv)
			hostSnap = hostLists{userHostCaps, userHostAllow, userHostRead, userHostAllowFile, userHostReadFile, userHostAllowEnv}
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
	cfg.Sandbox.Network.AllowDomains = dedupUnion(userAllowDomains, cfg.Sandbox.Network.AllowDomains)
	cfg.Sandbox.Command.EnvPassthrough = dedupUnion(userCmdEnv, cfg.Sandbox.Command.EnvPassthrough)
	cfg.Sandbox.Host.Capabilities = dedupUnion(hostSnap.caps, cfg.Sandbox.Host.Capabilities)
	cfg.Sandbox.Host.Allow = dedupUnion(hostSnap.allow, cfg.Sandbox.Host.Allow)
	cfg.Sandbox.Host.Read = dedupUnion(hostSnap.read, cfg.Sandbox.Host.Read)
	cfg.Sandbox.Host.AllowFile = dedupUnion(hostSnap.allowFile, cfg.Sandbox.Host.AllowFile)
	cfg.Sandbox.Host.ReadFile = dedupUnion(hostSnap.readFile, cfg.Sandbox.Host.ReadFile)
	cfg.Sandbox.Host.AllowEnv = dedupUnion(hostSnap.allowEnv, cfg.Sandbox.Host.AllowEnv)

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

// checkDeprecated rejects the removed sandbox.network.allow_cidrs /
// allow_hosts / allow_external keys and the removed sandbox.container
// section. It is applied per-file because the keys are not representable in
// Config, and must fire even when the file merely mentions a removed key with
// no value that validate would otherwise check.
func checkDeprecated(md toml.MetaData) error {
	if md.IsDefined("sandbox", "network", "allow_cidrs") || md.IsDefined("sandbox", "network", "allow_hosts") {
		return ErrDeprecatedNetworkKeys
	}
	if md.IsDefined("sandbox", "network", "allow_external") {
		return ErrRemovedAllowExternal
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
	for _, name := range cfg.Sandbox.Command.EnvPassthrough {
		if strings.HasPrefix(strings.TrimSpace(name), "NONO_") {
			return nil, fmt.Errorf("%w: %q", ErrEnvPassthroughNonoVar, name)
		}
	}
	return cfg, nil
}

// hostLists snapshots the user-scope sandbox.host list fields so they can be
// unioned with the project-scope values after the project decode.
type hostLists struct {
	caps, allow, read, allowFile, readFile, allowEnv []string
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
