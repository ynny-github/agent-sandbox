package config

import (
	"fmt"
	"os"
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
	Network   NetworkConfig   `toml:"network"`
	Command   CommandConfig   `toml:"command"`
	Container ContainerConfig `toml:"container"`
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

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	defer f.Close()

	var cfg Config
	md, err := toml.NewDecoder(f).Decode(&cfg)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if md.IsDefined("sandbox", "network", "allow_cidrs") || md.IsDefined("sandbox", "network", "allow_hosts") {
		return nil, ErrDeprecatedNetworkKeys
	}

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
	return &cfg, nil
}
