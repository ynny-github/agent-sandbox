package scaffold

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// stubRun replaces the nono invocation for the duration of one test.
func stubRun(t *testing.T, fn func(ctx context.Context, name string, args ...string) ([]byte, error)) {
	t.Helper()
	orig := runCommand
	runCommand = fn
	t.Cleanup(func() { runCommand = orig })
}

func fetchedProfiles() []Fetched {
	var out []Fetched
	for _, a := range Assets {
		out = append(out, Fetched{Asset: a, Body: []byte("{}")})
	}
	return out
}

func TestValidateRunsNonoOnBothProfilesOnly(t *testing.T) {
	var seen []string
	stubRun(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "nono" {
			t.Errorf("ran %q, want nono", name)
		}
		seen = append(seen, args[len(args)-1])
		return []byte("  Result: valid\n"), nil
	})
	warns, err := Validate(context.Background(), fetchedProfiles())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if len(seen) != 2 {
		t.Fatalf("nono was run %d times, want 2 (one per profile)", len(seen))
	}
	for _, path := range seen {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("temporary profile %s outlived Validate", path)
		}
	}
}

func TestValidateReturnsWarningsWithoutFailing(t *testing.T) {
	stubRun(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("  [warn] [allow_all_network] command 'ssh' from.git allows unrestricted child network\n  Result: valid (1 warning)\n"), nil
	})
	warns, err := Validate(context.Background(), fetchedProfiles())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(warns) != 2 {
		t.Fatalf("got %d warnings, want 2 (one per profile)", len(warns))
	}
	if !strings.Contains(warns[0], "allow_all_network") {
		t.Errorf("warning text lost: %q", warns[0])
	}
}

func TestValidateFailsAndCarriesNonoOutput(t *testing.T) {
	stubRun(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("  [err]  Profile parse error: unknown field `bogus`\n  Result: invalid (1 error)\n"), fmt.Errorf("exit status 1")
	})
	_, err := Validate(context.Background(), fetchedProfiles())
	if err == nil {
		t.Fatal("Validate accepted a profile nono rejected")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("error drops nono's own output: %v", err)
	}
	if !strings.Contains(err.Error(), "command-profile.json") {
		t.Errorf("error does not name the profile: %v", err)
	}
}
