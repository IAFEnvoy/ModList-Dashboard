// agent-state.js — Agent 全局状态、数据获取、辅助函数
var agentInfo = null;
var agentSessions = [];
var currentChatSessionId = null;
var currentAgentTab = 'chat';

async function fetchAgentInfo() {
  try {
    var r = await fetch('/api/agent/info', { headers: getAuthHeaders() });
    if (!r.ok) {
      console.warn('fetchAgentInfo: HTTP', r.status);
      return false;
    }
    agentInfo = await r.json();
    $('#statAgentTypes').textContent = (agentInfo.agents || []).length;
    $('#statMCPServers').textContent = (agentInfo.mcps || []).length + (agentInfo.builtin_providers || []).length;
    await fetchAgentSessions();
    return true;
  } catch (e) {
    console.warn('fetchAgentInfo:', e);
    return false;
  }
}

async function fetchAgentSessions(forceActive) {
  try {
    var archived = !forceActive && typeof historyArchivedMode !== 'undefined' && historyArchivedMode;
    var r = await fetch('/api/agent/sessions' + (archived ? '?archived=true' : ''), { headers: getAuthHeaders() });
    if (!r.ok) { console.warn('fetchAgentSessions: HTTP', r.status); return; }
    var data = await r.json();
    if (Array.isArray(data)) { agentSessions = data; }
    // 刷新历史列表（首次加载时替换转圈）
    if (typeof updateChatHistory === 'function') updateChatHistory();
  } catch (e) { console.warn('fetchAgentSessions:', e); }
}

async function deleteSession(id) {
  var permanent = typeof historyArchivedMode !== 'undefined' && historyArchivedMode;
  if (!confirm(permanent ? '永久删除后无法恢复，确认继续？' : '确认归档此对话？')) return;
  agentSessions = (agentSessions || []).filter(function (s) { return s.id !== id; });
  if (currentChatSessionId === id) {
    if (typeof backToHistory === 'function') backToHistory();
  }
  if (typeof updateChatHistory === 'function') updateChatHistory();
  if (typeof renderAgentListView === 'function') renderAgentListView();

  try {
    var url = '/api/agent/sessions/' + id + (permanent ? '?permanent=true' : '');
    var r = await fetch(url, { method: 'DELETE', headers: getAuthHeaders() });
    if (!r.ok) {
      // 回滚：重新拉取列表
      await fetchAgentSessions();
      if (typeof updateChatHistory === 'function') updateChatHistory();
      if (typeof renderAgentListView === 'function') renderAgentListView();
      alert((permanent ? '删除' : '归档') + '失败: ' + r.status);
    }
  } catch (e) {
    await fetchAgentSessions();
    if (typeof updateChatHistory === 'function') updateChatHistory();
    if (typeof renderAgentListView === 'function') renderAgentListView();
    alert((permanent ? '删除' : '归档') + '失败: ' + (e.message || String(e)));
  }
}

async function restoreSession(id) {
  try {
    var r = await fetch('/api/agent/sessions/' + id + '/restore', { method: 'POST', headers: getAuthHeaders() });
    if (!r.ok) throw new Error('HTTP ' + r.status);
    agentSessions = (agentSessions || []).filter(function (s) { return s.id !== id; });
    if (typeof updateChatHistory === 'function') updateChatHistory();
  } catch (e) {
    alert('恢复失败: ' + (e.message || String(e)));
  }
}

function FindAgentName(id) {
  var d = (agentInfo ? (agentInfo.agents || []) : []).find(function (a) { return a.id === id; });
  return d ? d.name : id;
}

function getCurrentModelName() {
  if (!currentChatSessionId || !agentInfo || !agentSessions) return '';
  var s = agentSessions.find(function (x) { return x.id === currentChatSessionId; });
  if (!s) return '';
  var m = (agentInfo.models || []).find(function (x) { return x.id === s.provider; });
  return m ? m.name : '';
}

function fmtTok(n) {
  if (!n) return '0';
  if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
  if (n >= 1000) return (n / 1000).toFixed(1) + 'K';
  return String(n);
}
