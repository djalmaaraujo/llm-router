// Package claude reads and rewrites the request bodies Claude Code sends. The
// body shape is undocumented and moves between CLI releases, so every function
// here is defensive: a missing or unexpected field returns a zero value rather
// than panicking.
package claude

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/djalmaaraujo/llm-router/internal/config"
)

var systemReminder = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

var boundPairs = [][2]string{
	{"exclusiveMinimum", "minimum"},
	{"exclusiveMaximum", "maximum"},
}

// SanitizeSchema converts draft-04 boolean exclusive bounds to the 2020-12
// numeric form. Claude Code performs this conversion itself when talking to
// its provider directly, but skips it behind a custom base URL, so the API
// rejects the request unless we do it here.
func SanitizeSchema(node any) {
	switch v := node.(type) {
	case map[string]any:
		for _, pair := range boundPairs {
			exclusiveKey, plainKey := pair[0], pair[1]
			exclusive, ok := v[exclusiveKey]
			if !ok {
				continue
			}
			flag, ok := exclusive.(bool)
			if !ok {
				continue
			}
			bound, boundOK := v[plainKey]
			if flag && isNumber(bound) && boundOK {
				v[exclusiveKey] = bound
				delete(v, plainKey)
			} else {
				delete(v, exclusiveKey)
			}
		}
		for _, child := range v {
			SanitizeSchema(child)
		}
	case []any:
		for _, child := range v {
			SanitizeSchema(child)
		}
	}
}

func isNumber(v any) bool {
	switch v.(type) {
	case json.Number, float64, int, int64:
		return true
	default:
		return false
	}
}

// NewTurnPrompt returns the user text that opened this turn, or "" when the
// request is not the opening request of a turn. A turn continues across many
// requests while the model works through tool calls, and those continuations
// end in a tool_result; routing them would re-ask the provider on every tool
// call and let the model flip mid-task, so only the opening request counts.
func NewTurnPrompt(body map[string]any) string {
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) == 0 {
		return ""
	}

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		return ""
	}

	// A hook's additionalContext lands as a trailing system-role message
	// appended after the real turn, so the last message is not always the
	// one that started it; skip past any trailing system messages to find it.
	var last map[string]any
	for i := len(messages) - 1; i >= 0; i-- {
		m, ok := messages[i].(map[string]any)
		if !ok {
			return ""
		}
		if m["role"] == "system" {
			continue
		}
		last = m
		break
	}
	if last == nil || last["role"] != "user" {
		return ""
	}

	var text string
	switch content := last["content"].(type) {
	case string:
		text = content
	case []any:
		var parts []string
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "tool_result" {
				return ""
			}
			if b["type"] == "text" {
				if s, ok := b["text"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
		text = strings.Join(parts, "\n")
	default:
		return ""
	}

	text = systemReminder.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

// ApplyTier points a request at a tier and removes the fields that tier cannot
// accept. Claude Code composes the body for whatever model it thinks it is
// talking to, so routing down to Haiku while leaving thinking in place is a
// hard 400.
func ApplyTier(body map[string]any, tier, model string) {
	spec, ok := config.Spec(tier)
	if !ok {
		return
	}
	body["model"] = model

	if !spec.Thinking {
		delete(body, "thinking")
		// A context-management strategy that prunes thinking blocks is itself
		// rejected once thinking is gone, so it has to go with it.
		if cm, ok := body["context_management"].(map[string]any); ok {
			if edits, ok := cm["edits"].([]any); ok {
				kept := edits[:0]
				for _, e := range edits {
					entry, _ := e.(map[string]any)
					kind, _ := entry["type"].(string)
					if !strings.Contains(strings.ToLower(kind), "thinking") {
						kept = append(kept, e)
					}
				}
				if len(kept) == 0 {
					delete(body, "context_management")
				} else {
					cm["edits"] = kept
				}
			}
		}
	}

	if !spec.Effort {
		if oc, ok := body["output_config"].(map[string]any); ok {
			delete(oc, "effort")
			if len(oc) == 0 {
				delete(body, "output_config")
			}
		}
	}
}

// SessionOf reads the session id out of metadata.user_id, which arrives as a
// JSON string that itself contains JSON. Any failure returns "".
func SessionOf(body map[string]any) string {
	metadata, ok := body["metadata"].(map[string]any)
	if !ok {
		return ""
	}
	raw, ok := metadata["user_id"].(string)
	if !ok {
		return ""
	}
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	return payload.SessionID
}

// ConversationKey identifies the conversation a request belongs to. Claude Code
// runs sub-agents through the same endpoint, so one pinned model would let a
// sub-agent's choice leak into the main conversation.
//
// Only stable fields may be used. Claude Code moves its cache_control
// breakpoint between requests and rewrites message metadata, so the key is the
// session id plus the text of the first message, which is fixed once a
// conversation starts and differs between the main agent and each sub-agent.
func ConversationKey(body map[string]any) string {
	var text strings.Builder
	if messages, ok := body["messages"].([]any); ok && len(messages) > 0 {
		if first, ok := messages[0].(map[string]any); ok {
			switch content := first["content"].(type) {
			case string:
				text.WriteString(content)
			case []any:
				for _, block := range content {
					b, _ := block.(map[string]any)
					if b["type"] == "text" {
						s, _ := b["text"].(string)
						text.WriteString(s)
					}
				}
			}
		}
	}
	sum := sha1.Sum([]byte(SessionOf(body) + "|" + text.String()))
	return hex.EncodeToString(sum[:])[:12]
}

// ModelsFrom keeps catalog entries whose id maps to a known tier and builds a
// human-readable description from whatever fields are present. When nothing in
// the catalog matches a tier, it falls back to the static tier list so the
// model picker never comes up empty.
func ModelsFrom(catalog []map[string]any) []config.Model {
	var models []config.Model
	for _, entry := range catalog {
		id, ok := entry["id"].(string)
		if !ok {
			continue
		}
		tier := config.TierOf(id)
		if tier == "" {
			continue
		}
		models = append(models, config.Model{
			ID:          id,
			Tier:        tier,
			Description: describe(entry),
		})
	}
	if len(models) == 0 {
		for _, tier := range config.Tiers {
			models = append(models, config.Model{
				ID:          tier.ID,
				Tier:        tier.Name,
				Description: tier.ID,
			})
		}
	}
	return models
}

func describe(entry map[string]any) string {
	var parts []string
	if name, ok := entry["display_name"].(string); ok && name != "" {
		parts = append(parts, name)
	}
	if created, ok := entry["created_at"].(string); ok && created != "" {
		date := created
		if len(date) > 10 {
			date = date[:10]
		}
		parts = append(parts, "released "+date)
	}
	if tokens, ok := numberString(entry["max_input_tokens"]); ok {
		parts = append(parts, tokens+" input tokens")
	}
	return strings.Join(parts, "; ")
}

// numberString renders a JSON number without scientific notation, whether the
// decoder produced a json.Number or a plain float64. Any other type,
// including a plain Go int, is dropped rather than rendered wrong.
func numberString(v any) (string, bool) {
	switch n := v.(type) {
	case json.Number:
		return n.String(), true
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64), true
	default:
		return "", false
	}
}
