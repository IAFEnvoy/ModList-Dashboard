package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// estimateTokens 估算 token 数（回退算法，不调用外部库避免 WARN）
func estimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	chineseChars := 0
	englishChars := 0
	for _, r := range text {
		if r >= 0x4e00 && r <= 0x9fff || r >= 0x3400 && r <= 0x4dbf {
			chineseChars++
		} else {
			englishChars++
		}
	}
	// 中文 ~1.5 token/char, 英文 ~0.3 token/char
	return int(float64(chineseChars)*1.5+float64(englishChars)*0.3) + 1
}

// ========== 上下文用量统计 ==========

// ContextUsage 上下文用量快照
type ContextUsage struct {
	SystemPromptTokens int  `json:"system_prompt_tokens"`
	MessagesTokens     int  `json:"messages_tokens"`
	TotalTokens        int  `json:"total_tokens"`
	MaxTokens          int  `json:"max_tokens"`
	UsagePercent       int  `json:"usage_percent"`
	AutoCompressed     bool `json:"auto_compressed,omitempty"` // 已自动压缩过
}

// GetContextUsage 计算会话的上下文用量（优先使用消息中存储的实际 token 数）
func GetContextUsage(session *ChatSession) ContextUsage {
	def := FindAgentDef(session.Agent)
	info := FindModel(session.Provider)

	maxTokens := 128000
	if info != nil && info.MaxContextLength > 0 {
		maxTokens = info.MaxContextLength
	}

	usage := ContextUsage{MaxTokens: maxTokens}

	// 系统提示词
	sysPrompt := ""
	if def != nil {
		sysPrompt = def.AgentMD
	}
	usage.SystemPromptTokens = estimateTokens(sysPrompt)

	// 消息：优先用存储的 token 数，否则用 estimateTokens
	for _, msg := range session.Messages {
		if msg.Type == "system" {
			continue
		}
		if msg.Tokens > 0 {
			usage.MessagesTokens += msg.Tokens
		} else {
			usage.MessagesTokens += estimateTokens(msgContent(msg))
		}
	}

	usage.TotalTokens = usage.SystemPromptTokens + usage.MessagesTokens
	if maxTokens > 0 {
		usage.UsagePercent = usage.TotalTokens * 100 / maxTokens
	}
	return usage
}

// buildContextBarLabel 构建类似 Copilot 的上下文用量标签
func buildContextBarLabel(usage ContextUsage) string {
	sysBar := drawBar(usage.SystemPromptTokens, usage.MaxTokens, "Sys")
	msgBar := drawBar(usage.MessagesTokens, usage.MaxTokens, "Msg")

	return fmt.Sprintf("Context [%s][%s] %d/%d (%d%%)",
		sysBar, msgBar, usage.TotalTokens, usage.MaxTokens, usage.UsagePercent)
}

func drawBar(tokens, max int, label string) string {
	if max <= 0 {
		return label + ":???"
	}
	pct := tokens * 100 / max
	blocks := pct / 10
	if blocks > 10 {
		blocks = 10
	}
	bar := strings.Repeat("#", blocks) + strings.Repeat("-", 10-blocks)
	return fmt.Sprintf("%s:%s", label, bar)
}

// ========== 自动压缩 ==========

const (
	autoCompactThreshold = 0.40 // 超过 40% 自动压缩（Tool Calling 时消耗翻倍，需提前压缩）
	compactKeepRounds    = 2    // 保留最近 N 轮对话
)

// ShouldAutoCompact 判断是否需要自动压缩
func ShouldAutoCompact(session *ChatSession) bool {
	usage := GetContextUsage(session)
	return usage.TotalTokens > 0 &&
		usage.MaxTokens > 0 &&
		float64(usage.TotalTokens)/float64(usage.MaxTokens) > autoCompactThreshold
}

// CompactSession 压缩会话：在消息列表末尾追加 summary 块，不删除任何历史消息。
// 摘要覆盖从上一个 summary（或 system）之后到最近 compactKeepRounds 轮之前的所有轮次。
func CompactSession(session *ChatSession) *CompactResult {
	usage := GetContextUsage(session)
	if usage.TotalTokens <= 0 {
		return &CompactResult{Compacted: false, Usage: usage}
	}

	// Step 1: 从后往前找到最近的 summary 块，确定"活跃区间"的起点
	summaryIdx := -1
	for i := len(session.Messages) - 1; i >= 0; i-- {
		if session.Messages[i].Type == "summary" {
			summaryIdx = i
			break
		}
	}
	startScan := summaryIdx + 1
	if summaryIdx < 0 {
		startScan = 1 // 跳过 system 消息
	}

	// Step 2: 将活跃区间的消息按 user 分组为"轮次"
	type round struct {
		messages []ChatMessage
		startIdx int
	}
	var rounds []round
	var cur *round
	for i := startScan; i < len(session.Messages); i++ {
		msg := &session.Messages[i]
		if msg.Type == "user" {
			if cur != nil {
				rounds = append(rounds, *cur)
			}
			cur = &round{startIdx: i}
		}
		if cur != nil {
			cur.messages = append(cur.messages, *msg)
		}
	}
	if cur != nil && len(cur.messages) > 0 {
		rounds = append(rounds, *cur)
	}

	if len(rounds) <= compactKeepRounds {
		return &CompactResult{Compacted: false, Usage: usage}
	}

	// Step 3: 构建摘要（含工具调用及其关键结果）
	var summaryLines []string
	oldRounds := rounds[:len(rounds)-compactKeepRounds]

	for _, rd := range oldRounds {
		var userText string
		var toolSummary []string
		var asstText string

		for _, msg := range rd.messages {
			switch msg.Type {
			case "user":
				userText = truncateMsg(msgContent(msg), 150)
			case "tool", "tool_call":
				toolData, _ := msg.Content.(map[string]interface{})
				toolName := ""
				if toolData != nil {
					if n, ok := toolData["name"].(string); ok {
						toolName = n
					}
				}
				toolSummary = append(toolSummary, toolName)
			case "tool_result":
				var rt string
				if s, ok := msg.Content.(string); ok {
					rt = s
				} else {
					b, _ := json.Marshal(msg.Content)
					rt = string(b)
				}
				if len(rt) > 200 {
					rt = rt[:200] + "..."
				}
				if n := len(toolSummary); n > 0 {
					toolSummary[n-1] += " → " + truncate(rt, 100)
				}
			case "assistant", "message":
				asstText = truncateMsg(msgContent(msg), 150)
			}
		}

		line := "Q: " + userText
		if len(toolSummary) > 0 {
			line += " [🔧 " + strings.Join(toolSummary, ", ") + "]"
		}
		if asstText != "" {
			line += " → " + asstText
		}
		summaryLines = append(summaryLines, line)
	}

	if len(summaryLines) > 10 {
		summaryLines = summaryLines[len(summaryLines)-10:]
	}

	summaryText := fmt.Sprintf("## 对话历史摘要（已压缩 %d 轮）\n%s",
		len(oldRounds),
		strings.Join(summaryLines, "\n"))

	// Step 4: 在 message 列表末尾追加 summary 块（不删除历史消息）
	session.Messages = append(session.Messages, ChatMessage{Type: "summary", Content: summaryText})
	newUsage := GetContextUsage(session)
	newUsage.AutoCompressed = true

	// 持久化 summary 块到 DB
	dbAppendMessage(session.ID, ChatMessage{Type: "summary", Content: summaryText})

	log.Printf("[Compact] 会话 %s: 活跃 %d 轮 → 保留 %d 轮，追加 summary (%d 行, %d 字符) (%d%%)",
		session.ID, len(rounds), compactKeepRounds,
		len(summaryLines), len(summaryText), newUsage.UsagePercent)

	return &CompactResult{
		Compacted:      true,
		Usage:          newUsage,
		OldUsage:       usage,
		MessagesBefore: len(session.Messages),
		MessagesAfter:  len(session.Messages),
	}
}

type CompactResult struct {
	Compacted      bool         `json:"compacted"`
	Usage          ContextUsage `json:"usage"`
	OldUsage       ContextUsage `json:"old_usage,omitempty"`
	MessagesBefore int          `json:"messages_before,omitempty"`
	MessagesAfter  int          `json:"messages_after,omitempty"`
}

func truncateMsg(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
