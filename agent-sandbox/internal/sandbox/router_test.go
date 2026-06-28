package sandbox_test

import (
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandbox"
)

func TestRoute_MatchingPattern_ReturnsHost(t *testing.T) {
	if got, _ := sandbox.Route("git status", []string{"git *"}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_NoMatchingPattern_ReturnsContainer(t *testing.T) {
	if got, _ := sandbox.Route("npm test", []string{"git *"}, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
}

func TestRoute_EmptyPatterns_ReturnsContainer(t *testing.T) {
	if got, _ := sandbox.Route("git status", nil, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
	if got, _ := sandbox.Route("git status", []string{}, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
}

func TestRoute_MultiplePatterns_MatchesSecond(t *testing.T) {
	patterns := []string{"git *", "make *", "npm run *"}
	if got, _ := sandbox.Route("make build", patterns, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
	if got, _ := sandbox.Route("npm run test", patterns, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_RawStringMatching_NoShellExpansion(t *testing.T) {
	if got, _ := sandbox.Route("echo $HOME", []string{"git *"}, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
	if got, _ := sandbox.Route("echo $HOME", []string{"echo $HOME"}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_WildcardMatchesSlash(t *testing.T) {
	if got, _ := sandbox.Route("git add src/main.go", []string{"git *"}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
	if got, _ := sandbox.Route("go test ./...", []string{"go test *"}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_RegexMetacharactersMatchLiterally(t *testing.T) {
	if got, _ := sandbox.Route("echo $HOME", []string{"echo $HOME"}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
	if got, _ := sandbox.Route("echo /tmp/file.txt", []string{"echo /tmp/file.txt"}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
	if got, _ := sandbox.Route("echo /tmp/fileXtxt", []string{"echo /tmp/file.txt"}, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
}

func TestRoute_ExactMatch_ReturnsHost(t *testing.T) {
	if got, _ := sandbox.Route("git status", []string{"git status"}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_NoPatterns_NilAndEmpty_BothContainer(t *testing.T) {
	cases := [][]string{nil, {}}
	for _, patterns := range cases {
		if got, _ := sandbox.Route("anything", patterns, nil); got != "container" {
			t.Errorf("patterns=%v: got %q, want container", patterns, got)
		}
	}
}

func TestRoute_MultilineCommitMessage_MatchesHost(t *testing.T) {
	cmd := "git commit -m \"fix bug\n\nMore details here\""
	if got, _ := sandbox.Route(cmd, []string{"git *"}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_DropMatch_ReturnsDrop(t *testing.T) {
	got, matched := sandbox.Route("rm -rf /tmp/x", nil, []string{"rm -rf *"})
	if got != "drop" {
		t.Errorf("decision = %q, want drop", got)
	}
	if matched != "rm -rf *" {
		t.Errorf("matched = %q, want \"rm -rf *\"", matched)
	}
}

func TestRoute_DropOverridesAllow(t *testing.T) {
	allow := []string{"git *"}
	drop := []string{"git push *"}
	got, matched := sandbox.Route("git push origin main", allow, drop)
	if got != "drop" {
		t.Errorf("decision = %q, want drop", got)
	}
	if matched != "git push *" {
		t.Errorf("matched = %q, want \"git push *\"", matched)
	}
}

func TestRoute_EmptyDropPatterns_BehaviorUnchanged(t *testing.T) {
	cases := [][]string{nil, {}}
	for _, drop := range cases {
		if got, _ := sandbox.Route("git status", []string{"git *"}, drop); got != "host" {
			t.Errorf("drop=%v: got %q, want host", drop, got)
		}
		if got, _ := sandbox.Route("npm test", []string{"git *"}, drop); got != "container" {
			t.Errorf("drop=%v: got %q, want container", drop, got)
		}
	}
}

func TestRoute_DefaultContainer_MatchedEmpty(t *testing.T) {
	_, matched := sandbox.Route("nothing matches", []string{"git *"}, []string{"rm *"})
	if matched != "" {
		t.Errorf("matched = %q, want empty for default-container", matched)
	}
}

func TestRoute_AllowMatch_ReturnsAllowPattern(t *testing.T) {
	_, matched := sandbox.Route("git status", []string{"git *"}, nil)
	if matched != "git *" {
		t.Errorf("matched = %q, want \"git *\"", matched)
	}
}
