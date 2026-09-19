package execd

import (
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// refusePolicyPipeChains reports whether file's parsed command line contains
// a pipe (`|` or `|&`) with two or more stages that resolve, before anything
// runs, to a policy-controlled command. names lists which resolved command
// names triggered the refusal, for the caller's error message.
//
// The exact two-stage shape this exists for — a policy-controlled reader
// that blocks on stdin, fed by a policy-controlled writer — is measured,
// against a real nono session, to hang and strand a process
// (task-8-report.md's Finding B). A three-or-more-stage chain with a
// policy-controlled command at two non-adjacent ends (`policy | floor |
// policy`) is refused by the same check but was not independently measured
// to hang — it is reasoned to be exposed to the identical hazard, since all
// stages of one pipe still run concurrently regardless of how many there
// are, not confirmed to reproduce it. Two policy commands piped together
// where neither blocks on stdin (`git --version | git --version`) was
// measured *not* to hang, and would still be refused here — this check
// cannot tell that case apart from the one it exists for, since doing so
// would require knowing whether a stage reads its stdin to EOF, which is a
// runtime property a static, parse-time pass cannot see.
//
// This is a static, parse-time check: LookPathDir is used only to learn
// where a literal command name would resolve, never to run anything, so
// there is no race and no side effect to get wrong — the two properties a
// runtime detection attempt (tried and abandoned; see the doc comment on the
// old execHandler ordering this replaced) could not deliver.
//
// Deliberately narrow, not a general "two policy commands running
// concurrently in one request" detector: it inspects only a literal pipe
// chain's own immediate stages (`a | b | c`, flattened through nested
// Pipe/PipeAll operators), each stage's command name only when it is a
// simple, unexpanded literal. It does not follow into a stage that is itself
// a compound command (a `{ }` group, `if`, `while`, a subshell, ...) to find
// a pipe buried inside it, and it has no way to see a policy command reached
// through backgrounding (`policy & policy`) or command substitution
// (`$(policy) | policy`) — both are exposed to the same underlying hazard,
// by the same reasoning, but neither is a literal pipe stage this function
// can resolve without simulating expansion or execution, which is exactly
// what staying static rules out. A profile author relying on this as a
// complete guarantee against the hang, rather than the specific shape it
// covers, would be relying on more than it delivers.
func refusePolicyPipeChains(file *syntax.File, cwd string, env expand.Environ) (names []string, refuse bool) {
	var found []string
	syntax.Walk(file, func(n syntax.Node) bool {
		bc, ok := n.(*syntax.BinaryCmd)
		if !ok || (bc.Op != syntax.Pipe && bc.Op != syntax.PipeAll) {
			return true
		}
		var policyNames []string
		for _, stage := range flattenPipeStages(bc) {
			call, ok := stage.Cmd.(*syntax.CallExpr)
			if !ok || len(call.Args) == 0 {
				continue
			}
			name := call.Args[0].Lit()
			if name == "" {
				continue // not a simple literal; cannot resolve statically
			}
			path, err := interp.LookPathDir(cwd, env, name)
			if err != nil {
				continue
			}
			if isPolicyControlledPath(path) {
				policyNames = append(policyNames, name)
			}
		}
		if len(policyNames) >= 2 {
			found = append(found, policyNames...)
		}
		return false // this chain is fully inspected; do not also revisit its nested Pipe/PipeAll nodes
	})
	return found, len(found) > 0
}

// flattenPipeStages returns bc's pipe chain as an ordered list of stages,
// flattening through any nested Pipe/PipeAll operator (`a | b | c` parses as
// nested BinaryCmd nodes) so a 3-or-more-stage pipe is inspected as a whole
// rather than as two independent 2-stage checks that would each see only
// half of it.
func flattenPipeStages(bc *syntax.BinaryCmd) []*syntax.Stmt {
	var stages []*syntax.Stmt
	var collect func(s *syntax.Stmt)
	collect = func(s *syntax.Stmt) {
		if inner, ok := s.Cmd.(*syntax.BinaryCmd); ok && (inner.Op == syntax.Pipe || inner.Op == syntax.PipeAll) {
			collect(inner.X)
			collect(inner.Y)
			return
		}
		stages = append(stages, s)
	}
	collect(bc.X)
	collect(bc.Y)
	return stages
}
