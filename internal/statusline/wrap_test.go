package statusline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSettings(t *testing.T, dir, command string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"statusLine": map[string]string{"type": "command", "command": command},
	})
	if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWrapsTheUserOwnStatusLineInsteadOfReplacingIt(t *testing.T) {
	isolate(t)
	home, _ := os.UserHomeDir()
	writeSettings(t, home, `printf "MINE"`)

	got := Render(stdin("s1"))
	if !strings.HasPrefix(got, "MINE") {
		t.Errorf("the user's own line must come first: %q", got)
	}
	if !strings.Contains(got, "waiting for first prompt") {
		t.Errorf("the routing segment must be appended: %q", got)
	}
}

func TestFallsBackToOurLineWhenTheirCommandFails(t *testing.T) {
	isolate(t)
	home, _ := os.UserHomeDir()
	writeSettings(t, home, `exit 1`)

	got := Render(stdin("s1"))
	if got == "" {
		t.Fatal("a failing user command must not cost both lines")
	}
	if !strings.Contains(got, "context") {
		t.Errorf("our own full line must be rendered: %q", got)
	}
}

func TestDoesNotWrapItself(t *testing.T) {
	isolate(t)
	home, _ := os.UserHomeDir()
	writeSettings(t, home, `/somewhere/llm-router statusline`)

	if command := userStatusLine(); command != "" {
		t.Errorf("a command naming this binary must not be wrapped, got %q", command)
	}
}

func TestProjectSettingsWinOverHome(t *testing.T) {
	isolate(t)
	home, _ := os.UserHomeDir()
	writeSettings(t, home, `printf "HOME"`)
	cwd, _ := os.Getwd()
	writeSettings(t, cwd, `printf "PROJECT"`)

	if got := userStatusLine(); !strings.Contains(got, "PROJECT") {
		t.Errorf("project settings must win, got %q", got)
	}
}
