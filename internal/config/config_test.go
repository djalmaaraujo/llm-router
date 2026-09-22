package config

import "testing"

func TestTierOfMatchesVersionsWithinAFamily(t *testing.T) {
	cases := map[string]string{
		"claude-haiku-4-5-20251001": "haiku",
		"claude-sonnet-4-6":         "sonnet",
		"claude-opus-5":             "opus",
		"claude-fable-5-1":          "fable",
		"gpt-5.6-luna":              "",
		"jev-router":                "",
	}
	for model, want := range cases {
		if got := TierOf(model); got != want {
			t.Errorf("TierOf(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestRankOfOrdersTiersCheapestFirst(t *testing.T) {
	if RankOf("haiku") >= RankOf("sonnet") || RankOf("sonnet") >= RankOf("opus") || RankOf("opus") >= RankOf("fable") {
		t.Fatal("tiers are not ordered cheapest first")
	}
	if RankOf("nope") != -1 {
		t.Error("an unknown tier must rank -1")
	}
}

func TestFableIsOptInOnly(t *testing.T) {
	t.Setenv("LLMR_ALLOW_FABLE", "")
	t.Setenv("JEV_ALLOW_FABLE", "")
	for _, name := range AvailableTiers() {
		if name == "fable" {
			t.Fatal("fable must not be available without an opt-in")
		}
	}
	t.Setenv("LLMR_ALLOW_FABLE", "1")
	found := false
	for _, name := range AvailableTiers() {
		if name == "fable" {
			found = true
		}
	}
	if !found {
		t.Error("LLMR_ALLOW_FABLE=1 must make fable available")
	}
}

func TestPricesMatchTheSpec(t *testing.T) {
	want := map[string][3]float64{
		"haiku":  {1, 5, 0.1},
		"sonnet": {2, 10, 0.1},
		"opus":   {5, 25, 0.1},
		"fable":  {10, 50, 0.025},
	}
	for _, tier := range Tiers {
		w := want[tier.Name]
		if tier.CostIn != w[0] || tier.CostOut != w[1] || tier.CacheRead != w[2] {
			t.Errorf("%s prices = %v/%v/%v, want %v", tier.Name, tier.CostIn, tier.CostOut, tier.CacheRead, w)
		}
	}
}

func TestHorizonsAreReadPerCallNotAtInit(t *testing.T) {
	t.Setenv("LLMR_SWITCH_HORIZON", "")
	t.Setenv("JEV_SWITCH_HORIZON", "")
	if got := SwitchHorizon(); got != 5 {
		t.Errorf("SwitchHorizon = %d, want the default 5", got)
	}
	t.Setenv("LLMR_SWITCH_HORIZON", "9")
	if got := SwitchHorizon(); got != 9 {
		t.Errorf("SwitchHorizon = %d, want 9: env files load after package init", got)
	}
	t.Setenv("LLMR_SUBAGENT_HORIZON", "3")
	if got := SubAgentHorizon(); got != 3 {
		t.Errorf("SubAgentHorizon = %d, want 3", got)
	}
}
