package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvFilesDoNotOverrideTheRealEnvironment(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("LLMR_API_KEY=from-file\nLLMR_DEBUG=1\n"), 0o600)
	t.Chdir(dir)
	t.Setenv("LLMR_API_KEY", "from-shell")
	t.Setenv("LLMR_DEBUG", "")

	LoadEnvFiles()

	if got := os.Getenv("LLMR_API_KEY"); got != "from-shell" {
		t.Errorf("LLMR_API_KEY = %q, want the shell value to win", got)
	}
	if got := os.Getenv("LLMR_DEBUG"); got != "1" {
		t.Errorf("LLMR_DEBUG = %q, want the file value where the shell is empty", got)
	}
}

func TestProjectLocalEnvIgnoresHTTPSProxy(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("HTTPS_PROXY=http://evil.example\n"), 0o600)
	t.Chdir(dir)
	t.Setenv("HTTPS_PROXY", "")

	LoadEnvFiles()

	if got := os.Getenv("HTTPS_PROXY"); got != "" {
		t.Errorf("HTTPS_PROXY = %q, want a project-local ./.env unable to set an unrelated variable like this", got)
	}
}

func TestProjectLocalEnvAcceptsLLMRVariables(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("LLMR_API_KEY=from-project-env\n"), 0o600)
	t.Chdir(dir)
	t.Setenv("LLMR_API_KEY", "")

	LoadEnvFiles()

	if got := os.Getenv("LLMR_API_KEY"); got != "from-project-env" {
		t.Errorf("LLMR_API_KEY = %q, want the project-local ./.env to still set its own LLMR_ variables", got)
	}
}

func TestUserEnvFileAcceptsAnyKey(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, ".llm-router.env"), []byte("HTTPS_PROXY=http://trusted.example\n"), 0o600)
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", home)
	t.Setenv("HTTPS_PROXY", "")

	LoadEnvFiles()

	if got := os.Getenv("HTTPS_PROXY"); got != "http://trusted.example" {
		t.Errorf("HTTPS_PROXY = %q, want ~/.llm-router.env, the user's own file, to still set any key", got)
	}
}

func TestAPIKeyFallsBackThroughTheLegacyNames(t *testing.T) {
	t.Setenv("LLMR_API_KEY", "")
	t.Setenv("JEV_API_KEY", "")
	t.Setenv("TYPESAFE_API_KEY", "ts")
	if got := APIKey(); got != "ts" {
		t.Errorf("APIKey = %q, want ts", got)
	}
	t.Setenv("JEV_API_KEY", "jev")
	if got := APIKey(); got != "jev" {
		t.Errorf("APIKey = %q, want jev to beat TYPESAFE_API_KEY", got)
	}
}
