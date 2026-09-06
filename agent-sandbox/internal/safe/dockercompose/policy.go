package dockercompose

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe"
)

// dangerousCaps is the built-in set of Linux capabilities that are refused when
// present in cap_add. Names are stored upper-case without the CAP_ prefix.
var dangerousCaps = map[string]bool{
	"ALL":             true,
	"SYS_ADMIN":       true,
	"SYS_PTRACE":      true,
	"SYS_MODULE":      true,
	"SYS_RAWIO":       true,
	"SYS_BOOT":        true,
	"SYS_TIME":        true,
	"NET_ADMIN":       true,
	"NET_RAW":         true,
	"DAC_READ_SEARCH": true,
	"DAC_OVERRIDE":    true,
	"MKNOD":           true,
}

// CheckCLI refuses subcommands that are entrypoints for arbitrary command
// execution, and fails closed on any leading flag ParseArgs could not classify.
func CheckCLI(p ParsedArgs) []safe.Violation {
	if p.Unrecognized != "" {
		return []safe.Violation{{
			Source:  "cli",
			Setting: fmt.Sprintf("unrecognized global flag %q is not allowed", p.Unrecognized),
		}}
	}
	switch p.Subcommand {
	case "run", "exec":
		return []safe.Violation{{
			Source:  "cli",
			Setting: fmt.Sprintf("%q subcommand is not allowed", p.Subcommand),
		}}
	}
	return nil
}

// CheckModel validates the resolved Compose model against the built-in rules,
// using cwd as the mount boundary.
func CheckModel(m Model, cwd string) []safe.Violation {
	var out []safe.Violation
	root := safe.RealPath(cwd)

	// Iterate services in a stable order so output is deterministic.
	for _, name := range sortedServiceNames(m) {
		svc := m.Services[name]
		add := func(setting string) {
			out = append(out, safe.Violation{Source: "compose", Service: name, Setting: setting})
		}

		for _, vol := range svc.Volumes {
			if vol.Type != "bind" {
				continue
			}
			if isDockerSocket(vol.Source) {
				add(fmt.Sprintf("bind mount of the docker socket %q is not allowed", vol.Source))
				continue
			}
			if !safe.PathWithin(root, safe.RealPath(vol.Source)) {
				add(fmt.Sprintf("bind mount %q escapes the work directory", vol.Source))
			}
		}

		if svc.Privileged {
			add("privileged: true is not allowed")
		}
		if svc.NetworkMode == "host" {
			add("network_mode: host is not allowed")
		}
		if svc.Pid == "host" {
			add("pid: host is not allowed")
		}
		if svc.Ipc == "host" {
			add("ipc: host is not allowed")
		}
		if svc.UsernsMode == "host" {
			add("userns_mode: host is not allowed")
		}
		if len(svc.Devices) > 0 {
			add("device passthrough (devices) is not allowed")
		}
		for _, c := range svc.CapAdd {
			if dangerousCaps[normalizeCap(c)] {
				add(fmt.Sprintf("dangerous capability %q in cap_add is not allowed", c))
			}
		}
		for _, opt := range svc.SecurityOpt {
			if isUnsafeSecurityOpt(opt) {
				add(fmt.Sprintf("security_opt %q is not allowed", opt))
			}
		}
	}
	return out
}

func sortedServiceNames(m Model) []string {
	names := make([]string, 0, len(m.Services))
	for n := range m.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func isDockerSocket(source string) bool {
	return filepath.Base(filepath.Clean(source)) == "docker.sock"
}

func normalizeCap(c string) string {
	c = strings.ToUpper(strings.TrimSpace(c))
	return strings.TrimPrefix(c, "CAP_")
}

func isUnsafeSecurityOpt(opt string) bool {
	o := strings.ToLower(strings.TrimSpace(opt))
	if strings.Contains(o, "unconfined") {
		return true
	}
	// label:disable / label=disable both turn off the labeling confinement.
	return strings.Contains(o, "label") && strings.Contains(o, "disable")
}
