package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/agentconfig"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Write agent context doc to .claude/rules and register it in .gitignore",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
		return runInitIn(wd, cmd.OutOrStdout())
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func runInitIn(dir string, out io.Writer) error {
	rulesDir := filepath.Join(dir, ".claude", "rules")
	docPath := filepath.Join(rulesDir, "agent-sandbox.md")

	if err := os.MkdirAll(rulesDir, 0755); err != nil {
		return fmt.Errorf("create .claude/rules dir: %w", err)
	}

	if err := os.WriteFile(docPath, []byte(agentconfig.Content()), 0644); err != nil {
		return fmt.Errorf("write %s: %w", docPath, err)
	}
	fmt.Fprintln(out, "wrote .claude/rules/agent-sandbox.md")

	added, err := appendLineIfAbsent(filepath.Join(dir, ".gitignore"), ".claude/rules/agent-sandbox.md")
	if err != nil {
		return fmt.Errorf("update .gitignore: %w", err)
	}
	if added {
		fmt.Fprintln(out, "added .claude/rules/agent-sandbox.md to .gitignore")
	}

	return nil
}

// appendLineIfAbsent appends line+"\n" to path if that exact line is not already
// present as a complete line. Creates the file if it does not exist.
// Returns true if the line was added.
func appendLineIfAbsent(path, line string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for _, l := range strings.Split(string(data), "\n") {
		if l == line {
			return false, nil
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	prefix := ""
	if len(data) > 0 && data[len(data)-1] != '\n' {
		prefix = "\n"
	}
	_, err = fmt.Fprintf(f, "%s%s\n", prefix, line)
	return err == nil, err
}
