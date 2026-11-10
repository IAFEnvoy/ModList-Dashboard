(() => {
  'use strict';

  const state = {
    items: [],
    currentId: '',
    isNew: false,
    dirty: false,
    formSnapshot: null,
    statusSet: new Set(['']),
    statusColors: {},
    loaders: [],
    loaderColors: {},
    versionOptions: [],
    versionOptionValues: new Set(),
    presets: { license: [], architecture: [], tags: [] },
  };
  const $ = id => document.getElementById(id);
  let messageTimer = null;
  let fadeTimer = null;

  function authHeaders(json) {
    const token = localStorage.getItem('modlist_token') || '';
    const headers = { Authorization: 'Bearer ' + token };
    if (json) headers['Content-Type'] = 'application/json';
    return headers;
  }

  async function readResponse(response) {
    const text = await response.text();
    let data = null;
    if (text.trim()) {
      try { data = JSON.parse(text); } catch (_) {
        throw new Error(response.ok ? '服务器返回了无效 JSON' : text.trim());
      }
    }
    if (!response.ok) throw new Error(data?.error || data?.message || '请求失败 (' + response.status + ')');
    return data;
  }

  // persist 为 true 时消息不会自动消失，直到被新消息替换或 message() 手动关闭
  function message(text, error = false, persist = false) {
    const el = $('modEditorMessage');
    clearTimeout(messageTimer);
    clearTimeout(fadeTimer);
    if (!text) {
      el.hidden = true;
      el.classList.remove('fade-out');
      return;
    }
    el.textContent = text;
    el.classList.toggle('error', error);
    el.classList.remove('fade-out');
    el.hidden = false;
    if (!persist) {
      messageTimer = setTimeout(() => {
        el.classList.add('fade-out');
        fadeTimer = setTimeout(() => {
          el.hidden = true;
          el.classList.remove('fade-out');
          fadeTimer = null;
        }, 250);
      }, 2600);
    }
  }

  function values(id) {
    return $(id).value.split(',').map(value => value.trim()).filter(Boolean);
  }

  // ---- 多选 chip 字段（架构 / 标签 / 合作作者）----
  function chipFieldIds() {
    return ['modEditArch', 'modEditTags', 'modEditCoop'];
  }

  function chipValues(hiddenId) {
    return ($(hiddenId).value || '').split(',').map(value => value.trim()).filter(Boolean);
  }

  function setChipValue(hiddenId, items) {
    $(hiddenId).value = items.join(', ');
    renderChips(hiddenId);
  }

  function renderChips(hiddenId) {
    const box = $(hiddenId + 'Chips');
    box.textContent = '';
    chipValues(hiddenId).forEach(item => {
      const chip = document.createElement('span');
      chip.className = 'mod-editor-chip';
      const text = document.createElement('span');
      text.textContent = item;
      const remove = document.createElement('button');
      remove.type = 'button';
      remove.className = 'mod-editor-chip-x';
      remove.textContent = '×';
      remove.title = '删除';
      remove.setAttribute('aria-label', '删除 ' + item);
      remove.addEventListener('click', event => {
        event.preventDefault();
        event.stopPropagation();
        setChipValue(hiddenId, chipValues(hiddenId).filter(value => value !== item));
        markDirtyUI();
      });
      chip.appendChild(text);
      chip.appendChild(remove);
      box.appendChild(chip);
    });
  }

  function refreshChipFields() {
    chipFieldIds().forEach(renderChips);
  }

  function addChipFromInput(hiddenId) {
    const input = $(hiddenId + 'Pick');
    const value = input.value.trim();
    input.value = '';
    addChipValue(hiddenId, value);
  }

  function addChipValue(hiddenId, value) {
    value = (value || '').trim();
    if (!value) return;
    const items = chipValues(hiddenId);
    if (!items.includes(value)) {
      items.push(value);
      setChipValue(hiddenId, items);
      markDirtyUI();
    }
  }

  // 自绘预填下拉：架构/标签/合作作者多选，可点选（chips）也可手输后回车/逗号添加
  function attachChipPicker(hiddenId, optionsGetter) {
    const input = $(hiddenId + 'Pick');
    const picker = input.closest('.mod-editor-picker');
    const list = document.createElement('div');
    list.className = 'mod-editor-suggest-list';
    list.hidden = true;
    picker.appendChild(list);
    let active = -1;

    function filterOptions() {
      const options = (optionsGetter ? (optionsGetter() || []) : []).slice();
      const picked = new Set(chipValues(hiddenId));
      const keyword = input.value.trim().toLowerCase();
      return options.filter(value => !picked.has(value) && (!keyword || value.toLowerCase().includes(keyword))).slice(0, 50);
    }

    function renderList() {
      const shown = filterOptions();
      list.textContent = '';
      active = -1;
      if (!shown.length) { list.hidden = true; return; }
      shown.forEach(value => {
        const item = document.createElement('button');
        item.type = 'button';
        item.className = 'mod-editor-suggest-item';
        item.textContent = value;
        item.addEventListener('pointerdown', event => event.preventDefault());
        item.addEventListener('click', () => {
          addChipValue(hiddenId, value);
          input.value = '';
          input.focus();
        });
        list.appendChild(item);
      });
      list.hidden = false;
    }

    function setActive(index) {
      const items = list.querySelectorAll('.mod-editor-suggest-item');
      if (!items.length) return;
      active = Math.max(0, Math.min(items.length - 1, index));
      items.forEach((item, i) => item.classList.toggle('active', i === active));
      items[active].scrollIntoView({ block: 'nearest' });
    }

    input.addEventListener('focus', renderList);
    input.addEventListener('input', renderList);
    input.addEventListener('keydown', event => {
      if (!list.hidden && event.key === 'ArrowDown') { event.preventDefault(); setActive(active + 1); return; }
      if (!list.hidden && event.key === 'ArrowUp') { event.preventDefault(); setActive(active - 1); return; }
      if (event.key === 'Escape') { list.hidden = true; return; }
      if (event.key === 'Enter' || event.key === ',') {
        event.preventDefault();
        if (!list.hidden && active >= 0) { list.children[active].click(); return; }
        addChipFromInput(hiddenId);
        renderList();
      }
    });
    picker.addEventListener('click', event => {
      if (event.target.closest('.mod-editor-chip-x')) return;
      input.focus();
      renderList();
    });
    document.addEventListener('pointerdown', event => {
      if (!picker.contains(event.target)) list.hidden = true;
    });
  }

  // 自绘预填下拉：许可证单选，可点选也可手输自定义值
  function attachLicensePicker(inputId, optionsGetter) {
    const input = $(inputId);
    const wrap = input.closest('.mod-editor-suggest');
    const list = document.createElement('div');
    list.className = 'mod-editor-suggest-list';
    list.hidden = true;
    wrap.appendChild(list);
    let active = -1;

    function renderList() {
      const keyword = input.value.trim().toLowerCase();
      const shown = (optionsGetter() || []).filter(value => !keyword || value.toLowerCase().includes(keyword)).slice(0, 50);
      list.textContent = '';
      active = -1;
      if (!shown.length) { list.hidden = true; return; }
      shown.forEach(value => {
        const item = document.createElement('button');
        item.type = 'button';
        item.className = 'mod-editor-suggest-item';
        item.textContent = value;
        item.addEventListener('pointerdown', event => event.preventDefault());
        item.addEventListener('click', event => {
          event.stopPropagation();
          if (input.value !== value) {
            input.value = value;
            markDirtyUI();
          }
          input.classList.remove('invalid');
          list.hidden = true;
          input.focus();
        });
        list.appendChild(item);
      });
      list.hidden = false;
    }

    function setActive(index) {
      const items = list.querySelectorAll('.mod-editor-suggest-item');
      if (!items.length) return;
      active = Math.max(0, Math.min(items.length - 1, index));
      items.forEach((item, i) => item.classList.toggle('active', i === active));
      items[active].scrollIntoView({ block: 'nearest' });
    }

    input.addEventListener('focus', renderList);
    input.addEventListener('input', renderList);
    input.addEventListener('keydown', event => {
      if (!list.hidden && event.key === 'ArrowDown') { event.preventDefault(); setActive(active + 1); return; }
      if (!list.hidden && event.key === 'ArrowUp') { event.preventDefault(); setActive(active - 1); return; }
      if (event.key === 'Escape') { list.hidden = true; return; }
      if (event.key === 'Enter' && !list.hidden) {
        event.preventDefault();
        if (active >= 0) list.children[active].click();
      }
    });
    document.addEventListener('pointerdown', event => {
      if (!wrap.contains(event.target)) list.hidden = true;
    });
  }

  // 从 /api/config 填充预填下拉：许可证来自 data/license/ 目录，架构与标签来自 config/editor.yml
  function configureFieldPresets(config) {
    state.presets.license = (config && config.license_options) || [];
    state.presets.architecture = (config && config.architecture_options) || [];
    state.presets.tags = (config && config.tag_options) || [];
  }

  function setLogoState(logoName, modID) {
    const preview = $('modEditLogoPreview');
    if (preview.src && preview.src.startsWith('blob:')) URL.revokeObjectURL(preview.src);
    preview.removeAttribute('src');
    $('modEditLogo').value = logoName || '';
    $('modEditLogoName').textContent = logoName || '未设置';
    $('modEditLogoFile').value = '';
    $('modEditLogoBtn').textContent = logoName ? '更换图片' : '选择图片';
    preview.hidden = !logoName;
    if (logoName) preview.src = '/api/logo/' + encodeURIComponent(modID || state.currentId || '');
  }

  function renderList() {
    const query = $('modEditorSearch').value.trim().toLowerCase();
    const list = $('modEditorList');
    const items = state.items.filter(item => !query || (item.name + ' ' + item.id).toLowerCase().includes(query));
    $('modEditorCount').textContent = state.items.length + ' 个模组';
    list.textContent = '';
    if (!items.length) {
      list.textContent = '没有匹配的模组';
      return;
    }
    items.forEach(item => {
      const button = document.createElement('button');
      button.type = 'button';
      const active = item.id === state.currentId;
      button.className = 'mod-editor-list-item' + (active ? ' active' : '') + (active && state.dirty ? ' mod-editor-list-item-dirty' : '');
      button.innerHTML = '<span class="mod-editor-list-name"></span><span class="mod-editor-list-id"></span>';
      button.querySelector('.mod-editor-list-name').textContent = item.name || item.id;
      button.querySelector('.mod-editor-list-id').textContent = item.id;
      button.addEventListener('click', () => selectItem(item.id));
      list.appendChild(button);
    });
  }

  function populateStatusSelect(statuses, colors) {
    const select = $('modEditStatus');
    select.textContent = '';
    state.statusColors = colors || {};
    const empty = document.createElement('option');
    empty.value = '';
    empty.textContent = '未设置';
    select.appendChild(empty);
    state.statusSet = new Set(['']);
    (statuses || []).forEach(status => {
      const option = document.createElement('option');
      option.value = status;
      option.textContent = status;
      option.style.color = state.statusColors[status] || '';
      select.appendChild(option);
      state.statusSet.add(status);
    });
    syncStatusColor(select);
  }

  function syncStatusColor(select) {
    select = select || $('modEditStatus');
    select.style.color = state.statusColors[select.value] || '';
  }

  function setStatusValue(status) {
    const select = $('modEditStatus');
    if (status && !state.statusSet.has(status)) {
      const option = document.createElement('option');
      option.value = status;
      option.textContent = status;
      select.appendChild(option);
      state.statusSet.add(status);
    }
    select.value = status;
    syncStatusColor(select);
  }

  function configureVersionColumns(loaders, colors, options) {
    state.loaders = loaders || [];
    state.loaderColors = colors || {};
    state.versionOptions = options || [];
    state.versionOptionValues = new Set(state.versionOptions.map(option => option.value));
    renderVersionHead();
  }

  function renderVersionHead() {
    const head = $('modEditVersionHead');
    head.textContent = '';
    head.style.gridTemplateColumns = versionGridTemplate();
    ['版本'].concat(state.loaders, ['']).forEach(label => {
      const cell = document.createElement('span');
      cell.textContent = label;
      if (state.loaderColors[label]) cell.style.color = state.loaderColors[label];
      head.appendChild(cell);
    });
  }

  function versionGridTemplate() {
    const loaderColumns = state.loaders.map(() => 'minmax(96px, 1fr)').join(' ');
    return 'minmax(120px, .8fr) ' + loaderColumns + ' 84px';
  }

  function renderVersions(versions) {
    const container = $('modEditVersionsRows');
    container.textContent = '';
    Object.keys(versions || {}).sort().forEach(version => addVersionRow(version, versions[version] || {}));
    if (!container.children.length) addVersionRow('', {});
  }

  function addVersionRow(version, loaders) {
    const row = document.createElement('div');
    row.className = 'mod-editor-version-row';
    row.style.gridTemplateColumns = versionGridTemplate();
    const versionInput = document.createElement('input');
    versionInput.className = 'version-name';
    versionInput.autocomplete = 'off';
    versionInput.spellcheck = false;
    versionInput.placeholder = '版本，例如 1.21.1';
    versionInput.value = version || '';
    row.appendChild(versionInput);
    state.loaders.forEach(loader => {
      const select = document.createElement('select');
      select.className = 'version-loader';
      select.dataset.loader = loader;
      select.autocomplete = 'off';
      const empty = document.createElement('option');
      empty.value = '';
      empty.textContent = '未指定';
      select.appendChild(empty);
      state.versionOptions.forEach(option => {
        const item = document.createElement('option');
        item.value = option.value;
        item.textContent = (option.emoji ? option.emoji + ' ' : '') + (option.text || option.value);
        select.appendChild(item);
      });
      const stored = (loaders || {})[loader];
      if (stored && state.versionOptionValues.has(stored)) select.value = stored;
      row.appendChild(select);
    });
    const remove = document.createElement('button');
    remove.type = 'button';
    remove.className = 'mod-editor-danger';
    remove.textContent = '删除';
    remove.addEventListener('click', () => {
      const rows = $('modEditVersionsRows').querySelectorAll('.mod-editor-version-row');
      if (rows.length <= 1) {
        versionInput.value = '';
        row.querySelectorAll('select').forEach(select => { select.value = ''; });
      } else {
        row.remove();
      }
      markDirtyUI();
    });
    row.appendChild(remove);
    $('modEditVersionsRows').appendChild(row);
  }

  function readVersions() {
    const versions = {};
    $('modEditVersionsRows').querySelectorAll('.mod-editor-version-row').forEach(row => {
      const version = row.querySelector('.version-name').value.trim();
      if (!version) return;
      const loaders = {};
      row.querySelectorAll('.version-loader').forEach(select => {
        const loader = select.dataset.loader;
        const status = select.value;
        if (loader && status) loaders[loader] = status;
      });
      versions[version] = loaders;
    });
    return versions;
  }

  function fillForm(meta) {
    state.currentId = meta.id || '';
    state.isNew = false;
    $('modEditorTitle').textContent = meta.name ? '编辑：' + meta.name : '编辑模组';
    $('modEditId').value = meta.id || '';
    $('modEditId').readOnly = false; // mod_id 可修改：底层主键已是自增 id，保存时由后端执行改名
    $('modEditName').value = meta.name || '';
    $('modEditDescription').value = meta.description || '';
    setLogoState(meta.logo || '', meta.id);
    setStatusValue(meta.status || '');
    $('modEditGH').value = meta.ids?.gh || '';
    $('modEditCF').value = meta.ids?.cf || '';
    $('modEditWiki').value = meta.ids?.wiki || '';
    $('modEditPackage').value = meta.mod_meta?.package || '';
    $('modEditLicense').value = meta.mod_meta?.license || '';
    $('modEditArch').value = (meta.mod_meta?.architecture || []).join(', ');
    $('modEditTags').value = (meta.tags || []).join(', ');
    $('modEditCoop').value = (meta.coop || []).join(', ');
    $('modEditorDelete').disabled = false;
    renderVersions(meta.versions || {});
    refreshChipFields();
    captureSnapshot();
    renderList();
  }

  function newForm() {
    state.currentId = '';
    state.isNew = true;
    $('modEditorTitle').textContent = '新建模组';
    $('modEditorForm').reset();
    $('modEditId').readOnly = false;
    $('modEditorDelete').disabled = true;
    setLogoState('', '');
    renderVersions({});
    // form.reset() 不会重置 type=hidden 的 chip 字段，需显式清空
    chipFieldIds().forEach(hiddenId => { $(hiddenId).value = ''; });
    refreshChipFields();
    captureSnapshot();
    renderList();
    $('modEditId').focus();
  }

  function formData() {
    return {
      id: $('modEditId').value.trim(),
      name: $('modEditName').value.trim(),
      description: $('modEditDescription').value,
      logo: $('modEditLogo').value.trim(),
      mod_meta: { package: $('modEditPackage').value.trim(), license: $('modEditLicense').value.trim(), architecture: values('modEditArch') },
      ids: { gh: $('modEditGH').value.trim(), cf: $('modEditCF').value.trim(), wiki: $('modEditWiki').value.trim() },
      tags: values('modEditTags'),
      versions: readVersions(),
      status: $('modEditStatus').value.trim(),
      coop: values('modEditCoop'),
    };
  }

  function formIsDirty() {
    return state.formSnapshot !== null && JSON.stringify(formData()) !== state.formSnapshot;
  }

  function captureSnapshot() {
    state.formSnapshot = JSON.stringify(formData());
    state.dirty = false;
  }

  function markDirtyUI() {
    const dirty = formIsDirty();
    if (dirty === state.dirty) return;
    state.dirty = dirty;
    const active = $('modEditorList').querySelector('.mod-editor-list-item.active');
    if (active) active.classList.toggle('mod-editor-list-item-dirty', dirty);
  }

  // 若有未保存的修改，弹出确认；返回 true 表示可以继续
  function confirmDiscard(actionLabel) {
    if (!state.dirty) return true;
    return window.confirm('当前有未保存的修改，' + actionLabel + ' 将丢失这些修改。确定继续吗？');
  }

  // 基本信息（ID / 名称 / Logo / 状态 / 描述）必须全部填写
  function validateBasicInfo() {
    const fields = [
      { id: 'modEditId', label: 'ID' },
      { id: 'modEditName', label: '名称' },
      { id: 'modEditLogo', label: 'Logo' },
      { id: 'modEditStatus', label: '状态' },
      { id: 'modEditDescription', label: '描述' },
    ];
    const missing = [];
    fields.forEach(field => {
      const el = $(field.id);
      const empty = !el.value.trim();
      el.classList.toggle('invalid', empty);
      if (empty) missing.push(field.label);
    });
    if (missing.length) return '请填写：' + missing.join('、');
    const idValue = $('modEditId').value.trim();
    if (!/^[A-Za-z0-9._-]+$/.test(idValue)) {
      $('modEditId').classList.add('invalid');
      return 'ID 只能包含字母、数字、点、下划线和连字符';
    }
    return '';
  }

  async function save(event) {
    event.preventDefault();
    const validationError = validateBasicInfo();
    if (validationError) {
      message(validationError, true);
      const invalidEls = [...document.querySelectorAll('.mod-editor-grid .invalid')];
      const firstVisible = invalidEls.find(el => el.type !== 'hidden');
      if (firstVisible) firstVisible.focus();
      else if (invalidEls.length) $('modEditLogoBtn').focus();
      return;
    }
    const meta = formData();
    message('保存中...', false, true);
    const url = state.isNew ? '/api/admin/mods' : '/api/admin/mods/' + encodeURIComponent(state.currentId);
    try {
      const response = await fetch(url, { method: state.isNew ? 'POST' : 'PUT', headers: authHeaders(true), body: JSON.stringify(meta) });
      const data = await readResponse(response);
      const logoFile = $('modEditLogoFile').files && $('modEditLogoFile').files[0];
      if (logoFile) {
        const uploadUrl = '/api/admin/mods/' + encodeURIComponent(data.id) + '/logo';
        const token = localStorage.getItem('modlist_token') || '';
        const uploadResponse = await fetch(uploadUrl, {
          method: 'PUT',
          headers: { 'Content-Type': logoFile.type || 'application/octet-stream', 'Authorization': 'Bearer ' + token },
          body: logoFile,
        });
        const uploadResult = await readResponse(uploadResponse);
        if (uploadResult && uploadResult.logo) data.logo = uploadResult.logo;
      }
      const oldId = state.currentId;
      if (state.isNew) {
        state.items.push(data);
      } else {
        const index = state.items.findIndex(item => item.id === oldId);
        if (index >= 0) state.items[index] = data; else state.items.push(data);
      }
      const renamed = !state.isNew && oldId !== data.id;
      fillForm(data);
      message(renamed ? '已保存（ID: ' + oldId + ' → ' + data.id + '）' : '已保存');
    } catch (error) { message(error.message, true); }
  }

  async function remove() {
    if (!state.currentId || !window.confirm('确定删除这个模组吗？')) return;
    try {
      const response = await fetch('/api/admin/mods/' + encodeURIComponent(state.currentId), { method: 'DELETE', headers: authHeaders(false) });
      await readResponse(response);
      state.items = state.items.filter(item => item.id !== state.currentId);
      if (state.items.length) fillForm(state.items[0]); else newForm();
      message('已删除');
    } catch (error) { message(error.message, true); }
  }

  function selectItem(id) {
    if (id === state.currentId && !state.isNew) return;
    if (!confirmDiscard('切换到其他模组')) return;
    const item = state.items.find(value => value.id === id);
    if (item) fillForm(item);
  }

  async function load() {
    try {
      const statusResponse = await fetch('/api/auth/status', { headers: authHeaders(false) });
      const status = await readResponse(statusResponse);
      if (!status.authenticated) { window.location.href = '/'; return; }
      $('modEditorUser').textContent = '@' + status.username;
      const [configResponse, modsResponse] = await Promise.all([
        fetch('/api/config', { headers: authHeaders(false) }),
        fetch('/api/admin/mods', { headers: authHeaders(false) }),
      ]);
      const config = await readResponse(configResponse);
      populateStatusSelect(config && config.statuses, config && config.status_colors);
      configureVersionColumns(config && config.loaders, config && config.loader_colors, config && config.version_options);
      configureFieldPresets(config);
      state.items = await readResponse(modsResponse) || [];
      $('modEditorLoading').hidden = true;
      $('modEditorApp').hidden = false;
      renderList();
      if (state.items.length) fillForm(state.items[0]); else newForm();
    } catch (error) {
      $('modEditorLoading').hidden = true;
      $('modEditorError').hidden = false;
      $('modEditorError').textContent = error.message;
    }
  }

  $('modEditorSearch').addEventListener('input', renderList);
  $('modEditorNew').addEventListener('click', () => {
    if (confirmDiscard('新建模组')) newForm();
  });
  $('modEditorAddVersion').addEventListener('click', () => addVersionRow('', {}));
  $('modEditStatus').addEventListener('change', event => syncStatusColor(event.target));
  $('modEditorDelete').addEventListener('click', remove);
  $('modEditorForm').addEventListener('submit', save);
  $('modEditorForm').addEventListener('input', markDirtyUI);
  $('modEditorForm').addEventListener('change', markDirtyUI);
  $('modEditorLogout').addEventListener('click', () => { localStorage.removeItem('modlist_token'); window.location.href = '/'; });
  ['modEditId', 'modEditName', 'modEditLogo', 'modEditDescription'].forEach(id => $(id).addEventListener('input', () => $(id).classList.remove('invalid')));
  $('modEditStatus').addEventListener('change', () => $('modEditStatus').classList.remove('invalid'));
  attachChipPicker('modEditArch', () => state.presets.architecture);
  attachChipPicker('modEditTags', () => state.presets.tags);
  attachChipPicker('modEditCoop');
  attachLicensePicker('modEditLicense', () => state.presets.license);
  $('modEditLogoBtn').addEventListener('click', () => $('modEditLogoFile').click());
  $('modEditLogoFile').addEventListener('change', () => {
    const file = $('modEditLogoFile').files && $('modEditLogoFile').files[0];
    if (!file) return;
    if (!/^image\//.test(file.type)) {
      message('请选择图片文件', true);
      $('modEditLogoFile').value = '';
      return;
    }
    $('modEditLogo').value = file.name;
    $('modEditLogoName').textContent = file.name;
    const preview = $('modEditLogoPreview');
    if (preview.src && preview.src.startsWith('blob:')) URL.revokeObjectURL(preview.src);
    preview.hidden = false;
    preview.src = URL.createObjectURL(file);
    $('modEditLogoBtn').textContent = '更换图片';
    $('modEditLogo').classList.remove('invalid');
    markDirtyUI();
  });
  load();
})();
