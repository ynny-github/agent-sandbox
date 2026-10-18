// Package agentsandbox embeds the files `agent-sandbox init` writes, so the
// templates a binary writes always match that binary. It sits at the module
// root because go:embed cannot reach above its own package directory.
package agentsandbox

import "embed"

// Assets holds templates/minimal (including .agent-sandbox/, hence all:) and
// the two profile skills, under their repository paths.
//
//go:embed all:templates/minimal .claude/skills/growing-a-nono-profile/SKILL.md .claude/skills/verifying-nono-sandbox-claims/SKILL.md
var Assets embed.FS
