package executor

import (
	"context"
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestBuildNonoCommand_NoDeadline(t *testing.T) {
	got := buildNonoCommand(context.Background(), "git status", "nono.json")
	want := []string{"nono", "-s", "run", "--profile", "nono.json", "--allow-cwd", "--", "git", "status"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestBuildNonoCommand_WithDeadline_PrependTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got := buildNonoCommand(ctx, "git status", "nono.json")
	base := []string{"nono", "-s", "run", "--profile", "nono.json", "--allow-cwd", "--", "git", "status"}
	if len(got) != len(base)+2 {
		t.Fatalf("got %d elements %v, want %d", len(got), got, len(base)+2)
	}
	if got[0] != "timeout" {
		t.Errorf("got[0] = %q, want \"timeout\"", got[0])
	}
	secs, err := strconv.Atoi(got[1])
	if err != nil {
		t.Fatalf("got[1] = %q is not a number: %v", got[1], err)
	}
	if secs < 1 || secs > 29 {
		t.Errorf("secs = %d, want in [1, 29]", secs)
	}
	if !slices.Equal(got[2:], base) {
		t.Errorf("suffix = %v, want %v", got[2:], base)
	}
}

func TestBuildNonoCommand_DeadlineExpired_ReturnsOneSecond(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	got := buildNonoCommand(ctx, "echo hello", "nono-yolo.json")
	if len(got) < 2 || got[0] != "timeout" || got[1] != "1" {
		t.Errorf("got %v, want [timeout 1 nono ...]", got)
	}
}

func TestBuildNonoCommand_MultipleArgs(t *testing.T) {
	got := buildNonoCommand(context.Background(), "go test ./...", "nono.json")
	want := []string{"nono", "-s", "run", "--profile", "nono.json", "--allow-cwd", "--", "go", "test", "./..."}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestBuildNonoCommand_YoloProfile(t *testing.T) {
	got := buildNonoCommand(context.Background(), "git status", "nono-yolo.json")
	want := []string{"nono", "-s", "run", "--profile", "nono-yolo.json", "--allow-cwd", "--", "git", "status"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCleanResult_ZeroValue(t *testing.T) {
	var r CleanResult
	if r.Containers != 0 || r.Networks != 0 {
		t.Fatal("zero value should have Containers=0 and Networks=0")
	}
}
