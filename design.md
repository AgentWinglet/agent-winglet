# Winglet — Design Decisions

Reference doc for `cmd/agent-winglet-app/frontend` (Wails desktop app — vanilla JS/HTML/CSS). Ships on macOS, Windows, and Linux — nothing here may depend on mac-only chrome to look finished. This is a decision log, not a spec to re-derive from: if you're touching this UI, match what's here rather than reintroducing something that was already tried and reverted (noted below).

## Layout, invariant

Sidebar (200px) + main two-pane, three screens (Overview, Projects, Settings), card-grid + bar-list information architecture, the existing sparse Lucide-derived icon set (`icons.js`, stroke-width 1.75 throughout). None of this changes without a real reason — it's the one thing every screen shares.

## Typography

One font family everywhere: `--font-ui` (`-apple-system, "SF Pro Text", "Segoe UI", "Ubuntu", system-ui, sans-serif`). `--font-display` is an alias for `--font-ui`, used on titles/numerics/wordmark — kept as a separate token so a future display-face swap only touches one line, not every rule.

**Reverted:** a self-hosted Martian Mono variable font was tried for all numerics/titles (the classic "distinctive monospace" dashboard move). Explicitly rejected on review — read as generic AI-dashboard aesthetic, not distinctive. Don't reintroduce a second typeface here without a concrete reason; system sans is the considered choice, not a placeholder.

Numeric elements (`.hero-number`, `.hero-ring-figure`, `.card-detail`) keep `font-variant-numeric: tabular-nums` so live-updating figures don't jitter — that's independent of the font-family decision and should stay regardless of what font is in use.

## Color & dark mode

Dark mode is the primary theme (this app is the kind of thing left running in a dark dock/taskbar), not an inverted afterthought. Token structure is shared across themes; values differ:

| Token | Light | Dark |
|---|---|---|
| `--bg` | `#f5f5f7` | `#111113` |
| `--bg-elevated` | `#ffffff` | `#1a1a1d` |
| `--sidebar-bg` | `#ececef` | `#0e0e10` |
| `--accent` | `#1f9d55` | `#3fe08a` |
| `--accent-glow` | `0 0 10px rgba(31,157,85,.2)` | `0 0 10px rgba(63,224,138,.3)` |

`--accent-glow` (box-shadow) and `--accent-glow-bg` (a low-opacity fill for the background wash, §Background below) are additive highlight tokens — used sparingly (hero ring, nav active rail, toggle-on) so the accent still reads as a considered highlight, not flat fill everywhere. Don't apply `--accent-glow` to every green element; if it's everywhere it stops meaning anything.

## Hero — the signature moment

A radial ring (`.hero-ring-wrap`, 128×128, SVG `<circle>`, r=54, stroke-width 9) sits beside the headline text instead of a horizontal bar stacked below it. Percent fill via `stroke-dasharray`/`stroke-dashoffset`; the percent figure is centered inside the ring (`.hero-ring-figure`).

The ring only shows a real percent when `overview.hasTranscriptData` is true (mirrors the backend's own `hasPct` gate in `app.go`'s `buildOverview` — `stats.Percent` requires `transcriptContentBytes > 0`, which is exactly `hasTranscriptData`). Before that: empty track, `—` in the figure. No tick marks, no dial labels.

**Entrance motion** plays once per `navigate()` to Overview, never on the 1s poll ticks that follow (`main.js`'s `loadOverview(container, { animate })` — only the first call after `renderOverviewScreen` passes `animate: true`). Ring sweeps 0→value, the percent figure counts up via `requestAnimationFrame`, mechanism bars beneath stagger ~50ms apart. All gated behind `prefers-reduced-motion: reduce` (see Motion below). This is deliberately scoped to Overview only — Projects/session rows render their rings at final value with no entrance replay, so expanding a project doesn't trigger a mini fireworks show every time.

## Cards

`.card-grid` is `1.6fr 1fr 1fr`, not a flat 3-up — the first card (bytes saved, the headline metric) is wider, larger type (22px vs 16px `.card-detail`), and has a thin 1px accent top-border. This is the one place a colored-edge accent survived review (see Rejected below) — it's a single edge on a single card, not a repeated pattern across a list, so it doesn't read as templated.

## Mechanism bars (`.bar-row`)

Plain elevated rows, no per-row accent marker. The percent badge (`.bar-pill`) carries the accent via its own background tint (`--accent-bg`), same as the original design.

**Reverted:** a 3px colored left-edge tick on every `.bar-row` was tried and explicitly rejected on review — a colored strip repeated down every card in a list is a recognizable "AI-generated dashboard" tell. If you're tempted to add a per-row identity marker again, don't — vary something structural (spacing, type, icon) instead of a colored edge.

## Sidebar & navigation

- Wordmark (`.sidebar-title`): small, uppercase, letter-spaced, sits above a hairline rule (`border-bottom`) — typography alone carries the "designed" feeling, no badge/icon needed.
- Active nav state: a 2px left-edge accent rail (`.nav-item::before`, animates `height` 0→16px on `.active`) plus a faint background tint — the rail is the primary "you are here" signal, the fill is secondary. This is the one left-edge accent pattern kept for nav (distinct from the rejected bar-row tick above: it's a single indicator on one active item, not a marker repeated across every row in a list).
- Icon color and tooltip opacity/transform transition (150ms/120ms) rather than snapping instantly.

## Vibrancy (macOS only, progressive enhancement)

`body[data-os="darwin"] .sidebar` gets `backdrop-filter: blur(20px) saturate(1.4)` over a translucent `--sidebar-bg`, gated behind `@supports (backdrop-filter: blur(1px))`. Requires `Mac.WindowIsTranslucent: true` in `main.go`'s `wails.Run` options. Windows/Linux always get the solid `--sidebar-bg` fill — never a broken/unsupported blur. This must degrade cleanly; nothing else in the app depends on it rendering.

## Cross-platform chrome (`data-os`)

`App.GetPlatform()` (Go, `runtime.GOOS`) is called once at startup (`main.js`'s `initPlatform()`) and stamped as `data-os` on `<body>`. Drives:
- `--titlebar-inset` (sidebar top padding): 44px default (macOS traffic-light inset), 20px Windows, 16px Linux.
- The vibrancy gate above.

Don't hardcode mac-specific spacing/chrome outside this mechanism — if a new platform-dependent value comes up, extend the `body[data-os="…"]` pattern rather than sniffing the user agent (WebView2/WebKitGTK/WKWebView don't distinguish reliably).

## Background

A single low-opacity radial glow behind the hero area (`.main`'s `background-image`, `--accent-glow-bg`, ~5–7% opacity, centered top-left). No pattern/texture/grid overlay — that reads as a themed instrument panel, which this app deliberately avoids (no aviation/gauge metaphor tied to the product name).

## Settings

Toggle's "on" state adds `--accent-glow` as a box-shadow, otherwise mechanically unchanged. Empty states stay plain/low-emphasis — no retint needed beyond what falls out of the color tokens.

## Motion

Every new transition (ring fill, nav rail, toggle, chevron, icon color, tooltip) is disabled under `prefers-reduced-motion: reduce` (see the `@media` block at the bottom of `style.css`). The hero entrance animation checks `matchMedia('(prefers-reduced-motion: reduce)')` in JS directly, same rule. `--accent-glow`/rail/fill changes are always paired with a position or color change too, never the sole signal for state — holds under forced-colors/high-contrast modes.

## Non-goals (still true)

No new screens, no new data/metrics, no icon-set replacement, no departure from the sidebar+main two-pane structure, no visual identity that depends on macOS-only chrome to look finished, no aviation/gauge/instrument-panel decorative concept.
