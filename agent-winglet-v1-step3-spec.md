# SPEC: Agent Winglet v1 — Step 3 (minimal cross-platform desktop app)

Companion to `~/agent-winglet-spec.md` (the master spec). That spec's §5/§6
already name a "tray/menu-bar app" as a Phase 3 deliverable, gated on "once
there's more than one harness to manage/visualize." This doc **reprioritizes
that ahead of schedule**, at the user's request, and **upgrades its scope**
from a thin status/control surface to a real small window app with an actual
UI — the master spec's tray-only framing (§6, "Tray app stack: Go +
`getlantern/systray`") undersells what's being asked for here and is
superseded by §4 below for this deliverable specifically. Multi-harness
support is *not* in scope yet — this app visualizes the Claude-Code-only
mechanisms that already exist (step 1: Session Ledger, step 2: Context
Lifecycle hooks), nothing more.

## 1. What "step 1" and "step 2" already shipped

(Condensed from the now-removed `agent-winglet-v1-remaining.md`, recoverable
at git commit `5d5ed5b^`.)

- **Step 1 — Session Ledger** (`internal/ledger`, `cmd/ledger-hook`): dedupes
  exact-repeat Bash output within a session.
- **Step 2 — Context Lifecycle hooks** (`internal/phase`, `internal/retire`):
  output budgeting (long stdout trimmed to head/tail), an
  investigate→implement phase-boundary suggestion, and post-boundary
  retirement of investigate-tool output to disk with a receipt in-transcript.
- **Savings receipt** (`internal/stats`): a per-session tally (dedup hits/
  bytes, budget trims/lines, retired calls/bytes) and a lifetime tally,
  printed as a one-line message at `SessionEnd` unless
  `AGENT_WINGLET_QUIET=1`.
- All of it is Claude-Code-hooks-only — no daemon, no GUI. State lives as
  JSON files under `<project>/.claude/agent-winglet/`, **keyed per-project**
  (see §6 — this matters for the app).
- Measurement gate (usage_per_solve validation) is explicitly deferred, not
  done. Nothing here should be marketed as "proven to save cost" — only
  "mechanically works as designed" (master spec §7, §8.2).

## 2. Goals

- A minimal desktop app, **Windows / macOS / Linux**, that makes the
  already-built savings receipt (§1) visible without reading JSON files or
  parsing hook stdout — a dashboard, not a new mechanism.
- Native-feeling on macOS specifically (the user's own platform and, per
  master spec §10.1's launch checklist, the platform getting signed/notarized
  builds first) while remaining genuinely usable, not just "not broken," on
  Windows and Linux.
- Small surface area: view stats, see which projects have the hook
  installed, toggle quiet mode. Not a control panel for mechanisms that
  don't have per-mechanism on/off switches today (see §7 — most levers
  aren't independently toggleable yet; don't invent toggles the hook binary
  can't honor).

## 3. Non-goals

- Not the tray-only "status surface" the master spec's §6 originally scoped
  — this is a real window with real screens (see §8), though a tray/menu-bar
  icon is still in scope as a launcher/glance surface, not the whole app.
- Not multi-harness (Cursor/Copilot) visualization — nothing in step 1/2
  exists for those harnesses yet, so there's nothing to visualize.
- Not the Metering Profiler (master spec §5.2/Phase 4) — no cost-estimate
  UI. The app shows the same raw counts the receipt already prints (dedup
  hits, bytes, trims, retired calls) — master spec §8.2 explicitly warns
  against shipping a self-reported "savings" number as if it were validated
  cost reduction. Label these as counts, not dollars or "tokens saved."
- Not shipping mechanism-level enable/disable toggles beyond quiet mode
  unless §7's config-file plumbing is built first — a UI control with no
  wired backend is worse than no control.

## 4. Design references

Two references were named: **Flighty** and **caveman.so**. Both fetched and
reviewed directly (2026-08-03) rather than worked from memory.

**Flighty** (flighty.com) — clean, minimalist, high information density made
scannable through hierarchy and spacing rather than decoration. Card-based
data blocks (gate, delay, weather, baggage each their own block), a timeline
view for sequential status, status-based color coding instead of decorative
color, dark mode as a first-class citizen across every Apple platform it
ships on, live-activity-style glanceability (the thing updates itself,
you don't have to ask). This is the primary reference for the app's actual
screens (§8) — the receipt/stats data is inherently the same shape as
Flighty's data blocks (a count, a status, a timestamp).

**caveman.so** — minimalist single-column layout, numbered sections, heavy
sans-serif with monospace for technical/code bits, sparse iconography,
live before/after token counters as the centerpiece interaction.

**Flag worth being explicit about**: caveman.so is not a neutral aesthetic
reference — it's a **direct competitor**, a token-compression toolkit
targeting the same "reduce AI agent cost" niche this product is in (its own
tagline: "Cut 65% of your tokens"). Master spec §10.2 already documents one
competitive precedent (`headroom-desktop`) for the *licensing* question;
this is a second one, for the *product* question, worth the user's own
awareness even though it wasn't asked. For this spec, only the *pattern* is
being borrowed (a live numeric hero stat, generous whitespace, monospace for
technical values) — applied to Agent Winglet's own receipt data, not any of
caveman's copy, layout code, or visual identity.

## 5. Aesthetic direction (HIG-grounded, not freehand)

Pulled from Apple's Human Interface Guidelines (via the design-review
reference set), translated for a webview-rendered cross-platform app rather
than native AppKit — see §6 for why webview.

**Typography**: don't bundle a custom font. Use each OS's native font stack
so macOS renders actual San Francisco, Windows renders Segoe UI, Linux
renders its distro default:
```css
font-family: -apple-system, "SF Pro Text", "Segoe UI", "Ubuntu",
  system-ui, sans-serif;
font-family: ui-monospace, "SF Mono", "Cascadia Code", "Consolas", monospace;
```
Desktop default body size 13pt per HIG (mobile's 17pt doesn't apply); avoid
Ultralight/Thin/Light weights even for the "hero number" — HIG flags these
as hard to read at small sizes, use Semibold/Bold for the hero stat instead
of faking weight via oversized Thin type (the caveman.so approach — large
Thin numerals read as "marketing site," not "native app").

**Color / materials / dark mode**: no in-app light/dark toggle — HIG is
explicit that an app-specific appearance setting is an anti-pattern; follow
the OS (`prefers-color-scheme` in the webview, which all three targets
support). Status-based color coding (Flighty's pattern) over decorative
color: e.g. a project with the hook installed and active gets one accent,
one that's stale/uninstalled gets a neutral/muted one — never color-only,
pair with an icon or label (accessibility — color-blind safe). Vibrancy/
blur (`backdrop-filter`) is a progressive enhancement only: WebView2
(Windows) and macOS's WKWebView render it acceptably, WebKitGTK (Linux)
support is inconsistent — treat any glass/blur surface as decorative and
ship a solid, correctly-contrasted fallback background under it, never rely
on blur for legibility.

**Iconography**: sparse, one consistent icon set for every glyph in the app
(Lucide or Phosphor are reasonable SF-Symbols-equivalent choices — outline
style, consistent stroke weight) — don't mix an icon font with inline SVGs
with emoji. Status icons scale with the text next to them, not fixed-px.

**Spacing / shape**: generous padding inside cards (Flighty's and
caveman.so's shared trait), continuous/rounded corners on cards (~10–12px
radius reads as "Mac-native" without literally being an NSVisualEffectView),
shadow-based elevation instead of hairline borders for card separation.

**Anti-patterns to avoid specifically because this is a webview pretending
to be native**: don't hand-draw traffic-light window controls (mismatched
inset/spacing versus the real thing reads as fake immediately) — use the
webview shell's native window chrome (see §6) rather than a custom title
bar unless the framework's frameless mode is pixel-verified against a real
macOS title bar. Don't hardcode a light-only palette anywhere "temporarily"
— HIG's dark-mode note applies doubly to a small app where every screen will
get built once; retrofitting dark mode later means touching every screen
twice.

## 6. Architecture / stack decision

**Decision: Wails (Go backend + OS-native webview frontend), not
`getlantern/systray` alone, not Tauri, not Electron.**

The master spec's original tray-app stack pick (Go + `getlantern/systray`,
§6) was scoped for a *thin status/control surface* — "the real logic lives
in the daemon/hooks, not the UI." §2 of this doc changes that premise: the
user wants actual screens with real layout, matching Flighty/caveman-style
design (§4/§5), which a systray dropdown menu cannot render. That requires
reopening the stack choice:

- **Wails (chosen)**: Go backend, frontend rendered in the OS's native
  webview (WebView2 on Windows, WebKit via WebKitGTK on Linux, WKWebView on
  macOS) instead of a bundled Chromium. Two decisive wins for this specific
  codebase: (1) it's Go, so the app can `import` `internal/ledger`,
  `internal/stats`, `internal/phase`, `internal/retire` directly — reading
  the same on-disk state the hook binary already writes, no IPC/FFI
  boundary, no reimplementing the JSON schema in a second language; (2) it
  ships a real HTML/CSS/JS frontend, so §5's design direction is fully
  buildable, unlike a systray-only app.
- **Tauri (rejected, reconsidered)**: master spec §6 rejected it specifically
  for "cross-compiling Rust to Windows/Linux from macOS is genuinely
  painful." That specific objection is largely moot now — the master spec's
  own §6 CI-matrix guidance (`windows-latest`/`ubuntu-latest`/`macos-latest`
  building natively, not cross-compiling from one host) resolves it for
  *either* stack, and `headroom-desktop` (master spec §10.2) proves it out
  in the wild on Tauri specifically. Still rejected for this app, but for a
  different, still-valid reason: it would split this single-language Go repo
  into Go (hooks) + Rust (app), doubling the toolchain surface for a
  one-person project with no Rust code anywhere else in it, for a feature
  that doesn't need Rust's performance ceiling.
- **Electron (rejected)**: same reasoning the master spec already gave for
  the tray app — heavier runtime (bundles its own Chromium instead of using
  the OS webview) than a stats dashboard justifies, and same repo-language-
  split problem as Tauri (Node instead of Rust, same issue).
- **`getlantern/systray` alone (rejected as the *whole* app, kept as a
  *piece* of it — see open risk below)**: correct for the original
  tray-only scope, insufficient for §2's actual screens.

**Open risk, flag before committing build time**: Wails does not ship
first-class systray support the way `getlantern/systray` does standalone: a
menu-bar glance icon (in scope per §3 — "launcher/glance surface, not the
whole app") likely means running `getlantern/systray` alongside the Wails
window in the same Go binary, which is a documented community pattern but
not an officially maintained Wails feature. Spike this before committing to
the full screen build in §8 — if it's flaky, the fallback is "app icon in
the Dock/taskbar only, no menu-bar glance," which is a real scope cut, not
a footnote.

## 7. Data model gap: per-project stats, no global aggregation

`internal/stats` keys every state file on `projectDir` (the hook's `cwd`) —
confirmed by reading `internal/stats/stats.go`:
`.claude/agent-winglet/lifetime.stats.json` lives *inside each project*,
there is no global store today. A user with the hook installed in three
repos has three independent lifetime tallies and nothing that sums them.

Flighty's and caveman.so's shared pattern — one confident hero number — only
works here if the app can produce one. **Decision: add a small global
registry, don't change where stats live.**

- `install.sh` (already the single place that writes hook config into a
  project) additionally appends that project's absolute path to
  `~/.agent-winglet/projects.json` (a flat list, dedup on install, no other
  content) — new but small change, consistent with install.sh already being
  the source of truth for "which projects have this installed."
- The app reads that registry, then reads each listed project's
  `lifetime.stats.json` directly (no daemon, no IPC — same "just read the
  files the hooks already write" approach the rest of this product uses) and
  sums for the hero number, while keeping the per-project breakdown
  (§8 Projects screen) from the same data, unsummed.
- A project whose directory no longer exists (moved/deleted) is skipped
  silently on read, pruned from the registry lazily on next `install.sh` run
  — don't add a background watcher for this, it's a cold-path edge case.

This is new scope beyond "just build a UI over existing data" — it requires
one small `install.sh` change. Calling it out explicitly rather than
quietly expanding install.sh's job without flagging it.

## 8. Screens

Minimal set, each mapped to an existing data source — nothing here invents
a mechanism that doesn't already exist in `internal/*`.

1. **Overview** (default screen). Hero number = lifetime dedup bytes +
   retired bytes suppressed, summed across the §7 registry, in the
   caveman.so "big live number" style but set in a Semibold/Bold weight per
   §5, not Thin. Below it, three Flighty-style cards — Dedup, Budget Trims,
   Retired — each showing its own count + byte total from the summed
   lifetime tallies. No dollar figures, no "% saved" (§3 non-goal).
2. **Projects**. One row per `~/.agent-winglet/projects.json` entry: project
   name (dir basename), install status (hook config present in that
   project's `.claude/settings.json` — read, don't assume from registry
   presence alone, since a project can be removed from the hook without
   being removed from the registry), and that project's own lifetime tally.
   Clicking a row expands to that project's own three-card breakdown
   (same layout as Overview, scoped).
3. **Settings**. `AGENT_WINGLET_QUIET` toggle — but note: the hook reads
   this as an *environment variable* at invocation time, which a GUI app's
   toggle cannot set for a terminal-launched Claude Code session (different
   process tree, no shared env). Shipping this toggle requires the hook
   binary to also check a config file (e.g.
   `~/.agent-winglet/config.json`) in addition to the env var, env var
   taking precedence for backward compatibility. That's a small
   `cmd/ledger-hook` change, not just an app-side one — sequence it before
   building this screen's toggle, not after (a toggle with no wired backend
   is worse than no toggle, per §3).
4. No dedicated "Mechanisms" on/off screen for v1 of this app — none of the
   individual levers (budgeting, phase-boundary suggestion, retirement) has
   its own independent kill switch in `cmd/ledger-hook` today, and adding
   four new env-var/config-driven feature flags is new hook-binary scope
   that belongs in its own spec, not folded silently into "build the UI."

Menu-bar/tray icon (§6 open risk permitting): single glyph, click opens/
focuses the main window at the Overview screen. No dropdown menu contents
beyond that in v1 — resolves the risk noted in §6 toward the simpler side
if the systray+Wails combination turns out fragile.

## 9. Cross-platform build, signing, testing

Reuses the master spec's existing guidance rather than inventing new
tooling:

- **CI matrix** (master spec §6): stand up `windows-latest` / `ubuntu-latest`
  / `macos-latest` GitHub Actions builds for the Wails app now, at the same
  time as this feature, rather than waiting for "Phase 3 starts" as the
  master spec originally sequenced it — this *is* the Phase 3 trigger now.
- **Manual smoke testing without owning the hardware** (master spec §6):
  UTM for ARM Windows/Linux, or a low-cost cloud Linux box with a desktop +
  VNC if x86-specific behavior matters. Needed here more than it was for a
  systray-only app, since actual window chrome/webview rendering is exactly
  the thing CI can't catch.
- **GNOME systray landmine** (master spec §6): still applies if §6's
  systray spike goes forward — document the "AppIndicator/
  KStatusNotifierItem Support" extension requirement in the README.
- **Signed/notarized macOS builds** (master spec §10.1 launch checklist):
  applies to this app the same way it was scoped for the original tray app
  — do this before any public distribution, not after.
- **No-network-access claim** (master spec §10.1): the app reads local
  files only (§7) — keep it that way. If a later version needs to check for
  hook-binary updates, that's a new, disclosed network call, not something
  to add quietly while building this.

## 10. Suggested build order

1. Spike the Wails + `getlantern/systray` combination (§6's open risk) in
   isolation, before writing any screen — this determines whether §8's tray
   icon ships or gets cut.
2. `install.sh` registry change (§7) — small, unblocks everything else.
3. Overview screen (§8.1) against real data from this repo's own
   `.claude/agent-winglet/` state (dev fixture, same pattern the hooks
   themselves use per README's "Developing this repo" section).
4. Projects screen (§8.2).
5. `cmd/ledger-hook` config-file support for quiet mode (§8.3's backend
   prerequisite), then the Settings screen itself.
6. CI matrix (§9) — stand up in parallel with 3–5, not after.
7. Signing/notarization (§9) — last, once the screens are stable enough that
   rebuilding a signed artifact on every UI tweak isn't wasted effort.

## 11. Open risks to resolve before/while building

- §6: Wails + systray integration fidelity — unresolved until spiked.
- §7: registry file format/location (`~/.agent-winglet/projects.json`) is a
  new on-disk contract between `install.sh` and the app — get it right
  once, since both sides need to agree on it.
- §8.3: config-file vs env-var precedence for quiet mode needs the same
  same-session/cross-session care the rest of this product already applies
  to state files (master spec §5.1's scope constraint) — a *global* quiet
  toggle has no session-boundary risk (it's not a context substitution), so
  this one is lower-risk than the ledger/phase/retire state, but should
  still be reasoned through explicitly, not assumed safe by analogy.
- Whether the hero number (§8.1) double-counts anything: dedup bytes and
  retired bytes are disjoint mechanisms (confirmed by reading
  `cmd/ledger-hook/main.go` — the repeat-check and the post-boundary retire
  check are mutually exclusive branches per tool call), so summing them is
  safe. Re-verify this if either mechanism's trigger conditions change.
