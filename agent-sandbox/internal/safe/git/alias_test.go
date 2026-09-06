package git_test

import (
	"fmt"
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

// withFakeGitReal makes git.RealBinary ("realgit") resolvable on PATH for the
// duration of the test, as a symlink to the real "git" binary. This lets the
// test exercise the real production lookup path (exec.LookPath(RealBinary)
// then "realgit config --get ...") without requiring the sandbox's own
// "realgit" name to exist in the test environment.
//
// A plain symlink is safe to use here only because RealBinary does not start
// with "git-": an earlier version of this fixture used the name "git-real"
// and had to work around git's own argv[0] dispatch, which treats a
// "git-<word>" argv[0] as an attempt to run "<word>" as a builtin directly
// (measured: "git-real config --get x" invoked that way fails with "fatal:
// cannot handle real as a builtin", silently dropping the real arguments).
// resolveAlias also now forces argv[0] to "git" itself before running, which
// would paper over that even with the old name — this fixture no longer
// needs to, since RealBinary carries no "git-" prefix to trigger it in the
// first place.
func withFakeGitReal(t *testing.T) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found in PATH")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, git.RealBinary)
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
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

// TestAliasExpansion_NewlineInAliasValue_HardResetRefused is CRITICAL 1 from
// the review: git config supports a "\n" escape inside a quoted alias
// value, and git's own tokenizer (split_cmdline) treats it as whitespace.
// Measured on git 2.54.0: [alias] h = "reset --hard\nHEAD" makes
// "git config --get alias.h" report "reset --hard<LF>HEAD" as one config
// value, and "git h" runs it as three tokens: reset --hard HEAD. A
// tokenizer that only splits on space/tab (the bug: splitAliasValue used to
// switch on `c == ' ' || c == '\t'`) treats the embedded newline as part of
// the second token, "--hard\nHEAD", which hasLong/hasShort do not match, so
// hard-reset never fires and this exact .git/config-write attack sails
// through clean. Fails against the pre-fix tokenizer; passes now that
// splitAliasValue treats git's full isspace() set as whitespace.
func TestAliasExpansion_NewlineInAliasValue_HardResetRefused(t *testing.T) {
	dir := initRepo(t)
	appendConfig(t, dir, "[alias]\n\th = \"reset --hard\\nHEAD\"\n")
	withFakeGitReal(t)
	t.Chdir(dir)

	vs := git.Check([]string{"h"})
	if len(vs) == 0 {
		t.Fatal("expected alias \"h\" (= reset --hard<LF>HEAD) to be refused, got no violations")
	}
	t.Logf("violations: %v", vs)
}

// TestAliasExpansion_DashCGlobal_ForwardedToAliasLookup is CRITICAL 2 from
// the review: resolveAlias used to run "config --get alias.<name>" from the
// process's own cwd with none of the invocation's globals forwarded, while
// execGit passes the real invocation's globals (including -C) straight
// through to the real git binary. So a "-C sub" invocation could be checked
// against the alias.z defined in the parent repository while actually
// running the one defined in sub/ — two different repositories, both
// writable directly by the agent. Fails against the pre-fix resolveAlias
// (which resolved the parent's alias.z = status and let "-C sub z" through
// clean); passes now that resolveAlias forwards -C (and the other
// repo-selecting globals) onto its own "config --get" call.
func TestAliasExpansion_DashCGlobal_ForwardedToAliasLookup(t *testing.T) {
	parent := initRepo(t)
	appendConfig(t, parent, "[alias]\n\tz = status\n")

	sub := filepath.Join(parent, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", sub).CombinedOutput(); err != nil {
		t.Fatalf("git init sub: %v: %s", err, out)
	}
	appendConfig(t, sub, "[alias]\n\tz = reset --hard\n")

	withFakeGitReal(t)
	t.Chdir(parent)

	vs := git.Check([]string{"-C", "sub", "z"})
	if len(vs) == 0 {
		t.Fatal(`expected "git -C sub z" to resolve sub's alias.z (reset --hard) and be refused, got no violations`)
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

// TestAliasExpansion_SelfReferentialAlias_TerminatesAndRefuses is IMPORTANT 5
// from the review: nothing else drives recursion past depth 2, so the branch
// that distinguishes "bounded" from "loops forever" was never pinned. An
// alias that expands to itself would recurse without end if maxAliasDepth
// were ever dropped or miswired; this test only passes if checkAlias
// actually stops and refuses instead of hanging (a regression here fails by
// timeout, not by assertion).
func TestAliasExpansion_SelfReferentialAlias_TerminatesAndRefuses(t *testing.T) {
	dir := initRepo(t)
	appendConfig(t, dir, "[alias]\n\tloop = loop\n")
	withFakeGitReal(t)
	t.Chdir(dir)

	vs := git.Check([]string{"loop"})
	if len(vs) == 0 {
		t.Fatal("expected the self-referential alias to be refused, got no violations")
	}
	t.Logf("violations: %v", vs)
}

// TestAliasExpansion_ChainAtDepthCap_Refused pins the exact boundary
// maxAliasDepth enforces: a0..a10 is 11 names, none of them real git
// commands, so resolving a0 all the way down needs 11 alias lookups
// (depth 0..10) and never reaches a real command. The 11th lookup (for a10,
// at depth == maxAliasDepth) must be refused as "nests too deep", not
// attempted.
func TestAliasExpansion_ChainAtDepthCap_Refused(t *testing.T) {
	dir := initRepo(t)
	var cfg strings.Builder
	cfg.WriteString("[alias]\n")
	for i := 0; i <= 10; i++ {
		fmt.Fprintf(&cfg, "\ta%d = a%d\n", i, i+1)
	}
	appendConfig(t, dir, cfg.String())
	withFakeGitReal(t)
	t.Chdir(dir)

	vs := git.Check([]string{"a0"})
	if len(vs) == 0 {
		t.Fatal("expected the 11-deep alias chain to hit the depth cap and be refused, got no violations")
	}
	t.Logf("violations: %v", vs)
}

// TestAliasExpansion_ChainUnderDepthCap_ExpandsToRealCommand checks the other
// side of the same boundary: a chain of exactly maxAliasDepth (10) alias
// names that terminates in a real, dangerous command must still expand and
// be refused for what it actually does, not for hitting the cap.
func TestAliasExpansion_ChainUnderDepthCap_ExpandsToRealCommand(t *testing.T) {
	dir := initRepo(t)
	var cfg strings.Builder
	cfg.WriteString("[alias]\n")
	for i := 0; i < 9; i++ {
		fmt.Fprintf(&cfg, "\ta%d = a%d\n", i, i+1)
	}
	cfg.WriteString("\ta9 = reset --hard\n")
	appendConfig(t, dir, cfg.String())
	withFakeGitReal(t)
	t.Chdir(dir)

	vs := git.Check([]string{"a0"})
	if len(vs) == 0 {
		t.Fatal("expected the 10-deep alias chain to expand down to reset --hard and be refused for that, got no violations")
	}
	for _, v := range vs {
		if strings.Contains(v.Setting, "nests more than") {
			t.Errorf("expected refusal for reset --hard, not the depth cap: %v", vs)
		}
	}
	t.Logf("violations: %v", vs)
}

func TestAliasExpansion_KnownSubcommand_NotTreatedAsAlias(t *testing.T) {
	dir := initRepo(t)
	// Do NOT call withFakeGitReal: if "status" were (wrongly) treated as a
	// candidate alias, resolveAlias would try to LookPath realgit, fail
	// (nothing put it on PATH here), and the invocation would be refused.
	// Passing here proves known subcommands skip alias resolution entirely.
	t.Chdir(dir)

	vs := git.Check([]string{"status"})
	if len(vs) != 0 {
		t.Errorf("expected \"status\" to pass without any alias lookup, got %v", vs)
	}
}

// TestRealBinary_HasNoGitDashPrefix guards against reintroducing the trap a
// name like "git-real" is: git's own multi-call dispatch treats an argv[0]
// of the shape "git-<word>" as an attempt to run "<word>" as a builtin
// directly, silently ignoring the rest of argv (see the comment on
// git.RealBinary). A regression here would not fail loudly at compile time
// or in most manual testing — it only breaks once the real binary is
// invoked through this exact name — so it is checked directly.
func TestRealBinary_HasNoGitDashPrefix(t *testing.T) {
	if strings.HasPrefix(git.RealBinary, "git-") {
		t.Fatalf(
			"git.RealBinary = %q must not start with \"git-\": git's own argv[0] "+
				"dispatch treats \"git-<word>\" as an attempt to run <word> as a "+
				"builtin directly, which breaks this wrapper's exec target",
			git.RealBinary)
	}
}
