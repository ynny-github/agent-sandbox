package cmd

import "testing"

// The safe parent command and the docker wrapper must be registered so
// `agent-sandbox safe docker` resolves.
func TestSafeDockerCommand_Registered(t *testing.T) {
	safe, _, err := rootCmd.Find([]string{"safe"})
	if err != nil || safe.Name() != "safe" {
		t.Fatalf("safe command not found: %v", err)
	}
	cmd, _, err := rootCmd.Find([]string{"safe", "docker"})
	if err != nil {
		t.Fatalf("docker command not found: %v", err)
	}
	if cmd.Name() != "docker" {
		t.Errorf("got %q, want docker", cmd.Name())
	}
	if !cmd.DisableFlagParsing {
		t.Error("docker must disable flag parsing to pass args verbatim")
	}
}

func TestSplitDockerGlobal(t *testing.T) {
	cases := []struct {
		name         string
		args         []string
		sub          string
		rest         []string
		unrecognized string
	}{
		{"empty", nil, "", nil, ""},
		{"plain subcommand", []string{"run", "--rm", "alpine"}, "run", []string{"run", "--rm", "alpine"}, ""},
		{"compose immediate", []string{"compose", "up", "-d"}, "compose", []string{"compose", "up", "-d"}, ""},
		{"global bool then compose", []string{"--debug", "compose", "up"}, "compose", []string{"compose", "up"}, ""},
		{"global value separate then compose", []string{"--context", "foo", "compose", "up"}, "compose", []string{"compose", "up"}, ""},
		{"global value attached then compose", []string{"--context=foo", "compose", "up"}, "compose", []string{"compose", "up"}, ""},
		{"short value flag then compose", []string{"-H", "unix:///var/run/docker.sock", "compose", "config"}, "compose", []string{"compose", "config"}, ""},
		{"help flag alone", []string{"--help"}, "", nil, ""},
		{"unrecognized global flag", []string{"--bogus", "compose", "up"}, "", nil, "--bogus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub, rest, unrecognized := splitDockerGlobal(tc.args)
			if sub != tc.sub {
				t.Errorf("sub = %q, want %q", sub, tc.sub)
			}
			if unrecognized != tc.unrecognized {
				t.Errorf("unrecognized = %q, want %q", unrecognized, tc.unrecognized)
			}
			if len(rest) != len(tc.rest) {
				t.Fatalf("rest = %v, want %v", rest, tc.rest)
			}
			for i := range rest {
				if rest[i] != tc.rest[i] {
					t.Errorf("rest[%d] = %q, want %q", i, rest[i], tc.rest[i])
				}
			}
		})
	}
}

// TestDockerCLIViolations_RunPrivilegedHostMount_Refused is CRITICAL 3 from
// the review: before this check existed, a non-compose docker invocation
// passed straight through with no checks at all, so
// "docker run --privileged -v /:/host --network host alpine sh" ran
// untouched — strictly more powerful than everything dockercompose.CheckModel
// blocks for the equivalent compose invocation.
func TestDockerCLIViolations_RunPrivilegedHostMount_Refused(t *testing.T) {
	args := []string{"run", "--privileged", "-v", "/:/host", "--network", "host", "alpine", "sh"}
	vs := dockerCLIViolations("run", args, "/work")
	if len(vs) == 0 {
		t.Fatal("expected the reviewer's example to be refused, got no violations")
	}
	t.Logf("violations: %v", vs)
}

func TestDockerCLIViolations_PlainSubcommand_Passes(t *testing.T) {
	if vs := dockerCLIViolations("ps", []string{"ps"}, "/work"); len(vs) != 0 {
		t.Errorf("expected \"docker ps\" to pass, got %v", vs)
	}
}

func TestDockerCLIViolations_ExecSubcommand_Refused(t *testing.T) {
	if vs := dockerCLIViolations("exec", []string{"exec", "-it", "web", "sh"}, "/work"); len(vs) == 0 {
		t.Error("expected \"docker exec\" to be refused, got none")
	}
}

// TestDockerCLIViolations_PrivilegedWithoutRun_StillRefused checks that
// --privileged is refused regardless of subcommand, not only on "run": e.g.
// "docker create --privileged" also creates (though does not yet start) a
// privileged container.
func TestDockerCLIViolations_PrivilegedWithoutRun_StillRefused(t *testing.T) {
	vs := dockerCLIViolations("create", []string{"create", "--privileged", "alpine"}, "/work")
	if len(vs) == 0 {
		t.Fatal("expected --privileged to be refused even on \"create\", got no violations")
	}
}

func TestDockerCLIViolations_BindWithinCwd_Allowed(t *testing.T) {
	vs := dockerCLIViolations("create", []string{"create", "-v", "/work/data:/data", "alpine"}, "/work")
	if len(vs) != 0 {
		t.Errorf("expected a bind within cwd to pass, got %v", vs)
	}
}

func TestDockerCLIViolations_BindOutsideCwd_Refused(t *testing.T) {
	vs := dockerCLIViolations("create", []string{"create", "-v", "/etc:/etc", "alpine"}, "/work")
	if len(vs) == 0 {
		t.Fatal("expected a bind outside cwd to be refused, got no violations")
	}
}

func TestDockerCLIViolations_DockerSocketMount_Refused(t *testing.T) {
	vs := dockerCLIViolations("create",
		[]string{"create", "-v", "/var/run/docker.sock:/var/run/docker.sock", "alpine"}, "/work")
	if len(vs) == 0 {
		t.Fatal("expected a docker.sock bind mount to be refused, got no violations")
	}
}

func TestDockerCLIViolations_NamedVolume_Allowed(t *testing.T) {
	vs := dockerCLIViolations("create", []string{"create", "-v", "myvolume:/data", "alpine"}, "/work")
	if len(vs) != 0 {
		t.Errorf("expected a named volume mount to pass, got %v", vs)
	}
}

func TestDockerCLIViolations_MountFlagBindOutsideCwd_Refused(t *testing.T) {
	vs := dockerCLIViolations("create",
		[]string{"create", "--mount", "type=bind,src=/etc,dst=/etc", "alpine"}, "/work")
	if len(vs) == 0 {
		t.Fatal("expected a --mount type=bind outside cwd to be refused, got no violations")
	}
}

func TestDockerCLIViolations_MountFlagVolume_Allowed(t *testing.T) {
	vs := dockerCLIViolations("create",
		[]string{"create", "--mount", "type=volume,src=myvolume,dst=/data", "alpine"}, "/work")
	if len(vs) != 0 {
		t.Errorf("expected a --mount type=volume to pass, got %v", vs)
	}
}

// TestRunSafeDocker_ComposeWithLeadingGlobal_Detected is IMPORTANT 4 from the
// review: "docker --context prod compose up" must not silently execute as
// "docker compose up" against the default daemon. splitDockerGlobal already
// separates the leading docker-level globals from "compose up"; this pins
// that "args[:len(args)-len(rest)]" — the exact expression runSafeDocker
// uses to detect and refuse a non-empty leading segment — recovers precisely
// the flags that would otherwise vanish with no diagnostic.
func TestRunSafeDocker_ComposeWithLeadingGlobal_Detected(t *testing.T) {
	args := []string{"--context", "prod", "compose", "up"}
	sub, rest, unrecognized := splitDockerGlobal(args)
	if unrecognized != "" {
		t.Fatalf("unexpected unrecognized flag: %q", unrecognized)
	}
	if sub != "compose" {
		t.Fatalf("sub = %q, want compose", sub)
	}
	leading := args[:len(args)-len(rest)]
	want := []string{"--context", "prod"}
	if len(leading) != len(want) {
		t.Fatalf("leading = %v, want %v", leading, want)
	}
	for i := range leading {
		if leading[i] != want[i] {
			t.Errorf("leading[%d] = %q, want %q", i, leading[i], want[i])
		}
	}
}

// TestRunSafeDocker_ComposeNoLeadingGlobal_EmptySegment checks the other side
// of the same expression: a bare "compose up" (no docker-level global first)
// must compute an empty leading segment, so ordinary compose invocations are
// never refused by this check.
func TestRunSafeDocker_ComposeNoLeadingGlobal_EmptySegment(t *testing.T) {
	args := []string{"compose", "up"}
	sub, rest, _ := splitDockerGlobal(args)
	if sub != "compose" {
		t.Fatalf("sub = %q, want compose", sub)
	}
	if leading := args[:len(args)-len(rest)]; len(leading) != 0 {
		t.Errorf("leading = %v, want empty", leading)
	}
}
