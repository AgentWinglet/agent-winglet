import './style.css';
import { icons } from './icons.js';
import { GetOverview, GetPlatform, GetProjects, GetSessionStats, QuitApp } from '../wailsjs/go/main/App';

const state = {
  screen: 'overview',
  expanded: new Set(),
  expandedSessions: new Set(),
  sessionsByProject: new Map(),
};

const NAV_ITEMS = [
  { id: 'overview', label: 'Overview', icon: icons.overview },
  { id: 'projects', label: 'Projects', icon: icons.projects },
  { id: 'settings', label: 'Settings', icon: icons.settings },
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
  state.screen = screen;
  render();
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
// of that same figure as extra runway ("→ ~61% more usage with the same
// plan") — the actual claim agent-winglet makes.
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
          <div class="card-detail">${c.detail}</div>
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
// function, three call sites, so the "X% saved / X% more usage / bar"
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
// extra usage runway — the one line that has to justify expanding the row,
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
          <div class="empty-state">No projects registered yet — the hook installs globally; a project is added automatically the first time Claude Code starts a session in it.</div>
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
            <span class="project-row-spacer"></span>
            ${heroInline(row.overview)}
          </button>
          <div class="session-row-detail">${statsBlock(row.overview)}</div>
        </div>`;
      })
      .join('')}
  `;
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

// renderSettingsScreen's Quit row is the dashboard's own "quit everything"
// affordance (App.QuitApp) — closing the window itself (titlebar button,
// Cmd+Q/Alt+F4, Dock Quit) already exits the dashboard for real, but leaves
// a running tray helper alone so its "Open Winglet" can relaunch the
// dashboard later. This additionally tears down that tray, so "quit" here
// means the same thing it does from the tray's own menu.
function renderSettingsScreen(container) {
  container.innerHTML = `
    <h1 class="screen-title">Settings</h1>
    <div class="settings-row">
      <div>
        <div class="settings-row-title">Quit Winglet</div>
        <div class="settings-row-detail">Closes the dashboard and the menu-bar icon together.</div>
      </div>
      <button class="quit-button" data-quit>Quit Winglet</button>
    </div>
  `;
  container.querySelector('[data-quit]').addEventListener('click', () => QuitApp());
}

function renderMain(container) {
  if (state.screen === 'overview') return renderOverviewScreen(container);
  if (state.screen === 'projects') return renderProjectsScreen(container);
  if (state.screen === 'settings') return renderSettingsScreen(container);
}

function render() {
  const app = document.querySelector('#app');
  app.innerHTML = `
    <div class="sidebar">
      <div class="sidebar-title">Winglet</div>
      <nav class="nav">
        ${NAV_ITEMS.map(
          (item) => `
          <button class="nav-item ${state.screen === item.id ? 'active' : ''}" data-nav="${item.id}">
            ${item.icon}
            <span>${item.label}</span>
          </button>`
        ).join('')}
      </nav>
    </div>
    <div class="main" id="main"></div>
  `;

  app.querySelectorAll('[data-nav]').forEach((btn) => {
    btn.addEventListener('click', () => navigate(btn.getAttribute('data-nav')));
  });

  renderMain(app.querySelector('#main'));
}

initPlatform();
render();
