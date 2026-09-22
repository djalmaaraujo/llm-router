package launch

import (
	"io"
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

func TestInstallWritesItsChatterToStderrNotStdout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	stdout := captureOutput(t, &os.Stdout)
	stderr := captureOutput(t, &os.Stderr)

	if code := Install(); code != 0 {
		t.Fatalf("Install returned %d", code)
	}

	if got := stdout(); got != "" {
		t.Errorf("stdout = %q, want it empty: `llmr-claude -p ...` output must be pipeable without router chatter mixed in", got)
	}
	if got := stderr(); !strings.Contains(got, "[llmr] wrote") {
		t.Errorf("stderr = %q, want it to carry the install lines instead", got)
	}
}

// captureOutput redirects *target to a pipe for the rest of the test and
// returns a function that restores the original and reports what was
// written.
func captureOutput(t *testing.T, target **os.File) func() string {
	t.Helper()
	original := *target
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	*target = w

	done := make(chan string, 1)
	go func() {
		buf, _ := io.ReadAll(r)
		done <- string(buf)
	}()

	t.Cleanup(func() {
		w.Close()
		*target = original
		r.Close()
	})

	return func() string {
		w.Close()
		*target = original
		return <-done
	}
}
