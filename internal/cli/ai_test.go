package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/internal/agentconfig"
)

func TestRunExplain_NamesTheProfilesFromItsFlags(t *testing.T) {
	prevC, prevA := explainCommandProfile, explainAgentProfile
	explainCommandProfile, explainAgentProfile = "/p/cmd.json", "/p/agent.json"
	t.Cleanup(func() { explainCommandProfile, explainAgentProfile = prevC, prevA })

	var buf bytes.Buffer
	explainCmd.SetOut(&buf)
	if err := runExplain(explainCmd, nil); err != nil {
		t.Fatalf("runExplain: %v", err)
	}
	for _, want := range []string{"# agent-sandbox environment", "/p/cmd.json", "/p/agent.json"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n%s", want, buf.String())
		}
	}
}

// config-check hands both profiles to nono rather than describing them: the
// only authority on what a profile grants is nono itself.
func TestAiPointer_PrintsThePointer(t *testing.T) {
	var out bytes.Buffer
	pointerCmd.SetOut(&out)
	pointerCmd.Run(pointerCmd, nil)
	if out.String() != agentconfig.Pointer() {
		t.Errorf("ai pointer = %q, want %q", out.String(), agentconfig.Pointer())
	}
}
