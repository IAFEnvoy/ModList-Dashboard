// core.js — state, constants, helpers, init
const state = {
  mods: [],
  statusOrder: [],
  statusArchived: [],
  publicConfig: {},
  allGitHubItems: { issues: [], pulls: [] },
  currentPage: 'mods',
  currentView: 'list',
  currentGhTab: 'issues',
  currentGhFilter: 'open',
  detailGhFilter: 'all',
  currentSort: 'downloads_desc',
  currentAgentTab: 'chat',
  loggedIn: false,
  currentUser: '',
};

// ========== Hash 路由持久化 ==========

function saveHash() {
  var hash = state.currentPage;
  if (state.currentPage === 'mods' && state.currentView === 'scatter') hash = 'mods-scatter';
  else if (state.currentPage === 'agents') hash = 'agents-' + (state.currentAgentTab || 'chat');
  location.replace('#' + hash);
}

function restoreFromHash() {
  var hash = (location.hash || '#mods').replace('#', '');
  if (hash === 'github') { state.currentPage = 'github'; }
  else if (hash === 'issues') { state.currentPage = 'issues'; }
  else if (hash === 'todos') { state.currentPage = 'todos'; }
  else if (hash === 'mods-scatter') { state.currentPage = 'mods'; state.currentView = 'scatter'; }
  else if (hash.indexOf('agents-') === 0) { state.currentPage = 'agents'; state.currentAgentTab = hash.slice(7); }
  else if (hash === 'drive') { state.currentPage = 'drive'; }
  else { state.currentPage = 'mods'; state.currentView = 'list'; } // default/legacy
}

const STATUS_DEFAULT = '#9ca3af';

// 状态颜色与版本支持图标统一取自 /api/config（即 config/configuration.json），
// 与编辑器等页面共享同一份配置，避免多处硬编码。
function statusColorMap() {
  return state.publicConfig.status_colors || {};
}
function isStatusValue(status) {
  return status ? Object.prototype.hasOwnProperty.call(statusColorMap(), status) : false;
}
function getStatusColor(status) {
  return statusColorMap()[status] || STATUS_DEFAULT;
}
function versionOptions() {
  return state.publicConfig.version_options || [];
}
function getVersionIcon(value) {
  if (!value) return '';
  const option = versionOptions().find(o => o.value === value);
  return option ? option.emoji : '';
}

// 版本支持状态 → i18n 文案键（与配置 icons.versions 的值对应）
const versionLegendKeys = { All: 'fullySupported', Partly: 'partlySupported', Planned: 'planned', BugFix: 'bugFixOnly', Ended: 'ended', None: 'none' };

// 先走 t() 本地化；未配置翻译或配置值不在标准键内时回退到配置映射文本 option.text
function versionStatusLabel(value) {
  if (!value) return '';
  const option = versionOptions().find(o => o.value === value);
  const labelKey = versionLegendKeys[value];
  if (labelKey) {
    const localized = t(labelKey);
    if (localized && localized !== labelKey) return localized;
  }
  return option ? (option.text || option.value) : value;
}

const $ = (s) => document.querySelector(s);
const $$ = (s) => document.querySelectorAll(s);

function esc(s) { const d = document.createElement('div'); d.textContent = s || ''; return d.innerHTML; }

function renderMarkdownSafe(source) {
  var text = source == null ? '' : String(source);
  if (typeof marked === 'undefined' || typeof DOMPurify === 'undefined') return esc(text);
  return DOMPurify.sanitize(marked.parse(text), { USE_PROFILES: { html: true } });
}

function renderHighlightSafe(source) {
  var text = source == null ? '' : String(source);
  if (typeof DOMPurify === 'undefined') return esc(text);
  return DOMPurify.sanitize(text, { ALLOWED_TAGS: ['mark'], ALLOWED_ATTR: [] });
}

function formatNum(n) {
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
  return String(n);
}

function statusIndex(s) {
  const order = state.statusOrder || [];
  const i = order.indexOf(s);
  return i >= 0 ? i : 999;
}

function ghCount(mod) {
  if (!mod.github) return 0;
  return (mod.github.open_issues || 0) + (mod.github.open_prs || 0);
}

function getLogo(mod) { return mod && mod.logo ? '/api/logo/' + encodeURIComponent(mod.id) : ''; }

function getStatusClass(status) {
  switch (status) {
    case 'Rewrite Queued': return 'rewrite';
    case 'End Support': return 'end';
    case 'Developing': return 'dev';
    case 'Low Priority': return 'low';
    case 'Not Release Yet': return 'notrelease';
    default: return '';
  }
}

function renderStatusTag(status) {
  if (!status) return '';
  const key = 'status_' + status.replace(/\s+/g, '');
  return `<span class="mod-tag tag-status ${getStatusClass(status)}">${esc(t(key))}</span>`;
}

// getGhStateIcon 根据条目状态和类型返回对应的状态图标
function getGhStateIcon(item, type) {
  var state = item.state || '';
  var stateReason = item.state_reason || '';
  var isPR = type === 'pr' || type === 'pulls';
  if (isPR) {
    if (item.merged) return 'img/pr-merged.svg';
    if (item.draft && state === 'open') return 'img/pr-draft.svg';
    if (state === 'open') return 'img/pr-open.svg';
    return 'img/pr-closed.svg';
  }
  // Issues: 根据 state_reason 细分
  if (state === 'closed') {
    if (stateReason === 'completed') return 'img/issue_completed.svg';
    return 'img/issue_closed.svg'; // not_planned 或 fallback
  }
  return 'img/issue_open.svg';
}

function getGhStateCls(item) {
  if (item.merged) return 'merged';
  if (item.draft && item.state === 'open') return 'draft';
  var stateReason = item.state_reason || '';
  if (item.state === 'closed' && stateReason === 'completed') return 'completed';
  if (item.state === 'closed' && stateReason === 'not_planned') return 'not-planned';
  return item.state === 'open' ? 'open' : 'closed';
}

function getGhStateLabel(item) {
  if (item.merged) return 'merged';
  if (item.draft && item.state === 'open') return 'draft';
  var stateReason = item.state_reason || '';
  if (item.state === 'closed' && stateReason === 'completed') return 'completed';
  if (item.state === 'closed' && stateReason === 'not_planned') return 'not planned';
  return item.state;
}

function renderGhItemList(items, filter, repoPath, ghType) {
  let filtered = items;
  if (filter === 'open') filtered = items.filter(i => i.state === 'open');
  else if (filter === 'closed') filtered = items.filter(i => i.state !== 'open');
  if (filtered.length === 0) return '<div style="color:#9ca3af;padding:4px 0;">' + t('noMatch') + '</div>';
  var rp = repoPath || '';
  var parts = rp ? rp.split('/') : [];
  var owner = parts[0] || '', repo = parts[1] || '';
  var loggedIn = (typeof state !== 'undefined' && state.loggedIn);
  var type = ghType || 'issue';
  return filtered.map(item => `
    <div class="gh-item">
      <img src="${getGhStateIcon(item, type)}" class="stat-icon gh-state-icon" title="${getGhStateLabel(item)}">
      <span class="gh-repo">#${item.number}</span>
      <a href="${item.url}" target="_blank">${esc(item.title)}</a>
      <span class="gh-meta">
      ${(item.labels || []).map(l => `<span class="gh-label">${esc(l)}</span>`).join('')}
      <span class="gh-user">${esc(item.user || '')}</span>
      ${item.created_at ? `<span class="gh-date">${item.created_at.slice(0, 10)}</span>` : ''}
      ${(rp && loggedIn) ? ' <button class="gh-dispatch-btn" data-gh-type="' + esc(type) + '" data-gh-owner="' + esc(owner) + '" data-gh-repo="' + esc(repo) + '" data-gh-number="' + item.number + '" data-gh-title="' + esc(item.title) + '" title="派发到智能体">🚀</button>' : ''}
      </span>
    </div>
  `).join('');
}

function buildMainVersion(versions) {
  const ignored = ['', 'Planned', 'None'];
  const avail = Object.keys(versions || {}).filter(vc =>
    Object.values(versions[vc] || {}).some(s => !ignored.includes(s))
  ).sort();
  if (avail.length === 0) return '???';
  if (avail.length === 1) return avail[0];
  if (avail.length === 2) return avail[0] + ', ' + avail[1];
  const first = avail[0].includes('~') ? avail[0].split('~')[0] : avail[0];
  const last = avail[avail.length - 1].includes('~') ? avail[avail.length - 1].split('~')[1] : avail[avail.length - 1];
  return first + '~' + last;
}

function buildVersionTag(mod) {
  if (!mod.versions) return '';
  return `<span class="mod-tag tag-version" title="${buildMainVersion(mod.versions)}">${buildMainVersion(mod.versions)}</span>`;
}

function buildVersionTable(versions) {
  if (!versions) return '<div style="color:#9ca3af;padding:8px;">' + t('noVersion') + '</div>';
  const loaders = [], loaderSet = {};
  Object.values(versions).forEach(lm => Object.keys(lm).forEach(l => { if (!loaderSet[l]) { loaderSet[l] = true; loaders.push(l); } }));
  const versionKeys = Object.keys(versions).sort().reverse();
  const usedStatus = new Set();
  versionKeys.forEach(vc => Object.values(versions[vc] || {}).forEach(s => usedStatus.add(s)));
  let html = '<div class="version-legend">';
  versionOptions().forEach(option => {
    if (!usedStatus.has(option.value)) return;
    html += '<span>' + option.emoji + ' ' + esc(versionStatusLabel(option.value)) + '</span>';
  });
  html += '</div><table class="version-table"><thead><tr><th>' + t('mcVersion') + '</th>';
  loaders.forEach(l => { html += `<th>${esc(l)}</th>`; });
  html += '</tr></thead><tbody>';
  versionKeys.forEach(vc => {
    html += `<tr><td>${vc}</td>`;
    loaders.forEach(l => { const s = (versions[vc] || {})[l] || ''; html += `<td title="${esc(versionStatusLabel(s))}">${getVersionIcon(s)}</td>`; });
    html += '</tr>';
  });
  html += '</tbody></table>';
  return html;
}

function getAuthHeaders() {
  var token = localStorage.getItem('modlist_token');
  return token ? { 'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json' } : { 'Content-Type': 'application/json' };
}

function setupSidebarToggle() {
  var btn = document.getElementById('sidebarToggle');
  var sidebar = document.getElementById('sidebar');
  var backdrop = document.getElementById('sidebarBackdrop');
  if (!btn || !sidebar) return;

  // 窄屏默认折叠——由 HTML 中的 collapsed class 保证初始无闪烁
  var OVERLAY_BREAKPOINT = 900;
  var narrow = window.innerWidth <= OVERLAY_BREAKPOINT;
  if (narrow) {
    sidebar.classList.add('collapsed');
  } else {
    // 宽屏但 HTML 默认 collapsed，需要在这里展开
    sidebar.classList.remove('collapsed');
  }

  function openSidebar() {
    sidebar.classList.remove('collapsed');
    if (backdrop && window.innerWidth <= OVERLAY_BREAKPOINT) {
      backdrop.classList.add('show');
    }
  }

  function closeSidebar() {
    sidebar.classList.add('collapsed');
    if (backdrop) backdrop.classList.remove('show');
  }

  btn.addEventListener('click', function () {
    if (sidebar.classList.contains('collapsed')) {
      openSidebar();
    } else {
      closeSidebar();
    }
  });

  if (backdrop) {
    backdrop.addEventListener('click', function () {
      closeSidebar();
    });
  }

  // 窗口 resize 时自动适配
  var resizeTimer;
  window.addEventListener('resize', function () {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(function () {
      var nowNarrow = window.innerWidth <= OVERLAY_BREAKPOINT;
      if (nowNarrow) {
        // 进入窄屏，折叠
        sidebar.classList.add('collapsed');
        if (backdrop) backdrop.classList.remove('show');
      } else {
        // 进入宽屏，展开
        sidebar.classList.remove('collapsed');
        if (backdrop) backdrop.classList.remove('show');
      }
    }, 150);
  });
}

// ========== init ==========
document.addEventListener('DOMContentLoaded', async () => {
  setupViewToggle();
  setupFilters();
  setupRefreshButtons();
  setupDetailClose();
  setupGitHubTabs();
  setupScatterToggle();
  setupStatsBar();
  setupSidebarToggle();
  setupLangSwitcher();
  setupLicenseDropdown();
  setupAdminDropdown();
  await loadLocales();
  applyI18n();
  setupLogin();
  fetchData();
  fetchTodoCounts();
  fetchAgentInfo();
});

// 浏览器前进/后退时恢复页面状态
window.addEventListener('hashchange', function () {
  var oldPage = state.currentPage, oldView = state.currentView, oldAgentTab = state.currentAgentTab;
  restoreFromHash();
  if (state.currentPage !== oldPage || state.currentView !== oldView || state.currentAgentTab !== oldAgentTab) {
    switchPage(state.currentPage);
    if (state.currentPage === 'agents') {
      switchAgentTab(state.currentAgentTab || 'chat');
    }
    if (state.currentPage === 'mods' && state.currentView === 'scatter') {
      $$('.mods-tab').forEach(function (b) { b.classList.toggle('active', b.dataset.view === 'scatter'); });
      $('#listView').style.display = 'none';
      $('#scatterView').style.display = 'block';
      renderScatter();
    }
    if (state.currentPage === 'mods' && state.currentView === 'list') {
      $$('.mods-tab').forEach(function (b) { b.classList.toggle('active', b.dataset.view === 'list'); });
      $('#listView').style.display = 'block';
      $('#scatterView').style.display = 'none';
    }
  }
});
