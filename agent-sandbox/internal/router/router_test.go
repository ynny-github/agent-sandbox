package router_test

import (
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
)

func TestRoute_MatchingPattern_ReturnsHost(t *testing.T) {
	if got := router.Route("git status", []string{"git *"}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_NoMatchingPattern_ReturnsContainer(t *testing.T) {
	if got := router.Route("npm test", []string{"git *"}, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
}

func TestRoute_EmptyPatterns_ReturnsContainer(t *testing.T) {
	if got := router.Route("git status", nil, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
	if got := router.Route("git status", []string{}, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
}

func TestRoute_MultiplePatterns_MatchesSecond(t *testing.T) {
	patterns := []string{"git *", "make *", "npm run *"}
	if got := router.Route("make build", patterns, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
	if got := router.Route("npm run test", patterns, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_RawStringMatching_NoShellExpansion(t *testing.T) {
	if got := router.Route("echo $HOME", []string{"git *"}, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
	if got := router.Route("echo $HOME", []string{"echo $HOME"}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_WildcardMatchesSlash(t *testing.T) {
	if got := router.Route("git add src/main.go", []string{"git *"}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
	if got := router.Route("go test ./...", []string{"go test *"}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_RegexMetacharactersMatchLiterally(t *testing.T) {
	if got := router.Route("echo $HOME", []string{"echo $HOME"}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
	if got := router.Route("echo /tmp/file.txt", []string{"echo /tmp/file.txt"}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
	if got := router.Route("echo /tmp/fileXtxt", []string{"echo /tmp/file.txt"}, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
}

func TestRoute_ExactMatch_ReturnsHost(t *testing.T) {
	if got := router.Route("git status", []string{"git status"}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_NoPatterns_NilAndEmpty_BothContainer(t *testing.T) {
	cases := [][]string{nil, {}}
	for _, patterns := range cases {
		if got := router.Route("anything", patterns, nil); got != "container" {
			t.Errorf("patterns=%v: got %q, want container", patterns, got)
		}
	}
}

func TestRoute_DenyOverridesAllow(t *testing.T) {
	allow := []string{"go *"}
	deny := []string{"go run *"}
	if got := router.Route("go run main.go", allow, deny); got != "container" {
		t.Errorf("got %q, want container", got)
	}
	if got := router.Route("go build ./...", allow, deny); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}

func TestRoute_NoDenyPatterns_BehaviorUnchanged(t *testing.T) {
	if got := router.Route("git status", []string{"git *"}, nil); got != "host" {
		t.Errorf("got %q, want host", got)
	}
	if got := router.Route("npm test", []string{"git *"}, nil); got != "container" {
		t.Errorf("got %q, want container", got)
	}
}

func TestRoute_EmptyDenyPatterns_BehaviorUnchanged(t *testing.T) {
	if got := router.Route("git status", []string{"git *"}, []string{}); got != "host" {
		t.Errorf("got %q, want host", got)
	}
}
