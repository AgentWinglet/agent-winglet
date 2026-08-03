package ledger

import "testing"

func TestCheckFirstSightIsNotRepeat(t *testing.T) {
	s := &State{Entries: map[string]Entry{}}
	turn, repeat := s.Check("Bash:echo hi", "hi\n")
	if repeat {
		t.Fatalf("first sight reported as repeat")
	}
	if turn != 0 {
		t.Fatalf("first sight returned nonzero repeatOfTurn: %d", turn)
	}
	if s.Turn != 1 {
		t.Fatalf("turn counter = %d, want 1", s.Turn)
	}
}

func TestCheckExactRepeatIsDetected(t *testing.T) {
	s := &State{Entries: map[string]Entry{}}
	s.Check("Bash:echo hi", "hi\n")
	turn, repeat := s.Check("Bash:echo hi", "hi\n")
	if !repeat {
		t.Fatalf("identical content on same key not detected as repeat")
	}
	if turn != 1 {
		t.Fatalf("repeatOfTurn = %d, want 1", turn)
	}
	if s.Turn != 1 {
		t.Fatalf("turn counter advanced on a repeat: %d", s.Turn)
	}
}

func TestCheckChangedContentIsNotRepeat(t *testing.T) {
	s := &State{Entries: map[string]Entry{}}
	s.Check("Bash:cat f", "v1")
	_, repeat := s.Check("Bash:cat f", "v2")
	if repeat {
		t.Fatalf("changed content on same key reported as repeat")
	}
	if s.Turn != 2 {
		t.Fatalf("turn counter = %d, want 2 after content change", s.Turn)
	}
}

func TestCheckDifferentKeysAreIndependent(t *testing.T) {
	s := &State{Entries: map[string]Entry{}}
	s.Check("Bash:echo a", "a\n")
	_, repeat := s.Check("Bash:echo b", "a\n")
	if repeat {
		t.Fatalf("same content under a different key reported as repeat")
	}
}

func TestLoadMissingFileReturnsEmptyState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	s, err := Load(dir, "session-does-not-exist")
	if err != nil {
		t.Fatalf("Load returned error for missing file: %v", err)
	}
	if s.Entries == nil || len(s.Entries) != 0 {
		t.Fatalf("expected empty entries, got %+v", s.Entries)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "sess-roundtrip"

	s, _ := Load(dir, sessionID)
	s.Check("Bash:echo hi", "hi\n")
	if err := Save(dir, sessionID, s); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	reloaded, err := Load(dir, sessionID)
	if err != nil {
		t.Fatalf("Load after Save failed: %v", err)
	}
	turn, repeat := reloaded.Check("Bash:echo hi", "hi\n")
	if !repeat || turn != 1 {
		t.Fatalf("reloaded state didn't recognize prior entry: turn=%d repeat=%v", turn, repeat)
	}
}

func TestInvalidateRemovesState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "sess-invalidate"

	s, _ := Load(dir, sessionID)
	s.Check("Bash:echo hi", "hi\n")
	if err := Save(dir, sessionID, s); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	if err := Invalidate(dir, sessionID); err != nil {
		t.Fatalf("Invalidate failed: %v", err)
	}

	reloaded, err := Load(dir, sessionID)
	if err != nil {
		t.Fatalf("Load after Invalidate failed: %v", err)
	}
	_, repeat := reloaded.Check("Bash:echo hi", "hi\n")
	if repeat {
		t.Fatalf("entry survived Invalidate — same-session-only constraint violated")
	}
}

func TestInvalidateOnMissingFileIsNotAnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := Invalidate(dir, "never-existed"); err != nil {
		t.Fatalf("Invalidate on nonexistent session errored: %v", err)
	}
}
