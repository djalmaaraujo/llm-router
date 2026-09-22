package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// projectEnvAllowedPrefixes are the only names LoadEnvFiles accepts out of
// the project-local ./.env. That file lives in whatever repository the
// binary happens to be run inside, unlike the three files under $HOME, which
// are the user's own. Without this filter, a ./.env committed to a cloned
// repository could set HTTPS_PROXY, which http.DefaultTransport (and so this
// proxy's transport) honours, and silently route every upstream request —
// carrying the user's real Authorization header — through a host of the
// repository author's choosing.
var projectEnvAllowedPrefixes = []string{"LLMR_", "JEV_", "TYPESAFE_", "ANTHROPIC_CUSTOM_MODEL_OPTION"}

// LoadEnvFiles reads the project and user env files in order, setting a
// variable only where the real environment leaves it empty. The real
// environment always wins, so a value exported in the shell overrides
// anything a file says.
func LoadEnvFiles() {
	home, _ := os.UserHomeDir()
	loadEnvFile("./.env", projectEnvAllowedPrefixes)
	for _, name := range []string{".llm-router.env", ".jev-router.env", ".jev-claude.env"} {
		loadEnvFile(filepath.Join(home, name), nil)
	}
}

// loadEnvFile reads path and sets each key found, skipping one already set
// in the real environment. allowed, when non-empty, restricts which key
// names are accepted; a key outside it is ignored silently, since a
// project's own unrelated variables in its ./.env are normal and must not
// produce noise. A nil or empty allowed list accepts every key.
func loadEnvFile(path string, allowed []string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = trimQuotes(strings.TrimSpace(value))
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		if len(allowed) > 0 && !hasAnyPrefix(key, allowed) {
			continue
		}
		os.Setenv(key, value)
	}
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func trimQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// APIKey returns the first non-empty of the names this project has used for
// the TypeSafe API key, oldest legacy name last.
func APIKey() string {
	for _, name := range []string{"LLMR_API_KEY", "JEV_API_KEY", "TYPESAFE_API_KEY"} {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}
