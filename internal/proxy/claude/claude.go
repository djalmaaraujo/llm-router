// Package claude wires the routing pipeline to Claude Code's own wire
// format: it recognises a turn, calls the router, applies policy, and
// records the decision, all behind the proxy.Hooks the transport calls.
package claude

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/djalmaaraujo/llm-router/internal/config"
	"github.com/djalmaaraujo/llm-router/internal/log"
	"github.com/djalmaaraujo/llm-router/internal/policy"
	"github.com/djalmaaraujo/llm-router/internal/proxy"
	"github.com/djalmaaraujo/llm-router/internal/router"
	"github.com/djalmaaraujo/llm-router/internal/state"
)

// maxConvos caps the per-conversation state the handler keeps in memory. A
// long-running Claude Code session opens many sub-agent conversations; without
// a cap, one session would leak memory for as long as the router process runs.
const maxConvos = 50

// defaultTier is used the first time a conversation is seen, before any
// routing decision has pinned one. It is deliberately the most expensive
// tier: it is what the prompt cache was most likely built on, and guessing
// high is the safe direction.
const defaultTier = "opus"

type convo struct {
	tier     string
	model    string
	cached   int
	output   int
	subAgent bool
}

// Handler holds the per-conversation and per-session state that lets a
// stateless HTTP proxy make a stateful routing decision. Every field below
// the router is guarded by mu; the CLI issues concurrent requests, and a race
// here would corrupt routing state mid-session.
type Handler struct {
	route router.Router

	mu       sync.Mutex
	convos   map[string]*convo
	order    []string
	sessions map[string]string
	catalog  []map[string]any
}

// New builds a Handler that asks r for a routing decision on every fresh
// turn.
func New(r router.Router) *Handler {
	return &Handler{
		route:    r,
		convos:   make(map[string]*convo),
		sessions: make(map[string]string),
	}
}

// Hooks adapts the handler to the proxy's transport-level hook points.
func (h *Handler) Hooks() proxy.Hooks {
	return proxy.Hooks{
		RewritesPath:    func(path string) bool { return path == "/v1/messages" },
		RewriteRequest:  h.RewriteRequest,
		ObserveUsage:    h.ObserveUsage,
		BuffersResponse: func(method, path string) bool { return method == "GET" && path == "/v1/models" },
		ObserveResponse: h.ObserveResponse,
	}
}

// RewriteRequest recognises, routes, and pins a turn, then rewrites body to
// name a real model. It returns the conversation key ObserveUsage will later
// report against, or "" when the request should not be tapped.
func (h *Handler) RewriteRequest(_ string, body map[string]any) string {
	sanitizeTools(body)

	// A model that is not the sentinel means the user picked one in Claude
	// Code's own picker; an explicit human choice beats the router.
	model, _ := body["model"].(string)
	if !config.IsAuto(model) {
		if _, hasTools := body["tools"]; hasTools {
			// Claude Code makes cheap auxiliary calls with no tools; those
			// must not flip the status line to manual mid-session.
			state.Write(SessionOf(body), state.Status{Manual: true, At: time.Now().Unix()})
		}
		return ""
	}

	key := ConversationKey(body)
	sessionID := SessionOf(body)

	h.mu.Lock()
	c, exists := h.convos[key]
	if !exists {
		c = &convo{}
		h.registerConvo(key, c)
		h.registerSession(sessionID, key, c)
	} else {
		// A conversation still being looked up is still in use, however long
		// ago it was created; move it to the back so 50 unrelated
		// conversations churning through cannot evict it out from under an
		// active tool loop.
		h.touch(key)
	}
	pinnedTier := c.tier
	pinnedModel := c.model
	cached, output, subAgent := c.cached, c.output, c.subAgent
	models := h.availableModelsLocked()
	h.mu.Unlock()

	// routerTier/routerModel are only what gets SENT to the router and, when
	// nothing better exists yet, ApplyTier: a concrete starting point, guessed
	// high because that is what the prompt cache was most likely built on.
	// pinnedTier stays "" for policy.Decide below when nothing is pinned yet,
	// since a fresh conversation has no cache at all to protect.
	routerTier := pinnedTier
	if routerTier == "" {
		routerTier = defaultTier
	}
	routerModel := pinnedModel
	if routerModel == "" {
		routerModel = config.IDOf(routerTier)
	}

	// An empty prompt means a tool-loop continuation, not a new turn.
	// Routing it would let the model flip mid-task and re-ask the router on
	// every tool call. The <jev-explain> marker is how the bundled
	// explanation skill asks for the report; routing it would both cost a
	// router call and overwrite the decision it is asking to show.
	prompt := NewTurnPrompt(body)
	fresh := prompt != "" && !strings.Contains(prompt, "<jev-explain>")

	var pending *state.Status
	var writeKey string

	if fresh {
		decision, err := h.route.Route(context.Background(), router.Input{
			Prompt:        prompt,
			Current:       routerModel,
			ContextTokens: cached,
			Models:        models,
		})

		var answer *policy.Answer
		if err != nil {
			log.Debug("router failed: %v", err)
		} else if decision != nil {
			answer = &policy.Answer{Choice: config.TierOf(decision.Choice), Confidence: decision.Confidence}
		}

		outcome := policy.Decide(policy.Input{
			Prompt:       prompt,
			Jev:          answer,
			Current:      pinnedTier,
			Available:    config.AvailableTiers(),
			CachedTokens: cached,
			OutputTokens: output,
			SubAgent:     subAgent,
		})

		decidedModel := modelForTier(models, outcome.Tier)

		h.mu.Lock()
		c.tier = outcome.Tier
		c.model = decidedModel
		h.mu.Unlock()

		status := state.Status{
			Tier:          outcome.Tier,
			Target:        outcome.Target,
			Model:         decidedModel,
			Prompt:        prompt,
			Reason:        outcome.Reason,
			At:            time.Now().Unix(),
			BreakEven:     outcome.BreakEven,
			Rebuild:       outcome.Rebuild,
			SavingPerTurn: outcome.SavingPerTurn,
			Horizon:       outcome.Horizon,
		}
		if err == nil && decision != nil {
			confidence := decision.Confidence
			status.Confidence = &confidence
			metrics := decision.Metrics
			status.Metrics = &metrics
			status.Request = decision.Request
			status.Response = decision.Response
		}

		// claude -p omits metadata on a session's first request; without the
		// conversation-key fallback the decision would be silently dropped.
		sessionKey := sessionID
		if sessionKey == "" {
			sessionKey = key
		}
		pending = &status
		writeKey = sessionKey

		log.Debug("routed key=%s tier=%s reason=%s", key, outcome.Tier, outcome.Reason)
	}

	h.mu.Lock()
	tier, modelID := c.tier, c.model
	h.mu.Unlock()

	// No pin and no router opinion (policy.Decide returns "" for exactly
	// that case) lands the request on the default rather than inventing a
	// pin: the conversation stays unpinned in convo, so the next turn routes
	// again from scratch instead of inheriting a fiction.
	applyTier, applyModel := tier, modelID
	if applyTier == "" {
		applyTier = defaultTier
		applyModel = config.IDOf(defaultTier)
	}

	// The sentinel is not a real model id, so every routed request must be
	// rewritten, including continuations that reuse the turn's pinned tier.
	ApplyTier(body, applyTier, applyModel)

	// Recorded only once the tier is final, so the status on disk always
	// matches what was actually applied to the request.
	if pending != nil {
		pending.Tier = applyTier
		pending.Model = applyModel
		state.WriteDecision(writeKey, *pending)
	}

	return key
}

// ObserveUsage records the measured token counts for a conversation's next
// decision.
func (h *Handler) ObserveUsage(key string, u proxy.Usage) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c, ok := h.convos[key]
	if !ok {
		return
	}
	// A truncated stream leaves partial numbers in u. Overwriting the stored
	// counts with those would understate the cached context and let the cost
	// policy switch models freely, destroying the very cache it protects.
	if !u.Complete {
		return
	}
	c.cached = u.CachedTotal()
	c.output = u.OutputTokens
}

// ObserveResponse keeps the account's real model catalog so the router is
// offered exact model ids rather than static tier defaults.
func (h *Handler) ObserveResponse(_ string, body []byte) []byte {
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}

	var entries []map[string]any
	for _, entry := range payload.Data {
		id, _ := entry["id"].(string)
		if config.TierOf(id) == "" {
			continue
		}
		entries = append(entries, entry)
	}

	h.mu.Lock()
	h.catalog = entries
	h.mu.Unlock()
	return nil
}

// IsSubAgent reports whether key was registered as a sub-agent conversation.
func (h *Handler) IsSubAgent(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.convos[key]
	return ok && c.subAgent
}

// Cached reports the measured cache total last recorded for key.
func (h *Handler) Cached(key string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.convos[key]
	if !ok {
		return 0
	}
	return c.cached
}

// registerConvo stores c under key, evicting the oldest entry once the cap is
// reached. Callers must hold h.mu.
func (h *Handler) registerConvo(key string, c *convo) {
	if len(h.order) >= maxConvos {
		oldest := h.order[0]
		h.order = h.order[1:]
		delete(h.convos, oldest)
		delete(h.sessions, oldest)
	}
	h.convos[key] = c
	h.order = append(h.order, key)
}

// touch moves key to the back of the eviction order, marking it as the most
// recently used. Callers must hold h.mu.
func (h *Handler) touch(key string) {
	for i, k := range h.order {
		if k == key {
			h.order = append(h.order[:i], h.order[i+1:]...)
			h.order = append(h.order, key)
			return
		}
	}
}

// registerSession marks c as a sub-agent when key is not the first
// conversation seen for sessionID. Callers must hold h.mu.
func (h *Handler) registerSession(sessionID, key string, c *convo) {
	if sessionID == "" {
		return
	}
	if main, ok := h.sessions[sessionID]; ok {
		if main != key {
			c.subAgent = true
		}
		return
	}
	h.sessions[sessionID] = key
}

// availableModelsLocked builds the model list the router is offered, filtered
// to the tiers this account may run. Callers must hold h.mu.
func (h *Handler) availableModelsLocked() []config.Model {
	available := config.AvailableTiers()
	var out []config.Model
	for _, m := range ModelsFrom(h.catalog) {
		if containsString(available, m.Tier) {
			out = append(out, m)
		}
	}
	return out
}

// modelForTier finds the exact model id this account has for tier, falling
// back to the static tier default when the catalog carries none.
func modelForTier(models []config.Model, tier string) string {
	for _, m := range models {
		if m.Tier == tier {
			return m.ID
		}
	}
	return config.IDOf(tier)
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sanitizeTools(body map[string]any) {
	tools, ok := body["tools"].([]any)
	if !ok {
		return
	}
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if schema, ok := tool["input_schema"]; ok {
			SanitizeSchema(schema)
		}
	}
}
