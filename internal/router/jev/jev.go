// Package jev calls TypeSafe System One. The SDK's defaults are far too slow
// for a per-prompt hot path, so the timeout, retry count, and an outer deadline
// are all pinned here.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/djalmaaraujo/llm-router/internal/config"
	"github.com/djalmaaraujo/llm-router/internal/router"
)

const defaultBaseURL = "https://api.typesafe.ai"

const defaultModel = "jev-latest"

// modelID lets the System One model version be pinned. TypeSafe publishes
// version-scoped limitation pages, so behaviour drifts between versions; an
// unpinned "jev-latest" would let the router change which model handles a
// turn without anyone noticing.
func modelID() string {
	if v := config.Env("JEV_MODEL"); v != "" {
		return v
	}
	return defaultModel
}

type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

func New(apiKey string) *Client { return NewWithBaseURL(apiKey, defaultBaseURL) }

func NewWithBaseURL(apiKey, baseURL string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		http:    &http.Client{Timeout: time.Duration(config.Thresholds.JevTimeoutMS) * time.Millisecond},
	}
}

type answer struct {
	Choice     string  `json:"choice"`
	Confidence float64 `json:"confidence"`
	Score      float64 `json:"score"`
}

type result struct {
	Model   string            `json:"model"`
	Answers map[string]answer `json:"answers"`
}

func (c *Client) Route(ctx context.Context, in router.Input) (*router.Decision, error) {
	if len(in.Models) == 0 {
		return nil, errors.New("no models to choose between")
	}
	body := map[string]any{
		// The model catalog already lives in the choice question's criteria
		// keys; repeating it here would send the same data twice and add
		// context pollution, which TypeSafe's own limitations page names as
		// a direct accuracy risk.
		"state": map[string]any{
			"request": in.Prompt,
			"session": map[string]any{"current_model": in.Current, "context_tokens": in.ContextTokens},
		},
		"questions": config.QuestionsFor(in.Models),
		"model":     modelID(),
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(config.Thresholds.JevDeadlineMS)*time.Millisecond)
	defer cancel()

	started := time.Now()
	var res *result
	var err error
	for attempt := 0; attempt <= config.Thresholds.JevMaxRetries; attempt++ {
		res, err = c.post(ctx, body)
		if err == nil || ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}

	pick, ok := res.Answers["model"]
	if !ok || pick.Choice == "" {
		return nil, errors.New("no model answer")
	}
	norm := func(name string) float64 { return res.Answers[name].Score / config.ComplexityMaxScore }
	ctxSize := float64(in.ContextTokens) / config.ContextWindowTokens
	if ctxSize > 1 {
		ctxSize = 1
	}
	return &router.Decision{
		Choice:     pick.Choice,
		Confidence: pick.Confidence,
		Metrics: router.Metrics{
			TaskComplexity:    norm("task_complexity"),
			ReasoningRequired: norm("reasoning_required"),
			ToolComplexity:    norm("tool_complexity"),
			ContextSize:       ctxSize,
		},
		Request:  body,
		Response: res,
		Elapsed:  time.Since(started),
	}, nil
}

func (c *Client) post(ctx context.Context, body map[string]any) (*result, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/systemone", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("systemone: http %d", resp.StatusCode)
	}
	var out result
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}
