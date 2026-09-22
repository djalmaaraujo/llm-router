package claude

import (
	"context"
	"encoding/json"
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

const turn = `{"model":"jev-router","tools":[{"name":"Read"}],
	"metadata":{"user_id":"{\"session_id\":\"s1\"}"},
	"messages":[{"role":"user","content":"%s"}]}`

func TestRoutesAFreshTurnAndRewritesTheSentinel(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "claude-opus-5", conf: 0.95})
	b := body(t, strings.Replace(turn, "%s", "design the schema", 1))
	h.Hooks().RewriteRequest("/v1/messages", b)
	if b["model"] != "claude-opus-5" {
		t.Errorf("model = %v, want the sentinel replaced", b["model"])
	}
}

func TestPassesThroughAModelTheUserPicked(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	r := &fakeRouter{choice: "claude-opus-5", conf: 0.95}
	h := New(r)
	b := body(t, `{"model":"claude-sonnet-5","tools":[{"name":"Read"}],
		"metadata":{"user_id":"{\"session_id\":\"s1\"}"},"messages":[{"role":"user","content":"hi"}]}`)
	h.Hooks().RewriteRequest("/v1/messages", b)
	if b["model"] != "claude-sonnet-5" {
		t.Errorf("model = %v, want the user's choice untouched", b["model"])
	}
	if r.calls != 0 {
		t.Error("an explicit choice must not consult the router")
	}
}

func TestDoesNotRouteAToolLoopContinuation(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	r := &fakeRouter{choice: "claude-opus-5", conf: 0.95}
	h := New(r)
	first := body(t, strings.Replace(turn, "%s", "start the work", 1))
	h.Hooks().RewriteRequest("/v1/messages", first)

	cont := body(t, `{"model":"jev-router","tools":[{"name":"Read"}],
		"metadata":{"user_id":"{\"session_id\":\"s1\"}"},
		"messages":[{"role":"user","content":"start the work"},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"x"}]}]}`)
	h.Hooks().RewriteRequest("/v1/messages", cont)

	if r.calls != 1 {
		t.Errorf("router calls = %d, want 1: a continuation reuses the turn's tier", r.calls)
	}
	if cont["model"] != "claude-opus-5" {
		t.Errorf("model = %v, want the tier pinned for the turn", cont["model"])
	}
}

func TestTheFirstConversationIsMainAndLaterOnesAreSubAgents(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "claude-opus-5", conf: 0.95})
	main := body(t, strings.Replace(turn, "%s", "the user's first prompt", 1))
	h.Hooks().RewriteRequest("/v1/messages", main)
	sub := body(t, strings.Replace(turn, "%s", "search the repo for X", 1))
	h.Hooks().RewriteRequest("/v1/messages", sub)

	if h.IsSubAgent(ConversationKey(main)) {
		t.Error("the first routed conversation in a session is the main one")
	}
	if !h.IsSubAgent(ConversationKey(sub)) {
		t.Error("a later conversation under the same session is a sub-agent")
	}
}

func TestUsageFeedsTheNextDecision(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "claude-haiku-4-5-20251001", conf: 0.95})
	b := body(t, strings.Replace(turn, "%s", "first", 1))
	key := h.Hooks().RewriteRequest("/v1/messages", b)
	if key == "" {
		t.Fatal("a routed request must return a correlation key")
	}
	h.Hooks().ObserveUsage(key, proxy.Usage{CacheReadTokens: 94000, OutputTokens: 800, Complete: true})
	if got := h.Cached(key); got != 94000 {
		t.Errorf("cached tokens = %d, want the measured 94000 rather than an estimate", got)
	}
}

func TestRouterFailureKeepsTheCurrentModel(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{err: context.DeadlineExceeded})
	b := body(t, strings.Replace(turn, "%s", "anything", 1))
	h.Hooks().RewriteRequest("/v1/messages", b)
	if config.TierOf(b["model"].(string)) == "" {
		t.Errorf("model = %v, want a real model even when routing failed", b["model"])
	}
}

// Carry-forward 1: policy.Outcome.Target must reach state.Status.Target, even
// when policy overrides the router's pick, or the explanation report can
// never show what the router actually named.
func TestRecordsTheRouterTargetEvenWhenPolicyOverridesIt(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "claude-opus-5", conf: 0.5})

	pin := body(t, strings.Replace(turn, "%s", "let's use sonnet for this", 1))
	key := h.Hooks().RewriteRequest("/v1/messages", pin)
	h.Hooks().ObserveUsage(key, proxy.Usage{CacheReadTokens: 300000, Complete: true})

	again := body(t, `{"model":"jev-router","tools":[{"name":"Read"}],
		"metadata":{"user_id":"{\"session_id\":\"s1\"}"},
		"messages":[{"role":"user","content":"let's use sonnet for this"},
			{"role":"user","content":"now handle something huge"}]}`)
	h.Hooks().RewriteRequest("/v1/messages", again)

	got := state.Read("s1")
	if got == nil {
		t.Fatal("no status recorded for session s1")
	}
	if got.Tier != "sonnet" {
		t.Fatalf("tier = %q, want the pinned tier held: sonnet (low-confidence big context)", got.Tier)
	}
	if got.Target != "opus" {
		t.Errorf("target = %q, want the router's own pick preserved as opus", got.Target)
	}
}

// Carry-forward 2: the model list handed to the router must never include
// fable unless the account opted into its extra usage credits.
func TestExcludesFableFromTheModelListByDefault(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("LLMR_ALLOW_FABLE", "")
	t.Setenv("JEV_ALLOW_FABLE", "")
	r := &fakeRouter{choice: "claude-opus-5", conf: 0.95}
	h := New(r)
	captured := make(chan []config.Model, 1)
	spy := routerFunc(func(_ context.Context, in router.Input) (*router.Decision, error) {
		captured <- in.Models
		return r.Route(context.Background(), in)
	})
	h.route = spy

	b := body(t, strings.Replace(turn, "%s", "design the schema", 1))
	h.Hooks().RewriteRequest("/v1/messages", b)

	models := <-captured
	for _, m := range models {
		if m.Tier == "fable" {
			t.Errorf("model list = %+v, must not include fable without opt-in", models)
		}
	}
}

// Carry-forward 3: a truncated response's partial token counts must never
// overwrite the last measured, complete ones.
func TestIgnoresIncompleteUsage(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "claude-haiku-4-5-20251001", conf: 0.95})
	b := body(t, strings.Replace(turn, "%s", "first", 1))
	key := h.Hooks().RewriteRequest("/v1/messages", b)

	h.Hooks().ObserveUsage(key, proxy.Usage{CacheReadTokens: 94000, OutputTokens: 800, Complete: true})
	h.Hooks().ObserveUsage(key, proxy.Usage{CacheReadTokens: 10, OutputTokens: 1, Complete: false})

	if got := h.Cached(key); got != 94000 {
		t.Errorf("cached tokens = %d, want the last complete measurement of 94000 kept", got)
	}
}

// Carry-forward 4: a nil router.Metrics field must survive into state.Status
// as nil, never as a fabricated zero.
func TestPreservesNilMetricsRatherThanFabricatingZero(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "claude-opus-5", conf: 0.95})
	b := body(t, strings.Replace(turn, "%s", "design the schema", 1))
	h.Hooks().RewriteRequest("/v1/messages", b)

	got := state.Read("s1")
	if got == nil || got.Metrics == nil {
		t.Fatal("no metrics recorded")
	}
	if got.Metrics.TaskComplexity != nil {
		t.Errorf("TaskComplexity = %v, want nil preserved as nil, not a fabricated 0.0", *got.Metrics.TaskComplexity)
	}
}

type routerFunc func(context.Context, router.Input) (*router.Decision, error)

func (f routerFunc) Route(ctx context.Context, in router.Input) (*router.Decision, error) {
	return f(ctx, in)
}
