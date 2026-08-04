import './style.css';
import { icons } from './icons.js';
import { GetOverview, GetProjects, GetSessionStats, GetSettings, SetQuiet } from '../wailsjs/go/main/App';

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

function navigate(screen) {
  stopPolling();
  state.screen = screen;
  render();
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

// hero renders the primary figure: the headline percent-saved figure (e.g.
// "38% saved"), a one-line restatement of that same figure as extra runway
// ("→ ~61% more usage with the same plan") — the actual claim agent-winglet
// makes — and, once real transcript data exists to size it against, a bytes
// bar showing suppressed bytes against the real total (transcript content
// bytes + suppressed) — the same total the headline percent itself is
// computed from, so the fill width and the number always agree.
function hero(overview) {
  // hasTranscriptData gates the bytes bar (it needs a real total, which only
  // exists once the transcript's been read at SessionEnd) but must not gate
  // the usage-detail line too — hasActivity alone is enough to explain why
  // the number above it isn't a percent yet, and that explanation is exactly
  // what an in-progress session most needs to show instead of going silent.
  const showUsageLine = overview.hasTranscriptData || overview.hasActivity;
  return `
    <div class="hero">
      <div class="hero-number">${overview.heroHeadline}</div>
      ${showUsageLine ? `<div class="hero-usage">${overview.hasTranscriptData ? '→ ' : ''}${overview.heroUsageDetail} ${overview.heroUsageSub}</div>` : ''}
      ${overview.hasTranscriptData ? heroBar(overview) : ''}
    </div>`;
}

function heroBar(overview) {
  return `
    <div class="hero-bar">
      <div class="hero-bar-track">
        <div class="hero-bar-fill" style="width: ${Math.min(100, overview.heroPercent).toFixed(1)}%"></div>
      </div>
      <div class="hero-bar-labels">
        <span>${overview.bytesSavedCard.detail} saved</span>
        <span>${overview.heroTotalBytesLabel} total</span>
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
          <div class="card-label">${c.label}</div>
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

async function loadOverview(container) {
  const o = await GetOverview();
  // Guard against a poll tick landing after the user has already navigated
  // away — GetOverview is async, so a stale response could otherwise clobber
  // whatever screen is now showing.
  if (state.screen !== 'overview') return;
  withPreservedScroll(container, () => {
    container.innerHTML = `
      <h1 class="screen-title">Overview</h1>
      <p class="screen-subtitle">Lifetime, across ${o.projectCount} project${o.projectCount === 1 ? '' : 's'} and ${o.sessionCount} session${o.sessionCount === 1 ? '' : 's'}.</p>
      ${statsBlock(o)}
    `;
  });
}

async function renderOverviewScreen(container) {
  container.innerHTML = `<div class="empty-state">Loading…</div>`;
  await loadOverview(container);
  startPolling(() => loadOverview(container));
}

function statusPill(installed) {
  if (installed) {
    return `<span class="status-pill active">${icons.checkCircle} Installed</span>`;
  }
  return `<span class="status-pill stale">${icons.circle} Not wired in</span>`;
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
    withPreservedScroll(container, () => {
      container.innerHTML = `
        <h1 class="screen-title">Projects</h1>
        <p class="screen-subtitle">Projects with the agent-winglet hook installed.</p>
        <div class="empty-state">No projects registered yet — the hook installs globally; a project is added automatically the first time Claude Code starts a session in it.</div>
      `;
    });
    return;
  }

  withPreservedScroll(container, () => {
    container.innerHTML = `
      <h1 class="screen-title">Projects</h1>
      <p class="screen-subtitle">${rows.length} registered project${rows.length === 1 ? '' : 's'}.</p>
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
          <span class="project-hero-inline">${row.overview.heroHeadline}</span>
          ${statusPill(row.installed)}
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

  for (const row of rows) {
    if (state.expanded.has(row.path)) {
      renderSessionsSection(container, row.path);
    }
  }
}

async function renderProjectsScreen(container) {
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
            <span class="project-hero-inline">${row.overview.heroHeadline}</span>
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

  withPreservedScroll(container, () => {
    target.innerHTML = sessionsSectionMarkup(projectPath, sessions);
    wireSessionToggles(container, target, projectPath);
  });
}

async function renderSettingsScreen(container) {
  container.innerHTML = `<div class="empty-state">Loading…</div>`;
  const settings = await GetSettings();

  container.innerHTML = `
    <h1 class="screen-title">Settings</h1>
    <p class="screen-subtitle">Only the levers that actually have a wired backend today.</p>
    <div class="settings-row">
      <div>
        <div class="settings-row-label">Quiet mode</div>
        <div class="settings-row-desc">Suppresses the end-of-session savings receipt message. Every mechanism (dedup, budgeting, retirement, the compact nudge) keeps running either way.</div>
      </div>
      <button class="toggle ${settings.quiet ? 'on' : ''}" id="quiet-toggle" aria-pressed="${settings.quiet}">
        <span class="toggle-knob"></span>
      </button>
    </div>
    <p class="settings-note">This writes ~/.agent-winglet/config.json. AGENT_WINGLET_QUIET, if set in a terminal session's environment, still takes precedence over this toggle for that session.</p>
  `;

  container.querySelector('#quiet-toggle').addEventListener('click', async (e) => {
    const btn = e.currentTarget;
    const next = !btn.classList.contains('on');
    btn.classList.toggle('on', next);
    btn.setAttribute('aria-pressed', String(next));
    await SetQuiet(next);
  });
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
      <div class="sidebar-title">Agent Winglet</div>
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

render();
