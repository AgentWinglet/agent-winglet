import './style.css';
import { icons } from './icons.js';
import { GetOverview, GetOverviewWindow, GetProjects, GetSessionStats, GetSettings, SetQuiet } from '../wailsjs/go/main/App';

const WINDOW_DAYS = 7;

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

function navigate(screen) {
  state.screen = screen;
  render();
}

function hero(overview, compact = false) {
  return `
    <div class="hero ${compact ? 'compact' : ''}">
      <div class="hero-number">${overview.heroHeadline}</div>
      <div class="hero-label">${overview.heroSubtext}</div>
    </div>`;
}

function windowBlock(windowOverview) {
  const n = windowOverview.sessionCount;
  return `
    <div class="subsection-title">Last ${WINDOW_DAYS} days (${n} session${n === 1 ? '' : 's'})</div>
    ${hero(windowOverview, true)}
    ${cardGrid(windowOverview)}`;
}

function cardGrid(overview) {
  const cards = [overview.dedup, overview.budgetTrims, overview.retired];
  return `
    <div class="card-grid">
      ${cards
        .map(
          (c) => `
        <div class="card">
          <div class="card-label">${c.label}</div>
          <div class="card-count">${c.count}</div>
          <div class="card-detail">${c.detail}</div>
        </div>`
        )
        .join('')}
    </div>`;
}

async function renderOverviewScreen(container) {
  container.innerHTML = `<div class="empty-state">Loading…</div>`;
  const [o, w] = await Promise.all([GetOverview(), GetOverviewWindow()]);
  container.innerHTML = `
    <h1 class="screen-title">Overview</h1>
    <p class="screen-subtitle">Lifetime, across ${o.projectCount} project${o.projectCount === 1 ? '' : 's'} and ${o.sessionCount} session${o.sessionCount === 1 ? '' : 's'}.</p>
    ${hero(o)}
    ${cardGrid(o)}
    ${windowBlock(w)}
  `;
}

function statusPill(installed) {
  if (installed) {
    return `<span class="status-pill active">${icons.checkCircle} Installed</span>`;
  }
  return `<span class="status-pill stale">${icons.circle} Not wired in</span>`;
}

async function renderProjectsScreen(container) {
  container.innerHTML = `<div class="empty-state">Loading…</div>`;
  const rows = await GetProjects();

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
        ${cardGrid(row.overview)}
        ${windowBlock(row.window)}
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
      renderProjectsScreen(container);
    });
  });

  for (const row of rows) {
    if (state.expanded.has(row.path)) {
      renderSessionsSection(container, row.path);
    }
  }
}

async function renderSessionsSection(container, projectPath) {
  const target = container.querySelector(`[data-sessions="${CSS.escape(projectPath)}"]`);
  if (!target) return;

  if (!state.sessionsByProject.has(projectPath)) {
    target.innerHTML = `<div class="sessions-loading">Loading sessions…</div>`;
    state.sessionsByProject.set(projectPath, await GetSessionStats(projectPath));
  }
  const sessions = state.sessionsByProject.get(projectPath);

  if (!sessions || sessions.length === 0) {
    target.innerHTML = `<div class="sessions-empty">No completed sessions on disk yet for this project.</div>`;
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
          <div class="session-row-detail">${cardGrid(row.overview)}</div>
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
