// detail.js — detail card overlay
function openDetail(modId) {
  const mod = state.mods.find(m => m.id === modId);
  if (!mod) return;

  const detailLogo = $('#detailLogo');
  detailLogo.src = getLogo(mod);
  detailLogo.onerror = function () { this.style.display = 'none'; $('#detailLogoFallback').style.display = 'flex'; };
  detailLogo.onload = function () { this.style.display = ''; $('#detailLogoFallback').style.display = 'none'; };
  $('#detailLogoFallback').textContent = mod.name?.[0] || '?';
  $('#detailLogoFallback').style.display = mod.logo ? 'none' : 'flex';
  $('#detailName').textContent = mod.name;
  $('#detailDesc').textContent = mod.description || '';

  const tags = [mod.status, ...(mod.tags || [])].filter(Boolean);
  $('#detailTags').innerHTML = tags.map(tag => {
    const isStatus = isStatusValue(tag);
    const label = isStatus ? t('status_' + tag.replace(/\s+/g, '')) : tag;
    return `<span class="mod-tag ${isStatus ? `tag-status ${getStatusClass(tag)}` : 'tag-down'}">${esc(label)}</span>`;
  }).join('') + `<span class="mod-tag tag-down">${t('difficulty')}: ${mod.difficulty}</span>`;

  let linksHtml = '';
  let idsHtml = '';
  const displayModId = (mod.mod_meta && mod.mod_meta.id) || modId || '';
  if (displayModId) idsHtml += `<button type="button" class="detail-id-copy" data-id-source="mod" title="${esc(t('copyModId'))}"><span>MOD ID</span><code>${esc(displayModId)}</code><span class="detail-id-copy-state">${esc(t('copy'))}</span></button>`;
  if (mod.ids?.gh) linksHtml += `<a class="gh-link-d" href="https://github.com/${mod.ids.gh}" target="_blank">GitHub</a>`;
  if (mod.ids?.cf) linksHtml += `<a class="cf-link-d" href="https://www.curseforge.com/projects/${mod.ids.cf}" target="_blank">CurseForge</a>`;
  if (mod.ids?.wiki) linksHtml += `<a class="wiki-link-d" href="${mod.ids.wiki}" target="_blank">Wiki</a>`;
  if (mod.ids?.cf) idsHtml += `<button type="button" class="detail-id-copy cf-id-copy" data-id-source="cf" title="${esc(t('copyCfId'))}"><span>CF ID</span><code>${esc(mod.ids.cf)}</code><span class="detail-id-copy-state">${esc(t('copy'))}</span></button>`;
  const coop = mod.coop || [];
  $('#detailCoop').innerHTML = coop.length ? `<span class="coop-label">${t('coAuthor')}</span>` + coop.map(c => `<a class="coop-link" href="https://github.com/${c}" target="_blank">@${esc(c)}</a>`).join('') : '';

  $('#detailLinks').innerHTML = `<div class="detail-platform-links">${linksHtml}</div>${idsHtml ? `<div class="detail-id-links">${idsHtml}</div>` : ''}`;
  $('#detailLinks').querySelectorAll('.detail-id-copy').forEach(button => {
    button.addEventListener('click', () => {
      const value = button.dataset.idSource === 'cf' ? ((mod.ids && mod.ids.cf) || '') : ((mod.mod_meta && mod.mod_meta.id) || mod.id || '');
      copyDetailId(button, value);
    });
  });

  $('#detailCF').innerHTML = `<img src="img/curseforge.webp" class="stat-icon"> ${formatNum(mod.cf_downloads || 0)}`;
  $('#detailPop').textContent = (mod.popularity || 0).toFixed(2);
  $('#detailDiff').textContent = mod.difficulty;

  let ghHtml = '';
  if (mod.github || mod.versions) {
    const issues = (mod.github?.issues || []).sort((a, b) => (b.created_at || '').localeCompare(a.created_at || ''));
    const prs = (mod.github?.pull_requests || []).sort((a, b) => (b.created_at || '').localeCompare(a.created_at || ''));
    ghHtml += '<div class="detail-gh-tabs">';
    if (mod.versions) ghHtml += '<button class="detail-gh-tab active" data-gh-tab-type="version">📋 ' + t('tabVersion') + '</button>';
    if (mod.github) {
      const oi = issues.filter(i => i.state === 'open').length, op = prs.filter(p => p.state === 'open').length;
      ghHtml += '<button class="detail-gh-tab' + (mod.versions ? '' : ' active') + '" data-gh-tab-type="issue"><img src="img/issue.svg" class="stat-icon"> ' + t('issues') + ' (' + oi + ')</button>';
      ghHtml += '<button class="detail-gh-tab" data-gh-tab-type="pr"><img src="img/pr.svg" class="stat-icon"> ' + t('pulls') + ' (' + op + ')</button>';
    }
    ghHtml += '</div>';
    if (mod.versions) { ghHtml += '<div class="detail-gh-panel" data-gh-panel="version">' + buildVersionTable(mod.versions) + '</div>'; }
    if (mod.github) {
      ghHtml += '<div class="detail-gh-panel" data-gh-panel="issue"' + (mod.versions ? ' style="display:none;"' : '') + '>';
      ghHtml += '<div class="gh-filter-group" style="margin-bottom:8px;"><button class="gh-filter" data-detail-filter="all">' + t('all') + '</button><button class="gh-filter active" data-detail-filter="open">' + t('open') + '</button><button class="gh-filter" data-detail-filter="closed">' + t('closed') + '</button></div><div class="detail-gh-list" id="detailIssueList">' + renderGhItemList(issues, 'open', mod.ids.gh, 'issue') + '</div></div>';
      ghHtml += '<div class="detail-gh-panel" data-gh-panel="pr" style="display:none;">';
      ghHtml += '<div class="gh-filter-group" style="margin-bottom:8px;"><button class="gh-filter" data-detail-filter="all">' + t('all') + '</button><button class="gh-filter active" data-detail-filter="open">' + t('open') + '</button><button class="gh-filter" data-detail-filter="closed">' + t('closed') + '</button></div><div class="detail-gh-list" id="detailPrList">' + renderGhItemList(prs, 'open', mod.ids.gh, 'pr') + '</div></div>';
    }
    const isRef = issues, prRef = prs, localRepoPath = mod.ids.gh || '';
    setTimeout(() => {
      $('#detailGitHub').querySelectorAll('.detail-gh-tab').forEach(tab => {
        tab.addEventListener('click', () => {
          const type = tab.dataset.ghTabType;
          $('#detailGitHub').querySelectorAll('.detail-gh-tab').forEach(t => t.classList.remove('active'));
          tab.classList.add('active');
          $('#detailGitHub').querySelectorAll('.detail-gh-panel').forEach(p => { p.style.display = p.dataset.ghPanel === type ? 'block' : 'none'; });
        });
      });
      $('#detailGitHub').querySelectorAll('.gh-filter').forEach(btn => {
        btn.addEventListener('click', () => {
          const panel = btn.closest('.detail-gh-panel');
          if (!panel) return;
          const type = panel.dataset.ghPanel;
          panel.querySelectorAll('.gh-filter').forEach(b => b.classList.remove('active'));
          btn.classList.add('active');
          const filter = btn.dataset.detailFilter;
          const listEl = panel.querySelector('.detail-gh-list');
          if (listEl) listEl.innerHTML = renderGhItemList(type === 'issue' ? isRef : prRef, filter, localRepoPath, type);
        });
      });
      // 派发按钮点击 → 打开简易派发弹窗
      $('#detailGitHub').addEventListener('click', function (e) {
        var btn = e.target.closest('.gh-dispatch-btn');
        if (!btn) return;
        e.preventDefault();
        e.stopPropagation();
        showDispatchDialog(
          btn.dataset.ghOwner,
          btn.dataset.ghRepo,
          parseInt(btn.dataset.ghNumber),
          btn.dataset.ghTitle,
          btn.dataset.ghType
        );
      });
    }, 0);
  } else { ghHtml = '<div style="color:#9ca3af;padding:8px;">' + t('noRepo') + '</div>'; }
  $('#detailGitHub').innerHTML = ghHtml;
  $('#detailOverlay').classList.add('show');
  document.body.style.overflow = 'hidden';
}

async function copyDetailId(button, value) {
  try {
    let copied = false;
    if (navigator.clipboard?.writeText) {
      try {
        await navigator.clipboard.writeText(String(value));
        copied = true;
      } catch (_) { }
    }
    if (!copied) {
      const input = document.createElement('textarea');
      input.value = String(value);
      input.setAttribute('readonly', '');
      input.style.position = 'fixed';
      input.style.opacity = '0';
      document.body.appendChild(input);
      input.select();
      copied = document.execCommand('copy');
      input.remove();
      if (!copied) throw new Error('copy failed');
    }
    const state = button.querySelector('.detail-id-copy-state');
    if (!state) return;
    state.textContent = t('copied');
    button.classList.add('copied');
    setTimeout(() => {
      state.textContent = t('copy');
      button.classList.remove('copied');
    }, 630);
  } catch (error) {
    const state = button.querySelector('.detail-id-copy-state');
    if (!state) return;
    state.textContent = t('copyFailed');
    setTimeout(() => { state.textContent = t('copy'); }, 1200);
  }
}

function setupDetailClose() {
  $('#detailClose').addEventListener('click', closeDetail);
  $('#detailOverlay').addEventListener('click', e => { if (e.target === $('#detailOverlay')) closeDetail(); });
  // 派发弹窗关闭
  var dd = $('#dispatchDialog');
  if (dd) {
    dd.addEventListener('click', function (e) { if (e.target === dd) closeDispatchDialog(); });
  }
  document.addEventListener('keydown', e => {
    if (e.key === 'Escape') {
      var dd = $('#dispatchDialog');
      if (dd && dd.classList.contains('show')) closeDispatchDialog();
      else closeDetail();
    }
  });
}

function closeDetail() { $('#detailOverlay').classList.remove('show'); document.body.style.overflow = ''; }

// ========== 简易派发弹窗 ==========

async function showDispatchDialog(owner, repo, number, title, ghType) {
  var dd = $('#dispatchDialog');
  if (!dd) return;

  // ====== 存储 ghType 到隐藏字段，供 doDispatch 使用 ======
  $('#dispatchGhType').value = ghType || 'issue';
  var isPR = (ghType === 'pr');
  $('#dispatchTitle').textContent = (isPR ? '🔀 ' : '') + '#' + number + ' ' + (title || '');

  // 确保 agentInfo 已加载
  if (!agentInfo || !agentInfo.agents || !agentInfo.models) {
    console.log('[Dispatch] agentInfo 未就绪，尝试获取... agentInfo=', agentInfo);
    if (typeof fetchAgentInfo === 'function') {
      for (var attempt = 0; attempt < 5; attempt++) {
        var ok = await fetchAgentInfo();
        if (ok && agentInfo && agentInfo.agents && agentInfo.agents.length > 0) break;
        await new Promise(function (r) { setTimeout(r, 600); });
      }
    }
    if (!agentInfo || !agentInfo.agents || agentInfo.agents.length === 0) {
      alert('无法获取 Agent 信息。请确认：\n1) 已登录（页面右上角显示退出按钮）\n2) HTTP API 可访问\n3) 浏览器控制台查看是否有 fetch 错误');
      console.error('[Dispatch] agentInfo 最终状态:', JSON.stringify(agentInfo));
      return;
    }
  }

  $('#dispatchOwner').value = owner;
  $('#dispatchRepo').value = repo;
  $('#dispatchNumber').value = number;
  $('#dispatchRepoLabel').textContent = owner + '/' + repo;
  $('#dispatchExtraPrompt').value = '';

  // 填充 Agent 选择
  var agents = agentInfo.agents || [];
  $('#dispatchAgentSelect').innerHTML = agents.length === 0
    ? '<option value="">-- 无可用 Agent --</option>'
    : agents.map(function (a) { return '<option value="' + esc(a.id) + '">' + esc(a.name) + '</option>'; }).join('');

  // 模型选择
  var models = agentInfo.models || [];
  var ms = $('#dispatchModelSelect');
  ms.innerHTML = models.length === 0
    ? '<option value="">-- 无可用模型 --</option>'
    : models.map(function (m) {
      return '<option value="' + esc(m.id) + '">(' + esc(m.provider_name || '') + ') ' + esc(m.name) + '</option>';
    }).join('');
  if (models.length > 0) ms.value = models[0].id;

  // 项目选择
  var mods = (typeof state !== 'undefined' && state.mods) ? state.mods : [];
  var ms2 = $('#dispatchModSelect');
  ms2.innerHTML = '<option value="">(自动匹配)</option>' + mods.slice().sort(function (a, b) { return (a.name || '').localeCompare(b.name || ''); }).map(function (m) {
    return '<option value="' + esc(m.name) + '">' + esc(m.name) + '</option>';
  }).join('');

  dd.classList.add('show');

  // 重置按钮到初始状态（防止上次派发失败后残留 disabled 状态）
  var btn = $('#dispatchGoBtn');
  if (btn) { btn.disabled = false; btn.textContent = '🚀 派发'; }
}

function closeDispatchDialog() {
  var dd = $('#dispatchDialog');
  if (dd) dd.classList.remove('show');
}

async function doDispatch() {
  var owner = $('#dispatchOwner').value;
  var repo = $('#dispatchRepo').value;
  var number = parseInt($('#dispatchNumber').value);
  var agentId = $('#dispatchAgentSelect').value;
  var modelId = $('#dispatchModelSelect').value;
  var modName = $('#dispatchModSelect').value;
  var extraPrompt = ($('#dispatchExtraPrompt') ? $('#dispatchExtraPrompt').value.trim() : '');

  if (!agentId) { alert('请选择 Agent'); return; }
  if (!modelId) { alert('请选择模型'); return; }

  var btn = $('#dispatchGoBtn');
  btn.disabled = true;

  var ghTypeEl = $('#dispatchGhType');
  var ghType = (ghTypeEl ? ghTypeEl.value : '') || 'issue';
  var isPR = (ghType === 'pr');
  btn.textContent = isPR ? '正在获取 PR & Diff...' : '正在获取 Issue...';

  try {
    var apiPath = isPR
      ? '/api/github/pr/' + owner + '/' + repo + '/' + number
      : '/api/github/issue/' + owner + '/' + repo + '/' + number;
    var resp = await fetch(apiPath, { headers: getAuthHeaders() });
    if (!resp.ok) {
      var errText = await resp.text();
      alert('获取 ' + (isPR ? 'PR' : 'Issue') + ' 失败: ' + resp.status + ' ' + errText);
      btn.disabled = false;
      btn.textContent = '🚀 派发';
      return;
    }
    var detail = await resp.json();

    // 将 Issue/PR 内容存入云盘 issues/ 文件夹
    try {
      var issueFileName = (isPR ? 'PR-' : 'ISSUE-') + number + '-' + (detail.user || 'unknown') + '.md';
      var issueContent = '# ' + (isPR ? 'PR' : 'Issue') + ' #' + number + ': ' + (detail.title || '') + '\n\n' +
        '- 项目: ' + owner + '/' + repo + '\n' +
        '- 状态: ' + (detail.state || '') + '\n' +
        '- 作者: ' + (detail.user || 'unknown') + '\n' +
        '- 日期: ' + (detail.created_at || '') + '\n' +
        (detail.labels ? '- 标签: ' + detail.labels.map(function (l) { return l.name || l; }).join(', ') + '\n' : '') +
        '\n## Body\n\n' + (detail.body || '(无描述)') +
        (isPR ? '\n\n## Diff\n\n```diff\n' + (detail.diff || '') + '\n```' : '');
      var formData = new FormData();
      formData.append('path', 'issues/' + owner + '/' + repo);
      formData.append('files', new Blob([issueContent], { type: 'text/markdown' }), issueFileName);
      var token = localStorage.getItem('modlist_token');
      var hdrs = token ? { 'Authorization': 'Bearer ' + token } : {};
      await fetch('/api/drive/upload', { method: 'POST', headers: hdrs, body: formData });
    } catch (e) {
      console.warn('[Dispatch] 云盘存储失败:', e);
    }

    // 构建纯文本用户消息
    var labelNames = (detail.labels || []).map(function (l) { return l.name || l; }).join(', ');
    var prefix = isPR ? '🔀 PR' : '🐛 Issue';
    var msg = '🐛 Bug 报告 #' + number + ': ' + (detail.title || '') + '\n' +
      '项目: ' + (project || owner + '/' + repo) + ' | ' +
      '报告者: ' + (detail.user || 'unknown') + ' | ' +
      '日期: ' + (detail.created_at || '').substring(0, 10);
    if (labelNames) msg += ' | 标签: ' + labelNames;
    if (extraPrompt) msg += '\n\n> 额外要求: ' + extraPrompt;

    // 构建 attachment 和 aiTask
    var attachment, aiTask;

    if (isPR) {
      // PR 的 attachment：元数据 + diff
      attachment = '[GitHub PR #' + number + '] ' + owner + '/' + repo + '\n' +
        '标题: ' + (detail.title || '') + '\n' +
        '状态: ' + (detail.state || '') + '\n' +
        '作者: ' + (detail.user || '') + '\n' +
        '目标分支: ' + (detail.base_ref || '') + ' ← 源分支: ' + (detail.head_ref || '') + '\n\n' +
        '--- PR Diff ---\n' + (detail.diff || '(diff 生成失败，请检查本地仓库)');

      aiTask = attachment + '\n\n' +
        '## ⚠️ 处理流程 — PR 代码审查\n' +
        '你正在审查一个 Pull Request。请按照以下步骤进行：\n' +
        '1. **阅读 diff**：上面的 attachment 已包含完整的 git diff。先整体浏览变更范围和涉及的文件。\n' +
        '2. **审查代码变更**：逐文件分析 diff，重点关注：\n' +
        '   - 逻辑正确性：变更是否解决了预期的问题？是否有边界条件遗漏？\n' +
        '   - 代码质量：命名、结构、注释是否符合项目风格？\n' +
        '   - 潜在风险：是否存在空指针、并发问题、性能退化、向后兼容性问题？\n' +
        '   - 测试覆盖：变更是否包含/需要测试？\n' +
        '3. **深入查看上下文**：对于不够清晰的变更，使用 read_file / grep_search 查看 ' + owner + '/' + repo + ' 项目中相关文件的完整上下文。\n' +
        '4. **输出审查结论**：总结发现的问题、改进建议、和总体评价（approve / comment / request changes）。' +
        (extraPrompt ? '\n\n额外要求: ' + extraPrompt : '');
    } else {
      // Issue 处理：附件内容 = 完整Bug报告（处理指令+正文+额外要求，全部在这里）
      // 不要提 GitHub！LLM 会条件反射去搜索。
      aiTask = '以下是用户发来的 Bug 报告，请直接分析并修复。\n\n' +
        '**重要：所有信息已包含在本消息中。禁止使用任何 GitHub 工具或网页抓取去获取 Issue。**\n\n' +
        '## 分析步骤\n' +
        '1. 仔细阅读下方 Bug 描述，提取异常类型、触发条件、关键类名/方法名。\n' +
        '2. 如有附件链接（github.com/user-attachments/files/...），用 download_file 下载日志/crash report。\n' +
        '3. 搜索项目 ' + (project || (owner + '/' + repo)) + ' 中的源代码，定位根本原因。\n' +
        '4. 给出分析结论和修复方案。\n\n' +
        '---\n\n' +
        '## 报告信息\n' +
        '编号: #' + number + '\n' +
        '标题: ' + (detail.title || '') + '\n' +
        '报告者: ' + (detail.user || 'unknown') + '\n' +
        '状态: ' + (detail.state || '') + '\n\n' +
        '## Bug 描述\n' + (detail.body || '(无描述)');
      if (extraPrompt) aiTask += '\n\n---\n\n额外要求: ' + extraPrompt;
    }

    // 项目匹配：优先选 dropdown，fallback 用 owner/repo 匹配
    var project = modName || '';
    if (!project) {
      var mods = (typeof state !== 'undefined' && state.mods) ? state.mods : [];
      var match = mods.find(function (m) {
        return m.ids && m.ids.gh && m.ids.gh.toLowerCase() === (owner + '/' + repo).toLowerCase();
      });
      if (match) project = match.name;
    }

    // ====== 三步流程：REST 创建会话 → 进入聊天界面 → SSE 流式输出 ======

    // Step 1: REST 创建会话
    // message=简短摘要（UI 显示一行），attachment=完整任务描述（含处理流程，折叠在附件块中）
    var createResp = await fetch('/api/agent/sessions', {
      method: 'POST',
      headers: getAuthHeaders(),
      body: JSON.stringify({
        agent: agentId,
        provider: modelId,
        project: project,
        message: msg,
        attachment: aiTask,
        bypass: !!(document.getElementById('bypassApprovalCheck') && document.getElementById('bypassApprovalCheck').checked)
      })
    });
    if (!createResp.ok) {
      var errText2 = await createResp.text();
      alert('创建会话失败: ' + createResp.status + ' ' + errText2);
      btn.disabled = false;
      btn.textContent = '🚀 派发';
      return;
    }
    var sessionData = await createResp.json();
    var sessionId = sessionData.id;

    closeDispatchDialog();
    closeDetail();

    // Step 2: 切换到 Agent 页面并进入聊天界面（直接传入预加载的消息，跳过异步 REST GET）
    if (typeof switchPage === 'function') switchPage('agents');

    var agentName = (typeof FindAgentName === 'function') ? FindAgentName(agentId) : agentId;
    if (typeof openChatWindow === 'function') openChatWindow(sessionId, agentName, sessionData.messages, project, sessionData.bypass_approval);

    // Step 3: SSE 流式输出（start=true 触发 Agent，消息已由 REST 预添加，不重复）
    if (typeof doSendStream === 'function') {
      doSendStream(sessionId, '', null, modelId, { start: true });
    }

  } catch (e) {
    if (typeof setAgentRunning === 'function') setAgentRunning(false);
    alert('派发失败: ' + (e.message || String(e)));
    btn.disabled = false;
    btn.textContent = '🚀 派发';
  }
}

function onDispatchModeChange() {
  var modeEl = document.querySelector('input[name="dispatchMode"]:checked');
  var isNew = !modeEl || modeEl.value === 'new';
  var af = document.getElementById('dispatchAgentField');
  var mf = document.getElementById('dispatchModelField');
  var sf = document.getElementById('dispatchSessionField');
  if (af) af.style.display = isNew ? '' : 'none';
  if (mf) mf.style.display = isNew ? '' : 'none';
  if (sf) sf.style.display = isNew ? 'none' : '';
  if (!isNew) {
    var sel = document.getElementById('dispatchSessionSelect');
    if (sel && typeof agentSessions !== 'undefined') {
      sel.innerHTML = (agentSessions || []).map(function (s) {
        var an = (typeof FindAgentName === 'function') ? FindAgentName(s.agent) : (s.agent || '');
        return '<option value="' + esc(s.id) + '">' + esc(an) + ' - ' + esc(s.project || '') + '</option>';
      }).join('');
    }
  }
}
