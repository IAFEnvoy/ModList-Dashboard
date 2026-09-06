package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tmc/langchaingo/llms"
)

// ========== LangChainGo Tool Definitions ==========

// AllBuiltinProviders 返回所有内置提供者（供前端展示用）
func AllBuiltinProviders() []BuiltinProvider {
	return []BuiltinProvider{
		{
			ID:          "read_only",
			Name:        "Unmodifiable File System",
			Description: "只读文件操作：读取、浏览、搜索文件和图片元数据",
			Tools: []BuiltinToolInfo{
				{ID: "read_file", Name: "Read File", Description: "读取文件指定行范围（必须提供 startLine/endLine，最多 200 行）"},
				{ID: "read_files", Name: "Read Multiple Files", Description: "批量读取多个文件（每个都要 startLine/endLine，最多 10 个）"},
				{ID: "list_files", Name: "List Files", Description: "列出目录内容，自动内联单文件夹目录。可指定 depth 控制深度"},
				{ID: "view_image", Name: "View Image", Description: "查看图片文件信息（大小、格式）"},
				{ID: "file_search", Name: "File Search", Description: "按 glob 模式搜索文件（如 **/*.go）"},
				{ID: "grep_search", Name: "Grep Search", Description: "在文件中按正则表达式搜索文本"},
			},
		},
		{
			ID:          "read_write",
			Name:        "Modifiable File System",
			Description: "可写文件操作：创建、写入、精确替换和批量编辑文件",
			Tools: []BuiltinToolInfo{
				{ID: "write_file", Name: "Write File", Description: "写入内容到文件，自动创建父目录"},
				{ID: "create_file", Name: "Create File", Description: "创建新文件并写入内容（已存在则报错）"},
				{ID: "create_directory", Name: "Create Directory", Description: "递归创建目录结构（mkdir -p）"},
				{ID: "replace_string_in_file", Name: "Replace in File", Description: "在文件中精确替换一处文本（需前后上下文）"},
				{ID: "multi_replace_string_in_file", Name: "Multi Replace in Files", Description: "一次性对多个文件执行多次替换"},
			},
		},
		{
			ID:          "web",
			Name:        "Web Fetch",
			Description: "从网页获取内容，用于摘要或分析",
			Tools: []BuiltinToolInfo{
				{ID: "fetch_webpage", Name: "Fetch Webpage", Description: "从网页获取主要内容，支持 HTML 转纯文本"},
				{ID: "download_file", Name: "Download File", Description: "从 URL 下载原始文件内容（适用于 .txt/.log/.json 等文本文件）"},
			},
		},
		{
			ID:          "todo",
			Name:        "TODO Tracking",
			Description: "管理当前会话的待办事项列表，方便用户查看任务进度",
			Tools: []BuiltinToolInfo{
				{ID: "manage_todo_list", Name: "Manage Todo List", Description: "创建/更新当前会话的待办事项列表，跟踪多步骤任务的进度"},
			},
		},
		{
			ID:          "ask",
			Name:        "Ask User",
			Description: "当遇到不确定或需要额外信息时，在聊天界面弹出对话框向用户提问",
			Tools: []BuiltinToolInfo{
				{ID: "ask_user", Name: "Ask User", Description: "向用户提问，支持选择题或自由文本输入"},
			},
		},
		{
			ID:          "shell",
			Name:        "Shell Command",
			Description: "在本地终端执行 Shell 命令并获取输出",
			Tools: []BuiltinToolInfo{
				{ID: "shell_exec", Name: "Execute Shell Command", Description: "Execute a shell command and get the output. Use `cmd /c` on Windows."},
			},
		},
		{
			ID:          "drive",
			Name:        "Cloud Drive",
			Description: "读写云盘文件系统中的 agent/<会话ID>/ 目录。可在此目录下创建、读取、写入、删除文件和文件夹，用于存储临时文件、计划、日志等。无需审批。",
			Tools: []BuiltinToolInfo{
				{ID: "drive_list", Name: "List Drive Files", Description: "列出云盘 agent 工作目录下的文件和文件夹"},
				{ID: "drive_read", Name: "Read Drive File", Description: "读取云盘 agent 工作目录下的文本文件内容"},
				{ID: "drive_write", Name: "Write Drive File", Description: "写入内容到云盘 agent 工作目录下"},
				{ID: "drive_mkdir", Name: "Create Drive Directory", Description: "在云盘 agent 工作目录下创建文件夹"},
				{ID: "drive_delete", Name: "Delete Drive Entry", Description: "删除云盘 agent 工作目录下的文件或文件夹"},
			},
		},
	}
}

// allBuiltinToolsByProvider 按提供者 ID 返回其工具定义
func allBuiltinToolsByProvider(providerID string) []llms.Tool {
	switch providerID {
	case "read_only":
		return []llms.Tool{
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "read_file",
				Description: "读取文件指定行范围的内容。行号从 1 开始。startLine 和 endLine 必须提供，范围不超过 200 行（endLine - startLine <= 199）。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":      map[string]any{"type": "string", "description": "文件绝对路径"},
						"startLine": map[string]any{"type": "number", "description": "起始行号（从 1 开始）"},
						"endLine":   map[string]any{"type": "number", "description": "结束行号（含），最大 startLine+199"},
					},
					"required": []string{"path", "startLine", "endLine"},
				},
			}},
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "read_files",
				Description: "批量读取多个文件。优先使用此工具同时读取多个相关文件，避免多次调用 read_file。每个文件必须指定 startLine/endLine（最多 200 行）。最多一次读取 10 个文件。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"paths": map[string]any{
							"type":        "array",
							"description": "要读取的文件列表，每个含 path(字符串), startLine(数字), endLine(数字)",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"path":      map[string]any{"type": "string", "description": "文件绝对路径"},
									"startLine": map[string]any{"type": "number", "description": "起始行号（1 开始）"},
									"endLine":   map[string]any{"type": "number", "description": "结束行号（含）"},
								},
								"required": []string{"path", "startLine", "endLine"},
							},
						},
					},
					"required": []string{"paths"},
				},
			}},
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "list_files",
				Description: "列出目录内容。自动跳过只有一个文件夹的目录（内联）。以 / 结尾表示文件夹。depth 参数控制搜索深度（跳过单文件夹目录不计入深度），默认 1。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":  map[string]any{"type": "string", "description": "目录绝对路径（可选）"},
						"depth": map[string]any{"type": "number", "description": "搜索深度（默认 1），跳过单文件夹目录不计入"},
					},
				},
			}},
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "view_image",
				Description: "查看图片文件信息。返回文件大小、格式等元数据。",
				Parameters:  toolParams("filePath", "string", "图片文件绝对路径"),
			}},
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "file_search",
				Description: "通过 glob 模式在 workspace 中搜索文件。仅返回匹配文件的路径。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query":      map[string]any{"type": "string", "description": "glob 匹配模式，如 **/*.go 或 src/**"},
						"maxResults": map[string]any{"type": "number", "description": "最大返回结果数（可选）"},
					},
					"required": []string{"query"},
				},
			}},
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "grep_search",
				Description: "在 workspace 中按正则表达式搜索文件内容。返回匹配行及文件路径。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query":               map[string]any{"type": "string", "description": "搜索模式（字符串或正则表达式）"},
						"isRegexp":            map[string]any{"type": "boolean", "description": "是否为正则表达式"},
						"includePattern":      map[string]any{"type": "string", "description": "限定搜索文件的 glob 模式（可选）"},
						"maxResults":          map[string]any{"type": "number", "description": "最大返回结果数（可选）"},
						"includeIgnoredFiles": map[string]any{"type": "boolean", "description": "是否包含 .gitignore 忽略的文件（可选）"},
					},
					"required": []string{"query", "isRegexp"},
				},
			}},
		}
	case "read_write":
		return []llms.Tool{
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "write_file",
				Description: "写入内容到文件。自动创建父目录。",
				Parameters:  toolParams2("path", "文件绝对路径", "content", "要写入的内容"),
			}},
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "create_file",
				Description: "在 workspace 中创建新文件并写入内容。目录不存在时会自动创建。不可用于编辑已存在的文件。",
				Parameters:  toolParams2("filePath", "要创建的文件的绝对路径", "content", "要写入文件的内容"),
			}},
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "create_directory",
				Description: "在 workspace 中递归创建目录结构（类似 mkdir -p）。",
				Parameters:  toolParams("dirPath", "string", "要创建的目录绝对路径"),
			}},
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "replace_string_in_file",
				Description: "在文件中精确替换一处文本。oldString 必须精确匹配（含空白和缩进），且只能匹配一处。需要前后至少3行上下文。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"filePath":  map[string]any{"type": "string", "description": "要编辑的文件绝对路径"},
						"oldString": map[string]any{"type": "string", "description": "要替换的精确文本（至少含3行前后上下文）"},
						"newString": map[string]any{"type": "string", "description": "替换后的精确文本"},
					},
					"required": []string{"filePath", "oldString", "newString"},
				},
			}},
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "multi_replace_string_in_file",
				Description: "一次性对多个文件执行多次精确替换。比逐个调用 replace_string_in_file 更高效。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"explanation": map[string]any{"type": "string", "description": "对此次多重替换的简要说明"},
						"replacements": map[string]any{
							"type":        "array",
							"description": "替换操作数组",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"filePath":  map[string]any{"type": "string", "description": "要编辑的文件绝对路径"},
									"oldString": map[string]any{"type": "string", "description": "要替换的精确文本"},
									"newString": map[string]any{"type": "string", "description": "替换后的精确文本"},
								},
								"required": []string{"filePath", "oldString", "newString"},
							},
						},
					},
					"required": []string{"explanation", "replacements"},
				},
			}},
		}
	case "web":
		return []llms.Tool{
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "fetch_webpage",
				Description: "从网页获取主要内容，用于摘要或分析。支持批量 URL 并基于关键词过滤。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"urls":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "要获取内容的 URL 数组"},
						"query": map[string]any{"type": "string", "description": "要在网页内容中搜索的查询描述"},
					},
					"required": []string{"urls", "query"},
				},
			}},
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "download_file",
				Description: "从 URL 下载原始文件内容（不转 HTML）。适合下载 .txt/.log/.json/.xml 等纯文本文件。返回原始文本内容，最多 2MB。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"url": map[string]any{"type": "string", "description": "要下载的文件的 URL"},
					},
					"required": []string{"url"},
				},
			}},
		}
	case "todo":
		return []llms.Tool{
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "manage_todo_list",
				Description: "管理当前会话的待办事项列表。用于跟踪多步骤任务的进度。每次调用会完全替换当前列表。每个事项必须有 id（数字）、title（简短描述）和 status（not-started/in-progress/completed）。允许多个 in-progress。完成后先标记 completed 再开始下一个。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"todoList": map[string]any{
							"type":        "array",
							"description": "待办事项数组，每项含 id(number), title(string), status(string)",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"id":     map[string]any{"type": "number", "description": "唯一标识符"},
									"title":  map[string]any{"type": "string", "description": "简短描述"},
									"status": map[string]any{"type": "string", "description": "not-started / in-progress / completed"},
								},
								"required": []string{"id", "title", "status"},
							},
						},
					},
					"required": []string{"todoList"},
				},
			}},
		}
	case "ask":
		return []llms.Tool{
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "ask_user",
				Description: "当遇到不确定的问题或需要用户额外输入时使用。向用户展示问题，可附带选择项或自由文本输入。调用后等待用户回答，将答案返回。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"questions": map[string]any{
							"type":        "array",
							"description": "问题列表（通常只需1个），每项含 header(简短标题), question(问题描述), options(可选选项数组, 每项含label), multiSelect(是否多选), allowFreeformInput(是否允许自由文本)",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"header":             map[string]any{"type": "string", "description": "简短标题/标识"},
									"question":           map[string]any{"type": "string", "description": "问题描述"},
									"options":            map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"label": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}}, "required": []string{"label"}}, "description": "可选选项列表"},
									"multiSelect":        map[string]any{"type": "boolean", "description": "是否允许多选"},
									"allowFreeformInput": map[string]any{"type": "boolean", "description": "是否允许自由文本输入"},
								},
								"required": []string{"header", "question"},
							},
						},
					},
					"required": []string{"questions"},
				},
			}},
		}
	case "shell":
		return []llms.Tool{
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "shell_exec",
				Description: "执行 Shell 命令并获取输出。每次调用必须提供 description 字段简要说明该命令的**作用**（不是重复命令文本），方便用户审批时理解。使用 `cmd /c` 在 Windows 上执行。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command":     map[string]any{"type": "string", "description": "要执行的 Shell 命令"},
						"cwd":         map[string]any{"type": "string", "description": "工作目录（可选）"},
						"description": map[string]any{"type": "string", "description": "简要描述该命令的作用（如：删除临时构建产物、重启服务等），用于审批展示"},
					},
					"required": []string{"command", "description"},
				},
			}},
		}
	case "drive":
		return []llms.Tool{
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "drive_list",
				Description: "列出云盘 agent 工作目录下的文件和文件夹。可选指定子路径。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "相对于 agent 工作目录的路径（选填，留空代表根目录）"},
					},
				},
			}},
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "drive_read",
				Description: "读取云盘 agent 工作目录下的文本文件内容。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "相对于 agent 工作目录的文件路径"},
					},
					"required": []string{"path"},
				},
			}},
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "drive_write",
				Description: "写入内容到云盘 agent 工作目录下，自动创建父目录。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": "相对于 agent 工作目录的文件路径"},
						"content": map[string]any{"type": "string", "description": "文件内容"},
					},
					"required": []string{"path", "content"},
				},
			}},
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "drive_mkdir",
				Description: "在云盘 agent 工作目录下创建文件夹。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "相对于 agent 工作目录的文件夹路径"},
					},
					"required": []string{"path"},
				},
			}},
			{Type: "function", Function: &llms.FunctionDefinition{
				Name:        "drive_delete",
				Description: "删除云盘 agent 工作目录下的文件或文件夹。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "相对于 agent 工作目录的路径"},
					},
					"required": []string{"path"},
				},
			}},
		}
	}
	return nil
}

// BuiltinTools 返回指定 Agent 启用的内置工具（LangChainGo 格式）。
func BuiltinTools(def *AgentDef) []llms.Tool {
	if def == nil {
		return nil
	}
	var ids []string
	for _, id := range def.MCPs {
		if isBuiltinProviderID(id) {
			ids = append(ids, id)
		}
	}
	return builtinToolsByIDs(ids)
}

func BuildToolsForMCP(mcpID string) []llms.Tool {
	srv := FindMCPServer(mcpID)
	if srv == nil || len(srv.Tools) == 0 {
		return nil
	}
	var tools []llms.Tool
	for _, t := range srv.Tools {
		fullName := mcpID + "__" + t.ID
		params := toolParamsOpt("input", "string", "Tool arguments as JSON string")
		tools = append(tools, llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        fullName,
				Description: fmt.Sprintf("[%s] %s", srv.Name, t.Description),
				Parameters:  params,
			},
		})
	}
	return tools
}

// isBuiltinProviderID 判断是否是内置工具提供者 ID
func isBuiltinProviderID(id string) bool {
	for _, p := range AllBuiltinProviders() {
		if p.ID == id {
			return true
		}
	}
	return false
}

// BuildTools 构建 Agent 的所有工具（内置提供者 + 外部 MCP 服务器混合）
func BuildTools(def *AgentDef) []llms.Tool {
	if def == nil {
		return nil
	}
	// 解析 mcps: 内置提供者 ID → 内置工具；其他 → MCP 服务器
	var builtinIDs []string
	for _, id := range def.MCPs {
		if isBuiltinProviderID(id) {
			builtinIDs = append(builtinIDs, id)
		}
	}

	tools := builtinToolsByIDs(builtinIDs)

	// 外部 MCP 工具
	for _, id := range def.MCPs {
		if !isBuiltinProviderID(id) {
			tools = append(tools, BuildToolsForMCP(id)...)
		}
	}
	return tools
}

// builtinToolsByIDs 按提供者 ID 列表返回内置工具
func builtinToolsByIDs(ids []string) []llms.Tool {
	// 空列表 = 全部内置工具
	if len(ids) == 0 {
		var all []llms.Tool
		for _, p := range AllBuiltinProviders() {
			all = append(all, allBuiltinToolsByProvider(p.ID)...)
		}
		return all
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	var filtered []llms.Tool
	for _, p := range AllBuiltinProviders() {
		if set[p.ID] {
			filtered = append(filtered, allBuiltinToolsByProvider(p.ID)...)
		}
	}
	return filtered
}

// ========== 参数辅助函数 ==========

func toolParams(name, typ, desc string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			name: map[string]any{"type": typ, "description": desc},
		},
		"required": []string{name},
	}
}

func toolParams2(n1, d1, n2, d2 string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			n1: map[string]any{"type": "string", "description": d1},
			n2: map[string]any{"type": "string", "description": d2},
		},
		"required": []string{n1, n2},
	}
}

func toolParamsOpt(name, typ, desc string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{name: map[string]any{"type": typ, "description": desc}},
	}
}

func toolParams2Opt(n1, d1, n2, d2 string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			n1: map[string]any{"type": "string", "description": d1},
			n2: map[string]any{"type": "string", "description": d2},
		},
		"required": []string{n1},
	}
}

// ========== Tool Execution ==========

// dangerousTools 需要用户审批的危险操作
var dangerousTools = map[string]bool{
	"write_file":                   true,
	"create_file":                  true,
	"create_directory":             true,
	"replace_string_in_file":       true,
	"multi_replace_string_in_file": true,
	"fetch_webpage":                true,
	"download_file":                true,
	"shell_exec":                   true,
}

var bypassSafeTools = map[string]bool{
	"download_file": true,
	"fetch_webpage": true,
}

// RequestApproval 请求用户审批危险操作。返回 true=通过, false=拒绝。
func RequestApproval(ctx context.Context, toolName string, argsJSON string) bool {
	sessionID := execSessionID(ctx)
	session := GetSession(sessionID)
	if session == nil {
		log.Printf("[Tool] approval rejected without active session: %s", toolName)
		return false
	}

	// bypass 开启：只读工具自动通过
	if session != nil && session.BypassApproval {
		if bypassSafeTools[toolName] {
			log.Printf("[Tool] bypass: %s", toolName)
			return true
		}
		// shell_exec 仅在纯只读命令时自动绕过
		if toolName == "shell_exec" {
			warnLevel := safetyCheckShell(argsJSON)
			if warnLevel == "" {
				log.Printf("[Tool] bypass shell (safe): %s", argsJSON)
				return true
			}
		}
	}
	summary, detail := formatApprovalSummary(toolName, argsJSON)
	warnLevel := ""
	if toolName == "shell_exec" {
		warnLevel = safetyCheckShell(argsJSON)
	}
	block := map[string]interface{}{"tool": toolName, "summary": summary, "detail": detail, "warnLevel": warnLevel}
	pub := getOrCreateBroadcaster(sessionID)
	if pub != nil {
		pub.ApprovalRequest(session, block)
	}
	go SaveSessions()
	ch := make(chan bool, 1)
	approvalChannelsMu.Lock()
	approvalChannels[sessionID] = ch
	approvalChannelsMu.Unlock()
	approved := <-ch
	block["result"] = approved
	if pub != nil {
		pub.broadcast(SSEEvent{Type: "block", Subtype: "approval_request", ID: len(session.Messages) - 1, Content: block})
	}
	return approved
}

// formatApprovalSummary 生成审批摘要和详情
func formatApprovalSummary(toolName string, argsJSON string) (summary, detail string) {
	var a struct {
		Path        string   `json:"path"`
		FilePath    string   `json:"file_path"`
		DirPath     string   `json:"dirPath"`
		Content     string   `json:"content"`
		Command     string   `json:"command"`
		Description string   `json:"description"`
		OldStr      string   `json:"oldString"`
		NewStr      string   `json:"newString"`
		URL         string   `json:"url"`
		URLs        []string `json:"urls"`
	}
	json.Unmarshal([]byte(argsJSON), &a)

	switch toolName {
	case "write_file":
		return "写入文件: " + filepath.Base(a.Path),
			"路径: " + a.Path + "\n内容预览: " + truncate(a.Content, 200)
	case "create_file":
		return "创建文件: " + filepath.Base(a.FilePath),
			"路径: " + a.FilePath + "\n内容预览: " + truncate(a.Content, 200)
	case "create_directory":
		return "创建目录: " + filepath.Base(a.DirPath),
			"路径: " + a.DirPath
	case "replace_string_in_file":
		fp := a.FilePath
		if fp == "" {
			fp = a.Path
		}
		return "修改文件: " + filepath.Base(fp),
			"文件: " + fp + "\n\n- 旧文本:\n" + truncate(a.OldStr, 300) + "\n\n+ 新文本:\n" + truncate(a.NewStr, 300)
	case "multi_replace_string_in_file":
		return "批量修改文件",
			"参数: " + truncate(argsJSON, 400)
	case "fetch_webpage":
		urls := strings.Join(a.URLs, ", ")
		return "访问网页: " + truncate(urls, 60),
			"URLs:\n" + strings.Join(a.URLs, "\n")
	case "download_file":
		return "下载文件: " + truncate(a.URL, 60),
			"URL: " + a.URL
	case "shell_exec":
		desc := a.Description
		if desc == "" {
			desc = a.Command
		}
		summary := "执行命令: " + truncate(a.Command, 80)
		// 检查是否有危险模式警告
		if warn := safetyCheckShell(a.Command); strings.HasPrefix(warn, "warn:") {
			summary = "⚠️ 危险命令: " + truncate(a.Command, 80)
			detail = "⚠️ 安全警告: " + warn[6:] + "\n\n作用: " + desc + "\n\n命令: " + a.Command
		} else {
			detail = "作用: " + desc + "\n命令: " + a.Command
		}
		return summary, detail
	}
	return "调用工具: " + toolName, "参数: " + truncate(argsJSON, 300)
}

// safetyCheckShell 检查 shell 命令，返回:
//
//	"" = 安全
//	"warn: ..." = 有风险但允许（会弹出警告）
//	"block: ..." = 严重危险，禁止执行
func safetyCheckShell(command string) string {
	lower := strings.ToLower(command)

	// 绝对禁止：裸盘操作、系统破坏
	blockPatterns := []string{
		"dd if=", "mkfs.", ":(){ :|:& };:",
		"> /dev/sda", ">/dev/sda", "format c:",
		"rd /s /q c:\\", "del /f /s /q c:\\",
	}
	for _, p := range blockPatterns {
		if strings.Contains(lower, strings.ToLower(p)) {
			return "block: 命令包含系统破坏模式: " + p
		}
	}

	// 危险但可能合理：删除、关机、注册表等（弹窗警告）
	warnPatterns := []string{
		"rm -rf", "rm -r", "del /f", "del /s",
		"shutdown", "reboot",
		"reg delete", "reg add",
		"chmod 777", "wget", "curl", "nc ", "ncat",
		"taskkill",
	}
	for _, p := range warnPatterns {
		if strings.Contains(lower, strings.ToLower(p)) {
			return "warn: 命令包含潜在危险模式: " + p
		}
	}
	return ""
}

// ExecuteTool 执行工具命令。cwd 为项目工作目录（可为空）。
func ExecuteTool(ctx context.Context, name string, argsJSON string, cwd string) (string, error) {
	// MCP 工具调度（名称格式: mcpID__toolName）
	if idx := strings.Index(name, "__"); idx > 0 {
		mcpID := name[:idx]
		toolName := name[idx+2:]
		return CallMCPTool(mcpID, toolName, argsJSON)
	}

	// 通用参数
	type tArgs struct {
		Path     string `json:"path"`
		FilePath string `json:"filePath"`
		File     string `json:"file"`
		DirPath  string `json:"dirPath"`
		Content  string `json:"content"`
		Command  string `json:"command"`
		Cmd      string `json:"cmd"`
		Cwd      string `json:"cwd"`
		// read_file 扩展
		StartLine int `json:"startLine"`
		EndLine   int `json:"endLine"`
		// list_files 扩展
		Depth int `json:"depth"`
		// read_files 扩展
		Paths []struct {
			Path      string `json:"path"`
			StartLine int    `json:"startLine"`
			EndLine   int    `json:"endLine"`
		} `json:"paths"`
		// replace
		OldString string `json:"oldString"`
		NewString string `json:"newString"`
		// multi_replace
		Explanation  string `json:"explanation"`
		Replacements []struct {
			FilePath  string `json:"filePath"`
			OldString string `json:"oldString"`
			NewString string `json:"newString"`
		} `json:"replacements"`
		// file_search / grep_search
		Query               string `json:"query"`
		IsRegexp            bool   `json:"isRegexp"`
		IncludePattern      string `json:"includePattern"`
		MaxResults          int    `json:"maxResults"`
		IncludeIgnoredFiles bool   `json:"includeIgnoredFiles"`
		// fetch_webpage
		URLs []string `json:"urls"`
		URL  string   `json:"url"`
		// manage_todo_list
		TodoList []struct {
			ID     int    `json:"id"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"todoList"`
		// ask_user
		Questions []struct {
			Header             string `json:"header"`
			Question           string `json:"question"`
			MultiSelect        bool   `json:"multiSelect"`
			AllowFreeformInput bool   `json:"allowFreeformInput"`
			Options            []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	var a tArgs
	json.Unmarshal([]byte(argsJSON), &a)

	// 兼容别名
	if a.Path == "" {
		if a.FilePath != "" {
			a.Path = a.FilePath
		} else if a.File != "" {
			a.Path = a.File
		}
	}
	if a.Command == "" && a.Cmd != "" {
		a.Command = a.Cmd
	}

	resolvePath := func(p string) string {
		if p == "" {
			return p
		}
		if base := getSandboxRoot(cwd); base != "" {
			// 相对路径相对于沙箱根
			if !filepath.IsAbs(p) {
				abs, _ := filepath.Abs(filepath.Join(base, p))
				return filepath.Clean(abs)
			}
			// 绝对路径必须也在沙箱内
			cleaned := filepath.Clean(p)
			if isPathWithin(cleaned, base) {
				return cleaned
			}
			// 否则尝试结合 base（路径可能写成了绝对但实际上在项目内）
			alt := filepath.Clean(filepath.Join(base, strings.TrimPrefix(cleaned, "\\")))
			if isPathWithin(alt, base) {
				return alt
			}
			// 绝对路径不在沙箱内 → 拒绝
			return "" // 调用方检查空字符串
		}
		if !filepath.IsAbs(p) {
			abs, _ := filepath.Abs(filepath.Join(cwd, p))
			return filepath.Clean(abs)
		}
		return filepath.Clean(p)
	}

	switch name {

	// ---- filesystem ----
	case "read_file":
		if a.Path == "" {
			return "error: path required", nil
		}
		if a.StartLine <= 0 || a.EndLine <= 0 {
			return "error: startLine and endLine are required (1-based, max 200 lines)", nil
		}
		if a.EndLine-a.StartLine >= 200 {
			a.EndLine = a.StartLine + 199
		}
		p := resolvePath(a.Path)
		if p == "" {
			return "error: path is outside the project directory (sandbox: " + getSandboxRoot(cwd) + ")", nil
		}
		return readFileLines(p, a.StartLine, a.EndLine)

	case "write_file":
		if a.Path == "" || a.Content == "" {
			return "error: path and content required", nil
		}
		p := resolvePath(a.Path)
		if p == "" {
			return "error: path is outside the project directory", nil
		}
		os.MkdirAll(filepath.Dir(p), 0755)
		if err := os.WriteFile(p, []byte(a.Content), 0644); err != nil {
			return "error: " + err.Error(), nil
		}
		return "ok", nil

	case "read_files":
		if len(a.Paths) == 0 {
			return "error: paths required (array of {path, startLine, endLine})", nil
		}
		if len(a.Paths) > 10 {
			a.Paths = a.Paths[:10]
		}
		var results []string
		for i, pf := range a.Paths {
			if pf.Path == "" {
				results = append(results, fmt.Sprintf("[%d] error: path required", i+1))
				continue
			}
			if pf.StartLine <= 0 || pf.EndLine <= 0 {
				results = append(results, fmt.Sprintf("[%d] %s: error: startLine and endLine required", i+1, filepath.Base(pf.Path)))
				continue
			}
			if pf.EndLine-pf.StartLine >= 200 {
				pf.EndLine = pf.StartLine + 199
			}
			p := resolvePath(pf.Path)
			if p == "" {
				results = append(results, fmt.Sprintf("[%d] %s: error: path outside sandbox", i+1, filepath.Base(pf.Path)))
				continue
			}
			content, _ := readFileLines(p, pf.StartLine, pf.EndLine)
			results = append(results, fmt.Sprintf("=== %s (L%d-L%d) ===\n%s", filepath.Base(pf.Path), pf.StartLine, pf.EndLine, content))
		}
		return strings.Join(results, "\n\n"), nil

	case "list_files":
		p := resolvePath(a.Path)
		if p == "" {
			sr := getSandboxRoot(cwd)
			if sr != "" {
				p = sr
			} else {
				p = "."
			}
		}
		depth := a.Depth
		if depth <= 0 {
			depth = 1
		}
		return listFilesWithDepth(p, depth), nil

	case "view_image":
		p := resolvePath(a.FilePath)
		if p == "" {
			p = resolvePath(a.Path)
		}
		if p == "" {
			return "error: filePath required", nil
		}
		if getSandboxRoot(cwd) != "" && !isPathWithin(p, getSandboxRoot(cwd)) {
			return "error: path is outside the project directory", nil
		}
		info, err := os.Stat(p)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		imgTypes := map[string]string{
			".png": "PNG", ".jpg": "JPEG", ".jpeg": "JPEG",
			".gif": "GIF", ".webp": "WebP", ".bmp": "BMP", ".svg": "SVG",
		}
		ft := imgTypes[ext]
		if ft == "" {
			ft = ext
		}
		return fmt.Sprintf("image/%s  size=%d  modified=%s", ft, info.Size(), info.ModTime().Format("2006-01-02 15:04:05")), nil

	// ---- edit ----
	case "create_file":
		if a.FilePath == "" || a.Content == "" {
			return "error: filePath and content required", nil
		}
		p := resolvePath(a.FilePath)
		if p == "" {
			return "error: filePath is outside the project directory", nil
		}
		if _, err := os.Stat(p); err == nil {
			return fmt.Sprintf("error: file already exists: %s (use replace_string_in_file to edit)", filepath.Base(p)), nil
		}
		os.MkdirAll(filepath.Dir(p), 0755)
		if err := os.WriteFile(p, []byte(a.Content), 0644); err != nil {
			return "error: " + err.Error(), nil
		}
		return "ok: created " + filepath.Base(p), nil

	case "create_directory":
		if a.DirPath == "" {
			return "error: dirPath required", nil
		}
		p := resolvePath(a.DirPath)
		if p == "" {
			return "error: path is outside the project directory", nil
		}
		if err := os.MkdirAll(p, 0755); err != nil {
			return "error: " + err.Error(), nil
		}
		return "ok", nil

	case "replace_string_in_file":
		if a.FilePath == "" || a.OldString == "" || a.NewString == "" {
			return "error: filePath, oldString, newString required", nil
		}
		p := resolvePath(a.FilePath)
		if p == "" {
			return "error: path is outside the project directory", nil
		}
		return doReplaceInFile(p, a.OldString, a.NewString)

	case "multi_replace_string_in_file":
		if len(a.Replacements) == 0 {
			return "error: replacements required", nil
		}
		var results []string
		ok, fail := 0, 0
		for i, rep := range a.Replacements {
			p := resolvePath(rep.FilePath)
			if p == "" {
				fail++
				results = append(results, fmt.Sprintf("[%d] %s: error: path is outside the project directory", i+1, filepath.Base(rep.FilePath)))
				continue
			}
			r, _ := doReplaceInFile(p, rep.OldString, rep.NewString)
			if strings.HasPrefix(r, "ok") {
				ok++
			} else {
				fail++
				results = append(results, fmt.Sprintf("[%d] %s: %s", i+1, filepath.Base(rep.FilePath), r))
			}
		}
		if fail == 0 {
			return fmt.Sprintf("ok: %d replacements applied", ok), nil
		}
		return fmt.Sprintf("partial: %d ok, %d failed\n%s", ok, fail, strings.Join(results, "\n")), nil

	// ---- web ----
	case "fetch_webpage":
		return doFetchWebpage(a.URLs, a.Query)
	case "download_file":
		if a.URL == "" {
			return "error: url required", nil
		}
		return doDownloadFile(a.URL)

	// ---- search ----
	case "file_search":
		if a.Query == "" {
			return "error: query (glob) required", nil
		}
		return doFileSearch(getSandboxRoot(cwd), a.Query, a.MaxResults)

	case "grep_search":
		if a.Query == "" {
			return "error: query required", nil
		}
		return doGrepSearch(getSandboxRoot(cwd), a.Query, a.IsRegexp, a.IncludePattern, a.MaxResults, a.IncludeIgnoredFiles)

	// ---- shell ----
	case "shell_exec":
		if a.Command == "" {
			return "error: command required", nil
		}
		// 安全校验
		scResult := safetyCheckShell(a.Command)
		if strings.HasPrefix(scResult, "block:") {
			return "error: 命令被安全策略拒绝: " + scResult, nil
		}
		workDir := a.Cwd
		if workDir == "" {
			workDir = cwd
		}
		cmd := exec.CommandContext(ctx, "cmd", "/c", a.Command)
		if workDir != "" {
			cmd.Dir = workDir
		}
		out, err := cmd.CombinedOutput()
		r := string(out)
		if len(r) > 3000 {
			r = r[:3000] + "\n... (truncated)"
		}
		if err != nil {
			r += "\nexit: " + err.Error()
		}
		return r, nil

	// ---- todo ----
	case "manage_todo_list":
		return executeManageTodoList(ctx, a.TodoList)

	// ---- ask ----
	case "ask_user":
		if len(a.Questions) == 0 {
			return "error: questions required", nil
		}
		return executeAskUser(ctx, a.Questions)

	// ---- drive (cloud storage scoped to agent/<sessionID>/) ----
	case "drive_list":
		return executeDriveList(ctx, a.Path)
	case "drive_read":
		return executeDriveRead(ctx, a.Path)
	case "drive_write":
		return executeDriveWrite(ctx, a.Path, a.Content)
	case "drive_mkdir":
		return executeDriveMkdir(ctx, a.Path)
	case "drive_delete":
		return executeDriveDelete(ctx, a.Path)
	}
	return "", nil
}

// executeManageTodoList 更新会话的 TODO 列表
func executeManageTodoList(ctx context.Context, todoList []struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}) (string, error) {
	// 通过全局 context 获取当前 sessionID（由 handler 注入）
	sessionID := execSessionID(ctx)
	if sessionID == "" {
		return `{"todos":[],"message":"no active session"}`, nil
	}

	sessionMu.Lock()
	// 获取旧列表用于对比变更
	var oldTodos []TodoItem
	for i := range AgentSessions {
		if AgentSessions[i].ID == sessionID {
			oldTodos = AgentSessions[i].Todos
			break
		}
	}
	oldMap := make(map[string]TodoItem, len(oldTodos))
	for _, t := range oldTodos {
		oldMap[t.ID] = t
	}

	todos := make([]TodoItem, 0, len(todoList))
	for _, t := range todoList {
		todos = append(todos, TodoItem{
			ID:     fmt.Sprintf("%s_%d", sessionID, t.ID),
			Title:  t.Title,
			Status: t.Status,
		})
	}
	// 更新会话的 Todos
	for i := range AgentSessions {
		if AgentSessions[i].ID == sessionID {
			AgentSessions[i].Todos = todos
			break
		}
	}
	sessionMu.Unlock()

	dbReplaceTodos(sessionID, todos)

	// 生成友好的变更摘要
	var added, updated, removed int
	newMap := make(map[string]TodoItem, len(todos))
	for _, t := range todos {
		newMap[t.ID] = t
	}

	var changes []string
	// 检查新增和更新
	for _, t := range todos {
		old, existed := oldMap[t.ID]
		if !existed {
			added++
		} else if old.Status != t.Status {
			updated++
			statusName := t.Status
			switch statusName {
			case "in-progress":
				statusName = "进行中"
			case "completed":
				statusName = "已完成"
			default:
				statusName = "未开始"
			}
			changes = append(changes, fmt.Sprintf("• %s → %s", t.Title, statusName))
		}
	}
	// 检查删除
	for _, old := range oldTodos {
		if _, kept := newMap[old.ID]; !kept {
			removed++
			changes = append(changes, fmt.Sprintf("• %s (已移除)", old.Title))
		}
	}

	var summary string
	if added > 0 && len(oldTodos) == 0 {
		summary = fmt.Sprintf("📋 已添加 %d 个待办事项", added)
	} else if added > 0 {
		summary = fmt.Sprintf("📋 已添加 %d 个待办事项，共 %d 项", added, len(todos))
	} else if updated > 0 || removed > 0 {
		summary = "📋 待办事项已更新"
		if len(changes) > 0 {
			if len(changes) > 3 {
				changes = changes[:3]
			}
			summary += "\n" + strings.Join(changes, "\n")
		}
	} else {
		summary = "📋 待办事项已更新（共 " + fmt.Sprintf("%d", len(todos)) + " 项）"
	}

	// 构建完整输出（含详细列表，供前端 widget 使用）
	var detail strings.Builder
	detail.WriteString(summary)
	detail.WriteString("\n")
	for _, t := range todos {
		icon := "⬜"
		switch t.Status {
		case "in-progress":
			icon = "🔄"
		case "completed":
			icon = "✅"
		}
		detail.WriteString(fmt.Sprintf("%s [%s] %s\n", icon, t.Status, t.Title))
	}
	return "__TODO_UPDATE__" + detail.String(), nil
}

// executeAskUser 暂停并等待用户回答
func executeAskUser(ctx context.Context, questions []struct {
	Header             string `json:"header"`
	Question           string `json:"question"`
	MultiSelect        bool   `json:"multiSelect"`
	AllowFreeformInput bool   `json:"allowFreeformInput"`
	Options            []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
}) (string, error) {
	sessionID := execSessionID(ctx)
	if sessionID == "" {
		return "error: no active session", nil
	}

	// 自包含实现：通过 broadcaster + channel 发布问题并等待回答
	header := ""
	multiSelect := false
	allowFreeform := true
	var opts []AskOption
	if len(questions) > 0 {
		header = questions[0].Header
		multiSelect = questions[0].MultiSelect
		allowFreeform = questions[0].AllowFreeformInput
		for _, o := range questions[0].Options {
			opts = append(opts, AskOption{Label: o.Label, Description: o.Description})
		}
	}

	qData := map[string]interface{}{
		"header": header, "question": questions[0].Question,
		"multiSelect": multiSelect, "allowFreeformInput": allowFreeform,
	}
	if len(opts) > 0 {
		qData["options"] = opts
	}

	// 通过 broadcaster 发布
	pub := getOrCreateBroadcaster(sessionID)
	pub.AskUser(GetSession(sessionID), qData)

	// 持久化
	sessionMu.Lock()
	for i := range AgentSessions {
		if AgentSessions[i].ID == sessionID {
			AgentSessions[i].PendingQuestion = &PendingQuestion{Header: header, Question: questions[0].Question, MultiSelect: multiSelect, AllowFreeformInput: allowFreeform, Options: opts}
			break
		}
	}
	sessionMu.Unlock()
	go SaveSessions()

	// 阻塞等待回答
	ch := make(chan string, 1)
	questionChannelsMu.Lock()
	questionChannels[sessionID] = ch
	questionChannelsMu.Unlock()
	answer := <-ch

	// 写回 result + 广播
	qData["result"] = answer
	blkID := len(GetSession(sessionID).Messages) - 1
	sessionMu.Lock()
	for i := range AgentSessions {
		if AgentSessions[i].ID == sessionID {
			if blkID < len(AgentSessions[i].Messages) {
				AgentSessions[i].Messages[blkID].Content = qData
			}
			AgentSessions[i].PendingQuestion = nil
			break
		}
	}
	sessionMu.Unlock()
	pub.broadcast(SSEEvent{Type: "block", Subtype: "ask_user", ID: blkID, Content: qData})
	go SaveSessions()
	return "用户回答: " + answer, nil
}

type execSessionKey struct{}

func withExecSession(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, execSessionKey{}, sessionID)
}

func execSessionID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	sessionID, _ := ctx.Value(execSessionKey{}).(string)
	return sessionID
}

// AnswerQuestion 供 HTTP handler 调用，将用户回答写入对应会话的 channel
func AnswerQuestion(sessionID, answer string) bool {
	questionChannelsMu.Lock()
	ch, ok := questionChannels[sessionID]
	if ok {
		delete(questionChannels, sessionID)
	}
	questionChannelsMu.Unlock()
	if ok {
		ch <- answer
		return true
	}
	return false
}

// ========== 路径沙箱 ==========

// getSandboxRoot 返回项目路径作为沙箱根目录（空表示无限制）
func getSandboxRoot(cwd string) string {
	if cwd == "" {
		return ""
	}
	// 确保是绝对路径
	if !filepath.IsAbs(cwd) {
		abs, _ := filepath.Abs(cwd)
		return filepath.Clean(abs)
	}
	return filepath.Clean(cwd)
}

// isPathWithin 检查 child 是否在 parent 目录内（含自身）
func isPathWithin(child, parent string) bool {
	child = filepath.Clean(child)
	parent = filepath.Clean(parent)
	if child == parent {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	// Rel 返回的路径不应以 .. 开头
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

// ========== 工具实现函数 ==========

// listFilesWithDepth 分层列出目录，跳过只有一个文件夹的子目录（内联）
// depth 计算: 每看到「多条目」或「文件+文件夹」才算消耗一级
func listFilesWithDepth(root string, depth int) string {
	if depth <= 0 {
		depth = 1
	}
	var sb strings.Builder
	listFilesDepth(&sb, root, "", depth, 0, 200)
	r := sb.String()
	if r == "" {
		return "(empty)"
	}
	return r
}

func listFilesDepth(sb *strings.Builder, dir, prefix string, maxDepth, curDepth, limit int) {
	if limit <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		sb.WriteString(prefix + "error: " + err.Error() + "\n")
		return
	}
	// 分类
	var dirs, files []os.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e)
		} else {
			files = append(files, e)
		}
	}
	// 输出文件
	for _, f := range files {
		sb.WriteString(prefix + f.Name() + "\n")
		limit--
		if limit <= 0 {
			sb.WriteString(prefix + "... (truncated)\n")
			return
		}
	}
	// 处理子目录
	for _, d := range dirs {
		childPath := filepath.Join(dir, d.Name())
		fullPrefix := prefix + d.Name() + "/"
		if isOnlyDir(childPath) && curDepth+1 < maxDepth {
			// 跳过只有一个文件夹的中间目录，内联
			subPath := getOnlyDir(childPath)
			sb.WriteString(fullPrefix + filepath.Base(subPath) + "/\n")
			if curDepth+2 < maxDepth {
				listFilesDepth(sb, subPath, prefix+d.Name()+"/"+filepath.Base(subPath)+"/", maxDepth, curDepth+2, limit)
			}
		} else if isOnlyDir(childPath) && curDepth+1 >= maxDepth {
			// 达到最大深度，只显示目录名不展开
			subPath := getOnlyDir(childPath)
			sb.WriteString(fullPrefix + filepath.Base(subPath) + "/\n")
		} else if curDepth+1 < maxDepth {
			// 正常展开下一级
			listFilesDepth(sb, childPath, fullPrefix, maxDepth, curDepth+1, limit)
		} else {
			// 达到最大深度，只显示目录名
			sb.WriteString(fullPrefix + "\n")
		}
		limit--
		if limit <= 0 {
			sb.WriteString("... (truncated)\n")
			return
		}
	}
}

// isOnlyDir 目录下是否只有一个子目录且没有文件
func isOnlyDir(p string) bool {
	entries, err := os.ReadDir(p)
	if err != nil {
		return false
	}
	hasFile := false
	dirCount := 0
	for _, e := range entries {
		if e.IsDir() {
			dirCount++
		} else {
			hasFile = true
		}
	}
	return !hasFile && dirCount == 1
}

// getOnlyDir 返回目录下唯一的子目录完整路径
func getOnlyDir(p string) string {
	entries, _ := os.ReadDir(p)
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(p, e.Name())
		}
	}
	return p
}

// readFileLines 按行范围读取文件
func readFileLines(path string, startLine, endLine int) (string, error) {
	if startLine <= 0 {
		startLine = 1
	}
	if endLine-startLine >= 200 {
		endLine = startLine + 199
	}
	f, err := os.Open(path)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	defer f.Close()

	if startLine <= 0 {
		startLine = 1
	}
	scanner := bufio.NewScanner(f)
	var lines []string
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum < startLine {
			continue
		}
		if endLine > 0 && lineNum > endLine {
			break
		}
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "error: " + err.Error(), nil
	}
	r := strings.Join(lines, "\n")
	if len(r) > 20000 {
		r = r[:20000] + "\n... (truncated)"
	}
	return r, nil
}

// doReplaceInFile 在文件中精确替换一处文本
func doReplaceInFile(filePath, oldStr, newStr string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	content := string(data)
	count := strings.Count(content, oldStr)
	if count == 0 {
		return fmt.Sprintf("error: oldString not found in %s", filepath.Base(filePath)), nil
	}
	if count > 1 {
		return fmt.Sprintf("error: oldString matches %d locations (must be unique). Add more context.", count), nil
	}
	newContent := strings.Replace(content, oldStr, newStr, 1)
	if err := os.WriteFile(filePath, []byte(newContent), 0644); err != nil {
		return "error: " + err.Error(), nil
	}
	return "ok: replaced in " + filepath.Base(filePath), nil
}

// doFileSearch 按 glob 模式搜索文件
func doFileSearch(cwd, pattern string, maxResults int) (string, error) {
	// 尝试递归搜索
	fullPattern := filepath.Join(cwd, pattern)
	matches, err := filepath.Glob(fullPattern)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	// 如果简单 glob 没匹配到，尝试递归 walk
	if len(matches) == 0 {
		_ = filepath.Walk(cwd, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				base := filepath.Base(path)
				if base == ".git" || base == "node_modules" || base == "__pycache__" {
					return filepath.SkipDir
				}
				return nil
			}
			matched, _ := filepath.Match(pattern, filepath.Base(path))
			if matched {
				matches = append(matches, path)
			}
			return nil
		})
	}
	if len(matches) == 0 {
		return "(no matches)", nil
	}
	if maxResults <= 0 {
		maxResults = 100
	}
	var sb strings.Builder
	for i, m := range matches {
		if i >= maxResults {
			sb.WriteString(fmt.Sprintf("\n... (%d more)", len(matches)-maxResults))
			break
		}
		rel, _ := filepath.Rel(cwd, m)
		if rel == "" {
			rel = m
		}
		sb.WriteString(rel + "\n")
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// isBinaryExt 返回文件是否为已知的二进制扩展名
func isBinaryExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".bin", ".exe", ".dll", ".so", ".dylib", ".o", ".obj", ".a", ".lib",
		".class", ".jar", ".war", ".ear",
		".zip", ".tar", ".gz", ".bz2", ".xz", ".7z", ".rar", ".zst",
		".png", ".jpg", ".jpeg", ".gif", ".bmp", ".ico", ".webp", ".svgz",
		".mp3", ".mp4", ".avi", ".mov", ".mkv", ".wav", ".flac", ".ogg",
		".ttf", ".otf", ".woff", ".woff2", ".eot",
		".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx",
		".db", ".sqlite", ".sqlite3",
		".wasm", ".bc", ".ll",
		".pak", ".dat", ".nbt", ".mca", ".mcr":
		return true
	}
	return false
}

// doGrepSearch 在文件中按正则搜索
func doGrepSearch(cwd, pattern string, isRegexp bool, includePattern string, maxResults int, includeIgnored bool) (string, error) {
	var re *regexp.Regexp
	if isRegexp {
		var err error
		re, err = regexp.Compile(pattern)
		if err != nil {
			return "error: invalid regex: " + err.Error(), nil
		}
	}
	if maxResults <= 0 {
		maxResults = 50
	}

	var results []string
	filepath.Walk(cwd, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			base := filepath.Base(path)
			if base == ".git" || base == "node_modules" || base == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		if !includeIgnored {
			// 简单跳过 .git 中的文件
			if strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)) {
				return nil
			}
		}
		if includePattern != "" {
			matched, _ := filepath.Match(includePattern, filepath.Base(path))
			if !matched {
				return nil
			}
		}
		if len(results) >= maxResults {
			return filepath.SkipAll
		}
		// 跳过已知二进制扩展名文件
		if isBinaryExt(info.Name()) {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		// 内容级二进制检测：读取前 8KB 检测 null 字节
		head := make([]byte, 8192)
		n, _ := f.Read(head)
		for i := 0; i < n; i++ {
			if head[i] == 0 {
				return nil // 跳过二进制文件
			}
		}
		// 重置到文件开头
		f.Seek(0, io.SeekStart)
		scanner := bufio.NewScanner(f)
		lineNum := 0
		rel, _ := filepath.Rel(cwd, path)
		for scanner.Scan() && len(results) < maxResults {
			lineNum++
			line := scanner.Text()
			matched := false
			if isRegexp {
				matched = re.MatchString(line)
			} else {
				matched = strings.Contains(strings.ToLower(line), strings.ToLower(pattern))
			}
			if matched {
				results = append(results, fmt.Sprintf("%s:%d: %s", rel, lineNum, line))
			}
		}
		return nil
	})
	if len(results) == 0 {
		return "(no matches)", nil
	}
	return strings.Join(results, "\n"), nil
}

// truncate 字符串截断
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ========== Web Fetch ==========

// doFetchWebpage 从指定 URL 获取网页内容，转为纯文本
func doFetchWebpage(urls []string, query string) (string, error) {
	if len(urls) == 0 {
		return "error: urls required", nil
	}

	client := &http.Client{Timeout: 15 * time.Second}
	var results []string

	for _, rawURL := range urls {
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" {
			continue
		}
		req, err := http.NewRequest("GET", rawURL, nil)
		if err != nil {
			results = append(results, fmt.Sprintf("[%s] error: %v", rawURL, err))
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

		resp, err := client.Do(req)
		if err != nil {
			results = append(results, fmt.Sprintf("[%s] error: %v", rawURL, err))
			continue
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024)) // 2MB 限制
		resp.Body.Close()
		if err != nil {
			results = append(results, fmt.Sprintf("[%s] error: %v", rawURL, err))
			continue
		}

		text := htmlToText(string(body))
		// 截断
		if len(text) > 8000 {
			text = text[:8000] + "\n... (truncated)"
		}
		results = append(results, fmt.Sprintf("--- %s (status=%d, size=%d) ---\n%s", rawURL, resp.StatusCode, len(body), text))
	}

	if len(results) == 0 {
		return "(no results)", nil
	}
	return strings.Join(results, "\n\n"), nil
}

// htmlToText 简单的 HTML 到纯文本转换
func htmlToText(html string) string {
	// 移除 script 和 style 标签
	re := regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	str := re.ReplaceAllString(html, "")
	re = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	str = re.ReplaceAllString(str, "")

	// 替换常见标签为换行
	re = regexp.MustCompile(`(?is)<br\s*/?>`)
	str = re.ReplaceAllString(str, "\n")
	re = regexp.MustCompile(`(?is)</?p[^>]*>`)
	str = re.ReplaceAllString(str, "\n")
	re = regexp.MustCompile(`(?is)</?div[^>]*>`)
	str = re.ReplaceAllString(str, "\n")
	re = regexp.MustCompile(`(?is)</?h[1-6][^>]*>`)
	str = re.ReplaceAllString(str, "\n")
	re = regexp.MustCompile(`(?is)</?li[^>]*>`)
	str = re.ReplaceAllString(str, "\n- ")
	re = regexp.MustCompile(`(?is)</?tr[^>]*>`)
	str = re.ReplaceAllString(str, "\n")

	// 移除所有剩余 HTML 标签
	re = regexp.MustCompile(`<[^>]+>`)
	str = re.ReplaceAllString(str, "")

	// 解码 HTML 实体
	str = strings.ReplaceAll(str, "&amp;", "&")
	str = strings.ReplaceAll(str, "&lt;", "<")
	str = strings.ReplaceAll(str, "&gt;", ">")
	str = strings.ReplaceAll(str, "&quot;", "\"")
	str = strings.ReplaceAll(str, "&#39;", "'")
	str = strings.ReplaceAll(str, "&nbsp;", " ")

	// 合并多个空行
	re = regexp.MustCompile(`\n{3,}`)
	str = re.ReplaceAllString(str, "\n\n")

	return strings.TrimSpace(str)
}

// doDownloadFile 从 URL 下载原始文件内容（不转 HTML）
func doDownloadFile(url string) (string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("http status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 500*1024)) // 500KB 限制
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	text := string(body)
	if len(text) > 10000 {
		text = text[:10000] + "\n... (truncated)"
	}
	return fmt.Sprintf("--- %s (size=%d, content-type=%s) ---\n%s",
		url, len(body), resp.Header.Get("Content-Type"), text), nil
}

// ========== 云盘内置工具（scoped to data/drive/agent/<sessionID>/） ==========

func driveAgentDir(ctx context.Context) string {
	sid := execSessionID(ctx)
	if sid == "" {
		return ""
	}
	return filepath.Join("data", "drive", "agent", sid)
}

func driveResolve(ctx context.Context, subPath string) string {
	baseDir := driveAgentDir(ctx)
	if baseDir == "" {
		return ""
	}
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return ""
	}
	full, err := resolveWithinRoot(baseDir, subPath)
	if err != nil {
		return ""
	}
	return full
}

func executeDriveList(ctx context.Context, subPath string) (string, error) {
	p := driveResolve(ctx, subPath)
	if p == "" {
		return "error: path outside agent directory", nil
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	if len(entries) == 0 {
		return "(empty directory)", nil
	}
	var lines []string
	for _, e := range entries {
		info, _ := e.Info()
		if e.IsDir() {
			lines = append(lines, fmt.Sprintf("[DIR]  %s/", e.Name()))
		} else {
			size := ""
			if info != nil && info.Size() > 0 {
				if info.Size() < 1024 {
					size = fmt.Sprintf(" (%dB)", info.Size())
				} else if info.Size() < 1024*1024 {
					size = fmt.Sprintf(" (%.1fKB)", float64(info.Size())/1024)
				} else {
					size = fmt.Sprintf(" (%.1fMB)", float64(info.Size())/(1024*1024))
				}
			}
			lines = append(lines, fmt.Sprintf("[FILE] %s%s", e.Name(), size))
		}
	}
	return strings.Join(lines, "\n"), nil
}

func resolveWithinRoot(root, subPath string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	cleaned := filepath.Clean(filepath.FromSlash(strings.TrimSpace(subPath)))
	if filepath.IsAbs(cleaned) || filepath.VolumeName(cleaned) != "" {
		return "", fmt.Errorf("absolute paths are not allowed")
	}
	full := filepath.Join(rootAbs, cleaned)
	if !isPathWithin(full, rootAbs) {
		return "", fmt.Errorf("path escapes root")
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	existing := full
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("no existing parent")
		}
		existing = parent
	}
	realExisting, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if !isPathWithin(realExisting, rootReal) {
		return "", fmt.Errorf("path resolves outside root")
	}
	return full, nil
}

func executeDriveRead(ctx context.Context, subPath string) (string, error) {
	p := driveResolve(ctx, subPath)
	if p == "" {
		return "error: path outside agent directory", nil
	}
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return "error: not found or is directory", nil
	}
	if info.Size() > 1024*1024 {
		return "error: file too large (max 1MB)", nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	return string(data), nil
}

func executeDriveWrite(ctx context.Context, subPath, content string) (string, error) {
	p := driveResolve(ctx, subPath)
	if p == "" {
		return "error: path outside agent directory", nil
	}
	os.MkdirAll(filepath.Dir(p), 0755)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		return "error: " + err.Error(), nil
	}
	return fmt.Sprintf("ok: wrote %d bytes to %s", len(content), filepath.Base(p)), nil
}

func executeDriveMkdir(ctx context.Context, subPath string) (string, error) {
	p := driveResolve(ctx, subPath)
	if p == "" {
		return "error: path outside agent directory", nil
	}
	if err := os.MkdirAll(p, 0755); err != nil {
		return "error: " + err.Error(), nil
	}
	return "ok: created " + filepath.Base(p), nil
}

func executeDriveDelete(ctx context.Context, subPath string) (string, error) {
	p := driveResolve(ctx, subPath)
	if p == "" {
		return "error: path outside agent directory", nil
	}
	dir := driveAgentDir(ctx)
	absDir, _ := filepath.Abs(dir)
	if filepath.Clean(p) == filepath.Clean(absDir) {
		return "error: cannot delete agent root", nil
	}
	if err := os.RemoveAll(p); err != nil {
		return "error: " + err.Error(), nil
	}
	return "ok: deleted " + filepath.Base(p), nil
}
