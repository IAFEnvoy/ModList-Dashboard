package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"
)

// ========== LLM 运行时 ==========

type agentRuntime struct {
	llm   llms.Model
	tools []llms.Tool
	info  AIModelInfo
	tr    *thinkingRoundTripper // DeepSeek 思考模式的 HTTP 拦截器，用于回传 reasoning_content
}

var llmCache = make(map[string]*agentRuntime)
var llmCacheMu sync.RWMutex

// getOrCreateLLM 获取或创建 LLM 实例（缓存 key = modelKey + toolHash）
func getOrCreateLLM(info AIModelInfo, toolDefs []llms.Tool) (*agentRuntime, error) {
	cacheKey := info.Key + ":" + toolsHash(toolDefs)
	llmCacheMu.RLock()
	if rt, ok := llmCache[cacheKey]; ok {
		llmCacheMu.RUnlock()
		return rt, nil
	}
	llmCacheMu.RUnlock()

	opts := []openai.Option{
		openai.WithModel(info.Model),
		openai.WithBaseURL(info.BaseURL),
	}
	if info.APIKey != "" {
		opts = append(opts, openai.WithToken(info.APIKey))
	}

	// DeepSeek 思考模式：注入 thinking 字段 + 自定义 HTTP 客户端
	if info.ThinkingEnabled && info.Type == "deepseek" {
		effort := info.ReasonEffort()
		tr := &thinkingRoundTripper{
			next:   http.DefaultTransport,
			effort: effort,
		}
		opts = append(opts, openai.WithHTTPClient(&http.Client{Transport: tr}))
		llm, err := openai.New(opts...)
		if err != nil {
			return nil, fmt.Errorf("创建 LLM 失败: %w", err)
		}
		rt := &agentRuntime{
			llm:   llm,
			tools: toolDefs,
			info:  info,
			tr:    tr,
		}
		llmCacheMu.Lock()
		llmCache[cacheKey] = rt
		llmCacheMu.Unlock()
		log.Printf("[LLM] 初始化: %s (%s) [思考模式]", info.Name, info.Key)
		return rt, nil
	}

	llm, err := openai.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("创建 LLM 失败: %w", err)
	}

	rt := &agentRuntime{
		llm:   llm,
		tools: toolDefs,
		info:  info,
	}

	llmCacheMu.Lock()
	llmCache[cacheKey] = rt
	llmCacheMu.Unlock()

	log.Printf("[LLM] 初始化: %s (%s)", info.Name, info.Key)
	return rt, nil
}

func InvalidateLLMCache() {
	llmCacheMu.Lock()
	llmCache = make(map[string]*agentRuntime)
	llmCacheMu.Unlock()
	log.Printf("[LLM] 缓存已清除")
}

// ========== DeepSeek 思考模式 HTTP 拦截 ==========

// thinkingRoundTripper 拦截请求，为 DeepSeek 添加 thinking 字段和 reasoning_effort，
// 并在工具调用轮次间回传 reasoning_content（DeepSeek 要求）。
type thinkingRoundTripper struct {
	next          http.RoundTripper
	effort        string
	mu            sync.Mutex
	lastReasoning string // 上一轮响应中的 reasoning_content
}

// stashReasoning 保存本轮 AI 响应的 reasoning_content，供下一轮请求回传。
func (t *thinkingRoundTripper) stashReasoning(reasoning string) {
	t.mu.Lock()
	t.lastReasoning = reasoning
	t.mu.Unlock()
}

func (t *thinkingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// 只拦截 POST 请求
	if req.Method == "POST" && req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err == nil {
			var bodyMap map[string]interface{}
			if json.Unmarshal(bodyBytes, &bodyMap) == nil {
				bodyMap["thinking"] = map[string]interface{}{"type": "enabled"}
				if t.effort != "" {
					bodyMap["reasoning_effort"] = t.effort
				}
				// 注入上轮缓存的 reasoning_content 到最新的 assistant 消息中
				t.mu.Lock()
				stashed := t.lastReasoning
				t.lastReasoning = "" // 只使用一次
				t.mu.Unlock()
				if stashed != "" {
					injectReasoning(bodyMap, stashed)
				}
				newBody, _ := json.Marshal(bodyMap)
				req.Body = io.NopCloser(bytes.NewReader(newBody))
				req.ContentLength = int64(len(newBody))
				req.Header.Set("Content-Length", fmt.Sprintf("%d", len(newBody)))
			} else {
				req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}
		}
	}
	return t.next.RoundTrip(req)
}

// injectReasoning 将 reasoning_content 注入到请求体 messages 数组中最后一个 assistant 消息
func injectReasoning(bodyMap map[string]interface{}, reasoning string) {
	msgs, ok := bodyMap["messages"].([]interface{})
	if !ok {
		return
	}
	// 从后往前找第一个 role=assistant 的消息
	for i := len(msgs) - 1; i >= 0; i-- {
		msg, ok := msgs[i].(map[string]interface{})
		if !ok {
			continue
		}
		if role, _ := msg["role"].(string); role == "assistant" {
			msg["reasoning_content"] = reasoning
			return
		}
	}
}

// ========== 对话方法 ==========

// chatOptions 构建通用调用选项
func (rt *agentRuntime) chatOptions(temp float64) []llms.CallOption {
	var opts []llms.CallOption
	if temp > 0 {
		opts = append(opts, llms.WithTemperature(temp))
	}
	if rt.info.MaxTokens > 0 {
		opts = append(opts, llms.WithMaxTokens(rt.info.MaxTokens))
	}
	return opts
}

// chatOptionsStream 构建流式调用选项（含原生 Tool Calling）
// onChunk: 流式文本回调；tool call 的 JSON chunk（[ 开头）会被自动抑制，不推送到前端
func (rt *agentRuntime) chatOptionsStream(temp float64, onChunk func(chunk string)) []llms.CallOption {
	opts := rt.chatOptions(temp)
	if onChunk == nil {
		return opts
	}

	// DeepSeek 思考模式：使用 StreamingReasoningFunc 统一处理推理和正文流
	// 不能同时设 StreamingFunc + StreamingReasoningFunc，否则每个 chunk 双回调导致碎片化
	if rt.tr != nil && rt.info.ThinkingEnabled {
		opts = append(opts, llms.WithStreamingReasoningFunc(func(ctx context.Context, reasoningChunk, chunk []byte) error {
			// 推理内容：以 __REASONING__ 前缀转发
			if len(reasoningChunk) > 0 {
				onChunk("__REASONING__" + string(reasoningChunk))
			}
			// 正文：抑制 tool call JSON，其余推送
			if len(chunk) > 0 && chunk[0] != '[' {
				onChunk(string(chunk))
			}
			return nil
		}))
	} else {
		opts = append(opts, llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
			// 抑制 tool call 的 JSON delta chunk（以 '[' 开头）
			if len(chunk) > 0 && chunk[0] == '[' {
				return nil
			}
			onChunk(string(chunk))
			return nil
		}))
	}
	if len(rt.tools) > 0 {
		opts = append(opts, llms.WithTools(rt.tools))
	}
	return opts
}

// chatSync 同步对话
func (rt *agentRuntime) chatSync(ctx context.Context, messages []llms.MessageContent, temp float64) (string, *TokenUsage, error) {
	return rt.chatWithTools(ctx, messages, temp, nil)
}

// chatSyncWithCWD 同步对话（注入工作目录到系统提示）
func (rt *agentRuntime) chatSyncWithCWD(ctx context.Context, messages []llms.MessageContent, temp float64, cwd string) (string, *TokenUsage, error) {
	if cwd != "" && len(messages) > 0 && messages[0].Role == llms.ChatMessageTypeSystem {
		// 注入当前工作目录
		for i, p := range messages[0].Parts {
			if tc, ok := p.(llms.TextContent); ok {
				messages[0].Parts[i] = llms.TextContent{Text: tc.Text + fmt.Sprintf("\n\n所有文件路径的根目录是: %s", cwd)}
			}
		}
	}
	return rt.chatWithTools(ctx, messages, temp, nil)
}

// chatStream 流式对话
func (rt *agentRuntime) chatStream(ctx context.Context, messages []llms.MessageContent, temp float64, onChunk func(chunk string)) (string, *TokenUsage, error) {
	var fullContent strings.Builder
	opts := rt.chatOptions(temp)
	opts = append(opts, llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
		fullContent.Write(chunk)
		if onChunk != nil {
			onChunk(string(chunk))
		}
		return nil
	}))

	resp, err := rt.llm.GenerateContent(ctx, messages, opts...)
	if err != nil {
		return "", nil, fmt.Errorf("LLM 流式调用失败: %w", err)
	}

	// 确保完整内容（有些 provider 流式回调可能不完整）
	if fullContent.Len() == 0 && len(resp.Choices) > 0 {
		fullContent.WriteString(resp.Choices[0].Content)
	}

	result := fullContent.String()
	if result == "" {
		return "(模型返回了空内容)", nil, nil
	}
	return result, extractTokenUsage(resp), nil
}

// extractTokenUsage 从 ContentResponse.GenerationInfo 中提取 token 用量
func extractTokenUsage(resp *llms.ContentResponse) *TokenUsage {
	if resp == nil || len(resp.Choices) == 0 {
		return nil
	}
	gi := resp.Choices[0].GenerationInfo
	if gi == nil {
		return nil
	}
	u := &TokenUsage{}
	if v, ok := gi["prompt_tokens"]; ok {
		u.PromptTokens = toInt(v)
	}
	if v, ok := gi["completion_tokens"]; ok {
		u.CompletionTokens = toInt(v)
	}
	if v, ok := gi["total_tokens"]; ok {
		u.TotalTokens = toInt(v)
	}
	if v, ok := gi["input_tokens"]; ok {
		u.PromptTokens = toInt(v)
	}
	if v, ok := gi["output_tokens"]; ok {
		u.CompletionTokens = toInt(v)
	}
	if u.TotalTokens == 0 && (u.PromptTokens > 0 || u.CompletionTokens > 0) {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	if u.TotalTokens == 0 {
		return nil
	}
	return u
}

func toInt(v any) int {
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case int64:
		return int(val)
	case json.Number:
		n, _ := val.Int64()
		return int(n)
	}
	return 0
}

// TokenUsage 实际 token 用量（从 API 响应中提取）
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// chatWithTools 工具调用循环（使用原生 Function Calling）
func (rt *agentRuntime) chatWithTools(ctx context.Context, messages []llms.MessageContent, temp float64, onChunk func(chunk string)) (string, *TokenUsage, error) {
	lcMsgs := make([]llms.MessageContent, len(messages))
	copy(lcMsgs, messages)

	hasTools := len(rt.tools) > 0

	// 无工具 → 直接单次调用，不走循环
	if !hasTools {
		opts := rt.chatOptions(temp)
		resp, err := rt.llm.GenerateContent(ctx, lcMsgs, opts...)
		if err != nil {
			return "", nil, fmt.Errorf("LLM 调用失败: %w", err)
		}
		if len(resp.Choices) == 0 || resp.Choices[0].Content == "" {
			return "(模型返回了空内容)", nil, nil
		}
		return resp.Choices[0].Content, extractTokenUsage(resp), nil
	}

	// chatWithTools 统一使用 5 轮（非 Agent 循环，仅辅助工具调用）
	for round := 0; round < 5; round++ {
		opts := rt.chatOptionsStream(temp, onChunk)
		resp, err := rt.llm.GenerateContent(ctx, lcMsgs, opts...)
		if err != nil {
			return "", nil, fmt.Errorf("LLM 调用失败: %w", err)
		}
		if len(resp.Choices) == 0 {
			return "(模型返回了空内容)", nil, nil
		}

		text := resp.Choices[0].Content

		// 检查原生 ToolCalls（由 WithTools 触发）
		if len(resp.Choices[0].ToolCalls) > 0 {
			lcMsgs = append(lcMsgs, aiMsgWithToolCalls(text, resp.Choices[0].ToolCalls))
			// DeepSeek 思考模式：保存 reasoning_content 供下一轮回传
			if rt.tr != nil && resp.Choices[0].ReasoningContent != "" {
				rt.tr.stashReasoning(resp.Choices[0].ReasoningContent)
			}
			for _, tc := range resp.Choices[0].ToolCalls {
				name := tc.FunctionCall.Name
				args := tc.FunctionCall.Arguments
				log.Printf("[Tool] 第%d轮: %s", round+1, name)
				result, toolErr := ExecuteTool(ctx, name, args, "")
				if toolErr != nil {
					result = "error: " + toolErr.Error()
				}
				log.Printf("[Tool] %s -> %s", name, truncate(fmt.Sprintf("%v", result), 80))
				// TODO 更新标记 → 推送给前端并去除前缀
				if strings.HasPrefix(result, "__TODO_UPDATE__") {
					if onChunk != nil {
						onChunk(result)
					}
					result = strings.TrimPrefix(result, "__TODO_UPDATE__")
				}
				lcMsgs = append(lcMsgs, toolResultMsg(tc.ID, name, fmt.Sprintf("%v", result)))
			}
			continue
		}

		if text == "" {
			return "(模型返回了空内容)", nil, nil
		}
		return text, extractTokenUsage(resp), nil
	}
	return "(工具调用超过最大轮次)", nil, nil
}

// aiMsgWithToolCalls 构建包含 ToolCall part 的 AI 消息
func aiMsgWithToolCalls(text string, toolCalls []llms.ToolCall) llms.MessageContent {
	var parts []llms.ContentPart
	if text != "" {
		parts = append(parts, llms.TextContent{Text: text})
	}
	for _, tc := range toolCalls {
		tc := tc
		parts = append(parts, tc)
	}
	return llms.MessageContent{Role: llms.ChatMessageTypeAI, Parts: parts}
}

// toolResultMsg 构建工具执行结果消息（ChatMessageTypeTool）
func toolResultMsg(toolCallID, name, content string) llms.MessageContent {
	return llms.MessageContent{
		Role: llms.ChatMessageTypeTool,
		Parts: []llms.ContentPart{
			llms.ToolCallResponse{
				ToolCallID: toolCallID,
				Name:       name,
				Content:    content,
			},
		},
	}
}

// buildToolGuide 构建工具列表说明（仅用于需要文本提示的非原生场景）
func buildToolGuide(tools []llms.Tool) string {
	var sb strings.Builder
	sb.WriteString("## 可用工具\n")
	for _, t := range tools {
		if t.Function != nil {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", t.Function.Name, t.Function.Description))
		}
	}
	return sb.String()
}

// msgContent 获取 ChatMessage 的文本内容（content 为 string 时直接返回，为 map 时转 JSON）
func msgContent(m ChatMessage) string {
	if s, ok := m.Content.(string); ok {
		return s
	}
	b, _ := json.Marshal(m.Content)
	return string(b)
}

// ========== 消息转换 ==========

func messagesToLangChain(messages []ChatMessage) []llms.MessageContent {
	var result []llms.MessageContent
	for _, m := range messages {
		switch m.Type {
		case "system":
			result = append(result, llms.TextParts(llms.ChatMessageTypeSystem, msgContent(m)))
		case "summary":
			// 摘要注入 system：作为 system 消息上下文的一部分
			result = append(result, llms.TextParts(llms.ChatMessageTypeSystem, msgContent(m)))
		case "user":
			result = append(result, llms.TextParts(llms.ChatMessageTypeHuman, msgContent(m)))
		case "assistant", "message":
			result = append(result, llms.TextParts(llms.ChatMessageTypeAI, msgContent(m)))
		case "tool", "tool_call":
			// tool 块仅供前端展示，不喂给 LLM（原生 ToolCall 已处理）
		case "attachment":
			// 附件内容作为 HumanMessage 给 LLM
			result = append(result, llms.TextParts(llms.ChatMessageTypeHuman, msgContent(m)))
		}
	}
	return result
}

// toolsHash 计算工具集的 SHA256 哈希（用于缓存键）
func toolsHash(tools []llms.Tool) string {
	if len(tools) == 0 {
		return "noop"
	}
	h := sha256.New()
	for _, t := range tools {
		if t.Function != nil {
			h.Write([]byte(t.Function.Name))
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:8]
}
