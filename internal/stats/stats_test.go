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
	s.RecordBudgetTrim(30, 300)
	s.RecordBudgetTrim(10, 100)
	if s.BudgetTrims != 2 || s.BudgetLinesOmitted != 40 || s.BudgetBytesOmitted != 400 {
		t.Fatalf("got BudgetTrims=%d BudgetLinesOmitted=%d BudgetBytesOmitted=%d, want 2/40/400",
			s.BudgetTrims, s.BudgetLinesOmitted, s.BudgetBytesOmitted)
	}
}

func TestRecordRetireAccumulates(t *testing.T) {
	s := &Session{}
	s.RecordRetire(200)
	if s.RetiredCalls != 1 || s.RetiredBytes != 200 {
		t.Fatalf("got RetiredCalls=%d RetiredBytes=%d, want 1/200", s.RetiredCalls, s.RetiredBytes)
	}
}

func TestRecordProcessedAccumulates(t *testing.T) {
	s := &Session{}
	s.RecordProcessed(100)
	s.RecordProcessed(50)
	if s.ProcessedBytes != 150 {
		t.Fatalf("got ProcessedBytes=%d, want 150", s.ProcessedBytes)
	}
}

func TestIsZeroIgnoresProcessedBytes(t *testing.T) {
	s := &Session{}
	s.RecordProcessed(1000)
	if !s.IsZero() {
		t.Fatalf("a session with only ProcessedBytes recorded (no mechanism fired) should still report zero")
	}
}

func TestPercentReportsNoDataYetWhenNothingProcessed(t *testing.T) {
	if _, ok := Percent(0, 0, 0, 0); ok {
		t.Fatalf("Percent with zero processed bytes should report ok=false, not a computed percentage")
	}
}

func TestPercentComputesSuppressedFractionOfProcessed(t *testing.T) {
	pct, ok := Percent(20, 10, 8, 100)
	if !ok {
		t.Fatalf("Percent with nonzero processed bytes should report ok=true")
	}
	if pct != 38 {
		t.Fatalf("Percent = %v, want 38", pct)
	}
}

func TestPartPercent(t *testing.T) {
	pct, ok := PartPercent(12, 100)
	if !ok || pct != 12 {
		t.Fatalf("PartPercent = %v/%v, want 12/true", pct, ok)
	}
	if _, ok := PartPercent(0, 0); ok {
		t.Fatalf("PartPercent with zero processed bytes should report ok=false")
	}
}

func TestStretch(t *testing.T) {
	if got := Stretch(50); got != 2 {
		t.Fatalf("Stretch(50) = %v, want 2", got)
	}
	if got := Stretch(0); got != 1 {
		t.Fatalf("Stretch(0) = %v, want 1", got)
	}
	if got := Stretch(100); got != 0 {
		t.Fatalf("Stretch(100) should guard against divide-by-zero, got %v", got)
	}
}

func TestSessionSaveThenLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	sessionID := "sess-roundtrip"

	s, _ := LoadSession(dir, sessionID)
	s.RecordDedup(10)
	s.RecordBudgetTrim(5, 50)
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
	s.RecordBudgetTrim(15, 150)
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
