package router_test

import (
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
)

func TestRoute_MatchingPattern_ReturnsHost(t *testing.T) {
	if got, _ := router.Route("git status", []string{"git *"}, nil, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_NoMatchingPattern_ReturnsContainer(t *testing.T) {
	if got, _ := router.Route("npm test", []string{"git *"}, nil, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
}

func TestRoute_EmptyPatterns_ReturnsContainer(t *testing.T) {
	if got, _ := router.Route("git status", nil, nil, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
	if got, _ := router.Route("git status", []string{}, nil, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
}

func TestRoute_MultiplePatterns_MatchesSecond(t *testing.T) {
	patterns := []string{"git *", "make *", "npm run *"}
	if got, _ := router.Route("make build", patterns, nil, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
	if got, _ := router.Route("npm run test", patterns, nil, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_RawStringMatching_NoShellExpansion(t *testing.T) {
	if got, _ := router.Route("echo $HOME", []string{"git *"}, nil, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
	if got, _ := router.Route("echo $HOME", []string{"echo $HOME"}, nil, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_WildcardMatchesSlash(t *testing.T) {
	if got, _ := router.Route("git add src/main.go", []string{"git *"}, nil, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
	if got, _ := router.Route("go test ./...", []string{"go test *"}, nil, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_RegexMetacharactersMatchLiterally(t *testing.T) {
	if got, _ := router.Route("echo $HOME", []string{"echo $HOME"}, nil, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
	if got, _ := router.Route("echo /tmp/file.txt", []string{"echo /tmp/file.txt"}, nil, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
	if got, _ := router.Route("echo /tmp/fileXtxt", []string{"echo /tmp/file.txt"}, nil, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
}

func TestRoute_ExactMatch_ReturnsHost(t *testing.T) {
	if got, _ := router.Route("git status", []string{"git status"}, nil, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_NoPatterns_NilAndEmpty_BothContainer(t *testing.T) {
	cases := [][]string{nil, {}}
	for _, patterns := range cases {
		if got, _ := router.Route("anything", patterns, nil, nil); got != "container" {
			t.Errorf("patterns=%v: got %q, want container", patterns, got)
		}
	}
}

func TestRoute_DenyOverridesAllow(t *testing.T) {
	allow := []string{"go *"}
	deny := []string{"go run *"}
	if got, _ := router.Route("go run main.go", allow, deny, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
	if got, _ := router.Route("go build ./...", allow, deny, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_NoDenyPatterns_BehaviorUnchanged(t *testing.T) {
	if got, _ := router.Route("git status", []string{"git *"}, nil, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
	if got, _ := router.Route("npm test", []string{"git *"}, nil, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
}

func TestRoute_EmptyDenyPatterns_BehaviorUnchanged(t *testing.T) {
	if got, _ := router.Route("git status", []string{"git *"}, []string{}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_MultilineCommitMessage_MatchesHost(t *testing.T) {
	// git commit -m with embedded newlines must still route to host
	cmd := "git commit -m \"fix bug\n\nMore details here\""
	if got, _ := router.Route(cmd, []string{"git *"}, nil, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_DropMatch_ReturnsDrop(t *testing.T) {
	got, matched := router.Route("rm -rf /tmp/x", nil, nil, []string{"rm -rf *"})
	if got != "drop" {
		t.Errorf("decision = %q, want drop", got)
	}
	if matched != "rm -rf *" {
		t.Errorf("matched = %q, want \"rm -rf *\"", matched)
	}
}

func TestRoute_DropOverridesDeny(t *testing.T) {
	deny := []string{"go run *"}
	drop := []string{"go run *"}
	got, matched := router.Route("go run main.go", nil, deny, drop)
	if got != "drop" {
		t.Errorf("decision = %q, want drop", got)
	}
	if matched != "go run *" {
		t.Errorf("matched = %q, want \"go run *\"", matched)
	}
}

func TestRoute_DropOverridesAllow(t *testing.T) {
	allow := []string{"git *"}
	drop := []string{"git push *"}
	got, matched := router.Route("git push origin main", allow, nil, drop)
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
		if got, _ := router.Route("git status", []string{"git *"}, nil, drop); got != "host" {
			t.Errorf("drop=%v: got %q, want host", drop, got)
		}
		if got, _ := router.Route("npm test", []string{"git *"}, nil, drop); got != "container" {
			t.Errorf("drop=%v: got %q, want container", drop, got)
		}
	}
}

func TestRoute_DefaultContainer_MatchedEmpty(t *testing.T) {
	_, matched := router.Route("nothing matches", []string{"git *"}, []string{"go *"}, []string{"rm *"})
	if matched != "" {
		t.Errorf("matched = %q, want empty for default-container", matched)
	}
}

func TestRoute_AllowMatch_ReturnsAllowPattern(t *testing.T) {
	_, matched := router.Route("git status", []string{"git *"}, nil, nil)
	if matched != "git *" {
		t.Errorf("matched = %q, want \"git *\"", matched)
	}
}

func TestRoute_DenyMatch_ReturnsDenyPattern(t *testing.T) {
	_, matched := router.Route("go run main.go", []string{"go *"}, []string{"go run *"}, nil)
	if matched != "go run *" {
		t.Errorf("matched = %q, want \"go run *\"", matched)
	}
}
