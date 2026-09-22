// agent-sandbox/internal/claude/contextmode.go
package claude

// ContextModeEnvVar names the variable context-mode reads to choose where
// ctx_execute and its siblings actually run. agent-sandbox publishes it; the
// meaning of its values belongs to context-mode's resolveBackendConfig
// (src/exec-backend.ts in github.com/ynny-github/context-mode).
//
// Unset or empty resolves to the "local" backend over there — agent-authored
// code spawned as a child of the MCP server, inside the *agent* profile, under
// none of the command profile's policies. That is why every failure on this
// side of the boundary stops the launch instead of warning: a variable that
// does not arrive produces a working-looking session with the boundary quietly
// absent.
const ContextModeEnvVar = "CONTEXT_MODE_EXEC_BACKEND"

// ContextModeExecd is the only value agent-sandbox ever publishes. context-mode
// also accepts "local", which is its default and needs no help from here. It is
// exported because `agent-sandbox debug` prints the pair.
const ContextModeExecd = "execd"
