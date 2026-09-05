// command_profile_test.go
//
// Guards the class of mistake that produced this branch's two Critical
// findings before they were fixed: a path granted both write access and
// execute access to the same command_policies command. Both times, that
// intersection was the sandbox escape — a binary staged into a writable
// directory that the same command could then execve directly, unmediated
// by invocation_policy or any shim. Neither was catchable by
// `agent-sandbox doctor` (doctor only ever checked the broker's own binary
// path against writable grants, not this general writable-and-executable
// shape), so the guard belongs here instead: a plain test over the
// committed command-profile.json itself.
package main

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// commandProfileForGuard is the minimal shape this test needs: the
// session-wide filesystem.allow list (read+write), and every
// command_policies command's every caller edge's fs_write and exec_paths.
// Everything else in command-profile.json (fs_read, invocation_policy,
// network, ...) is irrelevant to the writable/exec-able intersection and
// deliberately left unparsed.
type commandProfileForGuard struct {
	Filesystem struct {
		Allow []string `json:"allow"`
	} `json:"filesystem"`
	CommandPolicies struct {
		Commands map[string]struct {
			From map[string]struct {
				Sandbox struct {
					FSWrite   []string `json:"fs_write"`
					ExecPaths []string `json:"exec_paths"`
				} `json:"sandbox"`
			} `json:"from"`
		} `json:"commands"`
	} `json:"command_policies"`
}

// writableExecOverlapExceptions names every path this test is told to
// ignore, as "command:path", along with why. Add to this list only for a
// deliberately accepted residual — see the reason before entering another
// one.
var writableExecOverlapExceptions = map[string]string{
	// go's own fs_write and exec_paths both include /tmp: `go test` compiles
	// a test binary and immediately execs it, and that "write it, then run
	// it" directory cannot be read-only. README.md's "The command profile"
	// section documents this as a real, measured, unclosed residual (a
	// dynamically linked binary staged into $WORKDIR and copied to /tmp
	// fails at the shared-library-loading step, but a fully self-contained
	// static one would not) — this is the in-code half of that same
	// disclosure, not a new decision.
	"go:/tmp": "go test compiles into, then execs from, /tmp; see README.md's compiler caveat",
}

func TestCommandProfile_NoPathIsBothWritableAndExecutable(t *testing.T) {
	data, err := os.ReadFile("command-profile.json")
	if err != nil {
		t.Fatalf("read command-profile.json: %v", err)
	}
	var p commandProfileForGuard
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("unmarshal command-profile.json: %v", err)
	}

	for cmdName, cmd := range p.CommandPolicies.Commands {
		writable := map[string]bool{}
		for _, w := range p.Filesystem.Allow {
			writable[normalizePathForGuard(w)] = true
		}
		for _, edge := range cmd.From {
			for _, w := range edge.Sandbox.FSWrite {
				writable[normalizePathForGuard(w)] = true
			}
		}

		execable := map[string]bool{}
		for _, edge := range cmd.From {
			for _, e := range edge.Sandbox.ExecPaths {
				execable[normalizePathForGuard(e)] = true
			}
		}

		var overlaps []string
		for path := range writable {
			if !execable[path] {
				continue
			}
			key := cmdName + ":" + path
			if _, exempt := writableExecOverlapExceptions[key]; exempt {
				continue
			}
			overlaps = append(overlaps, path)
		}
		sort.Strings(overlaps)
		for _, path := range overlaps {
			t.Errorf("command %q: %q is both writable (filesystem.allow or a caller's fs_write) "+
				"and executable (a caller's exec_paths) — a binary written there can be exec'd "+
				"directly by this same command, bypassing invocation_policy entirely; either "+
				"narrow one of the two grants, or add a named, commented exception to "+
				"writableExecOverlapExceptions explaining why this one is accepted", cmdName, path)
		}
	}
}

// normalizePathForGuard trims whitespace and a trailing slash so "/tmp" and
// "/tmp/" (or an accidental leading/trailing space) are recognized as the
// same path. It does not resolve symlinks or $WORKDIR/$TMPDIR-style
// placeholders — those are nono's job at profile-load time, not this
// static check's.
func normalizePathForGuard(p string) string {
	p = strings.TrimSpace(p)
	if p != "/" {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}
