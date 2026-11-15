// login.js
function setupLangSwitcher() {
  const btn = document.getElementById('langDropdownBtn');
  const menu = document.getElementById('langDropdownMenu');
  updateLangDropdown();
  btn.addEventListener('click', (e) => { e.stopPropagation(); menu.classList.toggle('open'); document.getElementById('adminDropdownMenu').classList.remove('open'); document.getElementById('licenseDropdownMenu').classList.remove('open'); });
  document.addEventListener('click', () => { menu.classList.remove('open'); });
  menu.querySelectorAll('.lang-option').forEach(opt => {
    opt.addEventListener('click', () => { setLang(opt.dataset.lang); menu.classList.remove('open'); });
  });
}

// 右上角「许可证下载」：条目来自 data/license/ 目录（文件名即许可证名），
// 点击直接下载原文。目录为空时整个入口不显示。
function setupLicenseDropdown() {
  const dropdown = document.getElementById('licenseDropdown');
  const btn = document.getElementById('licenseDropdownBtn');
  const menu = document.getElementById('licenseDropdownMenu');
  if (!dropdown || !btn || !menu) return;

  btn.addEventListener('click', (e) => {
    e.stopPropagation();
    menu.classList.toggle('open');
    document.getElementById('langDropdownMenu').classList.remove('open');
    document.getElementById('adminDropdownMenu').classList.remove('open');
  });
  document.addEventListener('click', () => menu.classList.remove('open'));

  fetch('/api/config')
    .then(r => r.json())
    .then(config => {
      const names = (config && config.license_options) || [];
      if (!names.length) return;
      menu.textContent = '';
      names.forEach(name => {
        const item = document.createElement('div');
        item.className = 'lang-option';
        item.textContent = name;
        item.addEventListener('click', () => {
          menu.classList.remove('open');
          window.location.href = '/api/license/download?name=' + encodeURIComponent(name);
        });
        menu.appendChild(item);
      });
      dropdown.style.display = '';
    })
    .catch(() => { });
}

function setupAdminDropdown() {
  const btn = document.getElementById('adminDropdownBtn');
  const menu = document.getElementById('adminDropdownMenu');
  btn.addEventListener('click', (e) => { e.stopPropagation(); menu.classList.toggle('open'); document.getElementById('langDropdownMenu').classList.remove('open'); document.getElementById('licenseDropdownMenu').classList.remove('open'); });
  document.addEventListener('click', () => { menu.classList.remove('open'); });
  menu.querySelectorAll('.admin-option').forEach(opt => {
    opt.addEventListener('click', async () => {
      const source = opt.dataset.source;

      if (source === 'accounts') {
        menu.classList.remove('open');
        openAccountSettings();
        return;
      }

      if (source === 'mod-editor') {
        menu.classList.remove('open');
        window.location.href = 'mod-editor.html';
        return;
      }

      // MCP 刷新是特殊路径
      if (source === 'mcp-refresh') {
        menu.classList.remove('open');
        menu.querySelectorAll('.admin-option').forEach(o => o.classList.add('loading'));
        const toast = showToast('⏳ 正在重新检测 MCP 服务器...');
        try {
          const r = await fetch('/api/agent/mcp/refresh', { method: 'POST', headers: getAuthHeaders() });
          const data = await r.json();
          toast.textContent = '✅ ' + data.message;
          // 刷新 agent info
          setTimeout(() => {
            if (typeof fetchAgentInfo === 'function') fetchAgentInfo().then(() => {
              if (typeof renderMcpView === 'function') renderMcpView();
            });
          }, 500);
        } catch (e) { toast.textContent = '❌ 检测失败'; }
        finally {
          setTimeout(() => {
            menu.querySelectorAll('.admin-option').forEach(o => o.classList.remove('loading'));
            toast.remove();
          }, 2500);
        }
        return;
      }

      menu.classList.remove('open');
      menu.querySelectorAll('.admin-option').forEach(o => o.classList.add('loading'));
      const busyText = {
        db: '正在清理数据库缓存...',
        curseforge: '正在清理 CurseForge 缓存...',
        github: '正在清理 GitHub 缓存...',
        codacy: '正在清理 Codacy 缓存...',
        all: '正在清理全部缓存...',
        reload: '正在重载本地配置...',
      };
      const toast = showToast('⏳ ' + (busyText[source] || source + ' 处理中...'));
      try {
        const token = localStorage.getItem('modlist_token');
        const r = await fetch('/api/admin/flush-cache', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + (token || '') },
          body: JSON.stringify({ source }),
        });
        const data = await r.json();
        toast.textContent = data.message || ('✅ ' + source + ' 刷新已触发');
        // 延迟后刷新页面显示新数据
        setTimeout(() => {
          if (typeof state !== 'undefined') {
            state.mods = [];
            var s = $('#spinner');
            if (!s) { s = document.createElement('div'); s.id = 'spinner'; s.innerHTML = '<loading-indicator></loading-indicator>'; document.getElementById('contentArea').prepend(s); }
            if (typeof fetchData === 'function') fetchData();
            if (typeof fetchTodoCounts === 'function') fetchTodoCounts();
          }
        }, 2000);
      } catch (e) {
        toast.textContent = '❌ ' + (source === 'reload' ? '重载失败' : '清理失败');
      } finally {
        setTimeout(() => {
          menu.querySelectorAll('.admin-option').forEach(o => o.classList.remove('loading'));
          toast.remove();
        }, 2500);
      }
    });
  });
}

function showToast(msg) {
  let el = document.getElementById('adminToast');
  if (!el) {
    el = document.createElement('div');
    el.id = 'adminToast';
    el.style.cssText = 'position:fixed;bottom:24px;left:50%;transform:translateX(-50%);padding:10px 24px;background:#1a1a1a;color:#fff;border-radius:8px;font-size:0.85rem;z-index:9999;box-shadow:0 4px 16px rgba(0,0,0,0.2);transition:opacity 0.3s;';
    document.body.appendChild(el);
  }
  el.textContent = msg;
  el.style.opacity = '1';
  return el;
}

function authToken() {
  return localStorage.getItem('modlist_token') || '';
}

function authJsonHeaders() {
  const token = authToken();
  return token ? { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token } : { 'Content-Type': 'application/json' };
}

function accountMessage(id, message, isError) {
  const el = document.getElementById(id);
  if (!el) return;
  el.textContent = message || '';
  el.classList.toggle('error', !!isError);
}

async function openAccountSettings() {
  const overlay = document.getElementById('accountOverlay');
  if (!overlay) return;
  document.getElementById('accountCurrentUser').textContent = state.currentUser ? '@' + state.currentUser : '';
  accountMessage('accountPasswordMessage', '');
  accountMessage('accountUserMessage', '');
  overlay.classList.add('show');
  await refreshAccountUsers();
}

async function refreshAccountUsers() {
  const list = document.getElementById('accountUserList');
  if (!list) return;
  list.textContent = t('accountLoading');
  try {
    const response = await fetch('/api/auth/users', { headers: authJsonHeaders() });
    const data = await response.json();
    if (!response.ok) throw new Error(data.error || t('accountLoadFailed'));
    const users = data.users || [];
    document.getElementById('accountUserCount').textContent = users.length + ' ' + t('accountUsersCount');
    list.textContent = '';
    users.forEach(user => {
      const row = document.createElement('div');
      row.className = 'account-user-row' + (user.username === state.currentUser ? ' current' : '');
      const info = document.createElement('div');
      const name = document.createElement('span');
      name.className = 'account-user-name';
      name.textContent = user.username;
      info.appendChild(name);
      if (user.username === state.currentUser) {
        const current = document.createElement('span');
        current.className = 'account-user-meta';
        current.textContent = ' ' + t('accountCurrentBadge');
        info.appendChild(current);
      }
      row.appendChild(info);
      if (user.username !== state.currentUser) {
        const remove = document.createElement('button');
        remove.className = 'account-action-btn danger';
        remove.type = 'button';
        remove.textContent = t('accountDeleteUser');
        remove.addEventListener('click', () => deleteAccount(user.username));
        row.appendChild(remove);
      }
      list.appendChild(row);
    });
  } catch (error) {
    list.textContent = '';
    accountMessage('accountUserMessage', error.message, true);
  }
}

async function deleteAccount(username) {
  if (!window.confirm(t('accountDeleteConfirm').replace('{user}', username))) return;
  try {
    const response = await fetch('/api/auth/users/' + encodeURIComponent(username), { method: 'DELETE', headers: authJsonHeaders() });
    const data = await response.json();
    if (!response.ok) throw new Error(data.error || t('accountDeleteFailed'));
    await refreshAccountUsers();
  } catch (error) {
    accountMessage('accountUserMessage', error.message, true);
  }
}

function setupAccountSettings() {
  const overlay = document.getElementById('accountOverlay');
  if (!overlay) return;
  document.getElementById('accountCloseBtn').addEventListener('click', () => overlay.classList.remove('show'));
  overlay.addEventListener('click', event => { if (event.target === overlay) overlay.classList.remove('show'); });
  document.getElementById('accountChangePassword').addEventListener('click', async () => {
    const current = document.getElementById('accountCurrentPassword').value;
    const next = document.getElementById('accountNewPassword').value;
    const confirm = document.getElementById('accountConfirmPassword').value;
    if (!current || !next) { accountMessage('accountPasswordMessage', t('accountPasswordRequired'), true); return; }
    if (next !== confirm) { accountMessage('accountPasswordMessage', t('accountPasswordMismatch'), true); return; }
    try {
      const response = await fetch('/api/auth/password', { method: 'POST', headers: authJsonHeaders(), body: JSON.stringify({ current_password: current, new_password: next }) });
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || t('accountPasswordUpdateFailed'));
      localStorage.setItem('modlist_token', data.token);
      document.getElementById('accountCurrentPassword').value = '';
      document.getElementById('accountNewPassword').value = '';
      document.getElementById('accountConfirmPassword').value = '';
      accountMessage('accountPasswordMessage', t('accountPasswordUpdated'));
    } catch (error) {
      accountMessage('accountPasswordMessage', error.message, true);
    }
  });
  document.getElementById('accountCreateUser').addEventListener('click', async () => {
    const username = document.getElementById('accountNewUsername').value.trim();
    const password = document.getElementById('accountNewUserPassword').value;
    if (!username || !password) { accountMessage('accountUserMessage', t('accountUserFieldsRequired'), true); return; }
    try {
      const response = await fetch('/api/auth/users', { method: 'POST', headers: authJsonHeaders(), body: JSON.stringify({ username, password }) });
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || t('accountCreateFailed'));
      document.getElementById('accountNewUsername').value = '';
      document.getElementById('accountNewUserPassword').value = '';
      accountMessage('accountUserMessage', t('accountUserCreated'));
      await refreshAccountUsers();
    } catch (error) {
      accountMessage('accountUserMessage', error.message, true);
    }
  });
}
function updateLangDropdown() {
  document.querySelectorAll('.lang-option').forEach(opt => {
    opt.querySelector('.lang-check').style.visibility = opt.dataset.lang === currentLang ? 'visible' : 'hidden';
  });
}

function setupLogin() {
  const loginBtn = document.getElementById('loginBtn');
  const logoutBtn = document.getElementById('logoutBtn');
  const overlay = document.getElementById('loginOverlay');
  const closeBtn = document.getElementById('loginCloseBtn');
  const submitBtn = document.getElementById('loginSubmit');
  const errorEl = document.getElementById('loginError');
  const token = localStorage.getItem('modlist_token');
  if (token) {
    fetch('/api/auth/status', { headers: { Authorization: 'Bearer ' + token } })
      .then(r => r.json()).then(d => { if (d.authenticated) { state.loggedIn = true; state.currentUser = d.username || ''; updateLoginUI(); } else { state.currentUser = ''; localStorage.removeItem('modlist_token'); } });
  }
  const observer = setInterval(() => {
    if (state.publicConfig.login_enabled !== undefined) { updateLoginUI(); clearInterval(observer); }
  }, 200);
  updateLoginTxt();
  loginBtn.addEventListener('click', () => { overlay.classList.add('show'); errorEl.textContent = ''; document.getElementById('loginPass').value = ''; });
  closeBtn.addEventListener('click', () => overlay.classList.remove('show'));
  overlay.addEventListener('click', e => { if (e.target === overlay) overlay.classList.remove('show'); });
  submitBtn.addEventListener('click', async () => {
    const u = document.getElementById('loginUser').value.trim(), p = document.getElementById('loginPass').value;
    if (!u || !p) { errorEl.textContent = 'Please enter username and password'; return; }
    try {
      const res = await fetch('/api/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: u, password: p }) });
      if (!res.ok) { errorEl.textContent = (await res.json()).error || 'Login failed'; return; }
      const data = await res.json();
      localStorage.setItem('modlist_token', data.token); state.loggedIn = true; state.currentUser = data.username || u; updateLoginUI(); overlay.classList.remove('show');
    } catch { errorEl.textContent = 'Network error'; }
  });
  logoutBtn.addEventListener('click', () => { localStorage.removeItem('modlist_token'); state.loggedIn = false; state.currentUser = ''; updateLoginUI(); });
  setupAccountSettings();
  document.addEventListener('keydown', e => { if (e.key === 'Enter' && overlay.classList.contains('show')) submitBtn.click(); });
}
function updateLoginUI() {
  document.getElementById('loginBtn').style.display = state.loggedIn ? 'none' : '';
  document.getElementById('logoutBtn').style.display = state.loggedIn ? '' : 'none';
  document.getElementById('adminDropdown').style.display = state.loggedIn ? '' : 'none';
  // 隐藏/显示 Agent 统计卡片和网盘卡片
  var agentCard = document.querySelector('.stat-card[data-page="agents"]');
  if (agentCard) agentCard.style.display = state.loggedIn ? '' : 'none';
  var driveCard = document.querySelector('.stat-card[data-page="drive"]');
  if (driveCard) driveCard.style.display = state.loggedIn ? '' : 'none';
  var settingsView = document.getElementById('agentSettingsView');
  if (settingsView) settingsView.style.display = state.loggedIn && state.currentAgentTab === 'settings' ? '' : 'none';
  if (typeof updateIssuePreprocessAuth === 'function') updateIssuePreprocessAuth();
  if (typeof updateBotBindingAuth === 'function') updateBotBindingAuth();
  updateLoginTxt();
}
function updateLoginTxt() {
  const lb = document.getElementById('loginBtn');
  const lo = document.getElementById('logoutBtn');
  if (lb) lb.childNodes.forEach(n => { if (n.nodeType === 3) n.textContent = ''; if (n.tagName === 'SPAN') n.textContent = t('btnLogin'); });
  if (lo) lo.textContent = t('btnLogout');
}
