package config

import "testing"

func TestQuestionsForEmitsRelativeCostPerTier(t *testing.T) {
	models := []Model{
		{ID: "claude-haiku-4-5-20251001", Tier: "haiku", Description: "Haiku 4.5"},
		{ID: "claude-sonnet-5", Tier: "sonnet", Description: "Sonnet 5"},
		{ID: "claude-opus-5", Tier: "opus", Description: "Opus 5"},
		{ID: "claude-fable-5-1", Tier: "fable", Description: "Fable 5.1"},
		{ID: "gpt-5.6-luna", Tier: "", Description: "Unknown tier"},
	}
	want := map[string]string{
		"claude-haiku-4-5-20251001": "lowest",
		"claude-sonnet-5":           "low",
		"claude-opus-5":             "high",
		"claude-fable-5-1":          "highest",
	}

	questions := QuestionsFor(models)
	choice := questions["model"].(map[string]any)
	criteria := choice["criteria"].(map[string]any)

	for id, wantCost := range want {
		entry := criteria[id].(map[string]any)
		if got := entry["relative_cost"]; got != wantCost {
			t.Errorf("criteria[%q][relative_cost] = %v, want %q", id, got, wantCost)
		}
	}

	unknown := criteria["gpt-5.6-luna"].(map[string]any)
	if _, ok := unknown["relative_cost"]; ok {
		t.Errorf("criteria for an unknown tier must not carry a relative_cost key, got %v", unknown["relative_cost"])
	}
}
