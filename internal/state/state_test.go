package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
