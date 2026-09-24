package scaffold

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveAssets answers every asset path with its own name as the body, so a
// test can tell the responses apart. missing is served as 404.
func serveAssets(t *testing.T, missing string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for _, a := range Assets {
		source := a.Source
		mux.HandleFunc("/"+source, func(w http.ResponseWriter, r *http.Request) {
			if source == missing {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("ETag", `"etag-for-`+source+`"`)
			_, _ = w.Write([]byte("body of " + source))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchReturnsEveryAsset(t *testing.T) {
	srv := serveAssets(t, "")
	got, err := Fetch(context.Background(), srv.Client(), srv.URL+"/")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != len(Assets) {
		t.Fatalf("Fetch returned %d assets, want %d", len(got), len(Assets))
	}
	for _, f := range got {
		if string(f.Body) != "body of "+f.Asset.Source {
			t.Errorf("%s: body is %q", f.Asset.Dest, f.Body)
		}
		if f.ETag != `"etag-for-`+f.Asset.Source+`"` {
			t.Errorf("%s: etag is %q", f.Asset.Dest, f.ETag)
		}
		if !strings.HasSuffix(f.URL, f.Asset.Source) {
			t.Errorf("%s: url is %q", f.Asset.Dest, f.URL)
		}
	}
}

func TestFetchReturnsNothingWhenOneAssetIsMissing(t *testing.T) {
	srv := serveAssets(t, "templates/minimal/claude-profile.json")
	got, err := Fetch(context.Background(), srv.Client(), srv.URL+"/")
	if err == nil {
		t.Fatal("Fetch succeeded despite a 404")
	}
	if got != nil {
		t.Errorf("Fetch returned %d assets alongside the error; it must return none", len(got))
	}
	if !strings.Contains(err.Error(), "claude-profile.json") {
		t.Errorf("error does not name the missing asset: %v", err)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error does not carry the status: %v", err)
	}
}

// TestFetchRejectsABodyLargerThanMaxAssetBytes guards the response size
// bound: a body past the ceiling must be an error, not silently truncated
// into a short "success" that would then get validated or written as if it
// were the real file.
func TestFetchRejectsABodyLargerThanMaxAssetBytes(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), maxAssetBytes+1)
	mux := http.NewServeMux()
	for _, a := range Assets {
		mux.HandleFunc("/"+a.Source, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("ETag", `"etag-oversized"`)
			_, _ = w.Write(oversized)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	got, err := Fetch(context.Background(), srv.Client(), srv.URL+"/")
	if err == nil {
		t.Fatal("Fetch succeeded against a body larger than maxAssetBytes")
	}
	if got != nil {
		t.Errorf("Fetch returned %d assets alongside the error; it must return none", len(got))
	}
	if !strings.Contains(err.Error(), "larger than") {
		t.Errorf("error does not explain the size rejection: %v", err)
	}
}

func TestFetchNamesTheURLWhenTheHostIsUnreachable(t *testing.T) {
	srv := serveAssets(t, "")
	base := srv.URL + "/"
	srv.Close() // nothing is listening any more
	_, err := Fetch(context.Background(), srv.Client(), base)
	if err == nil {
		t.Fatal("Fetch succeeded against a closed server")
	}
	if !strings.Contains(err.Error(), base) && !strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("error names neither the base URL nor the host: %v", err)
	}
}
