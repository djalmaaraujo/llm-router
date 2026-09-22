// Package state records each turn's routing decision to a per-session file
// so the status line and the explanation report can read it back. The file
// holds prompt text and the exact exchange with the routing provider, so it
// is sensitive: it lives under owner-only permissions in the OS temp
// directory.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/djalmaaraujo/llm-router/internal/router"
)

const historyLimit = 20

var sessionIDPattern = regexp.MustCompile(`[^A-Za-z0-9_-]`)

var pruneOnce sync.Once

// Status is one turn's routing decision. Later tasks read these exact field
// names and JSON tags, so they must stay stable across the Node and Go
// versions of the router.
type Status struct {
	Tier          string          `json:"tier"`
	Model         string          `json:"model"`
	Prompt        string          `json:"prompt"`
	Reason        string          `json:"reason"`
	Confidence    *float64        `json:"confidence"`
	Manual        bool            `json:"manual"`
	At            int64           `json:"at"`
	Metrics       *router.Metrics `json:"metrics"`
	BreakEven     float64         `json:"breakEven"`
	Rebuild       float64         `json:"rebuild"`
	SavingPerTurn float64         `json:"savingPerTurn"`
	Horizon       int             `json:"horizon"`
	Request       any             `json:"request"`
	Response      any             `json:"response"`
	History       []Status        `json:"history,omitempty"`
}

// Dir returns the directory status files live in. It reads os.TempDir on
// every call, rather than caching it, so a test that sets TMPDIR takes
// effect immediately.
func Dir() string {
	return filepath.Join(os.TempDir(), "llm-router")
}

func path(sessionID string) string {
	safe := sessionIDPattern.ReplaceAllString(sessionID, "")
	if safe == "" {
		return ""
	}
	return filepath.Join(Dir(), safe+".json")
}

// Write records s under sessionID. It never returns an error, never panics
// and never blocks a request on a failure: status display is cosmetic and
// must not affect routing.
func Write(sessionID string, s Status) {
	pruneOnce.Do(func() {
		PruneStale(7*24*time.Hour, time.Now())
	})

	p := path(sessionID)
	if p == "" {
		return
	}

	dir := Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	// MkdirAll's mode only applies when it creates the directory; a
	// directory left by an earlier version may still be looser than 0700.
	_ = os.Chmod(dir, 0o700)

	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return
	}
	// WriteFile's mode only applies when it creates the file; a file left
	// by an earlier version may still be looser than 0600.
	_ = os.Chmod(p, 0o600)
}

// WriteDecision appends s to the session's history, keeping only the most
// recent entries, then writes the result.
func WriteDecision(sessionID string, s Status) {
	current := Read(sessionID)

	entry := s
	// Clearing History on the appended copy stops the file nesting a
	// history inside a history inside a history on every write.
	entry.History = nil

	var history []Status
	if current != nil {
		history = current.History
	}
	history = append(history, entry)
	if len(history) > historyLimit {
		history = history[len(history)-historyLimit:]
	}

	s.History = history
	Write(sessionID, s)
}

// Read returns the status recorded for sessionID, or nil on any error:
// missing file, bad JSON or a permissions problem.
func Read(sessionID string) *Status {
	p := path(sessionID)
	if p == "" {
		return nil
	}

	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}

	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return nil
	}
	return &s
}

// PruneStale deletes .json files in Dir() whose modification time is older
// than maxAge relative to now, and returns how many it removed.
func PruneStale(maxAge time.Duration, now time.Time) int {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		return 0
	}

	removed := 0
	cutoff := now.Add(-maxAge)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			// Another session may have removed the file already.
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(Dir(), entry.Name())); err == nil {
				removed++
			}
		}
	}
	return removed
}
