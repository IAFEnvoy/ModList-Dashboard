// agent-todo.js — TODO 任务清单（绑定到输入框上方）
var currentTodoSessionId = null;

function refreshTodoWidget() {
  if (!currentChatSessionId) return;
  fetchAgentSessions().then(function () {
    var s = (agentSessions || []).find(function (x) { return x.id === currentChatSessionId; });
    if (!s || !s.todos || s.todos.length === 0) {
      var tc = $('#todoWidgetContainer');
      if (tc) { tc.style.display = 'none'; tc.innerHTML = ''; }
      var bar = document.getElementById('todoStatusBar');
      if (bar) bar.classList.remove('has-todos');
      return;
    }
    renderTodoFromSession(s.todos);
  });
}

function renderTodoFromSession(todos) {
  var c = $('#todoWidgetContainer');
  var bar = document.getElementById('todoStatusBar');
  var countEl = document.getElementById('todoStatusCount');
  var modalBody = document.getElementById('todoModalBody');

  if (!c) return;

  if (!todos || todos.length === 0) {
    c.style.display = 'none';
    c.innerHTML = '';
    if (bar) bar.classList.remove('has-todos');
    if (modalBody) modalBody.innerHTML = '';
    return;
  }

  var inProgress = 0, completed = 0, total = todos.length;
  todos.forEach(function (item) {
    if (item.status === 'in-progress') inProgress++;
    if (item.status === 'completed') completed++;
  });

  var isRunning = (typeof _agentRunning !== 'undefined') ? _agentRunning : false;

  // === 完整 widget（正常高度时 CSS 显示此块） ===
  var h = '<div class="todo-widget-header" onclick="toggleTodoWidget()">' +
    '<span class="todo-collapse-icon">▼</span> ' + t('todoListTitle') +
    ' <span class="todo-progress">' + completed + '/' + total + '</span>' +
    '<button class="todo-clear-btn"' + ' title="' + t('todoClear') + '"' +
    ' onclick="event.stopPropagation();clearTodoList()"><img src="img/x.svg" alt="' + t('todoClear') + '" class="todo-clear-icon"></button></div>';
  todos.forEach(function (item) {
    var icon = 'img/todo-not-started.svg';
    var cls = 'todo-not-started';
    if (item.status === 'in-progress') { icon = 'img/todo-in-progress.svg'; cls = 'todo-in-progress'; }
    if (item.status === 'completed') { icon = 'img/todo-completed.svg'; cls = 'todo-completed'; }
    h += '<div class="todo-item ' + cls + '" data-todo-id="' + esc(item.id) + '"><span class="todo-icon"><img src="' + icon + '" alt=""></span><span class="todo-title">' + esc(item.title) + '</span></div>';
  });
  c.innerHTML = h;
  c.style.display = 'block';
  c.querySelectorAll('.todo-title').forEach(function (span) {
    span.addEventListener('dblclick', function () { editAgentTodo(span); });
  });

  // === 状态栏（小屏时 CSS 媒体查询展示此块） ===
  if (bar) {
    bar.classList.add('has-todos');
    if (countEl) countEl.textContent = completed + '/' + total;
  }

  // === 弹窗内容 ===
  if (modalBody) {
    var mh = '';
    todos.forEach(function (item) {
      var icon = 'img/todo-not-started.svg';
      if (item.status === 'in-progress') icon = 'img/todo-in-progress.svg';
      if (item.status === 'completed') icon = 'img/todo-completed.svg';
      mh += '<div class="todo-item" data-todo-id="' + esc(item.id) + '"><span class="todo-icon"><img src="' + icon + '" alt=""></span><span class="todo-title">' + esc(item.title) + '</span></div>';
    });
    mh += '<button class="todo-clear-btn" onclick="clearTodoListModal()">清空待办</button>';
    modalBody.innerHTML = mh;
    modalBody.querySelectorAll('.todo-title').forEach(function (span) {
      span.addEventListener('dblclick', function () { editAgentTodo(span); });
    });
  }
  // 同步清除按钮状态
  if (typeof setTodoClearDisabled === 'function') setTodoClearDisabled(isRunning);
}

function renderTodoFromSessionData(data) {
  if (!currentTodoSessionId) return;
  fetchAgentSessions().then(function () {
    var s = (agentSessions || []).find(function (x) { return x.id === currentTodoSessionId; });
    if (s && s.todos && s.todos.length > 0) {
      renderTodoFromSession(s.todos);
    }
  });
}

function editAgentTodo(span) {
  var itemDiv = span.closest('.todo-item');
  if (!itemDiv) return;
  var id = itemDiv.dataset.todoId;
  var oldText = span.textContent;
  var input = document.createElement('input');
  input.type = 'text';
  input.className = 'todo-edit-input';
  input.value = oldText;
  input.style.cssText = 'font-size:inherit;font-family:inherit;width:100%;padding:1px 3px;border:1px solid #6366f1;border-radius:3px;outline:none;background:var(--card-bg);color:var(--text);';
  span.replaceWith(input);
  input.focus();
  input.select();
  var done = function () {
    var newText = input.value.trim();
    if (!newText || newText === oldText) { input.replaceWith(span); return; }
    span.textContent = newText;
    input.replaceWith(span);
    var sid = currentChatSessionId || currentTodoSessionId;
    if (sid) {
      fetch('/api/agent/todo/update', { method: 'POST', headers: getAuthHeaders(), body: JSON.stringify({ session: sid, todo_id: id, title: newText }) }).catch(function () { });
    }
  };
  input.addEventListener('blur', done);
  input.addEventListener('keydown', function (e) { if (e.key === 'Enter') { e.preventDefault(); input.blur(); } if (e.key === 'Escape') { input.value = oldText; input.blur(); } });
}

function toggleTodoWidget() {
  var c = $('#todoWidgetContainer');
  if (c) c.classList.toggle('collapsed');
}

function clearTodoList() {
  if (!currentChatSessionId) return;
  if (!confirm(t('todoConfirmDelete'))) return;
  doClearTodoList();
}

function clearTodoListModal() {
  if (!confirm(t('todoConfirmDelete'))) return;
  closeTodoModal();
  doClearTodoList();
}

function doClearTodoList() {
  if (!currentChatSessionId) return;
  fetch('/api/agent/todo/clear', {
    method: 'POST',
    headers: getAuthHeaders(),
    body: JSON.stringify({ session: currentChatSessionId })
  }).then(function () {
    var tc = $('#todoWidgetContainer');
    if (tc) { tc.style.display = 'none'; tc.innerHTML = ''; }
    var bar = document.getElementById('todoStatusBar');
    if (bar) bar.classList.remove('has-todos');
    var mb = document.getElementById('todoModalBody');
    if (mb) mb.innerHTML = '';
  }).catch(function (e) { console.error('clearTodoList error:', e); });
}

function openTodoModal() {
  var modal = document.getElementById('todoModal');
  if (modal) modal.classList.add('show');
}

function closeTodoModal() {
  var modal = document.getElementById('todoModal');
  if (modal) modal.classList.remove('show');
}

function createElementFromHTML(html) {
  var div = document.createElement('div');
  div.innerHTML = html.trim();
  return div.firstChild;
}
