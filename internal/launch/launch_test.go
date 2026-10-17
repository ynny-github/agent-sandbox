package launch

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// project makes a temp project with a .agent-sandbox/ holding files (path →
// content; a path ending in "*" is made executable, the "*" stripped), chdirs
// into it, and points XDG_CONFIG_HOME at a temp dir.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, "*") {
			name, mode = strings.TrimSuffix(name, "*"), 0o755
		}
		p := filepath.Join(dir, Dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return dir
}

var basic = map[string]string{
	"bin/claude*":          "#!/bin/sh\necho hi\n",
	"command-profile.json": "{}",
}

// fakes returns Options that answer the prompt with answer, report the copy
// dir not writable, and record what would be exec'd.
type recorder struct {
	prompts int
	diff    string
	execed  string
	argv    []string
}

func fakes(r *recorder, answer bool) Options {
	return Options{
		Prompt:   func(d string) (bool, error) { r.prompts++; r.diff = d; return answer, nil },
		Writable: func(string, string) (bool, error) { return false, nil },
		Exec:     func(p string, argv, _ []string) error { r.execed, r.argv = p, argv; return nil },
		Out:      io.Discard,
	}
}

func trusted(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	d, err := TrustedDir(wd)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRun_FirstLaunchAsksAndCopies(t *testing.T) {
	project(t, basic)
	var r recorder
	if err := Run("claude", []string{"-p", "x"}, fakes(&r, true)); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if r.prompts != 1 || !strings.Contains(r.diff, "bin/claude") {
		t.Errorf("prompts = %d, diff = %q; want one prompt naming bin/claude", r.prompts, r.diff)
	}
	if b, err := os.ReadFile(filepath.Join(trusted(t), "bin", "claude")); err != nil || !strings.Contains(string(b), "echo hi") {
		t.Errorf("approved copy of bin/claude = %q (err %v)", b, err)
	}
	if strings.Join(r.argv[1:], " ") != "-p x" {
		t.Errorf("argv = %v, want the script followed by -p x", r.argv)
	}
}

func TestRun_UnchangedDoesNotAsk(t *testing.T) {
	project(t, basic)
	var r recorder
	Run("claude", nil, fakes(&r, true))
	r = recorder{}
	if err := Run("claude", nil, fakes(&r, true)); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if r.prompts != 0 || r.execed == "" {
		t.Errorf("prompts = %d, execed = %q; want no prompt and a launch", r.prompts, r.execed)
	}
}

func TestRun_DeclinedChangeRunsNothingAndKeepsTheCopy(t *testing.T) {
	dir := project(t, basic)
	var r recorder
	Run("claude", nil, fakes(&r, true))
	os.WriteFile(filepath.Join(dir, Dir, "bin", "claude"), []byte("#!/bin/sh\nevil\n"), 0o755)

	r = recorder{}
	err := Run("claude", nil, fakes(&r, false))
	if !errors.Is(err, ErrDeclined) || r.execed != "" {
		t.Fatalf("Run() = %v, execed %q; want ErrDeclined and nothing run", err, r.execed)
	}
	if !strings.Contains(r.diff, "evil") {
		t.Errorf("diff = %q, want it to show the change", r.diff)
	}
	if b, _ := os.ReadFile(filepath.Join(trusted(t), "bin", "claude")); strings.Contains(string(b), "evil") {
		t.Error("a declined change reached the approved copy")
	}
}

func TestRun_ExecsTheApprovedCopy(t *testing.T) {
	project(t, basic)
	var r recorder
	if err := Run("claude", nil, fakes(&r, true)); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(trusted(t), "bin", "claude"); r.execed != want || r.argv[0] != want {
		t.Errorf("execed %q (argv0 %q), want the approved copy %q", r.execed, r.argv[0], want)
	}
}

func TestRun_RemovedFileIsAChange(t *testing.T) {
	dir := project(t, map[string]string{"bin/claude*": "x", "command-profile.json": "{}", "extra": "e"})
	var r recorder
	Run("claude", nil, fakes(&r, true))
	os.Remove(filepath.Join(dir, Dir, "extra"))
	r = recorder{}
	if err := Run("claude", nil, fakes(&r, true)); err != nil {
		t.Fatal(err)
	}
	if r.prompts != 1 || !strings.Contains(r.diff, "extra") {
		t.Errorf("prompts = %d, diff %q; want the removal shown", r.prompts, r.diff)
	}
	if _, err := os.Stat(filepath.Join(trusted(t), "extra")); !os.IsNotExist(err) {
		t.Errorf("removed file still in the approved copy (stat err %v)", err)
	}
}

func TestRun_ExecutableBitIsCompared(t *testing.T) {
	dir := project(t, map[string]string{"bin/claude*": "x", "command-profile.json": "{}", "helper": "h"})
	var r recorder
	Run("claude", nil, fakes(&r, true))
	os.Chmod(filepath.Join(dir, Dir, "helper"), 0o755)
	r = recorder{}
	Run("claude", nil, fakes(&r, true))
	if r.prompts != 1 {
		t.Errorf("prompts = %d after chmod +x, want 1", r.prompts)
	}
}

func TestRun_RefusesSymlinks(t *testing.T) {
	dir := project(t, basic)
	os.Symlink("/etc/passwd", filepath.Join(dir, Dir, "bin", "link"))
	var r recorder
	if err := Run("claude", nil, fakes(&r, true)); err == nil || r.prompts != 0 {
		t.Fatalf("Run() = %v with %d prompts; want a refusal before asking", err, r.prompts)
	}
	if _, err := os.Stat(trusted(t)); !os.IsNotExist(err) {
		t.Error("an approved copy was created despite the symlink")
	}
}

func TestRun_RefusesBadNames(t *testing.T) {
	project(t, basic)
	for _, name := range []string{"", ".", "..", "bin/claude", "../x"} {
		if err := Run(name, nil, fakes(&recorder{}, true)); !errors.Is(err, ErrUsage) {
			t.Errorf("Run(%q) = %v, want ErrUsage", name, err)
		}
	}
}

func TestRun_MissingDirAndScript(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Run("claude", nil, fakes(&recorder{}, true)); err == nil || !strings.Contains(err.Error(), "agent-sandbox init") {
		t.Errorf("Run() without .agent-sandbox = %v, want the init hint", err)
	}
	project(t, map[string]string{"command-profile.json": "{}", "bin/other*": "x"})
	if err := Run("claude", nil, fakes(&recorder{}, true)); err == nil {
		t.Error("Run() with no bin/claude succeeded")
	}
}

func TestRun_NoTerminalRefusesAChange(t *testing.T) {
	project(t, basic)
	o := fakes(&recorder{}, true)
	o.Prompt = func(string) (bool, error) { return false, errors.New("no terminal") }
	if err := Run("claude", nil, o); err == nil {
		t.Error("Run() approved a change without a terminal")
	}
}

func TestRun_RefusesAWritableCopy(t *testing.T) {
	project(t, basic)
	var r recorder
	o := fakes(&r, true)
	o.Writable = func(profile, path string) (bool, error) {
		if !strings.HasPrefix(profile, path) {
			t.Errorf("writability asked with profile %s, want the approved copy's", profile)
		}
		return true, nil
	}
	if err := Run("claude", nil, o); err == nil || r.execed != "" {
		t.Errorf("Run() = %v, execed %q; want a refusal", err, r.execed)
	}
}
