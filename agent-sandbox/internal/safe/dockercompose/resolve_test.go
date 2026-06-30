package dockercompose_test

import (
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe/dockercompose"
)

const sampleConfigJSON = `{
  "services": {
    "web": {
      "privileged": true,
      "network_mode": "host",
      "cap_add": ["SYS_ADMIN"],
      "security_opt": ["seccomp:unconfined"],
      "devices": [{"source": "/dev/kvm", "target": "/dev/kvm"}],
      "volumes": [
        {"type": "bind", "source": "/etc", "target": "/etc"},
        {"type": "volume", "source": "data", "target": "/data"}
      ]
    },
    "db": {}
  }
}`

func TestDecodeModel(t *testing.T) {
	m, err := dockercompose.DecodeModel([]byte(sampleConfigJSON))
	if err != nil {
		t.Fatalf("DecodeModel: %v", err)
	}
	if len(m.Services) != 2 {
		t.Fatalf("got %d services, want 2", len(m.Services))
	}
	web := m.Services["web"]
	if !web.Privileged {
		t.Error("web.Privileged = false, want true")
	}
	if web.NetworkMode != "host" {
		t.Errorf("web.NetworkMode = %q, want host", web.NetworkMode)
	}
	if len(web.CapAdd) != 1 || web.CapAdd[0] != "SYS_ADMIN" {
		t.Errorf("web.CapAdd = %v, want [SYS_ADMIN]", web.CapAdd)
	}
	if len(web.Devices) != 1 {
		t.Errorf("len(web.Devices) = %d, want 1", len(web.Devices))
	}
	if len(web.Volumes) != 2 {
		t.Fatalf("len(web.Volumes) = %d, want 2", len(web.Volumes))
	}
	if web.Volumes[0].Type != "bind" || web.Volumes[0].Source != "/etc" {
		t.Errorf("web.Volumes[0] = %+v, want bind /etc", web.Volumes[0])
	}
}

func TestDecodeModel_Invalid(t *testing.T) {
	if _, err := dockercompose.DecodeModel([]byte("{not json")); err == nil {
		t.Error("expected error decoding invalid JSON, got nil")
	}
}
