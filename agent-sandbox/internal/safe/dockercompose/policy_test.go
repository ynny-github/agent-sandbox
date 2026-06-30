package dockercompose_test

import (
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe/dockercompose"
)

func TestCheckCLI(t *testing.T) {
	cases := []struct {
		name     string
		sub      string
		wantViol bool
	}{
		{"run refused", "run", true},
		{"exec refused", "exec", true},
		{"up allowed", "up", false},
		{"build allowed", "build", false},
		{"down allowed", "down", false},
		{"empty allowed", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := dockercompose.CheckCLI(dockercompose.ParsedArgs{Subcommand: tc.sub})
			if got := len(v) > 0; got != tc.wantViol {
				t.Errorf("violation=%v, want %v (%v)", got, tc.wantViol, v)
			}
		})
	}
}

func TestCheckCLI_UnrecognizedFlagRefused(t *testing.T) {
	v := dockercompose.CheckCLI(dockercompose.ParsedArgs{Unrecognized: "--bogus", Subcommand: "up"})
	if len(v) == 0 {
		t.Fatal("expected a violation for an unrecognized global flag, got none")
	}
	if !strings.Contains(v[0].String(), "unrecognized global flag") {
		t.Errorf("violation %q does not mention unrecognized flag", v[0].String())
	}
}

// modelFromJSON is a helper that decodes a compose-config JSON string or fails.
func modelFromJSON(t *testing.T, js string) dockercompose.Model {
	t.Helper()
	m, err := dockercompose.DecodeModel([]byte(js))
	if err != nil {
		t.Fatalf("DecodeModel: %v", err)
	}
	return m
}

func TestCheckModel(t *testing.T) {
	const cwd = "/work"
	cases := []struct {
		name     string
		js       string
		wantViol bool
		wantText string // substring expected in the first violation, when wantViol
	}{
		{
			name:     "in-cwd bind allowed",
			js:       `{"services":{"web":{"volumes":[{"type":"bind","source":"/work/src","target":"/src"}]}}}`,
			wantViol: false,
		},
		{
			name:     "named volume allowed",
			js:       `{"services":{"web":{"volumes":[{"type":"volume","source":"data","target":"/data"}]}}}`,
			wantViol: false,
		},
		{
			name:     "tmpfs allowed",
			js:       `{"services":{"web":{"volumes":[{"type":"tmpfs","target":"/tmp"}]}}}`,
			wantViol: false,
		},
		{
			name:     "absolute bind outside cwd refused",
			js:       `{"services":{"web":{"volumes":[{"type":"bind","source":"/etc","target":"/etc"}]}}}`,
			wantViol: true,
			wantText: "escapes the work directory",
		},
		{
			name:     "parent bind refused",
			js:       `{"services":{"web":{"volumes":[{"type":"bind","source":"/work/../secret","target":"/s"}]}}}`,
			wantViol: true,
			wantText: "escapes the work directory",
		},
		{
			name:     "docker socket bind refused even in cwd",
			js:       `{"services":{"web":{"volumes":[{"type":"bind","source":"/work/docker.sock","target":"/var/run/docker.sock"}]}}}`,
			wantViol: true,
			wantText: "docker socket",
		},
		{
			name:     "privileged refused",
			js:       `{"services":{"web":{"privileged":true}}}`,
			wantViol: true,
			wantText: "privileged",
		},
		{
			name:     "network_mode host refused",
			js:       `{"services":{"web":{"network_mode":"host"}}}`,
			wantViol: true,
			wantText: "network_mode: host",
		},
		{
			name:     "pid host refused",
			js:       `{"services":{"web":{"pid":"host"}}}`,
			wantViol: true,
			wantText: "pid: host",
		},
		{
			name:     "ipc host refused",
			js:       `{"services":{"web":{"ipc":"host"}}}`,
			wantViol: true,
			wantText: "ipc: host",
		},
		{
			name:     "userns host refused",
			js:       `{"services":{"web":{"userns_mode":"host"}}}`,
			wantViol: true,
			wantText: "userns_mode: host",
		},
		{
			name:     "dangerous cap refused",
			js:       `{"services":{"web":{"cap_add":["SYS_ADMIN"]}}}`,
			wantViol: true,
			wantText: "capability",
		},
		{
			name:     "dangerous cap with CAP_ prefix refused",
			js:       `{"services":{"web":{"cap_add":["CAP_NET_ADMIN"]}}}`,
			wantViol: true,
			wantText: "capability",
		},
		{
			name:     "dangerous cap lowercase refused",
			js:       `{"services":{"web":{"cap_add":["cap_sys_admin"]}}}`,
			wantViol: true,
			wantText: "capability",
		},
		{
			name:     "cap ALL refused",
			js:       `{"services":{"web":{"cap_add":["ALL"]}}}`,
			wantViol: true,
			wantText: "capability",
		},
		{
			name:     "benign cap allowed",
			js:       `{"services":{"web":{"cap_add":["CHOWN"]}}}`,
			wantViol: false,
		},
		{
			name:     "label disable equals form refused",
			js:       `{"services":{"web":{"security_opt":["label=disable"]}}}`,
			wantViol: true,
			wantText: "security_opt",
		},
		{
			name:     "devices refused",
			js:       `{"services":{"web":{"devices":[{"source":"/dev/kvm","target":"/dev/kvm"}]}}}`,
			wantViol: true,
			wantText: "device",
		},
		{
			name:     "seccomp unconfined refused",
			js:       `{"services":{"web":{"security_opt":["seccomp:unconfined"]}}}`,
			wantViol: true,
			wantText: "security_opt",
		},
		{
			name:     "label disable refused",
			js:       `{"services":{"web":{"security_opt":["label:disable"]}}}`,
			wantViol: true,
			wantText: "security_opt",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := dockercompose.CheckModel(modelFromJSON(t, tc.js), cwd)
			if got := len(v) > 0; got != tc.wantViol {
				t.Fatalf("violation=%v, want %v (%v)", got, tc.wantViol, v)
			}
			if tc.wantViol && !strings.Contains(v[0].String(), tc.wantText) {
				t.Errorf("violation %q does not contain %q", v[0].String(), tc.wantText)
			}
		})
	}
}

// CheckModel must report every violation across services in a deterministic
// (service-name sorted) order, since the wrapper prints all of them.
func TestCheckModel_MultipleViolations_DeterministicOrder(t *testing.T) {
	js := `{"services":{
		"zeta":{"privileged":true},
		"alpha":{"volumes":[{"type":"bind","source":"/etc","target":"/etc"}]}
	}}`
	v := dockercompose.CheckModel(modelFromJSON(t, js), "/work")
	if len(v) != 2 {
		t.Fatalf("got %d violations, want 2: %v", len(v), v)
	}
	if v[0].Service != "alpha" || v[1].Service != "zeta" {
		t.Errorf("services out of order: %q then %q, want alpha then zeta", v[0].Service, v[1].Service)
	}
}
