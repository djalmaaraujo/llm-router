package jev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/djalmaaraujo/llm-router/internal/config"
	"github.com/djalmaaraujo/llm-router/internal/router"
)

var models = []config.Model{
	{ID: "claude-haiku-4-5-20251001", Tier: "haiku", Description: "Haiku 4.5"},
	{ID: "claude-opus-5", Tier: "opus", Description: "Opus 5"},
}

func TestRouteSendsTheContractAndReadsTheAnswer(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path = %q, want /v1/systemone", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"jev-latest","answers":{
			"model":{"type":"choice","choice":"claude-opus-5","confidence":0.91,"probabilities":{}},
			"task_complexity":{"type":"score","score":8,"confidence":0.9},
			"reasoning_required":{"type":"score","score":9,"confidence":0.9},
			"tool_complexity":{"type":"score","score":6,"confidence":0.9}}}`))
	}))
	defer server.Close()

	client := NewWithBaseURL("secret", server.URL)
	out, err := client.Route(context.Background(), router.Input{
		Prompt: "design the cache policy", Current: "claude-haiku-4-5-20251001",
		ContextTokens: 6200, Models: models,
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if out.Choice != "claude-opus-5" || out.Confidence != 0.91 {
		t.Errorf("got %+v", out)
	}
	if out.Metrics.TaskComplexity < 0.88 || out.Metrics.TaskComplexity > 0.90 {
		t.Errorf("TaskComplexity = %v, want 8/9", out.Metrics.TaskComplexity)
	}
	if got["model"] != "jev-latest" {
		t.Errorf("request model = %v, want jev-latest", got["model"])
	}
	state := got["state"].(map[string]any)
	if state["request"] != "design the cache policy" {
		t.Errorf("state.request = %v", state["request"])
	}
	session := state["session"].(map[string]any)
	if session["current_model"] != "claude-haiku-4-5-20251001" {
		t.Errorf("state.session.current_model = %v", session["current_model"])
	}
	if _, ok := state["environment"]; ok {
		t.Error("state must not carry an environment key; it would duplicate the criteria model list")
	}
}

func TestRouteFailsClosedOnAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()
	if _, err := NewWithBaseURL("k", server.URL).Route(context.Background(), router.Input{
		Prompt: "x", Current: "claude-opus-5", Models: models,
	}); err == nil {
		t.Error("a 500 must return an error so the caller keeps the current model")
	}
}

func TestRouteRefusesAnEmptyModelList(t *testing.T) {
	if _, err := New("k").Route(context.Background(), router.Input{Prompt: "x"}); err == nil {
		t.Error("with no models there is nothing to choose between")
	}
}

func TestRouteUsesDefaultModelIDUnlessOverridden(t *testing.T) {
	t.Setenv("LLMR_JEV_MODEL", "")
	t.Setenv("JEV_JEV_MODEL", "")
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"model":"jev-latest","answers":{"model":{"type":"choice","choice":"claude-opus-5","confidence":0.9}}}`))
	}))
	defer server.Close()

	NewWithBaseURL("k", server.URL).Route(context.Background(), router.Input{Prompt: "x", Models: models})
	if got["model"] != "jev-latest" {
		t.Errorf("request model = %v, want jev-latest by default", got["model"])
	}

	t.Setenv("LLMR_JEV_MODEL", "jev-2026-08-01")
	got = nil
	NewWithBaseURL("k", server.URL).Route(context.Background(), router.Input{Prompt: "x", Models: models})
	if got["model"] != "jev-2026-08-01" {
		t.Errorf("request model = %v, want the LLMR_JEV_MODEL override", got["model"])
	}
}
