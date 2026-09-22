// Package codex reads and rewrites the request bodies Codex sends to the
// Responses API. The shape differs from Claude Code's own wire format in
// three ways: tool definitions live inside the `input` array rather than a
// top-level `tools` field, `prompt_cache_key` names the conversation
// directly, and tiers map to Codex model slugs rather than Claude model ids.
package codex

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/djalmaaraujo/llm-router/internal/config"
)

var systemReminder = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// CodexAutoModel is the sentinel offered as an extra row in Codex's own model
// picker. Seeing it in a request body is an exact signal that the turn
// should be routed.
const CodexAutoModel = "jev-router"

type codexTier struct {
	tier    string
	def     string
	envName string
}

var codexTiers = []codexTier{
	{"haiku", "gpt-5.6-luna", "CODEX_FAST_MODEL"},
	{"sonnet", "gpt-5.6-terra", "CODEX_BALANCED_MODEL"},
	{"opus", "gpt-5.6-sol", "CODEX_STRONG_MODEL"},
	{"fable", "gpt-6-astra", "CODEX_LONG_MODEL"},
}

// ModelFor names the Codex model slug for tier, honouring an env override
// through config.Env. It returns "" for an unknown tier.
func ModelFor(tier string) string {
	for _, t := range codexTiers {
		if t.tier != tier {
			continue
		}
		if v := config.Env(t.envName); v != "" {
			return v
		}
		return t.def
	}
	return ""
}

// NewTurnPrompt returns the user text that opened this turn, or "" when the
// request is not the opening request of a turn.
//
// An auxiliary call (Codex composing its own prompt, with no tool
// definitions offered) carries neither top-level `tools` nor the older
// `additional_tools` input item; that alone rules it out. A genuine turn is
// then found by walking `input`
// backwards: a tool-loop continuation ends in a function_call_output or
// custom_tool_call_output, which must not be routed again. Any other
// trailing item that is neither of those nor a user turn — a reasoning
// summary, a developer-role note, anything Codex or a future CLI release
// tacks on after the real turn — is skipped rather than treated as
// disqualifying, so the loop still finds the user text underneath it. This
// mirrors a bug found on the Claude side, where a hook-appended trailing
// system message silently defeated routing because only an exact last-item
// match was accepted.
func NewTurnPrompt(body map[string]any) string {
	input, ok := body["input"].([]any)
	if !ok || len(input) == 0 {
		return ""
	}

	if !hasTools(body, input) {
		return ""
	}

	for i := len(input) - 1; i >= 0; i-- {
		item, ok := input[i].(map[string]any)
		if !ok {
			continue
		}
		switch item["type"] {
		case "function_call_output", "custom_tool_call_output":
			return ""
		}
		if item["role"] != "user" {
			continue
		}
		text := codexUserText(item)
		if isAuxiliaryPrompt(text) {
			return ""
		}
		return text
	}
	return ""
}

func hasTools(body map[string]any, input []any) bool {
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		return true
	}
	return hasAdditionalTools(input)
}

func hasAdditionalTools(input []any) bool {
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if ok && item["type"] == "additional_tools" {
			return true
		}
	}
	return false
}

func isAuxiliaryPrompt(text string) bool {
	return strings.HasPrefix(text, "<user_instructions>") || strings.HasPrefix(text, "<environment_context>")
}

func codexUserText(item map[string]any) string {
	content, ok := item["content"].([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, raw := range content {
		block, ok := raw.(map[string]any)
		if !ok || block["type"] != "input_text" {
			continue
		}
		if s, ok := block["text"].(string); ok {
			parts = append(parts, s)
		}
	}
	text := strings.Join(parts, "\n")
	text = systemReminder.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

// ConversationKey identifies the conversation a request belongs to. Codex
// hands us a stable prompt_cache_key directly, so unlike Claude Code there is
// no need to hash the opening message to tell conversations apart; that is
// only a fallback for whichever of Codex's own identifiers is missing.
func ConversationKey(body map[string]any) string {
	basis, _ := body["prompt_cache_key"].(string)
	if basis == "" {
		if cm, ok := body["client_metadata"].(map[string]any); ok {
			basis, _ = cm["x-codex-turn-metadata"].(string)
		}
	}
	if basis == "" {
		instructions, _ := body["instructions"].(string)
		basis = instructions + "|" + firstUserText(body)
	}
	sum := sha1.Sum([]byte(basis))
	return hex.EncodeToString(sum[:])[:12]
}

func firstUserText(body map[string]any) string {
	input, ok := body["input"].([]any)
	if !ok {
		return ""
	}
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok || item["role"] != "user" {
			continue
		}
		return codexUserText(item)
	}
	return ""
}

// AddJevModel returns catalog with an extra row naming CodexAutoModel, copied
// from the account's real gpt-5.6-terra entry (or, failing that, the first
// row visible in the picker), so the sentinel appears in Codex's native model
// list. It is idempotent: calling it again on its own output changes nothing.
func AddJevModel(catalog map[string]any) map[string]any {
	models, ok := catalog["models"].([]any)
	if !ok {
		return catalog
	}
	for _, raw := range models {
		if row, ok := raw.(map[string]any); ok && row["slug"] == CodexAutoModel {
			return catalog
		}
	}

	template := findModelRow(models, "gpt-5.6-terra")
	if template == nil {
		template = findFirstListed(models)
	}
	if template == nil {
		return catalog
	}

	row := make(map[string]any, len(template))
	for k, v := range template {
		row[k] = v
	}
	row["slug"] = CodexAutoModel
	row["display_name"] = "Jev Router"

	catalog["models"] = append(models, row)
	return catalog
}

func findModelRow(models []any, slug string) map[string]any {
	for _, raw := range models {
		if row, ok := raw.(map[string]any); ok && row["slug"] == slug {
			return row
		}
	}
	return nil
}

func findFirstListed(models []any) map[string]any {
	for _, raw := range models {
		if row, ok := raw.(map[string]any); ok && row["visibility"] == "list" {
			return row
		}
	}
	return nil
}
