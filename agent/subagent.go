package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/tmc/langchaingo/llms"
)

// errMaxRoundsExceeded 达到最大轮次的哨兵错误
var errMaxRoundsExceeded = errors.New("max rounds exceeded")

// RunAgent 执行 Agent 对话（含子 Agent 调用循环）
// cwd: 项目工作目录
// parentCtx: 父上下文，用于外部停止（nil 则使用 Background）
func RunAgent(rt *agentRuntime, session *ChatSession, userMsg string, cwd string, onChunk func(chunk string), parentCtx context.Context) (string, error) {
	def := FindAgentDef(session.Agent)
	if def == nil {
		return "", fmt.Errorf("Agent %s 不存在", session.Agent)
	}

	temp := def.Temperature
	if temp <= 0 {
		temp = 0.7
	}

	// 系统提示词（含子 Agent 调用说明 + 环境信息）
	sysPrompt := buildAgentSystemPrompt(def)
	if session.Agent == IssuePreprocessorID {
		sysPrompt = expandPromptForSession(sysPrompt, session)
	}
	sysPrompt += "\n\n## 环境信息\n你运行在 Windows 系统上。shell_exec 使用 `cmd /c` 执行命令，请使用 Windows 命令（如 `type` 而非 `cat`，`dir` 而非 `ls`）。"
	sysPrompt += "\n\n## 🔧 工具使用优先级（必须遵守）\n"
	sysPrompt += "1. **内置只读工具优先**：read_file / read_files / list_files / file_search / grep_search 能满足需求时，不要用其他工具。\n"
	sysPrompt += "2. **外部 MCP**：内置工具无法满足时（如需要 GitHub API、数据库查询），优先使用外部 MCP 工具（名称含 `__`）。\n"
	sysPrompt += "3. **Web Fetch**：仅当前两步都无法获取所需信息时，才使用 fetch_webpage 或 download_file 抓取网页。**禁止用 fetch_webpage 去搜 GitHub Issue！** Issue 内容已在附件中。\n"
	sysPrompt += "4. **Shell 最后手段**：仅在前三步都无法完成时，才使用 shell_exec。**禁止用 shell_exec 代替 grep_search、file_search 等内置搜索工具！** `findstr` / `grep` 类操作一律用 grep_search。"
	if cwd != "" {
		sysPrompt += fmt.Sprintf("\n工作目录: `%s`\n所有文件相对路径基于此目录。", cwd)
	}

	// 每次启动 Agent 前压缩历史（< compactKeepRounds 轮时是 no-op），
	// 防止「达到最大轮次 → 用户说请继续 → 重发全部历史导致上下文爆掉」
	CompactSession(session)

	// 构建消息列表
	lcMsgs := messagesToLangChain([]ChatMessage{
		{Type: "system", Content: sysPrompt},
	})

	// 添加活跃上下文消息（从最后一个 summary 之前 compactKeepRounds 轮用户消息开始）
	// 这样 LLM 收到：[system] + [summary 前 2 轮(防止断链)] + [summary(作为 system)] + [summary 后的活跃消息]
	activeStart := 1 // 默认跳过 system
	lastSummaryIdx := -1
	for i := len(session.Messages) - 1; i >= 1; i-- {
		if session.Messages[i].Type == "summary" {
			lastSummaryIdx = i
			break
		}
	}
	if lastSummaryIdx >= 0 {
		// 从 summary 位置往前数 compactKeepRounds 个 user 消息，确保上下文有搭接
		userCount := 0
		activeStart = lastSummaryIdx
		for i := lastSummaryIdx - 1; i >= 1; i-- {
			if session.Messages[i].Type == "user" {
				userCount++
				if userCount >= compactKeepRounds {
					activeStart = i
					break
				}
			}
		}
		if userCount < compactKeepRounds {
			activeStart = 1 // 没找到足够的 user 消息，从头开始
		}
	}
	for _, m := range session.Messages[activeStart:] {
		if m.Type == "system" {
			continue
		}
		switch m.Type {
		case "summary":
			// 摘要注入为 system 上下文
			lcMsgs = append(lcMsgs, llms.TextParts(llms.ChatMessageTypeSystem, msgContent(m)))
		case "tool", "tool_result":
			continue
		case "attachment":
			// 合并到上一条 Human 消息末尾
			if len(lcMsgs) > 0 {
				last := &lcMsgs[len(lcMsgs)-1]
				if last.Role == llms.ChatMessageTypeHuman {
					last.Parts = append(last.Parts, llms.TextPart("\n\n---\n\n"+msgContent(m)))
					continue
				}
			}
			lcMsgs = append(lcMsgs, llms.TextParts(llms.ChatMessageTypeHuman, msgContent(m)))
		case "assistant", "message":
			lcMsgs = append(lcMsgs, llms.TextParts(llms.ChatMessageTypeAI, msgContent(m)))
		default:
			lcMsgs = append(lcMsgs, llms.TextParts(llms.ChatMessageTypeHuman, msgContent(m)))
		}
	}

	// 添加用户消息（消息可能已通过 REST/SSE handler 预添加到 session.Messages）
	if userMsg != "" {
		lcMsgs = append(lcMsgs, llms.TextParts(llms.ChatMessageTypeHuman, userMsg))
	}

	maxRounds := def.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 50
	}
	bg := context.Background()
	if parentCtx != nil {
		bg = parentCtx
	}
	bg = withExecSession(bg, session.ID)
	lctx, cancel := context.WithTimeout(bg, 600*time.Second)
	defer cancel()
	reply, blocks, compTokens, promptTokens, err := runAgentLoop(lctx, rt, lcMsgs, temp, cwd, session.Provider, maxRounds, onChunk)
	totalTokens := compTokens + promptTokens
	log.Printf("[Agent] runAgentLoop end: reply=%d bytes, blocks=%d, tokens=%d (comp=%d prompt=%d), err=%v", len(reply), len(blocks), totalTokens, compTokens, promptTokens, err)
	// 将 completion tokens 存到最后一个 AI message 块上
	if compTokens > 0 {
		sessionMu.Lock()
		for j := len(session.Messages) - 1; j >= 0; j-- {
			if session.Messages[j].Type == "message" && session.Messages[j].Tokens == 0 {
				session.Messages[j].Tokens = compTokens
				break
			}
		}
		sessionMu.Unlock()
	}
	if err != nil {
		return "", err
	}
	return reply, nil
}

// buildAgentSystemPrompt 构建 Agent 系统提示词（含子 Agent 调用说明）
func buildAgentSystemPrompt(def *AgentDef) string {
	var sb strings.Builder
	sb.WriteString(def.AgentMD)

	subs := def.GetSubAgents()
	if len(subs) > 0 {
		sb.WriteString("\n\n## 子 Agent 调用\n")
		sb.WriteString("当任务过于复杂或需要特定领域知识时，可以调用子 Agent：\n\n")
		sb.WriteString("CALL: 子Agent名 任务描述\n\n")
		sb.WriteString("可用的子 Agent:\n")
		for _, sub := range subs {
			sb.WriteString(fmt.Sprintf("- %s (%s): %s\n", sub.Name, sub.ID, sub.Description))
		}
		sb.WriteString("\n子 Agent 会独立完成任务并返回结果，然后你继续处理。\n")
	}
	return sb.String()
}

// ========== 对话循环 ==========

// runAgentLoop 执行对话循环（原生 Tool Calling + 子 Agent 调用 + 最终回答）
// maxRounds: 最大调用轮次
// 返回: (最终回复, 块列表, completion_tokens, prompt_tokens, error)
func runAgentLoop(ctx context.Context, rt *agentRuntime, messages []llms.MessageContent, temp float64, cwd string, providerKey string, maxRounds int, onChunk func(chunk string)) (string, []ChatMessage, int, int, error) {
	var lastText string
	var blocks []ChatMessage
	var readCount = make(map[string]int)
	var completionTokens int // 累计 completion tokens
	var promptTokens int     // 累计 prompt tokens（最后一轮）

	for round := 0; round < maxRounds; round++ {
		select {
		case <-ctx.Done():
			if lastText != "" {
				blocks = append(blocks, ChatMessage{Type: "message", Content: lastText, Tokens: completionTokens})
			}
			if len(blocks) > 0 && !isIssuePreprocessSession(ctx) {
				return lastText, blocks, completionTokens, promptTokens, nil
			}
			return "", blocks, completionTokens, promptTokens, fmt.Errorf("请求超时或已取消: %w", ctx.Err())
		default:
		}

		log.Printf("[LLM] 第%d轮调用 %s (消息数=%d)...", round+1, rt.info.Model, len(messages))

		opts := rt.chatOptionsStream(temp, onChunk)
		resp, err := rt.llm.GenerateContent(ctx, messages, opts...)
		if err != nil {
			log.Printf("[LLM] 第%d轮调用失败: %v", round+1, err)
			if lastText != "" {
				blocks = append(blocks, ChatMessage{Type: "message", Content: lastText, Tokens: completionTokens})
			}
			if len(blocks) > 0 && !isIssuePreprocessSession(ctx) {
				return lastText, blocks, completionTokens, promptTokens, nil
			}
			return "", blocks, completionTokens, promptTokens, fmt.Errorf("LLM 调用失败 (第%d轮): %w", round+1, err)
		}

		// 提取本轮 token 消耗
		completionTokens += extractTokensFromResponse(resp)
		promptTokens = extractPromptTokens(resp) // 最后一轮即为全量 prompt tokens

		if len(resp.Choices) == 0 {
			if isIssuePreprocessSession(ctx) {
				return "", blocks, completionTokens, promptTokens, fmt.Errorf("模型未返回预处理报告")
			}
			if lastText != "" {
				return lastText, blocks, completionTokens, promptTokens, nil
			}
			return "(模型返回了空内容)", blocks, completionTokens, promptTokens, nil
		}

		text := resp.Choices[0].Content
		log.Printf("[LLM] 第%d轮完成 (choices=%d, toolCalls=%d)", round+1, len(resp.Choices), len(resp.Choices[0].ToolCalls))

		// 1. 检查原生 ToolCalls（由 WithTools 触发，已从流式输出中抑制）
		if len(resp.Choices[0].ToolCalls) > 0 {
			messages = append(messages, aiMsgWithToolCalls(text, resp.Choices[0].ToolCalls))

			// DeepSeek 思考模式：保存 reasoning_content 供下一轮回传
			if rt.tr != nil && resp.Choices[0].ReasoningContent != "" {
				rt.tr.stashReasoning(resp.Choices[0].ReasoningContent)
			}

			// 工具调用前的文本作为一个 message 块保存
			trimText := strings.TrimSpace(text)
			if trimText != "" {
				blocks = append(blocks, ChatMessage{Type: "message", Content: trimText})
			}

			for _, tc := range resp.Choices[0].ToolCalls {
				name := tc.FunctionCall.Name
				args := tc.FunctionCall.Arguments

				// 先发出 tool_call 块（让前端即时显示）
				label := toolStepLabel(name, args)
				toolData := map[string]interface{}{
					"name":      name,
					"label":     label,
					"arguments": json.RawMessage(args),
				}
				toolJSON, _ := json.Marshal(toolData)
				if onChunk != nil {
					onChunk("__TOOL__" + string(toolJSON))
				}
				blocks = append(blocks, ChatMessage{Type: "tool", Content: toolData, Tool: name})

				var result string
				// 危险操作审批（阻塞等待）
				if dangerousTools[name] && issuePreprocessToolAllowed(ctx, name) {
					if !RequestApproval(ctx, name, args) {
						log.Printf("[Tool] 用户拒绝了 %s", name)
						toolData["rejected"] = true
						blocks[len(blocks)-1].Content = toolData
						result = fmt.Sprintf("用户拒绝了 %s 操作", name)
						resultData := map[string]interface{}{
							"name":   name,
							"result": result,
						}
						resultJSON, _ := json.Marshal(resultData)
						if onChunk != nil {
							onChunk("__TOOL_RESULT__" + string(resultJSON))
						}
						blocks = append(blocks, ChatMessage{Type: "tool_result", Content: resultData, Tool: name})
						messages = append(messages, toolResultMsg(tc.ID, name, result))
						continue
					}
				}

				log.Printf("[Tool] 第%d轮: %s args=%s", round+1, name, truncate(args, 60))
				var toolErr error
				result, toolErr = ExecuteTool(ctx, name, args, cwd)
				if toolErr != nil {
					result = "error: " + toolErr.Error()
				}
				log.Printf("[Tool] %s -> %s", name, truncate(fmt.Sprintf("%v", result), 80))

				// TODO 更新标记：直接推送给前端
				if strings.HasPrefix(result, "__TODO_UPDATE__") {
					if onChunk != nil {
						onChunk(result)
					}
					result = strings.TrimPrefix(result, "__TODO_UPDATE__")
				}

				if name == "read_file" {
					path := toolArgPath(args)
					readCount[path]++
					if path != "" && !strings.HasPrefix(result, "error:") {
						log.Printf("[Tool] 文件 %s 已读取 (%d 次)", path, readCount[path])
					}
				}

				// 写操作后重置该文件的 readCount，确保 agent 可以重新读取验证
				if isFileWriteTool(name) {
					path := toolArgPath(args)
					if path != "" {
						readCount[path] = 0
						log.Printf("[Tool] 文件 %s 已被写入，重置读取计数", path)
					}
				}

				// 工具结果块：持久化 + 前端展示
				truncated := truncate(fmt.Sprintf("%v", result), 2000)
				resultData := map[string]interface{}{
					"name":   name,
					"result": truncated,
				}
				resultJSON, _ := json.Marshal(resultData)
				if onChunk != nil {
					onChunk("__TOOL_RESULT__" + string(resultJSON))
				}
				blocks = append(blocks, ChatMessage{Type: "tool_result", Content: resultData, Tool: name})

				messages = append(messages, toolResultMsg(tc.ID, name, fmt.Sprintf("%v", result)))
			}
			continue
		}

		// 2. 检查 CALL: 子 Agent（文本协议，非 native tool call）
		if text != "" {
			subName, subTask := parseSubAgentCall(text)
			if subName != "" {
				subDef := FindAgentDef(subName)
				if subDef == nil {
					messages = append(messages, llms.TextParts(llms.ChatMessageTypeAI, text))
					messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman,
						fmt.Sprintf("子 Agent %s 不存在。可用的子 Agent 已在系统提示中列出。", subName)))
					continue
				}

				log.Printf("[Sub] 第%d轮: %s -> %s", round+1, subName, truncate(subTask, 80))
				// 子 Agent 块
				subJSON, _ := json.Marshal(map[string]interface{}{"name": subName, "label": subDef.Name})
				if onChunk != nil {
					onChunk("__TOOL__" + string(subJSON))
				}
				blocks = append(blocks, ChatMessage{
					Type:    "tool",
					Content: map[string]interface{}{"name": subName, "label": subDef.Name},
					Tool:    subName,
				})

				subResult, subErr := spawnSubAgent(ctx, subDef, subTask, cwd, providerKey)
				if subErr != nil {
					subResult = "error: " + subErr.Error()
				}
				log.Printf("[Sub] %s 完成 (%d 字符)", subName, len(subResult))

				messages = append(messages, llms.TextParts(llms.ChatMessageTypeAI, text))
				messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman,
					fmt.Sprintf("子 Agent %s 返回: %s\n请基于此结果继续处理。", subName, subResult)))
				continue
			}
		}

		// 3. 无 ToolCall / CALL → 最终回答
		if text == "" || (isIssuePreprocessSession(ctx) && strings.TrimSpace(text) == "") {
			if isIssuePreprocessSession(ctx) {
				return "", blocks, completionTokens, promptTokens, fmt.Errorf("模型未返回预处理报告")
			}
			if lastText != "" {
				blocks = append(blocks, ChatMessage{Type: "message", Content: lastText})
				return lastText, blocks, completionTokens, promptTokens, nil
			}
			return "(模型返回了空内容)", blocks, completionTokens, promptTokens, nil
		}
		lastText = strings.TrimSpace(text)
		blocks = append(blocks, ChatMessage{Type: "message", Content: lastText})
		return lastText, blocks, completionTokens, promptTokens, nil
	}
	if lastText != "" {
		blocks = append(blocks, ChatMessage{Type: "message", Content: lastText})
		return lastText, blocks, completionTokens, promptTokens, nil
	}
	return "(达到最大轮次，AI 推理已暂停，可点击继续按钮恢复)", blocks, completionTokens, promptTokens, errMaxRoundsExceeded
}

// ========== 子 Agent ==========

// spawnSubAgent 调用子 Agent
func spawnSubAgent(ctx context.Context, def *AgentDef, task string, cwd string, providerKey string) (string, error) {
	if isIssuePreprocessSession(ctx) {
		return "", fmt.Errorf("Issue 预处理禁止调用子 Agent")
	}
	info := FindModel(providerKey)
	if info == nil {
		return "", fmt.Errorf("模型 %s 不存在", providerKey)
	}
	subTools := BuildTools(def)
	subRt, err := getOrCreateLLM(*info, subTools)
	if err != nil {
		return "", err
	}

	subSysPrompt := buildAgentSystemPrompt(def)
	subSysPrompt += "\n\n## 环境信息\nWindows 系统。shell_exec 使用 `cmd /c`，请使用 Windows 命令。"
	if cwd != "" {
		subSysPrompt += fmt.Sprintf("\n工作目录: `%s`", cwd)
	}

	lcMsgs := messagesToLangChain([]ChatMessage{
		{Type: "system", Content: subSysPrompt},
		{Type: "user", Content: task},
	})

	subTemp := def.Temperature
	if subTemp <= 0 {
		subTemp = 0.5
	}
	subMaxRounds := def.MaxRounds
	if subMaxRounds <= 0 {
		subMaxRounds = 50
	}

	reply, _, _, _, err := runAgentLoop(ctx, subRt, lcMsgs, subTemp, cwd, providerKey, subMaxRounds, nil)
	return reply, err
}

// ========== 解析辅助 ==========

// parseSubAgentCall 解析 CALL: 指令
func parseSubAgentCall(text string) (name string, task string) {
	trimmed := strings.TrimSpace(text)
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), "CALL:") {
			rest := strings.TrimSpace(line[5:])
			space := strings.IndexByte(rest, ' ')
			if space < 0 {
				return rest, ""
			}
			return rest[:space], strings.TrimSpace(rest[space+1:])
		}
	}
	return "", ""
}

// toolArgPath 从工具参数 JSON 中提取 path/file_path/file/command/cmd 字段
func toolArgPath(argsJSON string) string {
	var a struct {
		Path     string `json:"path"`
		FilePath string `json:"filePath"`
		File     string `json:"file"`
		Command  string `json:"command"`
		Cmd      string `json:"cmd"`
	}
	json.Unmarshal([]byte(argsJSON), &a)
	if a.Path != "" {
		return a.Path
	}
	if a.FilePath != "" {
		return a.FilePath
	}
	if a.File != "" {
		return a.File
	}
	if a.Command != "" {
		return a.Command
	}
	if a.Cmd != "" {
		return a.Cmd
	}
	return ""
}

// toolStepLabel 构建前端展示的步骤标签，含文件名（过长截断）
func toolStepLabel(name string, argsJSON string) string {
	label := name
	arg := toolArgPath(argsJSON)
	if arg != "" {
		short := arg
		if idx := strings.LastIndexAny(arg, "/\\"); idx >= 0 {
			short = arg[idx+1:]
		}
		if len(short) > 50 {
			short = short[:47] + "..."
		}
		label += ": " + short
	}
	return label
}

// isFileWriteTool 判断工具是否为写文件操作（写/创建/替换）
func isFileWriteTool(name string) bool {
	switch name {
	case "write_file", "create_file", "replace_string_in_file", "multi_replace_string_in_file":
		return true
	}
	return false
}

// extractTokensFromResponse 从 LLM 响应中提取 completion_tokens
func extractTokensFromResponse(resp *llms.ContentResponse) int {
	if resp == nil || len(resp.Choices) == 0 {
		return 0
	}
	info := resp.Choices[0].GenerationInfo
	if info == nil {
		return 0
	}
	if v, ok := info["CompletionTokens"].(int); ok && v > 0 {
		return v
	}
	// fallback
	if v, ok := info["TotalTokens"].(int); ok && v > 0 {
		return v
	}
	return 0
}

// extractPromptTokens 从 LLM 响应中提取 prompt_tokens
func extractPromptTokens(resp *llms.ContentResponse) int {
	if resp == nil || len(resp.Choices) == 0 {
		return 0
	}
	info := resp.Choices[0].GenerationInfo
	if info == nil {
		return 0
	}
	if v, ok := info["PromptTokens"].(int); ok && v > 0 {
		return v
	}
	return 0
}
