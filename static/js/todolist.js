// todolist.js — 代办事项面板 (Bugs + TODO)
const TODO_PER_PAGE = 20;
let todoBugPage = 1, todoTodoPage = 1;
let todoBugTotal = 0, todoTodoTotal = 0;

function todoAuthHeaders() {
  const token = localStorage.getItem('modlist_token');
  return token ? { 'Authorization': 'Bearer ' + token } : {};
}

async function fetchTodoCounts() {
  try {
    const r = await fetch('/api/todos/counts');
    const data = await r.json();
    $('#statBugs').textContent = data.bugs || 0;
    $('#statTodos').textContent = data.todos || 0;
  } catch (e) { /* ignore */ }
}

function todoAddCard(type) {
  const isLoggedIn = typeof state !== 'undefined' && state.loggedIn;
  if (!isLoggedIn) return '';
  return `
    <div class="todo-add-card" id="todoAddCard_${type}">
      <input type="text" class="todo-add-title" id="todoTitle_${type}" placeholder="${t('todoAddTitle')}" maxlength="200">
      <textarea class="todo-add-content" id="todoContent_${type}" placeholder="${t('todoAddContent')}" rows="2"></textarea>
      <div class="todo-add-actions">
        <button class="todo-add-submit" id="todoSubmit_${type}">${t('todoAdd')}</button>
        <button class="todo-add-cancel" id="todoCancel_${type}">${t('todoCancel')}</button>
      </div>
    </div>
  `;
}

function todoAddBtn(type) {
  const isLoggedIn = typeof state !== 'undefined' && state.loggedIn;
  if (!isLoggedIn) return '';
  return `<button class="todo-add-btn" id="todoAddBtn_${type}">+ ${t('todoAdd')}</button>`;
}

async function renderTodosSection() {
  const section = $('#todosSection');
  const isLoggedIn = typeof state !== 'undefined' && state.loggedIn;

  section.innerHTML = `
    <div class="todos-panel">
      <div class="todo-column" id="todoBugCol">
        <h3><span data-i18n="knownBugs">已知Bug</span><span class="todo-count" id="todoBugCount"></span>${isLoggedIn ? `<span class="todo-add-btn-wrap" id="todoAddBtnWrap_bug">${todoAddBtn('bug')}</span>` : ''}</h3>
        <div class="todo-add-slot" id="todoAddSlot_bug"></div>
        <div class="todo-list" id="todoBugList"></div>
        <div class="todo-pagination" id="todoBugPag"></div>
      </div>
      <div class="todo-column" id="todoTodoCol">
        <h3><span data-i18n="todoItems">TODO</span><span class="todo-count" id="todoTodoCount"></span>${isLoggedIn ? `<span class="todo-add-btn-wrap" id="todoAddBtnWrap_todo">${todoAddBtn('todo')}</span>` : ''}</h3>
        <div class="todo-add-slot" id="todoAddSlot_todo"></div>
        <div class="todo-list" id="todoTodoList"></div>
        <div class="todo-pagination" id="todoTodoPag"></div>
      </div>
    </div>
  `;
  applyI18n();

  await loadTodoList('bug', todoBugPage);
  await loadTodoList('todo', todoTodoPage);

  if (isLoggedIn) {
    bindTodoAdd('bug');
    bindTodoAdd('todo');
  }
}

async function loadTodoList(type, page) {
  try {
    const r = await fetch(`/api/todos?type=${type}&page=${page}&per=${TODO_PER_PAGE}`);
    const data = await r.json();
    const items = data.items || [];
    const total = data.total || 0;
    const listEl = type === 'bug' ? $('#todoBugList') : $('#todoTodoList');
    const pagEl = type === 'bug' ? $('#todoBugPag') : $('#todoTodoPag');
    const countEl = type === 'bug' ? $('#todoBugCount') : $('#todoTodoCount');

    if (type === 'bug') todoBugTotal = total;
    else todoTodoTotal = total;

    const unresolvedCount = items.filter(i => !i.resolved).length;
    countEl.textContent = `(${unresolvedCount}/${total})`;

    const isLoggedIn = typeof state !== 'undefined' && state.loggedIn;

    if (items.length === 0) {
      listEl.innerHTML = `<div style="color:#9ca3af;padding:8px 0;font-size:0.8rem;">${t('noData')}</div>`;
    } else {
      listEl.innerHTML = items.map(item => `
        <div class="todo-item${item.resolved ? ' resolved' : ''}" data-id="${item.id}">
          ${isLoggedIn ? `<input type="checkbox" ${item.resolved ? 'checked' : ''} data-toggle-id="${item.id}">` : ''}
          <div class="todo-text">
            <span class="todo-title">${esc(item.title)}</span>
            ${item.content ? `<span class="todo-content">${esc(item.content)}</span>` : ''}
          </div>
          ${isLoggedIn ? `<button class="todo-delete" data-del-id="${item.id}">×</button>` : ''}
        </div>
      `).join('');
    }

    if (total > TODO_PER_PAGE) {
      const maxPage = Math.ceil(total / TODO_PER_PAGE);
      pagEl.innerHTML = `
        <button ${page <= 1 ? 'disabled' : ''} data-page="${page - 1}">«</button>
        <span class="page-info">${page}/${maxPage}</span>
        <button ${page >= maxPage ? 'disabled' : ''} data-page="${page + 1}">»</button>
      `;
    } else {
      pagEl.innerHTML = '';
    }

    if (isLoggedIn) {
      listEl.querySelectorAll('.todo-delete').forEach(btn => {
        btn.addEventListener('click', async () => {
          await todoDelete(btn.dataset.delId, type, page);
        });
      });
      listEl.querySelectorAll('input[type="checkbox"]').forEach(cb => {
        cb.addEventListener('change', async () => {
          await todoToggle(cb.dataset.toggleId, type, page);
        });
      });
      listEl.querySelectorAll('.todo-title').forEach(function (span) {
        span.addEventListener('dblclick', function () {
          makeTodoEditable(span, type, page);
        });
      });
      listEl.querySelectorAll('.todo-content').forEach(function (span) {
        span.addEventListener('dblclick', function () {
          makeTodoEditable(span, type, page);
        });
      });
    }

    pagEl.querySelectorAll('button').forEach(btn => {
      btn.addEventListener('click', () => {
        const p = parseInt(btn.dataset.page);
        if (type === 'bug') todoBugPage = p; else todoTodoPage = p;
        loadTodoList(type, p);
      });
    });
  } catch (e) {
    console.warn('Todo load failed:', e);
  }
}

function bindTodoAdd(type) {
  const addBtn = document.querySelector('#todoAddBtn_' + type);
  const slot = document.querySelector('#todoAddSlot_' + type);
  if (!addBtn || !slot) return;

  addBtn.addEventListener('click', () => {
    const existing = slot.querySelector('.todo-add-card');
    if (existing) { existing.remove(); return; }
    slot.innerHTML = todoAddCard(type);

    const titleEl = slot.querySelector('#todoTitle_' + type);
    const contentEl = slot.querySelector('#todoContent_' + type);
    const submitBtn = slot.querySelector('#todoSubmit_' + type);
    const cancelBtn = slot.querySelector('#todoCancel_' + type);

    titleEl.focus();

    const doAdd = async () => {
      const title = titleEl.value.trim();
      if (!title) return;
      const content = contentEl.value.trim();
      try {
        const r = await fetch('/api/todos', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', ...todoAuthHeaders() },
          body: JSON.stringify({ type, title, content }),
        });
        if (r.ok) {
          slot.innerHTML = '';
          if (type === 'bug') todoBugPage = 1; else todoTodoPage = 1;
          await loadTodoList(type, type === 'bug' ? todoBugPage : todoTodoPage);
          await fetchTodoCounts();
        } else if (r.status === 401) {
          alert(t('loginRequired'));
        }
      } catch (e) { /* ignore */ }
    };

    submitBtn.addEventListener('click', doAdd);
    titleEl.addEventListener('keydown', e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); doAdd(); } });
    cancelBtn.addEventListener('click', () => { slot.innerHTML = ''; });
  });
}

async function todoToggle(id, type, page) {
  try {
    const r = await fetch(`/api/todos/${id}`, { method: 'PUT', headers: todoAuthHeaders() });
    if (r.ok) {
      await loadTodoList(type, page);
      await fetchTodoCounts();
    }
  } catch (e) { /* ignore */ }
}

function makeTodoEditable(span, type, page) {
  var itemDiv = span.closest('.todo-item');
  if (!itemDiv) return;
  var id = itemDiv.dataset.id;
  var isContent = span.classList.contains('todo-content');
  var oldText = span.textContent;
  var input;
  if (isContent) {
    input = document.createElement('textarea');
    input.rows = 3;
  } else {
    input = document.createElement('input');
    input.type = 'text';
  }
  input.className = 'todo-edit-input';
  input.value = oldText;
  input.style.cssText = 'font-size:inherit;font-family:inherit;width:100%;padding:2px 4px;border:1px solid #6366f1;border-radius:4px;outline:none;background:var(--card-bg);color:var(--text);resize:vertical;';
  span.replaceWith(input);
  input.focus();
  input.select();
  var done = function () {
    var newText = input.value.trim();
    if (!newText || newText === oldText) { input.replaceWith(span); return; }
    span.textContent = newText;
    input.replaceWith(span);
    var body = {};
    body[isContent ? 'content' : 'title'] = newText;
    fetch('/api/todos/' + id, { method: 'PUT', headers: { 'Content-Type': 'application/json', ...todoAuthHeaders() }, body: JSON.stringify(body) }).catch(function () { });
  };
  input.addEventListener('blur', done);
  input.addEventListener('keydown', function (e) { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); input.blur(); } if (e.key === 'Escape') { input.value = oldText; input.blur(); } });
}

async function todoDelete(id, type, page) {
  if (!confirm(t('todoConfirmDelete'))) return;
  try {
    const r = await fetch(`/api/todos/${id}`, { method: 'DELETE', headers: todoAuthHeaders() });
    if (r.ok) {
      await loadTodoList(type, page);
      await fetchTodoCounts();
    }
  } catch (e) { /* ignore */ }
}
