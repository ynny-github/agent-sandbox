package mcptool

import (
	"os"
	"testing"
)

func TestResolveEnv_ExistingKey_ReturnsKeyValue(t *testing.T) {
	t.Setenv("CR_TEST_KEY_PRESENT", "myvalue")
	got := resolveEnv([]string{"CR_TEST_KEY_PRESENT"})
	if len(got) != 1 || got[0] != "CR_TEST_KEY_PRESENT=myvalue" {
		t.Errorf("got %v, want [CR_TEST_KEY_PRESENT=myvalue]", got)
	}
}

func TestResolveEnv_MissingKey_Skipped(t *testing.T) {
	os.Unsetenv("CR_TEST_KEY_ABSENT_XYZ")
	got := resolveEnv([]string{"CR_TEST_KEY_ABSENT_XYZ"})
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestResolveEnv_NilKeys_ReturnsNil(t *testing.T) {
	got := resolveEnv(nil)
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestResolveEnv_MixedKeys_SkipsMissing(t *testing.T) {
	t.Setenv("CR_TEST_KEY_PRESENT2", "hello")
	os.Unsetenv("CR_TEST_KEY_ABSENT2_XYZ")
	got := resolveEnv([]string{"CR_TEST_KEY_PRESENT2", "CR_TEST_KEY_ABSENT2_XYZ"})
	if len(got) != 1 || got[0] != "CR_TEST_KEY_PRESENT2=hello" {
		t.Errorf("got %v, want [CR_TEST_KEY_PRESENT2=hello]", got)
	}
}
