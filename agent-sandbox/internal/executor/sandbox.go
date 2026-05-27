package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/cli/cli/command"
	composeapi "github.com/docker/compose/v2/pkg/api"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
)

const SandboxServiceName = "workspace"

// NormalizeProjectName converts a directory name to a Docker Compose project name.
func NormalizeProjectName(name string) string {
	name = strings.ToLower(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-_")
}

func ProjectSandboxName(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	base := NormalizeProjectName(filepath.Base(abs))
	if base == "" {
		base = "workspace"
	}
	sum := sha256.Sum256([]byte(abs))
	return "cr-sandbox-" + base + "-" + hex.EncodeToString(sum[:])[:10]
}

// DetectProjectNetwork checks whether a Docker network named "<cwd-project>_<suffix>" exists.
// Returns the full network name if found, "" otherwise.
// If suffix is empty, "default" is used.
func DetectProjectNetwork(ctx context.Context, dockerCLI command.Cli, suffix string) string {
	if suffix == "" {
		suffix = "default"
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	projectName := NormalizeProjectName(filepath.Base(cwd))
	if projectName == "" {
		return ""
	}
	networkName := projectName + "_" + suffix
	_, inspectErr := dockerCLI.Client().NetworkInspect(ctx, networkName, dockernetwork.InspectOptions{})
	if inspectErr != nil {
		if !errdefs.IsNotFound(inspectErr) {
			slog.Warn("sandbox: network inspect error", "network", networkName, "error", inspectErr)
		}
		return ""
	}
	return networkName
}

// GenerateGostConfig produces a go-gost v3 YAML configuration with:
// - SOCKS5 proxy on :1080
// - HTTP proxy on :3128
// - default-deny bypass with whitelist of allowCIDRs and allowHosts
func GenerateGostConfig(allowCIDRs, allowHosts []string) string {
	var b strings.Builder
	b.WriteString("services:\n")
	b.WriteString("  - name: socks5-0\n")
	b.WriteString("    addr: \":1080\"\n")
	b.WriteString("    handler:\n")
	b.WriteString("      type: socks5\n")
	b.WriteString("      bypass: allow-list\n")
	b.WriteString("    listener:\n")
	b.WriteString("      type: tcp\n")
	b.WriteString("  - name: http-0\n")
	b.WriteString("    addr: \":3128\"\n")
	b.WriteString("    handler:\n")
	b.WriteString("      type: http\n")
	b.WriteString("      bypass: allow-list\n")
	b.WriteString("    listener:\n")
	b.WriteString("      type: tcp\n")
	b.WriteString("\nbypasses:\n")
	b.WriteString("  - name: allow-list\n")
	b.WriteString("    reverse: true\n")
	b.WriteString("    matchers:\n")
	for _, cidr := range allowCIDRs {
		fmt.Fprintf(&b, "      - %s\n", cidr)
	}
	for _, host := range allowHosts {
		fmt.Fprintf(&b, "      - %s\n", host)
	}
	return b.String()
}

func strPtr(s string) *string { return &s }

func NewSandboxProject(pid, uid, gid int, buildContext, dockerfile, image string, allowCIDRs, allowHosts []string, externalNetwork string) (*composetypes.Project, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("sandbox: getwd: %w", err)
	}
	absContext, err := filepath.Abs(buildContext)
	if err != nil {
		return nil, fmt.Errorf("sandbox: abs build_context: %w", err)
	}
	if uid == 0 {
		return nil, fmt.Errorf("sandbox: running as root is not allowed")
	}

	projectName := ProjectSandboxName(cwd)
	projectNetworks := composetypes.Networks{
		"default":          composetypes.NetworkConfig{Name: projectName + "_default"},
		"sandbox_internal": {Internal: true, Name: projectName + "_sandbox_internal"},
	}
	// workspace starts on default during Up (full internet for build/pull).
	// ApplyNetworkPolicy() moves it to sandbox_internal after Up completes.
	workspaceNetworks := map[string]*composetypes.ServiceNetworkConfig{
		"default": nil,
	}
	if externalNetwork != "" {
		projectNetworks[externalNetwork] = composetypes.NetworkConfig{External: true, Name: externalNetwork}
		workspaceNetworks[externalNetwork] = nil
	}

	gostConfig := GenerateGostConfig(allowCIDRs, allowHosts)

	serviceCustomLabels := func(name string) composetypes.Labels {
		return composetypes.Labels{
			composeapi.ProjectLabel:     projectName,
			composeapi.ServiceLabel:     name,
			composeapi.VersionLabel:     composeapi.ComposeVersion,
			composeapi.WorkingDirLabel:  cwd,
			composeapi.ConfigFilesLabel: "",
			composeapi.OneoffLabel:      "False",
		}
	}

	return &composetypes.Project{
		Name:       projectName,
		WorkingDir: cwd,
		Networks:   projectNetworks,
		Configs: composetypes.Configs{
			"gost_config": {
				Name:    "gost_config",
				Content: gostConfig,
			},
		},
		Services: composetypes.Services{
			SandboxServiceName: {
				Name:         SandboxServiceName,
				Image:        "agent-sandbox/" + image,
				User:         fmt.Sprintf("%d:%d", uid, gid),
				WorkingDir:   "/workspace",
				CustomLabels: serviceCustomLabels(SandboxServiceName),
				Build: &composetypes.BuildConfig{
					Context:    absContext,
					Dockerfile: dockerfile,
				},
				Volumes: []composetypes.ServiceVolumeConfig{
					{
						Type:   "bind",
						Source: cwd,
						Target: "/workspace",
					},
				},
				// Pre-configure proxy env so workspace uses gost after ApplyNetworkPolicy.
				Environment: composetypes.MappingWithEquals{
					"HOME":        strPtr("/tmp"),
					"HTTP_PROXY":  strPtr("http://gost:3128"),
					"HTTPS_PROXY": strPtr("http://gost:3128"),
					"http_proxy":  strPtr("http://gost:3128"),
					"https_proxy": strPtr("http://gost:3128"),
					"ALL_PROXY":   strPtr("socks5://gost:1080"),
					"all_proxy":   strPtr("socks5://gost:1080"),
					"NO_PROXY":    strPtr("localhost,127.0.0.1,gost"),
					"no_proxy":    strPtr("localhost,127.0.0.1,gost"),
				},
				Labels: composetypes.Labels{
					"cr.managed":     "true",
					"cr.project_dir": cwd,
				},
				Networks: workspaceNetworks,
			},
			"gost": {
				Name:         "gost",
				Image:        "gogost/gost:3",
				Restart:      "on-failure",
				CustomLabels: serviceCustomLabels("gost"),
				Configs: []composetypes.ServiceConfigObjConfig{
					{
						Source: "gost_config",
						Target: "/etc/gost/config.yaml",
					},
				},
				Labels: composetypes.Labels{
					"cr.managed":     "true",
					"cr.project_dir": cwd,
				},
				// gost bridges sandbox_internal (workspace) and default (internet).
				Networks: map[string]*composetypes.ServiceNetworkConfig{
					"sandbox_internal": nil,
					"default":          nil,
				},
			},
		},
	}, nil
}
