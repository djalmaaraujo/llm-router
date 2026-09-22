package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallWritesTheSkillAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if code := Install(); code != 0 {
		t.Fatalf("Install returned %d", code)
	}
	path := filepath.Join(home, ".claude", "skills", "llmr-explain", "SKILL.md")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("skill not written: %v", err)
	}
	if !strings.Contains(string(first), "<jev-explain>") {
		t.Error("the marker the proxy matches must be present")
	}

	if code := Install(); code != 0 {
		t.Fatalf("second Install returned %d", code)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Error("installing twice must leave the same file")
	}
}
