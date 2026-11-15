// Shared binding management UI; QQ is the first platform exposed here.
var botBindingUI = { generation: 0, request: 0, busy: false, poll: null, code: '', expires: '', data: null };

function setupBotBindings() {
  document.getElementById('botBindingGenerate').addEventListener('click', function () { mutateBotBinding('POST'); });
  document.getElementById('botBindingCopy').addEventListener('click', async function () {
    var input = document.getElementById('botBindingCommand');
    var generation = botBindingUI.generation;
    if (!botBindingUI.code) return;
    try {
      await navigator.clipboard.writeText(input.value);
      if (generation === botBindingUI.generation) document.getElementById('botBindingMessage').textContent = t('botBindingCopied');
    } catch (_) { input.focus(); input.select(); }
  });
  document.getElementById('botBindingList').addEventListener('click', function (event) {
    var button = event.target.closest('button[data-target]');
    if (button) mutateBotBinding('DELETE', JSON.parse(button.dataset.target), button.dataset.configured === 'true');
  });
}

function updateBotBindingAuth() {
  botBindingUI.generation++;
  botBindingUI.request++;
  clearInterval(botBindingUI.poll);
  botBindingUI.poll = null;
  botBindingUI.busy = false;
  botBindingUI.code = '';
  botBindingUI.expires = '';
  botBindingUI.data = null;
  document.getElementById('botBindingControls').style.display = state.loggedIn ? '' : 'none';
  document.getElementById('botBindingMessage').textContent = '';
  renderBotBindings();
  if (state.loggedIn) {
    loadBotBindings();
    botBindingUI.poll = setInterval(loadBotBindings, 5000);
  }
}

async function loadBotBindings() {
  if (!state.loggedIn || botBindingUI.busy) return;
  var generation = botBindingUI.generation, request = ++botBindingUI.request;
  try {
    var response = await fetch('/api/admin/bot-bindings', { headers: getAuthHeaders(), cache: 'no-store' });
    var data = await response.json();
    if (generation !== botBindingUI.generation || request !== botBindingUI.request) return;
    if (!response.ok) throw new Error(data.error || t('botBindingFailed'));
    var previousCount = botBindingUI.data ? botBindingUI.data.bindings.length : 0;
    botBindingUI.data = data;
    if (botBindingUI.code && (!data.pending_expires_at || data.pending_expires_at !== botBindingUI.expires)) {
      var expired = Date.now() >= Date.parse(botBindingUI.expires);
      document.getElementById('botBindingMessage').textContent = t(data.bindings.length > previousCount ? 'botBindingSuccess' : expired ? 'botBindingExpired' : 'botBindingCodeInvalidated');
      botBindingUI.code = '';
    }
    renderBotBindings();
  } catch (error) {
    if (generation === botBindingUI.generation && request === botBindingUI.request) document.getElementById('botBindingMessage').textContent = error.message;
  }
}

async function mutateBotBinding(method, target, configured) {
  if (!state.loggedIn || botBindingUI.busy) return;
  var generation = botBindingUI.generation, request = ++botBindingUI.request;
  botBindingUI.busy = true;
  renderBotBindings();
  try {
    var response = await fetch('/api/admin/bot-bindings', {
      method: method, headers: getAuthHeaders(), body: target ? JSON.stringify(target) : undefined
    });
    var data = response.status === 204 ? {} : await response.json();
    if (generation !== botBindingUI.generation || request !== botBindingUI.request) return;
    if (!response.ok) throw new Error(data.error || t('botBindingFailed'));
    if (method === 'POST') {
      botBindingUI.code = data.code;
      botBindingUI.expires = data.expires_at;
      document.getElementById('botBindingMessage').textContent = t('botBindingOneTime');
    } else {
      document.getElementById('botBindingMessage').textContent = t(configured ? 'botBindingStaticRemains' : 'botBindingRemoved');
    }
  } catch (error) {
    if (generation === botBindingUI.generation && request === botBindingUI.request) document.getElementById('botBindingMessage').textContent = error.message;
  } finally {
    if (generation === botBindingUI.generation && request === botBindingUI.request) {
      botBindingUI.busy = false;
      renderBotBindings();
      loadBotBindings();
    }
  }
}

function renderBotBindings() {
  var data = botBindingUI.data;
  document.getElementById('botBindingGenerate').disabled = botBindingUI.busy || !data || !data.qq_enabled;
  document.getElementById('botBindingAvailability').textContent = data && !data.qq_enabled ? t('botBindingDisabled') : '';
  document.getElementById('botBindingCodeBox').hidden = !botBindingUI.code;
  document.getElementById('botBindingCommand').value = botBindingUI.code ? '/bind ' + botBindingUI.code : '';
  document.getElementById('botBindingExpiry').textContent = botBindingUI.code ? t('botBindingExpires') + new Date(botBindingUI.expires).toLocaleTimeString() : '';
  var list = document.getElementById('botBindingList');
  list.replaceChildren();
  ((data && data.bindings) || []).forEach(function (binding) {
    var row = document.createElement('li');
    var label = document.createElement('span');
    label.textContent = binding.target.provider + ' / ' + binding.target.type + ' · ' + binding.target.id;
    if (binding.configured) label.textContent += ' · ' + t('botBindingConfigured');
    var button = document.createElement('button');
    button.type = 'button';
    button.textContent = t('botBindingUnbind');
    button.dataset.target = JSON.stringify(binding.target);
    button.dataset.configured = String(!!binding.configured);
    button.disabled = botBindingUI.busy;
    row.append(label, button);
    list.append(row);
  });
}
