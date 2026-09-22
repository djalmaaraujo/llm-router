package statusline

import (
	"strings"
	"testing"

	"github.com/djalmaaraujo/llm-router/internal/state"
)

func stdin(sessionID string) []byte {
	return []byte(`{"session_id":"` + sessionID + `","workspace":{"current_dir":"/home/me/my-project"},` +
		`"context_window":{"used_percentage":8.4},"model":{"display_name":"Opus 4.6"}}`)
}

// isolate keeps a test off the real machine. Render reads the user's own
// settings to wrap their status line, so without an isolated HOME and working
// directory a test would run whatever status line the developer has installed.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
}

func TestWaitsBeforeTheFirstPrompt(t *testing.T) {
	isolate(t)
	got := Render(stdin("s1"))
	if !strings.Contains(got, "waiting for first prompt") || !strings.Contains(got, "my-project") {
		t.Errorf("got %q", got)
	}
}

func TestShowsTheRoutedModelAndConfidence(t *testing.T) {
	isolate(t)
	c := 0.98
	state.Write("s1", state.Status{Tier: "haiku", Model: "claude-haiku-4-5-20251001", Reason: "jev", Confidence: &c})
	got := Render(stdin("s1"))
	for _, want := range []string{"claude-haiku-4-5-20251001", "p=0.98", "my-project", "8% context"} {
		if !strings.Contains(got, want) {
			t.Errorf("status line is missing %q: %q", want, got)
		}
	}
}

func TestNilConfidenceOmitsTheProbabilitySegment(t *testing.T) {
	isolate(t)
	state.Write("s1", state.Status{Tier: "haiku", Model: "claude-haiku-4-5-20251001", Reason: "jev", Confidence: nil})
	got := Render(stdin("s1"))
	if strings.Contains(got, "p=") {
		t.Errorf("nil confidence must render nothing for the p= segment, got %q", got)
	}
	if strings.Contains(got, "0.00") {
		t.Errorf("nil confidence must never render as 0.00, got %q", got)
	}
}

func TestNamesTheReasonOnlyWhenRoutingDeclined(t *testing.T) {
	isolate(t)
	state.Write("s1", state.Status{Tier: "opus", Model: "claude-opus-5", Reason: "jev"})
	if strings.Contains(Render(stdin("s1")), "(") && !strings.Contains(Render(stdin("s1")), "p=") {
		t.Error("the common case must stay short")
	}
	state.Write("s2", state.Status{Tier: "opus", Model: "claude-opus-5", Reason: "downgrade-not-worth-cache-rebuild/no-change"})
	if !strings.Contains(Render(stdin("s2")), "downgrade-not-worth-cache-rebuild") {
		t.Error("a held decision must say why")
	}
}

func TestShowsManualControl(t *testing.T) {
	isolate(t)
	state.Write("s1", state.Status{Manual: true})
	got := Render(stdin("s1"))
	if !strings.Contains(got, "manual") || !strings.Contains(got, "Opus 4.6") {
		t.Errorf("got %q", got)
	}
}

func TestSurvivesMalformedInput(t *testing.T) {
	isolate(t)
	if got := Render([]byte("not json")); got == "" {
		t.Error("a broken payload must still produce a usable line")
	}
}

func TestSurvivesAnEmptyPayload(t *testing.T) {
	isolate(t)
	if got := Render([]byte("{}")); got == "" {
		t.Error("an empty payload must still produce a usable line")
	}
}
