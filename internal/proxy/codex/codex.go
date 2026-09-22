// Package codex wires the routing pipeline to the Responses API's wire
// format: it recognises a turn, calls the router, applies policy, and
// records the decision, all behind the proxy.Hooks the transport calls. It
// mirrors internal/proxy/claude, with the differences that follow from
// Codex's own body shape and its lack of Claude Code-style capability
// negotiation.
package codex

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

// maxConvos caps the per-conversation state the handler keeps in memory, for
// the same reason as the Claude side: a long-running session must not leak
// memory for as long as the router process runs.
const maxConvos = 50

// defaultTier is used the first time a conversation is seen, before any
// routing decision has pinned one. Deliberately the most expensive tier, for
// the same reason as the Claude side: guessing high is the safe direction.
const defaultTier = "opus"

type convo struct {
	tier   string
	model  string
	cached int
	output int
}

// Handler holds the per-conversation state that lets a stateless HTTP proxy
// make a stateful routing decision, guarded by mu against the concurrent
// requests a CLI session issues.
type Handler struct {
	route router.Router

	mu      sync.Mutex
	convos  map[string]*convo
	order   []string
	catalog []map[string]any

	// OnDecision, when set, is called after every fresh routing decision
	// with the model Codex will actually run and the router's confidence.
	// Unlike Claude Code, the Codex CLI has no status-line hook the router
	// can render into on its own, so the launcher uses this to print the
	// one commentary line per turn the brief calls for.
	OnDecision func(model string, confidence float64)
}

// New builds a Handler that asks r for a routing decision on every fresh
// turn.
func New(r router.Router) *Handler {
	return &Handler{
		route:  r,
		convos: make(map[string]*convo),
	}
}

// Hooks adapts the handler to the proxy's transport-level hook points.
func (h *Handler) Hooks() proxy.Hooks {
	return proxy.Hooks{
		RewritesPath:    rewritesPath,
		RewriteRequest:  h.RewriteRequest,
		ObserveUsage:    h.ObserveUsage,
		BuffersResponse: buffersResponse,
		ObserveResponse: h.ObserveResponse,
	}
}

func rewritesPath(path string) bool {
	return strings.HasSuffix(path, "/responses")
}

func buffersResponse(method, path string) bool {
	return method == "GET" && strings.HasSuffix(path, "/models")
}

// RewriteRequest recognises, routes, and pins a turn, then rewrites body to
// name a real model. There is no capability stripping here: Codex composes
// its own body for whichever model it is told to use, and the Responses API
// takes the model id as given.
func (h *Handler) RewriteRequest(_ string, body map[string]any) string {
	model, _ := body["model"].(string)
	if model != CodexAutoModel {
		// An explicit human choice beats the router.
		return ""
	}

	key := ConversationKey(body)

	h.mu.Lock()
	c, exists := h.convos[key]
	if !exists {
		c = &convo{}
		h.registerConvo(key, c)
	} else {
		// A conversation still being looked up is still in use, however
		// long ago it was created; touch it so unrelated conversations
		// churning through cannot evict it out from under an active tool
		// loop.
		h.touch(key)
	}
	pinnedTier := c.tier
	pinnedModel := c.model
	cached, output := c.cached, c.output
	models := h.availableModelsLocked()
	h.mu.Unlock()

	// routerModel is only what gets SENT to the router and, when nothing
	// better exists yet, what gets applied below: a concrete starting point,
	// guessed high because that is what the prompt cache was most likely
	// built on. pinnedTier stays "" for policy.Decide when nothing is
	// pinned yet, since a fresh conversation has no cache at all to
	// protect: substituting a default there would tell the policy an
	// expensive cache exists on a brand-new conversation and stick it on
	// the strongest model.
	routerTier := pinnedTier
	if routerTier == "" {
		routerTier = defaultTier
	}
	routerModel := pinnedModel
	if routerModel == "" {
		routerModel = ModelFor(routerTier)
	}

	prompt := NewTurnPrompt(body)
	fresh := prompt != ""

	var pending *state.Status

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
			answer = &policy.Answer{Choice: tierOfModel(models, decision.Choice), Confidence: decision.Confidence}
		}

		outcome := policy.Decide(policy.Input{
			Prompt:       prompt,
			Jev:          answer,
			Current:      pinnedTier,
			Available:    config.AvailableTiers(),
			CachedTokens: cached,
			OutputTokens: output,
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

			if h.OnDecision != nil {
				h.OnDecision(decidedModel, decision.Confidence)
			}
		}
		pending = &status

		log.Debug("routed key=%s tier=%s reason=%s", key, outcome.Tier, outcome.Reason)
	}

	h.mu.Lock()
	tier, modelID := c.tier, c.model
	h.mu.Unlock()

	// No pin and no router opinion lands the request on the default rather
	// than inventing a pin: the conversation stays unpinned in convo, so
	// the next turn routes again from scratch instead of inheriting a
	// fiction.
	applyTier, applyModel := tier, modelID
	if applyTier == "" {
		applyTier = defaultTier
		applyModel = ModelFor(defaultTier)
	}

	body["model"] = applyModel

	if pending != nil {
		pending.Tier = applyTier
		pending.Model = applyModel
		state.WriteDecision(key, *pending)
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
	// A truncated stream leaves partial numbers in u. Overwriting the
	// stored counts with those would understate the cached context and let
	// the cost policy switch models freely, destroying the very cache it
	// protects.
	if !u.Complete {
		return
	}
	c.cached = u.CachedTotal()
	c.output = u.OutputTokens
}

// ObserveResponse keeps the account's real model catalog so the router is
// offered exact model ids, and runs the body through AddJevModel before
// returning it, so the rewritten catalog — sentinel row included — is what
// Codex's own picker actually renders, not just what this handler happens to
// have cached internally.
func (h *Handler) ObserveResponse(_ string, body []byte) []byte {
	var catalog map[string]any
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil
	}
	catalog = AddJevModel(catalog)

	rows, _ := catalog["models"].([]any)
	var entries []map[string]any
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if slug, _ := row["slug"].(string); slug == CodexAutoModel || tierOfSlug(slug) == "" {
			continue
		}
		entries = append(entries, row)
	}

	h.mu.Lock()
	h.catalog = entries
	h.mu.Unlock()

	rewritten, err := json.Marshal(catalog)
	if err != nil {
		return nil
	}
	return rewritten
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

// registerConvo stores c under key, evicting the least-recently-used entry
// once the cap is reached. Callers must hold h.mu.
func (h *Handler) registerConvo(key string, c *convo) {
	if len(h.order) >= maxConvos {
		oldest := h.order[0]
		h.order = h.order[1:]
		delete(h.convos, oldest)
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

// availableModelsLocked builds the model list the router is offered,
// filtered to the tiers this account may run. Callers must hold h.mu.
func (h *Handler) availableModelsLocked() []config.Model {
	available := config.AvailableTiers()
	var out []config.Model
	for _, m := range modelsFromCatalog(h.catalog) {
		if containsString(available, m.Tier) {
			out = append(out, m)
		}
	}
	return out
}

// modelsFromCatalog keeps catalog entries whose slug maps to a known tier.
// When nothing in the catalog matches a tier, it falls back to the static
// tier defaults so the model picker never comes up empty.
func modelsFromCatalog(catalog []map[string]any) []config.Model {
	var models []config.Model
	for _, entry := range catalog {
		slug, ok := entry["slug"].(string)
		if !ok {
			continue
		}
		tier := tierOfSlug(slug)
		if tier == "" {
			continue
		}
		desc, _ := entry["display_name"].(string)
		if desc == "" {
			desc = slug
		}
		models = append(models, config.Model{ID: slug, Tier: tier, Description: desc})
	}
	if len(models) == 0 {
		for _, t := range codexTiers {
			models = append(models, config.Model{ID: ModelFor(t.tier), Tier: t.tier, Description: ModelFor(t.tier)})
		}
	}
	return models
}

// tierOfSlug names the tier for a Codex model slug, matching either its
// default or its configured override, or "" when it is not one of ours.
func tierOfSlug(slug string) string {
	for _, t := range codexTiers {
		if ModelFor(t.tier) == slug {
			return t.tier
		}
	}
	return ""
}

// tierOfModel resolves a router answer to a tier name: first against the
// exact catalog handed to it, then by falling back to the static defaults, in
// case the router answered with a slug that never made it into that catalog.
func tierOfModel(models []config.Model, id string) string {
	for _, m := range models {
		if m.ID == id {
			return m.Tier
		}
	}
	return tierOfSlug(id)
}

// modelForTier finds the exact model id this account has for tier, falling
// back to the static tier default when the catalog carries none.
func modelForTier(models []config.Model, tier string) string {
	for _, m := range models {
		if m.Tier == tier {
			return m.ID
		}
	}
	return ModelFor(tier)
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
