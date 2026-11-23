// github.js — GitHub Issues/PRs panel
var issuePreprocessState = { enabled: false, reports: {} };
var issuePreprocessPoll = null;
var issuePreprocessSaving = false;
var issuePreprocessAuthGeneration = 0;

function setupGitHubTabs() {
  document.getElementById('issuePreprocessCheck').addEventListener('change', saveIssuePreprocessSetting);
  $('#githubList').addEventListener('click', function (e) {
    var reportBtn = e.target.closest('.gh-preprocess-report');
    var retryBtn = e.target.closest('.gh-preprocess-retry');
    if (reportBtn) {
      e.preventDefault();
      openIssuePreprocessReport(reportBtn.dataset.sessionId);
    } else if (retryBtn) {
      e.preventDefault();
      retryIssuePreprocess(retryBtn);
    }
  });
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
      (ghType === 'issue' && loggedIn ? issuePreprocessReportButton(rp, item.number) : '') + dispatchBtn +
      '</span>' +
      '</div>';
  }).join('');
}

function updateIssuePreprocessAuth() {
  issuePreprocessAuthGeneration++;
  clearInterval(issuePreprocessPoll);
  issuePreprocessPoll = null;
  issuePreprocessState = { enabled: false, reports: {} };
  document.getElementById('issuePreprocessControls').style.display = state.loggedIn ? '' : 'none';
  document.getElementById('issuePreprocessMessage').textContent = '';
  document.getElementById('issuePreprocessCheck').checked = false;
  document.getElementById('issuePreprocessCheck').disabled = true;
  if (state.loggedIn) {
    fetchIssuePreprocessState();
    issuePreprocessPoll = setInterval(fetchIssuePreprocessState, 10000);
  }
  renderGitHubPanel();
}

async function fetchIssuePreprocessState() {
  if (!state.loggedIn || issuePreprocessSaving) return;
  var generation = issuePreprocessAuthGeneration;
  try {
    var response = await fetch('/api/admin/issue-preprocess', { headers: getAuthHeaders() });
    var data = await response.json();
    if (generation !== issuePreprocessAuthGeneration || issuePreprocessSaving) return;
    if (!response.ok) throw new Error(data.error || t('issuePreprocessLoadFailed'));
    issuePreprocessState = data;
    document.getElementById('issuePreprocessCheck').checked = !!data.enabled;
    document.getElementById('issuePreprocessCheck').disabled = false;
    document.getElementById('issuePreprocessMessage').textContent = '';
    renderGitHubPanel();
  } catch (e) {
    if (generation !== issuePreprocessAuthGeneration || issuePreprocessSaving) return;
    document.getElementById('issuePreprocessCheck').disabled = true;
    document.getElementById('issuePreprocessMessage').textContent = e.message;
  }
}

async function saveIssuePreprocessSetting() {
  var checkbox = document.getElementById('issuePreprocessCheck');
  var generation = ++issuePreprocessAuthGeneration;
  checkbox.disabled = true;
  issuePreprocessSaving = true;
  try {
    var response = await fetch('/api/admin/issue-preprocess', {
      method: 'PUT', headers: getAuthHeaders(), body: JSON.stringify({ enabled: checkbox.checked })
    });
    var data = await response.json();
    if (generation !== issuePreprocessAuthGeneration) return;
    if (!response.ok) throw new Error(data.error || t('issuePreprocessSaveFailed'));
    issuePreprocessState = data;
    document.getElementById('issuePreprocessMessage').textContent = t(data.enabled ? 'issuePreprocessEnabled' : 'issuePreprocessDisabled');
    renderGitHubPanel();
  } catch (e) {
    if (generation === issuePreprocessAuthGeneration) document.getElementById('issuePreprocessMessage').textContent = e.message;
  } finally {
    issuePreprocessSaving = false;
    if (generation === issuePreprocessAuthGeneration) {
      checkbox.checked = !!issuePreprocessState.enabled;
      checkbox.disabled = false;
    }
  }
}

function issuePreprocessReportButton(repo, number) {
  var report = (issuePreprocessState.reports || {})[repo.toLowerCase() + '#' + number];
  if (!report) return '';
  var label = t('issuePreprocess_' + report.status);
  var error = report.error === 'max rounds exceeded' ? t('issuePreprocessRoundsExceeded') : report.error;
  var html = report.session_id && report.status === 'completed'
    ? '<button type="button" class="gh-preprocess-report" data-session-id="' + esc(report.session_id) + '" title="' + esc(error || t('issuePreprocessViewReport')) + '">' + esc(label) + '</button>'
    : '<span class="gh-preprocess-status" title="' + esc(error || '') + '">' + esc(label) + '</span>';
  if (report.session_id && report.status !== 'completed') {
    html += '<button type="button" class="gh-preprocess-report" data-session-id="' + esc(report.session_id) + '" title="' + esc(t('issuePreprocessViewReport')) + '">' + esc(t('issuePreprocessViewConversation')) + '</button>';
  }
  if (report.status === 'failed' && issuePreprocessState.enabled) {
    html += '<button type="button" class="gh-preprocess-retry" data-repo="' + esc(repo) + '" data-number="' + number + '">' + esc(t('issuePreprocessRetry')) + '</button>';
  }
  return html;
}

async function retryIssuePreprocess(btn) {
  btn.disabled = true;
  try {
    var response = await fetch('/api/admin/issue-preprocess', {
      method: 'POST', headers: getAuthHeaders(), body: JSON.stringify({ repo: btn.dataset.repo, number: Number(btn.dataset.number) })
    });
    if (!response.ok) {
      var data = await response.json();
      throw new Error(data.error || t('issuePreprocessSaveFailed'));
    }
    await fetchIssuePreprocessState();
  } catch (e) {
    document.getElementById('issuePreprocessMessage').textContent = e.message;
  } finally {
    btn.disabled = false;
  }
}

async function openIssuePreprocessReport(sessionId) {
  if (!state.loggedIn) return;
  if (!await fetchAgentInfo()) return;
  switchPage('agents');
  openChatWindow(sessionId, FindAgentName('issue-preprocessor'));
}
