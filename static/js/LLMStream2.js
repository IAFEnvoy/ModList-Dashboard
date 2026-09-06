/**
 * LLMStream - 大模型请求和Markdown实时渲染库
 * 支持流式/非流式响应、Markdown渲染、错误处理等
 * @version 1.0.4 - 增加深度思考模型兼容 (DeepSeek R1等)
 */
class LLMStream {
  constructor(options) {
    this.url = options.url;
    this.method = options.method || 'POST';
    this.headers = options.headers || {};
    this.body = options.body || {};
    this.target = options.target;
    this.markdown = options.markdown !== undefined ? options.markdown : true;
    this.stream = options.stream !== undefined ? options.stream : true;
    // 新增：thinking 配置 (true: 强制显示, false: 不显示, 'auto': 有则显示)
    this.thinking = options.thinking !== undefined ? options.thinking : 'auto';
    this.typewriterEffect = options.typewriterEffect || false;
    this.typewriterSpeed = options.typewriterSpeed || 30;
    this.timeout = options.timeout || 600000; // 超时时间，默认10分钟
    // 自定义协议 (默认 'openai'，可选 'custom' 用于我们自己的 SSE 格式)
    this.protocol = options.protocol || 'openai';
    // append 模式：不覆盖 target，追加子元素
    this.appendMode = options.appendMode || false;

    // 回调函数
    this.onChunk = options.onChunk;
    this.onComplete = options.onComplete;
    this.onError = options.onError;
    this.onStart = options.onStart;
    this.onSession = options.onSession; // custom 协议的 session 事件回调
    this.onStep = options.onStep;       // tool call 步骤回调
    this.onTodos = options.onTodos;     // custom 协议的 todos 事件回调
    this.onAskUser = options.onAskUser; // custom 协议的 ask_user 事件回调
    this.onToolCall = options.onToolCall; // custom 协议的 tool_call 事件回调
    this.onBlock = options.onBlock;       // custom 协议的 block 事件回调（非流式块）
    this.onStreamStart = options.onStreamStart;   // 流式文本开始
    this.onContent = options.onContent;           // 流式文本增量
    this.onStreamEnd = options.onStreamEnd;       // 流式文本结束
    this.onUpdate = options.onUpdate;             // 块更新事件
    this.onApprovalRequest = options.onApprovalRequest; // 审批请求回调
    this.onPending = options.onPending;     // custom 协议的 pending 事件回调（新协议）
    this.onBypassStatus = options.onBypassStatus; // bypass 状态变更回调
    this.onLimitExceeded = options.onLimitExceeded; // 达到最大轮次的回调
    this.onTitleGenerated = options.onTitleGenerated; // 标题生成事件

    this.controller = null;
    this.content = '';
    this.reasoning = ''; // 新增：存储深度思考内容
    this.targetElement = null;
    this.typewriterTimer = null;
    this.timeoutId = null;
    this._completed = false;

    // 初始化目标元素
    if (this.target) {
      this.targetElement = typeof this.target === 'string'
        ? document.querySelector(this.target)
        : this.target;
      if (!this.targetElement) {
        console.error(`目标元素 ${this.target} 未找到`);
      }
    }

    // 动态加载Markdown渲染库
    if (this.markdown && !window.marked) {
      this.loadMarkdownLibrary();
    }
  }

  /**
   * 动态加载marked.js库
   */
  async loadMarkdownLibrary() {
    return new Promise((resolve, reject) => {
      if (window.marked) {
        resolve();
        return;
      }

      const script = document.createElement('script');
      script.src = 'https://fastly.jsdelivr.net/npm/marked/marked.min.js';
      script.onload = () => {
        console.log('Marked.js 加载成功');
        if (window.marked) {
          marked.setOptions({
            breaks: true,
            gfm: true,
            highlight: function (code, lang) {
              if (window.hljs && lang) {
                try {
                  return hljs.highlight(code, { language: lang }).value;
                } catch (e) {
                  return code;
                }
              }
              return code;
            }
          });
        }
        resolve();
      };
      script.onerror = reject;
      document.head.appendChild(script);
    });
  }

  /**
   * 开始请求
   */
  async start() {
    try {
      // 确保Markdown库已加载
      if (this.markdown && !window.marked) {
        await this.loadMarkdownLibrary();
      }

      // 清空内容
      this.content = '';
      this.reasoning = ''; // 清空思考内容
      if (this.targetElement) {
        this.targetElement.innerHTML = '';
      }

      // 调用开始回调
      if (this.onStart) {
        this.onStart();
      }

      // 创建AbortController
      this.controller = new AbortController();

      // 设置超时
      this.timeoutId = setTimeout(() => {
        this.controller.abort();
        const timeoutError = new Error('请求超时');
        timeoutError.name = 'TimeoutError';
        if (this.onError) {
          this.onError(timeoutError);
        }
      }, this.timeout);

      // 判断是流式还是非流式（custom 协议始终流式）
      var isStreamRequest = this.protocol === 'custom' || this.body.stream === true;

      console.log(`🚀 开始${isStreamRequest ? '流式' : '非流式'}请求`);

      if (isStreamRequest) {
        await this.startStreamRequest();
      } else {
        await this.startNormalRequest();
      }

    } catch (error) {
      if (error.name === 'AbortError') {
        console.log('请求已取消');
      } else {
        console.error('请求错误:', error);
        if (this.onError) {
          this.onError(error);
        }
      }
    } finally {
      if (this.timeoutId) {
        clearTimeout(this.timeoutId);
        this.timeoutId = null;
      }
    }
  }

  /**
   * 流式请求
   */
  async startStreamRequest() {
    const response = await fetch(this.url, {
      method: this.method,
      headers: this.headers,
      body: JSON.stringify(this.body),
      signal: this.controller.signal
    });

    if (!response.ok) {
      const errorText = await response.text();
      throw new Error(`HTTP错误 ${response.status}: ${errorText}`);
    }

    await this.handleStreamResponse(response);
  }

  /**
   * 非流式请求
   */
  async startNormalRequest() {
    console.log('📡 发起非流式请求...');

    const response = await fetch(this.url, {
      method: this.method,
      headers: this.headers,
      body: JSON.stringify(this.body),
      signal: this.controller.signal
    });

    if (!response.ok) {
      const errorText = await response.text();
      throw new Error(`HTTP错误 ${response.status}: ${errorText}`);
    }

    // 解析JSON响应
    const data = await response.json();
    console.log('📦 收到响应数据:', data);

    // 检查是否包含错误
    if (data.error) {
      throw new Error(data?.error?.message || data?.error || '未知错误');
    }

    // 提取内容（支持多种响应格式），修改为返回对象
    const result = this.extractContent(data);
    const content = result.content;
    const reasoning = result.reasoning;

    console.log('📝 提取的内容:', content, '🤔 思考:', reasoning);

    if (!content && !reasoning) {
      console.warn('⚠️ 未能从响应中提取内容，完整响应:', JSON.stringify(data, null, 2));
      throw new Error('响应中没有找到有效内容');
    }

    this.reasoning = reasoning || '';

    // 使用打字机效果或直接显示
    if (this.typewriterEffect) {
      await this.typewriterRender(content || '');
    } else {
      this.content = content || '';
      this.render(this.content);

      if (this.onComplete) {
        this.onComplete(this.content, this.reasoning);
      }
    }
  }

  /**
   * 从响应数据中提取内容（支持多种格式）
   * @returns {Object} { content: string, reasoning: string }
   */
  extractContent(data) {
    // 默认返回结构
    let result = { content: null, reasoning: null };

    // 格式1: OpenAI / DeepSeek / 通义千问格式
    if (data.choices && Array.isArray(data.choices) && data.choices.length > 0) {
      const choice = data.choices[0];

      // message.content (标准格式)
      if (choice.message) {
        result.content = choice.message.content;
        result.reasoning = choice.message.reasoning_content; // 兼容深度思考
        return result;
      }

      // text (某些API)
      if (choice.text) {
        result.content = choice.text;
        return result;
      }
    }

    // 格式2: 直接在data下
    if (data.content) {
      result.content = data.content;
      return result;
    }

    // 格式3: output字段
    if (data.output) {
      if (typeof data.output === 'string') {
        result.content = data.output;
      } else {
        result.content = data.output.content || data.output.text;
      }
      return result;
    }

    // 格式4: response字段
    if (data.response) {
      result.content = typeof data.response === 'string' ? data.response : data.response.content;
      return result;
    }

    // 格式5: text字段
    if (data.text) {
      result.content = data.text;
      return result;
    }

    // 格式6: result字段
    if (data.result) {
      result.content = typeof data.result === 'string' ? data.result : data.result.content;
      return result;
    }

    // 格式7: message字段
    if (data.message) {
      result.content = typeof data.message === 'string' ? data.message : data.message.content;
      return result;
    }

    // 格式8: 直接是字符串
    if (typeof data === 'string') {
      result.content = data;
      return result;
    }

    return result;
  }

  /**
   * 打字机效果渲染
   */
  async typewriterRender(fullContent) {
    return new Promise((resolve) => {
      let index = 0;
      this.content = '';

      // 初始渲染一次，确保思考过程（如果有）能先显示出来
      this.render('');

      const type = () => {
        if (index < fullContent.length) {
          const char = fullContent[index];
          this.content += char;
          this.render(this.content);

          if (this.onChunk) {
            this.onChunk(char, this.content, this.reasoning);
          }

          index++;
          this.typewriterTimer = setTimeout(type, this.typewriterSpeed);
        } else {
          if (this.onComplete) {
            this.onComplete(this.content, this.reasoning);
          }
          resolve();
        }
      };

      type();
    });
  }

  /**
   * 处理SSE流式响应（支持 openai 和 custom 两种协议）
   */
  async handleStreamResponse(response) {
    const reader = response.body.getReader();
    const decoder = new TextDecoder('utf-8');
    let buffer = '';
    this._completed = false;

    try {
      while (true) {
        const { done, value } = await reader.read();

        if (done) {
          console.log('✅ 流式传输完成');
          if (!this._completed && this.onComplete) {
            this.onComplete(this.content, this.reasoning);
          }
          break;
        }

        buffer += decoder.decode(value, { stream: true });
        const events = buffer.split('\n\n');
        buffer = events.pop() || '';

        for (const event of events) {
          this._processSSEEvent(event);
        }
      }
    } finally {
      reader.releaseLock();
    }
  }

  _processSSEEvent(eventRaw) {
    const trimmed = eventRaw.trim();
    if (!trimmed) return;

    const lines = trimmed.split('\n');
    let dataStr = '';
    for (const line of lines) {
      if (line.indexOf('data:') === 0) {
        dataStr = line.substring(5).trim();
      }
    }
    if (!dataStr) return;

    if (dataStr === '[DONE]') return;

    try {
      const json = JSON.parse(dataStr);

      // === 自定义协议 ===
      if (this.protocol === 'custom') {
        if (json.type === 'session') {
          if (this.onSession) this.onSession(json.content);
          return;
        }
        if (json.type === 'delta') {
          this.content += json.content;
          this.render(this.content);
          if (this.onChunk) this.onChunk(json.content, this.content, this.reasoning);
          return;
        }
        if (json.type === 'error') {
          if (this.onError) this.onError(new Error(json.content));
          return;
        }
        if (json.type === 'done') {
          this._completed = true;
          if (this.onComplete) this.onComplete(this.content, this.reasoning);
          return;
        }
        // 新协议事件
        if (json.type === 'start') {
          if (this.onStart) this.onStart();
          return;
        }
        if (json.type === 'end') {
          this._completed = true;
          if (this.onComplete) this.onComplete(json.error || null);
          return;
        }
        if (json.type === 'end_limit_exceeded') {
          this._completed = true;
          if (this.onComplete) this.onComplete(json.error || null);
          if (this.onLimitExceeded) this.onLimitExceeded(json.error || null);
          return;
        }
        if (json.type === 'pending') {
          if (this.onPending) this.onPending(json.id, json.content);
          return;
        }
        // 流式块（顶层事件）
        if (json.type === 'stream_start') {
          if (this.onStreamStart) this.onStreamStart(json);
          return;
        }
        if (json.type === 'content') {
          if (this.onContent) this.onContent(json);
          return;
        }
        if (json.type === 'stream_end') {
          if (this.onStreamEnd) this.onStreamEnd(json);
          return;
        }
        // 非流式块
        if (json.type === 'block') {
          if (this.onBlock) this.onBlock(json);
          return;
        }
        // 块更新
        if (json.type === 'update') {
          if (this.onUpdate) this.onUpdate(json);
          return;
        }
        if (json.type === 'todo') {
          if (this.onTodos) this.onTodos(json.content);
          return;
        }
        if (json.type === 'todos') {
          if (this.onTodos) this.onTodos(json.content);
          return;
        }
        if (json.type === 'bypass_status') {
          if (this.onBlock) this.onBlock(json);
          return;
        }
        if (json.type === 'bypass_status') {
          if (this.onBypassStatus) this.onBypassStatus(json.content);
          return;
        }
        if (json.type === 'title_generated') {
          if (this.onTitleGenerated) this.onTitleGenerated(json.content);
          return;
        }
        return;
      }

      // === 标准 OpenAI 协议 ===
      if (json.error) {
        throw new Error(json.error?.message || json.error || '未知错误');
      }

      const delta = json.choices?.[0]?.delta?.content || '';
      const reasoningDelta = json.choices?.[0]?.delta?.reasoning_content || '';

      let hasUpdate = false;
      if (reasoningDelta) { this.reasoning += reasoningDelta; hasUpdate = true; }
      if (delta) { this.content += delta; hasUpdate = true; }

      if (hasUpdate) {
        this.render(this.content);
        if (this.onChunk) this.onChunk(delta || reasoningDelta, this.content, this.reasoning);
      }
    } catch (e) {
      if (e.message && e.message.indexOf('JSON') < 0) throw e;
      console.warn('SSE JSON解析错误:', e, dataStr);
    }
  }

  /**
   * 渲染内容到目标元素
   */
  render(content) {
    if (!this.targetElement) return;

    // 思考中圆点由外部显式管理，不在这里自动隐藏
    let showThinking = false;
    if (this.thinking === true) {
      showThinking = true;
    } else if (this.thinking === 'auto') {
      showThinking = !!this.reasoning;
    } else {
      showThinking = false;
    }

    let finalContent = content;

    // 组装思考过程和正文
    if (showThinking) {
      const reasoningText = this.reasoning || '';
      if (this.markdown) {
        finalContent = `<details open><summary>深度思考 (Thinking Process)</summary>\n\n${reasoningText}\n\n</details>\n\n${content}`;
      } else {
        finalContent = `[深度思考]\n${reasoningText}\n\n[回答]\n${content}`;
      }
    }

    if (this.markdown && window.marked) {
      if (typeof window.renderMarkdownSafe === 'function') {
        this.targetElement.innerHTML = window.renderMarkdownSafe(finalContent);
      } else if (window.DOMPurify) {
        this.targetElement.innerHTML = window.DOMPurify.sanitize(marked.parse(finalContent), { USE_PROFILES: { html: true } });
      } else {
        this.targetElement.textContent = finalContent;
      }
    } else {
      this.targetElement.textContent = finalContent;
    }

    // 保存原始数据到 DOM 属性，方便调试或外部获取
    this.targetElement.dataset.markdown = content;
    this.targetElement.dataset.reasoning = this.reasoning;

    // 智能滚动：仅在用户在底部时跟随（用 rAF 确保 scrollHeight 已更新）
    var self = this;
    requestAnimationFrame(function () {
      if (typeof smartScroll === 'function') {
        smartScroll(self.targetElement);
        if (self.protocol === 'custom') {
          var chat = document.getElementById('chatMessages');
          smartScroll(chat);
        }
      } else {
        self.targetElement.scrollTop = self.targetElement.scrollHeight;
        if (self.protocol === 'custom') {
          var chat2 = document.getElementById('chatMessages');
          if (chat2) chat2.scrollTop = chat2.scrollHeight;
        }
      }
    });
  }

  /**
   * 停止请求
   */
  stop() {
    if (this.controller) {
      this.controller.abort();
      console.log('已停止HTTP请求');
    }

    if (this.typewriterTimer) {
      clearTimeout(this.typewriterTimer);
      this.typewriterTimer = null;
      console.log('已停止打字机效果');
    }

    if (this.timeoutId) {
      clearTimeout(this.timeoutId);
      this.timeoutId = null;
    }
  }

  /**
   * 获取当前内容
   */
  getContent() {
    return this.content;
  }

  /**
   * 获取当前思考内容
   */
  getReasoning() {
    return this.reasoning;
  }

  /**
   * 重置流式内容（块切换时调用，不清除 DOM）
   */
  resetContent() {
    this.content = '';
    this.targetElement = null;
  }

  /**
   * 清空内容
   */
  clear() {
    this.content = '';
    this.reasoning = '';
    if (this.targetElement) {
      this.targetElement.innerHTML = '';
    }
  }
}

// 导出
if (typeof module !== 'undefined' && module.exports) {
  module.exports = LLMStream;
}
if (typeof window !== 'undefined') {
  window.LLMStream = LLMStream;
}
