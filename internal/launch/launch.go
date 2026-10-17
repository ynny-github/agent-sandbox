// Package launch runs a user-owned launch script from .agent-sandbox/ only
// after the whole directory matches a copy the user approved, kept where a
// sandboxed command cannot write. Everything under $WORKDIR — this directory,
// the profiles, even .git — is writable from the command profile, so the
// approved copy, not git, is the trust anchor, and the copy is what runs.
package launch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Dir is the project directory a launch is made of.
const Dir = ".agent-sandbox"

var (
	ErrUsage    = errors.New("a launch name is a file name in .agent-sandbox/bin")
	ErrDeclined = errors.New("not run: the changes were not approved")
)

// Options are Run's collaborators; the CLI supplies the real ones.
type Options struct {
	// Prompt shows diff and asks for approval. An error means it could not
	// ask (no terminal).
	Prompt func(diff string) (bool, error)
	// Writable reports whether profile grants write access to path.
	Writable func(profile, path string) (bool, error)
	// Exec replaces the process (syscall.Exec); it returns only on failure.
	Exec func(path string, argv, env []string) error
	Out  io.Writer
}

// TrustedDir is where project's approved copy lives.
func TrustedDir(project string) (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	sum := sha256.Sum256([]byte(project))
	return filepath.Join(base, "agent-sandbox", "trusted", hex.EncodeToString(sum[:])[:16]), nil
}

type file struct {
	data []byte
	exec bool
}

func Run(name string, args []string, o Options) error {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
		return ErrUsage
	}
	project, err := os.Getwd()
	if err != nil {
		return err
	}
	src := filepath.Join(project, Dir)
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return fmt.Errorf("no %s/ in %s; run `agent-sandbox init`", Dir, project)
	}
	cur, err := readTree(src)
	if err != nil {
		return err
	}
	if f, ok := cur[filepath.Join("bin", name)]; !ok || !f.exec {
		return fmt.Errorf("%s/bin/%s is not an executable file", Dir, name)
	}
	trusted, err := TrustedDir(project)
	if err != nil {
		return err
	}
	old, err := readTree(trusted)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if diff := describe(old, cur, trusted, src); diff != "" {
		ok, err := o.Prompt(diff)
		if err != nil {
			return fmt.Errorf("%s/ changed and needs approval: %w", Dir, err)
		}
		if !ok {
			return ErrDeclined
		}
		if err := replaceTree(trusted, cur); err != nil {
			return fmt.Errorf("store the approved copy: %w", err)
		}
	}
	profile := filepath.Join(trusted, "command-profile.json")
	writable, err := o.Writable(profile, trusted)
	if err != nil {
		return fmt.Errorf("check that the command profile cannot write %s: %w", trusted, err)
	}
	if writable {
		return fmt.Errorf("the command profile can write %s, where the approved copy lives; "+
			"narrow the grant (`nono why --profile %s --path %s --op write` names it)", trusted, profile, trusted)
	}
	script := filepath.Join(trusted, "bin", name)
	return o.Exec(script, append([]string{script}, args...), os.Environ())
}

// readTree reads every regular file under root, keyed by relative path. A
// symlink is refused: its target is outside what is compared.
func readTree(root string) (map[string]file, error) {
	if _, err := os.Lstat(root); err != nil {
		return nil, err
	}
	tree := map[string]file{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; %s/ may hold only regular files and directories", p, Dir)
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		tree[rel] = file{data: data, exec: info.Mode()&0o111 != 0}
		return nil
	})
	return tree, err
}

// describe returns "" when old and cur match, else a summary of added,
// removed and changed files followed by `diff -ru` output when available.
func describe(old, cur map[string]file, oldRoot, curRoot string) string {
	var lines []string
	for _, p := range sortedKeys(cur) {
		o, ok := old[p]
		switch {
		case !ok:
			lines = append(lines, "added:   "+p)
		case !bytes.Equal(o.data, cur[p].data) || o.exec != cur[p].exec:
			lines = append(lines, "changed: "+p)
		}
	}
	for _, p := range sortedKeys(old) {
		if _, ok := cur[p]; !ok {
			lines = append(lines, "removed: "+p)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	out := strings.Join(lines, "\n") + "\n"
	if old != nil {
		if d, err := exec.Command("diff", "-ru", oldRoot, curRoot).Output(); len(d) > 0 || err == nil {
			out += "\n" + string(d)
		}
	} else {
		for _, p := range sortedKeys(cur) {
			out += fmt.Sprintf("\n--- %s\n%s", p, cur[p].data)
		}
	}
	return out
}

func sortedKeys(m map[string]file) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// replaceTree writes tree into a fresh directory beside dst and swaps it in.
func replaceTree(dst string, tree map[string]file) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), filepath.Base(dst)+".new-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for rel, f := range tree {
		p := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if f.exec {
			mode = 0o700
		}
		if err := os.WriteFile(p, f.data, mode); err != nil {
			return err
		}
	}
	old := dst + ".old"
	os.RemoveAll(old)
	if err := os.Rename(dst, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Rename(old, dst)
		return err
	}
	return os.RemoveAll(old)
}
