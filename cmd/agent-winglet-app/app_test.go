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
	if o.HasTranscriptData {
		t.Fatalf("expected HasTranscriptData = false with no transcript content bytes, got true")
	}
	if o.BytesSavedCard.Detail != "no data yet" || o.DollarSavedCard.Detail != "no data yet" || o.MoreUsageCard.Detail != "no data yet" {
		t.Fatalf("card details should read 'no data yet' with no processed-bytes data, got bytesSaved=%q dollarSaved=%q moreUsage=%q",
			o.BytesSavedCard.Detail, o.DollarSavedCard.Detail, o.MoreUsageCard.Detail)
	}
	for _, bar := range o.Bars {
		if bar.HasPercent {
			t.Fatalf("bar %q should have HasPercent=false with no processed-bytes data, got %+v", bar.Label, bar)
		}
	}
}

func TestBuildOverviewComputesBytesAndStretch(t *testing.T) {
	o := buildOverview(overviewTotals{
		DedupHits: 1, DedupBytes: 20,
		BudgetTrims: 1, BudgetLinesOmitted: 5, BudgetBytesOmitted: 10,
		RetiredCalls: 1, RetiredBytes: 8,
		// suppressed = 38; real total = transcriptContentBytes(62) + 38 = 100.
		TranscriptContentBytes: 62,
	}, 1, 1)

	// 38/100 -> 38% -> stretch = 100/(100-38) ≈ 1.6129
	if o.HeroHeadline != "38% saved" {
		t.Fatalf("HeroHeadline = %q, want %q", o.HeroHeadline, "38% saved")
	}
	if !o.HasTranscriptData {
		t.Fatalf("expected HasTranscriptData = true with transcript content bytes seeded, got false")
	}
	if o.HeroBytes != 38 {
		t.Fatalf("HeroBytes = %d, want 38", o.HeroBytes)
	}
	if o.BytesSavedCard.Detail != "38 B" {
		t.Fatalf("BytesSavedCard.Detail = %q, want %q", o.BytesSavedCard.Detail, "38 B")
	}
	if o.BytesSavedCard.Sub != "38%" {
		t.Fatalf("BytesSavedCard.Sub = %q, want %q", o.BytesSavedCard.Sub, "38%")
	}
	if !strings.Contains(o.MoreUsageCard.Detail, "1.6x") {
		t.Fatalf("MoreUsageCard.Detail missing expected stretch multiplier, got %q", o.MoreUsageCard.Detail)
	}
	if o.MoreUsageCard.Sub != "with the same plan" {
		t.Fatalf("MoreUsageCard.Sub = %q, want %q", o.MoreUsageCard.Sub, "with the same plan")
	}

	// Bars: dedup(20) > budget(10) > retired(8), descending by bytes.
	if len(o.Bars) != 3 {
		t.Fatalf("got %d bars, want 3", len(o.Bars))
	}
	if o.Bars[0].Label != "Repeat output skipped" || o.Bars[0].Bytes != 20 {
		t.Fatalf("Bars[0] = %+v, want dedup with 20 bytes first (descending by bytes)", o.Bars[0])
	}
	if o.Bars[1].Label != "Long output trimmed" || o.Bars[1].Bytes != 10 {
		t.Fatalf("Bars[1] = %+v, want budget-trim with 10 bytes second", o.Bars[1])
	}
	if o.Bars[2].Label != "Old investigation output archived" || o.Bars[2].Bytes != 8 {
		t.Fatalf("Bars[2] = %+v, want retire with 8 bytes third", o.Bars[2])
	}
	if o.Bars[0].FillRatio != 1 {
		t.Fatalf("largest bar's FillRatio = %v, want 1 (fill relative to the largest mechanism)", o.Bars[0].FillRatio)
	}
	if !o.Bars[0].HasPercent || o.Bars[0].Percent != 20 {
		t.Fatalf("Bars[0].Percent = %v/%v, want 20/true", o.Bars[0].Percent, o.Bars[0].HasPercent)
	}
}

func TestBuildOverviewWithTranscriptDataPricesDollarCard(t *testing.T) {
	o := buildOverview(overviewTotals{
		DedupHits: 1, DedupBytes: 100,
		// This rollup's transcript usage: $1 cost backed by 4000 content
		// bytes -> costPerByte = $0.00025/byte.
		TranscriptCostUSD:      1.0,
		TranscriptContentBytes: 4000,
	}, 1, 1)

	if !o.HasTranscriptData {
		t.Fatalf("expected HasTranscriptData = true when TranscriptContentBytes > 0")
	}
	// usdSaved = 100 suppressed bytes * $0.00025/byte = $0.025 -> "$0.03".
	if o.DollarSavedCard.Detail != "$0.03" {
		t.Fatalf("DollarSavedCard.Detail = %q, want %q", o.DollarSavedCard.Detail, "$0.03")
	}
	// suppressed = 100, real total = 4000+100 = 4100 -> 100/4100 ≈ 2.4%.
	if o.DollarSavedCard.Sub != "2%" {
		t.Fatalf("DollarSavedCard.Sub = %q, want %q", o.DollarSavedCard.Sub, "2%")
	}
}

func TestBuildOverviewDollarCardStaysProportionalAcrossOutlierSessions(t *testing.T) {
	// Regression test: pricing must go through a cost-per-byte ratio
	// computed from independently-summed cost and content bytes, never
	// through a per-token intermediate — a lifetime rollup mixing a
	// content-heavy session with a trivial one that still burned huge
	// context-replay tokens must not distort the $ estimate by orders of
	// magnitude (see the manual-verification note this test captures).
	o := buildOverview(overviewTotals{
		DedupHits: 1, DedupBytes: 13926, // ~13.6 KiB, matches the observed regression
		TranscriptCostUSD:      0.1578,
		TranscriptContentBytes: 455, // tiny, from a mostly-trivial session
	}, 1, 1)

	if !o.HasTranscriptData {
		t.Fatalf("expected HasTranscriptData = true")
	}
	// costPerByte = 0.1578/455 ≈ 0.000347 -> usdSaved ≈ 13926*0.000347 ≈ $4.83,
	// proportional to suppressed bytes and nowhere near the ~2-orders-of-
	// magnitude-too-high figure the tokens-based math produced.
	if o.DollarSavedCard.Detail != "$4.83" {
		t.Fatalf("DollarSavedCard.Detail = %q, want %q (proportional to suppressed bytes, not inflated)",
			o.DollarSavedCard.Detail, "$4.83")
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

	l1 := &stats.Lifetime{Sessions: 1, DedupHits: 1, DedupBytes: 10, TranscriptContentBytes: 100}
	if err := stats.SaveLifetime(dir1, l1); err != nil {
		t.Fatalf("SaveLifetime dir1 errored: %v", err)
	}
	l2 := &stats.Lifetime{Sessions: 2, RetiredCalls: 1, RetiredBytes: 20}
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
		t.Fatalf("expected a computed hero headline across projects, got %q", o.HeroHeadline)
	}
}

func TestGetSessionStatsListsSessionsNewestFirstAndSkipsLifetime(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, ".claude", "agent-winglet")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("MkdirAll errored: %v", err)
	}

	older := &stats.Session{DedupHits: 1, DedupBytes: 5}
	if err := stats.SaveSession(dir, "sess-older", older); err != nil {
		t.Fatalf("SaveSession sess-older errored: %v", err)
	}
	newerPath := filepath.Join(agentDir, "sess-newer.stats.json")
	if err := os.WriteFile(newerPath, []byte(`{"retiredCalls":1,"retiredBytes":7}`), 0o644); err != nil {
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

func TestGetProjectsReturnsLifetimeOverviewPerProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := registry.Register(dir); err != nil {
		t.Fatalf("Register errored: %v", err)
	}
	if err := stats.SaveLifetime(dir, &stats.Lifetime{Sessions: 2, DedupHits: 2, DedupBytes: 110}); err != nil {
		t.Fatalf("SaveLifetime errored: %v", err)
	}

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
}
