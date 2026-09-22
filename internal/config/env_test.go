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
