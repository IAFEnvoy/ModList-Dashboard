// github.js — GitHub Issues/PRs panel
function setupGitHubTabs() {
  $$('.gh-tab').forEach(tab => {
    tab.addEventListener('click', () => {
      $$('.gh-tab').forEach(t => t.classList.remove('active'));
      tab.classList.add('active');
      state.currentGhTab = tab.dataset.gh;
      renderGitHubPanel();
    });
  });
  $$('.gh-filter').forEach(btn => {
    btn.addEventListener('click', () => {
      $$('.gh-filter').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      state.currentGhFilter = btn.dataset.ghFilter;
      renderGitHubPanel();
    });
  });
  document.getElementById('showArchivedCheck').addEventListener('change', renderGitHubPanel);
  // 派发按钮点击
  $('#githubList').addEventListener('click', function (e) {
    var btn = e.target.closest('.gh-dispatch-btn');
    if (!btn) return;
    e.preventDefault();
    e.stopPropagation();
    if (typeof showDispatchDialog === 'function') {
      showDispatchDialog(
        btn.dataset.ghOwner,
        btn.dataset.ghRepo,
        parseInt(btn.dataset.ghNumber),
        btn.dataset.ghTitle,
        btn.dataset.ghType
      );
    }
  });
}

function renderGitHubPanel() {
  let items = state.currentGhTab === 'issues' ? state.allGitHubItems.issues : state.allGitHubItems.pulls;
  const showArchived = document.getElementById('showArchivedCheck').checked;
  if (!showArchived) items = items.filter(i => !i.archived);
  if (state.currentGhFilter === 'open') items = items.filter(i => i.state === 'open');
  else if (state.currentGhFilter === 'closed') items = items.filter(i => i.state !== 'open');
  $('#githubList').innerHTML = items.length === 0 ? '<div style="padding:16px;color:#9ca3af;text-align:center;">' + t('noData') + '</div>' : items.map(item => {
    var rp = item.repoPath || '';
    var parts = rp ? rp.split('/') : [];
    var owner = parts[0] || '', repo = parts[1] || '';
    var loggedIn = (typeof state !== 'undefined' && state.loggedIn);
    var ghType = state.currentGhTab === 'pulls' ? 'pr' : 'issue';
    var dispatchBtn = (rp && loggedIn) ? ' <button class="gh-dispatch-btn" data-gh-type="' + esc(ghType) + '" data-gh-owner="' + esc(owner) + '" data-gh-repo="' + esc(repo) + '" data-gh-number="' + item.number + '" data-gh-title="' + esc(item.title) + '" title="派发到智能体">🚀</button>' : '';
    return '<div class="gh-item">' +
      '<img src="' + getGhStateIcon(item, ghType) + '" class="stat-icon gh-state-icon" title="' + getGhStateLabel(item) + '">' +
      '<span class="gh-repo">' + esc(item.repo || '') + (item.archived ? ' 📦' : '') + ' #' + item.number + '</span>' +
      '<a href="' + item.url + '" target="_blank">' + esc(item.title) + '</a>' +
      '<span class="gh-meta">' +
      (item.labels || []).map(function (l) { return '<span class="gh-label">' + esc(l) + '</span>'; }).join('') +
      '<span class="gh-user">' + esc(item.user || '') + '</span>' +
      (item.created_at ? '<span class="gh-date">' + item.created_at.slice(0, 10) + '</span>' : '') +
      dispatchBtn +
      '</span>' +
      '</div>';
  }).join('');
}
