package claude

import (
	"os"
	"testing"
)

// TestMain clears herdr's variables so the suite behaves the same inside a
// herdr pane as outside one. With HERDR_PANE_ID inherited, run would re-exec
// (see herdrReexecEnv) in every test that does not stub reexec. The herdr
// tests set the variables themselves.
func TestMain(m *testing.M) {
	os.Unsetenv(herdrPaneEnvVar)
	os.Unsetenv(HerdrAgentEnvVar)
	os.Exit(m.Run())
}
