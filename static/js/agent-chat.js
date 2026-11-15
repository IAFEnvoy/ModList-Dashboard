// agent-chat.js — 新 SSE 协议前端
// 协议: pending / start / end / block(subtype) / todo

var currentSSE = null;        // 当前 SSE 连接
var currentSSESessionId = null;
var currentStreamTarget = null; // 当前流式渲染的目标元素
var _streamBlockId = -1;       // 当前流式块的 ID

// ========== 核心：建立 SSE 连接 ==========

function connectSSE(sessionId, resume) {
  disconnectSSE();
  currentSSESessionId = sessionId;
  // 如果处于 SSE 续接模式且已有渲染块，保留 _lastBlockId 避免漏判新块
  if (!resume || _lastBlockId < 0) _lastBlockId = -1;
  _streamBlockId = -1;
  _pendingToolGroup = null;
  _lastBlockType = '';

  currentSSE = new LLMStream({
    url: '/api/agent/chat/stream',
    protocol: 'custom',
    headers: getAuthHeaders(),
    body: { session: sessionId, resume: resume || false },
    markdown: false,
    onStreamStart: function (data) { handleStreamStart(data); },
    onContent: function (data) { handleContent(data); },
    onStreamEnd: function (data) { handleStreamEnd(data); },
    onBlock: function (data) { handleBlock(data); },
    onUpdate: function (data) { handleUpdate(data); },
    onTodos: function () {
      refreshTodoWidget();
    },
    onBypassStatus: function (enabled) {
      var check = document.getElementById('bypassApprovalCheck');
      if (check) check.checked = !!enabled;
    },
    onLimitExceeded: function (errMsg) {
      // 重放或 idle 时收到 end_limit_exceeded → 显示继续按钮
      setAgentRunning(false);
      hideThinking();
      showContinueButton();
    },
    onTitleGenerated: function (data) {
      if (!data || !data.session_id) return;
      // 更新当前聊天标题
      var ct = document.getElementById('chatTitle');
      if (ct && data.session_id === currentChatSessionId) {
        ct.textContent = data.title;
      }
      // 更新历史列表中的标题
      updateSessionTitle(data.session_id, data.title);
    },
  });
  currentSSE.start();
}

function disconnectSSE() {
  if (currentSSE) {
    try { currentSSE.stop(); } catch (e) { }
    currentSSE = null;
    currentSSESessionId = null;
  }
}

var _blockElems = {};         // block ID → DOM element 映射
var _pendingToolGroup = null; // 最后一个 tool_call 的 DOM 容器
var _lastBlockType = '';       // 追踪上一个非流式块的类型
var _approvalToGroup = {};    // approval block ID → 所属 tool group DOM（供 update 事件路由）

// ========== Block 事件处理 ==========

// handleBlock 处理非流式 block 事件
// 合并规则：
//   tool_call → 总是新建 group
//   approval_request → 前一个是 tool_call 则合并到该 group（保持 _lastBlockType=approval_request 以便 result 也能合并）
//   tool_result → 前一个是 tool_call 或（通过 approval_request 间接）tool_call 则合并；否则独立
function handleBlock(data) {
  var id = data.id, sub = data.subtype, content = data.content;

  // ID 连续性检查
  if (typeof id === 'number' && id >= 0) {
    if (_lastBlockId >= 0 && id > _lastBlockId + 1) {
      console.warn('[SSE] 漏块: last=' + _lastBlockId + ' current=' + id);
      fetchMissingBlocks(id);
    }
    if (id > _lastBlockId) _lastBlockId = id;
  }

  // 去重：已存在的块跳过
  if (typeof id === 'number' && id >= 0 && _blockElems[id]) return;

  // 按子类型创建 DOM
  switch (sub) {
    case 'tool_call':
      _pendingToolGroup = createToolGroup(content, id);
      _blockElems[id] = _pendingToolGroup;
      _lastBlockType = 'ai';
      // 立即检查后文已存在的孤块是否需要合并
      mergeAdjacentForBlock(id);
      return;
    case 'approval_request':
      if (_lastBlockType === 'ai' && _pendingToolGroup) {
        appendApprovalToGroup(_pendingToolGroup, content, id);
        _approvalToGroup[id] = _pendingToolGroup;
        _blockElems[id] = _pendingToolGroup;
        _lastBlockType = 'ai';
        return;
      }
      if (_lastBlockType !== 'ai') { _pendingToolGroup = null; _lastBlockType = 'ai'; }
      createApprovalBlock(content, id);
      _lastBlockType = 'ai';
      return;
    case 'tool_result':
      if (_lastBlockType === 'ai' && _pendingToolGroup) {
        appendResultToGroup(_pendingToolGroup, content);
        _blockElems[id] = _pendingToolGroup;
        _pendingToolGroup = null;
        _lastBlockType = 'ai';
        return;
      }
      if (_lastBlockType !== 'ai') { _pendingToolGroup = null; _lastBlockType = 'ai'; }
      createToolResultBlock(content, id);
      _lastBlockType = 'ai';
      return;
    case 'ask_user':
      _pendingToolGroup = null;
      createAskUserBlock(content, id);
      _lastBlockType = 'ai';
      return;
    case 'user':
      _lastBlockType = '';
      _pendingToolGroup = null;
      createUserBlock(id, typeof content === 'string' ? content : JSON.stringify(content));
      return;
    case 'attachment':
      _lastBlockType = '';
      _pendingToolGroup = null;
      createAttachmentBlock(id, typeof content === 'string' ? content : JSON.stringify(content));
      return;
    case 'summary':
      createCompactDoneBlock(id);
      return;
  }
}

// ========== 流式块处理（顶层事件 stream_start / content / stream_end） ==========

var _streamBlockId = -1;       // 当前流式块的 ID

function handleStreamStart(data) {
  var id = data.id, sub = data.subtype;
  // 去重：重放时 REST 已加载的块跳过
  if (typeof id === 'number' && id >= 0 && _blockElems[id]) return;

  if (sub === 'thinking') {
    // 思考块：独立的引用块样式
    _streamBlockId = -1; // thinking 不属于文本流
    currentThinkingBlock(id);
    return;
  }

  _streamBlockId = id;
  currentStreamTarget = createAIResponseSlot();
  if (typeof id === 'number' && id >= 0) _blockElems[id] = currentStreamTarget.parentElement;
}

function handleContent(data) {
  var id = data.id, sub = data.subtype, text = data.content;

  if (sub === 'thinking') {
    // 思考内容追加，thinking-text 自身随内容滚动到底
    var tEl = _blockElems[id];
    if (tEl && typeof text === 'string') {
      var tInner = tEl.querySelector('.thinking-text');
      if (tInner) {
        tInner.textContent += text;
        tInner.scrollTop = tInner.scrollHeight;
      }
      smartScroll(document.getElementById('chatMessages'));
    }
    return;
  }

  // 重放去重：非当前流式块且已存在 → 跳过
  if (typeof id === 'number' && id >= 0 && _blockElems[id] && _streamBlockId !== id) return;

  if (typeof text === 'string' && currentStreamTarget) {
    var prev = currentStreamTarget.dataset.rawText || '';
    currentStreamTarget.dataset.rawText = prev + text;
    currentStreamTarget.innerHTML = renderMarkdownSafe(currentStreamTarget.dataset.rawText);
    smartScroll(document.getElementById('chatMessages'));
  }
}

function handleStreamEnd(data) {
  var id = data.id, sub = data.subtype;

  if (sub === 'thinking') {
    // 思考流结束 → 无需操作（已经流式展示完了）
    return;
  }

  // 重放去重
  if (typeof id === 'number' && id >= 0 && _blockElems[id] && _streamBlockId !== id) return;

  currentStreamTarget = null;
  _streamBlockId = -1;
}

// 创建 thinking 块
function currentThinkingBlock(id) {
  var chat = document.getElementById('chatMessages');
  if (!chat) return;
  var wrap = document.createElement('div');
  wrap.className = 'chat-msg-wrap';
  wrap.setAttribute('data-author', 'ai');
  wrap.title = '双击查看完整思考过程';
  if (typeof id === 'number' && id >= 0) {
    wrap.id = 'block-' + id;
    _blockElems[id] = wrap;
  }
  wrap.innerHTML = '<div class="chat-msg-role">AI' + aiModelLabel() + '</div>' +
    '<div class="chat-msg thinking-block"><div class="chat-msg-content"><div class="thinking-text"></div></div></div>';
  wrap.addEventListener('dblclick', function () {
    var text = wrap.querySelector('.thinking-text');
    if (text) openThinkingModal(text.textContent || '');
  });
  chat.appendChild(wrap);
  smartScroll(chat);
}

// ========== 块更新处理（update 事件） ==========

function handleUpdate(data) {
  var id = data.id, sub = data.subtype, content = data.content;
  // 优先从审批→组映射找
  var el = _approvalToGroup[id] || _blockElems[id];
  if (!el) return;

  if (sub === 'approval_request') {
    var act = el.querySelector('.tool-approval');
    var statusEl = el.querySelector('.tool-status');
    if (act) {
      var bd = content && content.content ? content.content : content;
      var approved = bd && bd.result === true;
      act.innerHTML = '';
      if (statusEl) statusEl.innerHTML = approved ? '✅' : '❌';
    }
  } else if (sub === 'ask_user') {
    updateAskUserElement(el, content);
  }
}

// 原地更新审批块 DOM 元素
function updateApprovalElement(el, content) {
  var bd = content && content.content ? content.content : content;
  var approved = bd && bd.result === true;
  var actionsDiv = el.querySelector('.approval-actions');
  if (actionsDiv) {
    actionsDiv.innerHTML = '<span class="approval-result">' + (approved ? '✅ 已批准' : (bd && bd.result !== undefined ? '❌ 已拒绝' : '')) + '</span>';
  }
  el.classList.add('historical');
  // 禁用内部按钮
  var btns = el.querySelectorAll('button');
  for (var i = 0; i < btns.length; i++) btns[i].disabled = true;
}

// 原地更新 ask_user 块 DOM 元素
function updateAskUserElement(el, content) {
  var bd = content && content.question ? content : content;
  var answered = bd && bd.result !== undefined && bd.result !== '';
  var textEl = el.querySelector('.chat-msg-text');
  if (textEl && answered) {
    textEl.innerHTML = '<div class="ask-inline-question">' + esc(bd.question || '') + '</div>' +
      '<div class="ask-result-box">💬 ' + esc(bd.result || '') + '</div>';
  }
  el.classList.add('historical');
  var btns = el.querySelectorAll('button');
  for (var i = 0; i < btns.length; i++) btns[i].disabled = true;
}

// 检测到漏块时用 REST API 拉取补全，并在补全后进行 DOM 合并
function fetchMissingBlocks(currentId) {
  if (!currentSSESessionId) return;
  var from = _lastBlockId + 1;
  if (from >= currentId) return;
  fetch('/api/agent/sessions/' + currentSSESessionId + '/chunks?from=' + from + '&to=' + currentId, {
    headers: getAuthHeaders()
  }).then(function (r) { return r.json(); }).then(function (d) {
    if (d && d.chunks) {
      // 先渲染所有缺失的块（renderBlockFromMessage 会设置 _pendingToolGroup 等状态）
      d.chunks.forEach(function (m, i) {
        var bid = from + i;
        if (_blockElems[bid]) return;
        renderBlockFromMessage(bid, m);
        if (bid > _lastBlockId) _lastBlockId = bid;
      });
      // 补全后进行 DOM 合并：向前扫描，将孤立的 approval/tool_result 合并到前面的 tool_group
      mergeAdjoiningToolBlocks(currentId);
    }
  }).catch(function (e) { console.warn('[SSE] 补全失败:', e); });
}

// 补全后合并相邻工具块：向前扫描，将后面孤立的 approval_request / tool_result 合并到前面的 tool_group
function mergeAdjoiningToolBlocks(endId) {
  var maxId = endId;
  var keys = Object.keys(_blockElems);
  for (var k = 0; k < keys.length; k++) {
    var n = parseInt(keys[k]);
    if (n > maxId) maxId = n;
  }
  var pendingGroup = null;
  for (var bid = 0; bid <= maxId; bid++) {
    var el = _blockElems[bid];
    if (!el) continue;
    // el is now chat-msg-wrap; find inner chat-msg for class checks
    var inner = el.querySelector('.chat-msg');
    var isToolGroup = inner && inner.classList.contains('tool-group');
    var isApproval = inner && inner.classList.contains('approval-dialog');
    var isToolResult = inner && inner.classList.contains('tool-result');
    if (isToolGroup) { pendingGroup = el; continue; }
    if (isApproval && pendingGroup) {
      var summaryEl = inner.querySelector('.approval-summary');
      var resultEl = inner.querySelector('.approval-result');
      var approved = resultEl && resultEl.textContent.indexOf('已批准') >= 0;
      appendApprovalToGroup(pendingGroup, { summary: summaryEl ? summaryEl.textContent : '', result: approved }, bid);
      _approvalToGroup[bid] = pendingGroup;
      _blockElems[bid] = pendingGroup;
      el.remove();
      continue;
    }
    if (isToolResult && pendingGroup) {
      var textEl = inner.querySelector('.tool-result-body');
      if (textEl) appendResultToGroup(pendingGroup, { result: textEl.textContent });
      _blockElems[bid] = pendingGroup;
      el.remove();
      continue;
    }
    if (inner && (inner.classList.contains('user') || inner.classList.contains('assistant') || inner.classList.contains('ask-inline-msg'))) {
      pendingGroup = null;
    }
  }
}

// 单个块到达后，立即检查相邻块是否需要合并
function mergeAdjacentForBlock(blockId) {
  var groupWrap = _blockElems[blockId];
  if (!groupWrap) return;
  var group = groupWrap.querySelector('.chat-msg');
  if (!group || !group.classList.contains('tool-group')) return;
  for (var i = 0; i < 5; i++) {
    var nextId = blockId + 1 + i;
    var nextWrap = _blockElems[nextId];
    if (!nextWrap) continue;
    var next = nextWrap.querySelector('.chat-msg');
    var isApproval = next && next.classList.contains('approval-dialog');
    var isToolResult = next && next.classList.contains('tool-result');
    if (isApproval) {
      var summaryEl = next.querySelector('.approval-summary');
      var resultEl = next.querySelector('.approval-result');
      var approved = resultEl && resultEl.textContent.indexOf('已批准') >= 0;
      appendApprovalToGroup(groupWrap, { summary: summaryEl ? summaryEl.textContent : '', result: approved }, nextId);
      _approvalToGroup[nextId] = groupWrap;
      _blockElems[nextId] = groupWrap;
      nextWrap.remove();
    } else if (isToolResult) {
      var textEl = next.querySelector('.tool-result-body');
      if (textEl) appendResultToGroup(group, { result: textEl.textContent });
      _blockElems[nextId] = group;
      next.remove();
      break;  // result 是最后一个，不再继续
    } else {
      break;  // 遇到其他类型，停止
    }
  }
}

// 从 ChatMessage 对象直接渲染（REST 补全 / 初始加载用）
// 注意：SSE 重放可能先于 REST 到达，需要去重
function renderBlockFromMessage(id, m) {
  if (typeof id === 'number' && id >= 0 && _blockElems[id]) return;
  switch (m.type) {
    case 'summary':
      createCompactDoneBlock(id);
      return;
    case 'user':
      _lastBlockType = ''; _pendingToolGroup = null;
      createUserBlock(id, typeof m.content === 'string' ? m.content : JSON.stringify(m.content));
      break;
    case 'attachment':
      _lastBlockType = ''; _pendingToolGroup = null;
      createAttachmentBlock(id, typeof m.content === 'string' ? m.content : JSON.stringify(m.content));
      break;
    case 'thinking':
      _lastBlockType = ''; _pendingToolGroup = null;
      currentThinkingBlock(id);
      var tt = _blockElems[id];
      if (tt) {
        var tInner = tt.querySelector('.thinking-text');
        if (tInner) tInner.textContent = typeof m.content === 'string' ? m.content : '';
      }
      break;
    case 'message':
      _pendingToolGroup = null;
      createCompleteMessage(id, typeof m.content === 'string' ? m.content : JSON.stringify(m.content));
      _lastBlockType = 'ai';
      break;
    case 'tool_call':
    case 'tool':
      _pendingToolGroup = createToolGroup(m.content, id);
      _blockElems[id] = _pendingToolGroup;
      _lastBlockType = 'ai';
      break;
    case 'approval_request':
      if (_lastBlockType === 'ai' && _pendingToolGroup) {
        appendApprovalToGroup(_pendingToolGroup, m.content, id);
        _approvalToGroup[id] = _pendingToolGroup;
        _blockElems[id] = _pendingToolGroup;
      } else {
        createApprovalBlock(m.content, id);
      }
      _lastBlockType = 'ai';
      break;
    case 'tool_result':
      if (_lastBlockType === 'ai' && _pendingToolGroup) {
        appendResultToGroup(_pendingToolGroup, m.content);
        _blockElems[id] = _pendingToolGroup;
        _pendingToolGroup = null;
      } else {
        createToolResultBlock(m.content, id);
      }
      _lastBlockType = 'ai';
      break;
    case 'ask_user':
      _pendingToolGroup = null;
      createAskUserBlock(m.content, id);
      _lastBlockType = 'ai';
      break;
  }
}

// ========== 审批块（create = 新渲染，update = 原地更新） ==========

// createApprovalBlock: 首次渲染审批块，注册到 _blockElems
function createApprovalBlock(blockData, blockId) {
  var bd = blockData;
  // 兼容旧格式嵌套: { content: { tool, summary, ... }, cwd }
  if (bd && bd.content && bd.tool) bd = bd.content;

  // 已有 result → 直接渲染历史结果
  if (bd.result !== undefined) {
    createApprovalResultBlock(bd, blockId);
    return;
  }

  var chat = document.getElementById('chatMessages');
  if (!chat) return;

  var wrap = document.createElement('div');
  wrap.className = 'chat-msg-wrap';
  wrap.setAttribute('data-author', 'ai');
  if (typeof blockId === 'number' && blockId >= 0) {
    wrap.id = 'block-' + blockId;
    _blockElems[blockId] = wrap;
  }
  wrap.innerHTML =
    '<div class="chat-msg-role">AI' + aiModelLabel() + '</div>' +
    '<div class="chat-msg approval-dialog"><div class="chat-msg-content">' +
    '<div class="approval-summary">' + esc(bd.summary || '') + '</div>' +
    (bd.purpose ? '<div class="approval-purpose">🎯 ' + esc(bd.purpose) + '</div>' : '') +
    (bd.warnLevel ? '<div class="approval-warn">⚠️ ' + esc(String(bd.warnLevel).replace('warn:', '')) + '</div>' : '') +
    (bd.detail ? '<pre class="approval-detail">' + esc(bd.detail) + '</pre>' : '') +
    '<div class="approval-actions">' +
    '<button class="approval-btn approve">✅ 批准</button>' +
    '<button class="approval-btn deny">❌ 拒绝</button>' +
    '</div>' +
    '</div></div></div>';
  chat.appendChild(wrap);
  smartScroll(chat);

  var approveBtn = wrap.querySelector('.approve');
  var denyBtn = wrap.querySelector('.deny');

  function sendDecision(approved) {
    approveBtn.disabled = true;
    denyBtn.disabled = true;
    var sid = currentChatSessionId || currentSSESessionId;
    fetch('/api/agent/sessions/' + sid + '/blocks/' + blockId, {
      method: 'POST', headers: getAuthHeaders(),
      body: JSON.stringify({ result: approved })
    }).then(function () {
      var actionsDiv = wrap.querySelector('.approval-actions');
      if (actionsDiv) {
        actionsDiv.innerHTML = '<span class="approval-result">' + (approved ? '✅ 已批准' : '❌ 已拒绝') + '</span>';
      }
    }).catch(function () { });
  }

  approveBtn.addEventListener('click', function () { sendDecision(true); });
  denyBtn.addEventListener('click', function () { sendDecision(false); });
}

function createApprovalResultBlock(blockData, blockId) {
  var chat = document.getElementById('chatMessages');
  if (!chat) return;
  var approved = blockData.result === true;
  var wrap = document.createElement('div');
  wrap.className = 'chat-msg-wrap';
  wrap.setAttribute('data-author', 'ai');
  if (typeof blockId === 'number' && blockId >= 0) {
    wrap.id = 'block-' + blockId;
    _blockElems[blockId] = wrap;
  }
  wrap.innerHTML =
    '<div class="chat-msg-role">AI' + aiModelLabel() + '</div>' +
    '<div class="chat-msg approval-dialog historical"><div class="chat-msg-content">' +
    '<div class="approval-summary">' + esc(blockData.summary || '') + '</div>' +
    '<div class="approval-actions">' +
    '<span class="approval-result">' + (approved ? '✅ 已批准' : '❌ 已拒绝') + '</span>' +
    '</div>' +
    '</div></div></div>';
  chat.appendChild(wrap);
  smartScroll(chat);
}

// ask_user 块
function createAskUserBlock(blockData, blockId) {
  var bd = blockData;
  if (bd && bd.content && bd.question) bd = bd.content; // 兼容

  if (bd.result !== undefined && bd.result !== '') {
    createAskUserResultBlock(bd, blockId);
    return;
  }

  var chat = document.getElementById('chatMessages');
  if (!chat) return;

  var hasOptions = bd.options && bd.options.length > 0;
  var isMulti = !!bd.multiSelect;
  var hasFreeform = bd.allowFreeformInput !== false;

  var optionsHtml = '';
  if (hasOptions) {
    optionsHtml = '<div class="ask-inline-options">';
    bd.options.forEach(function (opt) {
      var label = (typeof opt === 'string') ? opt : (opt.label || opt.title || '');
      var desc = (typeof opt === 'object' && opt.description) ? '<span class="ask-opt-desc">' + esc(opt.description) + '</span>' : '';
      optionsHtml += '<label class="ask-inline-opt-label">' +
        '<input type="' + (isMulti ? 'checkbox' : 'radio') + '" name="ask_inline_opt" value="' + esc(label) + '" class="ask-inline-opt-input">' +
        '<span class="ask-inline-opt-text">' + esc(label) + '</span>' + desc + '</label>';
    });
    optionsHtml += '</div>';
  }

  var inputHtml = hasFreeform ? '<textarea class="ask-inline-input" placeholder="输入回答..." rows="2"></textarea>' : '';
  var hintText = (isMulti && hasOptions) ? '<div class="ask-inline-hint">可多选</div>' : '';

  var wrap = document.createElement('div');
  wrap.className = 'chat-msg-wrap';
  wrap.setAttribute('data-author', 'ai');
  if (typeof blockId === 'number' && blockId >= 0) {
    wrap.id = 'block-' + blockId;
    _blockElems[blockId] = wrap;
  }
  wrap.innerHTML =
    '<div class="chat-msg-role">AI' + aiModelLabel() + '</div>' +
    '<div class="chat-msg ask-inline-msg"><div class="chat-msg-content"><div class="chat-msg-text">' +
    '<div class="ask-inline-question">' + esc(bd.question || '') + '</div>' +
    hintText + optionsHtml + inputHtml +
    '<div class="ask-inline-btns">' +
    '<button class="ask-inline-cancel-btn">跳过</button>' +
    '<button class="ask-inline-confirm-btn">回答</button>' +
    '</div></div></div></div>';
  chat.appendChild(wrap);
  smartScroll(chat);

  function gatherAnswer() {
    var parts = [];
    var checked = wrap.querySelectorAll('.ask-inline-opt-input:checked');
    for (var i = 0; i < checked.length; i++) parts.push(checked[i].value);
    var inputEl = wrap.querySelector('.ask-inline-input');
    if (inputEl) { var t = inputEl.value.trim(); if (t) parts.push(t); }
    return parts.join('\n');
  }

  function submit() {
    var answer = gatherAnswer();
    var btns = wrap.querySelectorAll('button');
    for (var i = 0; i < btns.length; i++) btns[i].disabled = true;
    var inputs = wrap.querySelectorAll('input, textarea');
    for (var i = 0; i < inputs.length; i++) inputs[i].disabled = true;

    var sid = currentChatSessionId || currentSSESessionId;
    fetch('/api/agent/sessions/' + sid + '/blocks/' + blockId, {
      method: 'POST',
      headers: getAuthHeaders(),
      body: JSON.stringify({ result: answer })
    }).catch(function () { });
  }

  wrap.querySelector('.ask-inline-confirm-btn').addEventListener('click', submit);
  wrap.querySelector('.ask-inline-cancel-btn').addEventListener('click', function () {
    var btns = wrap.querySelectorAll('button');
    for (var i = 0; i < btns.length; i++) btns[i].disabled = true;
    var inputs = wrap.querySelectorAll('input, textarea');
    for (var i = 0; i < inputs.length; i++) inputs[i].disabled = true;
    var sid = currentChatSessionId || currentSSESessionId;
    fetch('/api/agent/sessions/' + sid + '/blocks/' + blockId, {
      method: 'POST',
      headers: getAuthHeaders(),
      body: JSON.stringify({ result: '' })
    }).catch(function () { });
  });
}

function createAskUserResultBlock(blockData, blockId) {
  var chat = document.getElementById('chatMessages');
  if (!chat) return;
  var answered = blockData.result !== undefined && blockData.result !== '';
  var wrap = document.createElement('div');
  wrap.className = 'chat-msg-wrap';
  wrap.setAttribute('data-author', 'ai');
  wrap.innerHTML =
    '<div class="chat-msg-role">AI' + aiModelLabel() + '</div>' +
    '<div class="chat-msg ask-inline-msg historical"><div class="chat-msg-content"><div class="chat-msg-text">' +
    '<div class="ask-inline-question">' + esc(blockData.question || '') + '</div>' +
    (answered ? '<div class="ask-result-box">💬 ' + esc(blockData.result) + '</div>' : '<div class="ask-result-box" style="color:#9ca3af;">(未回答)</div>') +
    '</div></div></div>';
  chat.appendChild(wrap);
  smartScroll(chat);
}

// ========== 块渲染辅助 ==========
// 统一结构: chat-msg-wrap[data-author] > chat-msg-role + chat-msg > chat-msg-content
// CSS 用相邻兄弟隐藏连续同作者块的 chat-msg-role

// aiModelLabel 返回当前模型名的 HTML 片段
function aiModelLabel() {
  var mn = getCurrentModelName();
  return mn ? ' <span class="chat-model-label">' + esc(mn) + '</span>' : '';
}

function createAIResponseSlot() {
  var chat = document.getElementById('chatMessages');
  if (!chat) return null;
  var wrap = document.createElement('div');
  wrap.className = 'chat-msg-wrap';
  wrap.setAttribute('data-author', 'ai');
  wrap.innerHTML = '<div class="chat-msg-role">AI' + aiModelLabel() + '</div>' +
    '<div class="chat-msg assistant"><div class="chat-msg-content"><div class="chat-msg-text"></div></div></div>';
  chat.appendChild(wrap);
  smartScroll(chat);
  return wrap.querySelector('.chat-msg-text');
}

function createCompleteMessage(id, content, mn) {
  var chat = document.getElementById('chatMessages');
  if (!chat) return;
  var wrap = document.createElement('div');
  wrap.className = 'chat-msg-wrap';
  wrap.setAttribute('data-author', 'ai');
  if (typeof id === 'number' && id >= 0) { wrap.id = 'block-' + id; _blockElems[id] = wrap; }
  wrap.innerHTML = '<div class="chat-msg-role">AI' + aiModelLabel() + '</div>' +
    '<div class="chat-msg assistant"><div class="chat-msg-content"><div class="chat-msg-text">' + renderMarkdownSafe(content) + '</div></div></div>';
  chat.appendChild(wrap);
  smartScroll(chat);
}

function createUserBlock(id, text) {
  var chat = document.getElementById('chatMessages');
  if (!chat) return;
  var wrap = document.createElement('div');
  wrap.className = 'chat-msg-wrap';
  wrap.setAttribute('data-author', 'user');
  if (typeof id === 'number' && id >= 0) { wrap.id = 'block-' + id; _blockElems[id] = wrap; }
  wrap.innerHTML = '<div class="chat-msg-role">You</div>' +
    '<div class="chat-msg user"><div class="chat-msg-content"><div class="chat-msg-text">' + esc(text || '') + '</div></div></div>';
  chat.appendChild(wrap);
  smartScroll(chat);
}

function createAttachmentBlock(id, text) {
  var chat = document.getElementById('chatMessages');
  if (!chat) return;
  var last = chat.lastElementChild;
  if (last && last.getAttribute('data-author') === 'user') {
    var det = document.createElement('details');
    det.className = 'chat-attachment';
    det.innerHTML = '<summary>📎 附件</summary><pre class="chat-attachment-body">' + esc((text || '').substring(0, 2000)) + '</pre>';
    last.querySelector('.chat-msg-content').appendChild(det);
    if (typeof id === 'number' && id >= 0) _blockElems[id] = last;
  } else {
    var wrap = document.createElement('div');
    wrap.className = 'chat-msg-wrap';
    wrap.setAttribute('data-author', 'user');
    if (typeof id === 'number' && id >= 0) { wrap.id = 'block-' + id; _blockElems[id] = wrap; }
    wrap.innerHTML = '<div class="chat-msg-role">You</div>' +
      '<div class="chat-msg user"><div class="chat-msg-content"><details class="chat-attachment"><summary>📎 附件</summary><pre class="chat-attachment-body">' + esc((text || '').substring(0, 2000)) + '</pre></details></div></div>';
    chat.appendChild(wrap);
  }
  smartScroll(chat);
}

// 压缩完成提示块
function createCompactDoneBlock(id) {
  var chat = document.getElementById('chatMessages');
  if (!chat) return;
  var wrap = document.createElement('div');
  wrap.className = 'chat-msg-wrap';
  wrap.setAttribute('data-author', 'ai');
  if (typeof id === 'number' && id >= 0) { wrap.id = 'block-' + id; _blockElems[id] = wrap; }
  wrap.innerHTML = '<div class="chat-msg-role">AI' + aiModelLabel() + '</div>' +
    '<div class="chat-msg compact-done"><div class="chat-msg-content"><div class="compact-done-text">📦 已完成压缩</div></div></div>';
  chat.appendChild(wrap);
  smartScroll(chat);
  _lastBlockType = 'ai';
}

// ========== 工具组（tool_call + 可选 approval + tool_result 合并为一个 DOM 块） ==========

function createToolGroup(data, id) {
  var chat = document.getElementById('chatMessages');
  if (!chat) return null;
  var tlabel = ''; var tbrief = ''; var tfull = ''; var tTranslated = '';
  if (typeof data === 'object' && data) {
    tlabel = esc(data.label || data.name || '');
    if (data.arguments) {
      try {
        var a = typeof data.arguments === 'string' ? JSON.parse(data.arguments) : data.arguments;
        tbrief = formatToolArgsBrief(a);
        tfull = JSON.stringify(a, null, 2);
        // 尝试获取翻译（若无翻译键则返回空字符串）
        tTranslated = translateToolCall(data.name, a);
      } catch (e) { tbrief = String(data.arguments).substring(0, 60); }
    }
  } else { tlabel = esc(String(data || '')); }
  var wrap = document.createElement('div');
  wrap.className = 'chat-msg-wrap';
  wrap.setAttribute('data-author', 'ai');
  if (typeof id === 'number' && id >= 0) wrap.id = 'block-' + id;
  var contentDiv = document.createElement('div');
  contentDiv.className = 'chat-msg-content';
  var inner = document.createElement('div');
  inner.className = 'chat-msg tool-group';
  inner._expanded = false;

  wrap.innerHTML = '<div class="chat-msg-role">AI' + aiModelLabel() + '</div>';
  var head = document.createElement('div');
  head.className = 'tool-head';
  head.innerHTML = '<span class="tool-icon">🔧</span>' +
    '<span class="tool-name">' + (tTranslated || tlabel) + '</span>' +
    (tbrief && !tTranslated ? '<span class="tool-brief">' + esc(tbrief) + '</span>' : '') +
    '<span class="tool-status"></span>';
  head.addEventListener('click', function (e) {
    if (e.target.tagName === 'BUTTON') return;
    inner._expanded = !inner._expanded;
    inner.querySelector('.tool-body').style.display = inner._expanded ? '' : 'none';
    inner.classList.toggle('expanded', inner._expanded);
  });
  contentDiv.appendChild(head);

  // 展开体：args pre → approval → result pre
  var body = document.createElement('div');
  body.className = 'tool-body';
  body.style.display = 'none';
  if (tfull) body.innerHTML = '<pre class="tool-args">' + esc(tfull) + '</pre>';
  else body.innerHTML = '';
  body.appendChild(document.createElement('div')).className = 'tool-approval';
  body.appendChild(document.createElement('div')).className = 'tool-result';
  contentDiv.appendChild(body);

  inner.appendChild(contentDiv);
  wrap.appendChild(inner);

  chat.appendChild(wrap);
  smartScroll(chat);
  return wrap;
}

function appendApprovalToGroup(group, blockData, approvalId) {
  var bd = blockData;
  if (bd && bd.content && bd.tool) bd = bd.content;
  var approved = bd.result !== undefined ? bd.result : undefined;
  var act = group.querySelector('.tool-approval');
  var statusEl = group.querySelector('.tool-status');
  if (!act) return;
  if (approved === true) {
    act.innerHTML = '';
    if (statusEl) statusEl.innerHTML = '✅';
  } else if (approved === false) {
    act.innerHTML = '';
    if (statusEl) statusEl.innerHTML = '❌';
  } else {
    act.innerHTML = '<div class="approval-detail-text">' + esc(bd.summary || '') + '</div>' +
      (bd.detail ? '<pre class="approval-detail-pre">' + esc(bd.detail) + '</pre>' : '') +
      '<div class="approval-actions"><button class="approval-btn approve">✅ 批准</button><button class="approval-btn deny">❌ 拒绝</button></div>';
    if (statusEl) statusEl.innerHTML = '⏳';
    var approveBtn = act.querySelector('.approve');
    var denyBtn = act.querySelector('.deny');
    function sendDecision(approved) {
      if (approveBtn) approveBtn.disabled = true;
      if (denyBtn) denyBtn.disabled = true;
      var sid = currentChatSessionId || currentSSESessionId;
      fetch('/api/agent/sessions/' + sid + '/blocks/' + approvalId, {
        method: 'POST', headers: getAuthHeaders(),
        body: JSON.stringify({ result: approved })
      }).catch(function () { });
    }
    if (approveBtn) approveBtn.addEventListener('click', function () { sendDecision(true); });
    if (denyBtn) denyBtn.addEventListener('click', function () { sendDecision(false); });
    // 有审批时自动展开
    group._expanded = true;
    group.querySelector('.tool-body').style.display = '';
    group.classList.add('expanded');
    var chat = document.getElementById('chatMessages');
    if (chat) smartScroll(chat);
  }
}

function appendResultToGroup(group, data) {
  var res = group.querySelector('.tool-result');
  var statusEl = group.querySelector('.tool-status');
  if (!res) return;
  var text = '';
  var toolName = '';
  if (typeof data === 'object' && data) {
    text = esc((data.result || '').substring(0, 2000));
    toolName = data.name || '';
  }
  res.innerHTML = '<pre class="tool-result-pre">' + text + '</pre>';
  if (statusEl) statusEl.innerHTML = statusEl.textContent || '📋';

  // 搜索类工具：在名称后追加结果数量
  if (/^(file_search|grep_search|semantic_search|list_files)$/.test(toolName) && text) {
    var count = countResults(text, toolName);
    if (count >= 0) {
      var nameEl = group.querySelector('.tool-name');
      if (nameEl) {
        nameEl.textContent = (nameEl.textContent || '').replace(/\s*\(\d+ 条结果\)\s*$/, '').replace(/\s*\(\d+ results?\)\s*$/i, '') + ' (' + count + ' ' + t('toolResults') + ')';
      }
    }
  }
}

function countResults(text, toolName) {
  if (!text || text === '(no matches)' || text.indexOf('(no matches)') === 0) return 0;
  // 按行拆分，过滤空行和截断提示
  var lines = text.split('\n').filter(function (l) {
    var t = l.trim();
    return t && !/^\.\.\.\s*\(/.test(t); // 排除 "... (N more)" 截断行
  });
  return lines.length;
}

// 兼容旧调用：非合并模式（用于孤立的 tool_result，理论上不会发生但保留备用）
function createToolBlock(data, id) {
  var g = createToolGroup(data, id);
  if (g && typeof id === 'number' && id >= 0) _blockElems[id] = g;
}
function createToolResultBlock(data, id) {
  var chat = document.getElementById('chatMessages');
  if (!chat) return;
  var name = ''; var result = '';
  if (typeof data === 'object' && data) {
    name = esc(data.name || '');
    result = esc((data.result || '').substring(0, 2000));
  }
  var wrap = document.createElement('div');
  wrap.className = 'chat-msg-wrap';
  wrap.setAttribute('data-author', 'ai');
  if (typeof id === 'number' && id >= 0) { wrap.id = 'block-' + id; _blockElems[id] = wrap; }
  wrap.innerHTML = '<div class="chat-msg-role">AI' + aiModelLabel() + '</div>' +
    '<div class="chat-msg tool-result"><div class="chat-msg-content"><details><summary>🔧 <span class="tool-result-name">' + name + '</span></summary>' +
    '<pre class="tool-result-body">' + result + '</pre></details></div></div>';
  chat.appendChild(wrap);
  smartScroll(chat);
}

function appendSysMsg(text) {
  var chat = document.getElementById('chatMessages');
  if (!chat) return;
  var wrap = document.createElement('div');
  wrap.className = 'chat-msg-wrap';
  wrap.setAttribute('data-author', 'ai');
  wrap.innerHTML = '<div class="chat-msg-role">AI' + aiModelLabel() + '</div>' +
    '<div class="chat-msg assistant"><div class="chat-msg-content"><div class="chat-msg-text">' + esc(text) + '</div></div></div>';
  chat.appendChild(wrap);
  smartScroll(chat);
}

function formatToolArgsBrief(argObj) {
  if (!argObj || typeof argObj !== 'object') return '';
  var keys = Object.keys(argObj);
  if (keys.length === 0) return '';
  if (keys.length === 1) {
    var v = argObj[keys[0]];
    if (Array.isArray(v)) return keys[0] + '[' + v.length + '条]';
    if (typeof v === 'object' && v) return keys[0];
    return keys[0] + ': ' + String(v).substring(0, 40);
  }
  var parts = [];
  keys.forEach(function (k) {
    var v = argObj[k];
    if (typeof v === 'string') parts.push(k + ': ' + (v.length > 20 ? v.substring(0, 17) + '...' : v));
    else if (typeof v === 'number' || typeof v === 'boolean') parts.push(k + ': ' + v);
    else if (Array.isArray(v)) parts.push(k + '[' + v.length + '条]');
  });
  return parts.join(', ');
}

// ========== 会话窗口 ==========

var sessionPollTimer = null;
var historyArchivedMode = false;
function stopSessionPoll() {
  if (sessionPollTimer) { clearInterval(sessionPollTimer); sessionPollTimer = null; }
}

function switchAgentTab(view) {
  state.currentAgentTab = view;
  document.querySelectorAll('.agent-tab').forEach(function (t) {
    t.classList.toggle('active', t.dataset.agentView === view);
  });
  var cv = document.getElementById('agentChatView');
  var lv = document.getElementById('agentListView');
  var mv = document.getElementById('agentMcpView');
  var sv = document.getElementById('agentSettingsView');

  // chat 视图：有对话显示聊天面板，无对话显示历史面板
  if (view === 'chat') {
    if (cv) cv.style.display = 'block';
    var hp = document.getElementById('agentHistoryPanel');
    var cp = document.getElementById('agentChatPanel');
    if (currentChatSessionId) {
      if (hp) hp.style.display = 'none';
      if (cp) cp.style.display = '';
    } else {
      if (hp) hp.style.display = '';
      if (cp) cp.style.display = 'none';
    }
    updateChatHistory();
  } else {
    if (cv) cv.style.display = 'none';
  }

  if (lv) lv.style.display = view === 'agents' ? '' : 'none';
  if (mv) mv.style.display = view === 'mcps' ? '' : 'none';
  if (sv) sv.style.display = view === 'settings' && state.loggedIn ? '' : 'none';

  if (view === 'agents') { fetchAgentSessions(true).then(function () { if (typeof renderAgentListView === 'function') renderAgentListView(); }); }
  else if (view === 'mcps') { if (typeof renderMcpView === 'function') renderMcpView(); }
  saveHash();
}

function toggleArchivedHistory() {
  historyArchivedMode = !historyArchivedMode;
  var title = document.getElementById('historyPanelTitle');
  var toggle = document.getElementById('historyArchiveToggle');
  if (title) title.textContent = historyArchivedMode ? '归档对话' : '对话历史';
  if (toggle) toggle.textContent = historyArchivedMode ? '返回对话' : '已归档';
  var input = document.getElementById('historySearchInput');
  if (input) input.value = '';
  fetchAgentSessions();
}

// 更新会话标题（由 title_generated 事件触发）
function updateSessionTitle(sessionId, title) {
  if (!agentSessions || !title) return;
  for (var i = 0; i < agentSessions.length; i++) {
    if (agentSessions[i].id === sessionId) {
      agentSessions[i].title = title;
      break;
    }
  }
  // 刷新历史列表
  updateChatHistory();
}

var _historySearchTimer = null;

function onHistorySearch() {
  if (_historySearchTimer) clearTimeout(_historySearchTimer);
  _historySearchTimer = setTimeout(function () {
    var input = document.getElementById('historySearchInput');
    var q = input ? input.value.trim() : '';
    if (q) {
      doHistorySearch(q);
    } else {
      updateChatHistory();
    }
  }, 250);
}

function doHistorySearch(q) {
  var el = document.getElementById('agentHistoryList');
  if (!el) return;
  el.innerHTML = '<loading-indicator></loading-indicator>';

  fetch('/api/agent/search/sessions?q=' + encodeURIComponent(q) + '&limit=30', {
    headers: getAuthHeaders()
  }).then(function (r) {
    if (!r.ok) throw new Error('HTTP ' + r.status);
    return r.json();
  }).then(function (data) {
    var visibleIds = {};
    (agentSessions || []).forEach(function (s) { visibleIds[s.id] = true; });
    data = (data || []).filter(function (r) { return visibleIds[r.session_id]; });
    if (!data || data.length === 0) {
      el.innerHTML = '<div style="color:#9ca3af;padding:16px;text-align:center;">' + (t('historySearchNoMatch') || '无匹配的对话') + '</div>';
      return;
    }
    el.innerHTML = data.map(function (r) {
      var agentName = (typeof FindAgentName === 'function') ? FindAgentName(r.agent) : (r.agent || '');
      var matchLabel = '';
      switch (r.match_field) {
        case 'title': matchLabel = '📝 标题匹配'; break;
        case 'agent': matchLabel = '🤖 Agent匹配'; break;
        case 'project': matchLabel = '📂 项目匹配'; break;
        case 'message': matchLabel = '💬 消息匹配'; break;
        default: matchLabel = '';
      }
      return '<div class="history-card" onclick="openChatWindow(\'' + esc(r.session_id) + '\',\'' + esc(agentName) + '\')">' +
        '<div class="history-card-header">' +
        '<span class="history-agent">' + esc(agentName) + '</span>' +
        (r.project ? '<span class="history-project">' + esc(r.project) + '</span>' : '') +
        '<div class="history-card-actions">' +
        (historyArchivedMode ? '<button class="history-card-restore" onclick="event.stopPropagation();restoreSession(\'' + esc(r.session_id) + '\')">恢复</button>' : '') +
        '<button class="history-card-del" onclick="event.stopPropagation();deleteSession(\'' + esc(r.session_id) + '\')">' + (historyArchivedMode ? '永久删除' : '归档') + '</button>' +
        '</div>' +
        '</div>' +
        (r.title ? '<div class="history-title">' + esc(r.title) + '</div>' : '') +
        '<div class="history-preview">' + renderHighlightSafe(r.match_snippet || '') + '</div>' +
        '<div class="history-search-label">' + matchLabel + '</div>' +
        '<div class="history-time">' + esc(r.updated_at || '') + ' · ' + (r.message_count || 0) + ' 条消息</div>' +
        '</div>';
    }).join('');
  }).catch(function (e) {
    el.innerHTML = '<div style="color:#9ca3af;padding:16px;text-align:center;">搜索失败: ' + esc(e.message) + '</div>';
  });
}

function updateChatHistory() {
  var el = document.getElementById('agentHistoryList');
  if (!el) return;
  var sessions = (agentSessions || []).slice().sort(function (a, b) {
    return (b.updated_at || b.created_at || '').localeCompare(a.updated_at || a.created_at || '');
  });

  if (sessions.length === 0) {
    el.innerHTML = '<div style="color:#9ca3af;padding:16px;text-align:center;">暂无对话历史</div>';
    return;
  }
  el.innerHTML = sessions.map(function (s) {
    var msgs = s.messages || [];
    var lastMsg = '';
    for (var i = msgs.length - 1; i >= 0; i--) {
      if (msgs[i].type === 'system' || msgs[i].type === 'summary' || msgs[i].type === 'tool_call' || msgs[i].type === 'tool' || msgs[i].type === 'tool_result') continue;
      lastMsg = typeof msgs[i].content === 'string' ? msgs[i].content : JSON.stringify(msgs[i].content);
      break;
    }
    var preview = lastMsg ? lastMsg.substring(0, 120).replace(/\n/g, ' ') : '(空)';
    var agentName = (typeof FindAgentName === 'function') ? FindAgentName(s.agent) : (s.agent || '');
    var title = s.title || '';
    // Token 用量条
    var usageBar = '';
    var cu = s.context_usage || s.ContextUsage;
    if (cu && cu.usage_percent > 0) {
      var pct = Math.min(100, Math.round(cu.usage_percent));
      var cls = pct > 80 ? 'high' : pct > 50 ? 'mid' : 'low';
      usageBar = '<div class="history-usage"><div class="history-usage-bar ' + cls + '" style="width:' + pct + '%"></div></div>' +
        '<div class="history-usage-label">' + fmtTok(cu.total_tokens) + ' / ' + fmtTok(cu.max_tokens) + ' (' + pct + '%)</div>';
    }
    return '<div class="history-card" onclick="openChatWindow(\'' + s.id + '\',\'' + esc(agentName) + '\')">' +
      '<div class="history-card-header">' +
      '<span class="history-agent">' + esc(agentName) + '</span>' +
      (s.project ? '<span class="history-project">' + esc(s.project) + '</span>' : '') +
      '<div class="history-card-actions">' +
      (historyArchivedMode ? '<button class="history-card-restore" onclick="event.stopPropagation();restoreSession(\'' + s.id + '\')">恢复</button>' : '') +
      '<button class="history-card-del" onclick="event.stopPropagation();deleteSession(\'' + s.id + '\')">' + (historyArchivedMode ? '永久删除' : '归档') + '</button>' +
      '</div>' +
      '</div>' +
      (title ? '<div class="history-title">' + esc(title) + '</div>' : '') +
      '<div class="history-preview">' + esc(preview) + '</div>' +
      usageBar +
      '<div class="history-time">' + esc(s.updated_at || s.created_at || '') + '</div>' +
      '</div>';
  }).join('');
}

// ========== 惰性加载 ==========

function loadSessionMeta(sessionId) {
  fetchAgentSessions().then(function () {
    var s = (agentSessions || []).find(function (x) { return x.id === sessionId; });
    if (!s) return;
    var bypassCheck = document.getElementById('bypassApprovalCheck');
    if (bypassCheck) bypassCheck.checked = !!s.bypass_approval;
    var issueBtn = document.getElementById('chatIssueBtn');
    if (issueBtn) issueBtn.style.display = getSessionGhRepo() ? '' : 'none';
    var driveBtn = document.getElementById('chatDriveBtn');
    if (driveBtn) driveBtn.style.display = state.loggedIn ? '' : 'none';
    if (s.running) { showThinking(); setAgentRunning(true); }
    if (s.limit_exceeded) { showContinueButton(); }
    updateContextUsageBar(sessionId);
  });
}

function showLoadMoreButton(sessionId, firstId, batchSize) {
  var chat = document.getElementById('chatMessages');
  if (!chat || chat.querySelector('.load-more-btn')) return;
  var btn = document.createElement('button');
  btn.className = 'load-more-btn';
  btn.textContent = '▲ 加载更早的消息';
  btn.onclick = function () { loadEarlierMessages(sessionId, firstId, batchSize); };
  chat.insertBefore(btn, chat.firstChild);
}

function loadEarlierMessages(sessionId, currentFirstId, batchSize) {
  var btn = document.querySelector('.load-more-btn');
  if (btn) { btn.disabled = true; btn.textContent = '加载中...'; }
  var to = currentFirstId;
  var from = Math.max(0, to - batchSize);
  var chat = document.getElementById('chatMessages');
  var oldHeight = chat ? chat.scrollHeight : 0;
  // 记住按钮后的第一个旧块，用于加载后修复标签
  var firstOld = btn ? btn.nextSibling : null;
  while (firstOld && firstOld.nodeType !== 1) firstOld = firstOld.nextSibling;

  fetch('/api/agent/sessions/' + sessionId + '/chunks?from=' + from + '&to=' + to, { headers: getAuthHeaders() })
    .then(function (r) { return r.json(); })
    .then(function (data) {
      if (currentChatSessionId !== sessionId) return;
      var chunks = data.chunks || [];
      _lastBlockType = '';
      _pendingToolGroup = null;
      var newElems = [];
      for (var i = 0; i < chunks.length; i++) {
        var el = renderBlockFromMessageRaw(from + i, chunks[i]);
        if (el) newElems.push(el);
      }
      for (var j = 0; j < newElems.length; j++) {
        if (btn && btn.parentNode) {
          btn.parentNode.insertBefore(newElems[j], btn);
        } else if (chat) {
          chat.appendChild(newElems[j]);
        }
        if (from + j > _lastBlockId) _lastBlockId = from + j;
      }
      if (btn) btn.remove();
      // 清理新旧交界处的空白文本节点（确保 CSS + 相邻选择器生效）
      if (firstOld && firstOld.parentNode && newElems.length > 0) {
        var lastNew = newElems[newElems.length - 1];
        var n = lastNew.nextSibling;
        while (n && n !== firstOld) {
          var nxt = n.nextSibling;
          if (n.nodeType === 3 /* TEXT */) n.remove();
          n = nxt;
        }
        // 同作者 → 隐藏旧块标签
        if (lastNew.getAttribute('data-author') === firstOld.getAttribute('data-author')) {
          var oldRole = firstOld.querySelector('.chat-msg-role');
          if (oldRole) oldRole.style.display = 'none';
        }
      }
      if (chat) {
        var newHeight = chat.scrollHeight;
        chat.scrollTop += newHeight - oldHeight;
      }
      if (data.from > 0) {
        showLoadMoreButton(sessionId, data.from, batchSize);
      }
    }).catch(function (e) {
      if (btn) { btn.disabled = false; btn.textContent = '▲ 加载更早的消息 (失败)'; }
    });
}

// 渲染消息块并返回 DOM 元素（不自动 appendChild，由调用方决定位置）
function renderBlockFromMessageRaw(id, m) {
  if (typeof id === 'number' && id >= 0 && _blockElems[id]) return _blockElems[id];
  var chat = document.getElementById('chatMessages');
  // 保存插入前的 childNodes 数量
  var before = chat ? chat.childNodes.length : 0;

  renderBlockFromMessage(id, m);

  // 返回新插入的元素
  if (chat && chat.childNodes.length > before) {
    var el = chat.childNodes[before]; // 第一个新插入的
    return el;
  }
  // 有些类型不插入 DOM（summary 等），返回 _blockElems 的占位
  if (typeof id === 'number' && id >= 0) return _blockElems[id];
  return null;
}

// fetchMissingTail 补齐初次加载后末尾缺失的消息（?last=N 可能没覆盖到最新消息）
function fetchMissingTail(sessionId, fromSeq, totalSeq) {
  var chat = document.getElementById('chatMessages');
  fetch('/api/agent/sessions/' + sessionId + '/chunks?from=' + fromSeq + '&to=' + totalSeq, { headers: getAuthHeaders() })
    .then(function (r) { return r.json(); })
    .then(function (data) {
      if (currentChatSessionId !== sessionId) return;
      var chunks = data.chunks || [];
      if (chunks.length === 0) return;
      // 从当前最后一个块之后追加（最末尾可能有加载更多按钮，跳过）
      for (var i = 0; i < chunks.length; i++) {
        var el = renderBlockFromMessageRaw(fromSeq + i, chunks[i]);
        if (el) chat.appendChild(el);
        if (fromSeq + i > _lastBlockId) _lastBlockId = fromSeq + i;
      }
      if (!_lastBlockId || _lastBlockId < fromSeq + chunks.length - 1) {
        _lastBlockId = fromSeq + chunks.length - 1;
      }
      requestAnimationFrame(function () {
        requestAnimationFrame(function () {
          setTimeout(function () {
            chat.scrollTop = chat.scrollHeight;
          }, 50);
        });
      });
    }).catch(function (e) { console.warn('[Chat] 补拉末尾消息失败:', e); });
}

function editChatTitle(sessionId) {
  var ct = document.getElementById('chatTitle');
  if (!ct) return;
  var old = ct.textContent;
  var inp = document.createElement('input');
  inp.type = 'text'; inp.value = old; inp.className = 'chat-title-input';
  inp.style.cssText = 'font-weight:600;font-size:1rem;flex:1;border:1px solid var(--accent);border-radius:6px;padding:2px 8px;outline:none;background:var(--card-bg);color:var(--text);';
  ct.replaceWith(inp);
  inp.focus(); inp.select();
  inp.addEventListener('blur', function () {
    var newTitle = inp.value.trim() || old;
    var span = document.createElement('span');
    span.id = 'chatTitle'; span.className = 'chat-title'; span.textContent = newTitle;
    span.title = '点击编辑标题';
    span.onclick = function () { editChatTitle(sessionId); };
    inp.replaceWith(span);
  });
  inp.addEventListener('keydown', function (e) { if (e.key === 'Enter') { inp.blur(); } });
}

function openChatWindow(sessionId, agentName, preloadMessages, preloadProject, preloadBypass) {
  stopSessionPoll();
  currentChatSessionId = sessionId;
  currentTodoSessionId = sessionId;

  var hp = document.getElementById('agentHistoryPanel');
  var cp = document.getElementById('agentChatPanel');
  if (hp) hp.style.display = 'none';
  if (cp) cp.style.display = '';

  var s = (agentSessions || []).find(function (x) { return x.id === sessionId; }) || {};

  var ct = document.getElementById('chatTitle');
  if (ct) {
    ct.textContent = s.title || agentName || (typeof FindAgentName === 'function' ? FindAgentName(s.agent) : 'Agent');
    ct.onclick = function () { editChatTitle(sessionId); };
  }
  var cproj = document.getElementById('chatProject');
  if (cproj) cproj.textContent = (preloadProject || s.project) ? '[' + (preloadProject || s.project) + ']' : '';

  // sessionId 为 null 时仅切换面板（detail.js 派发新 Issue），不加载历史不连 SSE
  if (!sessionId) {
    ensureChatEventsBound();
    switchAgentTab('chat');
    return;
  }

  var ms = document.getElementById('chatModelSelect');
  if (ms && typeof agentInfo !== 'undefined' && agentInfo) {
    ms.style.display = '';
    var models = agentInfo.models || [];
    ms.innerHTML = models.map(function (m) {
      return '<option value="' + esc(m.id) + '"' + (m.id === s.provider ? ' selected' : '') + '>(' + esc(m.provider_name || '') + ') ' + esc(m.name) + '</option>';
    }).join('');
  }

  // 清除旧聊天
  var chat = document.getElementById('chatMessages');
  if (chat) chat.innerHTML = '';
  hideChatLoading();
  _lastBlockId = -1;
  _blockElems = {};
  _pendingToolGroup = null;
  _lastBlockType = '';
  _approvalToGroup = {};

  // 如果有预加载消息（来自 detail.js 的 REST POST 响应），直接渲染，避免异步 REST GET 竞态
  if (preloadMessages && preloadMessages.length > 0) {
    ensureChatEventsBound();
    switchAgentTab('chat');
    preloadMessages.forEach(function (m, i) {
      renderBlockFromMessage(i, m);
      _lastBlockId = i;
    });
    refreshTodoWidget();
    updateContextUsageBar(sessionId);
    requestAnimationFrame(function () {
      requestAnimationFrame(function () {
        setTimeout(function () {
          if (_pendingScrollSid === sessionId && _pendingScrollSeq >= 0) {
            scrollToBlock(_pendingScrollSeq);
            _pendingScrollSeq = -1;
            _pendingScrollSid = null;
          } else if (chat) {
            chat.scrollTop = chat.scrollHeight;
          }
        }, 50);
      });
    });
    var bypassCheck = document.getElementById('bypassApprovalCheck');
    if (bypassCheck) bypassCheck.checked = !!preloadBypass;
    var issueBtn = document.getElementById('chatIssueBtn');
    if (issueBtn) issueBtn.style.display = getSessionGhRepo() ? '' : 'none';
    var driveBtn = document.getElementById('chatDriveBtn');
    if (driveBtn) driveBtn.style.display = state.loggedIn ? '' : 'none';
    // 预加载路径没有 context_usage，异步拉取后刷新
    if (typeof fetchAgentSessions === 'function') {
      fetchAgentSessions().then(function () { updateContextUsageBar(sessionId); });
    }
  } else {
    // 惰性加载：先加载最近 30 条消息，其余按需加载。
    // 如果从搜索结果导航过来，则加载目标消息附近的窗口。
    // 注意：total <= 200 时一次性全量加载，避免 SSE 重放打乱顺序。
    showChatLoading();
    var BATCH_SIZE = 30;
    var targetSeq = (_pendingScrollSid === sessionId) ? _pendingScrollSeq : -1;
    var loadUrl;
    if (targetSeq >= 0) {
      var halfWindow = 15;
      var fromSeq = Math.max(0, targetSeq - halfWindow);
      var toSeq = targetSeq + halfWindow;
      loadUrl = '/api/agent/sessions/' + sessionId + '/chunks?from=' + fromSeq + '&to=' + toSeq;
    } else {
      // 先探一下总数，200 条以内直接全量加载
      loadUrl = '/api/agent/sessions/' + sessionId + '/chunks?last=' + BATCH_SIZE;
    }
    fetch(loadUrl, { headers: getAuthHeaders() })
      .then(function (r) { return r.json(); })
      .then(function (data) {
        if (currentChatSessionId !== sessionId) return;
        var total = data.total || 0;
        var firstId = data.from || 0;
        var chunks = data.chunks || [];

        // 如果总数 <= 200 且没从搜索跳过来，改全量加载
        if (total <= 200 && total > BATCH_SIZE && targetSeq < 0) {
          return fetch('/api/agent/sessions/' + sessionId + '/chunks?from=0&to=' + total, { headers: getAuthHeaders() })
            .then(function (r2) { return r2.json(); })
            .then(function (data2) {
              if (currentChatSessionId !== sessionId) return;
              total = data2.total;
              firstId = 0;
              chunks = data2.chunks || [];
              renderInitialMessages(sessionId, total, firstId, chunks);
            });
        }
        renderInitialMessages(sessionId, total, firstId, chunks);
      }).catch(function (e) {
        console.warn('[Chat] 加载消息失败，使用 fallback:', e);
        hideChatLoading();
        var ss = (agentSessions || []).find(function (x) { return x.id === sessionId; }) || {};
        var msgs = (ss.messages && ss.messages.length > 0) ? ss.messages : [];
        msgs.forEach(function (m, i) { renderBlockFromMessage(i, m); _lastBlockId = i; });
        if (msgs.length > 0) {
          requestAnimationFrame(function () {
            requestAnimationFrame(function () {
              setTimeout(function () {
                var chat3 = document.getElementById('chatMessages');
                if (_pendingScrollSid === sessionId && _pendingScrollSeq >= 0) {
                  scrollToBlock(_pendingScrollSeq);
                  _pendingScrollSeq = -1;
                  _pendingScrollSid = null;
                } else if (chat3) {
                  chat3.scrollTop = chat3.scrollHeight;
                }
              }, 50);
            });
          });
        }
        if (!currentSSE) connectSSE(sessionId, true);
        loadSessionMeta(sessionId);
      });
  }

  ensureChatEventsBound();
  switchAgentTab('chat');
}

function renderInitialMessages(sessionId, total, firstId, chunks) {
  var chat = document.getElementById('chatMessages');

  ensureChatEventsBound();
  switchAgentTab('chat');
}

// renderInitialMessages 渲染初始加载的消息列表
function renderInitialMessages(sessionId, total, firstId, chunks) {
  var chat = document.getElementById('chatMessages');
  if (chat) chat.innerHTML = '';
  _lastBlockId = -1;
  _blockElems = {};
  _pendingToolGroup = null;
  _lastBlockType = '';
  _approvalToGroup = {};

  chunks.forEach(function (m, i) {
    renderBlockFromMessage(firstId + i, m);
    _lastBlockId = firstId + i;
  });

  hideChatLoading();
  refreshTodoWidget();
  updateContextUsageBar(sessionId);

  // 全量加载完毕后连 SSE
  if (!currentSSE) {
    connectSSE(sessionId, true);
  }
  loadSessionMeta(sessionId);

  requestAnimationFrame(function () {
    requestAnimationFrame(function () {
      setTimeout(function () {
        if (_pendingScrollSid === sessionId && _pendingScrollSeq >= 0) {
          scrollToBlock(_pendingScrollSeq);
          _pendingScrollSeq = -1;
          _pendingScrollSid = null;
        } else if (chat) {
          chat.scrollTop = chat.scrollHeight;
        }
      }, 80);
    });
  });
}

// ========== 发送消息 ==========

// doSendStream 统一发送入口。
//   sid 为 null 时创建新会话，需传入 opts.agent / opts.project / opts.bypass
//   sid 非空时附加到已有会话
function doSendStream(sid, msg, attachment, provider, opts) {
  disconnectSSE();
  showThinking();
  setAgentRunning(true);

  var ms = document.getElementById('chatModelSelect');
  if (!provider) provider = ms ? ms.value : '';

  var body = { message: msg, provider: provider };
  if (sid) {
    body.session = sid;
    if (opts && opts.start) body.start = true;
  } else if (opts) {
    body.agent = opts.agent || '';
    body.task = msg;
    body.project = opts.project || '';
    body.bypass = !!(opts.bypass);
  }
  if (attachment) body.attachment = attachment;

  currentSSE = new LLMStream({
    url: '/api/agent/chat/stream',
    protocol: 'custom',
    headers: getAuthHeaders(),
    body: body,
    markdown: false,
    onSession: function (newSid) {
      if (!sid && newSid) {
        // 新建会话成功 → 切换面板到聊天视图
        currentChatSessionId = newSid;
        currentTodoSessionId = newSid;
        var hp = document.getElementById('agentHistoryPanel');
        var cp = document.getElementById('agentChatPanel');
        if (hp) hp.style.display = 'none';
        if (cp) cp.style.display = '';
        var ct = document.getElementById('chatTitle');
        if (ct) ct.textContent = (opts && opts.agentName) || '';
        // 异步获取会话元数据（project、bypass 等）
        fetch('/api/agent/sessions/' + newSid, { headers: getAuthHeaders() })
          .then(function (r) { return r.json(); })
          .then(function (s) {
            if (currentChatSessionId !== newSid) return;
            var cproj = document.getElementById('chatProject');
            if (cproj) cproj.textContent = s.project ? '[' + s.project + ']' : '';
            var bypassCheck = document.getElementById('bypassApprovalCheck');
            if (bypassCheck) bypassCheck.checked = !!s.bypass_approval;
          }).catch(function () { });
        // 同步 model 选择
        if (typeof agentInfo !== 'undefined' && agentInfo && agentInfo.models) {
          var ms = document.getElementById('chatModelSelect');
          if (ms) {
            ms.style.display = '';
            ms.innerHTML = agentInfo.models.map(function (m) {
              return '<option value="' + esc(m.id) + '"' + (m.id === provider ? ' selected' : '') + '>(' + esc(m.provider_name || '') + ') ' + esc(m.name) + '</option>';
            }).join('');
          }
        }
      }
      currentSSESessionId = newSid;
    },
    onStart: function () {
      setAgentRunning(true);
      showThinking();
    },
    onComplete: function (errMsg) {
      setAgentRunning(false);
      hideThinking();
      currentStreamTarget = null;
      _streamBlockId = -1;
      if (errMsg) appendSysMsg('[错误] ' + errMsg);
      refreshTodoWidget();
      fetchAgentSessions().then(function () { updateChatHistory(); renderAgentListView(); });
      updateContextUsageBar(currentChatSessionId || currentSSESessionId);
      // SSE 连接在 end 后被服务端关闭，重建 idle 连接保持监听
      var sid = currentChatSessionId || currentSSESessionId;
      if (sid) connectSSE(sid, true);
    },
    onLimitExceeded: function (errMsg) {
      // end_limit_exceeded: 执行与 end 相同的清理 + 显示继续按钮
      setAgentRunning(false);
      hideThinking();
      currentStreamTarget = null;
      _streamBlockId = -1;
      refreshTodoWidget();
      fetchAgentSessions().then(function () { updateChatHistory(); renderAgentListView(); });
      updateContextUsageBar(currentChatSessionId || currentSSESessionId);
      showContinueButton();
      // 重建 idle SSE 连接保持监听
      var sid = currentChatSessionId || currentSSESessionId;
      if (sid) connectSSE(sid, true);
    },
    onError: function () {
      setAgentRunning(false);
      hideThinking();
      // 出错也重建 idle 连接（可能是暂时性错误）
      var sid = currentChatSessionId || currentSSESessionId;
      if (sid) setTimeout(function () { connectSSE(sid, true); }, 1000);
    },
    onStreamStart: function (data) { handleStreamStart(data); },
    onContent: function (data) { handleContent(data); },
    onStreamEnd: function (data) { handleStreamEnd(data); },
    onBlock: function (data) { handleBlock(data); },
    onUpdate: function (data) { handleUpdate(data); },
    onTodos: function () {
      refreshTodoWidget();
    },
    onBypassStatus: function (enabled) {
      var check = document.getElementById('bypassApprovalCheck');
      if (check) check.checked = !!enabled;
    },
  });
  currentSSE.start();
}

// ========== 事件绑定 ==========

// Agent 标签点击事件
(function () {
  document.querySelectorAll('.agent-tab').forEach(function (tab) {
    tab.addEventListener('click', function () {
      switchAgentTab(tab.dataset.agentView);
    });
  });
})();

var chatEventsBound = false;

function ensureChatEventsBound() {
  if (chatEventsBound) return;
  chatEventsBound = true;

  var btn = document.getElementById('chatSendBtn');
  var stopBtn = document.getElementById('chatStopBtn');
  var inp = document.getElementById('chatInput');
  var bypassCheck = document.getElementById('bypassApprovalCheck');

  // 绕过审批开关：点击发 REST，SSE 返回 bypass_status 时才更新 UI
  if (bypassCheck) {
    bypassCheck.addEventListener('change', function () {
      var sid = currentChatSessionId;
      if (!sid) return;
      fetch('/api/agent/sessions/' + sid + '/bypass', {
        method: 'POST',
        headers: getAuthHeaders(),
        body: JSON.stringify({ bypass: !!bypassCheck.checked })
      }).catch(function () { });
    });
  }

  function sendMessage() {
    var sid = currentChatSessionId;
    if (!sid) return;
    var m = inp.value.trim();

    // Issue Picker: 收集已选中的 issues/prs
    var picker = document.getElementById('issuePicker');
    var selected = (picker && picker._selectedIssues) ? picker._selectedIssues : {};
    var selectedKeys = Object.keys(selected);
    var hasSelected = selectedKeys.length > 0;

    if (!m && !hasSelected) return;
    inp.value = '';
    setAgentRunning(true);

    // 不创建本地用户块！用户消息由后端持久化后通过 SSE block{subtype:user} 回传
    // 有选中的 issues → 异步获取详情后发送
    if (hasSelected) {
      sendWithAttachments(sid, m, selectedKeys, selected, picker);
      return;
    }

    doSendStream(sid, m, '', '');
  }

  if (btn) btn.addEventListener('click', sendMessage);
  if (inp) {
    inp.addEventListener('keydown', function (e) {
      if (e.key === 'Enter' && e.shiftKey) {
        e.preventDefault();
        if (!(currentSSE && currentSSESessionId === currentChatSessionId)) {
          e.preventDefault();
          sendMessage();
        }
      }
    });
  }

  if (stopBtn) {
    stopBtn.addEventListener('click', function () {
      var sid = currentChatSessionId || currentSSESessionId;
      if (sid) {
        fetch('/api/agent/stop', {
          method: 'POST',
          headers: getAuthHeaders(),
          body: JSON.stringify({ session: sid })
        }).catch(function () { });
      }
    });
  }

  var contBtn = document.getElementById('chatContinueBtn');
  if (contBtn) {
    contBtn.addEventListener('click', function () {
      var sid = currentChatSessionId || currentSSESessionId;
      if (!sid) return;
      hideContinueButton();
      setAgentRunning(true);
      showThinking();
      // 不切断 SSE —— 发请求启动 Agent，idle 连接会自动收到新事件
      fetch('/api/agent/chat/stream', {
        method: 'POST',
        headers: Object.assign({ 'Content-Type': 'application/json' }, getAuthHeaders()),
        body: JSON.stringify({ session: sid, message: '请继续完成剩余的推理任务。', bypass: true, start: true })
      }).catch(function (e) {
        setAgentRunning(false);
        hideThinking();
        appendSysMsg('[错误] 继续推理失败: ' + (e.message || String(e)));
      });
    });
  }
}

var _agentRunning = false; // 全局 Agent 运行状态，供 todo 清除按钮等使用

function setAgentRunning(running) {
  _agentRunning = running;
  var btn = document.getElementById('chatSendBtn');
  var stopBtn = document.getElementById('chatStopBtn');
  var contBtn = document.getElementById('chatContinueBtn');
  var inp = document.getElementById('chatInput');
  if (btn) btn.style.display = running ? 'none' : '';
  if (stopBtn) stopBtn.style.display = running ? '' : 'none';
  if (contBtn) contBtn.style.display = 'none'; // 运行时总是隐藏继续按钮
  if (inp) inp.disabled = running;
  setTodoClearDisabled(running);
}

function showContinueButton() {
  var btn = document.getElementById('chatSendBtn');
  var stopBtn = document.getElementById('chatStopBtn');
  var contBtn = document.getElementById('chatContinueBtn');
  var inp = document.getElementById('chatInput');
  if (btn) btn.style.display = '';
  if (stopBtn) stopBtn.style.display = 'none';
  if (contBtn) contBtn.style.display = '';
  if (inp) inp.disabled = false;
  setTodoClearDisabled(false);
}

function setTodoClearDisabled(disabled) {
  document.querySelectorAll('.todo-clear-btn').forEach(function (btn) {
    btn.disabled = disabled;
    if (disabled) {
      btn.title = 'Agent 工作中不可用';
    } else {
      btn.title = (typeof t === 'function') ? t('todoClear') : '清除';
    }
  });
}

function hideContinueButton() {
  var contBtn = document.getElementById('chatContinueBtn');
  if (contBtn) contBtn.style.display = 'none';
}

function showChatLoading() {
  var el = document.getElementById('chatLoadingSpinner');
  if (el) el.style.display = '';
}

function hideChatLoading() {
  var el = document.getElementById('chatLoadingSpinner');
  if (el) el.style.display = 'none';
}

function openThinkingModal(text) {
  var modal = document.getElementById('thinkingModal');
  var body = document.getElementById('thinkingModalBody');
  if (modal && body) {
    body.textContent = text;
    modal.classList.add('show');
  }
}

function closeThinkingModal() {
  var modal = document.getElementById('thinkingModal');
  if (modal) modal.classList.remove('show');
}

function showThinking() {
  var el = document.getElementById('thinkingIndicator');
  if (el) el.style.display = 'flex';
}

function hideThinking() {
  var el = document.getElementById('thinkingIndicator');
  if (el) el.style.display = 'none';
}

function backToHistory() {
  // 关闭分享模式
  if (_shareMode) toggleShareMode();
  disconnectSSE();
  currentChatSessionId = null;
  currentTodoSessionId = null;
  var hp = document.getElementById('agentHistoryPanel');
  var cp = document.getElementById('agentChatPanel');
  if (hp) hp.style.display = '';
  if (cp) cp.style.display = 'none';
  var chat = document.getElementById('chatMessages');
  if (chat) chat.innerHTML = '';
  _blockElems = {};
  _pendingToolGroup = null;
  _lastBlockType = '';
  _approvalToGroup = {};
  var tc = document.getElementById('todoWidgetContainer');
  if (tc) { tc.style.display = 'none'; tc.innerHTML = ''; }
  var ub = document.getElementById('ctxUsageBar');
  if (ub) ub.style.display = 'none';
  // 清除历史搜索
  var hs = document.getElementById('historySearchInput');
  if (hs) hs.value = '';
  updateChatHistory();
}

// updateContextUsageBar 更新底部对话界面的 Token 用量条
function updateContextUsageBar(sid) {
  var bar = document.getElementById('ctxUsageBar');
  if (!bar) return;
  if (!sid) { bar.style.display = 'none'; return; }
  var s = (agentSessions || []).find(function (x) { return x.id === sid; });
  var cu = s ? (s.context_usage || s.ContextUsage) : null;
  if (!cu || !cu.usage_percent) { bar.style.display = 'none'; return; }
  bar.style.display = '';
  var pct = Math.min(100, Math.round(cu.usage_percent));
  var fill = document.getElementById('ctxUsageFill');
  var label = document.getElementById('ctxUsageLabel');
  if (fill) {
    fill.style.width = pct + '%';
    fill.style.background = pct > 80 ? '#ef4444' : pct > 50 ? '#f59e0b' : '#22c55e';
  }
  if (label) label.textContent = fmtTok(cu.total_tokens) + ' / ' + fmtTok(cu.max_tokens) + ' (' + pct + '%)';
}

function getCurrentModelName() {
  var ms = document.getElementById('chatModelSelect');
  if (!ms || !ms.selectedOptions || !ms.selectedOptions[0]) return '';
  return ms.selectedOptions[0].textContent || '';
}

function finishStream() {
  hideThinking();
  setAgentRunning(false);
  fetchAgentSessions().then(function () { updateChatHistory(); renderAgentListView(); });
  refreshTodoWidget();
}
function smartScroll(el) {
  if (!el) return;
  requestAnimationFrame(function () {
    var atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 100;
    if (atBottom) el.scrollTop = el.scrollHeight;
  });
}

function compactCurrentChat() {
  var sid = currentChatSessionId;
  if (!sid) return;
  fetch('/api/agent/compact', {
    method: 'POST',
    headers: getAuthHeaders(),
    body: JSON.stringify({ session: sid })
  }).then(function (r) { return r.json(); }).then(function (d) {
    alert('Compacted: ' + d.messages_before + ' -> ' + d.messages_after);
    fetchAgentSessions().then(function () { updateChatHistory(); });
  }).catch(function (e) { alert('Compact failed: ' + e.message); });
}

// esc: HTML 转义
function esc(s) {
  if (!s) return '';
  return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

// ========== Issue Picker ==========

function getSessionGhRepo() {
  var sid = currentChatSessionId;
  if (!sid) return null;
  var s = (agentSessions || []).find(function (x) { return x.id === sid; });
  if (!s) return null;

  if (s.project) {
    var mods = (typeof state !== 'undefined' && state.mods) ? state.mods : [];
    var mod = mods.find(function (m) { return m.name === s.project; });
    if (mod && mod.ids && mod.ids.gh) return mod.ids.gh;
  }
  if (s.project_path) {
    var pp = s.project_path.replace(/\\/g, '/').toLowerCase();
    var mods2 = (typeof state !== 'undefined' && state.mods) ? state.mods : [];
    for (var i = 0; i < mods2.length; i++) {
      var m2 = mods2[i];
      if (m2.ids && m2.ids.gh) {
        var parts = m2.ids.gh.split('/');
        if (parts.length === 2 && pp.indexOf(parts[1].toLowerCase()) >= 0) return m2.ids.gh;
      }
    }
  }
  if (typeof state !== 'undefined' && state.allGitHubItems) {
    var all = (state.allGitHubItems.issues || []).concat(state.allGitHubItems.pulls || []);
    if (all.length > 0 && all[0].repoPath) return all[0].repoPath;
  }
  return null;
}

function showIssuePicker() {
  var picker = document.getElementById('issuePicker');
  if (!picker) return;
  if (picker.style.display !== 'none') { closeIssuePicker(); return; }

  var ghRepo = getSessionGhRepo();
  if (!ghRepo) { alert('无法确定项目的 GitHub 仓库'); return; }
  var parts = ghRepo.split('/');
  if (parts.length !== 2) { alert('无效的仓库名: ' + ghRepo); return; }
  var owner = parts[0], repo = parts[1];

  picker.style.display = '';
  var list = document.getElementById('issuePickerList');
  if (list) list.innerHTML = '<div class="issue-picker-empty">加载中...</div>';
  var search = document.getElementById('issuePickerSearch');
  if (search) search.value = '';
  picker._ghOwner = owner;
  picker._ghRepo = repo;
  if (!picker._selectedIssues) picker._selectedIssues = {};
  fetchIssuesForPicker(owner, repo, '');
}

function closeIssuePicker() {
  var picker = document.getElementById('issuePicker');
  if (picker) picker.style.display = 'none';
  renderSelectedIssueChips();
}

function fetchIssuesForPicker(owner, repo, filter) {
  var list = document.getElementById('issuePickerList');
  if (!list) return;
  var items = [];
  if (typeof state !== 'undefined' && state.allGitHubItems) {
    var targetGh = (owner + '/' + repo).toLowerCase();
    var issues = (state.allGitHubItems.issues || []).map(function (it) { it._ghType = 'issue'; return it; });
    var pulls = (state.allGitHubItems.pulls || []).map(function (it) { it._ghType = 'pr'; return it; });
    items = issues.concat(pulls).filter(function (it) { return it.repoPath && it.repoPath.toLowerCase() === targetGh; });
  }
  items.sort(function (a, b) { return (b.number || 0) - (a.number || 0); });
  if (items.length === 0) { list.innerHTML = '<div class="issue-picker-empty">暂无数据，请先刷新列表页</div>'; return; }
  var stateFilter = document.getElementById('issuePickerStateFilter');
  var stateVal = stateFilter ? stateFilter.value : 'all';
  renderIssuePickerList(items, filter, stateVal);
}

function renderIssuePickerList(items, filter, stateFilter) {
  var list = document.getElementById('issuePickerList');
  if (!list) return;
  var picker = document.getElementById('issuePicker');
  var selected = (picker && picker._selectedIssues) ? picker._selectedIssues : {};
  var f = (filter || '').toLowerCase();
  var sf = stateFilter || 'all';
  var filtered = items.filter(function (it) {
    if (sf === 'open' && it.state !== 'open') return false;
    if (sf === 'closed' && it.state === 'open') return false;
    if (!f) return true;
    return (it.title || '').toLowerCase().indexOf(f) >= 0 || String(it.number).indexOf(f) >= 0;
  });
  if (filtered.length === 0) { list.innerHTML = '<div class="issue-picker-empty">无匹配结果</div>'; return; }
  list.innerHTML = filtered.map(function (it) {
    var type = it._ghType || 'issue';
    var key = type + '_' + it.number;
    var checked = selected[key] ? ' checked' : '';
    return '<div class="issue-picker-item' + (selected[key] ? ' selected' : '') + '" onclick="toggleIssueSelection(\'' + esc(it.number) + '\',\'' + esc(type) + '\',\'' + esc(it.title || '') + '\',event)">' +
      '<span class="ip-check">' + (selected[key] ? '☑' : '☐') + '</span>' +
      '<span class="ip-number">#' + it.number + '</span>' +
      '<span class="ip-title">' + esc(it.title || '') + '</span></div>';
  }).join('');
}

function toggleIssueSelection(number, type, title, ev) {
  if (ev) { ev.stopPropagation(); ev.preventDefault(); }
  var picker = document.getElementById('issuePicker');
  if (!picker) return;
  if (!picker._selectedIssues) picker._selectedIssues = {};
  var key = type + '_' + number;
  if (picker._selectedIssues[key]) { delete picker._selectedIssues[key]; }
  else { picker._selectedIssues[key] = { number: number, type: type, title: title || '' }; }
  var search = document.getElementById('issuePickerSearch');
  fetchIssuesForPicker(picker._ghOwner, picker._ghRepo, search ? search.value : '');
}

function renderSelectedIssueChips() {
  var container = document.getElementById('selectedIssueChips');
  if (!container) return;
  var picker = document.getElementById('issuePicker');
  var selected = (picker && picker._selectedIssues) ? picker._selectedIssues : {};
  var keys = Object.keys(selected);
  if (keys.length === 0) { container.style.display = 'none'; container.innerHTML = ''; return; }
  container.style.display = '';
  container.innerHTML = keys.map(function (k) {
    var s = selected[k], type = s.type || 'issue';
    return '<span class="issue-chip" onclick="removeSelectedIssue(\'' + esc(k) + '\')" title="点击移除">' +
      (type === 'pr' ? '🔀' : '🐛') + ' #' + s.number + ' ✕</span>';
  }).join('');
}

function removeSelectedIssue(key) {
  var picker = document.getElementById('issuePicker');
  if (!picker || !picker._selectedIssues) return;
  delete picker._selectedIssues[key];
  renderSelectedIssueChips();
}

function onIssuePickerFilterChange() {
  var picker = document.getElementById('issuePicker');
  if (!picker || picker.style.display === 'none') return;
  var search = document.getElementById('issuePickerSearch');
  fetchIssuesForPicker(picker._ghOwner, picker._ghRepo, search ? search.value : '');
}

function sendWithAttachments(sid, msg, selectedKeys, selected, picker) {
  showThinking();
  var owner = picker._ghOwner, repo = picker._ghRepo;
  picker._selectedIssues = {};
  renderSelectedIssueChips();

  var fetches = selectedKeys.map(function (key) {
    var info = selected[key];
    var apiPath = (info.type === 'pr')
      ? '/api/github/pr/' + owner + '/' + repo + '/' + info.number
      : '/api/github/issue/' + owner + '/' + repo + '/' + info.number;
    return fetch(apiPath, { headers: getAuthHeaders() }).then(function (r) {
      if (!r.ok) throw new Error('HTTP ' + r.status + ' for #' + info.number);
      return r.json().then(function (d) { d._type = info.type; d._number = info.number; return d; });
    });
  });

  Promise.all(fetches).then(function (details) {
    var attachments = details.map(function (d) {
      if (d._type === 'pr') {
        return '[GitHub PR #' + d._number + '] ' + owner + '/' + repo + '\n' +
          '标题: ' + (d.title || '') + '\n状态: ' + (d.state || '') + '\n作者: ' + (d.user || '') + '\n' +
          '目标分支: ' + (d.base_ref || '') + ' ← 源分支: ' + (d.head_ref || '') + '\n\n--- PR Diff ---\n' + (d.diff || '(diff 生成失败)');
      }
      return '[GitHub Issue #' + d._number + '] ' + owner + '/' + repo + '\n' +
        '标题: ' + (d.title || '') + '\n状态: ' + (d.state || '') + '\n作者: ' + (d.user || '') + '\n\n--- Issue Body ---\n' + (d.body || '(无描述)');
    });
    var attachment = attachments.join('\n\n==========\n\n');

    var chat = document.getElementById('chatMessages');
    if (chat) {
      var attDiv = document.createElement('div');
      var label = '📎 附件: ' + details.map(function (d) { return '#' + d._number; }).join(', ');
      attDiv.innerHTML = '<details class="chat-attachment"><summary>' + esc(label) + '</summary><pre class="chat-attachment-body">' + esc(attachment.substring(0, 3000)) + '</pre></details>';
      chat.appendChild(attDiv); smartScroll(chat);
    }
    doSendStream(sid, msg, attachment, '');
  }).catch(function (e) {
    hideThinking(); setAgentRunning(false);
    appendSysMsg('获取附件失败: ' + e.message);
  });
}

// ========== 搜索对话 ==========

var _searchTimer = null;

function openSearchModal() {
  var modal = document.getElementById('searchModal');
  if (!modal) return;
  modal.classList.add('show');
  var input = document.getElementById('searchInput');
  if (input) {
    input.value = '';
    setTimeout(function () { input.focus(); }, 100);
  }
  var results = document.getElementById('searchResults');
  if (results) results.innerHTML = '<div class="search-hint">' + (t('chatSearchHint') || '输入关键词，搜索所有对话历史') + '</div>';
}

function closeSearchModal() {
  var modal = document.getElementById('searchModal');
  if (modal) modal.classList.remove('show');
  if (_searchTimer) { clearTimeout(_searchTimer); _searchTimer = null; }
}

function onSearchInput() {
  if (_searchTimer) clearTimeout(_searchTimer);
  var input = document.getElementById('searchInput');
  var q = input ? input.value.trim() : '';
  if (!q) {
    var results = document.getElementById('searchResults');
    if (results) results.innerHTML = '<div class="search-hint">' + (t('chatSearchHint') || '输入关键词，搜索所有对话历史') + '</div>';
    return;
  }
  _searchTimer = setTimeout(function () { doSearch(q); }, 300);
}

function doSearch(q) {
  var results = document.getElementById('searchResults');
  if (!results) return;
  results.innerHTML = '<div class="search-hint">搜索中...</div>';

  fetch('/api/agent/search?q=' + encodeURIComponent(q) + '&limit=20', {
    headers: getAuthHeaders()
  }).then(function (r) {
    if (!r.ok) throw new Error('HTTP ' + r.status);
    return r.json();
  }).then(function (data) {
    if (!data || data.length === 0) {
      results.innerHTML = '<div class="search-hint">' + (t('chatSearchNoResults') || '无匹配结果') + '</div>';
      return;
    }
    results.innerHTML = data.map(function (item) {
      var typeLabel = item.type === 'user' ? '👤 用户' : item.type === 'message' ? '🤖 助手' : '💬 ' + esc(item.type);
      var title = item.session_title || '(未命名会话)';
      return '<button class="search-result-item" onclick="navigateToSearchResult(\'' + esc(item.session_id) + '\',' + item.seq + ')">' +
        '<div class="search-result-snippet">' + (item.snippet || '') + '</div>' +
        '<div class="search-result-meta">' +
        '<span class="search-result-type">' + typeLabel + '</span>' +
        '<span class="search-result-session">' + esc(title) + '</span>' +
        '</div></button>';
    }).join('');
  }).catch(function (e) {
    results.innerHTML = '<div class="search-hint">搜索失败: ' + esc(e.message) + '</div>';
  });
}

function navigateToSearchResult(sessionId, seq) {
  closeSearchModal();
  // 如果已经在当前会话，直接滚动到对应位置
  if (currentChatSessionId === sessionId) {
    scrollToBlock(seq);
    return;
  }
  // 切换到目标会话
  openChatWindow(sessionId, '', null, '', false);
  // 等待加载完成后滚动
  _pendingScrollSeq = seq;
  _pendingScrollSid = sessionId;
}

var _pendingScrollSeq = -1;
var _pendingScrollSid = null;

function scrollToBlock(seq) {
  var el = _blockElems[seq];
  if (el) {
    el.scrollIntoView({ behavior: 'smooth', block: 'center' });
    // 高亮闪烁
    el.style.transition = 'background 0.3s';
    el.style.background = 'rgba(99, 102, 241, 0.12)';
    setTimeout(function () { el.style.background = ''; }, 1500);
  }
}

// 在 openChatWindow 加载完成后检查是否有待滚动
// 通过重写 _lastBlockId 的更新来触发（在 fetch 回调中处理）
// 直接 hook: 在 openChatWindow 的 lazy load 回调中添加检查

// ========== 分享模式 ==========

var _shareMode = false;
var _shareSelected = {}; // blockId → true

function toggleShareMode() {
  _shareMode = !_shareMode;
  var btn = document.getElementById('chatShareBtn');
  var chat = document.getElementById('chatMessages');
  if (!chat) return;

  if (_shareMode) {
    if (btn) btn.classList.add('active');
    chat.classList.add('share-mode');
    _shareSelected = {};
    _lastShareClick = -1;
    // 隐藏输入区域
    var inputArea = document.querySelector('.chat-input-area');
    if (inputArea) inputArea.style.display = 'none';
    var thinking = document.getElementById('thinkingIndicator');
    if (thinking) thinking.style.display = 'none';
    ensureShareBar();
    updateShareCount();
  } else {
    if (btn) btn.classList.remove('active');
    chat.classList.remove('share-mode');
    // 恢复输入区域
    var inputArea = document.querySelector('.chat-input-area');
    if (inputArea) inputArea.style.display = '';
    clearShareSelections();
    removeShareBar();
    removeShareResult();
  }
}

function ensureShareBar() {
  if (document.getElementById('shareConfirmBar')) return;
  var bar = document.createElement('div');
  bar.id = 'shareConfirmBar';
  bar.className = 'share-confirm-bar';
  bar.innerHTML = '<span class="share-count" id="shareCount">已选 0 个块</span>' +
    '<div><button class="share-confirm-cancel" onclick="toggleShareMode()">取消</button>' +
    '<button class="share-confirm-create" onclick="doCreateShare()">生成分享链接</button></div>';
  var chat = document.getElementById('chatMessages');
  if (chat) chat.parentElement.appendChild(bar);
}

function removeShareBar() {
  var bar = document.getElementById('shareConfirmBar');
  if (bar) bar.remove();
}

function removeShareResult() {
  var ov = document.getElementById('shareResultOverlay');
  if (ov) ov.remove();
}

function clearShareSelections() {
  Object.keys(_shareSelected).forEach(function (id) {
    var el = document.getElementById('block-' + id);
    if (el) el.classList.remove('selected');
  });
  _shareSelected = {};
}

function updateShareCount() {
  var cnt = Object.keys(_shareSelected).length;
  var el = document.getElementById('shareCount');
  if (el) el.textContent = '已选 ' + cnt + ' 个块';
}

// 点击消息块（分享模式绑定在 chat-messages 委托）
function onShareBlockClick(e) {
  if (!_shareMode) return;
  var wrap = e.target.closest('.chat-msg-wrap');
  if (!wrap) return;
  // 找 block id
  var bid = -1;
  if (wrap.id && wrap.id.indexOf('block-') === 0) {
    bid = parseInt(wrap.id.slice(6), 10);
    if (isNaN(bid)) bid = -1;
  }
  if (bid < 0) {
    // 尝试从 _blockElems 反查
    for (var k in _blockElems) {
      if (_blockElems[k] === wrap) { bid = parseInt(k, 10); break; }
    }
  }
  if (bid < 0) return;

  if (e.shiftKey && _lastShareClick >= 0) {
    // Shift 连续选择
    var lo = Math.min(_lastShareClick, bid);
    var hi = Math.max(_lastShareClick, bid);
    for (var i = lo; i <= hi; i++) {
      _shareSelected[i] = true;
      var el = document.getElementById('block-' + i);
      if (el) el.classList.add('selected');
    }
  } else {
    // 单击切换
    if (_shareSelected[bid]) {
      delete _shareSelected[bid];
      wrap.classList.remove('selected');
    } else {
      _shareSelected[bid] = true;
      wrap.classList.add('selected');
    }
  }
  _lastShareClick = bid;
  updateShareCount();
}

var _lastShareClick = -1;

function doCreateShare() {
  var ids = Object.keys(_shareSelected).map(Number).sort(function (a, b) { return a - b; });
  if (ids.length === 0) { alert('请至少选择一个消息块'); return; }

  fetch('/api/agent/share', {
    method: 'POST',
    headers: getAuthHeaders(),
    body: JSON.stringify({ session_id: currentChatSessionId, blocks: ids })
  }).then(function (r) {
    if (!r.ok) throw new Error('创建分享失败');
    return r.json();
  }).then(function (data) {
    var url = location.origin + '/share/' + data.share_id;
    showShareResult(url);
  }).catch(function (err) {
    alert('创建分享失败: ' + (err.message || err));
  });
}

function showShareResult(url) {
  removeShareResult();
  var ov = document.createElement('div');
  ov.id = 'shareResultOverlay';
  ov.className = 'share-result-overlay';

  var box = document.createElement('div');
  box.className = 'share-result-box';

  var h3 = document.createElement('h3');
  h3.textContent = '分享已创建';
  box.appendChild(h3);

  var urlDiv = document.createElement('div');
  urlDiv.className = 'share-url';
  urlDiv.textContent = url;
  box.appendChild(urlDiv);

  var copyBtn = document.createElement('button');
  copyBtn.className = 'share-result-copy';
  copyBtn.textContent = '复制链接';
  copyBtn.onclick = function () {
    navigator.clipboard.writeText(url).then(function () {
      copyBtn.textContent = '已复制';
      setTimeout(function () { copyBtn.textContent = '复制链接'; }, 630);
    });
  };
  box.appendChild(copyBtn);

  var closeBtn = document.createElement('button');
  closeBtn.className = 'share-result-close';
  closeBtn.textContent = '关闭';
  closeBtn.onclick = function () { ov.remove(); };
  box.appendChild(closeBtn);

  ov.appendChild(box);
  ov.addEventListener('click', function (e) { if (e.target === ov) ov.remove(); });
  document.body.appendChild(ov);
}

// 在聊天容器上绑定点击委托
function bindShareClick() {
  var chat = document.getElementById('chatMessages');
  if (!chat || chat._shareBound) return;
  chat._shareBound = true;
  chat.addEventListener('click', onShareBlockClick);
}

// 在 openChatWindow 中调用 bindShareClick
var _origOpenChatWindow = openChatWindow;
openChatWindow = function () {
  _origOpenChatWindow.apply(this, arguments);
  setTimeout(bindShareClick, 100);
};

// ========== 云盘文件选择器 ==========

var _drivePickerPath = '';
var _drivePickerSelected = null;

function showDrivePicker() {
  var overlay = document.getElementById('drivePickerOverlay');
  if (!overlay) return;
  overlay.classList.add('show');
  _drivePickerPath = '';
  _drivePickerSelected = null;
  drivePickerLoad('/');
  drivePickerUpdateFooter();
}

function closeDrivePicker() {
  var overlay = document.getElementById('drivePickerOverlay');
  if (overlay) overlay.classList.remove('show');
}

function drivePickerLoad(path) {
  _drivePickerPath = path;
  var bc = document.getElementById('drivePickerBreadcrumb');
  if (bc) {
    var parts = path.replace(/\/$/, '').split('/').filter(Boolean);
    var h = '<a onclick="drivePickerLoad(\'/\')">📂 根目录</a>';
    var a = '';
    parts.forEach(function (p) { a += '/' + p; h += ' / <a onclick="drivePickerLoad(\'' + a + '\')">' + esc(p) + '</a>'; });
    bc.innerHTML = h;
  }
  var list = document.getElementById('drivePickerList');
  if (list) list.innerHTML = '<div style="text-align:center;padding:32px;color:#9ca3af;">加载中…</div>';

  fetch('/api/drive/list' + (path === '/' ? '' : path), { headers: getAuthHeaders() })
    .then(function (r) { if (!r.ok) throw new Error('HTTP ' + r.status); return r.json(); })
    .then(function (data) { renderDrivePickerList(data); })
    .catch(function (e) {
      if (list) list.innerHTML = '<div style="text-align:center;padding:32px;color:#ef4444;">加载失败: ' + esc(e.message) + '</div>';
    });
}

function renderDrivePickerList(data) {
  var list = document.getElementById('drivePickerList');
  if (!list) return;
  var items = data.items || [];
  if (items.length === 0) {
    list.innerHTML = '<div style="text-align:center;padding:40px;color:#9ca3af;">📭 空文件夹</div>';
    return;
  }
  list.innerHTML = '';
  items.forEach(function (item) {
    var row = document.createElement('div');
    row.className = 'drive-picker-row';
    row.dataset.path = item.path;
    row.dataset.type = item.type;
    row.innerHTML = '<span class="picker-icon">' + (item.type === 'dir' ? '📁' : fileIcon(item.name)) + '</span>' +
      '<span class="picker-name">' + esc(item.name) + '</span>' +
      (item.type !== 'dir' ? '<span class="picker-size">' + formatSize(item.size) + '</span>' : '');
    row.addEventListener('click', function () {
      if (item.type === 'dir') {
        drivePickerLoad('/' + item.path);
      } else {
        list.querySelectorAll('.drive-picker-row').forEach(function (r) { r.classList.remove('selected'); });
        row.classList.add('selected');
        _drivePickerSelected = { path: item.path, name: item.name };
        drivePickerUpdateFooter();
      }
    });
    list.appendChild(row);
  });
}

function drivePickerUpdateFooter() {
  var footer = document.getElementById('drivePickerFooter');
  var label = document.getElementById('drivePickerSelected');
  if (!footer || !label) return;
  if (_drivePickerSelected) {
    footer.style.display = '';
    label.textContent = '已选: ' + _drivePickerSelected.name;
  } else {
    footer.style.display = 'none';
  }
}

function confirmDrivePicker() {
  if (!_drivePickerSelected) return;
  var name = _drivePickerSelected.name;
  var path = _drivePickerSelected.path;

  // 加载文件内容
  fetch('/api/drive/file?path=' + encodeURIComponent(path) + '&action=view', { headers: getAuthHeaders() })
    .then(function (r) { if (!r.ok) throw new Error('读取失败'); return r.json(); })
    .then(function (data) {
      var attachment = '[文件: ' + name + ']\n' + data.content;
      // 在聊天中显示附件提示
      var chat = document.getElementById('chatMessages');
      if (chat) {
        var attDiv = document.createElement('div');
        attDiv.innerHTML = '<details class="chat-attachment"><summary>📎 附件: ' + esc(name) + '</summary><pre class="chat-attachment-body">' + esc(attachment.substring(0, 3000)) + '</pre></details>';
        chat.appendChild(attDiv); smartScroll(chat);
      }
      doSendStream(currentChatSessionId, '', attachment, '');
      closeDrivePicker();
    })
    .catch(function (e) { alert('读取文件失败: ' + (e.message || '')); });
}
