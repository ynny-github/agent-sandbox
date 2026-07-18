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
	Claude   ClaudeConfig  `toml:"claude"`
	Sandbox  SandboxConfig `toml:"sandbox"`
}

type MCPConfig struct {
	CommandOutputDir string `toml:"command_output_dir"`
}

type ClaudeConfig struct {
	GithubMCP GithubMCPConfig `toml:"github_mcp"`
}

type GithubMCPConfig struct {
	Enabled bool   `toml:"enabled"`
	Secret  string `toml:"secret"`
}

type SandboxConfig struct {
	Network   NetworkConfig   `toml:"network"`
	Command   CommandConfig   `toml:"command"`
	Container ContainerConfig `toml:"container"`
	Host      HostConfig      `toml:"host"`
}

type NetworkConfig struct {
	AllowExternal bool `toml:"allow_external"`
}

type CommandConfig struct {
	Allow []string `toml:"allow"`
	Drop  []string `toml:"drop"`
}

type ContainerConfig struct {
	BuildContext    string   `toml:"build_context"`
	Dockerfile      string   `toml:"dockerfile"`
	Image           string   `toml:"image"`
	ExternalNetwork string   `toml:"external_network"`
	EnvPassthrough  []string `toml:"env_passthrough"`
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
// (command.allow, command.drop, container.env_passthrough): de-duplicated union.
func Load(path string) (*Config, error) {
	var cfg Config

	// 1. User config is the base (optional). Snapshot its list fields before the
	//    project decode can replace them. The snapshots must be *clones*: TOML
	//    decode reuses an existing slice's backing array in place when its cap is
	//    large enough, so a plain header copy would be corrupted by the project
	//    decode below.
	var userAllow, userDrop, userEnv []string
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
			userEnv = slices.Clone(cfg.Sandbox.Container.EnvPassthrough)
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
	cfg.Sandbox.Command.Drop = dedupUnion(userDrop, cfg.Sandbox.Command.Drop)
	cfg.Sandbox.Container.EnvPassthrough = dedupUnion(userEnv, cfg.Sandbox.Container.EnvPassthrough)
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

// checkDeprecated rejects the removed sandbox.network.allow_cidrs / allow_hosts
// keys. It is applied per-file because the keys are not representable in Config.
func checkDeprecated(md toml.MetaData) error {
	if md.IsDefined("sandbox", "network", "allow_cidrs") || md.IsDefined("sandbox", "network", "allow_hosts") {
		return ErrDeprecatedNetworkKeys
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
	if strings.TrimSpace(cfg.Sandbox.Container.BuildContext) == "" {
		return nil, ErrMissingContainerBuildContext
	}
	if strings.TrimSpace(cfg.Sandbox.Container.Dockerfile) == "" {
		return nil, ErrMissingContainerDockerfile
	}
	if strings.TrimSpace(cfg.Sandbox.Container.Image) == "" {
		return nil, ErrMissingContainerImage
	}
	if cfg.Claude.GithubMCP.Enabled && strings.TrimSpace(cfg.Claude.GithubMCP.Secret) == "" {
		return nil, ErrMissingGithubMCPSecret
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
