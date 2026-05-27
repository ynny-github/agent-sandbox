// agent-sandbox/cmd/nono_config.go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/nono"
)

var nonoConfigCmd = &cobra.Command{
	Use:   "nono-config",
	Short: "Generate nono JSON profile files from the TOML configuration",
	RunE:  runNonoConfig,
}

func init() {
	rootCmd.AddCommand(nonoConfigCmd)
}

func runNonoConfig(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	if cfg.Nono.Config == "" {
		return fmt.Errorf("[nono] config not set in agent-sandbox.toml")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	outputDir := cfg.Nono.OutputDir
	if outputDir == "" {
		outputDir = cwd
	}

	if _, err := writeNonoProfile(cfg.Nono.Config, cwd, outputDir); err != nil {
		return fmt.Errorf("nono profile: %w", err)
	}
	if cfg.Nono.YoloConfig != "" {
		if _, err := writeNonoProfile(cfg.Nono.YoloConfig, cwd, outputDir); err != nil {
			return fmt.Errorf("nono yolo profile: %w", err)
		}
	}
	return nil
}

func nonoProfileName(tomlPath string) string {
	return strings.TrimSuffix(filepath.Base(tomlPath), ".toml") + ".json"
}

// writeNonoProfile generates a nono JSON profile from tomlPath, scanning scanDir
// for filesystem entries, and writes the result to outputDir/<base>.json.
// Returns the output filename (base name only).
func writeNonoProfile(tomlPath, scanDir, outputDir string) (string, error) {
	data, err := nono.GenerateProfile(tomlPath, scanDir)
	if err != nil {
		return "", err
	}
	base := nonoProfileName(tomlPath)
	jsonPath := filepath.Join(outputDir, base)
	if err := os.WriteFile(jsonPath, data, 0600); err != nil {
		return "", fmt.Errorf("write %s: %w", base, err)
	}
	return base, nil
}
