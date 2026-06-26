package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	MCP     MCPConfig     `toml:"mcp"`
	Sandbox SandboxConfig `toml:"sandbox"`
	Nono    NonoConfig    `toml:"nono"`
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
	AllowCIDRs []string `toml:"allow_cidrs"`
	AllowHosts []string `toml:"allow_hosts"`
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

type NonoConfig struct {
	Profile string `toml:"profile"`
}

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	defer f.Close()

	var cfg Config
	if _, err := toml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	if strings.TrimSpace(cfg.MCP.CommandOutputDir) == "" {
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
