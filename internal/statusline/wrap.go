package statusline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// wrapTimeout bounds a user's own status line command. Theirs may shell out to
// a package manager, so the budget is generous; the point is only that a hung
// command must not wedge the status line forever.
const wrapTimeout = 10 * time.Second

// userStatusLine finds the status line command the user configured for
// themselves, or "" when they configured none.
//
// It reads their settings FILES directly rather than the merged configuration
// Claude Code assembles. The merged view already contains our own command,
// because that is how we get run at all, so reading it would make us wrap
// ourselves without end.
func userStatusLine() string {
	for _, file := range settingsFiles() {
		raw, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var settings struct {
			StatusLine struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"statusLine"`
		}
		if json.Unmarshal(raw, &settings) != nil {
			continue
		}
		command := strings.TrimSpace(settings.StatusLine.Command)
		if command == "" || isOurs(command) {
			continue
		}
		return command
	}
	return ""
}

func settingsFiles() []string {
	files := []string{filepath.Join(".claude", "settings.json")}
	if home, err := os.UserHomeDir(); err == nil {
		files = append(files, filepath.Join(home, ".claude", "settings.json"))
	}
	return files
}

// isOurs reports whether a configured command would re-enter this binary. A
// settings file written by an earlier session can name us, and wrapping that
// would recurse until the status line times out.
func isOurs(command string) bool {
	if strings.Contains(command, "llm-router statusline") || strings.Contains(command, "llmr-statusline") {
		return true
	}
	self, err := os.Executable()
	return err == nil && strings.Contains(command, self)
}

// runUserStatusLine runs the user's own command with the same session JSON on
// stdin and returns what it printed, trimmed of the trailing newline.
//
// Every failure returns "": a status line is cosmetic, and losing the user's
// own line because ours could not run theirs would be worse than showing only
// ours.
func runUserStatusLine(command string, stdin []byte) string {
	ctx, cancel := context.WithTimeout(context.Background(), wrapTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stderr = nil

	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return ""
	}
	return strings.TrimRight(string(out), "\r\n")
}
