package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/umitkaanusta/agent-winglet/internal/registry"
	"github.com/umitkaanusta/agent-winglet/internal/stats"
)

func TestBuildOverviewNoDataYetWhenNothingProcessed(t *testing.T) {
	o := buildOverview(overviewTotals{}, 1, 0)
	if o.HeroHeadline != "No data yet" {
		t.Fatalf("HeroHeadline = %q, want %q", o.HeroHeadline, "No data yet")
	}
	if strings.Contains(o.Dedup.Detail, "%") || strings.Contains(o.BudgetTrims.Detail, "%") || strings.Contains(o.Retired.Detail, "%") {
		t.Fatalf("card details should omit a percentage prefix with no processed-bytes data, got dedup=%q trims=%q retired=%q",
			o.Dedup.Detail, o.BudgetTrims.Detail, o.Retired.Detail)
	}
}

func TestBuildOverviewComputesPercentAndStretch(t *testing.T) {
	o := buildOverview(overviewTotals{
		DedupHits: 1, DedupBytes: 20,
		BudgetTrims: 1, BudgetLinesOmitted: 5, BudgetBytesOmitted: 10,
		RetiredCalls: 1, RetiredBytes: 8,
		ProcessedBytes: 100,
	}, 1, 1)

	// suppressed = 20+10+8 = 38 -> 38% -> stretch = 100/(100-38) ≈ 1.6129
	if o.HeroHeadline != "38% saved" {
		t.Fatalf("HeroHeadline = %q, want %q", o.HeroHeadline, "38% saved")
	}
	if !strings.Contains(o.HeroSubtext, "1.6x more headroom") {
		t.Fatalf("HeroSubtext missing expected stretch multiplier, got %q", o.HeroSubtext)
	}
	if !strings.Contains(o.HeroSubtext, "38 B of tool output never replayed") {
		t.Fatalf("HeroSubtext missing expected suppressed-byte detail, got %q", o.HeroSubtext)
	}
	if !strings.HasPrefix(o.Dedup.Detail, "20% of output") {
		t.Fatalf("Dedup.Detail = %q, want a 20%% of output prefix", o.Dedup.Detail)
	}
	if !strings.HasPrefix(o.BudgetTrims.Detail, "10% of output") {
		t.Fatalf("BudgetTrims.Detail = %q, want a 10%% of output prefix", o.BudgetTrims.Detail)
	}
	if !strings.HasPrefix(o.Retired.Detail, "8% of output") {
		t.Fatalf("Retired.Detail = %q, want an 8%% of output prefix", o.Retired.Detail)
	}
}

func TestGetOverviewSumsAcrossProjects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	if err := registry.Register(dir1); err != nil {
		t.Fatalf("Register dir1 errored: %v", err)
	}
	if err := registry.Register(dir2); err != nil {
		t.Fatalf("Register dir2 errored: %v", err)
	}

	l1 := &stats.Lifetime{Sessions: 1, DedupHits: 1, DedupBytes: 10, ProcessedBytes: 50}
	if err := stats.SaveLifetime(dir1, l1); err != nil {
		t.Fatalf("SaveLifetime dir1 errored: %v", err)
	}
	l2 := &stats.Lifetime{Sessions: 2, RetiredCalls: 1, RetiredBytes: 20, ProcessedBytes: 50}
	if err := stats.SaveLifetime(dir2, l2); err != nil {
		t.Fatalf("SaveLifetime dir2 errored: %v", err)
	}

	a := NewApp()
	o, err := a.GetOverview()
	if err != nil {
		t.Fatalf("GetOverview errored: %v", err)
	}
	if o.ProjectCount != 2 || o.SessionCount != 3 {
		t.Fatalf("ProjectCount/SessionCount = %d/%d, want 2/3", o.ProjectCount, o.SessionCount)
	}
	if o.HeroBytes != 30 {
		t.Fatalf("HeroBytes = %d, want 30 (10 dedup + 20 retired, summed across projects)", o.HeroBytes)
	}
	if o.HeroHeadline == "No data yet" {
		t.Fatalf("expected computed percentage across projects, got %q", o.HeroHeadline)
	}
}

func TestGetSessionStatsListsSessionsNewestFirstAndSkipsLifetime(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, ".claude", "agent-winglet")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("MkdirAll errored: %v", err)
	}

	older := &stats.Session{DedupHits: 1, DedupBytes: 5, ProcessedBytes: 10}
	if err := stats.SaveSession(dir, "sess-older", older); err != nil {
		t.Fatalf("SaveSession sess-older errored: %v", err)
	}
	newerPath := filepath.Join(agentDir, "sess-newer.stats.json")
	if err := os.WriteFile(newerPath, []byte(`{"retiredCalls":1,"retiredBytes":7,"processedBytes":10}`), 0o644); err != nil {
		t.Fatalf("write sess-newer errored: %v", err)
	}
	// Give sess-newer a distinct, later mtime than sess-older so ordering is
	// deterministic regardless of filesystem timestamp resolution.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(newerPath, future, future); err != nil {
		t.Fatalf("Chtimes errored: %v", err)
	}
	if err := stats.SaveLifetime(dir, &stats.Lifetime{Sessions: 1}); err != nil {
		t.Fatalf("SaveLifetime errored: %v", err)
	}

	a := NewApp()
	rows, err := a.GetSessionStats(dir)
	if err != nil {
		t.Fatalf("GetSessionStats errored: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (lifetime.stats.json must be excluded), rows=%+v", len(rows), rows)
	}
	if rows[0].SessionID != "sess-newer" {
		t.Fatalf("rows[0].SessionID = %q, want %q (newest by mtime first)", rows[0].SessionID, "sess-newer")
	}
}

func TestGetSessionStatsMissingDirReturnsEmpty(t *testing.T) {
	a := NewApp()
	rows, err := a.GetSessionStats(t.TempDir())
	if err != nil {
		t.Fatalf("GetSessionStats errored: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no rows for a project with no .claude/agent-winglet dir, got %+v", rows)
	}
}

// seedSessionWithAge writes a session-stats file with DedupBytes/ProcessedBytes
// set so its contribution to a windowed sum is identifiable, then backdates
// its mtime by age — the only signal windowedTotals has for "when."
func seedSessionWithAge(t *testing.T, dir, sessionID string, dedupBytes, processedBytes int64, age time.Duration) {
	t.Helper()
	s := &stats.Session{DedupHits: 1, DedupBytes: dedupBytes, ProcessedBytes: processedBytes}
	if err := stats.SaveSession(dir, sessionID, s); err != nil {
		t.Fatalf("SaveSession %s errored: %v", sessionID, err)
	}
	path := filepath.Join(dir, ".claude", "agent-winglet", sessionID+".stats.json")
	mtime := time.Now().Add(-age)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("Chtimes %s errored: %v", sessionID, err)
	}
}

func TestWindowedTotalsExcludesSessionsOlderThanWindow(t *testing.T) {
	dir := t.TempDir()
	seedSessionWithAge(t, dir, "sess-recent", 10, 20, 1*time.Hour)
	seedSessionWithAge(t, dir, "sess-old", 100, 200, (windowDays+1)*24*time.Hour)

	total, n, err := windowedTotals(dir, time.Now().AddDate(0, 0, -windowDays))
	if err != nil {
		t.Fatalf("windowedTotals errored: %v", err)
	}
	if n != 1 {
		t.Fatalf("n = %d, want 1 (only the recent session should count)", n)
	}
	if total.DedupBytes != 10 || total.ProcessedBytes != 20 {
		t.Fatalf("totals = %+v, want DedupBytes=10 ProcessedBytes=20 (the old session's 100/200 must be excluded)", total)
	}
}

func TestGetOverviewWindowExcludesOldSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := registry.Register(dir); err != nil {
		t.Fatalf("Register errored: %v", err)
	}
	seedSessionWithAge(t, dir, "sess-recent", 10, 20, 1*time.Hour)
	seedSessionWithAge(t, dir, "sess-old", 100, 200, (windowDays+1)*24*time.Hour)

	a := NewApp()
	w, err := a.GetOverviewWindow()
	if err != nil {
		t.Fatalf("GetOverviewWindow errored: %v", err)
	}
	if w.SessionCount != 1 {
		t.Fatalf("SessionCount = %d, want 1", w.SessionCount)
	}
	if w.HeroBytes != 10 {
		t.Fatalf("HeroBytes = %d, want 10 (only the recent session's dedup bytes)", w.HeroBytes)
	}
}

func TestGetProjectsIncludesWindow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := registry.Register(dir); err != nil {
		t.Fatalf("Register errored: %v", err)
	}
	if err := stats.SaveLifetime(dir, &stats.Lifetime{Sessions: 2, DedupHits: 2, DedupBytes: 110, ProcessedBytes: 220}); err != nil {
		t.Fatalf("SaveLifetime errored: %v", err)
	}
	seedSessionWithAge(t, dir, "sess-recent", 10, 20, 1*time.Hour)
	seedSessionWithAge(t, dir, "sess-old", 100, 200, (windowDays+1)*24*time.Hour)

	a := NewApp()
	rows, err := a.GetProjects()
	if err != nil {
		t.Fatalf("GetProjects errored: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.Overview.HeroBytes != 110 {
		t.Fatalf("Overview (lifetime) HeroBytes = %d, want 110", row.Overview.HeroBytes)
	}
	if row.Window.HeroBytes != 10 {
		t.Fatalf("Window HeroBytes = %d, want 10 (only the recent session)", row.Window.HeroBytes)
	}
	if row.Window.SessionCount != 1 {
		t.Fatalf("Window.SessionCount = %d, want 1", row.Window.SessionCount)
	}
}
