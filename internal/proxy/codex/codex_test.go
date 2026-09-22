package codex

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/djalmaaraujo/llm-router/internal/config"
	"github.com/djalmaaraujo/llm-router/internal/proxy"
	"github.com/djalmaaraujo/llm-router/internal/router"
	"github.com/djalmaaraujo/llm-router/internal/state"
)

type fakeRouter struct {
	choice string
	conf   float64
	err    error
	calls  int
}

func (f *fakeRouter) Route(context.Context, router.Input) (*router.Decision, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &router.Decision{Choice: f.choice, Confidence: f.conf}, nil
}

func body(t *testing.T, s string) map[string]any {
	t.Helper()
	var out map[string]any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

const turn = `{"model":"jev-router","prompt_cache_key":"s1","input":[
	{"type":"additional_tools"},
	{"role":"user","content":[{"type":"input_text","text":"%s"}]}]}`

func TestRoutesAFreshCodexTurn(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "gpt-5.6-sol", conf: 0.91})
	b := body(t, strings.Replace(turn, "%s", "design the schema", 1))
	h.Hooks().RewriteRequest("/responses", b)
	if b["model"] != "gpt-5.6-sol" {
		t.Errorf("model = %v, want the sentinel replaced with the router's pick", b["model"])
	}
}

func TestPassesThroughAModelTheUserPicked(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	r := &fakeRouter{choice: "gpt-5.6-sol", conf: 0.91}
	h := New(r)
	b := body(t, `{"model":"gpt-5.6-terra","prompt_cache_key":"s1","input":[
		{"type":"additional_tools"},
		{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	h.Hooks().RewriteRequest("/responses", b)
	if b["model"] != "gpt-5.6-terra" {
		t.Errorf("model = %v, want the user's choice untouched", b["model"])
	}
	if r.calls != 0 {
		t.Error("an explicit choice must not consult the router")
	}
}

func TestDoesNotRouteAContinuation(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	r := &fakeRouter{choice: "gpt-5.6-sol", conf: 0.91}
	h := New(r)
	first := body(t, strings.Replace(turn, "%s", "start the work", 1))
	h.Hooks().RewriteRequest("/responses", first)

	cont := body(t, `{"model":"jev-router","prompt_cache_key":"s1","input":[
		{"type":"additional_tools"},
		{"role":"user","content":[{"type":"input_text","text":"start the work"}]},
		{"type":"function_call_output","output":"done"}]}`)
	h.Hooks().RewriteRequest("/responses", cont)

	if r.calls != 1 {
		t.Errorf("router calls = %d, want 1: a continuation reuses the turn's tier", r.calls)
	}
	if cont["model"] != "gpt-5.6-sol" {
		t.Errorf("model = %v, want the tier pinned for the turn", cont["model"])
	}
}

// A nil router.Metrics field must survive into state.Status untouched: the
// handler copies the pointers as given, without dereferencing, so a value
// Jev did not report renders as n/a rather than a fabricated 0.0.
func TestPreservesNilMetricsRatherThanFabricatingZero(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "gpt-5.6-sol", conf: 0.95})
	b := body(t, strings.Replace(turn, "%s", "design the schema", 1))
	key := h.Hooks().RewriteRequest("/responses", b)

	got := state.Read(key)
	if got == nil || got.Metrics == nil {
		t.Fatal("no metrics recorded")
	}
	if got.Metrics.TaskComplexity != nil {
		t.Errorf("TaskComplexity = %v, want nil preserved as nil, not a fabricated 0.0", *got.Metrics.TaskComplexity)
	}
}

// RewritesPath must accept a bare /responses and anything ending in it, since
// the ChatGPT backend Codex talks through may prefix the path.
func TestRewritesPathMatchesAnyPathEndingInResponses(t *testing.T) {
	cases := map[string]bool{
		"/responses":             true,
		"/backend-api/responses": true,
		"/responses/other":       false,
		"/v1/chat/completions":   false,
	}
	for path, want := range cases {
		if got := rewritesPath(path); got != want {
			t.Errorf("rewritesPath(%q) = %v, want %v", path, got, want)
		}
	}
}

// Carry-forward 1: policy.Outcome.Target must reach state.Status.Target.
func TestRecordsTheRouterTargetEvenWhenPolicyOverridesIt(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "gpt-5.6-sol", conf: 0.5})

	pin := body(t, `{"model":"jev-router","prompt_cache_key":"s1","input":[
		{"type":"additional_tools"},
		{"role":"user","content":[{"type":"input_text","text":"use sonnet for this"}]}]}`)
	key := h.Hooks().RewriteRequest("/responses", pin)
	h.Hooks().ObserveUsage(key, mustUsage(300000))

	again := body(t, `{"model":"jev-router","prompt_cache_key":"s1","input":[
		{"type":"additional_tools"},
		{"role":"user","content":[{"type":"input_text","text":"use sonnet for this"}]},
		{"role":"user","content":[{"type":"input_text","text":"now handle something huge"}]}]}`)
	h.Hooks().RewriteRequest("/responses", again)

	got := readStatus(t, key)
	if got.Tier != "sonnet" {
		t.Fatalf("tier = %q, want the pinned tier held: sonnet", got.Tier)
	}
	if got.Target != "opus" {
		t.Errorf("target = %q, want the router's own pick preserved as opus", got.Target)
	}
}

// Carry-forward 2: fable must never be offered without the opt-in.
func TestExcludesFableFromTheModelListByDefault(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("LLMR_ALLOW_FABLE", "")
	t.Setenv("JEV_ALLOW_FABLE", "")
	r := &fakeRouter{choice: "gpt-5.6-sol", conf: 0.95}
	h := New(r)
	captured := make(chan []config.Model, 1)
	h.route = routerFunc(func(_ context.Context, in router.Input) (*router.Decision, error) {
		captured <- in.Models
		return r.Route(context.Background(), in)
	})

	b := body(t, strings.Replace(turn, "%s", "design the schema", 1))
	h.Hooks().RewriteRequest("/responses", b)

	models := <-captured
	for _, m := range models {
		if m.Tier == "fable" {
			t.Errorf("model list = %+v, must not include fable without opt-in", models)
		}
	}
}

type routerFunc func(context.Context, router.Input) (*router.Decision, error)

func (f routerFunc) Route(ctx context.Context, in router.Input) (*router.Decision, error) {
	return f(ctx, in)
}

// Carry-forward 3: a truncated response's partial counts must not overwrite
// a complete measurement.
func TestIgnoresIncompleteUsage(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "gpt-5.6-luna", conf: 0.95})
	b := body(t, strings.Replace(turn, "%s", "first", 1))
	key := h.Hooks().RewriteRequest("/responses", b)

	h.Hooks().ObserveUsage(key, mustUsage(94000))
	h.Hooks().ObserveUsage(key, incompleteUsage(10))

	if got := h.Cached(key); got != 94000 {
		t.Errorf("cached tokens = %d, want the last complete measurement of 94000 kept", got)
	}
}

// Carry-forward 5: a fresh conversation's Current must reach policy.Decide as
// "", not the internal opus guess.
func TestFreshConversationLowConfidenceAnswerDoesNotDefaultToOpus(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "gpt-5.6-luna", conf: 0.2})
	b := body(t, strings.Replace(turn, "%s", "anything", 1))
	h.Hooks().RewriteRequest("/responses", b)

	if b["model"] == "gpt-5.6-sol" {
		t.Errorf("model = %v, want a fresh conversation's low-confidence answer to not default to opus", b["model"])
	}
	if b["model"] != "gpt-5.6-luna" {
		t.Errorf("model = %v, want gpt-5.6-luna: nothing pinned yet, so no downgrade guard applies", b["model"])
	}
}

// Carry-forward 6: least-recently-used eviction, not first-inserted.
func TestActiveConversationSurvivesEviction(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "gpt-5.6-sol", conf: 0.95})

	pin := body(t, `{"model":"jev-router","prompt_cache_key":"pin","input":[
		{"type":"additional_tools"},
		{"role":"user","content":[{"type":"input_text","text":"use haiku for this one"}]}]}`)
	h.Hooks().RewriteRequest("/responses", pin)
	if pin["model"] != "gpt-5.6-luna" {
		t.Fatalf("setup: pinned model = %v, want haiku", pin["model"])
	}

	continuation := func() map[string]any {
		return body(t, `{"model":"jev-router","prompt_cache_key":"pin","input":[
			{"type":"additional_tools"},
			{"role":"user","content":[{"type":"input_text","text":"use haiku for this one"}]},
			{"type":"function_call_output","output":"x"}]}`)
	}

	for i := 0; i < 50; i++ {
		other := body(t, `{"model":"jev-router","prompt_cache_key":"other","input":[
			{"type":"additional_tools"},
			{"role":"user","content":[{"type":"input_text","text":"unrelated task"}]}]}`)
		h.Hooks().RewriteRequest("/responses", other)

		mid := continuation()
		h.Hooks().RewriteRequest("/responses", mid)
	}

	final := continuation()
	h.Hooks().RewriteRequest("/responses", final)

	if final["model"] != "gpt-5.6-luna" {
		t.Errorf("model = %v, want the pinned haiku tier to survive 50 unrelated conversations", final["model"])
	}
}

// This is the assertion the review flagged as missing: a Codex response,
// driven through the real proxy tap (not fabricated with mustUsage), must
// end up recorded as a non-zero cache. Before the tap learned OpenAI's wire
// shape, this was 0 for every Codex conversation of any size, and every
// downgrade looked free.
func TestOpenAIStreamThroughTheProxyTapYieldsANonZeroCache(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "gpt-5.6-sol", conf: 0.91})

	turnBody := strings.Replace(turn, "%s", "design the schema", 1)
	key := ConversationKey(body(t, turnBody))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\",\"usage\":null}}\n\n")
		io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":94012,\"input_tokens_details\":{\"cached_tokens\":94000},\"output_tokens\":877}}}\n\n")
	}))
	defer upstream.Close()

	p, err := proxy.Start(upstream.URL, h.Hooks())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	resp, err := http.Post(p.URL()+"/responses", "application/json", strings.NewReader(turnBody))
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	if got := h.Cached(key); got == 0 {
		t.Fatal("Cached(key) = 0, want the OpenAI response's cache total to have reached the handler")
	}
}

func mustUsage(cacheRead int) proxy.Usage {
	return proxy.Usage{CacheReadTokens: cacheRead, Complete: true}
}

func incompleteUsage(cacheRead int) proxy.Usage {
	return proxy.Usage{CacheReadTokens: cacheRead, Complete: false}
}

func readStatus(t *testing.T, key string) state.Status {
	t.Helper()
	got := state.Read(key)
	if got == nil {
		t.Fatalf("no status recorded for key %q", key)
	}
	return *got
}
