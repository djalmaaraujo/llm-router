package explain

import (
	"strings"
	"testing"

	"github.com/djalmaaraujo/llm-router/internal/router"
	"github.com/djalmaaraujo/llm-router/internal/state"
)

func f(v float64) *float64 {
	return &v
}

func TestReportsWhenNothingHasBeenRouted(t *testing.T) {
	if got := Render(nil); got != "llm-router: no routing decision has been recorded for this session." {
		t.Errorf("got %q", got)
	}
}

func TestReportsManualControl(t *testing.T) {
	if got := Render(&state.Status{Manual: true}); got != "llm-router: routing is paused because you selected a model manually." {
		t.Errorf("got %q", got)
	}
}

func TestShowsTheScoresAndTheSelectedModel(t *testing.T) {
	c := 0.94
	got := Render(&state.Status{
		Tier: "sonnet", Model: "claude-sonnet-5", Prompt: "explain the router",
		Reason: "jev", Confidence: &c,
		Metrics: &router.Metrics{
			TaskComplexity:    f(0.82),
			ReasoningRequired: f(0.91),
			ToolComplexity:    f(0.64),
			ContextSize:       f(0.31),
		},
	})
	for _, want := range []string{"0.82", "0.91", "0.64", "0.31", "94%", "CLAUDE-SONNET-5", "explain the router"} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
}

func TestShowsTheCacheArithmeticWhenARebuildWasWeighed(t *testing.T) {
	got := Render(&state.Status{
		Tier:          "sonnet",
		Model:         "claude-sonnet-5",
		Reason:        "downgrade-not-worth-cache-rebuild/no-change",
		Rebuild:       0.19,
		SavingPerTurn: 0.02,
		BreakEven:     9.5,
		Horizon:       5,
	})
	for _, want := range []string{"$0.190", "$0.020", "9.5", "horizon is 5"} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "cache rebuild avoided") {
		t.Error("the report must show the arithmetic, not a reason code")
	}
}

func TestNilMetricRendersNotAvailableWhileSiblingsStillRender(t *testing.T) {
	got := Render(&state.Status{
		Tier: "sonnet", Model: "claude-sonnet-5",
		Reason: "jev",
		Metrics: &router.Metrics{
			TaskComplexity:    nil,
			ReasoningRequired: f(0.91),
			ToolComplexity:    f(0.64),
			ContextSize:       f(0.31),
		},
	})
	if !strings.Contains(got, "n/a") {
		t.Errorf("report is missing n/a for the unreported metric:\n%s", got)
	}
	for _, want := range []string{"0.91", "0.64", "0.31"} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "0.00") {
		t.Errorf("a nil metric must never render as 0.00:\n%s", got)
	}
}

func TestNilMetricsAsAWholeRendersAllFourAsNotAvailable(t *testing.T) {
	got := Render(&state.Status{Tier: "sonnet", Model: "claude-sonnet-5", Reason: "jev", Metrics: nil})
	if strings.Count(got, "n/a") < 4 {
		t.Errorf("expected all four metrics to render n/a:\n%s", got)
	}
	if strings.Contains(got, "0.00") {
		t.Errorf("a nil Metrics pointer must never render as 0.00:\n%s", got)
	}
}

func TestNilConfidenceRendersNotAvailable(t *testing.T) {
	got := Render(&state.Status{Tier: "sonnet", Model: "claude-sonnet-5", Reason: "jev", Confidence: nil})
	if !strings.Contains(got, "Confidence: n/a") {
		t.Errorf("report is missing Confidence: n/a:\n%s", got)
	}
	if strings.Contains(got, "0%") {
		t.Errorf("a nil confidence must never render as 0%%:\n%s", got)
	}
}

func TestReasonOrderingPutsCacheRebuildBeforeBareUnavailable(t *testing.T) {
	got := Render(&state.Status{
		Tier:   "sonnet",
		Model:  "claude-sonnet-5",
		Reason: "downgrade-not-worth-cache-rebuild+unavailable",
	})
	if strings.Contains(got, "nearest available tier") {
		t.Errorf("cache-rebuild must be checked before the bare unavailable check:\n%s", got)
	}
}

func TestSurvivesAHostileStatus(t *testing.T) {
	got := Render(&state.Status{
		Tier:       "sonnet",
		Model:      "claude-sonnet-5",
		Prompt:     "explain " + strings.Repeat("x", 200),
		Reason:     "",
		Confidence: nil,
		Metrics:    nil,
		Request:    "not a map",
	})
	if got == "" {
		t.Error("expected a usable box, got empty string")
	}
	if !strings.Contains(got, "unknown") {
		t.Errorf("expected unknown fields to render as unknown:\n%s", got)
	}
	if !strings.Contains(got, "n/a") {
		t.Errorf("expected metrics to render as n/a:\n%s", got)
	}
}
