package claude

import (
	"encoding/json"
	"fmt"
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

func TestSanitizeSchemaConvertsDraft04Bounds(t *testing.T) {
	body := decode(t, `{"input_schema":{"properties":{"n":{"type":"number","minimum":5,"exclusiveMinimum":true}}}}`)
	SanitizeSchema(body["input_schema"])
	prop := body["input_schema"].(map[string]any)["properties"].(map[string]any)["n"].(map[string]any)
	if fmt.Sprint(prop["exclusiveMinimum"]) != "5" {
		t.Errorf("exclusiveMinimum = %v, want the numeric bound 5", prop["exclusiveMinimum"])
	}
	if _, still := prop["minimum"]; still {
		t.Error("minimum must be removed once it became exclusiveMinimum")
	}
}

func TestSanitizeSchemaDropsAFalseBound(t *testing.T) {
	body := decode(t, `{"s":{"maximum":9,"exclusiveMaximum":false}}`)
	SanitizeSchema(body["s"])
	s := body["s"].(map[string]any)
	if _, still := s["exclusiveMaximum"]; still {
		t.Error("a false boolean bound must be dropped entirely")
	}
	if fmt.Sprint(s["maximum"]) != "9" {
		t.Error("the plain bound must survive")
	}
}

func TestNewTurnPromptFindsATypedTurn(t *testing.T) {
	body := decode(t, `{"tools":[{"name":"Read"}],"messages":[
		{"role":"user","content":"first"},
		{"role":"assistant","content":"ok"},
		{"role":"user","content":[{"type":"text","text":"fix the parser"}]}]}`)
	if got := NewTurnPrompt(body); got != "fix the parser" {
		t.Errorf("got %q", got)
	}
}

func TestNewTurnPromptStripsSystemReminders(t *testing.T) {
	body := decode(t, `{"tools":[{"name":"Read"}],"messages":[{"role":"user","content":
		"<system-reminder>noise</system-reminder>  real prompt  "}]}`)
	if got := NewTurnPrompt(body); got != "real prompt" {
		t.Errorf("got %q, want the reminder stripped and trimmed", got)
	}
}

func TestNewTurnPromptIgnoresToolLoopContinuations(t *testing.T) {
	body := decode(t, `{"tools":[{"name":"Read"}],"messages":[{"role":"user","content":
		[{"type":"tool_result","tool_use_id":"x"}]}]}`)
	if got := NewTurnPrompt(body); got != "" {
		t.Errorf("got %q, want empty: a continuation is not a new turn", got)
	}
}

func TestNewTurnPromptSkipsATrailingHookSystemMessage(t *testing.T) {
	body := decode(t, `{"tools":[{"name":"Read"}],"messages":[
		{"role":"user","content":[{"type":"text","text":"fix the parser"}]},
		{"role":"system","content":[{"type":"text","text":"hook additionalContext"}]}]}`)
	if got := NewTurnPrompt(body); got != "fix the parser" {
		t.Errorf("got %q, want the trailing hook message skipped", got)
	}
}

func TestNewTurnPromptIgnoresAuxiliaryCalls(t *testing.T) {
	body := decode(t, `{"messages":[{"role":"user","content":"summarise this"}]}`)
	if got := NewTurnPrompt(body); got != "" {
		t.Errorf("got %q, want empty: no tools means an auxiliary call", got)
	}
}

func TestApplyTierStripsWhatHaikuCannotAccept(t *testing.T) {
	body := decode(t, `{"model":"jev-router","thinking":{"type":"adaptive"},
		"output_config":{"effort":"high"},
		"context_management":{"edits":[{"type":"clear_thinking_20251015"}]}}`)
	ApplyTier(body, "haiku", "claude-haiku-4-5-20251001")
	if body["model"] != "claude-haiku-4-5-20251001" {
		t.Errorf("model = %v", body["model"])
	}
	for _, gone := range []string{"thinking", "output_config", "context_management"} {
		if _, still := body[gone]; still {
			t.Errorf("%s must be removed for haiku", gone)
		}
	}
}

func TestApplyTierKeepsWhatOpusAccepts(t *testing.T) {
	body := decode(t, `{"model":"jev-router","thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`)
	ApplyTier(body, "opus", "claude-opus-5")
	if _, ok := body["thinking"]; !ok {
		t.Error("opus supports thinking")
	}
	if _, ok := body["output_config"]; !ok {
		t.Error("opus supports effort")
	}
}

func TestApplyTierKeepsANonThinkingEdit(t *testing.T) {
	body := decode(t, `{"model":"jev-router","thinking":{"type":"adaptive"},
		"context_management":{"edits":[{"type":"clear_thinking_20251015"},{"type":"clear_tool_uses_20250919"}]}}`)
	ApplyTier(body, "haiku", "claude-haiku-4-5-20251001")
	cm, ok := body["context_management"].(map[string]any)
	if !ok {
		t.Fatal("an edit that does not mention thinking must survive")
	}
	if len(cm["edits"].([]any)) != 1 {
		t.Errorf("edits = %v, want only the tool-uses edit", cm["edits"])
	}
}

func TestSessionOfReadsTheNestedJSONString(t *testing.T) {
	body := decode(t, `{"metadata":{"user_id":"{\"session_id\":\"abc-123\"}"}}`)
	if got := SessionOf(body); got != "abc-123" {
		t.Errorf("got %q", got)
	}
	if got := SessionOf(decode(t, `{"metadata":{"user_id":"not-json"}}`)); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := SessionOf(decode(t, `{}`)); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestConversationKeyIgnoresTheMovingCacheBreakpoint(t *testing.T) {
	a := decode(t, `{"metadata":{"user_id":"{\"session_id\":\"s\"}"},"messages":[{"role":"user","content":
		[{"type":"text","text":"do the thing"}]}]}`)
	b := decode(t, `{"metadata":{"user_id":"{\"session_id\":\"s\"}"},"messages":[{"role":"user","content":
		[{"type":"text","text":"do the thing","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`)
	if ConversationKey(a) != ConversationKey(b) {
		t.Error("Claude Code moves the breakpoint between requests; the key must not follow it")
	}
}

func TestConversationKeySeparatesSessionsAndSubAgents(t *testing.T) {
	main := decode(t, `{"metadata":{"user_id":"{\"session_id\":\"s\"}"},"messages":[{"role":"user","content":"hello"}]}`)
	sub := decode(t, `{"metadata":{"user_id":"{\"session_id\":\"s\"}"},"messages":[{"role":"user","content":"search for X"}]}`)
	other := decode(t, `{"metadata":{"user_id":"{\"session_id\":\"t\"}"},"messages":[{"role":"user","content":"hello"}]}`)
	if ConversationKey(main) == ConversationKey(sub) {
		t.Error("a sub-agent opens with different text and must get its own key")
	}
	if ConversationKey(main) == ConversationKey(other) {
		t.Error("the same opening text in two sessions must get two keys")
	}
}

func TestModelsFromFallsBackToStaticIDs(t *testing.T) {
	if got := ModelsFrom(nil); len(got) != 4 {
		t.Errorf("an empty catalog must fall back to the four static tiers, got %d", len(got))
	}
	catalog := []map[string]any{
		{"id": "claude-opus-5", "display_name": "Opus 5", "created_at": "2026-04-01T00:00:00Z"},
		{"id": "gpt-5.6-luna"},
	}
	got := ModelsFrom(catalog)
	if len(got) != 1 || got[0].Tier != "opus" {
		t.Fatalf("got %+v, want only the opus row", got)
	}
	if !strings.Contains(got[0].Description, "Opus 5") || !strings.Contains(got[0].Description, "2026-04-01") {
		t.Errorf("description = %q", got[0].Description)
	}
}

func TestNewTurnPromptAndConversationKeyToleratesMissingMessages(t *testing.T) {
	noMessages := decode(t, `{"tools":[{"name":"Read"}]}`)
	if got := NewTurnPrompt(noMessages); got != "" {
		t.Errorf("got %q, want empty when messages is absent", got)
	}
	if got := ConversationKey(noMessages); got == "" {
		t.Error("ConversationKey must still produce a key when messages is absent")
	}

	emptyMessages := decode(t, `{"tools":[{"name":"Read"}],"messages":[]}`)
	if got := NewTurnPrompt(emptyMessages); got != "" {
		t.Errorf("got %q, want empty when messages is empty", got)
	}
	if got := ConversationKey(emptyMessages); got == "" {
		t.Error("ConversationKey must still produce a key when messages is empty")
	}
}

func TestSessionOfHandlesUnexpectedJSONShapes(t *testing.T) {
	noSessionID := decode(t, `{"metadata":{"user_id":"{\"other\":\"x\"}"}}`)
	if got := SessionOf(noSessionID); got != "" {
		t.Errorf("got %q, want empty when session_id is absent", got)
	}

	jsonNumber := decode(t, `{"metadata":{"user_id":"42"}}`)
	if got := SessionOf(jsonNumber); got != "" {
		t.Errorf("got %q, want empty when user_id decodes to a JSON number", got)
	}
}

func TestModelsFromHandlesShortDatesAndLargeTokenCounts(t *testing.T) {
	catalog := []map[string]any{
		{"id": "claude-opus-5", "display_name": "Opus 5", "created_at": "2026", "max_input_tokens": json.Number("1000000")},
	}
	got := ModelsFrom(catalog)
	if len(got) != 1 {
		t.Fatalf("got %+v, want one model", got)
	}
	if strings.Contains(got[0].Description, "1e+06") {
		t.Errorf("description = %q, must not contain scientific notation", got[0].Description)
	}
	if !strings.Contains(got[0].Description, "1000000 input tokens") {
		t.Errorf("description = %q, want the plain token count", got[0].Description)
	}
	if !strings.Contains(got[0].Description, "released 2026") {
		t.Errorf("description = %q, want the short date used as-is", got[0].Description)
	}

	catalogFloat := []map[string]any{
		{"id": "claude-opus-5", "max_input_tokens": float64(1000000)},
	}
	got = ModelsFrom(catalogFloat)
	if strings.Contains(got[0].Description, "1e+06") {
		t.Errorf("description = %q, must not contain scientific notation", got[0].Description)
	}
}

func TestApplyTierToleratesMalformedShapes(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		tier  string
		check func(t *testing.T, body map[string]any)
	}{
		{
			name: "context_management with no edits key survives untouched",
			body: `{"model":"jev-router","thinking":{"type":"adaptive"},"context_management":{}}`,
			tier: "haiku",
			check: func(t *testing.T, body map[string]any) {
				cm, ok := body["context_management"].(map[string]any)
				if !ok {
					t.Fatal("context_management must survive when it has no edits key")
				}
				if len(cm) != 0 {
					t.Errorf("context_management = %v, want unchanged (empty)", cm)
				}
			},
		},
		{
			name: "edits present but not a slice survives untouched",
			body: `{"model":"jev-router","thinking":{"type":"adaptive"},"context_management":{"edits":"nope"}}`,
			tier: "haiku",
			check: func(t *testing.T, body map[string]any) {
				cm, ok := body["context_management"].(map[string]any)
				if !ok {
					t.Fatal("context_management must survive when edits is not a slice")
				}
				if cm["edits"] != "nope" {
					t.Errorf("edits = %v, want left untouched", cm["edits"])
				}
			},
		},
		{
			name: "edits is a slice of non-maps: none look like thinking edits, all kept",
			body: `{"model":"jev-router","thinking":{"type":"adaptive"},"context_management":{"edits":[1,2,3]}}`,
			tier: "haiku",
			check: func(t *testing.T, body map[string]any) {
				cm, ok := body["context_management"].(map[string]any)
				if !ok {
					t.Fatal("context_management must survive when its edits are not maps")
				}
				edits, ok := cm["edits"].([]any)
				if !ok || len(edits) != 3 {
					t.Errorf("edits = %v, want all 3 non-map entries kept, none dropped", cm["edits"])
				}
			},
		},
		{
			name: "output_config with no effort key but other fields survives",
			body: `{"model":"jev-router","output_config":{"foo":"bar"}}`,
			tier: "haiku",
			check: func(t *testing.T, body map[string]any) {
				oc, ok := body["output_config"].(map[string]any)
				if !ok {
					t.Fatal("output_config must survive when it has fields besides effort")
				}
				if oc["foo"] != "bar" {
					t.Errorf("output_config = %v, want foo untouched", oc)
				}
			},
		},
		{
			name: "an unknown tier leaves the body completely untouched",
			body: `{"model":"jev-router","thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`,
			tier: "nonexistent-tier",
			check: func(t *testing.T, body map[string]any) {
				if body["model"] != "jev-router" {
					t.Errorf("model = %v, want left as jev-router for an unknown tier", body["model"])
				}
				if _, ok := body["thinking"]; !ok {
					t.Error("thinking must survive for an unknown tier")
				}
				if _, ok := body["output_config"]; !ok {
					t.Error("output_config must survive for an unknown tier")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := decode(t, tc.body)
			ApplyTier(body, tc.tier, "some-model-id")
			tc.check(t, body)
		})
	}
}

func TestNewTurnPromptStripsTwoSeparateReminderSpans(t *testing.T) {
	body := decode(t, `{"tools":[{"name":"Read"}],"messages":[{"role":"user","content":
		"<system-reminder>first noise</system-reminder>keep this<system-reminder>second noise</system-reminder>"}]}`)
	if got := NewTurnPrompt(body); got != "keep this" {
		t.Errorf("got %q, want both reminder spans stripped", got)
	}
}

func TestNewTurnPromptStripsAReminderSpanningMultipleLines(t *testing.T) {
	body := map[string]any{
		"tools": []any{map[string]any{"name": "Read"}},
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "<system-reminder>line one\nline two\nline three</system-reminder>real prompt",
			},
		},
	}
	if got := NewTurnPrompt(body); got != "real prompt" {
		t.Errorf("got %q, want the multi-line reminder stripped completely", got)
	}
}

func TestNewTurnPromptPinsUnclosedReminderTagBehaviour(t *testing.T) {
	body := map[string]any{
		"tools": []any{map[string]any{"name": "Read"}},
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "  <system-reminder>never closed  ",
			},
		},
	}
	// The regexp requires a closing tag, so an unclosed opening tag does not
	// match and is left in the output verbatim; only leading/trailing
	// whitespace around it is trimmed. Pinning this so a change is deliberate.
	if got := NewTurnPrompt(body); got != "<system-reminder>never closed" {
		t.Errorf("got %q, want the unclosed tag left in place, only trimmed", got)
	}
}

func TestNewTurnPromptReturnsEmptyForNonStringNonArrayContent(t *testing.T) {
	body := map[string]any{
		"tools": []any{map[string]any{"name": "Read"}},
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": 42,
			},
		},
	}
	if got := NewTurnPrompt(body); got != "" {
		t.Errorf("got %q, want empty for content that is neither a string nor an array", got)
	}
}

func TestSessionOfHandlesMetadataAndSessionIDShapeMismatches(t *testing.T) {
	notAMap := decode(t, `{"metadata":"not-a-map"}`)
	if got := SessionOf(notAMap); got != "" {
		t.Errorf("got %q, want empty when metadata is not a map", got)
	}

	sessionIDIsNumber := decode(t, `{"metadata":{"user_id":"{\"session_id\":42}"}}`)
	if got := SessionOf(sessionIDIsNumber); got != "" {
		t.Errorf("got %q, want empty when session_id is a JSON number rather than a string", got)
	}
}

func TestSanitizeSchemaRecursesDeeplyAndToleratesNil(t *testing.T) {
	SanitizeSchema(nil)

	body := decode(t, `{"a":{"b":[{"c":{"d":{"minimum":1,"exclusiveMinimum":true}}}]}}`)
	SanitizeSchema(body)
	d := body["a"].(map[string]any)["b"].([]any)[0].(map[string]any)["c"].(map[string]any)["d"].(map[string]any)
	if fmt.Sprint(d["exclusiveMinimum"]) != "1" {
		t.Errorf("exclusiveMinimum = %v, want the numeric bound 1 four levels down", d["exclusiveMinimum"])
	}
	if _, still := d["minimum"]; still {
		t.Error("minimum must be removed four levels down too")
	}
}
