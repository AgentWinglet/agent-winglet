package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/umitkaanusta/agent-winglet/internal/config"
	"github.com/umitkaanusta/agent-winglet/internal/registry"
	"github.com/umitkaanusta/agent-winglet/internal/stats"
)

// windowDays is the size of the "recent" window shown alongside the lifetime
// tally (ccusage defaults to daily/weekly rather than only all-time; see
// spec.md's open question on windowing). 7 days, matching ccusage's weekly
// report.
const windowDays = 7

// App is the Wails-bound backend. Every method here does the same thing the
// rest of agent-winglet already does: read the JSON files the hooks and
// install.sh already write, no daemon, no IPC beyond Wails' own JS<->Go
// bridge. See internal/stats' package doc for why lifetime tallies (unlike
// ledger/phase/retire state) are safe to read outside the hooks' own
// same-session lifecycle.
type App struct {
	ctx context.Context
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// Card is one stat block: a raw suppressed-content count plus a
// pre-formatted detail string. Detail is formatted server-side because the
// three mechanisms don't share a unit — dedup and retirement suppress bytes,
// budget-trims suppress both lines and bytes (see stats.Session.
// BudgetBytesOmitted) — so the byte-percentage prefix and the raw
// supporting detail (hits/trims/calls, lines, bytes) are composed here, once,
// instead of duplicated in the frontend.
type Card struct {
	Label  string `json:"label"`
	Count  int    `json:"count"`
	Detail string `json:"detail"`
}

// Overview is the Overview screen's data: a percentage-first hero (see
// stats.Percent) plus the three per-mechanism cards, and how many projects/
// sessions contributed. HeroBytes is the raw suppressed-byte count (dedup +
// budget-trim + retire) backing HeroHeadline/HeroSubtext — summing across
// mechanisms is safe because they're mutually exclusive per tool call:
// cmd/ledger-hook's handlePostToolUse only ever takes one suppression branch
// for a given call, never two, so no byte is ever counted twice.
// HeroHeadline/HeroSubtext are pre-formatted so the frontend never has to
// duplicate the "no data yet" guard or the stretch-multiplier math.
type Overview struct {
	HeroBytes    int64  `json:"heroBytes"`
	HeroHeadline string `json:"heroHeadline"`
	HeroSubtext  string `json:"heroSubtext"`
	Dedup        Card   `json:"dedup"`
	BudgetTrims  Card   `json:"budgetTrims"`
	Retired      Card   `json:"retired"`
	ProjectCount int    `json:"projectCount"`
	SessionCount int    `json:"sessionCount"`
}

// GetOverview sums the lifetime tally of every project in the registry that
// still exists on disk. A project whose lifetime.stats.json is missing
// (hook installed but never fired yet) contributes a zero tally, not an
// error.
func (a *App) GetOverview() (Overview, error) {
	dirs, err := registry.Load()
	if err != nil {
		return Overview{}, err
	}

	var total overviewTotals
	sessions := 0
	for _, dir := range dirs {
		l, err := stats.LoadLifetime(dir)
		if err != nil {
			return Overview{}, err
		}
		total.add(totalsFromLifetime(l))
		sessions += l.Sessions
	}

	return buildOverview(total, len(dirs), sessions), nil
}

// GetOverviewWindow is GetOverview's windowDays-recent counterpart (ccusage's
// weekly report, applied here): instead of each project's ever-growing
// lifetime.stats.json, it sums only the per-session stats files whose file
// modification time falls within the last windowDays — the same files
// GetSessionStats reads, since completed sessions are never deleted (see
// SessionRow's doc comment).
func (a *App) GetOverviewWindow() (Overview, error) {
	dirs, err := registry.Load()
	if err != nil {
		return Overview{}, err
	}

	since := time.Now().AddDate(0, 0, -windowDays)
	var total overviewTotals
	sessions := 0
	for _, dir := range dirs {
		t, n, err := windowedTotals(dir, since)
		if err != nil {
			return Overview{}, err
		}
		total.add(t)
		sessions += n
	}

	return buildOverview(total, len(dirs), sessions), nil
}

// overviewTotals is the minimal common shape buildOverview needs — both
// stats.Session (one session's tally) and stats.Lifetime (a project or
// cross-project rollup's tally) share these fields, just without a common
// Go type, so callers adapt each into this before formatting.
type overviewTotals struct {
	DedupHits          int
	DedupBytes         int64
	BudgetTrims        int
	BudgetLinesOmitted int
	BudgetBytesOmitted int64
	RetiredCalls       int
	RetiredBytes       int64
	ProcessedBytes     int64
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
	t.ProcessedBytes += o.ProcessedBytes
}

func totalsFromLifetime(l *stats.Lifetime) overviewTotals {
	return overviewTotals{
		DedupHits:          l.DedupHits,
		DedupBytes:         l.DedupBytes,
		BudgetTrims:        l.BudgetTrims,
		BudgetLinesOmitted: l.BudgetLinesOmitted,
		BudgetBytesOmitted: l.BudgetBytesOmitted,
		RetiredCalls:       l.RetiredCalls,
		RetiredBytes:       l.RetiredBytes,
		ProcessedBytes:     l.ProcessedBytes,
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
		ProcessedBytes:     s.ProcessedBytes,
	}
}

func overviewFromLifetime(l *stats.Lifetime, projectCount int) Overview {
	return buildOverview(totalsFromLifetime(l), projectCount, l.Sessions)
}

func overviewFromSession(s *stats.Session, projectCount, sessionCount int) Overview {
	return buildOverview(totalsFromSession(s), projectCount, sessionCount)
}

// windowedTotals sums every session-stats file in projectDir modified at or
// after since, returning the summed totals and how many session files
// contributed. A project with no .claude/agent-winglet directory yet
// contributes a zero tally and 0 sessions, not an error.
func windowedTotals(projectDir string, since time.Time) (overviewTotals, int, error) {
	files, err := listSessionFiles(projectDir)
	if err != nil {
		return overviewTotals{}, 0, err
	}

	var total overviewTotals
	n := 0
	for _, f := range files {
		if f.modTime.Before(since) {
			continue
		}
		s, err := stats.LoadSession(projectDir, f.id)
		if err != nil {
			return overviewTotals{}, 0, err
		}
		total.add(totalsFromSession(s))
		n++
	}
	return total, n, nil
}

// buildOverview composes the percentage-first hero and the three cards from
// a totals tally. Percentages are of ProcessedBytes (see stats.Percent/
// stats.PartPercent) — the original size of every tool response the hook
// evaluated, whether or not it ended up suppressed. When ProcessedBytes is
// zero (hook installed, nothing seen yet), every percentage is replaced with
// "no data yet" rather than a misleading "0%" (0% reads as "this doesn't
// work," a different claim than "nothing to measure yet" — see spec.md).
func buildOverview(t overviewTotals, projectCount, sessionCount int) Overview {
	suppressed := t.DedupBytes + t.BudgetBytesOmitted + t.RetiredBytes

	headline := "No data yet"
	subtext := "Install the hook and run a session to see savings here."
	if pct, ok := stats.Percent(t.DedupBytes, t.BudgetBytesOmitted, t.RetiredBytes, t.ProcessedBytes); ok {
		stretch := stats.Stretch(pct)
		headline = fmt.Sprintf("%.0f%% saved", pct)
		subtext = fmt.Sprintf("same package, ~%.1fx more headroom — %s of tool output never replayed",
			stretch, formatBytes(suppressed))
	}

	return Overview{
		HeroBytes:    suppressed,
		HeroHeadline: headline,
		HeroSubtext:  subtext,
		Dedup: Card{
			Label: "Repeat output skipped", Count: t.DedupHits,
			Detail: cardDetail(t.DedupBytes, t.ProcessedBytes,
				fmt.Sprintf("%d hit%s, %s", t.DedupHits, plural(t.DedupHits), formatBytes(t.DedupBytes))),
		},
		BudgetTrims: Card{
			Label: "Long output trimmed", Count: t.BudgetTrims,
			Detail: cardDetail(t.BudgetBytesOmitted, t.ProcessedBytes,
				fmt.Sprintf("%d trim%s, %d line%s / %s", t.BudgetTrims, plural(t.BudgetTrims),
					t.BudgetLinesOmitted, plural(t.BudgetLinesOmitted), formatBytes(t.BudgetBytesOmitted))),
		},
		Retired: Card{
			Label: "Old investigation output archived", Count: t.RetiredCalls,
			Detail: cardDetail(t.RetiredBytes, t.ProcessedBytes,
				fmt.Sprintf("%d call%s, %s", t.RetiredCalls, plural(t.RetiredCalls), formatBytes(t.RetiredBytes))),
		},
		ProjectCount: projectCount,
		SessionCount: sessionCount,
	}
}

// cardDetail prefixes rest with this mechanism's own percentage of
// processed bytes, e.g. "12% of output — 3 hits, 4.0 KiB". Falls back to
// rest alone when there's no processed-bytes data yet, same "no data yet"
// guard as the hero (just omitting the prefix rather than repeating the
// phrase in every card).
func cardDetail(mechanismBytes, processedBytes int64, rest string) string {
	pct, ok := stats.PartPercent(mechanismBytes, processedBytes)
	if !ok {
		return rest
	}
	return fmt.Sprintf("%.0f%% of output — %s", pct, rest)
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
// a legacy/opt-in per-project .claude/settings.json entry, its lifetime
// tally (Overview), and its windowDays-recent tally (Window) — both broken
// into the same three cards, same as the Overview screen's lifetime/window
// pair.
type ProjectRow struct {
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	Installed bool     `json:"installed"`
	Overview  Overview `json:"overview"`
	Window    Overview `json:"window"`
}

// GetProjects returns one row per registered, still-existing project.
func (a *App) GetProjects() ([]ProjectRow, error) {
	dirs, err := registry.Load()
	if err != nil {
		return nil, err
	}

	globalInstalled := registry.GlobalHookInstalled()
	since := time.Now().AddDate(0, 0, -windowDays)
	rows := make([]ProjectRow, 0, len(dirs))
	for _, dir := range dirs {
		l, err := stats.LoadLifetime(dir)
		if err != nil {
			return nil, err
		}
		windowTotals, windowSessions, err := windowedTotals(dir, since)
		if err != nil {
			return nil, err
		}
		rows = append(rows, ProjectRow{
			Name:      filepath.Base(dir),
			Path:      dir,
			Installed: globalInstalled || registry.HookInstalled(dir),
			Overview:  overviewFromLifetime(l, 1),
			Window:    buildOverview(windowTotals, 1, windowSessions),
		})
	}
	return rows, nil
}

// SessionRow is one row of a project's per-session breakdown (ccusage's
// `ccusage session` report shape): one row per still-on-disk
// <sessionID>.stats.json file, using the same percentage-first Overview
// shape as the project/lifetime rollup. This only works because completed
// sessions' stats files are NOT deleted on SessionEnd — only
// stats.InvalidateSession (SessionStart/PostCompact) removes one, to wipe a
// resumed/compacted session's stale tally — so a finished session's file
// (and its ProcessedBytes/suppressed-bytes tally) persists for this to read.
type SessionRow struct {
	SessionID string   `json:"sessionId"`
	Overview  Overview `json:"overview"`
}

// sessionFileInfo identifies one <sessionID>.stats.json file on disk and its
// modification time — the closest thing to a session timestamp available,
// since the stats.Session content itself carries no clock reading.
type sessionFileInfo struct {
	id      string
	modTime time.Time
}

// listSessionFiles returns every session-stats file still on disk for
// projectDir (excluding lifetime.stats.json), newest first by modification
// time. A project with no .claude/agent-winglet directory yet (hook never
// fired here) returns an empty slice, not an error, same fail-soft
// convention the rest of this package follows.
func listSessionFiles(projectDir string) ([]sessionFileInfo, error) {
	dir := filepath.Join(projectDir, ".claude", "agent-winglet")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []sessionFileInfo
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == "lifetime.stats.json" || !strings.HasSuffix(name, ".stats.json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, sessionFileInfo{
			id:      strings.TrimSuffix(name, ".stats.json"),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })
	return files, nil
}

// GetSessionStats returns one row per session-stats file still on disk for
// projectDir, newest first.
func (a *App) GetSessionStats(projectDir string) ([]SessionRow, error) {
	files, err := listSessionFiles(projectDir)
	if err != nil {
		return nil, err
	}

	rows := make([]SessionRow, 0, len(files))
	for _, f := range files {
		s, err := stats.LoadSession(projectDir, f.id)
		if err != nil {
			return nil, err
		}
		rows = append(rows, SessionRow{
			SessionID: f.id,
			Overview:  overviewFromSession(s, 1, 1),
		})
	}
	return rows, nil
}

// Settings is the Settings screen's data: just the quiet-mode toggle.
// Dedup, budgeting, retirement, and the compact nudge have no independent
// on/off switch in cmd/ledger-hook today, so there's nothing else to wire a
// toggle to yet.
type Settings struct {
	Quiet bool `json:"quiet"`
}

func (a *App) GetSettings() (Settings, error) {
	cfg, err := config.Load()
	if err != nil {
		return Settings{}, err
	}
	return Settings{Quiet: cfg.Quiet}, nil
}

// SetQuiet writes the config-file quiet-mode toggle. This only affects the
// SessionEnd receipt message; every underlying mechanism (dedup, budgeting,
// retirement, the compact nudge) stays fully active either way — see
// cmd/ledger-hook's quiet() doc comment for why a config file exists
// alongside AGENT_WINGLET_QUIET (the env var still wins when set, for a
// terminal-launched session this app's toggle can't reach).
func (a *App) SetQuiet(quiet bool) error {
	return config.Save(&config.Config{Quiet: quiet})
}
