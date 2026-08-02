package phase

import "testing"

func TestObserveNeitherClassificationNeverCrosses(t *testing.T) {
	s := &State{}
	if crossed := s.Observe(false, false); crossed {
		t.Fatalf("unclassified call reported a crossing")
	}
	if s.Investigated {
		t.Fatalf("unclassified call marked Investigated")
	}
}

func TestObserveImplementBeforeInvestigateDoesNotCross(t *testing.T) {
	s := &State{}
	if crossed := s.Observe(false, true); crossed {
		t.Fatalf("implement call with no prior investigate call reported a crossing")
	}
}

func TestObserveInvestigateThenImplementCrosses(t *testing.T) {
	s := &State{}
	s.Observe(true, false)
	if crossed := s.Observe(false, true); !crossed {
		t.Fatalf("implement call after an investigate call did not report a crossing")
	}
}

func TestObserveOnlyCrossesOnce(t *testing.T) {
	s := &State{}
	s.Observe(true, false)
	s.Observe(false, true)
	if crossed := s.Observe(false, true); crossed {
		t.Fatalf("second implement call reported a second crossing")
	}
}

func TestObserveFiresAgainAfterInterveningInvestigate(t *testing.T) {
	// Not a requirement, just documents current behavior: Suggested latches
	// for the whole state lifetime, so a later investigate→implement swing
	// within the same (uninvalidated) session does not re-fire.
	s := &State{}
	s.Observe(true, false)
	s.Observe(false, true)
	s.Observe(true, false)
	if crossed := s.Observe(false, true); crossed {
		t.Fatalf("Suggested latch did not hold across a second investigate→implement swing")
	}
}

func TestLoadMissingFileReturnsZeroState(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(dir, "session-does-not-exist")
	if err != nil {
		t.Fatalf("Load returned error for missing file: %v", err)
	}
	if s.Investigated || s.Suggested {
		t.Fatalf("expected zero-value state, got %+v", s)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	sessionID := "sess-roundtrip"

	s, _ := Load(dir, sessionID)
	s.Observe(true, false)
	s.Observe(false, true)
	if err := Save(dir, sessionID, s); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	reloaded, err := Load(dir, sessionID)
	if err != nil {
		t.Fatalf("Load after Save failed: %v", err)
	}
	if !reloaded.Investigated || !reloaded.Suggested {
		t.Fatalf("reloaded state lost prior observations: %+v", reloaded)
	}
	if crossed := reloaded.Observe(false, true); crossed {
		t.Fatalf("reloaded state re-fired a crossing that already happened")
	}
}

func TestInvalidateRemovesState(t *testing.T) {
	dir := t.TempDir()
	sessionID := "sess-invalidate"

	s, _ := Load(dir, sessionID)
	s.Observe(true, false)
	s.Observe(false, true)
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
	if reloaded.Investigated || reloaded.Suggested {
		t.Fatalf("state survived Invalidate — same-session-only constraint violated: %+v", reloaded)
	}
}

func TestInvalidateOnMissingFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	if err := Invalidate(dir, "never-existed"); err != nil {
		t.Fatalf("Invalidate on nonexistent session errored: %v", err)
	}
}
