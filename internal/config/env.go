package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// LoadEnvFiles reads the project and user env files in order, setting a
// variable only where the real environment leaves it empty. The real
// environment always wins, so a value exported in the shell overrides
// anything a file says.
func LoadEnvFiles() {
	home, _ := os.UserHomeDir()
	files := []string{
		"./.env",
		filepath.Join(home, ".llm-router.env"),
		filepath.Join(home, ".jev-router.env"),
		filepath.Join(home, ".jev-claude.env"),
	}
	for _, file := range files {
		loadEnvFile(file)
	}
}

func loadEnvFile(path string) {
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
		os.Setenv(key, value)
	}
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
