// Package config holds every routing knob in one place, so the whole policy is
// reviewable without reading the proxy.
package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/djalmaaraujo/llm-router/internal/cost"
)

// Tier is one model tier. Prices are US dollars per million tokens. CacheRead is
// the multiplier applied to CostIn for tokens served from the prompt cache;
// fable reads at $0.25 per million, which is 0.025x rather than the usual 0.1x.
type Tier struct {
	Name      string
	ID        string
	Family    string
	Thinking  bool
	Effort    bool
	CostIn    float64
	CostOut   float64
	CacheRead float64
}

// Tiers are ordered cheapest first. Family is the substring used to recognise an
// older version within the same tier, such as claude-sonnet-4-6.
var Tiers = []Tier{
	{"haiku", "claude-haiku-4-5-20251001", "haiku", false, false, 1, 5, 0.1},
	{"sonnet", "claude-sonnet-5", "sonnet", true, true, 2, 10, 0.1},
	{"opus", "claude-opus-5", "opus", true, true, 5, 25, 0.1},
	{"fable", "claude-fable-5-1", "fable", true, true, 10, 50, 0.025},
}

// CacheWrite1h is the multiplier on CostIn for writing the cache. Claude Code
// uses the one-hour TTL, which costs 2x rather than the five-minute 1.25x.
const CacheWrite1h = 2.0

// AutoModel is the sentinel offered as an extra row in Claude Code's /model
// picker. Claude Code sends it verbatim behind a custom base URL, so seeing it
// in a request is an exact signal that the turn should be routed.
const AutoModel = "jev-router"

func IsAuto(model string) bool { return model == AutoModel }

func TierNames() []string {
	names := make([]string, len(Tiers))
	for i, tier := range Tiers {
		names[i] = tier.Name
	}
	return names
}

func RankOf(name string) int {
	for i, tier := range Tiers {
		if tier.Name == name {
			return i
		}
	}
	return -1
}

func Spec(name string) (Tier, bool) {
	for _, tier := range Tiers {
		if tier.Name == name {
			return tier, true
		}
	}
	return Tier{}, false
}

func IDOf(name string) string {
	tier, ok := Spec(name)
	if !ok {
		return ""
	}
	return tier.ID
}

// TierOf names the tier for a model string, or "" when it is not one of ours.
func TierOf(model string) string {
	for _, tier := range Tiers {
		if strings.Contains(model, tier.Family) {
			return tier.Name
		}
	}
	return ""
}

// AvailableTiers lists what the account can run. Fable bills extra usage
// credits, so it is opt-in; everything else is covered by a subscription.
func AvailableTiers() []string {
	allowFable := Env("ALLOW_FABLE") == "1"
	var names []string
	for _, tier := range Tiers {
		if tier.Name == "fable" && !allowFable {
			continue
		}
		names = append(names, tier.Name)
	}
	return names
}

type thresholds struct {
	MinConfidence           float64
	UncertainCeiling        string
	BigContextTokens        int
	BigContextMinConfidence float64
	AssumedOutputTokens     int
	JevTimeoutMS            int
	JevDeadlineMS           int
	JevMaxRetries           int
}

// Thresholds are the tuning knobs. The switch and sub-agent horizons live in
// SwitchHorizon and SubAgentHorizon instead of this struct, because main()
// loads the env files after package initialisation runs.
var Thresholds = thresholds{
	MinConfidence:           0.3,
	UncertainCeiling:        "sonnet",
	BigContextTokens:        200_000,
	BigContextMinConfidence: 0.6,
	AssumedOutputTokens:     2000,
	JevTimeoutMS:            1500,
	JevDeadlineMS:           3000,
	JevMaxRetries:           1,
}

// SwitchHorizon is how many more turns a conversation is assumed to keep
// reusing its cached prefix. Read per call rather than at init, because the
// env files load after package initialisation.
func SwitchHorizon() int { return envInt("SWITCH_HORIZON", 5) }

// SubAgentHorizon is the same for a sub-agent, whose context is short-lived.
func SubAgentHorizon() int { return envInt("SUBAGENT_HORIZON", 2) }

func envInt(name string, fallback int) int {
	if n, err := strconv.Atoi(Env(name)); err == nil && n > 0 {
		return n
	}
	return fallback
}

// Env reads LLMR_<name>, falling back to the legacy JEV_<name> so an existing
// setup keeps working after the rename.
func Env(name string) string {
	if v := os.Getenv("LLMR_" + name); v != "" {
		return v
	}
	return os.Getenv("JEV_" + name)
}

// RatesFor turns a tier's list prices into the dollars-per-million-token rates
// the cost package works in.
func RatesFor(name string) cost.Rates {
	tier, ok := Spec(name)
	if !ok {
		return cost.Rates{}
	}
	return cost.Rates{
		Read:  tier.CostIn * tier.CacheRead,
		Write: tier.CostIn * CacheWrite1h,
		Out:   tier.CostOut,
	}
}
