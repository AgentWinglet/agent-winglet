package main

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"sync/atomic"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/umitkaanusta/agent-winglet/internal/appipc"
	"github.com/umitkaanusta/agent-winglet/internal/registry"
	"github.com/umitkaanusta/agent-winglet/internal/stats"
)

// App is the Wails-bound backend. Every method here does the same thing the
// rest of agent-winglet already does: read the JSON files the hooks and
// install.sh already write, no daemon, no IPC beyond Wails' own JS<->Go
// bridge. See internal/stats' package doc for why lifetime tallies (unlike
// ledger/phase/retire state) are safe to read outside the hooks' own
// same-session lifecycle.
type App struct {
	ctx         context.Context
	ipcListener net.Listener
	// quitting is set right before the tray helper's "Quit" command calls
	// wailsruntime.Quit — beforeClose (see main.go) checks it to tell that
	// one real quit apart from every other way Quit/window-close can fire
	// (titlebar X, Cmd+Q/Alt+F4), all of which hide instead once a tray
	// helper exists to bring the window back. See internal/appipc's package
	// doc for why this exists as IPC to begin with.
	quitting atomic.Bool
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go a.serveIPC()

	// Defensive fallback: install.sh already calls RegisterLoginItem via
	// `--register-login-item` right after installing, but this covers any
	// other way the app ends up running (a dev build, a manual copy) — it's
	// idempotent (see loginitem_darwin.go), so calling it again here is
	// harmless. Errors are logged, not surfaced: the dashboard is fully
	// usable without a registered login item, just without the
	// launch-at-login convenience.
	go func() {
		if err := RegisterLoginItem(); err != nil {
			fmt.Println("agent-winglet-app: login item registration failed:", err)
		}
	}()
}

// serveIPC accepts local connections from the tray helper for the lifetime
// of the dashboard. A failure to bind the listener (e.g. two dashboard
// instances racing to claim the port file) is logged, not fatal — the
// dashboard still works standalone, just unreachable from the tray until
// it's restarted.
func (a *App) serveIPC() {
	ln, err := appipc.Listen()
	if err != nil {
		fmt.Println("agent-winglet-app: IPC listener failed to start:", err)
		return
	}
	a.ipcListener = ln

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go a.handleIPCConn(conn)
	}
}

func (a *App) handleIPCConn(conn net.Conn) {
	defer conn.Close()

	cmd, err := appipc.ReadCommand(conn)
	if err != nil {
		return
	}

	switch cmd {
	case appipc.Show:
		wailsruntime.WindowShow(a.ctx)
		wailsruntime.WindowUnminimise(a.ctx)
	case appipc.Quit:
		a.quitting.Store(true)
		wailsruntime.Quit(a.ctx)
	}
}

// beforeClose implements options.App.OnBeforeClose: once a tray helper is
// managing this dashboard, the titlebar close button and Cmd+Q/Alt+F4 hide
// the window instead of quitting — same pattern as Slack/Spotify-style
// tray-resident apps. Only the tray's own "Quit" (via handleIPCConn above)
// actually exits.
func (a *App) beforeClose(ctx context.Context) bool {
	if a.quitting.Load() {
		return false
	}
	wailsruntime.WindowHide(ctx)
	return true
}

// shutdown implements options.App.OnShutdown: releases the IPC listener and
// the port file it published, so a stale one left behind doesn't make the
// tray think a dashboard is still running when it isn't. Called on the real
// quit path only (beforeClose above gates everything else).
func (a *App) shutdown(ctx context.Context) {
	if a.ipcListener != nil {
		a.ipcListener.Close()
	}
	appipc.Cleanup()
}

// GetPlatform returns the Go-side runtime.GOOS ("darwin", "windows", or
// "linux") so the frontend can scope OS-specific chrome (the sidebar's
// traffic-light inset padding, mac-only vibrancy) via a data-os attribute
// instead of sniffing the user agent, which WebView2/WebKitGTK/WKWebView
// don't distinguish reliably.
func (a *App) GetPlatform() string {
	return goruntime.GOOS
}

// Card is one of the three summary cards on the card row: a label, a
// hover-tooltip explaining how that figure is derived (real measurement vs.
// estimate, and from what), a pre-formatted primary detail string, and an
// optional secondary line shown beneath it (e.g. a percent under a byte
// count, or a fixed caption under the net-gains multiplier).
type Card struct {
	Label   string `json:"label"`
	Tooltip string `json:"tooltip"`
	Detail  string `json:"detail"`
	Sub     string `json:"sub"`
}

// BarRow is one row of the suppressed-by-mechanism bar list: a
// label, a hover-tooltip explanation, this mechanism's share of processed
// bytes (already computed via stats.PartPercent, so the frontend never
// re-derives it), a fill ratio relative to the largest mechanism in this
// rollup (so the longest bar reads as ~full width instead of three
// near-invisible slivers when total suppression is small), and the raw
// count/unit-noun plus formatted bytes for the row's bottom line — split
// into their own fields so the frontend never has to parse a composed
// Detail string to lay out left/right.
type BarRow struct {
	Label      string  `json:"label"`
	Tooltip    string  `json:"tooltip"`
	Percent    float64 `json:"percent"`
	HasPercent bool    `json:"hasPercent"`
	FillRatio  float64 `json:"fillRatio"`
	CountLabel string  `json:"countLabel"`
	Bytes      int64   `json:"bytes"`
	BytesLabel string  `json:"bytesLabel"`
}

// Overview is the Overview screen's data (and each Projects-row's data —
// same shape, two call sites). Hierarchy, top to bottom:
//
//  1. HeroHeadline — the headline percent-saved figure (e.g. "38% saved"),
//     the primary claim, restated directly underneath as HeroUsageDetail —
//     the same stretch multiplier reframed as a percent ("with the same
//     plan" underneath), so the hero doesn't repeat itself with a bare "Ax"
//     multiplier right below a percent figure.
//  2. Cards — three small summary cards: bytes suppressed, the same bytes
//     converted to a token estimate, and that token estimate priced in
//     dollars. The net-gains multiplier lives only in HeroUsageDetail now —
//     a fourth card restating it would just repeat the hero line.
//  3. Bars — one row per suppression mechanism, descending by bytes.
type Overview struct {
	HeroBytes           int64   `json:"heroBytes"`
	HeroTotalBytes      int64   `json:"heroTotalBytes"`
	HeroTotalBytesLabel string  `json:"heroTotalBytesLabel"`
	HeroPercent         float64 `json:"heroPercent"`
	HeroHeadline        string  `json:"heroHeadline"`
	HeroUsageDetail     string  `json:"heroUsageDetail"`
	HeroUsageSub        string  `json:"heroUsageSub"`
	HasTranscriptData   bool    `json:"hasTranscriptData"`
	// HasActivity is true the moment any mechanism (dedup/budget-trim/retire)
	// has fired, independent of HasTranscriptData. Suppressed-byte totals
	// need nothing but the hook's own live-written stats file; the percent-
	// saved figure additionally needs this session's transcript, which is
	// only read once, at SessionEnd (see internal/transcript and
	// cmd/ledger-hook's handleSessionEnd). Without this split, a session
	// that's still running always renders identically to a session that
	// never did anything — real, live, moment-to-moment suppression data was
	// being discarded for the entire lifetime of every in-progress session.
	HasActivity     bool     `json:"hasActivity"`
	BytesSavedCard  Card     `json:"bytesSavedCard"`
	TokensSavedCard Card     `json:"tokensSavedCard"`
	DollarSavedCard Card     `json:"dollarSavedCard"`
	Bars            []BarRow `json:"bars"`
	ProjectCount    int      `json:"projectCount"`
	SessionCount    int      `json:"sessionCount"`
}

// GetOverview sums every project's session files (see stats.SumProject)
// across every project in the registry that still exists on disk — a live,
// still-in-progress session's file counts the same as a finished one, so
// this screen moves as soon as a session's stats file changes, not only once
// a session ends. A project with no state dir yet (hook installed but never
// fired here) contributes a zero rollup, not an error. There is no
// separately stored overall total: this is always the live sum of what's on
// disk, so it can't drift from what GetProjects/GetSessionStats show for the
// same projects.
func (a *App) GetOverview() (Overview, error) {
	dirs, err := registry.Load()
	if err != nil {
		return Overview{}, err
	}

	var total overviewTotals
	sessions := 0
	for _, dir := range dirs {
		r, err := stats.SumProject(dir)
		if err != nil {
			return Overview{}, err
		}
		total.add(totalsFromRollup(r))
		sessions += r.Sessions
	}

	return buildOverview(total, len(dirs), sessions), nil
}

// overviewTotals is the minimal common shape buildOverview needs — both
// stats.Session (one session's tally) and stats.Rollup (a project or
// cross-project sum) share these fields, just without a common Go type, so
// callers adapt each into this before formatting.
type overviewTotals struct {
	DedupHits          int
	DedupBytes         int64
	BudgetTrims        int
	BudgetLinesOmitted int
	BudgetBytesOmitted int64
	RetiredCalls       int
	RetiredBytes       int64

	TranscriptTokens       int64
	TranscriptCostUSD      float64
	TranscriptContentBytes int64
}

// add folds o into t in place, so a caller summing across many projects (or
// many sessions) can do so without a Lifetime/Session type of its own.
func (t *overviewTotals) add(o overviewTotals) {
	t.DedupHits += o.DedupHits
	t.DedupBytes += o.DedupBytes
	t.BudgetTrims += o.BudgetTrims
	t.BudgetLinesOmitted += o.BudgetLinesOmitted
	t.BudgetBytesOmitted += o.BudgetBytesOmitted
	t.RetiredCalls += o.RetiredCalls
	t.RetiredBytes += o.RetiredBytes
	t.TranscriptTokens += o.TranscriptTokens
	t.TranscriptCostUSD += o.TranscriptCostUSD
	t.TranscriptContentBytes += o.TranscriptContentBytes
}

func totalsFromRollup(r stats.Rollup) overviewTotals {
	return overviewTotals{
		DedupHits:          r.DedupHits,
		DedupBytes:         r.DedupBytes,
		BudgetTrims:        r.BudgetTrims,
		BudgetLinesOmitted: r.BudgetLinesOmitted,
		BudgetBytesOmitted: r.BudgetBytesOmitted,
		RetiredCalls:       r.RetiredCalls,
		RetiredBytes:       r.RetiredBytes,

		TranscriptTokens:       r.TranscriptTokens,
		TranscriptCostUSD:      r.TranscriptCostUSD,
		TranscriptContentBytes: r.TranscriptContentBytes,
	}
}

func totalsFromSession(s *stats.Session) overviewTotals {
	return overviewTotals{
		DedupHits:          s.DedupHits,
		DedupBytes:         s.DedupBytes,
		BudgetTrims:        s.BudgetTrims,
		BudgetLinesOmitted: s.BudgetLinesOmitted,
		BudgetBytesOmitted: s.BudgetBytesOmitted,
		RetiredCalls:       s.RetiredCalls,
		RetiredBytes:       s.RetiredBytes,

		TranscriptTokens:       s.TranscriptTokens,
		TranscriptCostUSD:      s.TranscriptCostUSD,
		TranscriptContentBytes: s.TranscriptContentBytes,
	}
}

func overviewFromSession(s *stats.Session, projectCount, sessionCount int) Overview {
	return buildOverview(totalsFromSession(s), projectCount, sessionCount)
}

// mechanism bundles one suppression mechanism's raw fields for barRows to
// sort and format uniformly, instead of repeating the same five-argument
// shape three times inline.
type mechanism struct {
	label      string
	tooltip    string
	bytes      int64
	countLabel string
}

const (
	dedupTooltip = "When Claude re-runs a shell command it's already run with identical output this session, " +
		"agent-winglet replaces the repeat with a short reference instead of sending it to the model again."
	budgetTrimTooltip = "Commands that succeed but print more than 60 lines have their middle section collapsed " +
		"to a head/tail summary."
	retireTooltip = "Once a session moves from investigating to editing, earlier read/search/fetch output is " +
		"assumed to have served its purpose and is archived instead of replayed."

	bytesSavedTooltip = "This is a real measurement, not an estimate. It is the size of the tool output " +
		"(command results, file reads, search results) that agent-winglet removed from the model's context."
	tokensSavedTooltip = "This is an estimate. It applies the percent saved above to the real token count " +
		"from the transcript."
	moneySavedTooltip = "Based on official API usage rates for the model in this session. Cache-read tokens " +
		"and output tokens are excluded."
)

// barRows builds the suppressed-by-mechanism bar list: one row per
// mechanism, descending by bytes, fill width relative to the largest
// mechanism in t (not an absolute 0-100% of total bytes) — otherwise a
// session dominated by one mechanism would render the other two as
// imperceptible slivers instead of comparable bars.
// A mechanism with zero bytes still gets a row (fill ratio 0) so the list
// always shows all three, in whatever order this rollup's own numbers
// produce. total is the same real total buildOverview passes to
// stats.Percent (transcriptContentBytes + suppressed), so each row's
// percent is directly comparable to — and sums to — the hero figure.
func barRows(t overviewTotals, total int64) []BarRow {
	mechanisms := []mechanism{
		{"Repeat output skipped", dedupTooltip, t.DedupBytes,
			fmt.Sprintf("%d hit%s", t.DedupHits, plural(t.DedupHits))},
		{"Long output trimmed", budgetTrimTooltip, t.BudgetBytesOmitted,
			fmt.Sprintf("%d trim%s", t.BudgetTrims, plural(t.BudgetTrims))},
		{"Old investigation output archived", retireTooltip, t.RetiredBytes,
			fmt.Sprintf("%d call%s", t.RetiredCalls, plural(t.RetiredCalls))},
	}
	sort.SliceStable(mechanisms, func(i, j int) bool { return mechanisms[i].bytes > mechanisms[j].bytes })

	var largest int64
	for _, m := range mechanisms {
		if m.bytes > largest {
			largest = m.bytes
		}
	}

	rows := make([]BarRow, len(mechanisms))
	for i, m := range mechanisms {
		pct, ok := stats.PartPercent(m.bytes, total)
		var fillRatio float64
		if largest > 0 {
			fillRatio = float64(m.bytes) / float64(largest)
		}
		rows[i] = BarRow{
			Label:      m.label,
			Tooltip:    m.tooltip,
			Percent:    pct,
			HasPercent: ok,
			FillRatio:  fillRatio,
			CountLabel: m.countLabel,
			Bytes:      m.bytes,
			BytesLabel: formatBytes(m.bytes),
		}
	}
	return rows
}

// buildOverview composes the percent-saved hero, the summary cards, and the
// suppressed-by-mechanism bars from a totals tally.
//
// HeroHeadline is always the headline percent-saved figure. The tokens and
// dollar cards both price the suppressed-byte figure as a proxy for tokens
// saved, ccusage-style: extrapolate from the same percent-saved figure as
// the hero headline (suppressed / (suppressed+actual)) rather than a second,
// independently-computed bytes-per-token ratio — pct/(100-pct) is the odds
// form of that percentage, which is algebraically the same suppressed/actual
// scale factor a separate ratio would give, just derived from the number
// already on screen instead of recomputed. Those priced tokens are then
// converted to dollars at this rollup's own cost-per-token rate. Both
// TranscriptTokens and TranscriptCostUSD (see internal/transcript's
// SessionUsage doc) count only content newly fed to the model — cache-read
// replays of earlier turns and output tokens are excluded at the source —
// so the price-per-token rate stays a stable per-content-unit price instead
// of one that inflates with how many turns a session ran. Cost, tokens, and
// content bytes are all summed independently across sessions before
// dividing, so a lifetime/project rollup mixing sessions of different sizes
// still comes out as a weighted average, not distorted by any single
// outlier session.
func buildOverview(t overviewTotals, projectCount, sessionCount int) Overview {
	suppressed := t.DedupBytes + t.BudgetBytesOmitted + t.RetiredBytes
	total := t.TranscriptContentBytes + suppressed
	hasActivity := suppressed > 0

	pct, hasPct := stats.Percent(t.DedupBytes, t.BudgetBytesOmitted, t.RetiredBytes, t.TranscriptContentBytes)

	// heroHeadline has three states, not two: real percent (transcript read,
	// at SessionEnd), real-but-partial activity (a mechanism has already
	// fired this session, but the transcript isn't readable yet), or
	// genuinely nothing (a session that hasn't done anything). Collapsing
	// the middle state into "No data yet" is what made a live, in-progress
	// session look identical to an untouched one for its entire duration —
	// see HasActivity's doc comment.
	heroHeadline := "No data yet"
	switch {
	case hasPct:
		heroHeadline = fmt.Sprintf("%.0f%% saved", pct)
	case hasActivity:
		heroHeadline = fmt.Sprintf("%s suppressed so far", formatBytes(suppressed))
	}

	hasTranscriptData := t.TranscriptContentBytes > 0
	hasTokenData := hasTranscriptData && t.TranscriptTokens > 0

	tokensSavedDetail := "no data yet"
	dollarDetail := "no data yet"
	if hasTokenData {
		tokensSaved := float64(t.TranscriptTokens) * pct / (100 - pct)
		tokensSavedDetail = formatTokens(tokensSaved)

		costPerToken := t.TranscriptCostUSD / float64(t.TranscriptTokens)
		usdSaved := tokensSaved * costPerToken
		dollarDetail = fmt.Sprintf("$%.2f", usdSaved)
	}

	// HeroUsageDetail reframes the percent-saved figure as extra runway on
	// the same plan — the actual claim agent-winglet makes — expressed as a
	// percent rather than a bare "Ax" multiplier with no unit. stats.Stretch
	// gives that runway as a ratio of 1 (e.g. 1.6129), so subtracting 100
	// after scaling to a percent yields "how much more," not "how much
	// total." Tokens/dollars genuinely require the completed transcript
	// (a cost-per-token rate derived from it), so those two cards stay "no
	// data yet" through an in-progress session — that's an honest gap, not
	// the same bug as the headline hiding data it already has.
	heroUsageDetail := "no data yet"
	heroUsageSub := ""
	switch {
	case hasPct:
		extraPercent := stats.Stretch(pct)*100 - 100
		heroUsageDetail = fmt.Sprintf("~%.0f%% more usage", extraPercent)
		heroUsageSub = "with the same plan"
	case hasActivity:
		heroUsageDetail = "% saved lands once this session ends"
	}

	// BytesSavedCard needs nothing but suppressed, which is already known
	// live — it does not need the completed transcript the way tokens/
	// dollars do, so it shouldn't wait for one either.
	bytesSavedDetail := "no data yet"
	if hasActivity {
		bytesSavedDetail = formatBytes(suppressed)
	}

	return Overview{
		HeroBytes:           suppressed,
		HeroTotalBytes:      total,
		HeroTotalBytesLabel: formatBytes(total),
		HeroPercent:         pct,
		HeroHeadline:        heroHeadline,
		HeroUsageDetail:     heroUsageDetail,
		HeroUsageSub:        heroUsageSub,
		HasTranscriptData:   hasTranscriptData,
		HasActivity:         hasActivity,
		BytesSavedCard: Card{
			Label: "Bytes saved", Tooltip: bytesSavedTooltip, Detail: bytesSavedDetail,
		},
		TokensSavedCard: Card{
			Label: "Tokens saved", Tooltip: tokensSavedTooltip, Detail: tokensSavedDetail,
		},
		DollarSavedCard: Card{
			Label: "Money saved", Tooltip: moneySavedTooltip, Detail: dollarDetail,
		},
		Bars:         barRows(t, total),
		ProjectCount: projectCount,
		SessionCount: sessionCount,
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// formatBytes renders a byte count the way the rest of the receipt does —
// a plain magnitude, never implying a cost or token-savings figure, since no
// such validated measurement exists yet.
// formatTokens renders a token count with a K/M/B/T suffix (base 1000, unlike
// formatBytes' base 1024 — tokens aren't a binary-scaled unit). n is a
// float64 because it's a derived estimate (suppressed bytes * a
// tokens-per-byte ratio), not a directly counted integer.
func formatTokens(n float64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%.0f", n)
	}
	div, exp := float64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", n/div, "KMBT"[exp])
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ProjectRow is one row of the Projects screen: the project's name (dir
// basename), whether the hook is actually active for this project right now
// (not just registry presence — see internal/registry's doc comment) via
// either the global ~/.claude/settings.json install.sh wires by default, or
// a legacy/opt-in per-project .claude/settings.json entry, and its lifetime
// tally (Overview) — the same shape the Overview screen uses, so the
// frontend renders both with one shared component.
type ProjectRow struct {
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	Installed bool     `json:"installed"`
	Overview  Overview `json:"overview"`
}

// GetProjects returns one row per registered, still-existing project. Each
// row's Overview is that project's sum of session files (see
// stats.SumProject) — the same rollup GetOverview sums across projects, so a
// project row and its slice of Overview can never disagree.
func (a *App) GetProjects() ([]ProjectRow, error) {
	dirs, err := registry.Load()
	if err != nil {
		return nil, err
	}

	globalInstalled := registry.GlobalHookInstalled()
	rows := make([]ProjectRow, 0, len(dirs))
	for _, dir := range dirs {
		r, err := stats.SumProject(dir)
		if err != nil {
			return nil, err
		}

		rows = append(rows, ProjectRow{
			Name:      filepath.Base(dir),
			Path:      dir,
			Installed: globalInstalled || registry.HookInstalled(dir),
			Overview:  buildOverview(totalsFromRollup(r), 1, r.Sessions),
		})
	}
	return rows, nil
}

// SessionRow is one row of a project's per-session breakdown (ccusage's
// `ccusage session` report shape): one row per still-on-disk
// <sessionID>.stats.json file, using the same percentage-first Overview
// shape as the project/lifetime rollup. This only works because completed
// sessions' stats files are NOT deleted on SessionEnd — only
// stats.InvalidateSession (SessionStart/PostCompact) touches one, to wipe a
// resumed/compacted session's stale dedup/budget/retire tally while
// preserving its real transcript-usage figures (and removing the file only
// if nothing worth keeping is left) — so a finished session's file (and its
// suppressed-bytes/transcript-usage tally) persists for this to read.
type SessionRow struct {
	SessionID string   `json:"sessionId"`
	Overview  Overview `json:"overview"`
}

// GetSessionStats returns one row per session-stats file still on disk for
// projectDir, newest first (see stats.ListSessions).
func (a *App) GetSessionStats(projectDir string) ([]SessionRow, error) {
	files, err := stats.ListSessions(projectDir)
	if err != nil {
		return nil, err
	}

	rows := make([]SessionRow, 0, len(files))
	for _, f := range files {
		s, err := stats.LoadSession(projectDir, f.ID)
		if err != nil {
			return nil, err
		}
		rows = append(rows, SessionRow{
			SessionID: f.ID,
			Overview:  overviewFromSession(s, 1, 1),
		})
	}
	return rows, nil
}
