package container

import "testing"

func TestEgressConflict(t *testing.T) {
	cases := []struct {
		name            string
		allowExternal   bool
		networkInternal bool
		want            bool
	}{
		{"blocked + external-reachable network = conflict", false, false, true},
		{"blocked + internal network = ok", false, true, false},
		{"allowed + external-reachable network = ok", true, false, false},
		{"allowed + internal network = ok", true, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := egressConflict(c.allowExternal, c.networkInternal); got != c.want {
				t.Errorf("egressConflict(%v,%v) = %v, want %v", c.allowExternal, c.networkInternal, got, c.want)
			}
		})
	}
}
