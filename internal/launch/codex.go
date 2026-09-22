package launch

import (
	"fmt"
	"os"

	"github.com/djalmaaraujo/llm-router/internal/config"
	"github.com/djalmaaraujo/llm-router/internal/proxy"
	"github.com/djalmaaraujo/llm-router/internal/proxy/codex"
	"github.com/djalmaaraujo/llm-router/internal/router/jev"
)

// codexUpstreamURL is the ChatGPT backend Codex's own CLI talks to once
// signed in with `codex login`. Targeting it here, rather than the plain
// OpenAI API base, keeps the account's ChatGPT session — not an API key —
// the thing that authenticates every request the proxy forwards.
const codexUpstreamURL = "https://chatgpt.com/backend-api/codex"

const codexProvider = "jev-router"

// Codex runs the Codex CLI, routing every turn through the proxy when an API
// key for the routing provider is available. Codex's own OpenAI credentials
// are untouched either way: they live in the user's existing `codex login`
// session and are simply forwarded.
func Codex(args []string) int {
	exe, err := Resolve("codex")
	if err != nil {
		fmt.Fprintln(os.Stderr, "[llmr] Codex is not installed, or `codex` is not on your PATH.")
		fmt.Fprintln(os.Stderr, "[llmr] llmr-codex runs the real Codex CLI; install it first:")
		fmt.Fprintln(os.Stderr, "[llmr]   https://developers.openai.com/codex/cli")
		return 1
	}

	apiKey := config.APIKey()
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "[llmr] no API key found, running Codex without routing")
		return Run(exe, args, os.Environ())
	}

	handler := codex.New(jev.New(apiKey))
	handler.OnDecision = func(model string, confidence float64) {
		fmt.Fprintf(os.Stderr, "[llmr] routed this turn to %s (jev, confidence %.2f).\n", model, confidence)
	}

	server, err := proxy.Start(codexUpstreamURL, handler.Hooks())
	if err != nil {
		fmt.Fprintln(os.Stderr, "[llmr] failed to start the routing proxy:", err)
		return Run(exe, args, os.Environ())
	}
	defer server.Close()

	args = append(codexProviderArgs(server.URL()), args...)

	return Run(exe, args, os.Environ())
}

// codexProviderArgs builds the -c overrides that register a custom
// "jev-router" model provider pointed at the local proxy and select it for
// this session.
//
// Codex's own --config flag takes dotted key=value TOML overrides, not a
// config file path (verified against the installed CLI's own --help; there
// is no flag that accepts an arbitrary config file). Passing the provider
// this way needs no file on disk and so needs no cleanup: the overrides
// apply only to this one process and vanish when it exits. It also leaves
// the real ~/.codex/config.toml and ~/.codex/auth.json untouched, which
// matters because auth.json is exactly what keeps the user's existing
// `codex login` session authenticating every request this proxy forwards;
// pointing Codex at a substitute config directory would have carried it away
// from that file.
func codexProviderArgs(baseURL string) []string {
	prefix := "model_providers." + codexProvider
	return []string{
		"--config", `model="` + codex.CodexAutoModel + `"`,
		"--config", `model_provider="` + codexProvider + `"`,
		"--config", prefix + `.name="Jev Router"`,
		"--config", prefix + `.base_url="` + baseURL + `"`,
		"--config", prefix + `.wire_api="responses"`,
		"--config", prefix + `.requires_openai_auth=true`,
	}
}
