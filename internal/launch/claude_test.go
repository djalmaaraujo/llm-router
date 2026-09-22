package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShouldAddStatusLineWhenNoSettingsFileExistsAnywhere(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	if !shouldAddStatusLine() {
		t.Error("shouldAddStatusLine = false, want true: nothing defines one")
	}
}

func TestShouldAddStatusLineIsFalseWhenLLMRNoStatuslineIsSet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv("LLMR_NO_STATUSLINE", "1")

	if shouldAddStatusLine() {
		t.Error("shouldAddStatusLine = true, want false: the user opted out")
	}
}

func TestShouldAddStatusLineIsTrueWhenTheSettingsFileIsMalformedJSON(t *testing.T) {
	// A file that exists but fails to parse carries no evidence either way, so
	// it is treated the same as no file: shouldAddStatusLine reports true and
	// llmr-claude still adds its own status line, rather than staying silent
	// forever because a config file happened to get corrupted. It must not
	// panic either way.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	writeUserSettings(t, home, `{not valid json`)

	if !shouldAddStatusLine() {
		t.Error("shouldAddStatusLine = false, want true: malformed JSON must not panic or count as a user-defined status line")
	}
}

func TestStatusLineSettingsFileWritesTheCommandUnderTheTempDir(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	file, err := statusLineSettingsFile()
	if err != nil {
		t.Fatalf("statusLineSettingsFile: %v", err)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("the file it named was not written: %v", err)
	}

	exe, _ := os.Executable()
	want := exe + " statusline"
	if got := string(data); !strings.Contains(got, want) {
		t.Errorf("settings = %s, want it to run %q", got, want)
	}
}

func writeProjectSettings(t *testing.T, dir, contents string) {
	t.Helper()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeUserSettings(t *testing.T, home, contents string) {
	t.Helper()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The renderer wraps a status line the user configured rather than replacing
// it, so its presence is no longer a reason to skip installing ours. Only an
// explicit opt-out stops it.
func TestShouldAddStatusLineEvenWhenTheUserConfiguredOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	writeUserSettings(t, home, `{"statusLine":{"type":"command","command":"printf MINE"}}`)

	if !shouldAddStatusLine() {
		t.Error("ours must still be installed; the renderer composes with theirs")
	}
}
