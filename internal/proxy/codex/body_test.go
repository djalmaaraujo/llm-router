package codex

import (
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var out map[string]any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNewTurnPromptNeedsTheToolsItem(t *testing.T) {
	without := decode(t, `{"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	if got := NewTurnPrompt(without); got != "" {
		t.Errorf("got %q, want empty: no additional_tools means an auxiliary call", got)
	}
	with := decode(t, `{"input":[{"type":"additional_tools"},
		{"role":"user","content":[{"type":"input_text","text":"fix the parser"}]}]}`)
	if got := NewTurnPrompt(with); got != "fix the parser" {
		t.Errorf("got %q", got)
	}
}

func TestNewTurnPromptStopsAtAToolOutput(t *testing.T) {
	body := decode(t, `{"input":[{"type":"additional_tools"},
		{"role":"user","content":[{"type":"input_text","text":"start"}]},
		{"type":"function_call_output","output":"done"}]}`)
	if got := NewTurnPrompt(body); got != "" {
		t.Errorf("got %q, want empty: a continuation is not a new turn", got)
	}
}

func TestConversationKeyPrefersThePromptCacheKey(t *testing.T) {
	main := decode(t, `{"prompt_cache_key":"main","input":[]}`)
	sub := decode(t, `{"prompt_cache_key":"sub-agent","input":[]}`)
	if ConversationKey(main) == ConversationKey(sub) {
		t.Error("Codex names the conversation itself; the key must follow it")
	}
	if ConversationKey(main) != ConversationKey(decode(t, `{"prompt_cache_key":"main","input":[{"x":1}]}`)) {
		t.Error("the key must not move when the input grows")
	}
}

func TestAddJevModelAddsExactlyOneRow(t *testing.T) {
	catalog := decode(t, `{"models":[{"slug":"gpt-5.6-terra","visibility":"list"}]}`)
	once := AddJevModel(catalog)
	twice := AddJevModel(once)
	rows := twice["models"].([]any)
	seen := 0
	for _, r := range rows {
		if r.(map[string]any)["slug"] == CodexAutoModel {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("found %d sentinel rows, want exactly 1", seen)
	}
}

func TestModelForReadsTheOverrides(t *testing.T) {
	t.Setenv("LLMR_CODEX_FAST_MODEL", "")
	t.Setenv("JEV_CODEX_FAST_MODEL", "")
	if got := ModelFor("haiku"); got != "gpt-5.6-luna" {
		t.Errorf("got %q, want the default fast model", got)
	}
	t.Setenv("LLMR_CODEX_FAST_MODEL", "my-fast")
	if got := ModelFor("haiku"); got != "my-fast" {
		t.Errorf("got %q, want the override", got)
	}
}

// A CLI release could tack on a trailing item after the real user turn that
// is neither a tool output nor a user message — a reasoning summary, a
// developer-role note. This mirrors the Claude-side bug where a trailing
// system-role message defeated routing outright. NewTurnPrompt must see past
// it to the real turn underneath, not treat it as disqualifying.
func TestNewTurnPromptSeesPastATrailingNonUserNonToolOutputItem(t *testing.T) {
	body := decode(t, `{"input":[{"type":"additional_tools"},
		{"role":"user","content":[{"type":"input_text","text":"fix the parser"}]},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking..."}]}]}`)
	if got := NewTurnPrompt(body); got != "fix the parser" {
		t.Errorf("got %q, want the trailing reasoning item skipped so the real turn is found", got)
	}
}

func TestNewTurnPromptTreatsCodexAuxiliaryPromptsAsNotATurn(t *testing.T) {
	body := decode(t, `{"input":[{"type":"additional_tools"},
		{"role":"user","content":[{"type":"input_text","text":"<user_instructions>be nice</user_instructions>"}]}]}`)
	if got := NewTurnPrompt(body); got != "" {
		t.Errorf("got %q, want empty: this is one of Codex's own auxiliary prompts", got)
	}
}
