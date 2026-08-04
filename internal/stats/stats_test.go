package stats

import (
	"testing"

	"github.com/umitkaanusta/agent-winglet/internal/transcript"
)

func TestLoadMissingSessionReturnsZeroValue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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

func TestSetTranscriptUsageCopiesFields(t *testing.T) {
	s := &Session{}
	s.SetTranscriptUsage(transcript.SessionUsage{Tokens: 500, CostUSD: 0.25, ContentBytes: 2000})
	if s.TranscriptTokens != 500 || s.TranscriptCostUSD != 0.25 || s.TranscriptContentBytes != 2000 {
		t.Fatalf("got TranscriptTokens=%d TranscriptCostUSD=%v TranscriptContentBytes=%d, want 500/0.25/2000",
			s.TranscriptTokens, s.TranscriptCostUSD, s.TranscriptContentBytes)
	}
}

func TestAddTranscriptUsageAccumulatesAndAdvancesOffset(t *testing.T) {
	s := &Session{}
	s.AddTranscriptUsage(transcript.SessionUsage{Tokens: 100, CostUSD: 0.01, ContentBytes: 400}, 500)
	s.AddTranscriptUsage(transcript.SessionUsage{Tokens: 50, CostUSD: 0.005, ContentBytes: 200}, 900)

	if s.TranscriptTokens != 150 || s.TranscriptCostUSD != 0.015 || s.TranscriptContentBytes != 600 {
		t.Fatalf("got TranscriptTokens=%d TranscriptCostUSD=%v TranscriptContentBytes=%d, want 150/0.015/600 (deltas must sum, not overwrite)",
			s.TranscriptTokens, s.TranscriptCostUSD, s.TranscriptContentBytes)
	}
	if s.TranscriptOffset != 900 {
		t.Fatalf("TranscriptOffset = %d, want 900 (should track the latest call's offset, not sum)", s.TranscriptOffset)
	}
}

func TestIsZeroIgnoresTranscriptUsage(t *testing.T) {
	s := &Session{}
	s.SetTranscriptUsage(transcript.SessionUsage{Tokens: 500, CostUSD: 0.25, ContentBytes: 2000})
	if !s.IsZero() {
		t.Fatalf("a session with only transcript usage set (no mechanism fired) should still report zero")
	}
}

func TestPercentReportsNoDataYetWhenNoTranscriptData(t *testing.T) {
	if _, ok := Percent(0, 0, 0, 0); ok {
		t.Fatalf("Percent with zero transcript content bytes should report ok=false, not a computed percentage")
	}
}

func TestPercentReportsNoDataYetWhenSuppressedButNoTranscriptData(t *testing.T) {
	// Regression guard: suppression happening (dedup/budget/retire bytes >
	// 0) must not, on its own, make Percent report a computed percentage
	// when there's no real transcript-content total to measure it against —
	// that degenerate case (total = suppressed) would report 100% saved,
	// which is a false claim, not an honest "no data yet."
	if _, ok := Percent(20, 10, 8, 0); ok {
		t.Fatalf("Percent with suppression but zero transcript content bytes should report ok=false")
	}
}

func TestPercentComputesSuppressedFractionOfRealTotal(t *testing.T) {
	// suppressed = 20+10+8 = 38, transcriptContentBytes = 62 -> real total =
	// 100 -> 38%.
	pct, ok := Percent(20, 10, 8, 62)
	if !ok {
		t.Fatalf("Percent with nonzero transcript content bytes should report ok=true")
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
		t.Fatalf("PartPercent with zero total should report ok=false")
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
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "sess-roundtrip"

	s, _ := LoadSession(dir, sessionID)
	s.RecordDedup(10)
	s.RecordBudgetTrim(5, 50)
	s.RecordRetire(20)
	s.SetTranscriptUsage(transcript.SessionUsage{Tokens: 1000, CostUSD: 0.5, ContentBytes: 4000})
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
	t.Setenv("HOME", t.TempDir())
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
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := InvalidateSession(dir, "never-existed"); err != nil {
		t.Fatalf("InvalidateSession on nonexistent session errored: %v", err)
	}
}

func TestLifetimeAddAccumulatesAcrossSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
	s1.SetTranscriptUsage(transcript.SessionUsage{Tokens: 1000, CostUSD: 0.4, ContentBytes: 3000})
	l.Add(s1)

	s2 := &Session{}
	s2.RecordDedup(50)
	s2.RecordRetire(25)
	s2.SetTranscriptUsage(transcript.SessionUsage{Tokens: 500, CostUSD: 0.1, ContentBytes: 1500})
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
	if l.TranscriptTokens != 1500 || l.TranscriptCostUSD != 0.5 || l.TranscriptContentBytes != 4500 {
		t.Fatalf("got TranscriptTokens=%d TranscriptCostUSD=%v TranscriptContentBytes=%d, want 1500/0.5/4500",
			l.TranscriptTokens, l.TranscriptCostUSD, l.TranscriptContentBytes)
	}
}

func TestLifetimeSurvivesSessionInvalidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	l, _ := LoadLifetime(dir)
	s := &Session{}
	s.RecordBudgetTrim(15, 150)
	s.SetTranscriptUsage(transcript.SessionUsage{Tokens: 750, CostUSD: 0.3, ContentBytes: 2250})
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
