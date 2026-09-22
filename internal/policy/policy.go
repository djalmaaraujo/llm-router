// Package policy turns a router answer into the tier that will actually run.
// It is pure and total: any missing, malformed, or unavailable input falls back
// to the tier already in use.
package policy

import (
	"regexp"

	"github.com/djalmaaraujo/llm-router/internal/config"
	"github.com/djalmaaraujo/llm-router/internal/cost"
)

type Answer struct {
	Choice     string
	Confidence float64
}

type Input struct {
	Prompt       string
	Jev          *Answer
	Current      string
	Available    []string
	CachedTokens int
	OutputTokens int
	SubAgent     bool
}

// Outcome carries the decision and the arithmetic behind it, so `explain` can
// show the numbers rather than a reason code.
type Outcome struct {
	Tier          string
	Target        string
	Reason        string
	Changed       bool
	BreakEven     float64
	Rebuild       float64
	SavingPerTurn float64
	Horizon       int
}

var overrides = []struct {
	tier string
	re   *regexp.Regexp
}{
	{"haiku", regexp.MustCompile(`(?i)\b(?:use|switch to)\s+(?:haiku|fast|luna)\b`)},
	{"sonnet", regexp.MustCompile(`(?i)\b(?:use|switch to)\s+(?:sonnet|balanced|terra)\b`)},
	{"opus", regexp.MustCompile(`(?i)\b(?:use|switch to)\s+(?:opus|strong|sol)\b`)},
	{"fable", regexp.MustCompile(`(?i)\b(?:use|switch to)\s+(?:fable|long|astra)\b`)},
}

// DetectOverride names the tier the user asked for in the prompt, or "".
func DetectOverride(prompt string) string {
	for _, o := range overrides {
		if o.re.MatchString(prompt) {
			return o.tier
		}
	}
	return ""
}

func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// clampToAvailable finds the nearest tier the account can run. It prefers
// stepping up rather than down, so a hard task is never handed to a weaker
// model, but it never steps up into fable, which bills extra credits.
func clampToAvailable(tier string, available []string) string {
	if contains(available, tier) {
		return tier
	}
	rank := config.RankOf(tier)
	names := config.TierNames()
	for i := rank + 1; i < len(names); i++ {
		if contains(available, names[i]) && names[i] != "fable" {
			return names[i]
		}
	}
	for i := rank - 1; i >= 0; i-- {
		if contains(available, names[i]) {
			return names[i]
		}
	}
	return ""
}

func Decide(in Input) Outcome {
	horizon := config.SwitchHorizon()
	if in.SubAgent {
		horizon = config.SubAgentHorizon()
	}
	out := in.OutputTokens
	if out <= 0 {
		out = config.Thresholds.AssumedOutputTokens
	}

	settle := func(tier, reason, routerTarget string) Outcome {
		final := clampToAvailable(tier, in.Available)
		if final == "" {
			final = in.Current
		}
		if final != tier {
			reason += "+unavailable"
		}
		if final == in.Current {
			reason += "/no-change"
		}
		return Outcome{Tier: final, Target: routerTarget, Reason: reason, Changed: final != in.Current, Horizon: horizon}
	}

	if override := DetectOverride(in.Prompt); override != "" {
		return settle(override, "override", "")
	}

	if in.Jev == nil || config.RankOf(in.Jev.Choice) < 0 {
		return settle(in.Current, "jev-unavailable", "")
	}

	target := in.Jev.Choice
	currentRank := config.RankOf(in.Current)
	targetRank := config.RankOf(target)

	if in.Jev.Confidence < config.Thresholds.MinConfidence {
		if targetRank < currentRank {
			return settle(in.Current, "low-confidence-no-downgrade", target)
		}
		ceiling := config.RankOf(config.Thresholds.UncertainCeiling)
		if currentRank > ceiling {
			ceiling = currentRank
		}
		if targetRank > ceiling {
			return settle(config.TierNames()[ceiling], "low-confidence-capped", target)
		}
	}

	// An upgrade is a capability decision, so cost never blocks it. The one
	// brake is confidence: past this much cached context a rebuild costs real
	// money in a single request, and a coin flip should not spend it.
	if targetRank > currentRank &&
		in.CachedTokens > config.Thresholds.BigContextTokens &&
		in.Jev.Confidence < config.Thresholds.BigContextMinConfidence {
		return settle(in.Current, "big-context-low-confidence", target)
	}

	if targetRank < currentRank {
		from := config.RatesFor(in.Current)
		to := config.RatesFor(target)
		turns, ok := cost.BreakEvenTurns(from, to, in.CachedTokens, out)
		if !ok || turns > float64(horizon) {
			held := settle(in.Current, "downgrade-not-worth-cache-rebuild", target)
			held.BreakEven = turns
			held.Rebuild, held.SavingPerTurn = rebuildAndSaving(from, to, in.CachedTokens, out)
			return held
		}
		switched := settle(target, "jev", target)
		switched.BreakEven = turns
		switched.Rebuild, switched.SavingPerTurn = rebuildAndSaving(from, to, in.CachedTokens, out)
		return switched
	}

	return settle(target, "jev", target)
}

// rebuildAndSaving reports the one-off premium for rebuilding the cache on `to`
// and what each later turn saves, both in dollars, for the explanation report.
func rebuildAndSaving(from, to cost.Rates, cachedTokens, outputTokens int) (float64, float64) {
	cached := float64(cachedTokens) / 1e6
	out := float64(outputTokens) / 1e6
	stay := cached*from.Read + out*from.Out
	first := cached*to.Write + out*to.Out
	later := cached*to.Read + out*to.Out
	return first - later, stay - later
}
