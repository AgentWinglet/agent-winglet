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

// hero renders the primary figure: the headline percent-saved figure (e.g.
// "38% saved"), a one-line restatement of that same figure as extra runway
// ("→ ~61% more usage with the same plan") — the actual claim agent-winglet
// makes — and, once real transcript data exists to size it against, a bytes
// bar showing suppressed bytes against the real total (transcript content
// bytes + suppressed) — the same total the headline percent itself is
// computed from, so the fill width and the number always agree.
function hero(overview) {
  return `
    <div class="hero">
      <div class="hero-number">${overview.heroHeadline}</div>
      ${overview.hasTranscriptData ? `<div class="hero-usage">→ ${overview.heroUsageDetail} ${overview.heroUsageSub}</div>` : ''}
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

// statsBlock is the full hierarchy shared by the Overview screen and each
// Projects-screen row: hero (raw suppressed bytes) -> summary cards (bytes/
// tokens/$) -> per-mechanism bars. One function, two call sites, so the
// layout can never drift between the two.
function statsBlock(overview) {
  return `
    ${hero(overview)}
    ${cardRow(overview)}
    ${barList(overview)}`;
}

// lightweightCardGrid is the compact view used only for per-session rows
// nested inside an expanded project — just the three cards, no hero/bars/
// tooltips stacked three levels deep.
function lightweightCardGrid(overview) {
  return cardRow(overview);
}

async function loadOverview(container) {
  const o = await GetOverview();
  // Guard against a poll tick landing after the user has already navigated
  // away — GetOverview is async, so a stale response could otherwise clobber
  // whatever screen is now showing.
  if (state.screen !== 'overview') return;
  container.innerHTML = `
    <h1 class="screen-title">Overview</h1>
    <p class="screen-subtitle">Lifetime, across ${o.projectCount} project${o.projectCount === 1 ? '' : 's'} and ${o.sessionCount} session${o.sessionCount === 1 ? '' : 's'}.</p>
    ${statsBlock(o)}
  `;
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
async function loadProjects(container) {
  const rows = await GetProjects();
  if (state.screen !== 'projects') return;

  if (!rows || rows.length === 0) {
    container.innerHTML = `
      <h1 class="screen-title">Projects</h1>
      <p class="screen-subtitle">Projects with the agent-winglet hook installed.</p>
      <div class="empty-state">No projects registered yet — the hook installs globally; a project is added automatically the first time Claude Code starts a session in it.</div>
    `;
    return;
  }

  container.innerHTML = `
    <h1 class="screen-title">Projects</h1>
    <p class="screen-subtitle">${rows.length} registered project${rows.length === 1 ? '' : 's'}.</p>
    <div id="project-list"></div>
  `;

  const list = container.querySelector('#project-list');
  list.innerHTML = rows
    .map(
      (row) => `
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
        <div class="sessions-section" data-sessions="${row.path}"></div>
      </div>
    </div>`
    )
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

// renderSessionsSection always fetches fresh session data (per-session stats
// files are written live, on every dedup hit/trim/retire — see
// internal/stats — so an in-progress session's numbers grow between polls
// too, not just once it ends). The "Loading…" placeholder only shows the
// first time a project is expanded — a poll tick refreshing an
// already-populated section swaps its content in place instead of flashing
// back to a loading state.
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

  if (!sessions || sessions.length === 0) {
    target.innerHTML = `<div class="sessions-empty">No sessions on disk yet for this project.</div>`;
    return;
  }

  target.innerHTML = `
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
          <div class="session-row-detail">${lightweightCardGrid(row.overview)}</div>
        </div>`;
      })
      .join('')}
  `;

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
