package agent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"
)

// ========== 运行时缓存 ==========
// agentInstances 使用 llm.go 中的 getOrCreateLLM 创建的 *agentRuntime

// ========== 会话管理 ==========

func CreateSession(agentID, providerID, task, project, projectPath string) (*ChatSession, error) {
	def := FindAgentDef(agentID)
	if def == nil {
		return nil, fmt.Errorf("Agent %s 不存在", agentID)
	}
	info := FindModel(providerID)
	if info == nil {
		return nil, fmt.Errorf("模型 %s 不存在", providerID)
	}

	id := make([]byte, 6)
	rand.Read(id)
	sessionID := hex.EncodeToString(id)

	systemMsg := ChatMessage{Type: "system", Content: expandPromptForDef(def.AgentMD, def)}
	now := time.Now().UTC().Format(time.RFC3339)

	session := ChatSession{
		ID:          sessionID,
		Agent:       agentID,
		InstanceID:  fmt.Sprintf("%s-%s", agentID, sessionID),
		Provider:    providerID,
		Project:     project,
		ProjectPath: projectPath,
		Task:        task,
		Messages:    []ChatMessage{systemMsg},
		CreatedAt:   now,
	}

	// 持久化到 SQLite
	if err := dbCreateSession(&session); err != nil {
		return nil, fmt.Errorf("保存会话失败: %v", err)
	}

	// 创建 LangChainGo 运行时并缓存
	tools := BuildTools(def)
	rt, llmErr := getOrCreateLLM(*info, tools)
	if llmErr != nil {
		return nil, fmt.Errorf("初始化 LLM 失败: %v", llmErr)
	}
	agentMu.Lock()
	agentInstances[sessionID] = rt
	agentMu.Unlock()

	sessionMu.Lock()
	AgentSessions = append(AgentSessions, &session)
	sessionMu.Unlock()

	for _, mcpID := range def.MCPs {
		ConnectMCP(mcpID)
	}

	log.Printf("[Agent] 新会话 %s: agent=%s model=%s (%s)", sessionID, agentID, providerID, info.Model)
	return &session, nil
}

func GetSession(id string) *ChatSession {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	for i := range AgentSessions {
		if AgentSessions[i].ID == id {
			return AgentSessions[i]
		}
	}
	return nil
}

func DeleteSession(id string) bool {
	sessionMu.Lock()
	var deleted bool
	for i := range AgentSessions {
		if AgentSessions[i].ID == id {
			def := FindAgentDef(AgentSessions[i].Agent)
			if def != nil {
				for _, mcpID := range def.MCPs {
					DisconnectMCP(mcpID)
				}
			}
			AgentSessions = append(AgentSessions[:i], AgentSessions[i+1:]...)
			agentMu.Lock()
			delete(agentInstances, id)
			agentMu.Unlock()
			deleted = true
			log.Printf("[Del] 删除会话 %s", id)
			break
		}
	}
	sessionMu.Unlock()
	if deleted {
		dbDeleteSession(id)
	}
	return deleted
}

func ListSessions() []ChatSession {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	result := make([]ChatSession, len(AgentSessions))
	for i, session := range AgentSessions {
		result[i] = *session
	}
	return result
}

// ========== 对话执行 ==========

func SendMessage(sessionID, userMessage string) (string, error) {
	session := GetSession(sessionID)
	if session == nil {
		return "", fmt.Errorf("会话 %s 不存在", sessionID)
	}

	agentMu.RLock()
	rt := agentInstances[sessionID]
	agentMu.RUnlock()
	if rt == nil {
		return "", fmt.Errorf("会话 %s 的 LLM 实例已失效", sessionID)
	}

	sessionMu.Lock()
	session.Messages = append(session.Messages, ChatMessage{Type: "user", Content: userMessage})
	sessionMu.Unlock()

	if ShouldAutoCompact(session) {
		CompactSession(session)
	}

	if strings.TrimSpace(userMessage) == "/compact" {
		result := CompactSession(session)
		resp := fmt.Sprintf("[Compact] %d -> %d (%d%% -> %d%%)",
			result.MessagesBefore, result.MessagesAfter,
			result.OldUsage.UsagePercent, result.Usage.UsagePercent)
		sessionMu.Lock()
		session.Messages = append(session.Messages, ChatMessage{Type: "message", Content: resp})
		sessionMu.Unlock()
		return resp, nil
	}

	// 使用 RunAgent（支持 TOOL + 子 Agent 调用）
	reply, err := RunAgent(rt, session, userMessage, session.ProjectPath, nil, nil)
	if err != nil {
		return "", err
	}

	sessionMu.Lock()
	session.Messages = append(session.Messages, ChatMessage{Type: "message", Content: reply})
	sessionMu.Unlock()
	go SaveSessions()

	log.Printf("[Agent] [%s/%s] user=%d reply=%d chars", session.Agent, session.Provider, len(userMessage), len(reply))
	return reply, nil
}
