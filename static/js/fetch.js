// fetch.js — data loading
async function fetchData() {
  try {
    const [modsRes, orderRes, cfgRes] = await Promise.all([
      fetch('/api/mods'), fetch('/api/status-order'), fetch('/api/config'),
    ]);
    state.mods = await modsRes.json();
    const orderData = await orderRes.json();
    state.statusOrder = orderData.order || [];
    state.statusArchived = orderData.archived || [];
    state.publicConfig = await cfgRes.json();

    aggregateGitHub();
    updateStats();
    populateFilters();
    $('#sortBy').value = state.currentSort;
    restoreFromHash();
    switchPage(state.currentPage);
    // 恢复 agents tab 和 mods view 状态
    if (state.currentPage === 'agents' && typeof switchAgentTab === 'function') {
      switchAgentTab(state.currentAgentTab || 'chat');
    }
    if (state.currentPage === 'mods' && state.currentView === 'scatter') {
      $$('.mods-tab').forEach(function (b) { b.classList.toggle('active', b.dataset.view === 'scatter'); });
      $('#listView').style.display = 'none';
      $('#scatterView').style.display = 'block';
    }
    var s = $('#spinner'); if (s) s.remove();
  } catch (err) {
    console.error('Data load failed:', err);
    var s = $('#spinner'); if (s) s.remove();
    $('#modList').innerHTML = '<p style="text-align:center;color:#ef4444;padding:40px;">' + t('loadingFailed') + '</p>';
  }
}

function aggregateGitHub() {
  state.allGitHubItems = { issues: [], pulls: [] };
  state.totalIssues = 0; state.totalPRs = 0;
  const skipArchived = state.publicConfig.ignore_archived !== false;
  state.mods.forEach(mod => {
    if (!mod.github) return;
    mod.github.issues?.forEach(i => {
      state.allGitHubItems.issues.push({ ...i, repo: mod.name, repoPath: mod.ids?.gh });
      if (i.state === 'open' && (!skipArchived || !i.archived)) state.totalIssues++;
    });
    mod.github.pull_requests?.forEach(p => {
      state.allGitHubItems.pulls.push({ ...p, repo: mod.name, repoPath: mod.ids?.gh });
      if (p.state === 'open' && (!skipArchived || !p.archived)) state.totalPRs++;
    });
  });
  const sortFn = (a, b) => (b.created_at || '').localeCompare(a.created_at || '');
  state.allGitHubItems.issues.sort(sortFn);
  state.allGitHubItems.pulls.sort(sortFn);
}

function updateStats() {
  $('#statTotal').textContent = state.mods.length;
  const totalDl = state.mods.reduce((s, m) => s + (m.total_downloads || 0), 0);
  $('#statDownloads').textContent = formatNum(totalDl);
  $('#statIssues').textContent = state.totalIssues;
  $('#statPRs').textContent = state.totalPRs;
}

// 刷新按钮：从后端重新拉取数据，无需刷新整个页面
function setupRefreshButtons() {
  const modsBtn = document.getElementById('modsRefreshBtn');
  const githubBtn = document.getElementById('githubRefreshBtn');
  if (modsBtn) modsBtn.addEventListener('click', () => refreshLiveData(modsBtn));
  if (githubBtn) githubBtn.addEventListener('click', () => refreshLiveData(githubBtn));
}

async function refreshLiveData(btn) {
  if (btn && btn.disabled) return;
  if (btn) { btn.disabled = true; btn.classList.add('spinning'); }
  try {
    const [modsRes, orderRes, cfgRes] = await Promise.all([
      fetch('/api/mods'), fetch('/api/status-order'), fetch('/api/config'),
    ]);
    if (!modsRes.ok || !orderRes.ok || !cfgRes.ok) throw new Error('refresh failed');
    state.mods = await modsRes.json();
    const orderData = await orderRes.json();
    state.statusOrder = orderData.order || [];
    state.statusArchived = orderData.archived || [];
    state.publicConfig = await cfgRes.json();

    aggregateGitHub();
    updateStats();
    populateFilters();

    if (state.currentPage === 'mods') {
      renderList();
      if (state.currentView === 'scatter' && typeof renderScatter === 'function') renderScatter();
    } else if (state.currentPage === 'github' || state.currentPage === 'issues') {
      renderGitHubPanel();
    }
  } catch (err) {
    console.error('Refresh failed:', err);
    alert(t('loadingFailed'));
  } finally {
    if (btn) { btn.disabled = false; btn.classList.remove('spinning'); }
  }
}
