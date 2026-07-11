package container

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	LabelManaged    = "cr.managed"
	LabelProjectDir = "cr.project_dir"
)

// SandboxSpec fully describes the single sandbox container to create.
type SandboxSpec struct {
	Name            string
	WorkingDir      string
	ImageTag        string
	BuildContext    string
	Dockerfile      string
	NetworkName     string
	ExternalNetwork string
	UID             int
	GID             int
	Internal        bool
	Labels          map[string]string
}

// NewSandboxSpec derives the container spec from config for the current directory.
func NewSandboxSpec(uid, gid int, buildContext, dockerfile, image string, allowExternal bool, externalNetwork string) (*SandboxSpec, error) {
	if uid == 0 {
		return nil, fmt.Errorf("sandbox: running as root is not allowed")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("sandbox: getwd: %w", err)
	}
	absContext, err := filepath.Abs(buildContext)
	if err != nil {
		return nil, fmt.Errorf("sandbox: abs build_context: %w", err)
	}
	name := ProjectSandboxName(cwd)
	return &SandboxSpec{
		Name:            name,
		WorkingDir:      cwd,
		ImageTag:        "agent-sandbox/" + image,
		BuildContext:    absContext,
		Dockerfile:      dockerfile,
		NetworkName:     name + "_sandbox",
		ExternalNetwork: externalNetwork,
		UID:             uid,
		GID:             gid,
		Internal:        !allowExternal,
		Labels: map[string]string{
			LabelManaged:    "true",
			LabelProjectDir: cwd,
		},
	}, nil
}
