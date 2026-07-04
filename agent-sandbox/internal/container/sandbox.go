package container

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

func strPtr(s string) *string { return &s }

func NewSandboxProject(pid, uid, gid int, buildContext, dockerfile, image string, allowExternal bool, externalNetwork string) (*composetypes.Project, error) {
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
	// One network. Internal:true blocks all internet egress (workspace can still
	// reach localhost, same-project containers, and external_network). Internal:false
	// gives full internet access.
	projectNetworks := composetypes.Networks{
		"sandbox": {Internal: !allowExternal, Name: projectName + "_sandbox"},
	}
	workspaceNetworks := map[string]*composetypes.ServiceNetworkConfig{
		"sandbox": nil,
	}
	if externalNetwork != "" {
		projectNetworks[externalNetwork] = composetypes.NetworkConfig{External: true, Name: externalNetwork}
		workspaceNetworks[externalNetwork] = nil
	}

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

	initTrue := true

	return &composetypes.Project{
		Name:       projectName,
		WorkingDir: cwd,
		Networks:   projectNetworks,
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
				Environment: composetypes.MappingWithEquals{
					"HOME": strPtr("/tmp"),
				},
				Labels: composetypes.Labels{
					"cr.managed":     "true",
					"cr.project_dir": cwd,
				},
				Networks: workspaceNetworks,
				Init:     &initTrue,
			},
		},
	}, nil
}
