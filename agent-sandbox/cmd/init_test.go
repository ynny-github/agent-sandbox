package cmd

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/scaffold"
)

// serveTemplates answers every asset with a body naming its source.
func serveTemplates(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for _, a := range scaffold.Assets {
		source := a.Source
		mux.HandleFunc("/"+source, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("ETag", `"etag-`+source+`"`)
			_, _ = w.Write([]byte("body of " + source))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// initHarness points init at a local server, stubs the nono call out, and
// moves the config path into a fresh directory.
func initHarness(t *testing.T) (dir string, out *bytes.Buffer) {
	t.Helper()
	srv := serveTemplates(t)

	origBase := initBaseURL
	initBaseURL = srv.URL + "/"
	t.Cleanup(func() { initBaseURL = origBase })

	origValidate := initValidate
	initValidate = func(fetched []scaffold.Fetched) ([]string, error) { return nil, nil }
	t.Cleanup(func() { initValidate = origValidate })

	dir = t.TempDir()
	origCfg := configPath
	configPath = filepath.Join(dir, "agent-sandbox.toml")
	t.Cleanup(func() { configPath = origCfg })

	out = &bytes.Buffer{}
	initCmd.SetOut(out)
	return dir, out
}

func TestRunInitWritesEveryAssetBesideTheConfigPath(t *testing.T) {
	dir, out := initHarness(t)
	if err := runInit(initCmd, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	for _, a := range scaffold.Assets {
		if _, err := os.Stat(filepath.Join(dir, a.Dest)); err != nil {
			t.Errorf("%s not written: %v", a.Dest, err)
		}
	}
	// Profiles are a security boundary; init reports where each byte came
	// from rather than leaving it implicit.
	for _, want := range []string{"etag-", "bytes", "command-profile.json"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report missing %q\n%s", want, out)
		}
	}
}

func TestRunInitSkipsExistingFilesAndStillSucceeds(t *testing.T) {
	dir, out := initHarness(t)
	if err := os.WriteFile(filepath.Join(dir, "claude-profile.json"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runInit(initCmd, nil); err != nil {
		t.Fatalf("runInit returned an error for an already-initialized file: %v", err)
	}
	if !strings.Contains(out.String(), "skip") {
		t.Errorf("report does not mention the skip\n%s", out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "claude-profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "mine" {
		t.Errorf("existing file was overwritten: %q", got)
	}
}

func TestRunInitWritesNothingWhenValidationFails(t *testing.T) {
	dir, _ := initHarness(t)
	initValidate = func(fetched []scaffold.Fetched) ([]string, error) {
		return nil, errNotValid
	}
	if err := runInit(initCmd, nil); err == nil {
		t.Fatal("runInit succeeded despite a validation failure")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("init left %d entries behind after a validation failure", len(entries))
	}
}

func TestRunInitPrintsWarningsWithoutFailing(t *testing.T) {
	_, out := initHarness(t)
	initValidate = func(fetched []scaffold.Fetched) ([]string, error) {
		return []string{"command-profile.json: [warn] [allow_all_network] command 'ssh' from.git allows unrestricted child network"}, nil
	}
	if err := runInit(initCmd, nil); err != nil {
		t.Fatalf("a warning failed the run: %v", err)
	}
	if !strings.Contains(out.String(), "allow_all_network") {
		t.Errorf("warning not reported\n%s", out)
	}
}

// errNotValid stands in for whatever nono reports; runInit must not write
// regardless of the wording.
var errNotValid = errors.New("command-profile.json: nono profile validate: Result: invalid (1 error)")
