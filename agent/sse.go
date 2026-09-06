package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
)

// ========== SSE 广播器（监听者模式） ==========

// SSEEvent SSE 原始事件。Type 为顶层事件类型（start/end/block/todo/pending），
// Subtype 仅 block 事件使用（stream_start/content/stream_end/tool_call/tool_result/approval_request/ask_user/user/attachment/message）。
type SSEEvent struct {
	Type    string      `json:"type"`
	Content interface{} `json:"content,omitempty"`
	ID      int         `json:"id,omitempty"`      // 块 ID（block 事件时有效，对应 session.Messages 索引）
	Subtype string      `json:"subtype,omitempty"` // block 子类型
	Error   string      `json:"error,omitempty"`   // end 事件的错误信息
}

// SSEBroadcaster 会话级 SSE 广播器。
// Agent 通过高层方法投递事件，广播器序列化为 SSE 协议并分发给所有订阅者。
// 无订阅者时事件静默丢弃，Agent 不受影响。
type SSEBroadcaster struct {
	mu          sync.RWMutex
	subscribers map[int64]chan SSEEvent
	nextID      int64

	// pending 状态（当前正在流式生成的文本块）
	pendingMu   sync.Mutex
	pendingID   int    // 正在生成的块 ID，-1 = 无
	pendingText string // 已生成的全部文本
}

var (
	broadcasterMu       sync.Mutex
	sessionBroadcasters = map[string]*SSEBroadcaster{}
)

func getOrCreateBroadcaster(sessionID string) *SSEBroadcaster {
	broadcasterMu.Lock()
	defer broadcasterMu.Unlock()
	if b, ok := sessionBroadcasters[sessionID]; ok {
		return b
	}
	b := &SSEBroadcaster{
		subscribers: make(map[int64]chan SSEEvent),
		pendingID:   -1,
	}
	sessionBroadcasters[sessionID] = b
	return b
}

func removeBroadcaster(sessionID string) {
	broadcasterMu.Lock()
	defer broadcasterMu.Unlock()
	if b, ok := sessionBroadcasters[sessionID]; ok {
		b.Close()
		delete(sessionBroadcasters, sessionID)
	}
}

// ========== 订阅管理 ==========

func (b *SSEBroadcaster) Subscribe() (int64, <-chan SSEEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	ch := make(chan SSEEvent, 2048)
	b.subscribers[id] = ch
	return id, ch
}

func (b *SSEBroadcaster) Unsubscribe(id int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subscribers[id]; ok {
		close(ch)
		delete(b.subscribers, id)
	}
}

func (b *SSEBroadcaster) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}

func (b *SSEBroadcaster) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subscribers {
		close(ch)
	}
	b.subscribers = nil
}

// broadcast 向所有订阅者推送事件（非阻塞，缓冲区满则丢弃并记录警告）
func (b *SSEBroadcaster) broadcast(event SSEEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subscribers {
		select {
		case ch <- event:
		default:
			log.Printf("[SSE] ⚠️ 丢弃事件 type=%s subtype=%s id=%d (通道满)", event.Type, event.Subtype, event.ID)
		}
	}
}

// ========== Agent 调用接口（高层语义方法） ==========

// Start AI 开始生成。前端切换到"运行中"状态。
func (b *SSEBroadcaster) Start() {
	b.broadcast(SSEEvent{Type: "start"})
}

// End AI 生成结束。errorMsg 为空表示正常完成。
func (b *SSEBroadcaster) End(errorMsg string) {
	b.clearPending()
	b.broadcast(SSEEvent{Type: "end", Error: errorMsg})
}

// EndLimitExceeded 达到最大轮次，前端应显示"继续推理"按钮。
func (b *SSEBroadcaster) EndLimitExceeded(errorMsg string) {
	b.clearPending()
	b.broadcast(SSEEvent{Type: "end_limit_exceeded", Error: errorMsg})
}

// StreamStart 开始一个新的流式文本块。返回块 ID（== len(session.Messages)）。
func (b *SSEBroadcaster) StreamStart(session *ChatSession) int {
	sessionMu.Lock()
	id := len(session.Messages)
	sessionMu.Unlock()

	b.pendingMu.Lock()
	b.pendingID = id
	b.pendingText = ""
	b.pendingMu.Unlock()

	b.broadcast(SSEEvent{Type: "stream_start", ID: id})
	return id
}

// Content 追加流式文本增量。
func (b *SSEBroadcaster) Content(id int, text string) {
	b.pendingMu.Lock()
	if b.pendingID == id {
		b.pendingText += text
	}
	b.pendingMu.Unlock()

	b.broadcast(SSEEvent{Type: "content", ID: id, Content: text})
}

// Reasoning 追加推理文本增量（DeepSeek 思考模式）。
// 已废弃：请使用 ThinkingStreamStart / ThinkingContent / ThinkingStreamEnd。
func (b *SSEBroadcaster) Reasoning(id int, text string) {
	b.broadcast(SSEEvent{Type: "reasoning", ID: id, Content: text})
}

// ThinkingStreamStart 开始一个新的思考流式块。返回块 ID。
// 与 StreamStart 的区别：持久化为 ChatMessage{Type:"thinking"}。
func (b *SSEBroadcaster) ThinkingStreamStart(session *ChatSession) int {
	sessionMu.Lock()
	id := len(session.Messages)
	msg := ChatMessage{Type: "thinking", Content: ""}
	session.Messages = append(session.Messages, msg)
	sessionMu.Unlock()

	dbAppendMessage(session.ID, msg)
	b.broadcast(SSEEvent{Type: "stream_start", Subtype: "thinking", ID: id})
	return id
}

// ThinkingContent 追加思考流式文本增量。
func (b *SSEBroadcaster) ThinkingContent(id int, text string) {
	b.broadcast(SSEEvent{Type: "content", Subtype: "thinking", ID: id, Content: text})
}

// ThinkingStreamEnd 结束思考流式块，回写持久化内容。
func (b *SSEBroadcaster) ThinkingStreamEnd(session *ChatSession, id int, fullText string) {
	b.broadcast(SSEEvent{Type: "stream_end", Subtype: "thinking", ID: id})

	if fullText != "" {
		sessionMu.Lock()
		valid := id >= 0 && id < len(session.Messages)
		if valid {
			session.Messages[id].Content = fullText
		}
		sessionMu.Unlock()
		if !valid {
			return
		}
		// 更新 DB 中已写入的空 content 为实际内容
		// seq 与内存消息索引一致。
		dbSetMessageContent(session.ID, id, fullText)
	}
}

// StreamEnd 结束当前流式块，持久化到 session.Messages 和数据库。
func (b *SSEBroadcaster) StreamEnd(session *ChatSession, id int) {
	b.pendingMu.Lock()
	fullText := b.pendingText
	b.pendingMu.Unlock()

	b.clearPending()
	b.broadcast(SSEEvent{Type: "stream_end", ID: id})

	if fullText != "" {
		msg := ChatMessage{Type: "message", Content: fullText}
		sessionMu.Lock()
		session.Messages = append(session.Messages, msg)
		sessionMu.Unlock()
		dbAppendMessage(session.ID, msg)
	}
}

// ToolCall 工具调用块（原子，立即持久化）。返回块 ID。
func (b *SSEBroadcaster) ToolCall(session *ChatSession, data map[string]interface{}) int {
	msg := ChatMessage{Type: "tool_call", Content: data}
	sessionMu.Lock()
	id := len(session.Messages)
	session.Messages = append(session.Messages, msg)
	sessionMu.Unlock()

	dbAppendMessage(session.ID, msg)
	b.broadcast(SSEEvent{Type: "block", Subtype: "tool_call", ID: id, Content: data})
	return id
}

// ToolResult 工具结果块（原子，立即持久化）。返回块 ID。
func (b *SSEBroadcaster) ToolResult(session *ChatSession, data map[string]interface{}) int {
	msg := ChatMessage{Type: "tool_result", Content: data}
	sessionMu.Lock()
	id := len(session.Messages)
	session.Messages = append(session.Messages, msg)
	sessionMu.Unlock()

	dbAppendMessage(session.ID, msg)
	b.broadcast(SSEEvent{Type: "block", Subtype: "tool_result", ID: id, Content: data})
	return id
}

// ApprovalRequest 审批请求块（无 result = 待审批，立即持久化）。返回块 ID。
func (b *SSEBroadcaster) ApprovalRequest(session *ChatSession, data map[string]interface{}) int {
	msg := ChatMessage{Type: "approval_request", Content: data}
	sessionMu.Lock()
	id := len(session.Messages)
	session.Messages = append(session.Messages, msg)
	sessionMu.Unlock()

	dbAppendMessage(session.ID, msg)
	b.broadcast(SSEEvent{Type: "block", Subtype: "approval_request", ID: id, Content: data})
	return id
}

// AskUser 向用户提问块（无 result = 待回答，立即持久化）。返回块 ID。
func (b *SSEBroadcaster) AskUser(session *ChatSession, data map[string]interface{}) int {
	msg := ChatMessage{Type: "ask_user", Content: data}
	sessionMu.Lock()
	id := len(session.Messages)
	session.Messages = append(session.Messages, msg)
	sessionMu.Unlock()

	dbAppendMessage(session.ID, msg)
	b.broadcast(SSEEvent{Type: "block", Subtype: "ask_user", ID: id, Content: data})
	return id
}

// UserMessage 用户消息块（立即持久化）。返回块 ID。
func (b *SSEBroadcaster) UserMessage(session *ChatSession, text string) int {
	msg := ChatMessage{Type: "user", Content: text}
	sessionMu.Lock()
	id := len(session.Messages)
	session.Messages = append(session.Messages, msg)
	sessionMu.Unlock()

	dbAppendMessage(session.ID, msg)
	b.broadcast(SSEEvent{Type: "block", Subtype: "user", ID: id, Content: text})
	return id
}

// Attachment 附件块（立即持久化）。返回块 ID。
func (b *SSEBroadcaster) Attachment(session *ChatSession, text string) int {
	msg := ChatMessage{Type: "attachment", Content: text}
	sessionMu.Lock()
	id := len(session.Messages)
	session.Messages = append(session.Messages, msg)
	sessionMu.Unlock()

	dbAppendMessage(session.ID, msg)
	b.broadcast(SSEEvent{Type: "block", Subtype: "attachment", ID: id, Content: text})
	return id
}

// Todo 更新 TODO 列表。items 为 []TodoItem 或兼容格式。
func (b *SSEBroadcaster) Todo(items interface{}) {
	b.broadcast(SSEEvent{Type: "todo", Content: items})
}

// TitleGenerated 广播标题生成事件。
func (b *SSEBroadcaster) TitleGenerated(sessionID, title string) {
	b.broadcast(SSEEvent{Type: "title_generated", Content: map[string]string{
		"session_id": sessionID,
		"title":      title,
	}})
}

// PendingForSubscriber 为新订阅者发送 pending 事件。
func (b *SSEBroadcaster) PendingForSubscriber(subID int64) {
	b.pendingMu.Lock()
	id := b.pendingID
	text := b.pendingText
	b.pendingMu.Unlock()

	b.mu.RLock()
	ch, ok := b.subscribers[subID]
	b.mu.RUnlock()

	if !ok {
		return
	}

	if id >= 0 {
		ch <- SSEEvent{Type: "pending", ID: id, Content: text}
	} else {
		ch <- SSEEvent{Type: "pending", ID: -1}
	}
}

// ClearPending 清除 pending 状态（Agent 停止时调用）
func (b *SSEBroadcaster) ClearPending() {
	b.clearPending()
}

// BypassStatus 广播绕过审批状态变更
func (b *SSEBroadcaster) BypassStatus(enabled bool) {
	b.broadcast(SSEEvent{Type: "bypass_status", Content: enabled})
}

// Update 更新已有块（审批结果、问答答案等）。前端按 id 原地更新 DOM。
func (b *SSEBroadcaster) Update(id int, subtype string, data interface{}) {
	b.broadcast(SSEEvent{Type: "update", ID: id, Subtype: subtype, Content: data})
}

func (b *SSEBroadcaster) clearPending() {
	b.pendingMu.Lock()
	b.pendingID = -1
	b.pendingText = ""
	b.pendingMu.Unlock()
}

// ========== SSE 写入工具函数 ==========

// writeSSE 将 SSEEvent 写入 http.ResponseWriter
func writeSSE(w http.ResponseWriter, event SSEEvent) error {
	data, _ := json.Marshal(event)
	_, err := fmt.Fprintf(w, "data: %s\n\n", data)
	if err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// replaySessionToWriter 重放所有历史消息为 SSE block 事件（每条附 id = 索引）
// 如果会话上次因达到最大轮次暂停，重放完后再发送 end_limit_exceeded 事件。
func replaySessionToWriter(w http.ResponseWriter, session *ChatSession) {
	for i, m := range session.Messages {
		writeSessionBlock(w, i, m)
	}
	if session.LimitExceeded {
		writeSSE(w, SSEEvent{Type: "end_limit_exceeded", Error: "达到最大轮次，AI 推理已暂停，可点击继续按钮恢复"})
	}
}

// writeSessionBlock 将单条 ChatMessage 写入为 SSE 事件（重放用）
func writeSessionBlock(w http.ResponseWriter, id int, m ChatMessage) {
	switch m.Type {
	case "user":
		writeSSE(w, SSEEvent{Type: "block", Subtype: "user", ID: id, Content: m.Content})
	case "attachment":
		writeSSE(w, SSEEvent{Type: "block", Subtype: "attachment", ID: id, Content: m.Content})
	case "message":
		// 历史消息用 stream_start/content/stream_end 三件套重放，保持前端流式块处理一致
		writeSSE(w, SSEEvent{Type: "stream_start", ID: id})
		if s, ok := m.Content.(string); ok && s != "" {
			writeSSE(w, SSEEvent{Type: "content", ID: id, Content: s})
		}
		writeSSE(w, SSEEvent{Type: "stream_end", ID: id})
	case "thinking":
		// 思考块：与 message 一样用流式三件套，但带 subtype
		writeSSE(w, SSEEvent{Type: "stream_start", Subtype: "thinking", ID: id})
		if s, ok := m.Content.(string); ok && s != "" {
			writeSSE(w, SSEEvent{Type: "content", Subtype: "thinking", ID: id, Content: s})
		}
		writeSSE(w, SSEEvent{Type: "stream_end", Subtype: "thinking", ID: id})
	case "summary":
		writeSSE(w, SSEEvent{Type: "block", Subtype: "summary", ID: id, Content: m.Content})
	case "tool_call", "tool":
		writeSSE(w, SSEEvent{Type: "block", Subtype: "tool_call", ID: id, Content: m.Content})
	case "tool_result":
		writeSSE(w, SSEEvent{Type: "block", Subtype: "tool_result", ID: id, Content: m.Content})
	case "approval_request":
		writeSSE(w, SSEEvent{Type: "block", Subtype: "approval_request", ID: id, Content: m.Content})
	case "ask_user":
		writeSSE(w, SSEEvent{Type: "block", Subtype: "ask_user", ID: id, Content: m.Content})
	case "todo":
		writeSSE(w, SSEEvent{Type: "todo", Content: m.Content})
	default:
		if m.Type != "system" {
			writeSSE(w, SSEEvent{Type: "block", Subtype: "message", ID: id, Content: m.Content})
		}
	}
}
