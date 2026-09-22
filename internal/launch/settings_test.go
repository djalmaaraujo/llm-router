package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, contents string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(file, []byte(contents), 0o600)
	return file
}

func TestReadSavedModelIgnoresTheSentinel(t *testing.T) {
	if got := ReadSavedModel(write(t, `{"model":"jev-router"}`)); got != "" {
		t.Errorf("got %q: a sentinel left by a crashed session is not a preference", got)
	}
	if got := ReadSavedModel(write(t, `{"model":"opus"}`)); got != "opus" {
		t.Errorf("got %q, want opus", got)
	}
	if got := ReadSavedModel(filepath.Join(t.TempDir(), "missing.json")); got != "" {
		t.Errorf("got %q, want empty for a missing file", got)
	}
}

func TestRestoreSavedModelPutsThePreviousValueBack(t *testing.T) {
	file := write(t, `{"model":"jev-router","theme":"dark"}`)
	if !RestoreSavedModel("opus", file) {
		t.Fatal("restore must report that it acted")
	}
	got, _ := os.ReadFile(file)
	if !strings.Contains(string(got), `"model": "opus"`) {
		t.Errorf("settings = %s", got)
	}
	if !strings.Contains(string(got), `"theme": "dark"`) {
		t.Error("restore must not drop unrelated settings")
	}
}

func TestRestoreSavedModelRemovesTheKeyWhenThereWasNone(t *testing.T) {
	file := write(t, `{"model":"jev-router"}`)
	RestoreSavedModel("", file)
	got, _ := os.ReadFile(file)
	if strings.Contains(string(got), "model") {
		t.Errorf("settings = %s, want the key gone", got)
	}
}

func TestRestoreSavedModelLeavesARealChoiceAlone(t *testing.T) {
	file := write(t, `{"model":"claude-opus-5"}`)
	if RestoreSavedModel("haiku", file) {
		t.Error("a model the user picked during the session must survive")
	}
}
