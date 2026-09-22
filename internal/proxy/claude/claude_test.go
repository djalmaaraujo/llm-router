package claude

import (
	"context"
	"encoding/json"
	"fmt"
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

// Minor 5: assert the exact model kept, not merely that some real tier came
// back — a wrong-but-real tier would have passed the old assertion.
func TestRouterFailureKeepsTheCurrentModel(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{err: context.DeadlineExceeded})

	pin := body(t, strings.Replace(turn, "%s", "use sonnet for this one", 1))
	h.Hooks().RewriteRequest("/v1/messages", pin)
	if pin["model"] != "claude-sonnet-5" {
		t.Fatalf("setup: pinned model = %v, want claude-sonnet-5", pin["model"])
	}

	b := body(t, `{"model":"jev-router","tools":[{"name":"Read"}],
		"metadata":{"user_id":"{\"session_id\":\"s1\"}"},
		"messages":[{"role":"user","content":"use sonnet for this one"},
			{"role":"user","content":"now something else entirely"}]}`)
	h.Hooks().RewriteRequest("/v1/messages", b)
	if b["model"] != "claude-sonnet-5" {
		t.Errorf("model = %v, want the pinned claude-sonnet-5 kept when routing fails", b["model"])
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

// Fix round 1, Critical 1: a fresh conversation has no cache to protect, so
// its Current must reach policy.Decide as "", not the internal "opus" guess
// used for ApplyTier and the router's own Current. Passing the opus guess
// into policy made a low-confidence downgrade look like a downgrade FROM
// opus, tripping low-confidence-no-downgrade and sticking on opus for no
// reason at all.
func TestFreshConversationLowConfidenceAnswerDoesNotDefaultToOpus(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "claude-haiku-4-5-20251001", conf: 0.2})
	b := body(t, strings.Replace(turn, "%s", "anything", 1))
	h.Hooks().RewriteRequest("/v1/messages", b)

	if b["model"] == "claude-opus-5" {
		t.Errorf("model = %v, want a fresh conversation's low-confidence answer to not default to opus", b["model"])
	}
	if b["model"] != "claude-haiku-4-5-20251001" {
		t.Errorf("model = %v, want claude-haiku-4-5-20251001: nothing pinned yet, so no downgrade guard applies", b["model"])
	}
}

// Fix round 1, Critical 2: a conversation still being looked up (mid tool
// loop) must move to the back of the eviction order on every access, or 50
// unrelated conversations churning past it can evict it out from under an
// active task even though it was just used.
func TestActiveConversationSurvivesEviction(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "claude-opus-5", conf: 0.95})

	pinFirstMessage := "use haiku for this one"
	pin := body(t, strings.Replace(turn, "%s", pinFirstMessage, 1))
	h.Hooks().RewriteRequest("/v1/messages", pin)
	if pin["model"] != "claude-haiku-4-5-20251001" {
		t.Fatalf("setup: pinned model = %v, want haiku", pin["model"])
	}

	continuation := func() map[string]any {
		return body(t, fmt.Sprintf(`{"model":"jev-router","tools":[{"name":"Read"}],
			"metadata":{"user_id":"{\"session_id\":\"s1\"}"},
			"messages":[{"role":"user","content":%q},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"x"}]}]}`, pinFirstMessage))
	}

	for i := 0; i < 50; i++ {
		other := body(t, fmt.Sprintf(`{"model":"jev-router","tools":[{"name":"Read"}],
			"metadata":{"user_id":"{\"session_id\":\"other-%d\"}"},
			"messages":[{"role":"user","content":"unrelated task %d"}]}`, i, i))
		h.Hooks().RewriteRequest("/v1/messages", other)

		// The pinned conversation is still mid tool loop: it keeps receiving
		// continuations while the 50 unrelated conversations churn through.
		mid := continuation()
		h.Hooks().RewriteRequest("/v1/messages", mid)
	}

	final := continuation()
	h.Hooks().RewriteRequest("/v1/messages", final)

	if final["model"] != "claude-haiku-4-5-20251001" {
		t.Errorf("model = %v, want the pinned haiku tier to survive 50 unrelated conversations", final["model"])
	}
}

// Minor 6a: Claude Code's own cheap auxiliary calls (a concrete model, no
// tools) must never flip an existing status to manual.
func TestAuxiliaryCallWithoutToolsDoesNotWriteManualStatus(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "claude-opus-5", conf: 0.95})

	routed := body(t, strings.Replace(turn, "%s", "design the schema", 1))
	h.Hooks().RewriteRequest("/v1/messages", routed)

	before := state.Read("s1")
	if before == nil || before.Manual {
		t.Fatal("setup: expected a non-manual status recorded")
	}

	aux := body(t, `{"model":"claude-haiku-4-5-20251001",
		"metadata":{"user_id":"{\"session_id\":\"s1\"}"},"messages":[{"role":"user","content":"cheap aux call"}]}`)
	h.Hooks().RewriteRequest("/v1/messages", aux)

	after := state.Read("s1")
	if after == nil || after.Manual {
		t.Error("an auxiliary call with no tools must not flip the status line to manual")
	}
}

// Minor 6b: claude -p omits metadata on a session's first request; without
// the conversation-key fallback the decision would be silently dropped.
func TestEmptySessionIDFilesUnderTheConversationKey(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "claude-opus-5", conf: 0.95})
	b := body(t, `{"model":"jev-router","tools":[{"name":"Read"}],
		"messages":[{"role":"user","content":"claude -p one-shot"}]}`)
	key := h.Hooks().RewriteRequest("/v1/messages", b)
	if key == "" {
		t.Fatal("expected a correlation key")
	}
	if got := state.Read(key); got == nil {
		t.Error("with no session id, the decision must be filed under the conversation key")
	}
}

// Important 4 (disclosed, not fixed this round): under `claude --resume`, a
// sub-agent whose opening request reaches the handler before the main
// conversation's own first routed request is recorded as that session's
// main, and the true main conversation is then mislabelled a sub-agent —
// which hands it the short horizon meant for throwaway work. This test
// documents the known behaviour so it is not "fixed" by accident.
func TestResumedSessionCanMisclassifyTheMainConversationAsASubAgent(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{choice: "claude-opus-5", conf: 0.95})

	subAgentArrivesFirst := body(t, strings.Replace(turn, "%s", "sub-agent's own first prompt", 1))
	h.Hooks().RewriteRequest("/v1/messages", subAgentArrivesFirst)

	mainArrivesSecond := body(t, strings.Replace(turn, "%s", "the resumed session's real first prompt", 1))
	h.Hooks().RewriteRequest("/v1/messages", mainArrivesSecond)

	if h.IsSubAgent(ConversationKey(subAgentArrivesFirst)) {
		t.Error("known limitation: whichever key arrives first under a session is recorded as main")
	}
	if !h.IsSubAgent(ConversationKey(mainArrivesSecond)) {
		t.Error("known limitation: the true main conversation is mislabelled a sub-agent when it arrives second")
	}
}

// Fix round 2: a fresh conversation whose router call fails must land on the
// default tier, never on the cheapest available one (the inverted fail-open
// bug fixed in policy.clampToAvailable), and must NOT come away pinned to
// that landing — the next turn should route again from scratch rather than
// inherit a fiction.
func TestFreshConversationWithFailedRouterLandsOnDefaultAndStaysUnpinned(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := New(&fakeRouter{err: context.DeadlineExceeded})

	first := body(t, strings.Replace(turn, "%s", "anything at all", 1))
	h.Hooks().RewriteRequest("/v1/messages", first)
	if first["model"] != "claude-opus-5" {
		t.Fatalf("model = %v, want the default tier (opus), not the cheapest available (haiku)", first["model"])
	}

	second := body(t, `{"model":"jev-router","tools":[{"name":"Read"}],
		"metadata":{"user_id":"{\"session_id\":\"s1\"}"},
		"messages":[{"role":"user","content":"anything at all"},
			{"role":"user","content":"a second fresh turn in the same conversation"}]}`)
	h.Hooks().RewriteRequest("/v1/messages", second)
	if second["model"] != "claude-opus-5" {
		t.Errorf("model = %v, want the default tier again: the first failure must not have pinned haiku", second["model"])
	}
}

type routerFunc func(context.Context, router.Input) (*router.Decision, error)

func (f routerFunc) Route(ctx context.Context, in router.Input) (*router.Decision, error) {
	return f(ctx, in)
}
