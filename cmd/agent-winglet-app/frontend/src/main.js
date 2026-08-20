import './style.css';
import { icons } from './icons.js';
import { renderBrand } from './brand.js';
import {
  CheckForUpdate,
  GetAccountStatus,
  GetCompactNudgesEnabled,
  GetHookHealth,
  GetOverview,
  GetPlatform,
  GetProjects,
  GetSessionStats,
  Logout,
  OpenBillingPortal,
  OpenDataFolder,
  OpenPricing,
  OpenReleaseNotes,
  OpenUpdateRelease,
  RefreshEntitlement,
  SetCompactNudgesEnabled,
  SetClaudeHookEnabled,
  SetCodexHookEnabled,
  StartBrowserSignIn,
  StartFreeTrial,
  UninstallWinglet,
} from '../wailsjs/go/main/App';
import { ClipboardSetText } from '../wailsjs/runtime/runtime';

const state = {
  screen: 'overview',
  expanded: new Set(),
  expandedSessions: new Set(),
  sessionsByProject: new Map(),
  settingsError: '',
  accountStatus: null,
  accountError: '',
  hookHealth: null,
  updateStatus: null,
  aboutError: '',
  dismissedUpdateVersions: new Set(),
};

const UPDATE_DISMISSED_PREFIX = 'winglet.update.dismissed.';

const NAV_ITEMS = [
  { id: 'overview', label: 'Overview', icon: icons.overview },
  { id: 'projects', label: 'Projects', icon: icons.projects },
];

const SETTINGS_ITEMS = [
  { id: 'account', label: 'Account' },
  { id: 'preferences', label: 'Preferences' },
  { id: 'installations', label: 'Installations' },
  { id: 'about', label: 'About' },
];

// Session/project stats files on disk are updated live by the hook (every
// dedup hit, budget trim, and retire is written immediately — see
// internal/stats' package doc), but nothing pushes those changes to this
// app. Polling on a short interval is what actually makes "start a session,
// watch the numbers move" true, instead of only updating on next navigation
// or app restart. Cleared on every navigate() so only the visible screen
// polls, and re-armed each time that screen is (re)rendered.
const REFRESH_INTERVAL_MS = 1000;
let pollTimer = null;

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer);
    pollTimer = null;
  }
}

function startPolling(fn) {
  stopPolling();
  pollTimer = setInterval(fn, REFRESH_INTERVAL_MS);
}

// A poll-driven container.innerHTML replace tears down and recreates every
// element inside it, even on a tick where nothing actually changed — which,
// among other things, kills whatever the mouse is currently hovering (e.g. a
// card's info tooltip), so it flickers shut once a second instead of staying
// put while read. renderIfChanged skips that rebuild whenever data
// JSON-matches what's already on screen for key, so a hovered tooltip is
// only ever disturbed by a poll tick that has real new data to show — never
// by an idle one. Anything the draw should also react to (e.g. which rows
// are expanded) must be folded into data, since only data is compared.
const lastRender = new Map();
function renderIfChanged(key, data, draw) {
  const snapshot = JSON.stringify(data);
  if (lastRender.get(key) === snapshot) return;
  lastRender.set(key, snapshot);
  draw();
}

function navigate(screen) {
  stopPolling();
  state.screen = screen === 'settings' ? 'preferences' : screen;
  render();
}

async function loadAccountStatus() {
  try {
    state.accountStatus = await GetAccountStatus();
    state.accountError = '';
  } catch (err) {
    state.accountError = err?.message || String(err);
    state.accountStatus = {
      state: 'server_error',
      message: 'Winglet could not read account status.',
      siteBaseURL: '',
      hookAllowed: false,
      dashboardAllowed: false,
    };
  }
}

async function loadUpdateStatus() {
  try {
    state.updateStatus = await CheckForUpdate();
  } catch {
    state.updateStatus = null;
  }
  renderUpdateBanner();
  const footer = document.querySelector('#version-footer');
  if (footer) footer.textContent = versionFooterText();
}

// Stamps data-os on <body> so CSS can scope the sidebar's title-bar inset
// (macOS's traffic lights need a taller dead zone than Windows/GNOME
// title bars do) and gate mac-only vibrancy without sniffing the user
// agent, which the three platforms' WebViews don't distinguish reliably.
// Runs in parallel with the first render rather than blocking on it — the
// CSS falls back to the mac inset by default, so a frame or two before this
// IPC round-trip resolves isn't visually disruptive.
async function initPlatform() {
  try {
    document.body.dataset.os = await GetPlatform();
  } catch {
    // Falls back to the default (mac) inset baked into style.css.
  }
}

const prefersReducedMotion = () => window.matchMedia('(prefers-reduced-motion: reduce)').matches;

// animateHeroEntrance plays the once-per-screen-load hero motion: the
// ring sweeps from 0 to its value, the percent figure counts up alongside
// it, and the mechanism bars beneath fill in staggered ~50ms apart. Called
// only from the Overview screen's first render for a given navigate() —
// never from a poll tick, which would replay this every second and turn a
// live-updating ring into a strobe (see loadOverview's animate flag).
function animateHeroEntrance(container, hasPct, percent) {
  if (prefersReducedMotion()) return;

  const ringFill = container.querySelector('.hero-ring-fill');
  const figure = container.querySelector('.hero-ring-figure');
  if (ringFill) {
    const svg = ringFill.closest('svg');
    const circumference = parseFloat(svg.dataset.circumference);
    const targetOffset = ringFill.style.strokeDashoffset;
    ringFill.style.transition = 'none';
    ringFill.style.strokeDashoffset = String(circumference);
    void ringFill.getBoundingClientRect(); // force reflow so the reset above commits before re-enabling the transition
    requestAnimationFrame(() => {
      ringFill.style.transition = '';
      ringFill.style.strokeDashoffset = targetOffset;
    });
  }

  if (figure && hasPct) {
    const target = Math.round(Math.min(100, Math.max(0, percent)));
    const duration = 500;
    const start = performance.now();
    figure.textContent = '0%';
    requestAnimationFrame(function tick(now) {
      const t = Math.min(1, (now - start) / duration);
      const eased = 1 - Math.pow(1 - t, 3);
      figure.textContent = `${Math.round(eased * target)}%`;
      if (t < 1) requestAnimationFrame(tick);
    });
  }

  container.querySelectorAll('.bar-fill').forEach((el, i) => {
    const targetWidth = el.style.width;
    el.style.transition = 'none';
    el.style.width = '0%';
    void el.getBoundingClientRect();
    setTimeout(() => {
      el.style.transition = '';
      el.style.width = targetWidth;
    }, i * 50);
  });
}

// Replacing a container's innerHTML on every poll tick can reset its scroll
// position back to the top in a WebView, even though nothing about where
// you were looking actually changed — jarring specifically on screens
// designed to be watched continuously while numbers update every second.
// Every poll-driven render captures scrollTop immediately before mutating
// the DOM and restores it right after, so watching a session's numbers move
// never yanks the view back to the top mid-read.
function withPreservedScroll(el, mutate) {
  const top = el.scrollTop;
  mutate();
  el.scrollTop = top;
  // The synchronous restore above isn't always enough: some WebViews (this
  // app's frontend runs in one, not a regular browser tab) run a layout
  // pass on the next frame after a large innerHTML swap that can re-clamp
  // scrollTop back toward 0 before that first assignment is visually
  // committed — restoring again once that frame settles closes the gap
  // without weakening the synchronous restore, which still matters
  // wherever that extra pass doesn't happen.
  requestAnimationFrame(() => {
    el.scrollTop = top;
  });
}

// heroRing renders the signature radial ring: a quiet 360° track plus
// an accent fill sized to heroPercent, with the percent figure centered
// inside it — the ring *is* the number's reading, not a separate element
// next to a bar. hasTranscriptData (== hasPct server-side, see stats.Percent
// and buildOverview in app.go) gates whether there's a real percent to show
// yet; before that, the track renders empty rather than guessing.
function heroRing(overview) {
  const hasPct = overview.hasTranscriptData;
  const percent = hasPct ? Math.min(100, Math.max(0, overview.heroPercent)) : 0;
  const r = 54;
  const circumference = 2 * Math.PI * r;
  const offset = circumference - (percent / 100) * circumference;
  return `
    <div class="hero-ring-wrap">
      <svg class="hero-ring" viewBox="0 0 128 128" data-circumference="${circumference.toFixed(2)}">
        <circle class="hero-ring-track" cx="64" cy="64" r="${r}"></circle>
        <circle class="hero-ring-fill" cx="64" cy="64" r="${r}" style="stroke-dasharray: ${circumference.toFixed(2)}; stroke-dashoffset: ${offset.toFixed(2)}"></circle>
      </svg>
      <div class="hero-ring-figure">${hasPct ? `${Math.round(percent)}%` : '—'}</div>
    </div>`;
}

// hero renders the primary figure: the signature ring beside the
// headline percent-saved text (e.g. "38% saved") and a one-line restatement
// of that same figure as plan stretch ("→ Your plan goes ~61% further") —
// the actual claim agent-winglet makes.
function hero(overview) {
  // hasTranscriptData gates the ring's real percent (it needs the completed
  // transcript, only read at SessionEnd) but must not gate the usage-detail
  // line too — hasActivity alone is enough to explain why the number isn't a
  // percent yet, and that explanation is exactly what an in-progress session
  // most needs to show instead of going silent.
  const showUsageLine = overview.hasTranscriptData || overview.hasActivity;
  return `
    <div class="hero">
      ${heroRing(overview)}
      <div class="hero-body">
        <div class="hero-number">${overview.heroHeadline}</div>
        ${showUsageLine ? `<div class="hero-usage">${overview.hasTranscriptData ? '→ ' : ''}${overview.heroUsageDetail} ${overview.heroUsageSub}</div>` : ''}
      </div>
    </div>`;
}

// barList renders the suppressed-by-mechanism bars: a total-bytes header
// stat above one row per mechanism, ordered (server-side) descending by
// bytes, fill width relative to the largest mechanism in this rollup.
function barList(overview) {
  const rows = overview.bars
    .map(
      (bar) => `
    <div class="bar-row">
      <div class="bar-row-top">
        <span class="bar-label">
          ${bar.label}
          <span class="info-affordance" tabindex="0">
            ${icons.info}
            <span class="tooltip" role="tooltip">${bar.tooltip}</span>
          </span>
        </span>
        <span class="bar-pill">${bar.hasPercent ? `${bar.percent.toFixed(0)}%` : '—'}</span>
      </div>
      <div class="bar-track">
        <div class="bar-fill" style="width: ${(bar.fillRatio * 100).toFixed(1)}%"></div>
      </div>
      <div class="bar-row-bottom">
        <span>${bar.countLabel}</span>
        <span>${bar.bytesLabel}</span>
      </div>
    </div>`
    )
    .join('');

  return `
    <div class="bar-list">
      <div class="bar-list-header">Where the savings come from</div>
      ${rows}
    </div>`;
}

function cardRow(overview) {
  const cards = [overview.bytesSavedCard, overview.tokensSavedCard, overview.dollarSavedCard];
  return `
    <div class="card-grid">
      ${cards
        .map(
          (c) => `
        <div class="card">
          <div class="card-label">
            ${c.label}
            <span class="info-affordance" tabindex="0">
              ${icons.info}
              <span class="tooltip" role="tooltip">${c.tooltip}</span>
            </span>
          </div>
          <div class="card-detail">
            ${c.detail}${c.estimated && c.detail !== 'no data yet' ? ' <span class="card-detail-est">(est.)</span>' : ''}
          </div>
          ${c.sub ? `<div class="card-sub">${c.sub}</div>` : ''}
        </div>`
        )
        .join('')}
    </div>`;
}

// statsBlock is the full hierarchy shared by the Overview screen, each
// Projects-screen row, and each expanded session row nested inside one:
// hero (percent saved, or live suppressed bytes so far — see hero's own doc
// comment) -> summary cards (bytes/tokens/$) -> per-mechanism bars. One
// function, three call sites, so the "X% saved / plan goes X% further / bar"
// treatment can never drift between them — a session watched live gets the
// same story an already-ended one gets in the Overview rollup, not a
// stripped-down view.
function statsBlock(overview) {
  return `
    ${hero(overview)}
    ${cardRow(overview)}
    ${barList(overview)}`;
}

// animate is only true on a screen's first render (see renderOverviewScreen)
// — poll ticks pass nothing, so the entrance sweep/count-up/stagger
// plays once per navigate(), not every second.
async function loadOverview(container, { animate = false } = {}) {
  const o = await GetOverview();
  // Guard against a poll tick landing after the user has already navigated
  // away — GetOverview is async, so a stale response could otherwise clobber
  // whatever screen is now showing.
  if (state.screen !== 'overview') return;
  renderIfChanged('overview', o, () => {
    withPreservedScroll(container, () => {
      container.innerHTML = `
        <h1 class="screen-title">Overview</h1>
        ${statsBlock(o)}
      `;
    });
    if (animate) animateHeroEntrance(container, o.hasTranscriptData, o.heroPercent);
  });
}

async function renderOverviewScreen(container) {
  // Cleared so the first render after (re)entering this screen always draws
  // — otherwise, revisiting with unchanged data would compare equal to
  // whatever was last cached and leave the "Loading…" placeholder above stuck
  // on screen forever.
  lastRender.delete('overview');
  container.innerHTML = `<div class="empty-state">Loading…</div>`;
  await loadOverview(container, { animate: true });
  startPolling(() => loadOverview(container));
}

// heroInline renders the compact one-line summary shown on every collapsed
// expander (a project row or a session row): the percent-saved headline,
// the raw bytes behind it in parentheses, and the same figure reframed as
// plan stretch — the one line that has to justify expanding the row,
// so it carries the same three numbers the expanded statsBlock leads with
// rather than a status readout unrelated to savings. Before the transcript
// is read (see Overview.HasTranscriptData), pct/bytes/runway aren't
// comparable numbers yet, so this falls back to the plain heroHeadline
// ("No data yet" / "X suppressed so far") instead of parenthesizing a
// figure that doesn't mean anything yet.
function heroInline(overview) {
  if (!overview.hasTranscriptData) {
    return `<span class="hero-inline">${overview.heroHeadline}</span>`;
  }
  return `
    <span class="hero-inline">
      ${overview.heroHeadline} <span class="hero-inline-bytes">(${overview.bytesSavedCard.detail})</span>
      <span class="hero-inline-usage">${overview.heroUsageDetail}</span>
    </span>`;
}

// loadProjects fetches and renders the Projects screen's rows. Used both for
// the initial render and every poll tick (see REFRESH_INTERVAL_MS) — polling
// calls this directly instead of going through renderProjectsScreen, so an
// already-expanded project's card doesn't flash back to a bare loading state
// every few seconds.
//
// An expanded project's sessions-section is seeded with its last-known
// (cached) content right here, synchronously, instead of left empty until
// renderSessionsSection's own async fetch resolves a moment later. That
// gap mattered: on every poll tick this function tears down and rebuilds
// the entire project list (#project-list.innerHTML), so an expanded row's
// session cards briefly disappeared and reappeared each second — shrinking
// #main's scrollable height for that instant, which clamps scrollTop toward
// 0 regardless of what withPreservedScroll restores it to right after
// (there's nothing to scroll down to yet). renderSessionsSection's own
// scroll-preserving fetch then only made things worse: it captured
// scrollTop *after* that clamp had already happened, so it faithfully
// restored the wrong, already-collapsed position. Seeding with cached
// content keeps #main's height essentially stable across the swap, so
// there's nothing left to clamp.
async function loadProjects(container) {
  const rows = await GetProjects();
  if (state.screen !== 'projects') return;

  if (!rows || rows.length === 0) {
    renderIfChanged('projects', { empty: true }, () => {
      withPreservedScroll(container, () => {
        container.innerHTML = `
          <h1 class="screen-title">Projects</h1>
          <p class="screen-subtitle">Projects with the agent-winglet hook installed.</p>
          <div class="empty-state">No projects registered yet — the hooks install globally; a project is added automatically the first time Claude or Codex starts a session in it.</div>
        `;
      });
    });
    return;
  }

  // expanded is folded into the compared data so toggling a row forces a
  // redraw even when the rows themselves haven't changed since the last
  // poll tick — see renderIfChanged's own doc comment.
  renderIfChanged('projects', { rows, expanded: [...state.expanded].sort() }, () => {
    withPreservedScroll(container, () => {
      container.innerHTML = `
        <h1 class="screen-title">Projects</h1>
        <div id="project-list"></div>
      `;

      const list = container.querySelector('#project-list');
      list.innerHTML = rows
        .map((row) => {
          const cachedSessions = state.sessionsByProject.get(row.path);
          const seeded = state.expanded.has(row.path) && cachedSessions ? sessionsSectionMarkup(row.path, cachedSessions) : '';
          return `
        <div class="project-row ${state.expanded.has(row.path) ? 'expanded' : ''}" data-path="${row.path}">
          <button class="project-row-header" data-toggle="${row.path}">
            <span class="project-row-chevron">${icons.chevron}</span>
            <div>
              <div class="project-name">${row.name}</div>
              <div class="project-path">${row.path}</div>
            </div>
            <span class="project-row-spacer"></span>
            ${heroInline(row.overview)}
          </button>
          <div class="project-row-detail">
            ${statsBlock(row.overview)}
            <div class="sessions-section" data-sessions="${row.path}">${seeded}</div>
          </div>
        </div>`;
        })
        .join('');

      list.querySelectorAll('[data-toggle]').forEach((btn) => {
        btn.addEventListener('click', () => {
          const path = btn.getAttribute('data-toggle');
          if (state.expanded.has(path)) {
            state.expanded.delete(path);
          } else {
            state.expanded.add(path);
          }
          loadProjects(container);
        });
      });

      // Wire the toggle handlers for whatever session rows were just seeded
      // from cache above — renderSessionsSection (called below, once its own
      // fetch resolves) re-wires these again once it swaps in fresh data, same
      // as it always has.
      for (const row of rows) {
        const target = list.querySelector(`[data-sessions="${CSS.escape(row.path)}"]`);
        if (target) wireSessionToggles(container, target, row.path);
      }
    });
  });

  for (const row of rows) {
    if (state.expanded.has(row.path)) {
      renderSessionsSection(container, row.path);
    }
  }
}

async function renderProjectsScreen(container) {
  // See renderOverviewScreen's identical reset for why this can't be skipped.
  lastRender.delete('projects');
  container.innerHTML = `<div class="empty-state">Loading…</div>`;
  await loadProjects(container);
  startPolling(() => loadProjects(container));
}

// sessionsSectionMarkup renders one project's sessions-section content —
// shared by loadProjects (seeding from cache, synchronously, to avoid the
// empty-then-populated flicker described on loadProjects' own doc comment)
// and renderSessionsSection (rendering fresh data once its fetch resolves).
// One function, two call sites, so the two can never drift out of sync.
function sessionsSectionMarkup(projectPath, sessions) {
  if (!sessions || sessions.length === 0) {
    return `<div class="sessions-empty">No sessions on disk yet for this project.</div>`;
  }
  return `
    <div class="sessions-title">Sessions (${sessions.length})</div>
    ${sessions
      .map((row) => {
        const key = `${projectPath}::${row.sessionId}`;
        const expanded = state.expandedSessions.has(key);
        return `
        <div class="session-row ${expanded ? 'expanded' : ''}">
          <button class="session-row-header" data-toggle-session="${key}">
            <span class="project-row-chevron">${icons.chevron}</span>
            <span class="session-id">${row.sessionId}</span>
            <span class="session-agent">${sessionAgentLabel(row.agent)}</span>
            <span class="project-row-spacer"></span>
            ${heroInline(row.overview)}
          </button>
          <div class="session-row-detail">${statsBlock(row.overview)}</div>
        </div>`;
      })
      .join('')}
  `;
}

function sessionAgentLabel(agent) {
  return agent === 'codex' ? 'Codex' : 'Claude';
}

// wireSessionToggles attaches the expand/collapse click handler to every
// session row inside target — shared by loadProjects (for content it seeded
// from cache) and renderSessionsSection (for freshly-fetched content).
function wireSessionToggles(container, target, projectPath) {
  target.querySelectorAll('[data-toggle-session]').forEach((btn) => {
    btn.addEventListener('click', () => {
      const key = btn.getAttribute('data-toggle-session');
      if (state.expandedSessions.has(key)) {
        state.expandedSessions.delete(key);
      } else {
        state.expandedSessions.add(key);
      }
      renderSessionsSection(container, projectPath);
    });
  });
}

// renderSessionsSection always fetches fresh session data (per-session stats
// files are written live, on every dedup hit/trim/retire — see
// internal/stats — so an in-progress session's numbers grow between polls
// too, not just once it ends). The "Loading…" placeholder only shows the
// first time a project is expanded and loadProjects had no cache to seed it
// with yet — every poll tick after that swaps content already seeded from
// cache for fresh content in place, never an empty gap in between.
async function renderSessionsSection(container, projectPath) {
  const hadCache = state.sessionsByProject.has(projectPath);
  if (!hadCache) {
    const target = container.querySelector(`[data-sessions="${CSS.escape(projectPath)}"]`);
    if (target) target.innerHTML = `<div class="sessions-loading">Loading sessions…</div>`;
  }

  const sessions = await GetSessionStats(projectPath);
  state.sessionsByProject.set(projectPath, sessions);

  // Bail if the user moved off this screen, or collapsed/re-navigated away
  // from this project row, while the fetch above was in flight.
  if (state.screen !== 'projects' || !state.expanded.has(projectPath)) return;
  const target = container.querySelector(`[data-sessions="${CSS.escape(projectPath)}"]`);
  if (!target) return;

  // expandedHere is folded into the compared data so expanding/collapsing an
  // individual session row forces a redraw even when its stats haven't moved
  // since the last poll tick — see renderIfChanged's own doc comment.
  const expandedHere = sessions.map((s) => `${projectPath}::${s.sessionId}`).filter((key) => state.expandedSessions.has(key));
  renderIfChanged(`sessions:${projectPath}`, { sessions, expandedHere }, () => {
    withPreservedScroll(container, () => {
      target.innerHTML = sessionsSectionMarkup(projectPath, sessions);
      wireSessionToggles(container, target, projectPath);
    });
  });
}

// The compact-nudges toggle controls the systemMessage/additionalContext
// the hook binaries emit directly — this dashboard has no part in showing it,
// only in this preference. Reading its state needs a round-trip
// (GetCompactNudgesEnabled), so this is async like the other screens, not
// fetched synchronously.
// installationsGateMessage explains, in terms specific to the signed-in
// account's actual state, why these integrations can't be turned on yet —
// replacing a single generic "sign in and subscribe" line that was equally
// wrong for someone who'd never signed in, someone with an unclaimed trial,
// and someone whose trial had just ended.
function installationsGateMessage(status) {
  switch (status?.state) {
    case 'trial_available':
      return "Start your free trial from the Account tab — until then, these won't do anything, even installed.";
    case 'expired':
      return "Your trial has ended. Subscribe from the Account tab — these won't do anything until you do.";
    case 'server_error':
      return status.message || 'Winglet could not verify your account.';
    default:
      return "Sign in and subscribe (or start your free trial) from the Account tab — these won't do anything until you do.";
  }
}

async function loadInstallations(container) {
  if (!state.accountStatus) await loadAccountStatus();
  state.hookHealth = await GetHookHealth();
  if (state.screen !== 'installations') return;
  const hookHealth = state.hookHealth;
  const hookAllowed = Boolean(state.accountStatus?.hookAllowed);
  const accountState = state.accountStatus?.state;

  renderIfChanged('installations', { hookHealth, hookAllowed, accountState, settingsError: state.settingsError }, () => {
    container.innerHTML = `
      <h1 class="screen-title">Installations</h1>
      <div class="settings-stack">
        ${state.settingsError ? `<div class="settings-error">${escapeHtml(state.settingsError)}</div>` : ''}
        ${hookAllowed ? '' : `<div class="settings-error">${escapeHtml(installationsGateMessage(state.accountStatus))}</div>`}
        <section class="settings-panel">
          <div class="hook-agent-list">
            ${hookAgentMarkup({
              name: 'Claude Code Integration',
              configured: hookHealth.claudeConfigured,
              reviewLikely: hookHealth.claudeReviewLikely,
              status: hookHealth.claudeStatus,
              detail: hookHealth.claudeDetail,
              action: hookHealth.claudeAction,
              key: 'claude',
              hookAllowed,
            })}
            ${hookAgentMarkup({
              name: 'Codex Integration',
              configured: hookHealth.codexConfigured,
              reviewLikely: hookHealth.codexReviewLikely,
              status: hookHealth.codexStatus,
              detail: hookHealth.codexDetail,
              action: hookHealth.codexAction,
              key: 'codex',
              hookAllowed,
            })}
          </div>
          <div class="settings-info">
            ${icons.info}
            <p>After enabling, disabling, or updating an integration, restart your terminal or IDE and start a new session so Winglet can pick up the change.</p>
          </div>
        </section>
        <section class="settings-panel settings-panel-danger">
          <div class="settings-panel-header">
            <div>
              <h2>Uninstall Winglet</h2>
              <p>Removes the app and disconnects the Claude Code and Codex hooks. Your saved usage stats and preferences in ~/.agent-winglet are kept.</p>
            </div>
            <button class="hook-action-button disable" type="button" data-uninstall>Uninstall</button>
          </div>
        </section>
      </div>
    `;
    container.querySelectorAll('[data-hook-action]').forEach((btn) => {
      btn.addEventListener('click', () => runHookAction(container, btn));
    });
    container.querySelectorAll('[data-uninstall]').forEach((btn) => {
      btn.addEventListener('click', () => runUninstall(container, btn));
    });
  });
}

// runUninstall calls the Go side's native confirm dialog + removal (see
// App.UninstallWinglet's doc comment for why it isn't a JS confirm() here).
// A successful uninstall quits the whole app shortly after on the Go side —
// there's nothing left to render at that point. The other two cases where
// the window is still around afterward both need to be visible, not silent:
// removal failing partway through (surfaced like any other settings error),
// and the user hitting Cancel in the native dialog — easy to land on by
// habit, since Cancel carries the Return-key equivalent there (see
// UninstallWinglet's doc comment) — which without an explicit message here
// would look identical to a click that silently did nothing.
async function runUninstall(container, btn) {
  const original = btn.textContent;
  state.settingsError = '';
  btn.disabled = true;
  btn.textContent = 'Uninstalling...';
  try {
    const proceeded = await UninstallWinglet();
    btn.disabled = false;
    btn.textContent = original;
    if (!proceeded) {
      state.settingsError = 'Uninstall cancelled — nothing was changed.';
      await renderInstallationsScreen(container);
    }
  } catch (err) {
    state.settingsError = err?.message || String(err);
    btn.disabled = false;
    btn.textContent = original;
    await renderInstallationsScreen(container);
  }
}

async function renderInstallationsScreen(container) {
  // See renderOverviewScreen's identical reset for why this can't be skipped.
  lastRender.delete('installations');
  container.innerHTML = `<div class="empty-state">Loading…</div>`;
  await loadInstallations(container);
  if (state.screen === 'installations') {
    startPolling(() => loadInstallations(container));
  }
}

async function renderPreferencesScreen(container) {
  const enabled = await GetCompactNudgesEnabled();
  if (state.screen !== 'preferences') return;

  container.innerHTML = `
    <h1 class="screen-title">Preferences</h1>
    <div class="settings-stack">
      <section class="settings-panel settings-panel-compact">
        <div class="settings-row">
          <div>
            <div class="settings-row-label">Compact nudges</div>
            <div class="settings-row-desc">Tell Claude Code or Codex to nudge you, inside the session, to run /compact once it moves from investigating to editing.</div>
          </div>
          <button class="toggle ${enabled ? 'on' : ''}" data-toggle-nudges aria-pressed="${enabled}" aria-label="Toggle compact nudges">
            <span class="toggle-knob"></span>
          </button>
        </div>
      </section>
    </div>
  `;
  container.querySelector('[data-toggle-nudges]').addEventListener('click', async (e) => {
    const btn = e.currentTarget;
    const next = !btn.classList.contains('on');
    btn.classList.toggle('on', next);
    btn.setAttribute('aria-pressed', String(next));
    await SetCompactNudgesEnabled(next);
  });
}

function platformLabel(goos) {
  switch (goos) {
    case 'darwin':
      return 'macOS';
    case 'windows':
      return 'Windows';
    case 'linux':
      return 'Linux';
    default:
      return '';
  }
}

// buildDiagnosticInfo assembles exactly what a support request needs and
// nothing more — no email, no tokens — since it's headed for the clipboard
// and from there possibly into a public support channel. Fetches hook
// health fresh rather than trusting state.hookHealth, which is only
// populated once the Installations screen has been visited this launch.
async function buildDiagnosticInfo() {
  if (!state.accountStatus) await loadAccountStatus();
  const hookHealth = await GetHookHealth().catch(() => null);
  return [
    `Winglet ${state.updateStatus?.currentVersion ? `v${state.updateStatus.currentVersion}` : '(version unknown)'} · ${platformLabel(document.body.dataset.os) || document.body.dataset.os || 'unknown OS'}`,
    `Account: ${accountLabel(state.accountStatus || {})}`,
    `Claude Code hook: ${hookHealth ? hookHealth.claudeStatus : 'unknown'}`,
    `Codex hook: ${hookHealth ? hookHealth.codexStatus : 'unknown'}`,
  ].join('\n');
}

// flashButtonLabel gives one-off action buttons (Copy to Clipboard, Check
// for Updates) a brief confirmation state without needing a persisted
// per-button flag in state — the timeout just no-ops harmlessly if the user
// has already navigated away and the button node is detached.
function flashButtonLabel(btn, html, revertHtml, ms = 1800) {
  btn.innerHTML = html;
  btn.disabled = true;
  setTimeout(() => {
    btn.innerHTML = revertHtml;
    btn.disabled = false;
  }, ms);
}

async function renderAboutScreen(container) {
  if (!state.updateStatus) await loadUpdateStatus();
  const version = state.updateStatus?.currentVersion;
  const platform = platformLabel(document.body.dataset.os);
  container.innerHTML = `
    <h1 class="screen-title">About</h1>
    <div class="settings-stack">
      ${state.aboutError ? `<div class="settings-error">${escapeHtml(state.aboutError)}</div>` : ''}
      <section class="settings-panel">
        <div class="settings-row">
          <div>
            <div class="settings-row-label">Winglet</div>
            <div class="settings-row-desc">${version ? `Version ${escapeHtml(version)}` : 'Version unknown'}${platform ? ` · ${escapeHtml(platform)}` : ''} · Apache License 2.0</div>
          </div>
          <button class="hook-action-button outline" type="button" data-about-action="check-updates">Check for Updates</button>
        </div>
        <div class="settings-row">
          <div>
            <div class="settings-row-label">Diagnostic info</div>
            <div class="settings-row-desc">Copies your Winglet version, platform, account status, and integration status for a support request.</div>
          </div>
          <button class="hook-action-button outline" type="button" data-about-action="copy-diagnostics">${icons.copy}Copy</button>
        </div>
        <div class="settings-row">
          <div>
            <div class="settings-row-label">Data folder</div>
            <div class="settings-row-desc">Saved usage stats, project registry, and preferences live in ~/.agent-winglet.</div>
          </div>
          <button class="hook-action-button outline" type="button" data-about-action="open-data-folder">Open Folder</button>
        </div>
      </section>
      <div class="about-links">
        <button class="hook-action-button outline" type="button" data-about-action="release-notes">Release notes</button>
      </div>
    </div>
  `;
  wireAboutActions(container);
}

function wireAboutActions(container) {
  container.querySelectorAll('[data-about-action]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const action = btn.getAttribute('data-about-action');
      if (action === 'release-notes') {
        OpenReleaseNotes();
        return;
      }
      if (action === 'check-updates') {
        btn.disabled = true;
        btn.textContent = 'Checking…';
        await loadUpdateStatus();
        flashButtonLabel(
          btn,
          state.updateStatus?.available ? 'Update available' : "You're up to date",
          'Check for Updates',
          2200
        );
        return;
      }
      if (action === 'copy-diagnostics') {
        try {
          const info = await buildDiagnosticInfo();
          await ClipboardSetText(info);
          flashButtonLabel(btn, 'Copied!', `${icons.copy}Copy`);
        } catch (err) {
          state.aboutError = err?.message || String(err);
          render();
        }
        return;
      }
      if (action === 'open-data-folder') {
        try {
          await OpenDataFolder();
        } catch (err) {
          state.aboutError = err?.message || String(err);
          render();
        }
      }
    });
  });
}

async function renderAccountScreen(container) {
  if (!state.accountStatus) {
    container.innerHTML = `<div class="empty-state">Loading...</div>`;
    await loadAccountStatus();
    render();
    return;
  }

  const status = state.accountStatus;
  const subscribed = status.dashboardAllowed && status.hookAllowed;
  container.innerHTML = `
    <h1 class="screen-title">Account</h1>
    <div class="settings-stack">
      ${state.accountError ? `<div class="settings-error">${escapeHtml(state.accountError)}</div>` : ''}
      <section class="settings-panel account-panel">
        <div class="account-status ${subscribed ? 'ok' : 'missing'}">
          <div>
            <div class="settings-row-label">${escapeHtml(status.emailHint || 'Winglet account')}</div>
            <div class="settings-row-desc">${escapeHtml(status.message || 'Sign in to Winglet to activate it.')}</div>
          </div>
          <span class="hook-status-badge">${escapeHtml(accountLabel(status))}</span>
        </div>
        ${subscribed ? accountSubscribedMarkup(status) : accountSignInMarkup(status)}
      </section>
      <div class="settings-info">
        ${icons.info}
        <p>For billing information — invoices, payment method, plan changes — head to <button class="settings-info-link" type="button" data-account-action="billing">agentwinglet.com</button>.</p>
      </div>
    </div>
  `;
  wireAccountActions(container);
}

// formatTrialCountdown turns status.expiresAt (only meaningful while
// state === 'trialing', where it's the trial's own end time — the signed
// entitlement's ExpiresAt, see appauth.Client.StartTrial's doc comment) into
// a short "ends in Xh"/"ends in Xd Yh" string. Returns '' once the trial has
// already lapsed so a stale re-render never claims time that's gone.
function formatTrialCountdown(expiresAt) {
  if (!expiresAt) return '';
  const ms = new Date(expiresAt).getTime() - Date.now();
  if (!Number.isFinite(ms) || ms <= 0) return '';
  const totalHours = Math.ceil(ms / 3_600_000);
  if (totalHours < 24) return `ends in ${totalHours}h`;
  const days = Math.floor(totalHours / 24);
  const hours = totalHours % 24;
  return `ends in ${days}d${hours ? ` ${hours}h` : ''}`;
}

// formatTrialDaysHours is formatTrialCountdown's always-both-units sibling,
// for the top-of-page trial banner ("ends in 2d 14h" rather than the
// account screen's terser, unit-dropping "ends in 6h"/"ends in 2d").
function formatTrialDaysHours(expiresAt) {
  if (!expiresAt) return '';
  const ms = new Date(expiresAt).getTime() - Date.now();
  if (!Number.isFinite(ms) || ms <= 0) return '';
  const totalHours = Math.ceil(ms / 3_600_000);
  const days = Math.floor(totalHours / 24);
  const hours = totalHours % 24;
  return `${days}d ${hours}h`;
}

// formatPlanLabel turns a raw billing-tier value from the site's API (e.g.
// "annual", "pro_yearly") into title-cased words instead of showing the raw
// enum verbatim next to the "Active" badge above it.
function formatPlanLabel(tier) {
  if (!tier) return 'Winglet';
  return tier
    .replace(/[_-]+/g, ' ')
    .trim()
    .split(' ')
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(' ');
}

function accountSubscribedMarkup(status) {
  const trialing = status.state === 'trialing';
  const countdown = trialing ? formatTrialCountdown(status.expiresAt) : '';
  return `
    <div class="account-meta">
      ${trialing
        ? `<div class="trial-pill">${icons.sparkle}Free trial${countdown ? ` · ${countdown}` : ''}</div>`
        : status.subscription
          ? `<div class="trial-pill">${icons.sparkle}${escapeHtml(formatPlanLabel(status.subscription.tier))} plan</div>`
          : ''}
    </div>
    <div class="account-actions">
      ${trialing ? `<button class="hook-action-button enable" type="button" data-account-action="pricing">Subscribe now</button>` : ''}
      <button class="hook-action-button ${trialing ? '' : 'enable'}" type="button" data-account-action="sync">Refresh</button>
      <button class="hook-action-button disable" type="button" data-account-action="logout">Sign out</button>
    </div>
  `;
}

// accountSignInMarkup covers every non-subscribed state the compact
// Settings > Account panel can show. "Sign in with browser" only appears
// when there's no emailHint — an already-signed-in account (trial_available,
// expired) has no business being offered a second sign-in, and doing so
// buried the actual next step (start the trial, or subscribe) behind a
// redundant button.
function accountSignInMarkup(status) {
  const trialAvailable = status.state === 'trial_available';
  const needsSubscribe = status.state === 'expired';
  return `
    <div class="account-actions">
      ${trialAvailable ? `<button class="hook-action-button enable" type="button" data-account-action="start-trial">${icons.sparkle}Start 3-day free trial</button>` : ''}
      ${needsSubscribe ? `<button class="hook-action-button enable" type="button" data-account-action="pricing">Subscribe</button>` : ''}
      ${status.emailHint ? '' : `<button class="hook-action-button enable" type="button" data-account-action="signin">Sign in with browser</button>`}
      ${status.emailHint ? `<button class="hook-action-button" type="button" data-account-action="sync">Refresh</button>` : ''}
      ${status.emailHint ? `<button class="hook-action-button disable" type="button" data-account-action="logout">Sign out</button>` : ''}
    </div>
  `;
}

function accountLabel(status) {
  if (status.dashboardAllowed && status.hookAllowed) return status.state === 'trialing' ? 'Trial active' : 'Active';
  if (status.state === 'trial_available') return 'Trial available';
  if (status.emailHint) return 'Subscription needed';
  return 'Signed out';
}

function wireAccountActions(container) {
  container.querySelectorAll('[data-account-action]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const action = btn.getAttribute('data-account-action');
      if (action === 'pricing') {
        OpenPricing();
        return;
      }
      if (action === 'billing') {
        OpenBillingPortal();
        return;
      }
      if (action === 'signin') {
        const originalLabel = btn.textContent;
        btn.disabled = true;
        btn.textContent = 'Waiting for browser…';
        try {
          await runAccountAction(async () => {
            state.accountStatus = await StartBrowserSignIn();
          });
        } finally {
          btn.disabled = false;
          btn.textContent = originalLabel;
        }
      }
      if (action === 'start-trial') {
        const originalLabel = btn.textContent;
        btn.disabled = true;
        btn.textContent = 'Starting trial…';
        try {
          await runAccountAction(async () => {
            state.accountStatus = await StartFreeTrial();
          });
        } finally {
          btn.disabled = false;
          btn.textContent = originalLabel;
        }
      }
      if (action === 'sync') {
        await runAccountAction(async () => {
          state.accountStatus = await RefreshEntitlement();
        });
      }
      if (action === 'logout') {
        await runAccountAction(async () => {
          state.accountStatus = await Logout();
        });
      }
    });
  });
}

async function runAccountAction(fn) {
  state.accountError = '';
  try {
    await fn();
  } catch (err) {
    state.accountError = err?.message || String(err);
    await loadAccountStatus();
  }
  render();
}

function needsAccountGate() {
  return (state.screen === 'overview' || state.screen === 'projects') &&
    (!state.accountStatus || !state.accountStatus.dashboardAllowed);
}

// gateContent picks the copy and actions for every non-trial gate state.
// signed_out and expired share the same shell (gateScreenMarkup) as the
// trial welcome screen (trialWelcomeMarkup) — one centered card, swapped
// content — so a brand-new user landing on 'signed_out' sees the same
// 3-day-trial pitch the 'trial_available' screen makes right after they
// actually sign in, instead of the trial only being mentioned once they're
// already past the point it would have sold them on signing in.
function gateContent(status) {
  switch (status.state) {
    // Reached almost exclusively after a claimed trial has run out (see
    // AccountStatus's doc comment in appauth.go) — the copy leads with that,
    // not a generic "subscribe to get started" pitch aimed at someone who
    // never had a trial.
    case 'expired':
      return {
        title: 'Subscribe to keep using Winglet',
        subtitle: 'Your trial has ended. Subscribe to keep Winglet running.',
        primaryAction: 'pricing',
        primaryLabel: 'See plans',
        secondaryAction: 'sync',
        secondaryLabel: 'I just subscribed — refresh',
      };
    case 'server_error':
      return {
        title: "Winglet couldn't verify your account",
        subtitle: status.message || 'Something went wrong. Try again in a moment.',
        primaryAction: 'sync',
        primaryLabel: 'Try again',
        secondaryAction: 'signin',
        secondaryLabel: 'Sign in again',
      };
    default:
      return {
        title: 'Sign in to Winglet',
        subtitle: 'Sign in to activate Winglet.',
        primaryAction: 'signin',
        primaryLabel: 'Sign in with browser',
        secondaryAction: 'pricing',
        secondaryLabel: 'View pricing',
      };
  }
}

function gateScreenMarkup(status) {
  const c = gateContent(status);
  return `
    <div class="gate-screen">
      <div class="gate-card">
        <div class="gate-brand">${renderBrand()}</div>
        <h1 class="gate-title">${escapeHtml(c.title)}</h1>
        <p class="gate-subtitle">${escapeHtml(c.subtitle)}</p>
        <button class="gate-cta" type="button" data-account-action="${c.primaryAction}">${escapeHtml(c.primaryLabel)}</button>
        <button class="gate-secondary" type="button" data-account-action="${c.secondaryAction}">${escapeHtml(c.secondaryLabel)}</button>
        ${state.accountError ? `<p class="gate-error">${escapeHtml(state.accountError)}</p>` : ''}
      </div>
    </div>`;
}

// trialWelcomeMarkup is the first-run screen a newly signed-in account with
// an unclaimed trial lands on (status.state === 'trial_available' — see
// appauth.Status.TrialEligible's doc comment).
function trialWelcomeMarkup() {
  return `
    <div class="gate-screen">
      <div class="gate-card">
        <div class="gate-brand">${renderBrand()}</div>
        <h1 class="gate-title">Welcome to Winglet</h1>
        <p class="gate-subtitle">Start your free 3-day trial. No credit card required.</p>
        <button class="gate-cta" type="button" data-account-action="start-trial">Start my 3-day trial</button>
        <button class="gate-secondary" type="button" data-account-action="pricing">See pricing instead</button>
        ${state.accountError ? `<p class="gate-error">${escapeHtml(state.accountError)}</p>` : ''}
      </div>
    </div>`;
}

async function renderGateScreen(container) {
  if (!state.accountStatus) {
    container.innerHTML = `<div class="empty-state">Loading…</div>`;
    await loadAccountStatus();
    render();
    return;
  }
  const status = state.accountStatus;
  container.innerHTML = status.state === 'trial_available' ? trialWelcomeMarkup() : gateScreenMarkup(status);
  wireAccountActions(container);
}

// hasAnyIntegration is the exit condition for the install-pick gate below —
// Winglet does nothing for an account with neither integration enabled, so
// "at least one" (not "both") is what actually unblocks the dashboard.
function hasAnyIntegration(hookHealth) {
  return Boolean(hookHealth?.claudeConfigured || hookHealth?.codexConfigured);
}

// needsInstallPick fires once dashboardAllowed is true (right after
// trialWelcomeMarkup's "Start my 3-day trial", or for anyone already
// subscribed) but before either integration is on — a fresh trial ticking
// away with Winglet wired into neither Claude Code nor Codex would silently
// waste it. state.hookHealth being null (not yet fetched) also reads as
// "needs the gate" so renderInstallPickScreen's loading branch runs first;
// it re-renders once the real value is in, same pattern as renderGateScreen.
function needsInstallPick() {
  return (state.screen === 'overview' || state.screen === 'projects') &&
    Boolean(state.accountStatus?.dashboardAllowed) &&
    !hasAnyIntegration(state.hookHealth);
}

// installPickMarkup reuses hookAgentMarkup verbatim (same cards the
// Installations settings screen shows) so enabling here and enabling there
// can never drift into two different pictures of the same toggle — just
// framed as a required first step instead of a settings row.
function installPickMarkup(hookHealth, hookAllowed) {
  return `
    <div class="gate-screen">
      <div class="gate-card gate-card-wide">
        <div class="gate-brand">${renderBrand()}</div>
        <h1 class="gate-title">Choose what to install Winglet for</h1>
        <p class="gate-subtitle">Enable at least one integration to start seeing savings. You can turn on both, and change this anytime from Installations.</p>
        <div class="hook-agent-list">
          ${hookAgentMarkup({
            name: 'Claude Code Integration',
            configured: hookHealth.claudeConfigured,
            reviewLikely: hookHealth.claudeReviewLikely,
            status: hookHealth.claudeStatus,
            detail: hookHealth.claudeDetail,
            action: hookHealth.claudeAction,
            key: 'claude',
            hookAllowed,
          })}
          ${hookAgentMarkup({
            name: 'Codex Integration',
            configured: hookHealth.codexConfigured,
            reviewLikely: hookHealth.codexReviewLikely,
            status: hookHealth.codexStatus,
            detail: hookHealth.codexDetail,
            action: hookHealth.codexAction,
            key: 'codex',
            hookAllowed,
          })}
        </div>
        ${state.settingsError ? `<p class="gate-error">${escapeHtml(state.settingsError)}</p>` : ''}
      </div>
    </div>`;
}

async function renderInstallPickScreen(container) {
  if (!state.hookHealth) {
    container.innerHTML = `<div class="empty-state">Loading…</div>`;
    try {
      state.hookHealth = await GetHookHealth();
    } catch (err) {
      state.settingsError = err?.message || String(err);
      state.hookHealth = { claudeConfigured: false, codexConfigured: false };
    }
    // Re-enters renderMain from scratch: if the fetch above turned up an
    // already-configured integration, needsInstallPick now reads false and
    // this whole screen is skipped in favor of Overview/Projects.
    render();
    return;
  }
  container.innerHTML = installPickMarkup(state.hookHealth, Boolean(state.accountStatus?.hookAllowed));
  container.querySelectorAll('[data-hook-action]').forEach((btn) => {
    btn.addEventListener('click', () => runInstallPickHookAction(btn));
  });
}

async function runInstallPickHookAction(btn) {
  const agent = btn.getAttribute('data-hook-agent');
  const action = btn.getAttribute('data-hook-action');
  const method = hookActionMethod(agent, action);
  if (!method) return;

  btn.disabled = true;
  btn.textContent = 'Working...';
  state.settingsError = '';
  try {
    await method();
    state.hookHealth = await GetHookHealth();
  } catch (err) {
    state.settingsError = err?.message || String(err);
  }
  // A full render() (not just this screen) so a successful enable falls
  // straight through needsInstallPick into Overview/Projects instead of
  // requiring a second click to "continue".
  render();
}

function hookAgentMarkup(agent) {
  const tone = agent.reviewLikely ? 'needs-action' : agent.configured ? 'ok' : 'missing';
  const primaryAction = agent.configured ? 'disable' : 'enable';
  const primaryLabel = agent.configured ? 'Disable' : 'Enable';
  const disabled = primaryAction === 'enable' && !agent.hookAllowed;
  return `
    <article class="hook-agent ${tone}">
      <div class="hook-agent-main">
        <div>
          <div class="hook-agent-title">${escapeHtml(agent.name)}</div>
          <div class="hook-agent-detail">${escapeHtml(agent.detail)}</div>
        </div>
        <span class="hook-status-badge">${escapeHtml(agent.status)}</span>
      </div>
      ${
        agent.action
          ? `<div class="hook-agent-note">${escapeHtml(agent.action)}</div>`
          : ''
      }
      <div class="hook-agent-actions">
        <button class="hook-action-button ${primaryAction}" type="button" data-hook-agent="${escapeHtml(agent.key)}" data-hook-action="${primaryAction}" ${disabled ? 'disabled' : ''}>
          ${escapeHtml(primaryLabel)}
        </button>
      </div>
    </article>
  `;
}

async function runHookAction(container, btn) {
  const agent = btn.getAttribute('data-hook-agent');
  const action = btn.getAttribute('data-hook-action');
  const method = hookActionMethod(agent, action);
  if (!method) return;

  const original = btn.textContent;
  state.settingsError = '';
  btn.disabled = true;
  btn.textContent = 'Working...';
  stopPolling();
  try {
    await method();
    await renderInstallationsScreen(container);
  } catch (err) {
    state.settingsError = err?.message || String(err);
    btn.disabled = false;
    btn.textContent = original;
    await renderInstallationsScreen(container);
  }
}

function hookActionMethod(agent, action) {
  if (agent === 'claude' && action === 'enable') return () => SetClaudeHookEnabled(true);
  if (agent === 'claude' && action === 'disable') return () => SetClaudeHookEnabled(false);
  if (agent === 'codex' && action === 'enable') return () => SetCodexHookEnabled(true);
  if (agent === 'codex' && action === 'disable') return () => SetCodexHookEnabled(false);
  return null;
}

function escapeHtml(value) {
  return String(value ?? '')
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

function renderMain(container) {
  if (state.screen === 'account') return renderAccountScreen(container);
  if (!state.accountStatus && (state.screen === 'overview' || state.screen === 'projects')) {
    return renderGateScreen(container);
  }
  if (needsAccountGate()) return renderGateScreen(container);
  if (needsInstallPick()) return renderInstallPickScreen(container);
  if (state.screen === 'overview') return renderOverviewScreen(container);
  if (state.screen === 'projects') return renderProjectsScreen(container);
  if (state.screen === 'installations') return renderInstallationsScreen(container);
  if (state.screen === 'preferences') return renderPreferencesScreen(container);
  if (state.screen === 'about') return renderAboutScreen(container);
}

function isSettingsScreen() {
  return SETTINGS_ITEMS.some((item) => item.id === state.screen);
}

function render() {
  const app = document.querySelector('#app');
  app.innerHTML = `
    <div class="sidebar">
      <div class="sidebar-title">${renderBrand()}</div>
      <nav class="nav">
        ${NAV_ITEMS.map(
          (item) => `
          <button class="nav-item ${state.screen === item.id ? 'active' : ''}" data-nav="${item.id}">
            ${item.icon}
            <span>${item.label}</span>
          </button>`
        ).join('')}
        <div class="nav-group ${isSettingsScreen() ? 'open' : ''}">
          <button class="nav-item nav-parent ${isSettingsScreen() ? 'active' : ''}" data-nav="settings" aria-expanded="${isSettingsScreen()}">
            ${icons.settings}
            <span>Settings</span>
            <span class="nav-chevron">&gt;</span>
          </button>
          <div class="nav-subitems">
            ${SETTINGS_ITEMS.map(
              (item) => `
              <button class="nav-subitem ${state.screen === item.id ? 'active' : ''}" data-nav="${item.id}">
                ${item.label}
              </button>`
            ).join('')}
          </div>
        </div>
      </nav>
      <div class="sidebar-footer" id="version-footer">${versionFooterText()}</div>
    </div>
    <div class="main">
      <div id="update-banner"></div>
      <div id="trial-banner"></div>
      <div class="main-scroll" id="main"></div>
    </div>
  `;

  app.querySelectorAll('[data-nav]').forEach((btn) => {
    btn.addEventListener('click', () => navigate(btn.getAttribute('data-nav')));
  });

  renderMain(app.querySelector('#main'));
  renderUpdateBanner();
  renderTrialBanner();
}

// versionFooterText reads CurrentVersion off the same UpdateStatus
// CheckForUpdate() already fetches for the update banner (loadUpdateStatus),
// rather than adding a second Go/JS binding just to say what build this is.
// CurrentVersion is set before any network call in checkForUpdate (see
// update.go), so it's populated even when the GitHub reachability check
// itself fails offline.
function versionFooterText() {
  const version = state.updateStatus?.currentVersion;
  if (!version) return '';
  return version === 'dev' ? 'Winglet · dev build' : `Winglet v${escapeHtml(version)}`;
}

function updateDismissedKey(status) {
  if (!status?.latestVersion) return '';
  return UPDATE_DISMISSED_PREFIX + status.latestVersion;
}

function isUpdateDismissed(status) {
  const key = updateDismissedKey(status);
  if (!key) return false;
  if (state.dismissedUpdateVersions.has(key)) return true;
  try {
    return localStorage.getItem(key) === '1';
  } catch {
    return false;
  }
}

function dismissUpdate(status) {
  const key = updateDismissedKey(status);
  if (!key) return;
  state.dismissedUpdateVersions.add(key);
  try {
    localStorage.setItem(key, '1');
  } catch {
    // A failed persistence write only means this launch can dismiss it.
  }
}

function updateBannerMarkup(status) {
  if (!status?.available || !status.releaseUrl || isUpdateDismissed(status)) return '';
  return `
    <div class="update-banner">
      <span class="update-banner-text">${icons.info}Winglet v${escapeHtml(status.latestVersion)} is available</span>
      <div class="update-banner-actions">
        <button class="update-banner-cta" type="button" data-update-action="open">Update</button>
        <button class="update-banner-dismiss" type="button" data-update-action="dismiss" aria-label="Dismiss update">
          ${icons.x}
        </button>
      </div>
    </div>`;
}

function renderUpdateBanner() {
  const slot = document.querySelector('#update-banner');
  if (!slot) return;
  slot.innerHTML = updateBannerMarkup(state.updateStatus);
  slot.querySelectorAll('[data-update-action]').forEach((btn) => {
    btn.addEventListener('click', () => {
      const action = btn.getAttribute('data-update-action');
      if (action === 'open') {
        OpenUpdateRelease(state.updateStatus.downloadUrl || state.updateStatus.releaseUrl);
      }
      if (action === 'dismiss') {
        dismissUpdate(state.updateStatus);
        renderUpdateBanner();
      }
    });
  });
}

// renderTrialBanner keeps the "you're on a free trial" strip visible above
// every screen (Overview, Projects, every Settings tab — anywhere other than
// the gate/welcome screens, which already make the trial the whole point of
// the page) for as long as state.accountStatus.state === 'trialing'. It
// targets #trial-banner, a sibling of #main set once in render()'s own
// markup — never touched by the per-screen renderOverviewScreen/
// renderProjectsScreen/etc. innerHTML swaps or their 1s polling — so it
// survives screen navigation and poll ticks without being wired into any of
// them individually. TRIAL_BANNER_REFRESH_MS re-renders it periodically so
// the countdown stays honest even while parked on a screen that doesn't poll
// on its own (e.g. Preferences).
function trialBannerMarkup(status) {
  if (!status || status.state !== 'trialing') return '';
  const countdown = formatTrialDaysHours(status.expiresAt);
  const text = countdown ? `Your Winglet free trial ends in ${countdown}` : 'Your Winglet free trial has ended';
  return `
    <div class="trial-banner">
      <span class="trial-banner-text">${icons.sparkle}${text}</span>
      <button class="trial-banner-cta" type="button" data-account-action="pricing">Subscribe</button>
    </div>`;
}

function renderTrialBanner() {
  const slot = document.querySelector('#trial-banner');
  if (!slot) return;
  slot.innerHTML = trialBannerMarkup(state.accountStatus);
  wireAccountActions(slot);
}

const TRIAL_BANNER_REFRESH_MS = 60_000;
const UPDATE_CHECK_INTERVAL_MS = 24 * 60 * 60 * 1000;

initPlatform();
render();
loadUpdateStatus();
setInterval(renderTrialBanner, TRIAL_BANNER_REFRESH_MS);
setInterval(loadUpdateStatus, UPDATE_CHECK_INTERVAL_MS);
