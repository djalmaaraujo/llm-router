// Package explain renders the one report a user can read to see what the
// router actually did on a turn, since the CLI's own UI keeps showing the
// picker row the user selected rather than the model that ran.
package explain

import (
	"fmt"
	"math"
	"strings"

	"github.com/djalmaaraujo/llm-router/internal/state"
)

const width = 33

func row(text string) string {
	r := []rune(text)
	if len(r) > width-2 {
		r = r[:width-2]
	}
	return "│ " + string(r) + strings.Repeat(" ", width-2-len(r)) + " │"
}

func wrapped(label, value string) []string {
	text := strings.Join(strings.Fields(label+value), " ")
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{row("")}
	}

	lines := []string{words[0]}
	for _, w := range words[1:] {
		last := lines[len(lines)-1]
		candidate := last + " " + w
		if len([]rune(candidate)) > width-2 {
			lines = append(lines, w)
		} else {
			lines[len(lines)-1] = candidate
		}
	}

	rows := make([]string, len(lines))
	for i, l := range lines {
		rows[i] = row(l)
	}
	return rows
}

func metric(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.2f", *v)
}

func confidence(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%d%%", int(math.Round(*v*100)))
}

// decisionWords names s.Reason for a turn with no cache arithmetic to show.
// cache-rebuild is checked ahead of the bare unavailable check: a reason can
// carry both (downgrade-not-worth-cache-rebuild+unavailable), and unavailable
// would otherwise match first and report the wrong cause.
func decisionWords(reason string) string {
	switch {
	case strings.Contains(reason, "override"):
		return "prompt override"
	case strings.Contains(reason, "jev-unavailable"):
		return "router unavailable; held"
	case strings.Contains(reason, "low-confidence-no-downgrade"):
		return "low confidence; held"
	case strings.Contains(reason, "low-confidence-capped"):
		return "low confidence; capped"
	case strings.Contains(reason, "big-context-low-confidence"):
		return "large context; needs more confidence"
	case strings.Contains(reason, "cache-rebuild"):
		return "router recommendation"
	case strings.Contains(reason, "unavailable"):
		return "nearest available tier"
	default:
		return "router recommendation"
	}
}

func sessionField(req any) map[string]any {
	m, ok := req.(map[string]any)
	if !ok {
		return nil
	}
	st, ok := m["state"].(map[string]any)
	if !ok {
		return nil
	}
	session, ok := st["session"].(map[string]any)
	if !ok {
		return nil
	}
	return session
}

func currentTier(session map[string]any) string {
	v, ok := session["current_model"].(string)
	if !ok || v == "" {
		return "unknown"
	}
	return strings.ToUpper(v)
}

func contextTokens(session map[string]any) string {
	// The status file went through encoding/json, so a numeric field
	// decodes as float64 even though it always holds a whole token count.
	v, ok := session["context_tokens"].(float64)
	if !ok {
		return "unknown"
	}
	return fmt.Sprintf("%d", int64(v))
}

// Render renders the report for the last recorded routing decision, or a
// one-line explanation when there is nothing to show.
func Render(s *state.Status) string {
	if s == nil {
		return "llm-router: no routing decision has been recorded for this session."
	}
	if s.Manual {
		return "llm-router: routing is paused because you selected a model manually."
	}

	session := sessionField(s.Request)

	var lines []string
	lines = append(lines, "┌"+strings.Repeat("─", width)+"┐")
	lines = append(lines, row("Jev request"))
	lines = append(lines, wrapped("Prompt: ", firstNonEmpty(s.Prompt, "not recorded"))...)
	lines = append(lines, row("Current tier: "+currentTier(session)))
	lines = append(lines, row("Context tokens: "+contextTokens(session)))
	lines = append(lines, row(""))
	lines = append(lines, row("Jev response"))

	var taskComplexity, reasoningRequired, toolComplexity, contextSize *float64
	if s.Metrics != nil {
		taskComplexity = s.Metrics.TaskComplexity
		reasoningRequired = s.Metrics.ReasoningRequired
		toolComplexity = s.Metrics.ToolComplexity
		contextSize = s.Metrics.ContextSize
	}
	lines = append(lines, row("Task complexity     "+metric(taskComplexity)))
	lines = append(lines, row("Reasoning required  "+metric(reasoningRequired)))
	lines = append(lines, row("Tool complexity     "+metric(toolComplexity)))
	lines = append(lines, row("Context size        "+metric(contextSize)))
	lines = append(lines, row(""))
	lines = append(lines, row("Selected model: "+strings.ToUpper(firstNonEmpty(s.Model, "unknown"))))
	lines = append(lines, row("Confidence: "+confidence(s.Confidence)))
	lines = append(lines, "└"+strings.Repeat("─", width)+"┘")

	// The arithmetic lines run well past the box's content width (a
	// dollar-and-turns breakdown does not fit in 31 columns), so this
	// block is plain text below the box rather than more boxed rows.
	if s.BreakEven > 0 {
		verb := "switched to"
		if strings.Contains(s.Reason, "cache-rebuild") {
			verb = "held on"
		}
		lines = append(lines, fmt.Sprintf("Decision: %s %s", verb, strings.ToUpper(firstNonEmpty(s.Tier, "unknown"))))
		lines = append(lines, fmt.Sprintf("  rebuild cost       $%.3f once", s.Rebuild))
		lines = append(lines, fmt.Sprintf("  saving             $%.3f per turn", s.SavingPerTurn))
		lines = append(lines, fmt.Sprintf("  pays off in        %.1f turns, horizon is %d", s.BreakEven, s.Horizon))
	} else {
		lines = append(lines, "Decision: "+decisionWords(s.Reason))
	}

	return strings.Join(lines, "\n")
}

func firstNonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
