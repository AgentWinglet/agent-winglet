package stats

import "testing"

func TestLoadMissingSessionReturnsZeroValue(t *testing.T) {
	dir := t.TempDir()
	s, err := LoadSession(dir, "session-does-not-exist")
	if err != nil {
		t.Fatalf("LoadSession returned error for missing file: %v", err)
	}
	if !s.IsZero() {
		t.Fatalf("expected zero-value session, got %+v", s)
	}
}

func TestRecordDedupAccumulates(t *testing.T) {
	s := &Session{}
	s.RecordDedup(100)
	s.RecordDedup(50)
	if s.DedupHits != 2 || s.DedupBytes != 150 {
		t.Fatalf("got DedupHits=%d DedupBytes=%d, want 2/150", s.DedupHits, s.DedupBytes)
	}
	if s.IsZero() {
		t.Fatalf("session with recorded activity reported as zero")
	}
}

func TestRecordBudgetTrimAccumulates(t *testing.T) {
	s := &Session{}
	s.RecordBudgetTrim(30)
	s.RecordBudgetTrim(10)
	if s.BudgetTrims != 2 || s.BudgetLinesOmitted != 40 {
		t.Fatalf("got BudgetTrims=%d BudgetLinesOmitted=%d, want 2/40", s.BudgetTrims, s.BudgetLinesOmitted)
	}
}

func TestRecordRetireAccumulates(t *testing.T) {
	s := &Session{}
	s.RecordRetire(200)
	if s.RetiredCalls != 1 || s.RetiredBytes != 200 {
		t.Fatalf("got RetiredCalls=%d RetiredBytes=%d, want 1/200", s.RetiredCalls, s.RetiredBytes)
	}
}

func TestSessionSaveThenLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	sessionID := "sess-roundtrip"

	s, _ := LoadSession(dir, sessionID)
	s.RecordDedup(10)
	s.RecordBudgetTrim(5)
	s.RecordRetire(20)
	if err := SaveSession(dir, sessionID, s); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	reloaded, err := LoadSession(dir, sessionID)
	if err != nil {
		t.Fatalf("LoadSession after Save failed: %v", err)
	}
	if *reloaded != *s {
		t.Fatalf("reloaded session = %+v, want %+v", reloaded, s)
	}
}

func TestInvalidateSessionRemovesState(t *testing.T) {
	dir := t.TempDir()
	sessionID := "sess-invalidate"

	s, _ := LoadSession(dir, sessionID)
	s.RecordDedup(10)
	if err := SaveSession(dir, sessionID, s); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	if err := InvalidateSession(dir, sessionID); err != nil {
		t.Fatalf("InvalidateSession failed: %v", err)
	}

	reloaded, err := LoadSession(dir, sessionID)
	if err != nil {
		t.Fatalf("LoadSession after Invalidate failed: %v", err)
	}
	if !reloaded.IsZero() {
		t.Fatalf("session tally survived InvalidateSession, got %+v", reloaded)
	}
}

func TestInvalidateSessionOnMissingFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	if err := InvalidateSession(dir, "never-existed"); err != nil {
		t.Fatalf("InvalidateSession on nonexistent session errored: %v", err)
	}
}

func TestLifetimeAddAccumulatesAcrossSessions(t *testing.T) {
	dir := t.TempDir()

	l, err := LoadLifetime(dir)
	if err != nil {
		t.Fatalf("LoadLifetime failed: %v", err)
	}
	if l.Sessions != 0 {
		t.Fatalf("expected fresh lifetime tally, got %+v", l)
	}

	s1 := &Session{}
	s1.RecordDedup(100)
	l.Add(s1)

	s2 := &Session{}
	s2.RecordDedup(50)
	s2.RecordRetire(25)
	l.Add(s2)

	if l.Sessions != 2 {
		t.Fatalf("Sessions = %d, want 2", l.Sessions)
	}
	if l.DedupHits != 2 || l.DedupBytes != 150 {
		t.Fatalf("got DedupHits=%d DedupBytes=%d, want 2/150", l.DedupHits, l.DedupBytes)
	}
	if l.RetiredCalls != 1 || l.RetiredBytes != 25 {
		t.Fatalf("got RetiredCalls=%d RetiredBytes=%d, want 1/25", l.RetiredCalls, l.RetiredBytes)
	}
}

func TestLifetimeSurvivesSessionInvalidation(t *testing.T) {
	dir := t.TempDir()
	sessionID := "sess1"

	l, _ := LoadLifetime(dir)
	s := &Session{}
	s.RecordDedup(10)
	l.Add(s)
	if err := SaveLifetime(dir, l); err != nil {
		t.Fatalf("SaveLifetime failed: %v", err)
	}
	if err := SaveSession(dir, sessionID, s); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	// Invalidating the session tally (as SessionStart/PostCompact do) must
	// not touch the lifetime tally — it isn't keyed by session_id at all.
	if err := InvalidateSession(dir, sessionID); err != nil {
		t.Fatalf("InvalidateSession failed: %v", err)
	}

	reloaded, err := LoadLifetime(dir)
	if err != nil {
		t.Fatalf("LoadLifetime after session invalidation failed: %v", err)
	}
	if reloaded.Sessions != 1 || reloaded.DedupHits != 1 || reloaded.DedupBytes != 10 {
		t.Fatalf("lifetime tally changed after session invalidation: %+v", reloaded)
	}
}

func TestLifetimeSaveThenLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()

	l, _ := LoadLifetime(dir)
	s := &Session{}
	s.RecordBudgetTrim(15)
	l.Add(s)
	if err := SaveLifetime(dir, l); err != nil {
		t.Fatalf("SaveLifetime failed: %v", err)
	}

	reloaded, err := LoadLifetime(dir)
	if err != nil {
		t.Fatalf("LoadLifetime failed: %v", err)
	}
	if *reloaded != *l {
		t.Fatalf("reloaded lifetime = %+v, want %+v", reloaded, l)
	}
}
