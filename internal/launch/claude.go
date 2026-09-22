package launch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/djalmaaraujo/llm-router/internal/config"
	"github.com/djalmaaraujo/llm-router/internal/proxy"
	"github.com/djalmaaraujo/llm-router/internal/proxy/claude"
	"github.com/djalmaaraujo/llm-router/internal/router/jev"
)

const anthropicBaseURL = "https://api.anthropic.com"

// Claude runs Claude Code, routing every turn through the proxy when an API
// key is available.
func Claude(args []string) int {
	exe, err := Resolve("claude")
	if err != nil {
		fmt.Fprintln(os.Stderr, "[llmr] Claude Code is not installed, or `claude` is not on your PATH.")
		fmt.Fprintln(os.Stderr, "[llmr] llmr-claude runs the real Claude Code CLI; install it first:")
		fmt.Fprintln(os.Stderr, "[llmr]   https://code.claude.com/docs/en/setup")
		return 1
	}

	settingsFile := UserSettingsPath()
	previousModel := ReadSavedModel(settingsFile)
	defer RestoreSavedModel(previousModel, settingsFile)

	apiKey := config.APIKey()
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "[llmr] no API key found, running Claude Code without routing")
		return Run(exe, args, os.Environ())
	}

	server, err := proxy.Start(anthropicBaseURL, claude.New(jev.New(apiKey)).Hooks())
	if err != nil {
		fmt.Fprintln(os.Stderr, "[llmr] failed to start the routing proxy:", err)
		return Run(exe, args, os.Environ())
	}
	defer server.Close()

	env := os.Environ()
	env = append(env,
		"ANTHROPIC_BASE_URL="+server.URL(),
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1",
		"ANTHROPIC_CUSTOM_MODEL_OPTION=jev-router",
		"ANTHROPIC_CUSTOM_MODEL_OPTION_NAME=Jev Router",
		"ANTHROPIC_CUSTOM_MODEL_OPTION_DESCRIPTION=Route each turn to the cheapest model that can do it",
		"ANTHROPIC_CUSTOM_MODEL_OPTION_SUPPORTED_CAPABILITIES=thinking,adaptive_thinking,interleaved_thinking,effort,max_effort",
		"CLAUDE_CODE_DISABLE_UNKNOWN_MODEL_WINDOW_ENFORCEMENT=1",
	)
	if os.Getenv("ANTHROPIC_MODEL") == "" {
		env = append(env, "ANTHROPIC_MODEL=jev-router")
	}

	if shouldAddStatusLine() {
		if file, err := statusLineSettingsFile(); err == nil {
			args = append(args, "--settings", file)
		}
	}

	return Run(exe, args, env)
}

// shouldAddStatusLine reports whether llmr-claude should add its own status
// line: only when the user opted out neither by env var nor by already
// configuring one themselves.
func shouldAddStatusLine() bool {
	return config.Env("NO_STATUSLINE") == "" && !hasStatusLine()
}

// hasStatusLine reports whether the user already configured a status line, in
// the project or the user settings. Overwriting a deliberate choice silently
// would be worse than showing no status line at all.
func hasStatusLine() bool {
	return definesStatusLine("./.claude/settings.json") || definesStatusLine(UserSettingsPath())
}

func definesStatusLine(file string) bool {
	v, ok := readSettings(file)
	if !ok {
		return false
	}
	_, ok = v["statusLine"]
	return ok
}

func statusLineSettingsFile() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}

	dir := filepath.Join(os.TempDir(), "llm-router")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	settings := map[string]any{
		"statusLine": map[string]any{
			"type":    "command",
			"command": exe + " statusline",
		},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')

	file := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return "", err
	}
	return file, nil
}
