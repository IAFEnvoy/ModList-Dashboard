// i18n.js — Native i18n engine, loads locales from JSON files
const LOCALE_DATA = {};

let currentLang = localStorage.getItem('lang') || 'zh_cn';

function t(key) {
  return (LOCALE_DATA[currentLang] && LOCALE_DATA[currentLang][key])
    || (LOCALE_DATA['en_us'] && LOCALE_DATA['en_us'][key])
    || key;
}

// ========== 工具调用翻译 ==========
// 翻译键格式: tool_call.<tool_name> 或 tool_call.<mcp_id>.<tool_name>
// 模板语法: ${paramName} 替换为参数值，${paramName|filename} 仅显示文件名
// 若未找到翻译键，返回空字符串（调用方保留原样）
function translateToolCall(name, args) {
  if (!name) return '';

  // 查找模板：先精确匹配 tool_call.<name>，再尝试 MCP 格式 tool_call.<mcpID>.<toolName>
  var tpl = resolveLocale('tool_call.' + name);
  if (!tpl) {
    var idx = name.indexOf('__');
    if (idx > 0) {
      tpl = resolveLocale('tool_call.' + name.substring(0, idx) + '.' + name.substring(idx + 2));
    }
  }
  if (!tpl) return ''; // 未找到翻译键，调用方保留原格式

  // 解析参数对象
  var a = args || {};
  if (typeof a === 'string') {
    try { a = JSON.parse(a); } catch (e) { a = {}; }
  }

  // 构建参数映射（含 |filename、|count 变体）
  var vals = {};
  for (var k in a) {
    if (!a.hasOwnProperty(k)) continue;
    var v = a[k];
    if (typeof v === 'string') {
      vals[k] = v;
      vals[k + '|filename'] = basename(v);
    } else if (Array.isArray(v)) {
      vals[k] = v.length;
      vals[k + '|count'] = v.length;
      vals[k + '|filename'] = v.map(function (x) { return typeof x === 'string' ? basename(x) : typeof x === 'object' && x.path ? basename(x.path) : ''; }).filter(Boolean).join(', ');
    } else if (typeof v === 'object' && v) {
      vals[k] = JSON.stringify(v);
    } else {
      vals[k] = String(v);
    }
  }

  // 替换 ${...} 占位符
  return tpl.replace(/\$\{([^}]+)\}/g, function (m, p) {
    return vals.hasOwnProperty(p) ? escHtml(String(vals[p])) : m;
  });
}

// 从当前语言或英语后备中查找 locale 值，未找到返回空字符串
function resolveLocale(key) {
  return (LOCALE_DATA[currentLang] && LOCALE_DATA[currentLang][key])
    || (LOCALE_DATA['en_us'] && LOCALE_DATA['en_us'][key])
    || '';
}

function basename(p) {
  if (!p || typeof p !== 'string') return p || '';
  var i = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'));
  return i >= 0 ? p.substring(i + 1) : p;
}

function escHtml(s) {
  return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

async function loadLocales() {
  try {
    const [en, zh] = await Promise.all([
      fetch('locales/en_us.json').then(r => r.json()),
      fetch('locales/zh_cn.json').then(r => r.json()),
    ]);
    LOCALE_DATA['en_us'] = en;
    LOCALE_DATA['zh_cn'] = zh;
    applyI18n();
  } catch (e) {
    console.warn('i18n locale files failed to load:', e);
  }
}

function setLang(lang) {
  currentLang = lang;
  localStorage.setItem('lang', lang);
  document.documentElement.lang = lang === 'zh_cn' ? 'zh-CN' : 'en';
  if (typeof updateLangDropdown === 'function') updateLangDropdown();
  applyI18n();
}

function applyI18n() {
  document.querySelectorAll('[data-i18n]').forEach(el => {
    const key = el.getAttribute('data-i18n');
    if (!key) return;
    if (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') {
      el.placeholder = t(key);
    } else if (el.tagName === 'OPTION') {
      el.textContent = t(key);
    } else if (el.querySelector('svg, img')) {
      // has SVG/IMG children, only replace text nodes
      el.childNodes.forEach(n => { if (n.nodeType === 3) n.textContent = t(key); });
    } else {
      el.textContent = t(key);
    }
  });

  if (typeof state !== 'undefined' && state.mods && state.mods.length > 0) {
    if (state.currentPage === 'mods') {
      if (typeof renderList === 'function') renderList();
      if (state.currentView === 'scatter' && typeof renderScatter === 'function') renderScatter();
    } else {
      if (typeof renderGitHubPanel === 'function') renderGitHubPanel();
    }
  }

  const sortSel = document.getElementById('sortBy');
  if (sortSel) {
    sortSel.querySelectorAll('option').forEach(opt => {
      const parts = opt.value.split('_');
      const field = parts[0], order = parts[1];
      const fieldMap = { downloads: t('sortDownloads'), name: t('sortName'), status: t('sortStatus'), issues: t('sortIssues') };
      const orderMap = { asc: t('asc'), desc: t('desc') };
      if (fieldMap[field]) opt.textContent = fieldMap[field] + ' ' + (orderMap[order] || '');
    });
  }

  document.querySelectorAll('.gh-tab').forEach(tab => {
    const gh = tab.dataset.gh;
    const label = gh === 'issues' ? t('issues') : t('pulls');
    tab.childNodes.forEach(n => { if (n.nodeType === 3) n.textContent = ' ' + label; });
  });

  if (typeof updateLoginTxt === 'function') updateLoginTxt();
}
