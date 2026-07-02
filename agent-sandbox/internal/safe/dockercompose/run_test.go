package dockercompose_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe/dockercompose"
)

// fakeResolver returns a fixed model or error and records whether it was called.
type fakeResolver struct {
	model  dockercompose.Model
	err    error
	called bool
}

func (f *fakeResolver) Resolve(ctx context.Context, globalFlags []string) (dockercompose.Model, error) {
	f.called = true
	return f.model, f.err
}

func TestPrepare_RunSubcommand_RefusedBeforeResolve(t *testing.T) {
	r := &fakeResolver{}
	v, err := dockercompose.Prepare(context.Background(), []string{"run", "web", "sh"}, "/work", r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(v) == 0 {
		t.Fatal("expected a violation for run, got none")
	}
	if r.called {
		t.Error("resolver should not be called when CLI check fails")
	}
}

func TestPrepare_SafeModel_NoViolations(t *testing.T) {
	r := &fakeResolver{model: modelFromJSON(t,
		`{"services":{"web":{"volumes":[{"type":"bind","source":"/work/src","target":"/src"}]}}}`)}
	v, err := dockercompose.Prepare(context.Background(), []string{"up", "-d"}, "/work", r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("expected no violations, got %v", v)
	}
	if !r.called {
		t.Error("resolver should be called for non-run subcommands")
	}
}

func TestPrepare_UnsafeModel_ReturnsViolations(t *testing.T) {
	r := &fakeResolver{model: modelFromJSON(t,
		`{"services":{"web":{"privileged":true}}}`)}
	v, err := dockercompose.Prepare(context.Background(), []string{"up"}, "/work", r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(v) == 0 {
		t.Fatal("expected violations for privileged service, got none")
	}
}

func TestPrepare_ResolverError_Wrapped(t *testing.T) {
	r := &fakeResolver{err: errors.New("boom")}
	_, err := dockercompose.Prepare(context.Background(), []string{"up"}, "/work", r)
	if err == nil {
		t.Fatal("expected error from resolver, got nil")
	}
	if !errors.Is(err, dockercompose.ErrResolve) {
		t.Errorf("error %v is not ErrResolve", err)
	}
}
