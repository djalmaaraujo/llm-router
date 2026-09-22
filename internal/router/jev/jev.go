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
	"net/url"
	"os"
	"time"

	"github.com/djalmaaraujo/llm-router/internal/config"
	"github.com/djalmaaraujo/llm-router/internal/router"
)

const defaultBaseURL = "https://api.typesafe.ai"

const defaultModel = "jev-latest"

// retryBackoff matches the Node original's initial backoff, and is what
// TypeSafe advises waiting before retrying a 429 or 529.
const retryBackoff = 150 * time.Millisecond

// modelID lets the System One model version be pinned. TypeSafe publishes
// version-scoped limitation pages, so behaviour drifts between versions; an
// unpinned "jev-latest" would let the router change which model handles a
// turn without anyone noticing.
//
// This reads LLMR_JEV_MODEL directly rather than through config.Env: that
// helper exists only to keep the Node original's legacy JEV_* names alive
// through the LLMR_ rename, and JEV_MODEL has no legacy name to preserve.
// Routing it through config.Env would invent a user-facing JEV_JEV_MODEL.
func modelID() string {
	if v := os.Getenv("LLMR_JEV_MODEL"); v != "" {
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

// statusError carries the HTTP status so the retry loop can tell a permanent
// failure (401 invalid key, 422 validation) from a transient one (429 rate
// limited, 529 overloaded) apart.
type statusError struct{ code int }

func (e *statusError) Error() string { return fmt.Sprintf("systemone: http %d", e.code) }

func isRetryable(err error) bool {
	var se *statusError
	if errors.As(err, &se) {
		return se.code == http.StatusTooManyRequests || se.code == 529
	}
	var ue *url.Error
	return errors.As(err, &ue)
}

func (c *Client) Route(ctx context.Context, in router.Input) (*router.Decision, error) {
	if len(in.Models) == 0 {
		return nil, errors.New("no models to choose between")
	}
	body := map[string]any{
		// The model catalog already lives in the choice question's criteria
		// keys; repeating it here would send the same data twice and add
		// context pollution, which TypeSafe's own limitations page names as
		// a direct accuracy risk: https://docs.typesafe.ai/model-jaggedness/jev-1.13
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
		if err == nil {
			break
		}
		if ctx.Err() != nil || !isRetryable(err) || attempt == config.Thresholds.JevMaxRetries {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(retryBackoff):
		}
	}
	if err != nil {
		return nil, err
	}

	pick, ok := res.Answers["model"]
	if !ok || pick.Choice == "" {
		return nil, errors.New("no model answer")
	}
	metric := func(name string) *float64 {
		a, ok := res.Answers[name]
		if !ok {
			return nil
		}
		v := a.Score / config.ComplexityMaxScore
		return &v
	}
	ctxSize := float64(in.ContextTokens) / config.ContextWindowTokens
	if ctxSize > 1 {
		ctxSize = 1
	}
	return &router.Decision{
		Choice:     pick.Choice,
		Confidence: pick.Confidence,
		Metrics: router.Metrics{
			TaskComplexity:    metric("task_complexity"),
			ReasoningRequired: metric("reasoning_required"),
			ToolComplexity:    metric("tool_complexity"),
			ContextSize:       &ctxSize,
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
		return nil, &statusError{code: resp.StatusCode}
	}
	var out result
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}
