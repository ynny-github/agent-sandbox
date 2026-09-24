package scaffold

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

// repoRoot walks up from the working directory until it finds go.mod. The
// templates live at the repository root, not beside this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

// TestTemplatesValidate is what lets init fetch from main: a template that
// nono rejects must fail here before it can be merged.
func TestTemplatesValidate(t *testing.T) {
	if _, err := exec.LookPath("nono"); err != nil {
		t.Skip("nono not on PATH")
	}
	root := repoRoot(t)
	for _, name := range []string{"command-profile.json", "claude-profile.json"} {
		path := filepath.Join(root, "templates", "minimal", name)
		out, err := exec.Command("nono", "profile", "validate", path).CombinedOutput()
		if err != nil {
			t.Errorf("nono profile validate %s: %v\n%s", name, err, out)
			continue
		}
		if !strings.Contains(string(out), "Result: valid") {
			t.Errorf("%s: nono did not report the profile valid\n%s", name, out)
		}
	}
}

// TestTemplateConfigNamesBothProfiles keeps the TOML template in step with the
// two JSON files beside it. A substring check alone has no teeth -- a file
// starting `[[[garbage` that still contains both lines would pass it while
// the real TOML parser rejects it -- so this also loads the template through
// config.Load, the same entry point agent-sandbox itself uses, and confirms
// it resolves both profile paths.
func TestTemplateConfigNamesBothProfiles(t *testing.T) {
	// Isolate HOME so a real ~/.config/agent-sandbox/config.toml on the
	// developer's machine cannot influence what config.Load resolves.
	t.Setenv("HOME", t.TempDir())

	root := repoRoot(t)
	tomlPath := filepath.Join(root, "templates", "minimal", "agent-sandbox.toml")
	body, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`command_profile = "command-profile.json"`, `profile = "claude-profile.json"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("template config missing %q\n%s", want, body)
		}
	}

	cfg, err := config.Load(tomlPath)
	if err != nil {
		t.Fatalf("config.Load(%s): %v", tomlPath, err)
	}
	wantCommand := filepath.Join(root, "templates", "minimal", "command-profile.json")
	if got := cfg.CommandProfilePath(); got != wantCommand {
		t.Errorf("CommandProfilePath() = %q, want %q", got, wantCommand)
	}
	wantAgent := filepath.Join(root, "templates", "minimal", "claude-profile.json")
	if got := cfg.AgentProfilePath("claude"); got != wantAgent {
		t.Errorf("AgentProfilePath(\"claude\") = %q, want %q", got, wantAgent)
	}
}

// TestAssetSourcesExistInTheRepository guards against path drift, the
// likeliest production failure of a fetch-from-main design: rename a skill
// directory and the rest of the suite stays green while init 404s for every
// user, because nothing else here ever looks at the working tree's copy of
// Asset.Source.
func TestAssetSourcesExistInTheRepository(t *testing.T) {
	root := repoRoot(t)
	for _, a := range Assets {
		path := filepath.Join(root, filepath.FromSlash(a.Source))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s: Asset.Source %q not found in the repository: %v", a.Dest, a.Source, err)
		}
	}
}

// execdSocketVar is the one grant that turns a command into a host-side fork
// bomb: a command that can reach it can recurse into execd, which spawns
// handlers with no concurrency cap.
const execdSocketVar = "AGENT_SANDBOX_EXECD_SOCKET"

// allowVarsArrayRE finds every "allow_vars": [ ... ] array in the command
// template's JSONC text. The arrays here hold only quoted strings with no
// nested brackets, so this is sufficient without a JSONC parser.
var allowVarsArrayRE = regexp.MustCompile(`"allow_vars"\s*:\s*\[([^\]]*)\]`)

// quotedStringRE pulls the individual string literals out of one matched
// array body.
var quotedStringRE = regexp.MustCompile(`"([^"]*)"`)

// commandTemplateAllowVarEntries returns every string literal appearing in
// every allow_vars array in text, wherever in the document it is nested.
func commandTemplateAllowVarEntries(text string) []string {
	var entries []string
	for _, arr := range allowVarsArrayRE.FindAllStringSubmatch(text, -1) {
		for _, m := range quotedStringRE.FindAllStringSubmatch(arr[1], -1) {
			entries = append(entries, m[1])
		}
	}
	return entries
}

// matchesExecdSocket reports whether an allow_vars entry -- a literal
// variable name or a glob pattern -- would let execdSocketVar through.
// Measured against nono 0.74.0: an allow_vars entry of "AGENT_SANDBOX_*"
// both validates cleanly and forwards the socket into the sandbox, so a
// prefix wildcard must be caught here, not just the literal name.
func matchesExecdSocket(entry string) bool {
	ok, err := path.Match(entry, execdSocketVar)
	return err == nil && ok
}

// TestExecdSocketWildcardDetection proves matchesExecdSocket actually
// discriminates before it is trusted to gate the real template: it must
// catch the literal name and the prefix-wildcard form nono measurably
// forwards the socket through, and it must not flag unrelated entries.
func TestExecdSocketWildcardDetection(t *testing.T) {
	cases := []struct {
		entry string
		want  bool
	}{
		{"AGENT_SANDBOX_EXECD_SOCKET", true},
		{"AGENT_SANDBOX_*", true},
		{"AGENT_SANDBOX_EXECD_*", true},
		{"*", true},
		{"PATH", false},
		{"HOME", false},
		{"AGENT_SANDBOX_OTHER_THING", false},
	}
	for _, c := range cases {
		if got := matchesExecdSocket(c.entry); got != c.want {
			t.Errorf("matchesExecdSocket(%q) = %v, want %v", c.entry, got, c.want)
		}
	}
}

// TestCommandTemplateNeverForwardsTheExecdSocket is the one grant that turns
// a command into a host-side fork bomb. The environment block is also what
// makes the environment an allowlist at all, so its absence is equally a
// failure. Beyond the literal name, this also rejects any allow_vars entry
// -- wildcard or not, wherever nested -- that would match the socket
// variable, because a wildcard such as "AGENT_SANDBOX_*" both validates
// cleanly under nono and forwards the socket exactly as the literal name
// would.
func TestCommandTemplateNeverForwardsTheExecdSocket(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "templates", "minimal", "command-profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `"allow_vars"`) {
		t.Fatal("command template declares no allow_vars; nono would forward the whole environment")
	}
	// The comment block names the variable on purpose; only a JSON string
	// entry would forward it.
	if strings.Contains(text, `"`+execdSocketVar+`"`) {
		t.Error("command template forwards AGENT_SANDBOX_EXECD_SOCKET")
	}
	for _, entry := range commandTemplateAllowVarEntries(text) {
		if matchesExecdSocket(entry) {
			t.Errorf("command template allow_vars entry %q would forward %s (wildcard or exact match)", entry, execdSocketVar)
		}
	}
}
