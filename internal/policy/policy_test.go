package policy

import (
	"strings"
	"testing"
)

var all = []string{"haiku", "sonnet", "opus", "fable"}

func sure(choice string) *Answer   { return &Answer{Choice: choice, Confidence: 0.95} }
func unsure(choice string) *Answer { return &Answer{Choice: choice, Confidence: 0.2} }

func base() Input {
	return Input{
		Prompt:       "refactor the parser",
		Current:      "sonnet",
		Available:    all,
		CachedTokens: 0,
		OutputTokens: 2000,
	}
}

func TestFollowsAConfidentAnswer(t *testing.T) {
	in := base()
	in.Jev = sure("opus")
	out := Decide(in)
	if out.Tier != "opus" || out.Reason != "jev" || !out.Changed {
		t.Errorf("got %+v, want opus/jev/changed", out)
	}
}

func TestAnExplicitOverrideBeatsTheRouter(t *testing.T) {
	in := base()
	in.Prompt = "use haiku to fix this typo"
	in.Jev = sure("opus")
	if out := Decide(in); out.Tier != "haiku" || out.Reason != "override" {
		t.Errorf("got %+v, want haiku/override", out)
	}
}

func TestDetectOverrideOnlyFiresOnARealInstruction(t *testing.T) {
	cases := map[string]string{
		"switch to opus":         "opus",
		"use luna":               "haiku",
		"use strong":             "opus",
		"the opus of his career": "",
	}
	for prompt, want := range cases {
		if got := DetectOverride(prompt); got != want {
			t.Errorf("DetectOverride(%q) = %q, want %q", prompt, got, want)
		}
	}
}

func TestKeepsTheCurrentModelWhenTheRouterIsUnreachable(t *testing.T) {
	in := base()
	in.Jev = nil
	out := Decide(in)
	if out.Tier != "sonnet" || out.Changed {
		t.Errorf("got %+v, want sonnet unchanged", out)
	}
	if !strings.Contains(out.Reason, "jev-unavailable") {
		t.Errorf("reason = %q, want it to mention jev-unavailable", out.Reason)
	}
}

func TestIgnoresATierTheRouterInvented(t *testing.T) {
	in := base()
	in.Jev = sure("gpt-9")
	if out := Decide(in); out.Tier != "sonnet" {
		t.Errorf("got %q, want sonnet", out.Tier)
	}
}

func TestNeverDowngradesOnALowConfidenceAnswer(t *testing.T) {
	in := base()
	in.Jev = unsure("haiku")
	out := Decide(in)
	if out.Tier != "sonnet" || !strings.Contains(out.Reason, "low-confidence-no-downgrade") {
		t.Errorf("got %+v, want sonnet held", out)
	}
}

func TestCapsALowConfidenceUpgradeAtTheSafeCeiling(t *testing.T) {
	in := base()
	in.Current = "haiku"
	in.Jev = unsure("fable")
	if out := Decide(in); out.Tier != "sonnet" || out.Reason != "low-confidence-capped" {
		t.Errorf("got %+v, want sonnet/low-confidence-capped", out)
	}
}

func TestAllowsTheDowngradeThatPaysForItself(t *testing.T) {
	in := base()
	in.Current = "opus"
	in.Jev = sure("haiku")
	in.CachedTokens = 100_000
	out := Decide(in)
	if out.Tier != "haiku" {
		t.Errorf("opus to haiku at 100k pays off in 2.4 turns, got %+v", out)
	}
	if out.BreakEven < 2.3 || out.BreakEven > 2.5 {
		t.Errorf("BreakEven = %.2f, want about 2.4", out.BreakEven)
	}
}

func TestRefusesTheDowngradeThatDoesNotPayForItself(t *testing.T) {
	in := base()
	in.Jev = sure("haiku")
	in.CachedTokens = 100_000
	out := Decide(in)
	if out.Tier != "sonnet" {
		t.Errorf("sonnet to haiku at 100k needs 9.5 turns, got %+v", out)
	}
	if !strings.Contains(out.Reason, "cache-rebuild") {
		t.Errorf("reason = %q, want it to mention cache-rebuild", out.Reason)
	}
}

func TestASubAgentSwitchesFreelyBecauseItsCacheIsCheap(t *testing.T) {
	in := base()
	in.Jev = sure("haiku")
	in.CachedTokens = 8000
	in.SubAgent = true
	if out := Decide(in); out.Tier != "haiku" {
		t.Errorf("a sub-agent with 8k cached should switch, got %+v", out)
	}
}

func TestAnUpgradeIsNeverBlockedOnCost(t *testing.T) {
	in := base()
	in.Current = "haiku"
	in.Jev = sure("opus")
	in.CachedTokens = 500_000
	if out := Decide(in); out.Tier != "opus" {
		t.Errorf("an upgrade is a capability decision, got %+v", out)
	}
}

func TestABigContextUpgradeNeedsRealConfidence(t *testing.T) {
	in := base()
	in.Jev = &Answer{Choice: "opus", Confidence: 0.4}
	in.CachedTokens = 500_000
	out := Decide(in)
	if out.Tier != "sonnet" || !strings.Contains(out.Reason, "big-context-low-confidence") {
		t.Errorf("got %+v, want sonnet held on a coin-flip upgrade", out)
	}
	in.Jev = &Answer{Choice: "opus", Confidence: 0.9}
	if out := Decide(in); out.Tier != "opus" {
		t.Errorf("a confident upgrade must still go through, got %+v", out)
	}
}

func TestSubstitutesUpwardWhenTheChosenTierIsUnavailable(t *testing.T) {
	in := base()
	in.Current = "haiku"
	in.Available = []string{"haiku", "opus"}
	in.Jev = sure("sonnet")
	out := Decide(in)
	if out.Tier != "opus" || !strings.Contains(out.Reason, "unavailable") {
		t.Errorf("got %+v, want opus/unavailable", out)
	}
}

func TestNeverSubstitutesUpwardIntoPaidFable(t *testing.T) {
	in := base()
	in.Current = "haiku"
	in.Available = []string{"haiku", "fable"}
	in.Jev = sure("opus")
	if out := Decide(in); out.Tier != "haiku" {
		t.Errorf("got %q, want haiku", out.Tier)
	}
}
