package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/umitkaanusta/agent-winglet/internal/registry"
	"github.com/umitkaanusta/agent-winglet/internal/statedir"
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
	if o.BytesSavedCard.Detail != "no data yet" || o.DollarSavedCard.Detail != "no data yet" {
		t.Fatalf("card details should read 'no data yet' with no processed-bytes data, got bytesSaved=%q dollarSaved=%q",
			o.BytesSavedCard.Detail, o.DollarSavedCard.Detail)
	}
	if o.HeroUsageDetail != "no data yet" {
		t.Fatalf("HeroUsageDetail = %q, want %q with no processed-bytes data", o.HeroUsageDetail, "no data yet")
	}
	for _, bar := range o.Bars {
		if bar.HasPercent {
			t.Fatalf("bar %q should have HasPercent=false with no processed-bytes data, got %+v", bar.Label, bar)
		}
	}
}

// TestBuildOverviewShowsLiveActivityBeforeTranscriptIsReadable is the
// in-progress-session case: dedup/budget/retire have fired (as they do live,
// on every PostToolUse) but TranscriptContentBytes is still zero, exactly
// like every session looks for its entire duration until SessionEnd. Before
// this test existed, this state was indistinguishable from
// TestBuildOverviewNoDataYetWhenNothingProcessed — a running session with
// real suppression activity rendered identically to one that had done
// nothing at all.
func TestBuildOverviewShowsLiveActivityBeforeTranscriptIsReadable(t *testing.T) {
	o := buildOverview(overviewTotals{
		BudgetTrims: 9, BudgetLinesOmitted: 1947, BudgetBytesOmitted: 93983,
	}, 1, 1)

	if o.HasTranscriptData {
		t.Fatalf("expected HasTranscriptData = false with no transcript content bytes, got true")
	}
	if !o.HasActivity {
		t.Fatalf("expected HasActivity = true with nonzero suppressed bytes")
	}
	if o.HeroHeadline == "No data yet" {
		t.Fatalf("HeroHeadline = %q, want it to reflect the real suppressed bytes instead of hiding them", o.HeroHeadline)
	}
	if !strings.Contains(o.HeroHeadline, "suppressed so far") {
		t.Fatalf("HeroHeadline = %q, want it to mention suppressed bytes so far", o.HeroHeadline)
	}
	if o.BytesSavedCard.Detail == "no data yet" {
		t.Fatalf("BytesSavedCard.Detail should show real suppressed bytes once a mechanism has fired, got %q", o.BytesSavedCard.Detail)
	}
	// Tokens/dollars genuinely need the completed transcript's cost-per-token
	// rate — those two staying "no data yet" mid-session is correct, not a
	// regression of this fix.
	if o.TokensSavedCard.Detail != "no data yet" || o.DollarSavedCard.Detail != "no data yet" {
		t.Fatalf("TokensSavedCard/DollarSavedCard should still read 'no data yet' without transcript data, got tokens=%q dollar=%q",
			o.TokensSavedCard.Detail, o.DollarSavedCard.Detail)
	}
	if o.HeroUsageDetail == "no data yet" || o.HeroUsageDetail == "" {
		t.Fatalf("HeroUsageDetail = %q, want an explanation that percent saved is pending, not a blank/generic placeholder", o.HeroUsageDetail)
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
	if o.BytesSavedCard.Sub != "Directly measured" {
		t.Fatalf("BytesSavedCard.Sub = %q, want %q", o.BytesSavedCard.Sub, "Directly measured")
	}
	if o.TokensSavedCard.Detail != "no data yet" {
		t.Fatalf("TokensSavedCard.Detail = %q, want %q (no TranscriptTokens seeded)", o.TokensSavedCard.Detail, "no data yet")
	}
	// HeroUsageDetail restates the same stretch (≈1.6129x) as a percent
	// instead of a multiplier: (1.6129 - 1) * 100 ≈ 61%.
	if !strings.Contains(o.HeroUsageDetail, "61%") || !strings.Contains(o.HeroUsageDetail, "usage") {
		t.Fatalf("HeroUsageDetail missing expected percent or 'usage' wording, got %q", o.HeroUsageDetail)
	}
	if o.HeroUsageSub != "with the same plan" {
		t.Fatalf("HeroUsageSub = %q, want %q", o.HeroUsageSub, "with the same plan")
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
	// Built via a stats.Session + totalsFromSession (not a hand-rolled
	// overviewTotals literal) so this exercises the same path production
	// code takes: TokensSaved/DollarSaved come from stats.Session's own
	// methods, not re-derived from an aggregate ratio inside buildOverview
	// (see overviewTotals' doc comment).
	s := &stats.Session{
		DedupHits: 1, DedupBytes: 100,
		// This session's transcript usage: $1 cost backed by 4000 content
		// bytes -> costPerByte = $0.00025/byte. TranscriptTokens: 4000 gives
		// a clean 1 token/byte ratio, so TokensSavedCard's expected value
		// below is easy to check; the dollar figure would come out the same
		// regardless of this value (it cancels out of the math).
		TranscriptTokens:       4000,
		TranscriptCostUSD:      1.0,
		TranscriptContentBytes: 4000,
	}
	o := buildOverview(totalsFromSession(s), 1, 1)

	if !o.HasTranscriptData {
		t.Fatalf("expected HasTranscriptData = true when TranscriptContentBytes > 0")
	}
	// tokensSaved = 100 suppressed bytes * 1 token/byte = 100 tokens.
	if o.BytesSavedCard.Label != "Bytes saved" || o.BytesSavedCard.Estimated {
		t.Fatalf("BytesSavedCard label/estimated = %q/%v, want Bytes saved/false", o.BytesSavedCard.Label, o.BytesSavedCard.Estimated)
	}
	if o.TokensSavedCard.Label != "Tokens saved" || !o.TokensSavedCard.Estimated {
		t.Fatalf("TokensSavedCard label/estimated = %q/%v, want Tokens saved/true", o.TokensSavedCard.Label, o.TokensSavedCard.Estimated)
	}
	if o.DollarSavedCard.Label != "Money saved" || !o.DollarSavedCard.Estimated {
		t.Fatalf("DollarSavedCard label/estimated = %q/%v, want Money saved/true", o.DollarSavedCard.Label, o.DollarSavedCard.Estimated)
	}
	if o.TokensSavedCard.Detail != "100" {
		t.Fatalf("TokensSavedCard.Detail = %q, want %q", o.TokensSavedCard.Detail, "100")
	}
	// usdSaved = 100 suppressed bytes * $0.00025/byte = $0.025 -> "$0.03".
	if o.DollarSavedCard.Detail != "$0.03" {
		t.Fatalf("DollarSavedCard.Detail = %q, want %q", o.DollarSavedCard.Detail, "$0.03")
	}
	if o.DollarSavedCard.Sub != "Uses API pricing" {
		t.Fatalf("DollarSavedCard.Sub = %q, want %q", o.DollarSavedCard.Sub, "Uses API pricing")
	}
}

func TestBuildOverviewDollarCardStaysProportionalAcrossOutlierSessions(t *testing.T) {
	// Regression test: a single outlier session's own tokens/cost/content
	// bytes must price its dollar card proportionally to its suppressed
	// bytes, not distort it by orders of magnitude. Built via stats.Session +
	// totalsFromSession (see overviewTotals' doc comment) so this exercises
	// the real production path: TokensSaved/DollarSaved come from
	// stats.Session's own methods. Going through a tokens intermediate is
	// safe here specifically because TranscriptTokens/TranscriptCostUSD (see
	// internal/transcript) exclude cache-read replays and output tokens at
	// the source, so they scale with actual content size, not with how many
	// turns a session ran.
	s := &stats.Session{
		DedupHits: 1, DedupBytes: 13926, // ~13.6 KiB, matches the observed regression
		TranscriptTokens:       100, // cancels out of the dollar math; only sizes TokensSavedCard below
		TranscriptCostUSD:      0.1578,
		TranscriptContentBytes: 455, // tiny, from a mostly-trivial session
	}
	o := buildOverview(totalsFromSession(s), 1, 1)

	if !o.HasTranscriptData {
		t.Fatalf("expected HasTranscriptData = true")
	}
	// tokensPerByte = 100/455 ≈ 0.2198 -> tokensSaved ≈ 13926*0.2198 ≈ 3061 -> "3.1K",
	// a plausible small token count, not the millions an uncorrected
	// cache-read-inflated ratio would have produced.
	if o.TokensSavedCard.Detail != "3.1K" {
		t.Fatalf("TokensSavedCard.Detail = %q, want %q", o.TokensSavedCard.Detail, "3.1K")
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

	s1 := &stats.Session{DedupHits: 1, DedupBytes: 10, TranscriptContentBytes: 100}
	if err := stats.SaveSession(dir1, "sess1", s1); err != nil {
		t.Fatalf("SaveSession dir1 errored: %v", err)
	}
	s2a := &stats.Session{RetiredCalls: 1, RetiredBytes: 20}
	if err := stats.SaveSession(dir2, "sess2a", s2a); err != nil {
		t.Fatalf("SaveSession dir2/sess2a errored: %v", err)
	}
	s2b := &stats.Session{}
	if err := stats.SaveSession(dir2, "sess2b", s2b); err != nil {
		t.Fatalf("SaveSession dir2/sess2b errored: %v", err)
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

// TestProjectTotalMatchesSumOfItsSessions is the regression test for the
// bug report this fix addressed: a project's Tokens saved / Money saved
// cards (from GetProjects) didn't equal the sum of the same cards across
// its own sessions (from GetSessionStats), because buildOverview used to
// re-derive tokensSaved as TranscriptTokens*pct/(100-pct) from each rollup's
// own aggregate percentage — a ratio, not a sum — instead of summing each
// session's already-computed figure. The two sessions below are
// deliberately heterogeneous (one all suppression/low tokens, one no
// suppression/high tokens) so a ratio-of-sums would visibly diverge from a
// sum-of-ratios if the bug were still present.
func TestProjectTotalMatchesSumOfItsSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := registry.Register(dir); err != nil {
		t.Fatalf("Register errored: %v", err)
	}

	s1 := &stats.Session{
		DedupBytes: 5000, TranscriptTokens: 5000, TranscriptCostUSD: 5.0, TranscriptContentBytes: 10000,
	}
	if err := stats.SaveSession(dir, "sess1", s1); err != nil {
		t.Fatalf("SaveSession sess1 errored: %v", err)
	}
	s2 := &stats.Session{
		TranscriptTokens: 20000, TranscriptCostUSD: 10.0, TranscriptContentBytes: 10000,
	}
	if err := stats.SaveSession(dir, "sess2", s2); err != nil {
		t.Fatalf("SaveSession sess2 errored: %v", err)
	}

	a := NewApp()
	rows, err := a.GetProjects()
	if err != nil {
		t.Fatalf("GetProjects errored: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d project rows, want 1", len(rows))
	}

	sessions, err := a.GetSessionStats(dir)
	if err != nil {
		t.Fatalf("GetSessionStats errored: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d session rows, want 2", len(sessions))
	}

	// sess1: suppressed=5000/content=10000 -> tokensSaved=5000*5000/10000=2500
	// ("2.5K"), priced at $5/5000tok -> dollarSaved=$2.50.
	// sess2: no suppression -> tokensSaved=0, dollarSaved=$0.00.
	// Sum across sessions: 2500 tokens / $2.50 — that must be exactly what
	// the project row shows. Before this fix, buildOverview re-derived the
	// project's figure from the rollup's own aggregate percentage instead
	// (tokensSaved=25000*20%/80%=6250, dollarSaved=$3.75) — 2.5x too high.
	for id, want := range map[string][2]string{"sess1": {"2.5K", "$2.50"}, "sess2": {"0", "$0.00"}} {
		var row *SessionRow
		for i := range sessions {
			if sessions[i].SessionID == id {
				row = &sessions[i]
			}
		}
		if row == nil {
			t.Fatalf("no session row for %q", id)
		}
		if row.Overview.TokensSavedCard.Detail != want[0] || row.Overview.DollarSavedCard.Detail != want[1] {
			t.Fatalf("%s cards = tokens=%q dollars=%q, want tokens=%q dollars=%q",
				id, row.Overview.TokensSavedCard.Detail, row.Overview.DollarSavedCard.Detail, want[0], want[1])
		}
	}

	if rows[0].Overview.TokensSavedCard.Detail != "2.5K" {
		t.Fatalf("project TokensSavedCard.Detail = %q, want %q (sum of session cards, not the pre-fix 6.3K)",
			rows[0].Overview.TokensSavedCard.Detail, "2.5K")
	}
	if rows[0].Overview.DollarSavedCard.Detail != "$2.50" {
		t.Fatalf("project DollarSavedCard.Detail = %q, want %q (sum of session cards, not the pre-fix $3.75)",
			rows[0].Overview.DollarSavedCard.Detail, "$2.50")
	}
}

// TestGetOverviewIncludesLiveInProgressSessions is the regression test for
// the bug report this fix originally addressed: Overview only ever
// reflected a session's numbers once it had ended and been folded into a
// separately persisted lifetime file — so a currently-running session
// (however active) never moved the Overview screen's numbers no matter how
// fast the frontend polled it. Now that GetOverview sums every session file
// on disk directly (stats.SumProject), a still-in-progress session's file
// counts the same as a finished one, with no "ended" distinction needed —
// verified here by summing two never-finalized session files.
func TestGetOverviewIncludesLiveInProgressSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := registry.Register(dir); err != nil {
		t.Fatalf("Register errored: %v", err)
	}

	finished := &stats.Session{DedupHits: 1, DedupBytes: 10, TranscriptContentBytes: 100}
	if err := stats.SaveSession(dir, "sess-finished", finished); err != nil {
		t.Fatalf("SaveSession finished errored: %v", err)
	}

	// A still-running session's file — nothing marks it "finished" in this
	// model, it's just another session file on disk. Its numbers must show
	// up on top of the finished one's.
	live := &stats.Session{BudgetTrims: 3, BudgetBytesOmitted: 900}
	if err := stats.SaveSession(dir, "sess-live", live); err != nil {
		t.Fatalf("SaveSession live errored: %v", err)
	}

	a := NewApp()
	o, err := a.GetOverview()
	if err != nil {
		t.Fatalf("GetOverview errored: %v", err)
	}
	if o.HeroBytes != 910 {
		t.Fatalf("HeroBytes = %d, want 910 (finished session's 10 + the live session's 900)", o.HeroBytes)
	}
	if o.SessionCount != 2 {
		t.Fatalf("SessionCount = %d, want 2 (1 finished + 1 live in-progress session)", o.SessionCount)
	}
}

// TestGetProjectsIncludesLiveInProgressSessions is GetProjects' counterpart
// to TestGetOverviewIncludesLiveInProgressSessions — same reasoning, same
// fix, different call site.
func TestGetProjectsIncludesLiveInProgressSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := registry.Register(dir); err != nil {
		t.Fatalf("Register errored: %v", err)
	}

	finished := &stats.Session{RetiredCalls: 1, RetiredBytes: 5}
	if err := stats.SaveSession(dir, "sess-finished", finished); err != nil {
		t.Fatalf("SaveSession finished errored: %v", err)
	}
	live := &stats.Session{DedupHits: 2, DedupBytes: 40}
	if err := stats.SaveSession(dir, "sess-live", live); err != nil {
		t.Fatalf("SaveSession live errored: %v", err)
	}

	a := NewApp()
	rows, err := a.GetProjects()
	if err != nil {
		t.Fatalf("GetProjects errored: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Overview.HeroBytes != 45 {
		t.Fatalf("HeroBytes = %d, want 45 (finished session's 5 retired + the live session's 40 deduped)", rows[0].Overview.HeroBytes)
	}
}

func TestGetSessionStatsListsSessionsNewestFirstAndSkipsLifetime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	agentDir, err := statedir.Dir(dir)
	if err != nil {
		t.Fatalf("statedir.Dir errored: %v", err)
	}
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
	legacyPath := filepath.Join(agentDir, stats.LifetimeFileName)
	if err := os.WriteFile(legacyPath, []byte(`{"dedupHits":99,"dedupBytes":9900}`), 0o644); err != nil {
		t.Fatalf("write legacy lifetime file errored: %v", err)
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
	if rows[0].Agent != stats.AgentClaudeCode {
		t.Fatalf("legacy rows[0].Agent = %q, want %q", rows[0].Agent, stats.AgentClaudeCode)
	}
}

func TestGetSessionStatsIncludesAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := stats.SaveSession(dir, "sess-codex", &stats.Session{Agent: stats.AgentCodex, DedupHits: 1}); err != nil {
		t.Fatalf("SaveSession codex errored: %v", err)
	}

	a := NewApp()
	rows, err := a.GetSessionStats(dir)
	if err != nil {
		t.Fatalf("GetSessionStats errored: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Agent != stats.AgentCodex {
		t.Fatalf("rows[0].Agent = %q, want %q", rows[0].Agent, stats.AgentCodex)
	}
}

func TestGetHookHealthReportsCodexNotInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	h, err := NewApp().GetHookHealth()
	if err != nil {
		t.Fatalf("GetHookHealth errored: %v", err)
	}
	if h.CodexConfigured || h.CodexObserved || h.CodexReviewLikely {
		t.Fatalf("unexpected Codex hook health: %+v", h)
	}
	if h.CodexStatus != "Inactive" {
		t.Fatalf("CodexStatus = %q, want %q", h.CodexStatus, "Inactive")
	}
}

func TestGetHookHealthReportsClaudeWaitingForActivity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeClaudeHookConfig(t, filepath.Join(home, ".claude", "settings.json"))

	h, err := NewApp().GetHookHealth()
	if err != nil {
		t.Fatalf("GetHookHealth errored: %v", err)
	}
	if !h.ClaudeConfigured || h.ClaudeObserved || h.ClaudeReviewLikely {
		t.Fatalf("unexpected Claude hook health: %+v", h)
	}
	if h.ClaudeStatus != "Waiting for activity" {
		t.Fatalf("ClaudeStatus = %q, want %q", h.ClaudeStatus, "Waiting for activity")
	}
}

func TestGetHookHealthReportsClaudeActiveAfterObservedSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	hooksPath := filepath.Join(home, ".claude", "settings.json")
	writeClaudeHookConfig(t, hooksPath)

	dir := t.TempDir()
	if err := registry.Register(dir); err != nil {
		t.Fatalf("Register errored: %v", err)
	}
	if err := stats.SaveSession(dir, "sess-claude", &stats.Session{Agent: stats.AgentClaudeCode}); err != nil {
		t.Fatalf("SaveSession errored: %v", err)
	}
	future := time.Now().Add(time.Hour)
	sessionPath, err := statsSessionPath(dir, "sess-claude")
	if err != nil {
		t.Fatalf("statsSessionPath errored: %v", err)
	}
	if err := os.Chtimes(sessionPath, future, future); err != nil {
		t.Fatalf("Chtimes errored: %v", err)
	}

	h, err := NewApp().GetHookHealth()
	if err != nil {
		t.Fatalf("GetHookHealth errored: %v", err)
	}
	if !h.ClaudeConfigured || !h.ClaudeObserved || h.ClaudeReviewLikely {
		t.Fatalf("unexpected Claude hook health: %+v", h)
	}
	if h.ClaudeStatus != "Active" {
		t.Fatalf("ClaudeStatus = %q, want %q", h.ClaudeStatus, "Active")
	}
}

func TestSetClaudeHookEnabledAddsAndRemovesGlobalEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeExecutable(t, filepath.Join(home, "go", "bin", "claude-hook"))

	app := NewApp()
	h, err := app.SetClaudeHookEnabled(true)
	if err != nil {
		t.Fatalf("SetClaudeHookEnabled(true) errored: %v", err)
	}
	if !h.ClaudeConfigured {
		t.Fatalf("ClaudeConfigured = false after enable: %+v", h)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if !hookFileContainsCommand(settingsPath, "claude-hook") {
		t.Fatalf("settings file does not contain claude-hook after enable")
	}

	h, err = app.SetClaudeHookEnabled(false)
	if err != nil {
		t.Fatalf("SetClaudeHookEnabled(false) errored: %v", err)
	}
	if h.ClaudeConfigured {
		t.Fatalf("ClaudeConfigured = true after disable: %+v", h)
	}
	if hookFileContainsCommand(settingsPath, "claude-hook") {
		t.Fatalf("settings file still contains claude-hook after disable")
	}
}

func TestSetCodexHookEnabledPreservesUnrelatedHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeExecutable(t, filepath.Join(home, "go", "bin", "codex-hook"))
	hooksPath := filepath.Join(home, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatalf("MkdirAll errored: %v", err)
	}
	data := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/tmp/other-hook"}]}]}}`
	if err := os.WriteFile(hooksPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile errored: %v", err)
	}

	app := NewApp()
	if _, err := app.SetCodexHookEnabled(true); err != nil {
		t.Fatalf("SetCodexHookEnabled(true) errored: %v", err)
	}
	if !hookFileContainsCommand(hooksPath, "codex-hook") {
		t.Fatalf("hooks file does not contain codex-hook after enable")
	}
	if !hookFileContainsCommand(hooksPath, "other-hook") {
		t.Fatalf("hooks file lost unrelated hook after enable")
	}

	if _, err := app.SetCodexHookEnabled(false); err != nil {
		t.Fatalf("SetCodexHookEnabled(false) errored: %v", err)
	}
	if hookFileContainsCommand(hooksPath, "codex-hook") {
		t.Fatalf("hooks file still contains codex-hook after disable")
	}
	if !hookFileContainsCommand(hooksPath, "other-hook") {
		t.Fatalf("hooks file lost unrelated hook after disable")
	}
}

func TestGetHookHealthReportsCodexReviewLikely(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeCodexHookConfig(t, filepath.Join(home, ".codex", "hooks.json"))

	h, err := NewApp().GetHookHealth()
	if err != nil {
		t.Fatalf("GetHookHealth errored: %v", err)
	}
	if !h.CodexConfigured || h.CodexObserved || !h.CodexReviewLikely {
		t.Fatalf("unexpected Codex hook health: %+v", h)
	}
	if h.CodexStatus != "Extra steps needed" {
		t.Fatalf("CodexStatus = %q, want %q", h.CodexStatus, "Extra steps needed")
	}
}

func TestGetHookHealthReportsCodexActiveAfterObservedSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	hooksPath := filepath.Join(home, ".codex", "hooks.json")
	writeCodexHookConfig(t, hooksPath)

	dir := t.TempDir()
	if err := registry.Register(dir); err != nil {
		t.Fatalf("Register errored: %v", err)
	}
	if err := stats.SaveSession(dir, "sess-codex", &stats.Session{Agent: stats.AgentCodex}); err != nil {
		t.Fatalf("SaveSession errored: %v", err)
	}
	future := time.Now().Add(time.Hour)
	sessionPath, err := statsSessionPath(dir, "sess-codex")
	if err != nil {
		t.Fatalf("statsSessionPath errored: %v", err)
	}
	if err := os.Chtimes(sessionPath, future, future); err != nil {
		t.Fatalf("Chtimes errored: %v", err)
	}

	h, err := NewApp().GetHookHealth()
	if err != nil {
		t.Fatalf("GetHookHealth errored: %v", err)
	}
	if !h.CodexConfigured || !h.CodexObserved || h.CodexReviewLikely {
		t.Fatalf("unexpected Codex hook health: %+v", h)
	}
	if h.CodexStatus != "Active" {
		t.Fatalf("CodexStatus = %q, want %q", h.CodexStatus, "Active")
	}
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll errored: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile errored: %v", err)
	}
}

func writeClaudeHookConfig(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll errored: %v", err)
	}
	data := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/tmp/claude-hook","timeout":5}]}]}}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile errored: %v", err)
	}
}

func writeCodexHookConfig(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll errored: %v", err)
	}
	data := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/tmp/codex-hook","timeout":5}]}]}}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile errored: %v", err)
	}
}

func statsSessionPath(projectDir, sessionID string) (string, error) {
	d, err := statedir.Dir(projectDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, sessionID+".stats.json"), nil
}

func TestGetSessionStatsMissingDirReturnsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := NewApp()
	rows, err := a.GetSessionStats(t.TempDir())
	if err != nil {
		t.Fatalf("GetSessionStats errored: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no rows for a project with no state dir yet, got %+v", rows)
	}
}

func TestGetProjectsReturnsSummedOverviewPerProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := registry.Register(dir); err != nil {
		t.Fatalf("Register errored: %v", err)
	}
	s1 := &stats.Session{DedupHits: 1, DedupBytes: 60}
	if err := stats.SaveSession(dir, "sess1", s1); err != nil {
		t.Fatalf("SaveSession sess1 errored: %v", err)
	}
	s2 := &stats.Session{DedupHits: 1, DedupBytes: 50}
	if err := stats.SaveSession(dir, "sess2", s2); err != nil {
		t.Fatalf("SaveSession sess2 errored: %v", err)
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
		t.Fatalf("Overview HeroBytes = %d, want 110 (60 + 50, summed across the project's sessions)", row.Overview.HeroBytes)
	}
}
