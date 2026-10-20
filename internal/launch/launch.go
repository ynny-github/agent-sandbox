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
	"unicode"
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

// afterRead runs right after the project tree is read; tests use it to change
// the directory between the read and the prompt.
var afterRead = func() {}

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
	afterRead()
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
	if changes := summarize(old, cur); changes != "" {
		// The diff is taken against a staging copy written from the tree just
		// read, never against the project directory again: a concurrent writer
		// must not be able to show one version and have another approved.
		staging, err := writeTree(trusted, cur)
		if err != nil {
			return fmt.Errorf("stage the copy: %w", err)
		}
		defer os.RemoveAll(staging)
		if old == nil {
			// First launch in this project: there is nothing approved to
			// compare with, so the directory as it stands becomes the approved
			// copy without asking. Accepted trade-off: a cloned repository's
			// script runs unapproved this once, and so does a launch from a
			// path (a symlink, say) that has no copy yet.
			fmt.Fprintf(o.Out, "agent-sandbox launch: first launch in %s; approved %s/ as it is, without asking\n",
				project, Dir)
		} else {
			ok, err := o.Prompt(sanitize(changes + "\n" + diffTrees(trusted, old, staging, cur)))
			if err != nil {
				return fmt.Errorf("%s/ changed and needs approval: %w", Dir, err)
			}
			if !ok {
				return ErrDeclined
			}
		}
		if err := swapIn(trusted, staging); err != nil {
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

// summarize returns "" when old and cur match, else one line per added,
// removed and changed file.
func summarize(old, cur map[string]file) string {
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
	return strings.Join(lines, "\n") + "\n"
}

// diffTrees shows the content of every change: `diff -ruN` between the
// approved copy and the staging copy, so an added file's content is shown too. Without diff, the added and changed
// files are printed whole.
func diffTrees(oldRoot string, old map[string]file, staging string, cur map[string]file) string {
	out, err := exec.Command("diff", "-ruN", oldRoot, staging).Output()
	if errors.Is(err, exec.ErrNotFound) {
		var b strings.Builder
		for _, p := range sortedKeys(cur) {
			if o, ok := old[p]; !ok || !bytes.Equal(o.data, cur[p].data) {
				fmt.Fprintf(&b, "--- %s\n%s\n", p, cur[p].data)
			}
		}
		return b.String()
	}
	return string(out)
}

// sanitize makes every control character other than newline and tab visible,
// so content written by a sandboxed command cannot move the cursor or
// overwrite a line of the diff the user is approving.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r):
			fmt.Fprintf(&b, "\\x%02x", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sortedKeys(m map[string]file) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// writeTree writes tree into a fresh directory beside dst and returns it.
func writeTree(dst string, tree map[string]file) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), filepath.Base(dst)+".new-")
	if err != nil {
		return "", err
	}
	for rel, f := range tree {
		p := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			os.RemoveAll(tmp)
			return "", err
		}
		mode := os.FileMode(0o600)
		if f.exec {
			mode = 0o700
		}
		if err := os.WriteFile(p, f.data, mode); err != nil {
			os.RemoveAll(tmp)
			return "", err
		}
	}
	return tmp, nil
}

// swapIn replaces dst with the staged directory.
func swapIn(dst, staged string) error {
	old := dst + ".old"
	os.RemoveAll(old)
	if err := os.Rename(dst, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(staged, dst); err != nil {
		os.Rename(old, dst)
		return err
	}
	return os.RemoveAll(old)
}
