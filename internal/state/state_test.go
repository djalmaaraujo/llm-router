package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmaaraujo/llm-router/internal/router"
)

func TestRoundTripsPerSessionAndMissesCleanly(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	Write("abc", Status{Tier: "haiku", Model: "claude-haiku-4-5-20251001"})
	got := Read("abc")
	if got == nil || got.Tier != "haiku" {
		t.Fatalf("Read = %+v, want the haiku status", got)
	}
	if Read("no-such-session") != nil {
		t.Error("an unknown session must read as nil")
	}
}

func TestFilesAreReadableOnlyByTheOwner(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	Write("abc", Status{Tier: "opus"})
	info, err := os.Stat(filepath.Join(Dir(), "abc.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600; it holds prompt text", info.Mode().Perm())
	}
	dir, err := os.Stat(Dir())
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", dir.Mode().Perm())
	}
}

func TestSessionIDsCannotEscapeTheDirectory(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	Write("../../escape", Status{Tier: "opus"})
	if _, err := os.Stat(filepath.Join(Dir(), "..", "..", "escape.json")); err == nil {
		t.Fatal("a session id must never become a path traversal")
	}
}

func TestWriteDecisionKeepsTwentyEntries(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for i := 0; i < 25; i++ {
		WriteDecision("abc", Status{Tier: "haiku", Prompt: string(rune('a' + i))})
	}
	got := Read("abc")
	if got == nil || len(got.History) != 20 {
		t.Fatalf("history length = %d, want 20", len(got.History))
	}
}

func TestPruneStaleRemovesOldFilesOnly(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	Write("fresh", Status{Tier: "haiku"})
	Write("old", Status{Tier: "haiku"})
	old := filepath.Join(Dir(), "old.json")
	past := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(old, past, past)

	if removed := PruneStale(7*24*time.Hour, time.Now()); removed != 1 {
		t.Errorf("removed %d files, want 1", removed)
	}
	if Read("fresh") == nil {
		t.Error("the fresh session must survive")
	}
	if Read("old") != nil {
		t.Error("the stale session must be gone")
	}
}

func TestWriteDecisionDoesNotNestHistory(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for i := 0; i < 25; i++ {
		WriteDecision("abc", Status{Tier: "haiku", Prompt: string(rune('a' + i))})
	}

	path := filepath.Join(Dir(), "abc.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 20*1024 {
		t.Errorf("file size = %d bytes, want it to stay small; history may be nesting", info.Size())
	}

	got := Read("abc")
	if got == nil {
		t.Fatal("Read = nil, want a status")
	}
	for i, entry := range got.History {
		if len(entry.History) != 0 {
			t.Errorf("history entry %d has a non-empty nested history: %+v", i, entry.History)
		}
	}
}

func TestNilMetricsSurviveTheRoundTripAsNil(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	reasoning := 0.7
	Write("abc", Status{
		Tier: "haiku",
		Metrics: &router.Metrics{
			TaskComplexity:    nil,
			ReasoningRequired: &reasoning,
			ToolComplexity:    nil,
			ContextSize:       nil,
		},
		Confidence: nil,
	})

	got := Read("abc")
	if got == nil {
		t.Fatal("Read = nil, want a status")
	}
	if got.Confidence != nil {
		t.Errorf("Confidence = %v, want nil", *got.Confidence)
	}
	if got.Metrics == nil {
		t.Fatal("Metrics = nil, want a metrics struct")
	}
	if got.Metrics.TaskComplexity != nil {
		t.Errorf("TaskComplexity = %v, want nil", *got.Metrics.TaskComplexity)
	}
	if got.Metrics.ToolComplexity != nil {
		t.Errorf("ToolComplexity = %v, want nil", *got.Metrics.ToolComplexity)
	}
	if got.Metrics.ContextSize != nil {
		t.Errorf("ContextSize = %v, want nil", *got.Metrics.ContextSize)
	}
	if got.Metrics.ReasoningRequired == nil {
		t.Fatal("ReasoningRequired = nil, want 0.7")
	}
	if *got.Metrics.ReasoningRequired != 0.7 {
		t.Errorf("ReasoningRequired = %v, want 0.7", *got.Metrics.ReasoningRequired)
	}
}

func TestSessionIDSanitiserDefeatsHostileShapes(t *testing.T) {
	nullOnly := strings.Repeat("\x00", 5000)

	cases := []struct {
		name        string
		sessionID   string
		reducesToNo bool
	}{
		{"parent traversal", "../../escape", false},
		{"absolute path", "/etc/passwd", false},
		{"embedded null byte", "abc\x00def", false},
		{"only dots", "...", true},
		{"only slashes", "///", true},
		{"long null-byte id", nullOnly, true},
		{"non-ASCII combining marks with some ASCII", "abć́-1", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())

			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("Write panicked on %q: %v", tc.sessionID, r)
					}
				}()
				Write(tc.sessionID, Status{Tier: "haiku"})
			}()

			if tc.reducesToNo {
				entries, err := os.ReadDir(Dir())
				if err != nil && !os.IsNotExist(err) {
					t.Fatalf("ReadDir(Dir()): %v", err)
				}
				if len(entries) != 0 {
					t.Errorf("store has %d files, want 0 for an id that reduces to empty", len(entries))
				}
				return
			}

			err := filepath.Walk(filepath.Dir(Dir()), func(p string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil
				}
				if filepath.Ext(p) != ".json" {
					return nil
				}
				rel, relErr := filepath.Rel(Dir(), p)
				if relErr != nil || strings.HasPrefix(rel, "..") {
					t.Errorf("found a file outside the store: %s", p)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("walk failed: %v", err)
			}
		})
	}
}

func TestWriteTightensPreExistingLoosePermissions(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	if err := os.MkdirAll(Dir(), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(Dir(), 0o777); err != nil {
		t.Fatal(err)
	}
	loosePath := filepath.Join(Dir(), "abc.json")
	if err := os.WriteFile(loosePath, []byte(`{"tier":"haiku"}`), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loosePath, 0o666); err != nil {
		t.Fatal(err)
	}

	Write("abc", Status{Tier: "opus"})

	dirInfo, err := os.Stat(Dir())
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700 after tightening a pre-existing 0777 dir", dirInfo.Mode().Perm())
	}

	fileInfo, err := os.Stat(loosePath)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600 after tightening a pre-existing 0666 file", fileInfo.Mode().Perm())
	}
}

func TestPruneStaleToleratesNonJSONEntriesAndSubdirectories(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	Write("fresh", Status{Tier: "haiku"})
	Write("stale", Status{Tier: "haiku"})

	stalePath := filepath.Join(Dir(), "stale.json")
	past := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(stalePath, past, past); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(filepath.Join(Dir(), "a-subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir(), "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	var removed int
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("PruneStale panicked: %v", r)
			}
		}()
		removed = PruneStale(7*24*time.Hour, time.Now())
	}()

	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if Read("fresh") == nil {
		t.Error("the fresh session must survive")
	}
	if Read("stale") != nil {
		t.Error("the stale session must be gone")
	}
	if _, err := os.Stat(filepath.Join(Dir(), "a-subdir")); err != nil {
		t.Errorf("subdirectory must survive PruneStale: %v", err)
	}
	if _, err := os.Stat(filepath.Join(Dir(), "notes.txt")); err != nil {
		t.Errorf("non-.json file must survive PruneStale: %v", err)
	}
}
