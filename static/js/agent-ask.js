// agent-ask.js — Ask User 内联到聊天消息中（支持 5 种模式）

function showAskUserDialog(dataJson, streamInstance) {
  var data;
  try { data = JSON.parse(dataJson); } catch (e) { return; }
  if (!data) return;

  var chat = $('#chatMessages');
  if (!chat) return;

  var hasOptions = data.options && data.options.length > 0;
  var isMulti = !!data.multiSelect;
  var hasFreeform = data.allowFreeformInput === true;
  var inputType = hasOptions ? (isMulti ? 'checkbox' : 'radio') : '';

  var optionsHtml = '';
  if (hasOptions) {
    optionsHtml = '<div class="ask-inline-options">';
    data.options.forEach(function (opt) {
      // 兼容纯字符串和对象两种格式
      var label, desc;
      if (typeof opt === 'string') {
        label = opt; desc = '';
      } else {
        label = opt.label || opt.title || opt.text || '';
        desc = opt.description || opt.desc || '';
      }
      desc = desc ? '<span class="ask-opt-desc">' + esc(desc) + '</span>' : '';
      optionsHtml += '<label class="ask-inline-opt-label">' +
        '<input type="' + inputType + '" name="ask_inline_opt" value="' + esc(label) + '" class="ask-inline-opt-input">' +
        '<span class="ask-inline-opt-wrap"><span class="ask-inline-opt-text">' + esc(label) + '</span>' + desc + '</span>' +
        '</label>';
    });
    optionsHtml += '</div>';
  }

  var inputHtml = '';
  if (hasFreeform) {
    inputHtml = '<textarea class="ask-inline-input" placeholder="' + t('askInputPlaceholder') + '" rows="2"></textarea>';
  }

  var inputRowHtml = hasFreeform ? '<div class="ask-inline-input-row">' + inputHtml + '</div>' : '';
  var hintText = (isMulti && hasOptions) ? '<div class="ask-inline-hint">' + t('askMultiHint') + '</div>' : '';

  var html = '<div class="chat-msg ask-inline-msg">' +
    '<div class="chat-msg-role">🤔 ' + esc(data.header || t('askDefaultHeader')) + '</div>' +
    '<div class="chat-msg-text">' +
    '<div class="ask-inline-question">' + esc(data.question || '') + '</div>' +
    hintText +
    optionsHtml +
    inputRowHtml +
    '<div class="ask-inline-btns">' +
    '<button class="ask-inline-cancel-btn">' + t('askCancel') + '</button>' +
    '<button class="ask-inline-confirm-btn">' + t('askAnswer') + '</button>' +
    '</div>' +
    '</div></div>';

  chat.insertAdjacentHTML('beforeend', html);
  chat.scrollTop = chat.scrollHeight;

  var msgDiv = chat.lastElementChild;
  if (!msgDiv || !msgDiv.classList.contains('ask-inline-msg')) {
    msgDiv = chat.querySelector('.ask-inline-msg:last-of-type');
  }
  if (!msgDiv) return;

  var answered = false;

  function gatherAnswer() {
    var parts = [];
    var checked = msgDiv.querySelectorAll('.ask-inline-opt-input:checked');
    var selected = [];
    for (var i = 0; i < checked.length; i++) { selected.push(checked[i].value); }
    if (selected.length > 0) parts.push(selected.join(', '));
    var inputEl = msgDiv.querySelector('.ask-inline-input');
    if (inputEl) {
      var text = inputEl.value.trim();
      if (text) parts.push(text);
    }
    return parts.join('\n');
  }

  function submitAnswer(answer) {
    if (answered) return;
    answered = true;

    // 禁用所有控件
    var allInputs = msgDiv.querySelectorAll('input, textarea, button');
    for (var i = 0; i < allInputs.length; i++) { allInputs[i].disabled = true; }
    var confirmBtn = msgDiv.querySelector('.ask-inline-confirm-btn');
    if (confirmBtn) confirmBtn.textContent = t('askSent');

    var sid = currentChatSessionId;
    fetch('/api/agent/answer', {
      method: 'POST',
      headers: getAuthHeaders(),
      body: JSON.stringify({ session: sid, answer: answer })
    }).catch(function (e) { console.error('Answer error:', e); });

    // 用结果框插入到 chatMessages 中（作为内联消息）
    var displayAnswer = answer || t('askCancelled');
    var chat = $('#chatMessages');
    if (chat) {
      var resultDiv = document.createElement('div');
      resultDiv.className = 'chat-msg user';
      resultDiv.innerHTML = '<div class="chat-msg-role">You</div><div class="chat-msg-text">' +
        '<span class="ask-result-box"><strong>' + t('askYourAnswer') + ':</strong> ' + esc(displayAnswer) + '</span>' +
        '</div>';
      chat.insertBefore(resultDiv, msgDiv.nextSibling || null);
      chat.scrollTop = chat.scrollHeight;
    }
    msgDiv.remove();
  }

  msgDiv.querySelector('.ask-inline-confirm-btn').addEventListener('click', function () {
    var answer = gatherAnswer();
    // 如果没有选项且没有自由输入 => 不允许空提交
    if (!answer && hasOptions && !hasFreeform) return;
    submitAnswer(answer || t('askCancelled'));
  });

  msgDiv.querySelector('.ask-inline-cancel-btn').addEventListener('click', function () {
    submitAnswer(t('askCancelled'));
  });

  var inputEl = msgDiv.querySelector('.ask-inline-input');
  if (inputEl) {
    inputEl.addEventListener('keydown', function (e) {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        var answer = gatherAnswer();
        if (!answer && hasOptions && !hasFreeform) return;
        submitAnswer(answer || t('askCancelled'));
      }
    });
    if (!hasOptions) { setTimeout(function () { inputEl.focus(); }, 100); }
  }
}
