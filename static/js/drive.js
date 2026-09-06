// drive.js — 网盘前端逻辑

var _driveCurrentPath = '';
var _driveSelectedFile = null;
var _driveSelectedSet = {}; // path → row element（多选）

function driveLoad(path) {
  _driveCurrentPath = path;
  driveUpdateBreadcrumb();
  var list = document.getElementById('driveFileList');
  if (!list) return;
  list.innerHTML = '<div style="text-align:center;padding:40px;"><loading-indicator></loading-indicator></div>';
  _driveSelectedFile = null;
  _driveSelectedSet = {};
  driveUpdateToolbar();

  fetch('/api/drive/list' + (path === '/' ? '' : path), { headers: getAuthHeaders() })
    .then(function (r) { if (!r.ok) throw new Error('HTTP ' + r.status); return r.json(); })
    .then(function (data) { renderDriveList(data); })
    .catch(function (e) {
      list.innerHTML = '<div style="text-align:center;padding:40px;color:#ef4444;">加载失败: ' + esc(e.message) + '</div>';
    });
}

function renderDriveList(data) {
  var list = document.getElementById('driveFileList');
  if (!list) return;
  var items = data.items || [];

  list.innerHTML = '';

  // 内联新建行（默认隐藏）
  var newRow = document.createElement('div');
  newRow.className = 'drive-row drive-new-row';
  newRow.id = 'driveNewRow';
  newRow.style.display = 'none';
  newRow.innerHTML = '<span class="drive-icon" id="driveNewIcon">📁</span>' +
    '<input type="text" class="drive-inline-input" id="driveNewInput" placeholder="输入名称…" onkeydown="if(event.key===\'Enter\')driveConfirmNew()" onblur="driveCancelNew()">' +
    '<button class="drive-btn drive-inline-ok" onmousedown="event.preventDefault();driveConfirmNew()">创建</button>';
  list.appendChild(newRow);

  if (items.length === 0) {
    list.innerHTML += '<div style="text-align:center;padding:60px;color:#9ca3af;"><div style="font-size:2rem;margin-bottom:8px;">📭</div>空文件夹</div>';
    return;
  }

  items.forEach(function (item) {
    var row = document.createElement('div');
    row.className = 'drive-row';
    row.dataset.path = item.path;
    row.dataset.type = item.type;
    row.addEventListener('click', function (e) { driveClickItem(row, e); });

    var icon = document.createElement('span');
    icon.className = 'drive-icon';
    icon.textContent = item.type === 'dir' ? '📁' : fileIcon(item.name);
    row.appendChild(icon);

    var nameEl = document.createElement('span');
    nameEl.className = 'drive-name';
    nameEl.textContent = item.name;
    row.appendChild(nameEl);

    if (item.type !== 'dir') {
      var sizeEl = document.createElement('span');
      sizeEl.className = 'drive-size';
      sizeEl.textContent = formatSize(item.size);
      row.appendChild(sizeEl);
    }

    if (item.updated_at) {
      var timeEl = document.createElement('span');
      timeEl.className = 'drive-time';
      timeEl.textContent = item.updated_at;
      row.appendChild(timeEl);
    }

    row.addEventListener('dblclick', function () { driveDblClick(row); });
    list.appendChild(row);
  });
}

function driveClickItem(el, e) {
  e = e || window.event;
  var path = el.dataset.path;

  if (e.ctrlKey || e.metaKey) {
    // Ctrl+: 切换选择
    if (_driveSelectedSet[path]) {
      delete _driveSelectedSet[path];
      el.classList.remove('drive-selected');
      if (_driveSelectedFile === el) _driveSelectedFile = null;
    } else {
      _driveSelectedSet[path] = el;
      el.classList.add('drive-selected');
      _driveSelectedFile = el;
    }
  } else if (e.shiftKey && _driveSelectedFile) {
    // Shift+: 连续选择
    var rows = document.querySelectorAll('#driveFileList .drive-row');
    var idxA = -1, idxB = -1;
    for (var i = 0; i < rows.length; i++) {
      if (rows[i] === _driveSelectedFile) idxA = i;
      if (rows[i] === el) idxB = i;
    }
    if (idxA >= 0 && idxB >= 0) {
      var lo = Math.min(idxA, idxB);
      var hi = Math.max(idxA, idxB);
      for (var j = lo; j <= hi; j++) {
        var r = rows[j];
        var p = r.dataset.path;
        if (!p || r.classList.contains('drive-new-row')) continue;
        _driveSelectedSet[p] = r;
        r.classList.add('drive-selected');
      }
    }
  } else {
    // 普通单击: 单选
    var hadSelection = Object.keys(_driveSelectedSet).length > 0;
    driveClearSelection();
    _driveSelectedSet[path] = el;
    _driveSelectedFile = el;
    el.classList.add('drive-selected');
  }
  driveUpdateToolbar();
}

function driveClearSelection() {
  Object.keys(_driveSelectedSet).forEach(function (p) {
    var el = _driveSelectedSet[p];
    if (el) el.classList.remove('drive-selected');
  });
  _driveSelectedSet = {};
  _driveSelectedFile = null;
}

function driveDblClick(el) {
  var type = el.dataset.type;
  var path = el.dataset.path;
  if (type === 'dir') {
    driveLoad('/' + path);
  } else {
    driveViewFile(path);
  }
}

// ========== 内联新建 ==========

function driveShowNewRow(type) {
  var row = document.getElementById('driveNewRow');
  var input = document.getElementById('driveNewInput');
  var icon = document.getElementById('driveNewIcon');
  if (!row || !input || !icon) return;
  row.style.display = '';
  row.dataset.newType = type;
  icon.textContent = type === 'folder' ? '📁' : '📄';
  input.value = '';
  input.placeholder = type === 'folder' ? '文件夹名称' : '文件名';
  input.focus();
}

function driveCancelNew() {
  var row = document.getElementById('driveNewRow');
  if (row) {
    // 延迟关闭，避免 blur 在 mousedown 之前触发导致 onclick 失效
    setTimeout(function () {
      if (row.style.display !== 'none') row.style.display = 'none';
    }, 150);
  }
}

function driveConfirmNew() {
  var row = document.getElementById('driveNewRow');
  var input = document.getElementById('driveNewInput');
  if (!row || !input) return;
  var name = (input.value || '').trim();
  if (!name) { row.style.display = 'none'; return; }

  var type = row.dataset.newType || 'folder';
  row.style.display = 'none';

  var prefix = _driveCurrentPath === '/' ? '' : _driveCurrentPath.replace(/\/$/, '');
  var fullName = prefix + '/' + name;

  if (type === 'folder') {
    fetch('/api/drive/mkdir', { method: 'POST', headers: getAuthHeaders(), body: JSON.stringify({ path: fullName }) })
      .then(function (r) { if (r.ok) driveLoad(_driveCurrentPath); else alert('创建失败'); })
      .catch(function () { alert('创建失败'); });
  } else {
    // 新建空文件
    var token = localStorage.getItem('modlist_token');
    var headers = token ? { 'Authorization': 'Bearer ' + token } : {};
    var formData = new FormData();
    formData.append('path', _driveCurrentPath === '/' ? '' : _driveCurrentPath);
    formData.append('files', new Blob([''], { type: 'text/plain' }), name);
    fetch('/api/drive/upload', { method: 'POST', headers: headers, body: formData })
      .then(function (r) { if (r.ok) driveLoad(_driveCurrentPath); else alert('创建失败'); })
      .catch(function () { alert('创建失败'); });
  }
}

// ========== 内联重命名 ==========

function driveStartRename() {
  if (!_driveSelectedFile) return;
  var nameEl = _driveSelectedFile.querySelector('.drive-name');
  if (!nameEl) return;

  var oldName = nameEl.textContent;
  var input = document.createElement('input');
  input.type = 'text';
  input.className = 'drive-inline-input drive-rename-input';
  input.value = oldName;
  input.dataset.oldName = oldName;
  input._handled = false;
  nameEl.replaceWith(input);
  input.focus();
  input.select();

  input.addEventListener('keydown', function (e) {
    if (e.key === 'Enter') { input._handled = true; driveConfirmRename(input); }
    else if (e.key === 'Escape') { input._handled = true; driveCancelRename(input); }
  });
  input.addEventListener('blur', function () {
    if (!input._handled) driveCancelRename(input);
  });
}

function driveCancelRename(input) {
  if (!input || !input.dataset.oldName) return;
  if (!input.parentElement) return;
  var oldName = input.dataset.oldName;
  var span = document.createElement('span');
  span.className = 'drive-name';
  span.textContent = oldName;
  try { input.replaceWith(span); } catch (e) { /* already replaced */ }
}

function driveConfirmRename(input) {
  if (!input || !input.parentElement) return;
  var row = input.closest('.drive-row');
  if (!row) return;
  var oldPath = row.dataset.path;
  var oldName = input.dataset.oldName || '';
  var newName = (input.value || '').trim();
  if (!newName || newName === oldName) { driveCancelRename(input); return; }

  fetch('/api/drive/rename', { method: 'POST', headers: getAuthHeaders(), body: JSON.stringify({ old_path: oldPath, new_name: newName }) })
    .then(function (r) { if (!r.ok) throw new Error('rename failed'); return r.json(); })
    .then(function () {
      // 更新行数据
      var parts = oldPath.split('/');
      parts[parts.length - 1] = newName;
      row.dataset.path = parts.join('/');
      var span = document.createElement('span');
      span.className = 'drive-name';
      span.textContent = newName;
      input.replaceWith(span);
    })
    .catch(function () { driveCancelRename(input); });
}

// ========== 下载 / 预览 / 上传 / 删除 ==========

function driveDownloadSelected() {
  if (!_driveSelectedFile) return;
  if (_driveSelectedFile.dataset.type !== 'file') return;
  driveDownload(_driveSelectedFile.dataset.path);
}

function driveDownload(path) {
  var token = localStorage.getItem('modlist_token');
  var link = document.createElement('a');
  link.href = '/api/drive/file?path=' + encodeURIComponent(path) + '&action=download&token=' + encodeURIComponent(token || '');
  link.download = (path || '').split('/').pop();
  document.body.appendChild(link);
  link.click();
  document.body.removeChild(link);
}

function driveViewFile(path) {
  var ext = (path || '').split('.').pop().toLowerCase();
  var textExts = ['txt', 'md', 'json', 'yml', 'yaml', 'xml', 'html', 'css', 'js', 'ts',
    'java', 'kt', 'py', 'go', 'rs', 'c', 'cpp', 'h', 'cfg', 'toml', 'ini', 'properties',
    'log', 'csv', 'gradle', 'sh', 'bat', 'ps1', 'sql', 'cmd'];
  if (textExts.indexOf(ext) === -1) { driveDownload(path); return; }

  fetch('/api/drive/file?path=' + encodeURIComponent(path) + '&action=view', { headers: getAuthHeaders() })
    .then(function (r) { if (!r.ok) throw new Error('预览失败'); return r.json(); })
    .then(function (data) { showDrivePreview(data.name, data.content); })
    .catch(function (e) { alert('预览失败: ' + (e.message || '')); });
}

function showDrivePreview(name, content) {
  var overlay = document.getElementById('drivePreviewOverlay');
  if (!overlay) return;
  document.getElementById('drivePreviewTitle').textContent = name;
  document.getElementById('drivePreviewContent').textContent = content;
  document.getElementById('drivePreviewContent').style.display = '';
  document.getElementById('driveEditTextarea').style.display = 'none';
  document.getElementById('driveEditBar').style.display = 'none';
  var editBtn = document.getElementById('drivePreviewEditBtn');
  if (editBtn) editBtn.style.display = '';
  overlay.style.display = '';
  overlay.onclick = function (e) { if (e.target === overlay) closeDrivePreview(); };
}

function closeDrivePreview() {
  var overlay = document.getElementById('drivePreviewOverlay');
  if (overlay) overlay.style.display = 'none';
  _driveEditPath = null;
}

// ========== 文件编辑 ==========

var _driveEditPath = null;

function driveEditSelected() {
  if (!_driveSelectedFile) return;
  if (_driveSelectedFile.dataset.type !== 'file') return;
  var path = _driveSelectedFile.dataset.path;
  // 先加载预览，再切换编辑模式
  var ext = (path || '').split('.').pop().toLowerCase();
  var textExts = ['txt', 'md', 'json', 'yml', 'yaml', 'xml', 'html', 'css', 'js', 'ts',
    'java', 'kt', 'py', 'go', 'rs', 'c', 'cpp', 'h', 'cfg', 'toml', 'ini', 'properties',
    'log', 'csv', 'gradle', 'sh', 'bat', 'ps1', 'sql', 'cmd'];
  if (textExts.indexOf(ext) === -1) { alert('此文件类型不支持编辑'); return; }

  fetch('/api/drive/file?path=' + encodeURIComponent(path) + '&action=view', { headers: getAuthHeaders() })
    .then(function (r) { if (!r.ok) throw new Error('加载失败'); return r.json(); })
    .then(function (data) {
      showDrivePreview(data.name, data.content);
      driveStartEdit();
    })
    .catch(function (e) { alert('无法打开: ' + (e.message || '')); });
}

function driveStartEdit() {
  var overlay = document.getElementById('drivePreviewOverlay');
  if (!overlay || overlay.style.display === 'none') return;
  var name = document.getElementById('drivePreviewTitle').textContent;
  var path = (_driveSelectedFile && _driveSelectedFile.dataset.type === 'file') ? _driveSelectedFile.dataset.path : null;
  if (!path) return;
  _driveEditPath = path;

  document.getElementById('drivePreviewContent').style.display = 'none';
  var btn = document.getElementById('drivePreviewEditBtn');
  if (btn) btn.style.display = 'none';
  var textarea = document.getElementById('driveEditTextarea');
  textarea.value = document.getElementById('drivePreviewContent').textContent;
  textarea.style.display = '';
  document.getElementById('driveEditBar').style.display = '';
  textarea.focus();
}

function driveCancelEdit() {
  document.getElementById('drivePreviewContent').style.display = '';
  document.getElementById('driveEditTextarea').style.display = 'none';
  document.getElementById('driveEditBar').style.display = 'none';
  var btn = document.getElementById('drivePreviewEditBtn');
  if (btn) btn.style.display = '';
  _driveEditPath = null;
}

function driveSaveEdit() {
  if (!_driveEditPath) return;
  var content = document.getElementById('driveEditTextarea').value;
  fetch('/api/drive/save', { method: 'POST', headers: getAuthHeaders(), body: JSON.stringify({ path: _driveEditPath, content: content }) })
    .then(function (r) { if (!r.ok) throw new Error('保存失败'); return r.json(); })
    .then(function () {
      // 更新预览内容
      document.getElementById('drivePreviewContent').textContent = content;
      driveCancelEdit();
    })
    .catch(function (e) { alert('保存失败: ' + (e.message || '')); });
}

function driveUpload(input) {
  var files = input.files;
  if (!files || files.length === 0) return;
  var formData = new FormData();
  formData.append('path', _driveCurrentPath === '/' ? '' : _driveCurrentPath);
  for (var i = 0; i < files.length; i++) formData.append('files', files[i]);
  var token = localStorage.getItem('modlist_token');
  var headers = token ? { 'Authorization': 'Bearer ' + token } : {};
  fetch('/api/drive/upload', { method: 'POST', headers: headers, body: formData })
    .then(function (r) { if (r.ok) return r.json(); throw new Error('upload failed'); })
    .then(function () { driveLoad(_driveCurrentPath); })
    .catch(function (e) { alert('上传失败: ' + (e.message || '')); });
  input.value = '';
}

function driveDeleteSelected() {
  var paths = Object.keys(_driveSelectedSet);
  if (paths.length === 0) return;
  var label = paths.length === 1
    ? (_driveSelectedSet[paths[0]].querySelector('.drive-name')?.textContent || paths[0])
    : paths.length + ' 个项目';
  if (!confirm('确定删除 ' + label + '？此操作不可撤销。')) return;

  var done = 0;
  var fail = 0;
  function next() {
    if (done + fail >= paths.length) {
      driveClearSelection();
      driveUpdateToolbar();
      driveLoad(_driveCurrentPath);
      return;
    }
    var p = paths[done + fail];
    fetch('/api/drive/delete', { method: 'POST', headers: getAuthHeaders(), body: JSON.stringify({ path: p }) })
      .then(function (r) { if (r.ok) { done++; } else { fail++; } next(); })
      .catch(function () { fail++; next(); });
  }
  next();
}

// ========== 工具栏 ==========

function driveUpdateToolbar() {
  var dBtn = document.getElementById('driveDeleteBtn');
  var dlBtn = document.getElementById('driveDownloadBtn');
  var rBtn = document.getElementById('driveRenameBtn');
  var eBtn = document.getElementById('driveEditBtn');
  var cBtn = document.getElementById('driveClearBtn');
  var cnt = Object.keys(_driveSelectedSet).length;
  var hasSelection = cnt > 0;
  var isSingle = cnt === 1;
  var isSingleFile = isSingle && _driveSelectedFile && _driveSelectedFile.dataset.type === 'file';
  if (dBtn) dBtn.style.display = hasSelection ? '' : 'none';
  if (dlBtn) dlBtn.style.display = isSingleFile ? '' : 'none';
  if (rBtn) rBtn.style.display = isSingle ? '' : 'none';
  if (eBtn) eBtn.style.display = isSingleFile ? '' : 'none';
  if (cBtn) cBtn.style.display = hasSelection ? '' : 'none';
  // 更新删除按钮文字显示数量
  if (dBtn && cnt > 1) dBtn.textContent = '🗑 删除(' + cnt + ')';
  else if (dBtn) dBtn.textContent = '🗑 删除';
}

function driveUpdateBreadcrumb() {
  var bc = document.getElementById('driveBreadcrumb');
  if (!bc) return;
  var parts = _driveCurrentPath.replace(/\/$/, '').split('/').filter(Boolean);
  var html = "<a class='drive-crumb' onclick=\"driveLoad('/')\">📂 根目录</a>";
  var accum = '';
  parts.forEach(function (p) {
    accum += '/' + p;
    html += ' / <a class="drive-crumb" onclick="driveLoad(' + "'" + accum + "'" + ')">' + esc(p) + '</a>';
  });
  bc.innerHTML = html;
}

// ========== 工具函数 ==========

function fileIcon(name) {
  var ext = (name || '').split('.').pop().toLowerCase();
  var map = {
    'md': '📝', 'txt': '📄', 'json': '📋', 'yml': '⚙️', 'yaml': '⚙️',
    'xml': '📰', 'html': '🌐', 'css': '🎨', 'js': '📜', 'ts': '📘',
    'java': '☕', 'kt': '🅱️', 'py': '🐍', 'go': '🔹', 'rs': '🦀',
    'c': '🔧', 'cpp': '🔧', 'h': '🔧', 'cfg': '⚙️', 'toml': '⚙️',
    'ini': '⚙️', 'properties': '⚙️', 'log': '📊', 'csv': '📊',
    'gradle': '🐘', 'sh': '💻', 'bat': '💻', 'ps1': '💻',
    'png': '🖼️', 'jpg': '🖼️', 'jpeg': '🖼️', 'gif': '🖼️', 'svg': '🖼️',
    'webp': '🖼️', 'ico': '🖼️',
    'zip': '📦', 'jar': '📦', 'tar': '📦', 'gz': '📦', 'rar': '📦', '7z': '📦',
    'pdf': '📕', 'doc': '📘', 'docx': '📘', 'xls': '📗', 'ppt': '📙',
    'mp3': '🎵', 'wav': '🎵', 'ogg': '🎵', 'flac': '🎵',
    'mp4': '🎬', 'avi': '🎬', 'mkv': '🎬',
    'ttf': '🔤', 'otf': '🔤', 'woff': '🔤', 'woff2': '🔤',
  };
  return map[ext] || '📄';
}

function formatSize(bytes) {
  if (!bytes || bytes <= 0) return '0 B';
  var units = ['B', 'KB', 'MB', 'GB', 'TB'];
  var i = Math.floor(Math.log(bytes) / Math.log(1024));
  if (i >= units.length) i = units.length - 1;
  return (bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1) + ' ' + units[i];
}
