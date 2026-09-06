// agent-mcp.js — MCP 服务器列表视图（双栏：左列表 + 右详情）

function renderMcpView() {
  if (!agentInfo) return;

  var leftPanel = $('#mcpLeftPanel');
  var placeholder = $('#mcpPlaceholder');
  var detailContent = $('#mcpDetailContent');
  if (!leftPanel) return;

  // 收集所有条目（内置 + 外部 MCP）
  var items = [];

  var bpl = agentInfo.builtin_providers || [];
  bpl.forEach(function (bp) {
    items.push({
      id: bp.id,
      name: bp.name,
      description: bp.description || '',
      type: 'builtin',
      toolCount: bp.tools.length,
      tools: bp.tools,
      status: 'builtin',
      statusMsg: ''
    });
  });

  var mcps = agentInfo.mcps || [];
  mcps.forEach(function (s) {
    items.push({
      id: s.id,
      name: s.name,
      description: s.description || '',
      type: s.type || 'stdio',
      toolCount: (s.tools || []).length || (s.command ? 1 : 0),
      tools: s.tools || [],
      status: s.status || '',
      statusMsg: s.status_msg || '',
      command: s.command || '',
      args: s.args || []
    });
  });

  if (items.length === 0) {
    leftPanel.innerHTML = '<div style="color:#9ca3af;padding:16px;text-align:center;">' + t('mcpNoServers') + '</div>';
    if (placeholder) placeholder.style.display = 'flex';
    if (detailContent) detailContent.style.display = 'none';
    return;
  }

  // 渲染左侧列表
  var listHTML = '';
  items.forEach(function (it) {
    var cls = 'mcp-list-item' + (it.type === 'builtin' ? ' builtin' : '');
    var badge = '';
    if (it.type === 'builtin') {
      badge = '<span class="builtin-badge">' + t('mcpBuiltin') + '</span>';
    } else {
      var sb = '';
      if (it.status === 'ok') sb = '<span class="mcp-status-badge ok">✓</span>';
      else if (it.status === 'builtin') sb = '<span class="mcp-status-badge builtin">◆</span>';
      else if (it.status === 'error') sb = '<span class="mcp-status-badge error">✗</span>';
      else sb = '<span class="mcp-status-badge pending">…</span>';
      badge = sb;
    }
    var toolsLabel = t('mcpToolsCount').replace('{count}', it.toolCount);
    var meta = '';
    if (it.type === 'builtin') {
      meta = toolsLabel;
    } else {
      meta = esc(it.type) + (it.toolCount > 0 ? ' · ' + toolsLabel : '');
    }

    listHTML +=
      '<div class="' + cls + '" data-mcp-id="' + esc(it.id) + '" onclick="selectMcpItem(\'' + esc(it.id) + '\')">' +
      '<div class="mcp-item-name">' + badge + esc(it.name) + '</div>' +
      '<div class="mcp-item-id">' + esc(it.id) + '</div>' +
      '<div class="mcp-item-meta">' + meta + '</div>' +
      '</div>';
  });

  leftPanel.innerHTML = listHTML;

  // 默认显示占位提示
  if (placeholder) placeholder.style.display = 'flex';
  if (detailContent) detailContent.style.display = 'none';

  // 清除上次选中
  window._selectedMcpId = null;
}

// selectMcpItem 点击左侧列表项后，右侧显示工具详情
function selectMcpItem(mcpId) {
  window._selectedMcpId = mcpId;

  // 高亮左侧
  var allItems = document.querySelectorAll('#mcpLeftPanel .mcp-list-item');
  allItems.forEach(function (el) {
    if (el.dataset.mcpId === mcpId) el.classList.add('selected');
    else el.classList.remove('selected');
  });

  // 查找对应数据
  var item = findMcpItemById(mcpId);
  if (!item) return;

  // 显示右侧详情
  var placeholder = $('#mcpPlaceholder');
  var detailContent = $('#mcpDetailContent');
  if (placeholder) placeholder.style.display = 'none';
  if (!detailContent) return;
  detailContent.style.display = '';

  var toolsHTML = '';
  if (item.tools && item.tools.length > 0) {
    toolsHTML = item.tools.map(function (t) {
      return '<div class="mcp-detail-tool">' +
        '<div class="tool-name">' + esc(t.name || t.id) + '</div>' +
        '<div class="tool-id">' + esc(t.id) + '</div>' +
        (t.description ? '<div class="tool-desc">' + esc(t.description) + '</div>' : '') +
        '</div>';
    }).join('');
  } else if (item.command) {
    toolsHTML = '<div class="mcp-detail-tool">' +
      '<div class="tool-id">' + esc(item.command + ' ' + (item.args || []).join(' ')) + '</div>' +
      '</div>';
  } else {
    toolsHTML = '<div style="color:#9ca3af;font-size:0.8rem;">' + t('mcpNoTools') + '</div>';
  }

  var badge = '';
  if (item.type === 'builtin') {
    badge = '<span class="builtin-badge">' + t('mcpBuiltin') + '</span> ';
  } else {
    var sb = '';
    if (item.status === 'ok') sb = '<span class="mcp-status-badge ok">' + t('mcpStatusConnected') + '</span>';
    else if (item.status === 'error') sb = '<span class="mcp-status-badge error">' + t('mcpStatusError') + '</span>';
    else sb = '<span class="mcp-status-badge pending">' + t('mcpStatusPending') + '</span>';
    badge = sb + ' ';
  }

  detailContent.innerHTML =
    '<div class="mcp-detail-header">' +
    '<div class="mcp-detail-name">' + badge + esc(item.name) + '</div>' +
    '<div class="mcp-detail-id">' + esc(item.id) + '</div>' +
    (item.description ? '<div class="mcp-detail-desc">' + esc(item.description) + '</div>' : '') +
    (item.statusMsg ? '<div class="mcp-detail-desc" style="color:#dc2626;">' + esc(item.statusMsg) + '</div>' : '') +
    '</div>' +
    '<div class="mcp-detail-tools">' + toolsHTML + '</div>';
}

// findMcpItemById 从 agentInfo 中查找条目
function findMcpItemById(mcpId) {
  if (!agentInfo) return null;
  var bpl = agentInfo.builtin_providers || [];
  for (var i = 0; i < bpl.length; i++) {
    if (bpl[i].id === mcpId) {
      return {
        id: bpl[i].id,
        name: bpl[i].name,
        description: bpl[i].description || '',
        type: 'builtin',
        toolCount: bpl[i].tools.length,
        tools: bpl[i].tools,
        status: 'builtin',
        statusMsg: ''
      };
    }
  }
  var mcps = agentInfo.mcps || [];
  for (var j = 0; j < mcps.length; j++) {
    if (mcps[j].id === mcpId) {
      var s = mcps[j];
      return {
        id: s.id,
        name: s.name,
        description: s.description || '',
        type: s.type || 'stdio',
        toolCount: (s.tools || []).length || (s.command ? 1 : 0),
        tools: s.tools || [],
        status: s.status || '',
        statusMsg: s.status_msg || '',
        command: s.command || '',
        args: s.args || []
      };
    }
  }
  return null;
}
