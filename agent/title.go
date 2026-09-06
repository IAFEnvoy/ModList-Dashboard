package agent

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/tmc/langchaingo/llms"
)

var titlePromptTemplate = ""

func init() {
	data, err := os.ReadFile("prompts/internal/title.md")
	if err != nil {
		log.Printf("[Title] 无法加载标题生成提示词: %v", err)
		return
	}
	titlePromptTemplate = string(data)
}

// generateTitleAsync 异步生成会话标题，使用当前对话的模型。
func generateTitleAsync(sessionID, userMessage string) {
	if titlePromptTemplate == "" {
		return
	}

	fallback := strings.TrimSpace(userMessage)
	if idx := strings.IndexAny(fallback, "\n\r"); idx > 0 {
		fallback = fallback[:idx]
	}
	if len([]rune(fallback)) > 50 {
		runes := []rune(fallback)
		fallback = string(runes[:47]) + "..."
	}

	go func() {
		session := GetSession(sessionID)
		if session == nil {
			return
		}
		if session.Title != "" {
			return
		}

		info := FindModel(session.Provider)
		if info == nil {
			sessionMu.Lock()
			session.Title = fallback
			sessionMu.Unlock()
			dbUpdateSessionMeta(sessionID, &fallback, nil, nil)
			broadcastTitle(sessionID, fallback)
			return
		}

		rt, err := getOrCreateLLM(*info, nil)
		if err != nil {
			log.Printf("[Title] 创建 LLM 失败: %v", err)
			sessionMu.Lock()
			session.Title = fallback
			sessionMu.Unlock()
			dbUpdateSessionMeta(sessionID, &fallback, nil, nil)
			broadcastTitle(sessionID, fallback)
			return
		}

		prompt := strings.Replace(titlePromptTemplate, "${user_message}", userMessage, 1)
		msgs := []llms.MessageContent{
			llms.TextParts(llms.ChatMessageTypeHuman, prompt),
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		resp, err := rt.llm.GenerateContent(ctx, msgs)
		if err != nil {
			log.Printf("[Title] LLM 调用失败: %v，使用 fallback", err)
			sessionMu.Lock()
			session.Title = fallback
			sessionMu.Unlock()
			dbUpdateSessionMeta(sessionID, &fallback, nil, nil)
			broadcastTitle(sessionID, fallback)
			return
		}

		if len(resp.Choices) == 0 || resp.Choices[0].Content == "" {
			sessionMu.Lock()
			session.Title = fallback
			sessionMu.Unlock()
			dbUpdateSessionMeta(sessionID, &fallback, nil, nil)
			broadcastTitle(sessionID, fallback)
			return
		}

		title := strings.TrimSpace(resp.Choices[0].Content)
		title = strings.Trim(title, "\"'`")
		title = strings.ReplaceAll(title, "<think>", "")
		title = strings.ReplaceAll(title, "</think>", "")
		title = strings.TrimSpace(title)

		if title == "" {
			title = fallback
		}
		if len([]rune(title)) > 50 {
			runes := []rune(title)
			title = string(runes[:47]) + "..."
		}

		sessionMu.Lock()
		session.Title = title
		sessionMu.Unlock()
		dbUpdateSessionMeta(sessionID, &title, nil, nil)
		broadcastTitle(sessionID, title)
		log.Printf("[Title] 会话 %s 标题已生成: %s", sessionID, title)
	}()
}

func broadcastTitle(sessionID, title string) {
	pub := getOrCreateBroadcaster(sessionID)
	if pub != nil {
		pub.TitleGenerated(sessionID, title)
	}
}

func getTitlePromptTemplate() string { return titlePromptTemplate }

var generatedTitles = make(map[string]bool)

func isTitleGenerated(sessionID string) bool { return generatedTitles[sessionID] }
func markTitleGenerated(sessionID string)    { generatedTitles[sessionID] = true }
func clearTitleGenerated(sessionID string)   { delete(generatedTitles, sessionID) }
