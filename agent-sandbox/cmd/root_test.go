package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPersistentEnv_LoadsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte("APPLY_PERSIST_KEY=hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	old := envRefs
	defer func() { envRefs = old }()
	envRefs = []string{"file:" + p}

	if err := applyPersistentEnv(nil, nil); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if os.Getenv("APPLY_PERSIST_KEY") != "hello" {
		t.Errorf("env not applied by applyPersistentEnv")
	}
}

func TestApplyPersistentEnv_EmptyNoop(t *testing.T) {
	old := envRefs
	defer func() { envRefs = old }()
	envRefs = nil
	if err := applyPersistentEnv(nil, nil); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}
