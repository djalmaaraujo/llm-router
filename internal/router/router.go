// Package router defines what a routing provider must do. Only Jev is
// implemented; the interface exists so a second provider does not require
// touching the policy.
package router

import (
	"context"
	"time"

	"github.com/djalmaaraujo/llm-router/internal/config"
)

type Input struct {
	Prompt        string
	Current       string
	ContextTokens int
	Models        []config.Model
}

type Metrics struct {
	TaskComplexity    float64
	ReasoningRequired float64
	ToolComplexity    float64
	ContextSize       float64
}

// Decision carries the exact request and response so the report can be
// rendered later without asking the provider again.
type Decision struct {
	Choice     string
	Confidence float64
	Metrics    Metrics
	Request    any
	Response   any
	Elapsed    time.Duration
}

type Router interface {
	Route(ctx context.Context, in Input) (*Decision, error)
}
