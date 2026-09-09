// list.js — mod list rendering + filters + page switching
function setupViewToggle() {
  $$('.mods-tab').forEach(btn => {
    btn.addEventListener('click', () => {
      $$('.mods-tab').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      state.currentView = btn.dataset.view;
      $('#listView').style.display = state.currentView === 'list' ? 'block' : 'none';
      $('#scatterView').style.display = state.currentView === 'scatter' ? 'block' : 'none';
      if (state.currentView === 'scatter') renderScatter();
      saveHash();
    });
  });
}

function setupStatsBar() {
  $$('.stat-card').forEach(card => {
    card.addEventListener('click', () => {
      const page = card.dataset.page;
      if (page === state.currentPage) return;
      state.currentPage = page;
      switchPage(page);
      saveHash();
    });
  });
}

function switchPage(page) {
  const showAgents = page === 'agents';
  const showDrive = page === 'drive';
  // 未登录拒绝进入 agents / drive
  if ((showAgents || showDrive) && !state.loggedIn) {
    alert(t('loginRequired'));
    return;
  }
  state.currentPage = page;
  $$('.stat-card').forEach(el => { el.classList.toggle('active', el.dataset.page === page); });
  // 窄屏模式下切换页面后自动折叠侧边栏
  if (window.innerWidth <= 900) {
    var sidebar = document.getElementById('sidebar');
    var backdrop = document.getElementById('sidebarBackdrop');
    if (sidebar) sidebar.classList.add('collapsed');
    if (backdrop) backdrop.classList.remove('show');
  }
  const showMods = page === 'mods';
  const showTodos = page === 'todos';
  $('#modsSection').style.display = showMods ? '' : 'none';
  $('#githubPanel').style.display = (showMods || showTodos || showAgents || showDrive) ? 'none' : '';
  $('#todosSection').style.display = showTodos ? '' : 'none';
  $('#agentsSection').style.display = showAgents ? '' : 'none';
  $('#driveSection').style.display = showDrive ? '' : 'none';
  // 切换标签时处理
  if (!showAgents) {
    currentChatSessionId = null;
  }
  if (showMods) { renderList(); if (state.currentView === 'scatter') renderScatter(); }
  else if (showTodos) { todoBugPage = 1; todoTodoPage = 1; renderTodosSection(); }
  else if (showAgents) {
    // 进入 Agents 页时先刷数据，面板由后续操作（openChatWindow/switchAgentTab）控制
    fetchAgentInfo().then(function () {
      fetchAgentSessions().then(function () {
        if (typeof updateChatHistory === 'function') updateChatHistory();
      });
    });
  }
  else if (showDrive) {
    _driveCurrentPath = '';
    if (typeof driveLoad === 'function') { driveLoad('/'); }
  }
  else { renderGitHubPanel(); }
}

function setupFilters() {
  $('#searchInput').addEventListener('input', renderList);
  $('#statusFilter').addEventListener('change', renderList);
  $('#sortBy').addEventListener('change', () => { state.currentSort = $('#sortBy').value; renderList(); });
}

function populateFilters() {
  const statusSel = $('#statusFilter');
  const current = statusSel.value;
  [...statusSel.options].forEach(opt => { if (opt.value !== 'all') opt.remove(); });
  const statuses = [...new Set(state.mods.map(m => m.status).filter(Boolean))];
  statuses.forEach(s => { const o = document.createElement('option'); o.value = s; o.textContent = s; statusSel.appendChild(o); });
  statusSel.value = statuses.includes(current) ? current : 'all';
}

function renderList() {
  const searchTerm = $('#searchInput').value.toLowerCase().trim();
  const statusFilter = $('#statusFilter').value;
  let filtered = state.mods;
  if (searchTerm) filtered = filtered.filter(m => m.name.toLowerCase().includes(searchTerm) || m.description?.toLowerCase().includes(searchTerm) || m.id?.toLowerCase().includes(searchTerm) || (m.tags || []).some(t => t.toLowerCase().includes(searchTerm)));
  if (statusFilter !== 'all') filtered = filtered.filter(m => m.status === statusFilter);

  const sortBy = state.currentSort || 'downloads_desc';
  if (sortBy === 'name_asc') filtered = [...filtered].sort((a, b) => (a.name || '').localeCompare(b.name || ''));
  else if (sortBy === 'name_desc') filtered = [...filtered].sort((a, b) => (b.name || '').localeCompare(a.name || ''));
  else if (sortBy === 'downloads_desc') filtered = [...filtered].sort((a, b) => (b.total_downloads || 0) - (a.total_downloads || 0));
  else if (sortBy === 'downloads_asc') filtered = [...filtered].sort((a, b) => (a.total_downloads || 0) - (b.total_downloads || 0));
  else if (sortBy === 'status_asc') filtered = [...filtered].sort((a, b) => statusIndex(a.status) - statusIndex(b.status));
  else if (sortBy === 'status_desc') filtered = [...filtered].sort((a, b) => statusIndex(b.status) - statusIndex(a.status));
  else if (sortBy === 'issues_desc') filtered = [...filtered].sort((a, b) => ghCount(b) - ghCount(a));
  else if (sortBy === 'issues_asc') filtered = [...filtered].sort((a, b) => ghCount(a) - ghCount(b));

  $('#modList').innerHTML = filtered.map(mod => `
    <div class="mod-card" data-mod-id="${mod.id}" onclick="openDetail('${mod.id}')">
      <div class="mod-card-header">
        <div class="mod-card-logo-wrap">
          <img class="mod-card-logo" src="${getLogo(mod)}" alt="" loading="lazy"
            onerror="this.style.display='none';this.parentElement.querySelector('.mod-logo-fallback').style.display='flex'"
            onload="this.style.display='';this.parentElement.querySelector('.mod-logo-fallback').style.display='none'">
          <span class="mod-logo-fallback">${esc(mod.name?.[0] || '?')}</span>
        </div><span class="mod-card-name">${esc(mod.name)}</span>
      </div>
      <div class="mod-card-desc">${esc(mod.description || '')}</div>
      <div class="mod-card-tags">
        ${buildVersionTag(mod)}${renderStatusTag(mod.status)}
        <span class="mod-tag tag-down">${t('difficulty')}: ${mod.difficulty}</span>
        ${(mod.tags || []).map(t => `<span class="mod-tag tag-down">${esc(t)}</span>`).join('')}
      </div>
      <div class="mod-card-stats">
        <span><img src="img/curseforge.webp" class="stat-icon"> ${formatNum(mod.cf_downloads || 0)}</span>
        ${mod.github ? `<span><img src="img/issue.svg" class="stat-icon"> ${mod.github.open_issues}</span><span><img src="img/pr.svg" class="stat-icon"> ${mod.github.open_prs}</span>` : ''}
      </div>
    </div>`).join('');
  $('#modList').querySelectorAll('.mod-card').forEach(layoutModCard);
}

// ===== 卡片两行截断：介绍最多 2 行、标签最多 2 行（行末省略号，悬停显示完整内容）=====

function layoutModCard(card) {
  const desc = card.querySelector('.mod-card-desc');
  if (desc) {
    if (desc.scrollHeight > desc.clientHeight + 1) desc.setAttribute('data-clamped', '1');
    else desc.removeAttribute('data-clamped');
  }
  const tags = card.querySelector('.mod-card-tags');
  if (tags) layoutCardTags(tags);
}

function layoutCardTags(tags) {
  // 先恢复所有标签，便于窗口缩放 / 语言切换后重新测量
  tags.style.height = '';
  tags.querySelectorAll('.mod-tag-more').forEach(node => node.remove());
  const chips = Array.from(tags.querySelectorAll('.mod-tag'));
  chips.forEach(node => node.classList.remove('mod-tag-hidden'));
  if (!chips.length) {
    tags.removeAttribute('data-clamped');
    return;
  }

  const rowGap = parseFloat(getComputedStyle(tags).rowGap) || 6;
  const chipTop = node => Math.round(node.getBoundingClientRect().top - tags.getBoundingClientRect().top);
  const distinctRows = nodes => [...new Set(nodes.map(chipTop))].sort((a, b) => a - b);

  const rows = distinctRows(chips);
  if (rows.length > 2) {
    // 隐藏第 3 行及之后的标签，末尾补一个省略号
    const thirdRowTop = rows[2];
    chips.forEach(node => { if (chipTop(node) >= thirdRowTop) node.classList.add('mod-tag-hidden'); });
    const more = document.createElement('span');
    more.className = 'mod-tag tag-down mod-tag-more';
    more.textContent = '…';
    tags.appendChild(more);

    // 第二行放不下省略号时，从末尾逐颗腾位
    let guard = chips.length + 2;
    while (guard-- > 0) {
      const current = Array.from(tags.querySelectorAll('.mod-tag')).filter(node => !node.classList.contains('mod-tag-hidden'));
      if (distinctRows(current).length <= 2) break;
      const visible = Array.from(tags.querySelectorAll('.mod-tag:not(.mod-tag-more):not(.mod-tag-hidden)'));
      const last = visible.pop();
      if (!last) break;
      last.classList.add('mod-tag-hidden');
    }
    tags.setAttribute('data-clamped', '1');
  } else {
    tags.removeAttribute('data-clamped');
  }

  // 固定为两行高度：标签少时底部留白，保证所有卡片对齐
  const visibleChips = Array.from(tags.querySelectorAll('.mod-tag')).filter(node => !node.classList.contains('mod-tag-hidden'));
  if (visibleChips.length) {
    const chipHeight = visibleChips.reduce((max, node) => Math.max(max, node.getBoundingClientRect().height), 0);
    tags.style.height = (2 * chipHeight + rowGap) + 'px';
  }
}

// ===== 悬停浮层：显示被截断的介绍 / 标签完整内容 =====

let _cardTipHost = null;

function modCardTipElement() {
  let tip = document.getElementById('modCardTip');
  if (!tip) {
    tip = document.createElement('div');
    tip.id = 'modCardTip';
    document.body.appendChild(tip);
  }
  return tip;
}

function showModCardTip(host) {
  const tip = modCardTipElement();
  tip.textContent = '';
  if (host.classList.contains('mod-card-desc')) {
    const text = document.createElement('div');
    text.className = 'mod-card-tip-desc';
    text.textContent = host.textContent;
    tip.appendChild(text);
  } else {
    const wrap = document.createElement('div');
    wrap.className = 'mod-card-tip-tags';
    host.querySelectorAll('.mod-tag:not(.mod-tag-more)').forEach(node => {
      const copy = node.cloneNode(true);
      copy.classList.remove('mod-tag-hidden');
      wrap.appendChild(copy);
    });
    tip.appendChild(wrap);
  }

  tip.setAttribute('data-show', '1');
  const hostRect = host.getBoundingClientRect();
  const tipRect = tip.getBoundingClientRect();
  let left = hostRect.left;
  if (left + tipRect.width > window.innerWidth - 8) left = Math.max(8, window.innerWidth - tipRect.width - 8);
  let top = hostRect.bottom + 8;
  if (top + tipRect.height > window.innerHeight - 8) top = Math.max(8, hostRect.top - tipRect.height - 8);
  tip.style.left = left + 'px';
  tip.style.top = top + 'px';
}

function hideModCardTip() {
  const tip = document.getElementById('modCardTip');
  if (tip) tip.removeAttribute('data-show');
  _cardTipHost = null;
}

document.addEventListener('mouseover', event => {
  const target = event.target;
  if (!target || !target.closest) return;
  const host = target.closest('.mod-card-desc[data-clamped], .mod-card-tags[data-clamped]');
  if (host) {
    if (_cardTipHost !== host) { _cardTipHost = host; showModCardTip(host); }
  } else {
    hideModCardTip();
  }
});

document.addEventListener('mouseout', event => {
  if (_cardTipHost) {
    const to = event.relatedTarget;
    if (!to || !_cardTipHost.contains(to)) hideModCardTip();
  }
});

// 缩放窗口 / 字体加载完成后重新按新宽度排版
let _cardTipResizeTimer = null;
window.addEventListener('resize', () => {
  clearTimeout(_cardTipResizeTimer);
  _cardTipResizeTimer = setTimeout(() => {
    document.querySelectorAll('#modList .mod-card').forEach(layoutModCard);
  }, 150);
});
if (document.fonts && document.fonts.ready) {
  document.fonts.ready.then(() => document.querySelectorAll('#modList .mod-card').forEach(layoutModCard));
}
