package config

// Model is one exact model the signed-in account can run. Sending the real
// catalog rather than tier names keeps model versions apart, so
// claude-opus-4-8 and claude-opus-5 stay separate choices.
type Model struct {
	ID          string
	Tier        string
	Description string
}

var complexityScale = []string{
	"None", "Very low", "Low", "Some", "Moderate",
	"Moderate to high", "High", "Very high", "Severe", "Extreme",
}

// ComplexityMaxScore normalises a raw score to 0..1 for the report.
const ComplexityMaxScore = 9

var guidance = map[string]map[string]any{
	"haiku": {
		"what":    "Trivial, mechanical, or purely factual work.",
		"signals": []string{"Rename, reformat, comment, or run one obvious command"},
		"not_for": "Design judgement or multi-file reasoning.",
	},
	"sonnet": {
		"what":    "Ordinary day-to-day engineering with a clear, bounded shape.",
		"signals": []string{"Implement a specified function, test existing behaviour, or fix an understood local bug"},
		"not_for": "Open-ended architecture, subtle concurrency, or unknown-cause debugging.",
	},
	"opus": {
		"what":    "Hard reasoning, ambiguity, or high blast radius.",
		"signals": []string{"Unknown-cause debugging, cross-module design, security, auth, concurrency, or migrations"},
		"not_for": "Routine work with a clear implementation.",
	},
	"fable": {
		"what":    "Very large or long-running work beyond a normal focused session.",
		"signals": []string{"Whole-repo migration, unusually large context, or multi-hour autonomous execution"},
		"not_for": "Anything a strong model can finish in one focused session.",
	},
}

// relativeCost ranks a tier's price in words rather than numbers. Jev's
// published limitations say it cannot reliably do arithmetic or judge numeric
// proximity, so a number here would ask it to do the one thing it is
// documented to fail at.
var relativeCost = map[string]string{
	"haiku":  "lowest",
	"sonnet": "low",
	"opus":   "high",
	"fable":  "highest",
}

func scoreQuestion(instructions string) map[string]any {
	return map[string]any{"type": "score", "instructions": instructions, "criteria": complexityScale}
}

// QuestionsFor builds the question set, including the model choice assembled
// from the exact catalog this account reported.
func QuestionsFor(models []Model) map[string]any {
	criteria := map[string]any{}
	for _, m := range models {
		entry := map[string]any{"model": m.Description}
		if cost, ok := relativeCost[m.Tier]; ok {
			entry["relative_cost"] = cost
		}
		for k, v := range guidance[m.Tier] {
			entry[k] = v
		}
		criteria[m.ID] = entry
	}
	return map[string]any{
		"task_complexity":    scoreQuestion("How complex is the coding task overall, including ambiguity, scope, and blast radius?"),
		"reasoning_required": scoreQuestion("How much reasoning is required to complete the request correctly in one pass?"),
		"tool_complexity":    scoreQuestion("How complex is the tool use required, from no tools to many coordinated or stateful operations?"),
		"model": map[string]any{
			"type": "choice",
			"instructions": []string{
				"Pick the cheapest exact model that can fully complete this coding request in one pass, without retrying on a stronger model.",
				"Treat different model versions as separate choices. Judge required reasoning, not requested reply length.",
			},
			"criteria": criteria,
		},
	}
}

// ContextWindowTokens normalises context size to 0..1 for the report.
const ContextWindowTokens = 200000
