// Package statusline renders the Claude Code status line from the session
// JSON piped on stdin, reading back the last routing decision this binary
// recorded. It replaces a Node process that used to be spawned on every
// render.
package statusline

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/djalmaaraujo/llm-router/internal/state"
)

const (
	dim    = "\x1b[2m"
	reset  = "\x1b[0m"
	haiku  = "\x1b[32m"
	sonnet = "\x1b[36m"
	opus   = "\x1b[35m"
	fable  = "\x1b[33m"
)

type payload struct {
	SessionID string `json:"session_id"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
	} `json:"workspace"`
	Cwd           string `json:"cwd"`
	ContextWindow struct {
		UsedPercentage float64 `json:"used_percentage"`
	} `json:"context_window"`
	Model struct {
		DisplayName string `json:"display_name"`
	} `json:"model"`
}

var tierColors = map[string]string{
	"haiku":  haiku,
	"sonnet": sonnet,
	"opus":   opus,
	"fable":  fable,
}

func colorFor(tier string) string {
	if c, ok := tierColors[tier]; ok {
		return c
	}
	return ""
}

func dirOf(p payload) string {
	dir := p.Workspace.CurrentDir
	if dir == "" {
		dir = p.Cwd
	}
	return filepath.Base(dir)
}

// routed renders the left half of the line: what the router decided, or that
// it has not decided yet.
func routed(s *state.Status, displayName string) string {
	if s == nil {
		return dim + "jev: waiting for first prompt" + reset
	}
	if s.Manual {
		return "⏸ manual " + displayName
	}

	label := s.Model
	if label == "" {
		label = strings.ToUpper(s.Tier)
	}
	if label == "" {
		label = "unknown"
	}

	text := colorFor(s.Tier) + label + reset

	if s.Confidence != nil {
		text += fmt.Sprintf(" %s(p=%.2f)%s", dim, *s.Confidence, reset)
	}

	// Naming the reason only when routing declined the obvious thing keeps
	// the common case short; a status line that explains every turn is noise.
	if reason := reasonToShow(s.Reason); reason != "" {
		text += fmt.Sprintf(" %s(%s)%s", dim, reason, reset)
	}

	return text
}

func reasonToShow(reason string) string {
	if reason == "" || reason == "jev" || reason == "jev/no-change" || strings.Contains(reason, "override") {
		return ""
	}
	if idx := strings.Index(reason, "/"); idx >= 0 {
		return reason[:idx]
	}
	return reason
}

// Render builds the status line from the session JSON Claude Code pipes on
// stdin. Any missing or malformed field degrades to a usable line rather
// than a panic: a status line failure must never break the prompt.
func Render(stdin []byte) string {
	var p payload
	_ = json.Unmarshal(stdin, &p)

	s := state.Read(p.SessionID)

	pct := int(math.Round(p.ContextWindow.UsedPercentage))

	return fmt.Sprintf("%s %s·%s %s %s· %d%% context%s",
		routed(s, p.Model.DisplayName), dim, reset, dirOf(p), dim, pct, reset)
}
