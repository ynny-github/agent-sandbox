// agent-sandbox/cmd/claude.go
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/nono"
)

var yolo bool

var claudeCmd = &cobra.Command{
	Use:   "claude [--yolo] [-- <claude-args>...]",
	Short: "Run Claude via nono sandbox",
	Args:  cobra.ArbitraryArgs,
	RunE:  runClaude,
}

func init() {
	claudeCmd.Flags().BoolVar(&yolo, "yolo", false, "use yolo nono config")
	rootCmd.AddCommand(claudeCmd)
}

func validateClaudePassthrough(args []string) error {
	for _, arg := range args {
		if strings.HasPrefix(arg, "--settings") {
			return fmt.Errorf("--settings is not allowed")
		}
	}
	return nil
}

func runClaude(cmd *cobra.Command, args []string) error {
	var claudeArgs []string
	if dashIdx := cmd.ArgsLenAtDash(); dashIdx >= 0 {
		claudeArgs = args[dashIdx:]
	}

	if err := validateClaudePassthrough(claudeArgs); err != nil {
		return err
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	tomlPath := cfg.Nono.Config
	if yolo {
		tomlPath = cfg.Nono.YoloConfig
	}
	if tomlPath == "" {
		return fmt.Errorf("[nono] config path not set in agent-sandbox.toml")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("error getting cwd: %w", err)
	}

	profile, err := nono.GenerateProfile(tomlPath, cwd)
	if err != nil {
		return fmt.Errorf("nono profile: %w", err)
	}

	outputDir := cfg.Nono.OutputDir
	if outputDir == "" {
		outputDir = cwd
	}
	base := strings.TrimSuffix(filepath.Base(tomlPath), ".toml") + ".json"
	jsonPath := filepath.Join(outputDir, base)

	if err := os.WriteFile(jsonPath, profile, 0600); err != nil {
		return fmt.Errorf("write profile: %w", err)
	}

	nonoPath, err := exec.LookPath("nono")
	if err != nil {
		return fmt.Errorf("nono not found in PATH: %w", err)
	}

	nonoArgs := []string{"nono", "run", "--profile", jsonPath, "claude", "--enable-auto-mode"}
	if yolo && cfg.Claude.YoloSettings != "" {
		nonoArgs = append(nonoArgs, "--settings", cfg.Claude.YoloSettings)
	}
	nonoArgs = append(nonoArgs, claudeArgs...)

	if err := syscall.Exec(nonoPath, nonoArgs, os.Environ()); err != nil {
		return fmt.Errorf("exec nono: %w", err)
	}
	return nil
}
