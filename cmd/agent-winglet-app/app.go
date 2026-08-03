package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/umitkaanusta/agent-winglet/internal/config"
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
// but internal/stats only ever tracked budget trims by lines omitted, never
// bytes (see stats.Session.BudgetLinesOmitted) — so a fixed "byte total"
// field would be a lie for Budget Trims. Rather than fabricate a byte figure
// that doesn't exist in the underlying data, this card reports the same "N
// lines omitted" framing the SessionEnd receipt message already uses.
// Deliberately never a dollar figure or a "% saved": no validated cost or
// token-savings measurement exists yet, so showing one would misrepresent a
// raw suppressed-content count as a proven savings claim.
type Card struct {
	Label  string `json:"label"`
	Count  int    `json:"count"`
	Detail string `json:"detail"`
}

// Overview is the Overview screen's data: the hero number (dedup bytes +
// retired bytes, summed across every registered project) plus the three
// per-mechanism cards, and how many projects contributed. Summing dedup and
// retired bytes is safe because they're mutually exclusive per tool call —
// cmd/ledger-hook's handlePostToolUse only ever takes the repeat-check
// branch or the post-boundary retire branch for a given call, never both —
// so no byte is ever counted twice. HeroBytes is the raw count for any
// future use; HeroDetail is the pre-formatted string the UI renders.
type Overview struct {
	HeroBytes    int64  `json:"heroBytes"`
	HeroDetail   string `json:"heroDetail"`
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

	var total stats.Lifetime
	for _, dir := range dirs {
		l, err := stats.LoadLifetime(dir)
		if err != nil {
			return Overview{}, err
		}
		total.Sessions += l.Sessions
		total.DedupHits += l.DedupHits
		total.DedupBytes += l.DedupBytes
		total.BudgetTrims += l.BudgetTrims
		total.BudgetLinesOmitted += l.BudgetLinesOmitted
		total.RetiredCalls += l.RetiredCalls
		total.RetiredBytes += l.RetiredBytes
	}

	return lifetimeToOverview(&total, len(dirs)), nil
}

func lifetimeToOverview(l *stats.Lifetime, projectCount int) Overview {
	heroBytes := l.DedupBytes + l.RetiredBytes
	return Overview{
		HeroBytes:  heroBytes,
		HeroDetail: formatBytes(heroBytes),
		Dedup: Card{
			Label: "Dedup", Count: l.DedupHits,
			Detail: formatBytes(l.DedupBytes) + " not replayed",
		},
		BudgetTrims: Card{
			Label: "Budget Trims", Count: l.BudgetTrims,
			Detail: fmt.Sprintf("%d lines omitted", l.BudgetLinesOmitted),
		},
		Retired: Card{
			Label: "Retired", Count: l.RetiredCalls,
			Detail: formatBytes(l.RetiredBytes) + " archived",
		},
		ProjectCount: projectCount,
		SessionCount: l.Sessions,
	}
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
// a legacy/opt-in per-project .claude/settings.json entry, and its own
// lifetime tally broken into the same three cards as Overview.
type ProjectRow struct {
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	Installed bool     `json:"installed"`
	Overview  Overview `json:"overview"`
}

// GetProjects returns one row per registered, still-existing project.
func (a *App) GetProjects() ([]ProjectRow, error) {
	dirs, err := registry.Load()
	if err != nil {
		return nil, err
	}

	globalInstalled := registry.GlobalHookInstalled()
	rows := make([]ProjectRow, 0, len(dirs))
	for _, dir := range dirs {
		l, err := stats.LoadLifetime(dir)
		if err != nil {
			return nil, err
		}
		rows = append(rows, ProjectRow{
			Name:      filepath.Base(dir),
			Path:      dir,
			Installed: globalInstalled || registry.HookInstalled(dir),
			Overview:  lifetimeToOverview(l, 1),
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
