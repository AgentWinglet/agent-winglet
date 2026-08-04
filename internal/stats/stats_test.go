package stats

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/umitkaanusta/agent-winglet/internal/statedir"
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
	s.SetTranscriptUsage(transcript.SessionUsage{Tokens: 500, CostUSD: 0.25, ContentBytes: 2000}, 2500)
	if s.TranscriptTokens != 500 || s.TranscriptCostUSD != 0.25 || s.TranscriptContentBytes != 2000 {
		t.Fatalf("got TranscriptTokens=%d TranscriptCostUSD=%v TranscriptContentBytes=%d, want 500/0.25/2000",
			s.TranscriptTokens, s.TranscriptCostUSD, s.TranscriptContentBytes)
	}
	if s.TranscriptOffset != 2500 {
		t.Fatalf("TranscriptOffset = %d, want 2500 (must be set alongside the usage totals it priced)", s.TranscriptOffset)
	}
}

// TestSetTranscriptUsageOverwritesStaleOffset is the regression test for the
// double-counting bug this fix addresses: SetTranscriptUsage used to leave
// TranscriptOffset untouched, so a session that had already accumulated an
// offset via AddTranscriptUsage (PostToolUse/Stop, mid-session) would still
// have that stale offset after SessionEnd's full reconciliation read — a
// later resume of the same session_id would then re-read and re-add
// everything between the stale offset and SessionEnd's end-of-file, on top
// of a total that already included it. SetTranscriptUsage must overwrite
// TranscriptOffset, not just leave whatever AddTranscriptUsage left behind.
func TestSetTranscriptUsageOverwritesStaleOffset(t *testing.T) {
	s := &Session{}
	s.AddTranscriptUsage(transcript.SessionUsage{Tokens: 100, CostUSD: 0.01, ContentBytes: 400}, 500)
	s.SetTranscriptUsage(transcript.SessionUsage{Tokens: 1000, CostUSD: 0.4, ContentBytes: 3000}, 3200)
	if s.TranscriptOffset != 3200 {
		t.Fatalf("TranscriptOffset = %d, want 3200 (SetTranscriptUsage must overwrite AddTranscriptUsage's stale offset)", s.TranscriptOffset)
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
	s.SetTranscriptUsage(transcript.SessionUsage{Tokens: 500, CostUSD: 0.25, ContentBytes: 2000}, 9999)
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
	s.SetTranscriptUsage(transcript.SessionUsage{Tokens: 1000, CostUSD: 0.5, ContentBytes: 4000}, 9999)
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

// TestInvalidateSessionPreservesTranscriptUsage is the regression test for
// the crash-after-compact gap: previously InvalidateSession unconditionally
// os.Remove'd the whole session file, so a process that crashed or was
// force-quit between a PostCompact and the next SessionEnd permanently lost
// whatever transcript usage (tokens/cost/content bytes) had already been
// recorded for that session — even though that usage genuinely happened and
// didn't become invalid just because the ledger/phase mechanisms reset. This
// asserts the mechanism counters (dedup/budget/retire) still reset to zero,
// exactly as before, while transcript usage set beforehand survives the
// call.
func TestInvalidateSessionPreservesTranscriptUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "sess-invalidate-transcript"

	s, _ := LoadSession(dir, sessionID)
	s.RecordDedup(10)
	s.RecordBudgetTrim(5, 50)
	s.RecordRetire(20)
	s.SetTranscriptUsage(transcript.SessionUsage{Tokens: 1000, CostUSD: 0.4, ContentBytes: 3000}, 9999)
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
	if reloaded.DedupHits != 0 || reloaded.DedupBytes != 0 || reloaded.BudgetTrims != 0 ||
		reloaded.RetiredCalls != 0 || reloaded.RetiredBytes != 0 {
		t.Fatalf("expected mechanism counters reset to zero, got %+v", reloaded)
	}
	if reloaded.TranscriptTokens != 1000 || reloaded.TranscriptCostUSD != 0.4 || reloaded.TranscriptContentBytes != 3000 {
		t.Fatalf("expected transcript usage to survive InvalidateSession, got %+v", reloaded)
	}
}

func TestInvalidateSessionOnMissingFileIsNotAnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := InvalidateSession(dir, "never-existed"); err != nil {
		t.Fatalf("InvalidateSession on nonexistent session errored: %v", err)
	}
}

func TestSumProjectAccumulatesAcrossSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	r, err := SumProject(dir)
	if err != nil {
		t.Fatalf("SumProject on empty project failed: %v", err)
	}
	if r.Sessions != 0 {
		t.Fatalf("expected fresh rollup, got %+v", r)
	}

	s1 := &Session{}
	s1.RecordDedup(100)
	s1.SetTranscriptUsage(transcript.SessionUsage{Tokens: 1000, CostUSD: 0.4, ContentBytes: 3000}, 9999)
	if err := SaveSession(dir, "sess1", s1); err != nil {
		t.Fatalf("SaveSession sess1 failed: %v", err)
	}

	s2 := &Session{}
	s2.RecordDedup(50)
	s2.RecordRetire(25)
	s2.SetTranscriptUsage(transcript.SessionUsage{Tokens: 500, CostUSD: 0.1, ContentBytes: 1500}, 9999)
	if err := SaveSession(dir, "sess2", s2); err != nil {
		t.Fatalf("SaveSession sess2 failed: %v", err)
	}

	r, err = SumProject(dir)
	if err != nil {
		t.Fatalf("SumProject failed: %v", err)
	}
	if r.Sessions != 2 {
		t.Fatalf("Sessions = %d, want 2", r.Sessions)
	}
	if r.DedupHits != 2 || r.DedupBytes != 150 {
		t.Fatalf("got DedupHits=%d DedupBytes=%d, want 2/150", r.DedupHits, r.DedupBytes)
	}
	if r.RetiredCalls != 1 || r.RetiredBytes != 25 {
		t.Fatalf("got RetiredCalls=%d RetiredBytes=%d, want 1/25", r.RetiredCalls, r.RetiredBytes)
	}
	if r.TranscriptTokens != 1500 || r.TranscriptCostUSD != 0.5 || r.TranscriptContentBytes != 4500 {
		t.Fatalf("got TranscriptTokens=%d TranscriptCostUSD=%v TranscriptContentBytes=%d, want 1500/0.5/4500",
			r.TranscriptTokens, r.TranscriptCostUSD, r.TranscriptContentBytes)
	}
}

func TestSumProjectDropsInvalidatedSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "sess1"

	s := &Session{}
	s.RecordDedup(10)
	if err := SaveSession(dir, sessionID, s); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	// Invalidating a session with no transcript usage recorded leaves
	// nothing worth keeping once its mechanism counters reset to zero, so
	// the file is removed entirely (same as the old unconditional-delete
	// behavior) and it drops out of the rollup — unlike a session with real
	// transcript usage, which InvalidateSession now preserves (see
	// TestInvalidateSessionPreservesTranscriptUsage).
	if err := InvalidateSession(dir, sessionID); err != nil {
		t.Fatalf("InvalidateSession failed: %v", err)
	}

	r, err := SumProject(dir)
	if err != nil {
		t.Fatalf("SumProject after session invalidation failed: %v", err)
	}
	if r.Sessions != 0 || r.DedupHits != 0 || r.DedupBytes != 0 {
		t.Fatalf("expected an invalidated session with no transcript usage to drop out of the rollup, got %+v", r)
	}
}

// TestSumProjectKeepsInvalidatedSessionsWithTranscriptUsage complements
// TestSumProjectDropsInvalidatedSessions: a session that had real transcript
// usage recorded before being invalidated stays in the rollup — its
// DedupHits/DedupBytes reset to zero, but its transcript-derived figures
// (and its place in the Sessions count) survive.
func TestSumProjectKeepsInvalidatedSessionsWithTranscriptUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "sess1"

	s := &Session{}
	s.RecordDedup(10)
	s.SetTranscriptUsage(transcript.SessionUsage{Tokens: 1000, CostUSD: 0.4, ContentBytes: 3000}, 9999)
	if err := SaveSession(dir, sessionID, s); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	if err := InvalidateSession(dir, sessionID); err != nil {
		t.Fatalf("InvalidateSession failed: %v", err)
	}

	r, err := SumProject(dir)
	if err != nil {
		t.Fatalf("SumProject after session invalidation failed: %v", err)
	}
	if r.Sessions != 1 || r.DedupHits != 0 || r.DedupBytes != 0 {
		t.Fatalf("expected the session to stay in the rollup with mechanism counters zeroed, got %+v", r)
	}
	if r.TranscriptTokens != 1000 || r.TranscriptCostUSD != 0.4 || r.TranscriptContentBytes != 3000 {
		t.Fatalf("expected transcript usage to survive into the rollup, got %+v", r)
	}
}

func TestListSessionsExcludesLegacyLifetimeFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	s := &Session{}
	s.RecordBudgetTrim(15, 150)
	if err := SaveSession(dir, "sess1", s); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	d, err := statedir.Dir(dir)
	if err != nil {
		t.Fatalf("statedir.Dir failed: %v", err)
	}
	// A leftover pre-Rollup lifetime.stats.json shares enough JSON field
	// names with Session that, if not excluded, it would get parsed as a
	// bogus extra session and double-count its own history.
	legacyPath := filepath.Join(d, LifetimeFileName)
	if err := os.WriteFile(legacyPath, []byte(`{"sessions":1,"dedupHits":99,"dedupBytes":9900}`), 0o644); err != nil {
		t.Fatalf("writing legacy lifetime file failed: %v", err)
	}

	files, err := ListSessions(dir)
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if len(files) != 1 || files[0].ID != "sess1" {
		t.Fatalf("ListSessions = %+v, want only sess1 (lifetime.stats.json must be excluded)", files)
	}

	r, err := SumProject(dir)
	if err != nil {
		t.Fatalf("SumProject failed: %v", err)
	}
	if r.DedupHits != 0 {
		t.Fatalf("SumProject picked up the legacy lifetime file's dedupHits, got %+v", r)
	}
}
