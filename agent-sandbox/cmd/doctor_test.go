// agent-sandbox/cmd/doctor_test.go
package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func stubNonoSeams(t *testing.T, lp func(string) (string, error), rc func(context.Context, string, ...string) ([]byte, error)) {
	t.Helper()
	origLookPath := lookPath
	origRun := runCommand
	lookPath = lp
	runCommand = rc
	t.Cleanup(func() {
		lookPath = origLookPath
		runCommand = origRun
	})
}

func TestCheckNono_NotInPath(t *testing.T) {
	stubNonoSeams(t,
		func(string) (string, error) { return "", errors.New("not found") },
		func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	)

	r := checkNono(context.Background())
	if r.ok {
		t.Fatal("expected NG, got OK")
	}
	if r.hint == "" {
		t.Error("expected hint to be set on NG")
	}
}

func TestCheckNono_VersionFails(t *testing.T) {
	stubNonoSeams(t,
		func(string) (string, error) { return "/usr/bin/nono", nil },
		func(context.Context, string, ...string) ([]byte, error) {
			return []byte("boom"), errors.New("exit status 1")
		},
	)

	r := checkNono(context.Background())
	if r.ok {
		t.Fatal("expected NG when --version fails")
	}
}

func TestCheckNono_OK(t *testing.T) {
	stubNonoSeams(t,
		func(string) (string, error) { return "/usr/bin/nono", nil },
		func(context.Context, string, ...string) ([]byte, error) {
			return []byte("nono 0.4.2\n"), nil
		},
	)

	r := checkNono(context.Background())
	if !r.ok {
		t.Fatal("expected OK")
	}
	joined := strings.Join(r.details, "\n")
	if !strings.Contains(joined, "path: /usr/bin/nono") {
		t.Errorf("details missing path: %v", r.details)
	}
	if !strings.Contains(joined, "version: nono 0.4.2") {
		t.Errorf("details missing version: %v", r.details)
	}
}

func TestRenderResults_AllOK(t *testing.T) {
	var buf bytes.Buffer
	renderResults(&buf, []checkResult{
		{name: "alpha", ok: true, details: []string{"path: /usr/bin/alpha", "version: 1.0"}},
		{name: "beta", ok: true, details: []string{"info: hello"}},
	})
	want := "[OK] alpha\n" +
		"     path: /usr/bin/alpha\n" +
		"     version: 1.0\n" +
		"\n" +
		"[OK] beta\n" +
		"     info: hello\n" +
		"\n" +
		"doctor: all checks passed\n"
	if got := buf.String(); got != want {
		t.Errorf("output mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestRenderResults_Mixed(t *testing.T) {
	var buf bytes.Buffer
	renderResults(&buf, []checkResult{
		{name: "alpha", ok: true, details: []string{"path: /usr/bin/alpha"}},
		{name: "beta", ok: false, details: []string{"error: something went wrong"}, hint: "try X"},
		{name: "gamma", ok: false, details: []string{"error: daemon down"}, hint: "start daemon"},
	})
	want := "[OK] alpha\n" +
		"     path: /usr/bin/alpha\n" +
		"\n" +
		"[NG] beta\n" +
		"     error: something went wrong\n" +
		"     hint: try X\n" +
		"\n" +
		"[NG] gamma\n" +
		"     error: daemon down\n" +
		"     hint: start daemon\n" +
		"\n" +
		"doctor: 2 of 3 checks failed\n"
	if got := buf.String(); got != want {
		t.Errorf("output mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestRenderResults_AllNG(t *testing.T) {
	var buf bytes.Buffer
	renderResults(&buf, []checkResult{
		{name: "a", ok: false, details: []string{"error: x"}, hint: "fix x"},
		{name: "b", ok: false, details: []string{"error: y"}, hint: "fix y"},
	})
	want := "[NG] a\n" +
		"     error: x\n" +
		"     hint: fix x\n" +
		"\n" +
		"[NG] b\n" +
		"     error: y\n" +
		"     hint: fix y\n" +
		"\n" +
		"doctor: 2 of 2 checks failed\n"
	if got := buf.String(); got != want {
		t.Errorf("output mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}
