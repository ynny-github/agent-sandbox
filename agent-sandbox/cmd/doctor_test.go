// agent-sandbox/cmd/doctor_test.go
package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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

func stubAllSeamsOK(t *testing.T) {
	t.Helper()
	stubNonoSeams(t,
		func(string) (string, error) { return "/usr/bin/nono", nil },
		func(context.Context, string, ...string) ([]byte, error) { return []byte("nono 0.4.2\n"), nil },
	)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func TestDoctorCmd_Registered(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "doctor" {
			return
		}
	}
	t.Fatal("doctor command not registered on rootCmd")
}

func TestRunDoctor_AllOK(t *testing.T) {
	stubAllSeamsOK(t)

	var buf bytes.Buffer
	doctorCmd.SetOut(&buf)
	t.Cleanup(func() { doctorCmd.SetOut(nil) })

	if err := runDoctor(doctorCmd, nil); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !strings.Contains(buf.String(), "doctor: all checks passed") {
		t.Errorf("missing summary in output:\n%s", buf.String())
	}
}

func TestRunDoctor_NonoNG(t *testing.T) {
	stubNonoSeams(t,
		func(string) (string, error) { return "", errors.New("not found") },
		func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var buf bytes.Buffer
	doctorCmd.SetOut(&buf)
	t.Cleanup(func() { doctorCmd.SetOut(nil) })

	err := runDoctor(doctorCmd, nil)
	if !errors.Is(err, errDoctorChecksFailed) {
		t.Fatalf("expected errDoctorChecksFailed, got %v", err)
	}
	if !strings.Contains(buf.String(), "checks failed") {
		t.Errorf("missing failure summary:\n%s", buf.String())
	}
}

func TestRunDoctor_RunsAllChecksEvenOnEarlyFailure(t *testing.T) {
	stubNonoSeams(t,
		func(string) (string, error) { return "", errors.New("not found") },
		func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var buf bytes.Buffer
	doctorCmd.SetOut(&buf)
	t.Cleanup(func() { doctorCmd.SetOut(nil) })

	_ = runDoctor(doctorCmd, nil)
	for _, want := range []string{"nono", "command broker"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing section %q:\n%s", want, buf.String())
		}
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

func TestCheckBrokerSocketDir_OK(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)

	r := checkBrokerSocketDir()
	if !r.ok {
		t.Fatalf("expected OK, got NG: %v", r.details)
	}
	want := filepath.Join(base, "agent-sandbox")
	joined := strings.Join(r.details, "\n")
	if !strings.Contains(joined, want) {
		t.Errorf("details missing socket dir %q: %v", want, r.details)
	}
}

func TestCheckBrokerSocketDir_UnwritableFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	base := t.TempDir()
	// Make base read-only so MkdirAll underneath it fails.
	if err := os.Chmod(base, 0o500); err != nil {
		t.Skipf("cannot make dir read-only in this environment: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(base, 0o700) })
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))

	r := checkBrokerSocketDir()
	if r.ok {
		t.Fatal("expected NG when the socket dir cannot be created")
	}
	if r.hint == "" {
		t.Error("expected hint on NG")
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
