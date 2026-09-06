package agent

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// expandPromptPlaceholders 展开提示词中的 ${placeholder} 占位符
// 支持的占位符:
//
//	${current_time}     - 当前时间 (HH:MM:SS)
//	${current_date}     - 当前日期 (YYYY-MM-DD)
//	${current_datetime} - 当前日期时间 (YYYY-MM-DD HH:MM:SS)
//	${model_name}       - 当前模型显示名称
//	${model_id}         - 当前模型 ID (provider/model)
//	${agent_id}         - 当前 Agent 的 ID
//	${agent_name}       - 当前 Agent 的显示名称
//	${tools_overview}   - 所有可用工具一览（内置 + 外部 MCP）
func expandPromptPlaceholders(template string, def *AgentDef, info *AIModelInfo, mcpID string) string {
	now := time.Now()
	result := template

	// 时间相关
	result = strings.ReplaceAll(result, "${current_time}", now.Format("15:04:05"))
	result = strings.ReplaceAll(result, "${current_date}", now.Format("2006-01-02"))
	result = strings.ReplaceAll(result, "${current_datetime}", now.Format("2006-01-02 15:04:05"))

	// Agent 相关
	if def != nil {
		result = strings.ReplaceAll(result, "${agent_id}", def.ID)
		result = strings.ReplaceAll(result, "${agent_name}", def.Name)
	}

	// 模型相关
	if info != nil {
		result = strings.ReplaceAll(result, "${model_name}", info.Name)
		result = strings.ReplaceAll(result, "${model_id}", info.Model)
	}

	// 工具一览（内置提供者 + 外部 MCP 统一）
	result = strings.ReplaceAll(result, "${tools_overview}", buildToolsOverview(def))

	return result
}

// expandPromptForSession 带 session 上下文的展开（用于流式对话）
func expandPromptForSession(template string, session *ChatSession) string {
	def := FindAgentDef(session.Agent)
	info := FindModel(session.Provider)
	return expandPromptPlaceholders(template, def, info, "")
}

// expandPromptForDef 仅根据 Agent 定义展开（用于创建会话时的初始 system message）
func expandPromptForDef(template string, def *AgentDef) string {
	return expandPromptPlaceholders(template, def, nil, "")
}

// buildToolsOverview 构建所有工具来源列表文本（内置 + 外部 MCP 统一）
func buildToolsOverview(def *AgentDef) string {
	if def == nil || len(def.MCPs) == 0 {
		return "(无可用工具)"
	}
	var lines []string
	for _, mcpID := range def.MCPs {
		// 内置提供者
		var bp *BuiltinProvider
		for _, p := range AllBuiltinProviders() {
			if p.ID == mcpID {
				bp = &p
				break
			}
		}
		if bp != nil {
			toolNames := make([]string, len(bp.Tools))
			for i, t := range bp.Tools {
				toolNames[i] = t.ID
			}
			lines = append(lines, fmt.Sprintf("- %s (%s): %s", bp.Name, bp.ID, strings.Join(toolNames, ", ")))
			continue
		}
		// 外部 MCP 服务器
		srv := FindMCPServer(mcpID)
		if srv == nil {
			continue
		}
		toolCount := len(srv.Tools)
		toolNames := make([]string, toolCount)
		for i, t := range srv.Tools {
			toolNames[i] = t.ID
		}
		sort.Strings(toolNames)
		status := "✓"
		if srv.Status != "ok" {
			status = "✗"
		}
		lines = append(lines, fmt.Sprintf("- %s %s (%s, %d tools: %s)", status, srv.Name, srv.ID, toolCount, strings.Join(toolNames, ", ")))
	}
	if len(lines) == 0 {
		return "(无可用工具)"
	}
	return strings.Join(lines, "\n")
}

// checkPromptToolPlaceholders 检查提示词是否包含必要的工具占位符
func checkPromptToolPlaceholders(def *AgentDef) []string {
	var warns []string
	hasToolOverview := strings.Contains(def.AgentMD, "${tools_overview}")

	if len(def.MCPs) > 0 && !hasToolOverview {
		warns = append(warns, "提示词缺少 ${tools_overview}，配置的工具可能无法被模型使用: "+strings.Join(def.MCPs, ", "))
	}
	return warns
}
