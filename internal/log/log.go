// Package log is a debug-only logger, off by default so a normal run never
// writes to disk.
package log

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/djalmaaraujo/llm-router/internal/config"
)

// Path returns ~/.llm-router.log, resolving the home directory on every call
// so a HOME set by a test takes effect.
func Path() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".llm-router.log"
	}
	return filepath.Join(home, ".llm-router.log")
}

// Debug appends a line when config.Env("DEBUG") is set, and does nothing
// otherwise. Every error is swallowed: a broken log file must never take down
// a session. Never pass prompt text, an API key, or an authorization header
// here — decisions, tier names, token counts, and timings only.
func Debug(format string, args ...any) {
	if config.Env("DEBUG") == "" {
		return
	}
	f, err := os.OpenFile(Path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// Dump writes body to <config.Env("DUMP")>.<unix millis>.json when DUMP is
// set, and does nothing otherwise. Claude Code's request shape is
// undocumented and moves; this is how a future wire change gets diagnosed.
// It writes whole bodies on purpose, which is why it is off by default.
func Dump(body []byte) {
	prefix := config.Env("DUMP")
	if prefix == "" {
		return
	}
	path := fmt.Sprintf("%s.%d.json", prefix, time.Now().UnixMilli())
	_ = os.WriteFile(path, body, 0o600)
}
