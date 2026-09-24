package scaffold

import (
	"slices"
	"strings"
	"testing"
)

func TestBaseURLPinsMain(t *testing.T) {
	if !strings.HasSuffix(BaseURL, "/main/") {
		t.Errorf("BaseURL does not fetch from main: %q", BaseURL)
	}
}

func TestAssetsCoverEveryDestination(t *testing.T) {
	want := []string{
		"agent-sandbox.toml",
		"command-profile.json",
		"claude-profile.json",
		".claude/skills/growing-a-nono-profile/SKILL.md",
		".claude/skills/verifying-nono-sandbox-claims/SKILL.md",
	}
	if len(Assets) != len(want) {
		t.Fatalf("Assets has %d entries, want %d", len(Assets), len(want))
	}
	for _, a := range Assets {
		if !slices.Contains(want, a.Dest) {
			t.Errorf("unexpected destination %q", a.Dest)
			continue
		}
		// Only the two JSON profiles are handed to nono.
		isProfile := strings.HasSuffix(a.Dest, "-profile.json")
		if a.Profile != isProfile {
			t.Errorf("%s: Profile is %v, want %v", a.Dest, a.Profile, isProfile)
		}
		if a.Source == "" {
			t.Errorf("%s: empty Source", a.Dest)
		}
		if strings.HasPrefix(a.Source, "/") {
			t.Errorf("%s: Source must be repository-relative, got %q", a.Dest, a.Source)
		}
	}
}

func TestSkillsAreFetchedFromTheirRepositoryLocation(t *testing.T) {
	for _, a := range Assets {
		if !strings.HasSuffix(a.Dest, "SKILL.md") {
			continue
		}
		if a.Source != a.Dest {
			t.Errorf("skill %q is fetched from %q; it should come from the same path", a.Dest, a.Source)
		}
	}
}
