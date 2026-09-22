// Package launch resolves and runs the underlying CLI (Claude Code, Codex)
// under the router's proxy.
package launch

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/djalmaaraujo/llm-router/internal/config"
)

// UserSettingsPath is Claude Code's own settings file.
func UserSettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

// ReadSavedModel returns the model saved in file, or "" when there is none,
// the file cannot be read, or the saved value is our sentinel rather than a
// real user preference.
func ReadSavedModel(file string) string {
	v, ok := readSettings(file)
	if !ok {
		return ""
	}
	model, _ := v["model"].(string)
	if config.IsAuto(model) {
		return ""
	}
	return model
}

// RestoreSavedModel puts previous back into file's "model" key, but only when
// that key still holds our sentinel. Choosing a row in the CLI's model picker
// with Enter saves it as the default for new sessions; leaving the sentinel
// there would break plain `claude`, which has no proxy to resolve it. A real
// model the user picked during the session must be left alone.
func RestoreSavedModel(previous, file string) bool {
	v, ok := readSettings(file)
	if !ok {
		return false
	}
	model, _ := v["model"].(string)
	if !config.IsAuto(model) {
		return false
	}
	if previous == "" {
		delete(v, "model")
	} else {
		v["model"] = previous
	}
	return writeSettings(file, v)
}

func readSettings(file string) (map[string]any, bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, false
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, false
	}
	return v, true
}

func writeSettings(file string, v map[string]any) bool {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return false
	}
	data = append(data, '\n')
	return os.WriteFile(file, data, 0o600) == nil
}
