// Package skills embeds the agent skills llm-router installs into Claude
// Code and Codex.
package skills

import "embed"

//go:embed all:llmr-explain
var FS embed.FS

// ExplainSkillPath is the embedded path to the explanation skill's content,
// relative to FS.
const ExplainSkillPath = "llmr-explain/SKILL.md"
