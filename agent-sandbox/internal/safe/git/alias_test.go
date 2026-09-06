package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe/git"
)

// initRepo creates a real git repository in a fresh temp dir and returns its
// path. It skips the test if git is not on PATH.
func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

// appendConfig appends body directly to the repo's .git/config, the route a
// "git config" argv rule can never see: no execve happens to install it.
func appendConfig(t *testing.T, dir, body string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
}

// withFakeGitReal makes git.RealBinary ("git-real") resolvable on PATH for
// the duration of the test, as a shell shim that re-execs the real "git"
// binary with argv[0] fixed back to "git". This lets the test exercise the
// real production lookup path (exec.LookPath(RealBinary) then "git-real
// config --get ...") without requiring the sandbox's own "git-real" name to
// exist in the test environment.
//
// A plain symlink named "git-real" is not equivalent: git's own dispatch
// looks at argv[0]'s basename, and a "git-<word>" argv[0] makes git try to
// run "<word>" as a builtin directly (measured: "git-real config --get x"
// invoked that way fails with "fatal: cannot handle real as a builtin",
// silently dropping the real arguments). In production this does not arise
// because nono's shim hands off to the real binary with its own argv[0], not
// the invoked command name — this shim reproduces that handoff.
func withFakeGitReal(t *testing.T) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found in PATH")
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, git.RealBinary)
	script := "#!/bin/sh\nexec -a git " + shellQuote(real) + ` "$@"` + "\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// shellQuote wraps s in single quotes for safe use as one /bin/sh word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func TestAliasExpansion_DirectConfigWrite_HardResetRefused(t *testing.T) {
	dir := initRepo(t)
	// The attack this rule exists for: the alias is installed by a direct
	// write to .git/config, not by "git config" and not by "-c alias.x=...".
	// Neither of the other alias rules (config-write, alias-injection) can
	// see this route at all.
	appendConfig(t, dir, "[alias]\n\th = reset --hard\n")
	withFakeGitReal(t)
	t.Chdir(dir)

	vs := git.Check([]string{"h"})
	if len(vs) == 0 {
		t.Fatal("expected alias \"h\" (= reset --hard) to be refused, got no violations")
	}
	t.Logf("violations: %v", vs)
}

func TestAliasExpansion_OrdinaryAlias_Passes(t *testing.T) {
	dir := initRepo(t)
	appendConfig(t, dir, "[alias]\n\tco = checkout\n")
	withFakeGitReal(t)
	t.Chdir(dir)

	vs := git.Check([]string{"co", "main"})
	if len(vs) != 0 {
		t.Errorf("expected alias \"co\" (= checkout) to pass, got violations %v", vs)
	}
}

func TestAliasExpansion_ShellAlias_Refused(t *testing.T) {
	dir := initRepo(t)
	appendConfig(t, dir, "[alias]\n\tsh = !echo hi\n")
	withFakeGitReal(t)
	t.Chdir(dir)

	vs := git.Check([]string{"sh"})
	if len(vs) == 0 {
		t.Fatal("expected shell alias \"sh\" to be refused, got no violations")
	}
}

func TestAliasExpansion_UnresolvedName_Refused(t *testing.T) {
	dir := initRepo(t)
	withFakeGitReal(t)
	t.Chdir(dir)

	// "bogus" is neither a real git command nor a configured alias.
	vs := git.Check([]string{"bogus"})
	if len(vs) == 0 {
		t.Fatal("expected an unresolvable name to be refused, got no violations")
	}
}

func TestAliasExpansion_NoGitReal_RefusesRatherThanAllow(t *testing.T) {
	dir := initRepo(t)
	t.Chdir(dir)
	// Deliberately do not call withFakeGitReal: RealBinary cannot be found.
	t.Setenv("PATH", "")

	vs := git.Check([]string{"h"})
	if len(vs) == 0 {
		t.Fatal("expected refusal when the alias cannot be resolved at all, got none")
	}
}

func TestAliasExpansion_NestedAlias_ExpandsAndRefuses(t *testing.T) {
	dir := initRepo(t)
	// "outer" expands to "inner", which expands to "reset --hard": neither
	// level is a known git subcommand, so both must be resolved in turn.
	appendConfig(t, dir, "[alias]\n\touter = inner\n\tinner = reset --hard\n")
	withFakeGitReal(t)
	t.Chdir(dir)

	vs := git.Check([]string{"outer"})
	if len(vs) == 0 {
		t.Fatal("expected the nested alias to expand down to reset --hard and be refused")
	}
}

func TestAliasExpansion_KnownSubcommand_NotTreatedAsAlias(t *testing.T) {
	dir := initRepo(t)
	// Do NOT call withFakeGitReal: if "status" were (wrongly) treated as a
	// candidate alias, resolveAlias would try to LookPath git-real, fail
	// (nothing put it on PATH here), and the invocation would be refused.
	// Passing here proves known subcommands skip alias resolution entirely.
	t.Chdir(dir)

	vs := git.Check([]string{"status"})
	if len(vs) != 0 {
		t.Errorf("expected \"status\" to pass without any alias lookup, got %v", vs)
	}
}
