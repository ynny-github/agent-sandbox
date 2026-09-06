package git

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe"
)

// knownSubcommands lists git's own builtin and plumbing command names (git
// 2.54.0's "git help -a", Main Porcelain / Ancillary / Interacting with
// Others / Low-level sections; the guide topics under "User-facing" and
// "Developer-facing" are not invocable as "git <name>" and are excluded).
//
// A name that is not in this set is treated as a candidate alias: it cannot
// be a real git command, so the only way it does anything is a configured
// alias, and the wrapper must resolve and re-check what it expands to. A
// future git version adding a command this list does not yet know about
// fails closed here (refused as an unresolved "alias"), not open.
var knownSubcommands = map[string]bool{
	"add": true, "am": true, "annotate": true, "apply": true, "archimport": true,
	"archive": true, "backfill": true, "bisect": true, "blame": true, "branch": true,
	"bugreport": true, "bundle": true, "cat-file": true, "check-attr": true,
	"check-ignore": true, "check-mailmap": true, "checkout": true, "checkout-index": true,
	"check-ref-format": true, "cherry": true, "cherry-pick": true, "citool": true,
	"clean": true, "clone": true, "column": true, "commit": true, "commit-graph": true,
	"commit-tree": true, "config": true, "count-objects": true, "credential": true,
	"credential-cache": true, "credential-store": true, "cvsexportcommit": true,
	"cvsimport": true, "cvsserver": true, "daemon": true, "describe": true,
	"diagnose": true, "diff": true, "diff-files": true, "diff-index": true,
	"diff-pairs": true, "difftool": true, "diff-tree": true, "fast-export": true,
	"fast-import": true, "fetch": true, "fetch-pack": true, "filter-branch": true,
	"fmt-merge-msg": true, "for-each-ref": true, "for-each-repo": true,
	"format-patch": true, "fsck": true, "gc": true, "get-tar-commit-id": true,
	"gitk": true, "gitweb": true, "grep": true, "gui": true, "hash-object": true,
	"help": true, "history": true, "hook": true, "http-backend": true,
	"imap-send": true, "index-pack": true, "init": true, "instaweb": true,
	"interpret-trailers": true, "last-modified": true, "log": true, "ls-files": true,
	"ls-remote": true, "ls-tree": true, "mailinfo": true, "mailsplit": true,
	"maintenance": true, "merge": true, "merge-base": true, "merge-file": true,
	"merge-index": true, "merge-one-file": true, "mergetool": true, "merge-tree": true,
	"mktag": true, "mktree": true, "multi-pack-index": true, "mv": true,
	"name-rev": true, "notes": true, "p4": true, "pack-objects": true,
	"pack-redundant": true, "pack-refs": true, "patch-id": true, "prune": true,
	"prune-packed": true, "pull": true, "push": true, "quiltimport": true,
	"range-diff": true, "read-tree": true, "rebase": true, "reflog": true,
	"refs": true, "remote": true, "repack": true, "replace": true, "replay": true,
	"repo": true, "request-pull": true, "rerere": true, "reset": true,
	"restore": true, "revert": true, "rev-list": true, "rev-parse": true,
	"rm": true, "scalar": true, "send-email": true, "send-pack": true,
	"sh-i18n": true, "shortlog": true, "show": true, "show-branch": true,
	"show-index": true, "show-ref": true, "sh-setup": true, "sparse-checkout": true,
	"stash": true, "status": true, "stripspace": true, "submodule": true, "svn": true,
	"switch": true, "symbolic-ref": true, "tag": true, "unpack-file": true,
	"unpack-objects": true, "update-index": true, "update-ref": true,
	"update-server-info": true, "var": true, "verify-commit": true,
	"verify-pack": true, "verify-tag": true, "version": true, "whatchanged": true,
	"worktree": true, "write-tree": true,
}

// maxAliasDepth bounds alias-expansion recursion so a self-referential or
// mutually recursive pair of aliases is refused rather than looped forever.
// git itself caps alias nesting; this limit sits well under that.
const maxAliasDepth = 10

// checkAlias handles the case where inv's first token is not a known git
// subcommand: it is either a configured alias or nothing at all. Neither
// -c alias.x=... nor a "git config alias.x ..." invocation is the only way
// to install one — .git/config is writable directly, with no execve at all —
// so this is the one place that route is caught, by resolving what the name
// actually expands to and re-checking that.
func checkAlias(inv Invocation, depth int) []safe.Violation {
	if inv.Subcommand == "" || knownSubcommands[inv.Subcommand] {
		return nil
	}

	if depth >= maxAliasDepth {
		return []safe.Violation{{Source: "cli", Setting: fmt.Sprintf(
			"%q nests more than %d aliases deep; refusing rather than keep expanding", inv.Subcommand, maxAliasDepth)}}
	}

	if bad := unsafeGlobalForAliasLookup(inv.Global); bad != "" {
		return []safe.Violation{{Source: "cli", Setting: fmt.Sprintf(
			"global option %q is not on the list this wrapper knows is safe to check before resolving %q as an alias; refusing rather than risk checking a different repository or config than the one that would actually run (add %q to aliasSafeGlobals once its effect on alias resolution is verified)",
			bad, inv.Subcommand, bad)}}
	}

	value, ok := resolveAlias(inv.Subcommand, inv.Global)
	if !ok {
		return []safe.Violation{{Source: "cli", Setting: fmt.Sprintf(
			"%q is not a git command and its alias could not be resolved (no repository, or git errored)", inv.Subcommand)}}
	}

	if strings.HasPrefix(value, "!") {
		return []safe.Violation{{Source: "cli", Setting: fmt.Sprintf(
			"alias %q runs a shell command (%s); there is no shell in this sandbox to inspect it", inv.Subcommand, value)}}
	}

	tokens := splitAliasValue(value)
	if len(tokens) == 0 {
		return []safe.Violation{{Source: "cli", Setting: fmt.Sprintf(
			"alias %q resolves to an empty command; refusing", inv.Subcommand)}}
	}

	expanded := Invocation{
		Global:     inv.Global,
		Subcommand: tokens[0],
		Args:       append(append([]string{}, tokens[1:]...), inv.Args...),
	}

	inner := checkInvocation(expanded, depth+1)
	if len(inner) == 0 {
		return nil
	}
	out := make([]safe.Violation, 0, len(inner))
	for _, v := range inner {
		out = append(out, safe.Violation{Source: "cli", Setting: fmt.Sprintf(
			"alias %q expands to %q, which is blocked: %s", inv.Subcommand, strings.Join(tokens, " "), v.Setting)})
	}
	return out
}

// aliasSafeGlobals are the git global options verified safe to forward,
// verbatim, onto this package's own "config --get alias.<name>" call. Each
// entry changes which repository, or which config values, a git invocation
// resolves against, and forwarding it reproduces that for this package's own
// lookup:
//
//   - -C, --git-dir, --work-tree, --namespace select the repository.
//   - -c, --config-env set a config value (including an alias.* one) ahead
//     of every file git would otherwise read.
//   - --bare sets GIT_DIR to the current directory. CRITICAL from review,
//     measured: with alias.z=status in outer/'s repository config and a
//     hand-written "config" file directly in outer/inner/ (no ".git"
//     subdirectory — a --bare GIT_DIR *is* the config file's directory)
//     holding "z = !echo BAREPAYLOAD", "git config --get alias.z" from
//     outer/inner reports "status" (the ordinary, non-bare repository) while
//     "git --bare z" from the same directory runs the shell payload. Not
//     forwarding --bare reproduced exactly the divergence forwarding -C was
//     added to close.
//
// Every other global is refused rather than silently dropped — including one
// Parse did not recognize at all (see unsafeGlobalForAliasLookup and
// checkAlias). Dropping an unclassified global is what made --bare a repeat
// of the -C problem: the fix is not "also handle --bare", it is to stop
// enumerating globals believed relevant and default to refusing any global
// this package has not verified, so the next git release cannot reopen this
// silently. The cost accepted for this: an invocation using a global that is
// genuinely harmless for alias resolution (say, --paginate) but is not yet
// in this map is refused too, until someone verifies it and adds it here —
// visibly, as a refusal with the global's name in it, not silently.
//
// --exec-path is deliberately not here, in either direction: it is refused
// outright, for every invocation, by the exec-path-injection rule in
// rules.go (R28), before alias-expansion ever runs. Adding it here would
// only mean "safe to drop for alias lookup," a different and wrong claim.
var aliasSafeGlobals = map[string]bool{
	"-C": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
	"-c": true, "--config-env": true, "--bare": true,
}

// unsafeGlobalForAliasLookup returns the name of the first global in globals
// that is not in aliasSafeGlobals, or "" if every one of them is.
func unsafeGlobalForAliasLookup(globals []GlobalOpt) string {
	for _, g := range globals {
		if !aliasSafeGlobals[g.Name] {
			return g.Name
		}
	}
	return ""
}

// aliasLookupArgv builds the "config --get alias.<name>" argv, prefixed with
// every aliasSafeGlobals entry from globals, in order. Callers must have
// already confirmed every global in globals is in aliasSafeGlobals (see
// unsafeGlobalForAliasLookup) — this does not re-check, it only knows how to
// format each safe global: with its value when git itself expects one
// (globalValueOpts, defined in git.go) and without one otherwise (--bare
// takes none; appending an empty value token for it would insert a spurious
// empty argument that git would misread as the subcommand).
func aliasLookupArgv(name string, globals []GlobalOpt) []string {
	var argv []string
	for _, g := range globals {
		if !aliasSafeGlobals[g.Name] {
			continue
		}
		argv = append(argv, g.Name)
		if globalValueOpts[g.Name] {
			argv = append(argv, g.Value)
		}
	}
	return append(argv, "config", "--get", "alias."+name)
}

// resolveAlias runs "<RealBinary> [repo-selecting globals] config --get
// alias.<name>" and returns the alias's raw value. Alias precedence spans
// repo, global and system config files and include directives, which is
// exactly why this asks the real git rather than parsing .git/config by
// hand.
//
// It resolves RealBinary ("realgit"), never "git", for the same reason
// execGit does: this wrapper is itself bound to the name "git". See
// git.RealBinary for why the resolved name must not start with "git-"
// either.
func resolveAlias(name string, globals []GlobalOpt) (string, bool) {
	path, err := exec.LookPath(RealBinary)
	if err != nil {
		return "", false
	}
	cmd := exec.Command(path, aliasLookupArgv(name, globals)...)
	// argv[0] is forced to "git", not the resolved RealBinary path: belt-and-
	// braces against git's own argv[0] dispatch (see git.RealBinary), and it
	// keeps any error git prints naming itself consistent. Do not remove this
	// as apparently-redundant.
	cmd.Args[0] = "git"
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimRight(string(out), "\n"), true
}

// isAliasSpace reports whether c is whitespace by git's own reckoning
// (isspace(3) under the C locale: space, tab, newline, vertical tab, form
// feed, carriage return) — not just space and tab. git config supports a
// "\n" escape inside a quoted alias value, and a tokenizer that only splits
// on space/tab treats "reset --hard\nHEAD" as a single unsplittable token,
// so hasLong/hasShort never see "--hard" and the hard-reset rule never
// fires. Measured on git 2.54.0: [alias] h = "reset --hard\nHEAD" makes
// "git h" run "reset --hard HEAD".
func isAliasSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	}
	return false
}

// splitAliasValue splits a non-shell alias's value into argv tokens,
// following git's own simple quoting for aliases: single and double quotes
// group a token, and a backslash escapes the next character. This is not
// full shell syntax — there is no shell in this sandbox to run one, which is
// also why a "!"-prefixed alias is refused outright instead of parsed here.
func splitAliasValue(value string) []string {
	var tokens []string
	var cur strings.Builder
	has := false
	var quote byte
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case quote != 0:
			switch {
			case c == quote:
				quote = 0
			case c == '\\' && i+1 < len(value):
				i++
				cur.WriteByte(value[i])
			default:
				cur.WriteByte(c)
			}
			has = true
		case c == '\'' || c == '"':
			quote = c
			has = true
		case c == '\\' && i+1 < len(value):
			i++
			cur.WriteByte(value[i])
			has = true
		case isAliasSpace(c):
			if has {
				tokens = append(tokens, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteByte(c)
			has = true
		}
	}
	if has {
		tokens = append(tokens, cur.String())
	}
	return tokens
}
