package log

import (
	"os"
	"testing"
)

func TestDebugIsSilentUnlessAskedFor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LLMR_DEBUG", "")
	t.Setenv("JEV_DEBUG", "")
	Debug("routed %s", "haiku")
	if _, err := os.Stat(Path()); err == nil {
		t.Error("no log file may appear when debugging is off")
	}
}

func TestDebugWritesAnOwnerOnlyFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LLMR_DEBUG", "1")
	Debug("routed %s", "haiku")
	info, err := os.Stat(Path())
	if err != nil {
		t.Fatalf("no log file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	body, _ := os.ReadFile(Path())
	if len(body) == 0 {
		t.Error("the line was not written")
	}
}
