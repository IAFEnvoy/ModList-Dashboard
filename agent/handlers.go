package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func HandleAgentChatStream(w http.ResponseWriter, r *http.Request) {
	defer func() { recover() }()
	var body struct {
		Agent      string `json:"agent"`
		Provider   string `json:"provider"`
		Project    string `json:"project"`
		Task       string `json:"task"`
		Message    string `json:"message"`
		Session    string `json:"session"`
		Attachment string `json:"attachment"`
		Bypass     bool   `json:"bypass"`
		Start      bool   `json:"start"`
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	sessionID := body.Session
	var session *ChatSession
	if sessionID == "" {
		if body.Provider == "" {
			body.Provider = DefaultModelKey()
		}
		def := FindAgentDef(body.Agent)
		if def != nil && InstanceCount(body.Agent) >= def.MaxInstances {
			writeSSE(w, SSEEvent{Type: "end", Error: "Agent 已达最大并发数"})
			return
		}
		p, err := CreateSession(body.Agent, body.Provider, "", body.Project, resolveProjectPath(body.Project))
		if err != nil {
			writeSSE(w, SSEEvent{Type: "end", Error: err.Error()})
			return
		}
		session = p
		sessionID = session.ID
	} else {
		session = GetSession(sessionID)
		if session == nil {
			writeSSE(w, SSEEvent{Type: "end", Error: "会话不存在"})
			return
		}
	}
	msg := body.Message
	if msg == "" {
		msg = body.Task
	}
	if strings.TrimSpace(msg) == "/compact" { /* TODO compact */
		writeSSE(w, SSEEvent{Type: "end"})
		return
	}

	if body.Provider != "" && body.Provider != session.Provider {
		switchModel(session, sessionID, body.Provider)
	}
	rt := getOrCreateRT(session, sessionID)
	if ShouldAutoCompact(session) {
		CompactSession(session)
	}
	if body.Bypass {
		session.BypassApproval = true
	}

	needStart := false
	if !session.Running && !strings.HasPrefix(strings.TrimSpace(msg), "/compact") {
		if msg != "" || body.Start {
			needStart = true
			sessionMu.Lock()
			if msg != "" {
				if body.Attachment != "" {
					session.Messages = append(session.Messages, ChatMessage{Type: "user", Content: msg}, ChatMessage{Type: "attachment", Content: body.Attachment})
					dbAppendMessage(sessionID, ChatMessage{Type: "user", Content: msg})
					dbAppendMessage(sessionID, ChatMessage{Type: "attachment", Content: body.Attachment})
				} else {
					session.Messages = append(session.Messages, ChatMessage{Type: "user", Content: msg})
					dbAppendMessage(sessionID, ChatMessage{Type: "user", Content: msg})
				}
				// 首次用户消息 → 异步生成标题
				if session.Title == "" && !isTitleGenerated(sessionID) {
					markTitleGenerated(sessionID)
					generateTitleAsync(sessionID, msg)
				}
				// 持久化会话元数据（title, bypass, pending 等）
				go SaveSessions()
			}
			session.Running = true
			sessionMu.Unlock()
		}
	}
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()
	// 发送 session ID 事件（新会话时前端靠 onSession 回调获取 ID）
	writeSSE(w, SSEEvent{Type: "session", Content: sessionID})
	replaySessionToWriter(w, session)

	pub := getOrCreateBroadcaster(sessionID)
	subID, subCh := pub.Subscribe()
	log.Printf("[SSE] +订阅 %s (%d)", sessionID, pub.SubscriberCount())
	pub.PendingForSubscriber(subID)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer pub.Unsubscribe(subID)
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[SSE] writer panic recovered: %v", rec)
			}
			r := pub.SubscriberCount()
			log.Printf("[SSE] -订阅 %s (%d)", sessionID, r)
			if r == 0 {
				removeBroadcaster(sessionID)
			}
		}()
		for {
			select {
			case e, ok := <-subCh:
				if !ok {
					return
				}
				// 写入前检查连接是否已断开
				select {
				case <-r.Context().Done():
					return
				default:
				}
				writeSSE(w, e)
			case <-r.Context().Done():
				return
			}
		}
	}()
	if needStart {
		go startAgent(session, msg, rt, pub)
	}
	select {
	case <-writerDone:
	case <-r.Context().Done():
		// 连接断开后仍需等待 writer goroutine 结束，
		// 避免 handler 返回后 ResponseWriter 失效导致 panic
		select {
		case <-writerDone:
		case <-time.After(3 * time.Second):
			log.Printf("[SSE] 等待 writer 超时 %s", sessionID)
		}
	}
}

func startAgent(session *ChatSession, userMsg string, rt *agentRuntime, pub *SSEBroadcaster) error {
	sid := session.ID
	log.Printf("[Agent] 开始 %s", sid)
	ctx, cancel := context.WithCancel(context.Background())
	sessionMu.Lock()
	session.StopCh = make(chan struct{})
	session.CancelFunc = cancel
	session.LimitExceeded = false // 新请求开始，清除限制标记
	sessionMu.Unlock()
	defer cancel()
	ctx = withExecSession(ctx, sid)
	var csid int
	var sa bool
	var tsa bool            // thinking stream active
	var tcid int            // thinking stream block ID
	var thinkingText string // accumulated thinking content
	pub.Start()
	reply, err := RunAgent(rt, session, userMsg, session.ProjectPath, func(chunk string) {
		// TODO 更新
		if strings.HasPrefix(chunk, "__TODO_UPDATE__") {
			pub.Todo(map[string]string{"summary": strings.TrimPrefix(chunk, "__TODO_UPDATE__")})
			return
		}
		// 推理内容（DeepSeek 思考模式）→ 独立的 thinking 流式块
		if strings.HasPrefix(chunk, "__REASONING__") {
			reasoningText := strings.TrimPrefix(chunk, "__REASONING__")
			if !tsa {
				// 关闭前方普通文本流（如果有）
				if sa {
					pub.StreamEnd(session, csid)
					sa = false
				}
				tcid = pub.ThinkingStreamStart(session)
				tsa = true
				thinkingText = ""
			}
			thinkingText += reasoningText
			pub.ThinkingContent(tcid, reasoningText)
			return
		}
		// 工具调用前关闭 thinking 块（LLM 本轮响应结束）
		if tsa && (strings.HasPrefix(chunk, "__TOOL__") || strings.HasPrefix(chunk, "__TOOL_RESULT__")) {
			pub.ThinkingStreamEnd(session, tcid, thinkingText)
			tsa = false
			thinkingText = ""
		}
		// 工具结果块
		if strings.HasPrefix(chunk, "__TOOL_RESULT__") {
			// 关闭当前流式块，持久化到 session.Messages
			if sa {
				pub.StreamEnd(session, csid)
				sa = false
			}
			jsonStr := strings.TrimPrefix(chunk, "__TOOL_RESULT__")
			var obj map[string]interface{}
			if json.Unmarshal([]byte(jsonStr), &obj) == nil {
				pub.ToolResult(session, obj)
			}
			return
		}
		// 工具调用块
		if strings.HasPrefix(chunk, "__TOOL__") {
			// 关闭当前流式块，持久化到 session.Messages
			if sa {
				pub.StreamEnd(session, csid)
				sa = false
			}
			jsonStr := strings.TrimPrefix(chunk, "__TOOL__")
			var obj map[string]interface{}
			if json.Unmarshal([]byte(jsonStr), &obj) == nil {
				pub.ToolCall(session, obj)
			}
			return
		}
		// 普通文本流
		if !sa {
			csid = pub.StreamStart(session)
			sa = true
		}
		pub.Content(csid, chunk)
	}, ctx)
	if session.Agent == IssuePreprocessorID && ctx.Err() != nil {
		err = ctx.Err()
	}
	if sa {
		pub.StreamEnd(session, csid)
	}
	if tsa {
		pub.ThinkingStreamEnd(session, tcid, thinkingText)
	}
	if reply != "" && !sa {
		s := pub.StreamStart(session)
		pub.Content(s, reply)
		pub.StreamEnd(session, s)
	}
	var ee string
	if err != nil {
		log.Printf("[Agent] err: %v", err)
		ee = err.Error()
	}
	sessionMu.Lock()
	session.Running = false
	session.StopCh = nil
	session.CancelFunc = nil
	if errors.Is(err, errMaxRoundsExceeded) {
		session.LimitExceeded = true
	}
	sessionMu.Unlock()
	SaveSessions()
	if errors.Is(err, errMaxRoundsExceeded) {
		pub.EndLimitExceeded(ee)
	} else {
		pub.End(ee)
	}
	return err
}

func HandleAgentSessions(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	pfx := "/api/agent/sessions/"
	if len(path) > len(pfx) {
		rest := path[len(pfx):]
		if strings.HasSuffix(rest, "/archive") || strings.HasSuffix(rest, "/restore") {
			HandleSessionArchive(w, r)
			return
		}
		if strings.Contains(rest, "/chunks") {
			HandleSessionChunks(w, r)
			return
		}
		if strings.Contains(rest, "/blocks/") {
			HandleBlockUpdate(w, r)
			return
		}
		if strings.Contains(rest, "/todos/clear") {
			HandleSessionTodosClear(w, r)
			return
		}
		if strings.Contains(rest, "/bypass") {
			HandleSessionBypass(w, r)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")

	// POST /api/agent/sessions — 创建新会话（REST，不启动流）
	if r.Method == "POST" && path == "/api/agent/sessions" {
		var cb struct {
			Agent      string `json:"agent"`
			Provider   string `json:"provider"`
			Project    string `json:"project"`
			Message    string `json:"message"`
			Attachment string `json:"attachment"`
			Bypass     bool   `json:"bypass"`
		}
		if json.NewDecoder(r.Body).Decode(&cb) != nil {
			http.Error(w, `{"error":"bad request"}`, 400)
			return
		}
		if cb.Provider == "" {
			cb.Provider = DefaultModelKey()
		}
		s, err := CreateSession(cb.Agent, cb.Provider, cb.Message, cb.Project, resolveProjectPath(cb.Project))
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, 400)
			return
		}
		if cb.Message != "" {
			sessionMu.Lock()
			s.Messages = append(s.Messages, ChatMessage{Type: "user", Content: cb.Message})
			sessionMu.Unlock()
			dbAppendMessage(s.ID, ChatMessage{Type: "user", Content: cb.Message})
		}
		if cb.Attachment != "" {
			sessionMu.Lock()
			s.Messages = append(s.Messages, ChatMessage{Type: "attachment", Content: cb.Attachment})
			sessionMu.Unlock()
			dbAppendMessage(s.ID, ChatMessage{Type: "attachment", Content: cb.Attachment})
		}
		if cb.Bypass {
			s.BypassApproval = true
			t := true
			dbUpdateSessionMeta(s.ID, nil, &t, nil)
		}
		go SaveSessions()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": s.ID, "agent": s.Agent, "provider": s.Provider,
			"project": s.Project, "project_path": s.ProjectPath,
			"title": s.Title, "messages": s.Messages,
			"bypass_approval": s.BypassApproval,
			"created_at":      s.CreatedAt, "updated_at": s.UpdatedAt,
		})
		return
	}
	if r.Method == "GET" && len(path) > len(pfx) {
		sid := path[len(pfx):]
		if sid != "" && !strings.Contains(sid, "/") {
			s := GetSession(sid)
			if s == nil {
				http.Error(w, `{"error":"not found"}`, 404)
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"id": s.ID, "agent": s.Agent, "provider": s.Provider, "project": s.Project, "project_path": s.ProjectPath, "title": s.Title, "task": s.Task, "messages": s.Messages, "todos": s.Todos, "running": s.Running, "bypass_approval": s.BypassApproval, "created_at": s.CreatedAt, "updated_at": s.UpdatedAt, "context_usage": GetContextUsage(s)})
			return
		}
	}
	if r.Method == "DELETE" {
		if !strings.HasPrefix(path, pfx) || len(path) <= len(pfx) {
			http.Error(w, `{"error":"session id required"}`, http.StatusBadRequest)
			return
		}
		sid := path[len(pfx):]
		if i := strings.Index(sid, "/"); i >= 0 {
			sid = sid[:i]
		}
		s := GetSession(sid)
		if s == nil {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("permanent") != "true" {
			if s.Running {
				http.Error(w, `{"error":"cannot archive a running session"}`, http.StatusConflict)
				return
			}
			if err := setSessionArchived(sid, true); err != nil {
				http.Error(w, `{"error":"archive failed"}`, http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "archived"})
			return
		}
		if !s.Archived {
			http.Error(w, `{"error":"only archived sessions can be permanently deleted"}`, http.StatusBadRequest)
			return
		}
		if !DeleteSession(sid) {
			http.Error(w, `{"error":"delete failed"}`, http.StatusNotFound)
			return
		}
		removeBroadcaster(sid)
		dbDeleteSharesBySession(sid)
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
		return
	}
	type sw struct {
		ChatSession
		ContextUsage ContextUsage `json:"context_usage"`
	}
	var ss []sw
	archivedOnly := strings.EqualFold(r.URL.Query().Get("archived"), "true")
	sessionMu.Lock()
	for _, s := range AgentSessions {
		if s.Archived != archivedOnly {
			continue
		}
		ss = append(ss, sw{ChatSession: *s, ContextUsage: GetContextUsage(s)})
	}
	sessionMu.Unlock()
	sort.Slice(ss, func(i, j int) bool { return ss[i].UpdatedAt > ss[j].UpdatedAt })
	json.NewEncoder(w).Encode(ss)
}

func setSessionArchived(sid string, archived bool) error {
	s := GetSession(sid)
	if s == nil {
		return fmt.Errorf("session not found")
	}
	sessionMu.Lock()
	s.Archived = archived
	s.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	sessionMu.Unlock()
	return dbSetSessionArchived(sid, archived)
}

func HandleSessionArchive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST required"}`, http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/agent/sessions/")
	archived := false
	if strings.HasSuffix(rest, "/archive") {
		archived = true
	} else if !strings.HasSuffix(rest, "/restore") {
		http.Error(w, `{"error":"invalid archive action"}`, http.StatusBadRequest)
		return
	}
	sid := strings.TrimSuffix(strings.TrimSuffix(rest, "/archive"), "/restore")
	if sid == "" || strings.Contains(sid, "/") {
		http.Error(w, `{"error":"invalid session"}`, http.StatusBadRequest)
		return
	}
	s := GetSession(sid)
	if s == nil {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	if s.Running {
		http.Error(w, `{"error":"cannot change archive state while running"}`, http.StatusConflict)
		return
	}
	if err := setSessionArchived(sid, archived); err != nil {
		http.Error(w, `{"error":"archive state update failed"}`, http.StatusInternalServerError)
		return
	}
	status := "restored"
	if archived {
		status = "archived"
	}
	json.NewEncoder(w).Encode(map[string]string{"status": status})
}

func HandleSessionChunks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	sid := extractSessionID(r.URL.Path, "/api/agent/sessions/")
	s := GetSession(sid)
	if s == nil {
		http.Error(w, `{"error":"not found"}`, 404)
		return
	}
	from, _ := strconv.Atoi(r.URL.Query().Get("from"))
	to, _ := strconv.Atoi(r.URL.Query().Get("to"))
	last, _ := strconv.Atoi(r.URL.Query().Get("last"))
	msgs := s.Messages
	total := len(msgs)
	if last > 0 {
		if last > total {
			last = total
		}
		from = total - last
		to = total
	}
	if from < 0 {
		from = 0
	}
	if to <= 0 || to > total {
		to = total
	}
	if from >= total {
		json.NewEncoder(w).Encode(map[string]interface{}{"chunks": []ChatMessage{}, "total": total})
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"chunks": msgs[from:to], "from": from, "to": to, "total": total})
}

func HandleBlockUpdate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	sid, bid := extractSessionAndBlock(r.URL.Path)
	s := GetSession(sid)
	if s == nil {
		http.Error(w, `{"error":"not found"}`, 404)
		return
	}
	var b struct {
		Result interface{} `json:"result"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	var bt string
	var ub ChatMessage
	var updated bool
	sessionMu.Lock()
	if bid >= 0 && bid < len(s.Messages) {
		m := &s.Messages[bid]
		bt = m.Type
		if bt == "approval_request" || bt == "ask_user" {
			if bm, ok := m.Content.(map[string]interface{}); ok {
				bm["result"] = b.Result
				m.Content = bm
			}
		}
		ub = *m
		updated = true
	}
	sessionMu.Unlock()
	// 持久化块更新到 DB（seq == bid，因为 system 消息始终在索引 0 且 seq=0）
	if updated {
		dbSetMessageContent(sid, bid, msgContent(ub))
	}
	pub := getOrCreateBroadcaster(sid)
	// 使用 update 事件（非 block），前端按 id 原地更新 DOM
	pub.Update(bid, ub.Type, ub.Content)
	go SaveSessions()
	if bt == "approval_request" {
		ap := false
		switch v := b.Result.(type) {
		case bool:
			ap = v
		case string:
			ap = (v == "true" || v == "approved")
		}
		approvalChannelsMu.Lock()
		ch, ok := approvalChannels[sid]
		if ok {
			delete(approvalChannels, sid)
		}
		approvalChannelsMu.Unlock()
		if ok {
			ch <- ap
		}
	}
	if bt == "ask_user" {
		ans := ""
		if s, ok := b.Result.(string); ok {
			ans = s
		}
		AnswerQuestion(sid, ans)
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func HandleSessionBypass(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	sid := extractSessionID(r.URL.Path, "/api/agent/sessions/")
	s := GetSession(sid)
	if s == nil {
		http.Error(w, `{"error":"not found"}`, 404)
		return
	}
	var b struct {
		Bypass bool `json:"bypass"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	sessionMu.Lock()
	s.BypassApproval = b.Bypass
	sessionMu.Unlock()
	getOrCreateBroadcaster(sid).BypassStatus(b.Bypass)
	go SaveSessions()
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "bypass": b.Bypass})
}

func HandleSessionTodosClear(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	sid := extractSessionID(r.URL.Path, "/api/agent/sessions/")
	s := GetSession(sid)
	if s == nil {
		http.Error(w, `{"error":"not found"}`, 404)
		return
	}
	sessionMu.Lock()
	s.Todos = nil
	sessionMu.Unlock()
	dbClearTodos(sid)
	getOrCreateBroadcaster(sid).Todo([]TodoItem{})
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func HandleAgentApproval(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var b struct {
		Session  string `json:"session"`
		Approved bool   `json:"approved"`
	}
	if json.NewDecoder(r.Body).Decode(&b) != nil || b.Session == "" {
		http.Error(w, `{"error":"bad request"}`, 400)
		return
	}
	approvalChannelsMu.Lock()
	ch, ok := approvalChannels[b.Session]
	if ok {
		delete(approvalChannels, b.Session)
	}
	approvalChannelsMu.Unlock()
	if !ok {
		http.Error(w, `{"error":"no pending"}`, 404)
		return
	}
	ch <- b.Approved
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func HandleAgentAnswer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var b struct {
		Session string `json:"session"`
		Answer  string `json:"answer"`
	}
	if json.NewDecoder(r.Body).Decode(&b) != nil || b.Session == "" {
		http.Error(w, `{"error":"bad request"}`, 400)
		return
	}
	if AnswerQuestion(b.Session, b.Answer) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	} else {
		http.Error(w, `{"error":"no pending"}`, 404)
	}
}

func HandleAgentStop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var b struct {
		Session string `json:"session"`
	}
	if json.NewDecoder(r.Body).Decode(&b) != nil || b.Session == "" {
		http.Error(w, `{"error":"bad request"}`, 400)
		return
	}
	s := GetSession(b.Session)
	if s == nil {
		http.Error(w, `{"error":"not found"}`, 404)
		return
	}
	sessionMu.Lock()
	if s.CancelFunc != nil {
		s.CancelFunc()
	}
	if s.StopCh != nil {
		close(s.StopCh)
		s.StopCh = nil
	}
	s.Running = false
	s.CancelFunc = nil
	sessionMu.Unlock()
	approvalChannelsMu.Lock()
	if ch, ok := approvalChannels[b.Session]; ok {
		delete(approvalChannels, b.Session)
		select {
		case ch <- false:
		default:
		}
	}
	approvalChannelsMu.Unlock()
	pub := getOrCreateBroadcaster(b.Session)
	pub.ClearPending()
	pub.End("stopped")
	go SaveSessions()
	json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
}

func HandleAgentCompact(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var b struct {
		Session string `json:"session"`
	}
	if json.NewDecoder(r.Body).Decode(&b) != nil || b.Session == "" {
		http.Error(w, `{"error":"bad request"}`, 400)
		return
	}
	s := GetSession(b.Session)
	if s == nil {
		http.Error(w, `{"error":"not found"}`, 404)
		return
	}
	r2 := CompactSession(s)
	go SaveSessions()
	json.NewEncoder(w).Encode(r2)
}

func HandleAgentInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"agents": AgentDefs, "mcps": MCPServers, "models": safeModels(), "builtin_providers": AllBuiltinProviders()})
}
func HandleMCPRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	RefreshAllMCPs()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"message": "MCP 已刷新", "mcps": MCPServers})
}

func resolveProjectPath(project string) string {
	if project == "" || ModMetaLookup == nil {
		return ""
	}
	meta := ModMetaLookup(project)
	if meta == nil {
		return ""
	}
	r, err := ResolveProject(*meta, AgentProjectPaths)
	if err != nil {
		return ""
	}
	return r
}
func switchModel(s *ChatSession, sid, pk string) {
	i := FindModel(pk)
	if i == nil {
		return
	}
	d := FindAgentDef(s.Agent)
	t := BuildTools(d)
	rt, err := getOrCreateLLM(*i, t)
	if err != nil {
		return
	}
	agentMu.Lock()
	agentInstances[sid] = rt
	agentMu.Unlock()
	s.Provider = pk
}
func getOrCreateRT(s *ChatSession, sid string) *agentRuntime {
	agentMu.RLock()
	rt := agentInstances[sid]
	agentMu.RUnlock()
	if rt != nil {
		return rt
	}
	d := FindAgentDef(s.Agent)
	i := FindModel(s.Provider)
	if d == nil || i == nil {
		return nil
	}
	t := BuildTools(d)
	var err error
	rt, err = getOrCreateLLM(*i, t)
	if err != nil {
		log.Printf("[Agent] LLM创建失败: %v", err)
		return nil
	}
	agentMu.Lock()
	agentInstances[sid] = rt
	agentMu.Unlock()
	return rt
}
func extractSessionID(path, pfx string) string {
	if !strings.HasPrefix(path, pfx) {
		return ""
	}
	r := path[len(pfx):]
	if i := strings.Index(r, "/"); i >= 0 {
		return r[:i]
	}
	return r
}

// HandleAgentSearch 全文搜索会话消息。
func HandleAgentSearch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "GET" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, `{"error":"query required"}`, 400)
		return
	}
	limit := 20
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 100 {
		limit = l
	}
	results := dbSearchMessages(q, limit)
	if results == nil {
		results = []SearchResult{}
	}
	json.NewEncoder(w).Encode(results)
}

// HandleAgentSearchSessions 全库搜索会话（标题/Agent/项目/消息内容）。
func HandleAgentSearchSessions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "GET" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, `{"error":"query required"}`, 400)
		return
	}
	limit := 20
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 100 {
		limit = l
	}
	results := dbSearchSessions(q, limit)
	if results == nil {
		results = []SessionSearchResult{}
	}
	json.NewEncoder(w).Encode(results)
}

func extractSessionAndBlock(path string) (string, int) {
	pfx := "/api/agent/sessions/"
	if !strings.HasPrefix(path, pfx) {
		return "", -1
	}
	r := path[len(pfx):]
	i := strings.Index(r, "/blocks/")
	if i < 0 {
		return "", -1
	}
	bid, _ := strconv.Atoi(r[i+len("/blocks/"):])
	return r[:i], bid
}
func safeModels() []AIModelInfo {
	var o []AIModelInfo
	for _, m := range Models {
		m2 := m
		m2.APIKey = ""
		o = append(o, m2)
	}
	return o
}
