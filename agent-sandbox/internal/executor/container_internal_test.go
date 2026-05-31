package executor

import (
	"testing"
)

func TestCleanResult_ZeroValue(t *testing.T) {
	var r CleanResult
	if r.Containers != 0 || r.Networks != 0 {
		t.Fatal("zero value should have Containers=0 and Networks=0")
	}
}
