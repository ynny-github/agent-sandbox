package scaffold

import (
	"testing"
	"testing/fstest"

	agentsandbox "github.com/ynny-github/agent-sandbox"
)

// Every asset init writes must be inside the binary: init no longer fetches,
// so a file missing from the embedded set would be missing for every user.
func TestLoadReadsEveryAssetFromTheEmbeddedFiles(t *testing.T) {
	files, err := Load(agentsandbox.Assets)
	if err != nil {
		t.Fatalf("Load(embedded) error = %v", err)
	}
	if len(files) != len(Assets) {
		t.Fatalf("Load returned %d files, want %d", len(files), len(Assets))
	}
	for _, f := range files {
		if len(f.Body) == 0 {
			t.Errorf("%s: empty body", f.Asset.Source)
		}
	}
}

func TestLoadFailsWhenAnAssetIsMissing(t *testing.T) {
	if _, err := Load(fstest.MapFS{}); err == nil {
		t.Error("Load(empty FS) error = nil, want an error naming the missing asset")
	}
}
