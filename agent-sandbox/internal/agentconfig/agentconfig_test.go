package agentconfig_test

import (
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/agentconfig"
)

func TestContent_ContainsKeyPhrases(t *testing.T) {
	got := agentconfig.Content()
	for _, want := range []string{
		"Command Router",
		"run_command",
		"native shell commands",
		"host",
		"container",
		"stdout/stderr",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Content() missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestContent_NonEmpty(t *testing.T) {
	if agentconfig.Content() == "" {
		t.Fatal("Content() returned empty string")
	}
}
