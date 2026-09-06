package agent

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "github.com/glebarez/sqlite"
)

var db *sql.DB
var dbMu sync.Mutex

const dbPath = "data/sessions.db"

// InitDB 初始化 SQLite 数据库并创建表结构。
func InitDB() error {
	dbMu.Lock()
	defer dbMu.Unlock()

	var err error
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}
	db.SetMaxOpenConns(1)

	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA foreign_keys=ON")
	db.Exec("PRAGMA busy_timeout=5000")

	if err := createSchema(); err != nil {
		return fmt.Errorf("创建表结构失败: %w", err)
	}

	return nil
}

// CloseDB 关闭数据库连接。
func CloseDB() {
	if db != nil {
		db.Close()
	}
}

// SaveSessions 将所有会话的元数据持久化到 SQLite。
// 消息块在生成时已通过 dbAppendMessage 逐个写入，此处仅更新会话级字段。
func SaveSessions() {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range AgentSessions {
		AgentSessions[i].UpdatedAt = now
		dbUpdateSessionFull(AgentSessions[i])
	}
}

// LoadSessions 从 SQLite 加载所有会话到内存。
func LoadSessions() {
	sessionMu.Lock()
	defer sessionMu.Unlock()

	list := dbListSessions()
	var count int
	for _, s := range list {
		if FindAgentDef(s.Agent) == nil {
			continue
		}
		full := dbGetSession(s.ID)
		if full == nil {
			continue
		}
		AgentSessions = append(AgentSessions, full)
		count++
	}
	if count > 0 {
		log.Printf("[Session] 从数据库恢复了 %d 个会话", count)
	}
}

func createSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		id              TEXT PRIMARY KEY,
		agent           TEXT NOT NULL,
		provider        TEXT NOT NULL,
		instance_id     TEXT DEFAULT '',
		project         TEXT DEFAULT '',
		project_path    TEXT DEFAULT '',
		task            TEXT DEFAULT '',
		title           TEXT DEFAULT '',
		bypass_approval INTEGER DEFAULT 0,
		limit_exceeded  INTEGER DEFAULT 0,
		pending_question TEXT DEFAULT '{}',
		pending_approval TEXT DEFAULT '{}',
		archived        INTEGER NOT NULL DEFAULT 0,
		created_at      TEXT NOT NULL,
		updated_at      TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at DESC);
	CREATE INDEX IF NOT EXISTS idx_sessions_agent   ON sessions(agent);

	CREATE TABLE IF NOT EXISTS messages (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
		seq        INTEGER NOT NULL,
		type       TEXT NOT NULL,
		tool       TEXT DEFAULT '',
		content    TEXT NOT NULL,
		tokens     INTEGER DEFAULT 0,
		UNIQUE(session_id, seq)
	);
	CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, seq);

	CREATE TABLE IF NOT EXISTS todos (
		id         TEXT PRIMARY KEY,
		session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
		title      TEXT NOT NULL,
		status     TEXT NOT NULL DEFAULT 'not-started',
		sort_order INTEGER DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_todos_session ON todos(session_id);

	-- FTS5 全文搜索（仅索引 user 和 message 类型，排除 thinking/tool 等）
	CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
		session_id UNINDEXED,
		content,
		content=messages,
		content_rowid=id
	);

	-- 触发器：INSERT 时自动同步到 FTS（仅可搜索类型）
	CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
		INSERT INTO messages_fts(rowid, session_id, content)
		SELECT NEW.id, NEW.session_id, NEW.content
		WHERE NEW.type IN ('user', 'message', 'assistant');
	END;

	-- 触发器：DELETE 时自动同步
	CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
		INSERT INTO messages_fts(messages_fts, rowid, session_id, content)
		VALUES ('delete', OLD.id, OLD.session_id, OLD.content);
	END;

	-- 触发器：UPDATE 时自动同步
	CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
		INSERT INTO messages_fts(messages_fts, rowid, session_id, content)
		VALUES ('delete', OLD.id, OLD.session_id, OLD.content);
		INSERT INTO messages_fts(rowid, session_id, content)
		SELECT NEW.id, NEW.session_id, NEW.content
		WHERE NEW.type IN ('user', 'message', 'assistant');
	END;

	CREATE TABLE IF NOT EXISTS shares (
		id          TEXT PRIMARY KEY,
		session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
		block_ranges TEXT NOT NULL DEFAULT '',
		created_at  TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_shares_session ON shares(session_id);
	`
	_, err := db.Exec(schema)
	if err != nil {
		return err
	}
	return nil
}

// ========== Session CRUD ==========

// dbCreateSession 在数据库中创建新会话，返回 ChatSession 指针。
// 调用方需自行创建 LLM 运行时并注册到 agentInstances。
func dbCreateSession(s *ChatSession) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	pqJSON, _ := json.Marshal(s.PendingQuestion)
	paJSON, _ := json.Marshal(s.PendingApproval)
	bypass := 0
	if s.BypassApproval {
		bypass = 1
	}
	var limitEx int
	if s.LimitExceeded {
		limitEx = 1
	}

	_, err := db.Exec(`INSERT INTO sessions(id,agent,provider,instance_id,project,project_path,task,title,bypass_approval,limit_exceeded,pending_question,pending_approval,archived,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.Agent, s.Provider, s.InstanceID, s.Project, s.ProjectPath, s.Task, s.Title, bypass, limitEx, string(pqJSON), string(paJSON), boolInt(s.Archived), s.CreatedAt, s.CreatedAt)
	if err != nil {
		return err
	}

	// 插入初始消息（system prompt）
	for seq, msg := range s.Messages {
		contentStr := msgContent(msg)
		db.Exec(`INSERT INTO messages(session_id,seq,type,tool,content,tokens) VALUES(?,?,?,?,?,?)`,
			s.ID, seq, msg.Type, msg.Tool, contentStr, msg.Tokens)
	}
	return nil
}

// dbGetSession 从数据库加载单个会话。
func dbGetSession(id string) *ChatSession {
	dbMu.Lock()
	defer dbMu.Unlock()

	row := db.QueryRow(`SELECT id,agent,provider,instance_id,project,project_path,task,title,bypass_approval,limit_exceeded,pending_question,pending_approval,archived,created_at,updated_at FROM sessions WHERE id=?`, id)

	var s ChatSession
	var bypass, limitEx, archived int
	var pqStr, paStr string
	err := row.Scan(&s.ID, &s.Agent, &s.Provider, &s.InstanceID, &s.Project, &s.ProjectPath, &s.Task, &s.Title, &bypass, &limitEx, &pqStr, &paStr, &archived, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil
	}
	s.BypassApproval = bypass != 0
	s.LimitExceeded = limitEx != 0
	s.Archived = archived != 0
	json.Unmarshal([]byte(pqStr), &s.PendingQuestion)
	json.Unmarshal([]byte(paStr), &s.PendingApproval)

	// 加载 messages
	rows, err := db.Query(`SELECT seq,type,tool,content,tokens FROM messages WHERE session_id=? ORDER BY seq`, id)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var m ChatMessage
			var contentStr string
			rows.Scan(&m.Seq, &m.Type, &m.Tool, &contentStr, &m.Tokens)
			m.Content = parseContentJSON(contentStr)
			s.Messages = append(s.Messages, m)
		}
	}

	// 加载 todos
	tRows, err := db.Query(`SELECT id,title,status FROM todos WHERE session_id=? ORDER BY sort_order`, id)
	if err == nil {
		defer tRows.Close()
		for tRows.Next() {
			var t TodoItem
			tRows.Scan(&t.ID, &t.Title, &t.Status)
			s.Todos = append(s.Todos, t)
		}
	}

	return &s
}

// dbDeleteSession 从数据库删除会话及其消息、待办。
func dbDeleteSession(id string) {
	dbMu.Lock()
	defer dbMu.Unlock()
	db.Exec("DELETE FROM sessions WHERE id=?", id)
	clearTitleGenerated(id)
}

// dbListSessions 列出所有会话（用于前端历史列表）。
func dbListSessions() []ChatSession {
	dbMu.Lock()
	defer dbMu.Unlock()

	rows, err := db.Query(`SELECT id,agent,provider,project,project_path,task,title,bypass_approval,limit_exceeded,pending_question,pending_approval,archived,created_at,updated_at FROM sessions ORDER BY updated_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var sessions []ChatSession
	for rows.Next() {
		var s ChatSession
		var bypass, limitEx, archived int
		var pqStr, paStr string
		rows.Scan(&s.ID, &s.Agent, &s.Provider, &s.Project, &s.ProjectPath, &s.Task, &s.Title, &bypass, &limitEx, &pqStr, &paStr, &archived, &s.CreatedAt, &s.UpdatedAt)
		s.BypassApproval = bypass != 0
		s.LimitExceeded = limitEx != 0
		s.Archived = archived != 0
		json.Unmarshal([]byte(pqStr), &s.PendingQuestion)
		json.Unmarshal([]byte(paStr), &s.PendingApproval)
		sessions = append(sessions, s)
	}
	return sessions
}

// dbUpdateSessionMeta 更新会话元数据（title, bypass, limitExceeded）。
func dbUpdateSessionMeta(id string, title *string, bypass *bool, limitExceeded *bool) {
	dbMu.Lock()
	defer dbMu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	if title != nil {
		db.Exec("UPDATE sessions SET title=?, updated_at=? WHERE id=?", *title, now, id)
	}
	if bypass != nil {
		v := 0
		if *bypass {
			v = 1
		}
		db.Exec("UPDATE sessions SET bypass_approval=?, updated_at=? WHERE id=?", v, now, id)
	}
	if limitExceeded != nil {
		v := 0
		if *limitExceeded {
			v = 1
		}
		db.Exec("UPDATE sessions SET limit_exceeded=?, updated_at=? WHERE id=?", v, now, id)
	}
}

// dbUpdateSessionFull 全量更新会话元数据（title, bypass, limitExceeded, pending, updated_at）。
func dbUpdateSessionFull(s *ChatSession) {
	dbMu.Lock()
	defer dbMu.Unlock()

	pqJSON, _ := json.Marshal(s.PendingQuestion)
	paJSON, _ := json.Marshal(s.PendingApproval)
	bypass := 0
	if s.BypassApproval {
		bypass = 1
	}
	limitEx := 0
	if s.LimitExceeded {
		limitEx = 1
	}

	db.Exec(`UPDATE sessions SET title=?, bypass_approval=?, limit_exceeded=?, pending_question=?, pending_approval=?, archived=?, updated_at=? WHERE id=?`,
		s.Title, bypass, limitEx, string(pqJSON), string(paJSON), boolInt(s.Archived), s.UpdatedAt, s.ID)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func dbSetSessionArchived(id string, archived bool) error {
	dbMu.Lock()
	defer dbMu.Unlock()
	_, err := db.Exec("UPDATE sessions SET archived=?, updated_at=? WHERE id=?", boolInt(archived), time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// dbAppendMessage 追加单条消息到数据库。
func dbAppendMessage(sessionID string, msg ChatMessage) {
	dbMu.Lock()
	defer dbMu.Unlock()

	contentStr := msgContent(msg)
	now := time.Now().UTC().Format(time.RFC3339)
	// 获取下一个 seq
	var maxSeq int
	db.QueryRow("SELECT COALESCE(MAX(seq),-1) FROM messages WHERE session_id=?", sessionID).Scan(&maxSeq)
	seq := maxSeq + 1

	db.Exec(`INSERT INTO messages(session_id,seq,type,tool,content,tokens) VALUES(?,?,?,?,?,?)`,
		sessionID, seq, msg.Type, msg.Tool, contentStr, msg.Tokens)
	db.Exec("UPDATE sessions SET updated_at=? WHERE id=?", now, sessionID)
}

// dbAppendMessages 批量追加以便批量持久化（如工具调用块）
func dbAppendMessages(sessionID string, msgs []ChatMessage) {
	for _, m := range msgs {
		dbAppendMessage(sessionID, m)
	}
}

// dbSetMessageContent 更新某条消息的内容（用于 thinking/message 流式结束）。
// 按 session_id + seq 定位。
func dbSetMessageContent(sessionID string, seq int, content string) {
	dbMu.Lock()
	defer dbMu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec("UPDATE messages SET content=? WHERE session_id=? AND seq=?", content, sessionID, seq)
	db.Exec("UPDATE sessions SET updated_at=? WHERE id=?", now, sessionID)
}

// ========== Todo CRUD ==========

// dbReplaceTodos 全量替换会话的待办列表。
func dbReplaceTodos(sessionID string, todos []TodoItem) {
	dbMu.Lock()
	defer dbMu.Unlock()

	db.Exec("DELETE FROM todos WHERE session_id=?", sessionID)
	for i, t := range todos {
		db.Exec(`INSERT OR REPLACE INTO todos(id,session_id,title,status,sort_order) VALUES(?,?,?,?,?)`,
			t.ID, sessionID, t.Title, t.Status, i)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec("UPDATE sessions SET updated_at=? WHERE id=?", now, sessionID)
}

// dbClearTodos 清空会话的待办列表。
func dbClearTodos(sessionID string) {
	dbMu.Lock()
	defer dbMu.Unlock()
	db.Exec("DELETE FROM todos WHERE session_id=?", sessionID)
	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec("UPDATE sessions SET updated_at=? WHERE id=?", now, sessionID)
}

// ========== Share CRUD ==========

// dbCreateShare 创建分享记录。
func dbCreateShare(id, sessionID, blockRanges string) error {
	dbMu.Lock()
	defer dbMu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`INSERT INTO shares(id,session_id,block_ranges,created_at) VALUES(?,?,?,?)`,
		id, sessionID, blockRanges, now)
	return err
}

// dbGetShare 获取分享记录。
func dbGetShare(id string) (sessionID string, blockRanges string, err error) {
	dbMu.Lock()
	defer dbMu.Unlock()
	row := db.QueryRow(`SELECT session_id, block_ranges FROM shares WHERE id=?`, id)
	err = row.Scan(&sessionID, &blockRanges)
	return
}

// dbDeleteSharesBySession 删除会话关联的所有分享。
func dbDeleteSharesBySession(sessionID string) {
	dbMu.Lock()
	defer dbMu.Unlock()
	db.Exec("DELETE FROM shares WHERE session_id=?", sessionID)
}

// ========== FTS5 搜索 ==========

// SearchResult 搜索结果
type SearchResult struct {
	SessionID    string `json:"session_id"`
	Seq          int    `json:"seq"`
	Type         string `json:"type"`
	Snippet      string `json:"snippet"` // 高亮片段
	SessionTitle string `json:"session_title"`
}

// sanitizeFTS5Query 将用户输入转为安全的 FTS5 后缀匹配查询。
// FTS5 保留字符：* " ( ) + - : = < > [ ] { } ~ ! @ # $ % ^ & / \ | ? ` .
// 策略：移除非字母/数字/中文/空格的字符，拆词取前 5 个，每个词加 * 做前缀匹配。
func sanitizeFTS5Query(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r) {
			b.WriteRune(r)
		} else if r == ' ' || r == '\t' {
			b.WriteRune(' ')
		}
	}
	clean := strings.TrimSpace(b.String())
	if clean == "" {
		return ""
	}
	// 拆词，每个加 * 前缀匹配，最多 5 个词
	words := strings.Fields(clean)
	if len(words) > 5 {
		words = words[:5]
	}
	for i, w := range words {
		words[i] = w + "*"
	}
	return strings.Join(words, " ")
}

// dbSearchMessages 全文搜索消息内容（仅 user/message/assistant 类型）。
func dbSearchMessages(query string, limit int) []SearchResult {
	if limit <= 0 {
		limit = 20
	}

	dbMu.Lock()
	defer dbMu.Unlock()

	ftsQ := sanitizeFTS5Query(query)
	if ftsQ == "" {
		return nil
	}

	rows, err := db.Query(`
		SELECT m.session_id, m.seq, m.type,
			snippet(messages_fts, 1, '<mark>', '</mark>', '...', 60) AS snippet,
			COALESCE(s.title, '') AS session_title
		FROM messages_fts
		JOIN messages m ON messages_fts.rowid = m.id
		LEFT JOIN sessions s ON m.session_id = s.id
		WHERE messages_fts MATCH ?
		ORDER BY rank
		LIMIT ?`, ftsQ, limit)
	if err != nil {
		log.Printf("[DB] FTS 搜索失败: %v", err)
		return nil
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		rows.Scan(&r.SessionID, &r.Seq, &r.Type, &r.Snippet, &r.SessionTitle)
		results = append(results, r)
	}
	return results
}

// ========== 全库搜索（会话级别） ==========

// SessionSearchResult 会话级搜索结果
type SessionSearchResult struct {
	SessionID    string `json:"session_id"`
	Title        string `json:"title"`
	Agent        string `json:"agent"`
	Project      string `json:"project"`
	UpdatedAt    string `json:"updated_at"`
	MatchSnippet string `json:"match_snippet"` // 高亮匹配片段
	MatchField   string `json:"match_field"`   // title / agent / project / message
	MessageCount int    `json:"message_count"`
}

// dbSearchSessions 全库搜索会话：标题、Agent、项目用 LIKE，消息内容优先 FTS5，失败降级 LIKE。
func dbSearchSessions(query string, limit int) []SessionSearchResult {
	if limit <= 0 {
		limit = 20
	}

	dbMu.Lock()
	defer dbMu.Unlock()

	likeQ := "%" + query + "%"

	// 先查标题/Agent/项目 LIKE，始终安全
	sqlMeta := `
		SELECT session_id, title, agent, COALESCE(project,'') AS project,
		       updated_at, match_snippet, match_field, message_count, priority
		FROM (
			SELECT s.id AS session_id, s.title, s.agent, COALESCE(s.project,'') AS project,
			       s.updated_at,
			       ('…' || substr(s.title, 1, 200) || '…') AS match_snippet,
			       'title' AS match_field,
			       (SELECT COUNT(*) FROM messages WHERE session_id = s.id) AS message_count,
			       1 AS priority
			FROM sessions s
			WHERE s.title LIKE ? COLLATE NOCASE

			UNION ALL

			SELECT s.id, s.title, s.agent, COALESCE(s.project,''),
			       s.updated_at,
			       ('Agent: ' || s.agent) AS match_snippet,
			       'agent' AS match_field,
			       (SELECT COUNT(*) FROM messages WHERE session_id = s.id) AS message_count,
			       2 AS priority
			FROM sessions s
			WHERE s.agent LIKE ? COLLATE NOCASE

			UNION ALL

			SELECT s.id, s.title, s.agent, COALESCE(s.project,''),
			       s.updated_at,
			       ('Project: ' || s.project) AS match_snippet,
			       'project' AS match_field,
			       (SELECT COUNT(*) FROM messages WHERE session_id = s.id) AS message_count,
			       3 AS priority
			FROM sessions s
			WHERE s.project LIKE ? COLLATE NOCASE
		)
		GROUP BY session_id
		ORDER BY MIN(priority), updated_at DESC
		LIMIT ?`

	rows, err := db.Query(sqlMeta, likeQ, likeQ, likeQ, limit)
	if err != nil {
		log.Printf("[DB] 全库搜索(meta)失败: %v", err)
		return nil
	}

	var results []SessionSearchResult
	seen := map[string]bool{}
	for rows.Next() {
		var r SessionSearchResult
		var prio int
		rows.Scan(&r.SessionID, &r.Title, &r.Agent, &r.Project, &r.UpdatedAt, &r.MatchSnippet, &r.MatchField, &r.MessageCount, &prio)
		results = append(results, r)
		seen[r.SessionID] = true
	}
	rows.Close()

	// 如果元数据匹配已经够 limit，直接返回
	if len(results) >= limit {
		return results
	}

	// 消息内容匹配：优先 FTS5，失败则降级到 LIKE
	var msgRows *sql.Rows
	ftsQ := sanitizeFTS5Query(query)
	if ftsQ != "" {
		msgRows, err = db.Query(`
			SELECT DISTINCT s.id, s.title, s.agent, COALESCE(s.project,''),
			       s.updated_at,
			       snippet(messages_fts, 2, '<mark>', '</mark>', '...', 60) AS match_snippet,
			       'message' AS match_field,
			       (SELECT COUNT(*) FROM messages WHERE session_id = s.id) AS message_count
			FROM messages_fts
			JOIN messages m ON messages_fts.rowid = m.id
			JOIN sessions s ON m.session_id = s.id
			WHERE messages_fts MATCH ?
			ORDER BY s.updated_at DESC
			LIMIT ?`, ftsQ, limit)
	}
	// FTS5 失败或 sanitize 后为空 → 降级到 LIKE
	if ftsQ == "" || err != nil {
		if err != nil {
			log.Printf("[DB] FTS5 搜索失败，降级 LIKE: %v", err)
		}
		msgRows, err = db.Query(`
			SELECT DISTINCT s.id, s.title, s.agent, COALESCE(s.project,''),
			       s.updated_at,
			       ('…' || substr(m.content, 1, 200) || '…') AS match_snippet,
			       'message' AS match_field,
			       (SELECT COUNT(*) FROM messages WHERE session_id = s.id) AS message_count
			FROM messages m
			JOIN sessions s ON m.session_id = s.id
			WHERE m.content LIKE ? AND m.type IN ('user','message','assistant')
			ORDER BY s.updated_at DESC
			LIMIT ?`, likeQ, limit)
		if err != nil {
			log.Printf("[DB] LIKE 降级也失败: %v", err)
			return results
		}
	}
	defer msgRows.Close()

	for msgRows.Next() {
		var r SessionSearchResult
		msgRows.Scan(&r.SessionID, &r.Title, &r.Agent, &r.Project, &r.UpdatedAt, &r.MatchSnippet, &r.MatchField, &r.MessageCount)
		if seen[r.SessionID] {
			continue
		}
		results = append(results, r)
		seen[r.SessionID] = true
		if len(results) >= limit {
			break
		}
	}
	if results == nil {
		results = []SessionSearchResult{}
	}
	return results
}

// ========== 辅助函数 ==========

// parseContentJSON 尝试将 JSON 字符串解析为 map，失败则返回原字符串。
func parseContentJSON(s string) interface{} {
	if s == "" {
		return ""
	}
	if s[0] == '{' || s[0] == '[' {
		var obj map[string]interface{}
		if json.Unmarshal([]byte(s), &obj) == nil {
			return obj
		}
		var arr []interface{}
		if json.Unmarshal([]byte(s), &arr) == nil {
			return arr
		}
	}
	return s
}

// getMessageSeq 获取会话当前最大消息序号，用于流式持久化时的起始位置。
func getMessageSeq(sessionID string) int {
	var maxSeq int
	db.QueryRow("SELECT COALESCE(MAX(seq),-1) FROM messages WHERE session_id=?", sessionID).Scan(&maxSeq)
	return maxSeq + 1
}

// dbSessionExists 检查会话是否存在。
func dbSessionExists(id string) bool {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM sessions WHERE id=?", id).Scan(&count)
	return count > 0
}

// dbSessionStats 返回会话统计信息（消息数、todo 数）。
func dbSessionStats(id string) (msgCount, todoCount int) {
	db.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id=?", id).Scan(&msgCount)
	db.QueryRow("SELECT COUNT(*) FROM todos WHERE session_id=?", id).Scan(&todoCount)
	return
}
