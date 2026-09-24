// internal/cli/profiles.go
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// validateProfile checks that a nono profile is on disk and that nono accepts
// it. Nothing here parses the file: nono owns the schema, and asking nono is
// the only answer that cannot drift from what a launch will actually accept.
func validateProfile(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("not found: %s", path)
	}
	out, err := runCommand(context.Background(), "nono", "profile", "validate", path)
	if err != nil {
		return fmt.Errorf("nono profile validate %s: %s", path, strings.TrimSpace(string(out)))
	}
	return nil
}

// profileHelp is the two commands that answer any question about a profile.
// agent-sandbox prints them rather than restating what a profile grants.
const profileHelp = `
agent-sandbox does not read either profile. To see what one grants:
  nono profile show <path>
To ask why an access was refused:
  nono why --profile <path> --path <p> --op read|write|readwrite
`
