//go:build e2e

package launch_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/internal/launch"
)

// TestLaunch_TemplateRoutesACommandThroughExecd builds both binaries, writes
// the repository's template into a temp project, and launches it with a fake
// `claude` that runs one command through execd from inside the agent's
// sandbox. The approval prompt is answered through `script`, which supplies
// the /dev/tty the prompt reads.
func TestLaunch_TemplateRoutesACommandThroughExecd(t *testing.T) {
	for _, tool := range []string{"nono", "script"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not on PATH")
		}
	}
	root, _ := filepath.Abs("../..")
	// The binaries live under ~/.cache, not /tmp: the template's command
	// profile can write /tmp, and doctor rightly refuses a binary the sandbox
	// could replace.
	home, _ := os.UserHomeDir()
	scratch, err := os.MkdirTemp(filepath.Join(home, ".cache"), "asb-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(scratch) })
	bin := filepath.Join(scratch, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"agent-sandbox", "execd"} {
		b := exec.Command("go", "build", "-o", filepath.Join(bin, cmd), "./cmd/"+cmd)
		b.Dir = root
		if out, err := b.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", cmd, err, out)
		}
	}
	proj := t.TempDir()
	// The fake claude: run one command through execd, then exit. It lives in
	// the project, which the launch script's --allow-cwd lets the agent read;
	// the real claude's directory is granted by the agent profile's pack.
	fakeBin := filepath.Join(proj, "fakebin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	// It also checks, from inside the agent sandbox, that the hook settings it
	// was given are usable: inline JSON, or a file it can read. A settings file
	// the sandbox cannot read would leave Claude without the hook.
	fake := `#!/bin/sh
settings=""
while [ $# -gt 0 ]; do
  if [ "$1" = --settings ]; then settings=$2; fi
  shift
done
case "$settings" in
  "{"*) ;;
  *) cat "$settings" >/dev/null || { echo "settings unreadable: $settings" > "$PWD/result"; exit 1; } ;;
esac
execd exec -- echo routed-ok > "$PWD/result"
`
	if err := os.WriteFile(filepath.Join(fakeBin, "claude"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cp := exec.Command("cp", "-r", filepath.Join(root, "templates/minimal/.agent-sandbox"), proj)
	if out, err := cp.CombinedOutput(); err != nil {
		t.Fatalf("copy template: %v\n%s", err, out)
	}
	// XDG_CONFIG_HOME stays the user's: nono finds installed profile packs
	// (the template extends nolabs-ai/claude) under it. The approved copy this
	// launch creates is keyed by the temp project path, so it is removed here.
	trusted, err := launch.TrustedDir(proj)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(trusted) })
	// Bounded: a failing launch can leave nono waiting, and a hung e2e test
	// would leave its sessions behind.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := exec.CommandContext(ctx, "script", "-qec", "agent-sandbox launch claude", "/dev/null")
	run.Dir = proj
	run.Env = append(os.Environ(), "PATH="+fakeBin+":"+bin+":"+os.Getenv("PATH"))
	run.Stdin = strings.NewReader("y\n")
	out, err := run.CombinedOutput()
	got, _ := os.ReadFile(filepath.Join(proj, "result"))
	if err != nil {
		t.Fatalf("launch: %v\nresult: %s\n%s", err, got, out)
	}
	if strings.TrimSpace(string(got)) != "routed-ok" {
		t.Errorf("result = %q, want routed-ok\n%s", got, out)
	}
}
