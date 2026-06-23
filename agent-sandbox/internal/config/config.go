package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server        ServerConfig        `toml:"server"`
	Sandbox       SandboxConfig       `toml:"sandbox"`
	AllowPatterns AllowPatternsConfig `toml:"allow_patterns"`
	DenyPatterns  DenyPatternsConfig  `toml:"deny_patterns"`
	DropPatterns  DropPatternsConfig  `toml:"drop_patterns"`
	Container     ContainerConfig     `toml:"container"`
	Nono          NonoConfig          `toml:"nono"`
}

type ServerConfig struct {
	OutputDir string `toml:"output_dir"`
}

type SandboxConfig struct {
	BuildContext    string   `toml:"build_context"`
	Dockerfile      string   `toml:"dockerfile"`
	Image           string   `toml:"image"`
	AllowCIDRs      []string `toml:"allow_cidrs"`
	AllowHosts      []string `toml:"allow_hosts"`
	ExternalNetwork string   `toml:"external_network"`
}

type ContainerConfig struct {
	EnvPassthrough []string `toml:"env_passthrough"`
}

type AllowPatternsConfig struct {
	Patterns []string `toml:"patterns"`
}

type DenyPatternsConfig struct {
	Patterns []string `toml:"patterns"`
}

type DropPatternsConfig struct {
	Patterns []string `toml:"patterns"`
}

type NonoConfig struct {
	Profile    string `toml:"profile"`
	Subcommand string `toml:"subcommand"`
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

	if strings.TrimSpace(cfg.Server.OutputDir) == "" {
		return nil, ErrMissingOutputDir
	}
	if strings.TrimSpace(cfg.Sandbox.BuildContext) == "" {
		return nil, ErrMissingSandboxBuildContext
	}
	if strings.TrimSpace(cfg.Sandbox.Dockerfile) == "" {
		return nil, ErrMissingSandboxDockerfile
	}
	if strings.TrimSpace(cfg.Sandbox.Image) == "" {
		return nil, ErrMissingSandboxImage
	}
	cfg.Nono.Subcommand = strings.TrimSpace(cfg.Nono.Subcommand)
	if sub := cfg.Nono.Subcommand; sub != "" && sub != "run" && sub != "wrap" {
		return nil, fmt.Errorf("%w: got %q", ErrInvalidNonoSubcommand, sub)
	}
	return &cfg, nil
}
