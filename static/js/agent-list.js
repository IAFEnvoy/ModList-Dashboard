// agent-list.js — Agent list with dropdown + detail panel

function renderAgentListView() {
  if (typeof state !== 'undefined' && !state.loggedIn) {
    $('#agentListView').innerHTML = '<div style="text-align:center;padding:40px;color:var(--text-secondary);">Login required</div>';
    return;
  }
  if (!agentInfo) return;

  var agents = agentInfo.agents || [];
  var models = agentInfo.models || [];
  var sessions = agentSessions || [];
  var mods = (typeof state !== 'undefined' && state.mods) ? state.mods : [];

  // Populate dropdown
  var sel = $('#agentSelect');
  if (sel) {
    var opts = '<option value="">' + t('agentSelectPlaceholder') + '</option>' +
      agents.map(function (def) {
        var acts = sessions.filter(function (s) { return s.agent === def.id; }).length;
        var desc = (def.description || '').substring(0, 80);
        return '<option value="' + esc(def.id) + '">' +
          esc(def.name) + ' — ' + esc(desc) +
          '</option>';
      }).join('');
    sel.innerHTML = opts;
    sel.onchange = function () { onAgentSelect(sel.value); };
  }

  var dp = $('#agentDetailPanel');
  if (dp) dp.style.display = 'none';

  window._agentModels = models;
  window._agentSessions = sessions;
  window._agentMods = mods;
}

function onAgentSelect(agentId) {
  var dp = $('#agentDetailPanel');
  if (!dp || !agentId) { if (dp) dp.style.display = 'none'; return; }

  var agents = (agentInfo && agentInfo.agents) ? agentInfo.agents : [];
  var def = agents.find(function (a) { return a.id === agentId; });
  if (!def) { dp.style.display = 'none'; return; }

  var models = window._agentModels || agentInfo.models || [];
  var sessions = window._agentSessions || [];
  var mods = window._agentMods || [];
  var bpMap = {}, mcpMap = {};
  (agentInfo.builtin_providers || []).forEach(function (bp) { bpMap[bp.id] = bp; });
  (agentInfo.mcps || []).forEach(function (m) { mcpMap[m.id] = m; });

  var acts = sessions.filter(function (s) { return s.agent === def.id; }).length;
  var running = sessions.filter(function (s) { return s.agent === def.id && s.running; }).length;

  // Sub agents
  var subsHtml = '';
  if ((def.sub_agents || []).length > 0) {
    subsHtml = def.sub_agents.map(function (id) {
      return '<span class="agent-subagent-tag">' + esc(FindAgentName(id)) + '</span>';
    }).join(' ');
  }

  // Tool tags
  var toolTags = [];
  (def.mcps || []).forEach(function (t) {
    var bp = bpMap[t];
    if (bp) {
      var toolList = bp.tools.map(function (x) { return x.id; }).join(', ');
      toolTags.push('<span class="agent-mcp-tag builtin-tag" title="' + esc(bp.name) + ': ' + esc(toolList) + '">' + esc(bp.name) + '</span>');
    } else {
      var srv = mcpMap[t];
      var name = srv ? srv.name : t;
      var status = srv ? srv.status : '';
      var cls = 'agent-mcp-tag' + (status !== 'ok' ? ' broken' : '');
      var title = esc(name) + (srv ? ' (' + (srv.tools ? srv.tools.length : 0) + ' tools, ' + status + ')' : '');
      toolTags.push('<span class="' + cls + '" title="' + title + '">' + esc(name) + '</span>');
    }
  });

  var modelOpts = models.map(function (m) {
    var pn = m.provider_name || (m.id ? m.id.split('/')[0] : '');
    return '<option value="' + esc(m.id) + '">(' + esc(pn) + ') ' + esc(m.name) + '</option>';
  }).join('');

  var modOpts = '<option value="">(Any)</option>' + mods.slice().sort(function (a, b) {
    return (a.name || '').localeCompare(b.name || '');
  }).map(function (m) {
    return '<option value="' + esc(m.name) + '">' + esc(m.name) + '</option>';
  }).join('');

  // Warnings
  var warnHtml = '';
  var pw = def.prompt_warnings || [];
  var brokenMcps = [];
  (def.mcps || []).forEach(function (t) {
    if (!bpMap[t]) { var srv = mcpMap[t]; if (srv && srv.status !== 'ok') brokenMcps.push(t); }
  });
  if (brokenMcps.length > 0) pw = pw.concat(['MCP broken: ' + brokenMcps.join(', ')]);
  if (pw.length > 0) warnHtml = ' <span class="agent-warn-icon" title="' + esc(pw.join('\n')) + '">⚠️</span>';

  var sessionsLabel = t('agentDetailSessions').replace('{count}', acts);

  dp.innerHTML =
    '<div class="agent-detail-card">' +
    '<div class="agent-detail-header">' +
    '<span class="agent-detail-name">' + esc(def.name) + warnHtml + '</span>' +
    '<span class="agent-detail-id">' + esc(def.id) + '</span>' +
    '</div>' +
    '<div class="agent-detail-desc">' + esc(def.description || '') + '</div>' +
    '<div class="agent-detail-grid">' +
    '<div class="agent-detail-row">' +
    '<div class="agent-detail-field">' +
    '<label>' + t('agentDetailInstances') + '</label>' +
    '<span class="agent-detail-value">' + running + ' / ' + def.max_instances + ' (' + sessionsLabel + ')</span>' +
    '</div>' +
    (def.temperature !== undefined ? '<div class="agent-detail-field">' +
      '<label>' + t('agentDetailTemperature') + '</label>' +
      '<span class="agent-detail-value">' + def.temperature + '</span>' +
      '</div>' : '') +
    (def.max_rounds > 0 ? '<div class="agent-detail-field">' +
      '<label>' + t('agentDetailMaxRounds') + '</label>' +
      '<span class="agent-detail-value">' + def.max_rounds + '</span>' +
      '</div>' : '') +
    '</div>' +
    (subsHtml ? '<div class="agent-detail-row"><div class="agent-detail-field full-width">' +
      '<label>' + t('agentDetailSubAgents') + '</label>' +
      '<div class="agent-detail-value">' + subsHtml + '</div>' +
      '</div></div>' : '') +
    (toolTags.length > 0 ? '<div class="agent-detail-row"><div class="agent-detail-field full-width">' +
      '<label>' + t('agentDetailMcpTools') + '</label>' +
      '<div class="agent-detail-value agent-mcps">' + toolTags.join(' ') + '</div>' +
      '</div></div>' : '') +
    '</div>' +
    '<div class="agent-detail-launch">' +
    '<div class="agent-detail-launch-row">' +
    '<select class="agent-provider-select">' + modelOpts + '</select>' +
    '<select class="agent-project-select">' + modOpts + '</select>' +
    '<button class="agent-launch-btn" data-agent-id="' + esc(def.id) + '">' + t('agentLaunchBtn') + '</button>' +
    '</div>' +
    '<textarea class="agent-task-input" placeholder="' + t('agentLaunchPlaceholder') + '" rows="2"></textarea>' +
    '</div>' +
    '</div>';

  dp.style.display = '';
  bindLaunch();
  bindTaskInputKeys();
}

function bindTaskInputKeys() {
  $('#agentListView').addEventListener('keydown', function (e) {
    if (e.key === 'Enter' && e.shiftKey && e.target.classList.contains('agent-task-input')) {
      e.preventDefault();
      var card = e.target.closest('.agent-detail-card');
      if (card) { var btn = card.querySelector('.agent-launch-btn'); if (btn && !btn.disabled) btn.click(); }
    }
  });
}

function bindLaunch() {
  document.querySelectorAll('.agent-launch-btn').forEach(function (btn) {
    btn.addEventListener('click', function () {
      var agentId = btn.dataset.agentId;
      var card = btn.closest('.agent-detail-card');
      var provider = card.querySelector('.agent-provider-select');
      var task = card.querySelector('.agent-task-input');
      var project = card.querySelector('.agent-project-select');
      provider = provider ? provider.value : '';
      task = task ? task.value.trim() : '';
      project = project ? project.value : '';
      if (!provider || !task) return alert('Choose model and enter task');
      btn.disabled = true; btn.textContent = '...';
      var stream = new LLMStream({
        url: '/api/agent/chat/stream', protocol: 'custom', headers: getAuthHeaders(),
        body: { agent: agentId, provider: provider, task: task, project: project }, markdown: true,
        onSession: function (sessionId) { openChatWindow(sessionId, FindAgentName(agentId), task); currentTodoSessionId = sessionId; showThinking(); fetchAgentSessions(); },
        onChunk: function () { hideThinking(); },
        onComplete: function () { finishStream(btn); },
        onError: function (e) { finishStream(btn); if (typeof appendSysMsg === 'function') appendSysMsg(e.message || String(e)); },
        onTodos: function () { },
        onAskUser: function () { },
        onToolCall: function (data) { if (typeof createToolBlock === 'function') createToolBlock(data); },
        onBlock: function (data) { if (typeof handleBlock === 'function') handleBlock(data); },
        onApprovalRequest: function () { }
      });
      stream.start();
    });
  });
}
