package agent

import (
	"encoding/json"
	"strings"
	"sync"
)

// ========== AI 提供商配置 ==========

// AIEndpoint 一个 API 端点（base_url + api_key），可承载多个模型
type AIEndpoint struct {
	ID      string    `yaml:"id" json:"id"`
	Name    string    `yaml:"name" json:"name"`
	Type    string    `yaml:"type" json:"type"` // openai / openai-compatible / ollama / deepseek
	BaseURL string    `yaml:"base_url" json:"base_url"`
	APIKey  string    `yaml:"api_key" json:"-"`
	Models  []AIModel `yaml:"models" json:"models"`
}

// AIModel 单个模型配置
type AIModel struct {
	ID               string `yaml:"id" json:"id"`
	Name             string `yaml:"name" json:"name"`
	MaxTokens        int    `yaml:"max_tokens" json:"max_tokens"`
	MaxContextLength int    `yaml:"max_context_length" json:"max_context_length"` // 最大上下文长度 (tokens)，0=使用默认
	ThinkingEnabled  bool   `yaml:"thinking_enabled" json:"thinking_enabled,omitempty"`
	ReasoningEffort  string `yaml:"reasoning_effort" json:"reasoning_effort,omitempty"`
}

// AIModelInfo 扁平化后的运行时信息（Endpoint + Model 合并）
type AIModelInfo struct {
	Key              string `json:"id"`            // "provider_id/model_id"
	Name             string `json:"name"`          // 模型显示名称
	ProviderName     string `json:"provider_name"` // Provider 显示名称
	Type             string `json:"type"`          // openai / deepseek / ollama
	BaseURL          string `json:"base_url"`
	APIKey           string `json:"-"`
	Model            string `json:"model"` // 传给 API 的 model 名
	MaxTokens        int    `json:"max_tokens"`
	MaxContextLength int    `json:"max_context_length"` // 最大上下文长度 (tokens)
	ThinkingEnabled  bool   `json:"thinking_enabled,omitempty"`
	ReasoningEffort  string `json:"reasoning_effort,omitempty"`
}

func (m AIModelInfo) ReasonEffort() string {
	if m.ReasoningEffort == "" {
		return "high"
	}
	return m.ReasoningEffort
}

func (m AIModelInfo) IsDeepSeek() bool {
	return m.Type == "deepseek" || strings.Contains(m.BaseURL, "deepseek")
}

// defaultMaxContextLength 根据模型名推断默认上下文窗口
func defaultMaxContextLength(modelID, providerType string) int {
	id := strings.ToLower(modelID)
	if strings.Contains(id, "gpt-4") {
		return 128000
	}
	if strings.Contains(id, "gpt-3.5") {
		return 16385
	}
	if strings.Contains(id, "deepseek") || providerType == "deepseek" {
		return 131072 // DeepSeek 默认 128K
	}
	if strings.Contains(id, "claude") {
		return 200000
	}
	// 默认 128K（大多数现代模型）
	return 128000
}

// ========== MCP 服务器 ==========

// MCPToolInfo MCP 工具元数据
type MCPToolInfo struct {
	ID          string `yaml:"id" json:"id"`
	Description string `yaml:"description" json:"description"`
}

type MCPServer struct {
	ID          string            `yaml:"id" json:"id"`
	Name        string            `yaml:"name" json:"name"`
	Type        string            `yaml:"type" json:"type"` // stdio / streamableHttp / sse
	Command     string            `yaml:"command" json:"command,omitempty"`
	Args        []string          `yaml:"args" json:"args,omitempty"`
	URL         string            `yaml:"url" json:"url,omitempty"`
	Env         map[string]string `yaml:"env" json:"-"`
	Description string            `yaml:"description" json:"description"`
	Tools       []MCPToolInfo     `yaml:"tools" json:"tools,omitempty"`
	Status      string            `yaml:"status" json:"status"` // ok / error / ""
	StatusMsg   string            `yaml:"status_msg" json:"status_msg"`
}

// IsSelfHosted 是否有 command → 自托管启动
func (s *MCPServer) IsSelfHosted() bool {
	return s.Command != ""
}

// ========== Agent 定义 ==========

// BuiltinToolInfo 单个内置工具元数据
type BuiltinToolInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// BuiltinProvider 内置工具提供者（一组工具的容器，类似一个虚拟 MCP）
type BuiltinProvider struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Tools       []BuiltinToolInfo `json:"tools"`
}

type AgentDef struct {
	ID             string   `yaml:"id" json:"id"`
	Name           string   `yaml:"name" json:"name"`
	Description    string   `yaml:"description" json:"description"`
	MaxInstances   int      `yaml:"max_instances" json:"max_instances"`
	MaxRounds      int      `yaml:"max_rounds" json:"max_rounds"` // AI 最大调用轮次，默认 20
	Temperature    float64  `yaml:"temperature" json:"temperature"`
	SubAgents      []string `yaml:"sub_agents" json:"sub_agents"` // 可调用的子 Agent ID 列表
	MCPs           []string `yaml:"mcps" json:"mcps"`             // 工具来源（内置提供者 + 外部 MCP 服务器 ID）
	AgentMDFile    string   `yaml:"agent_md_file" json:"agent_md_file"`
	AgentMD        string   `yaml:"agent_md" json:"agent_md"`
	PromptWarnings []string `yaml:"-" json:"prompt_warnings,omitempty"` // 提示词检查警告（运行时填充）
}

// ========== Agent 实例（运行时） ==========

type AgentInstance struct {
	ID       string `json:"id"`
	AgentID  string `json:"agent_id"`
	Name     string `json:"name"`
	Provider string `json:"provider"` // 使用的 AI 提供商 ID
	Task     string `json:"task"`     // 初始任务或描述
}

// ========== 对话消息 ==========

type ChatMessage struct {
	Type    string      `json:"type"`
	Content interface{} `json:"content"`
	Tool    string      `json:"tool,omitempty"`
	Tokens  int         `json:"tokens,omitempty"`
	Seq     int         `json:"seq,omitempty"` // 消息在会话中的序号（DB 持久化用）
}

// UnmarshalJSON 兼容旧版 role 字段和 string content
func (m *ChatMessage) UnmarshalJSON(data []byte) error {
	type alias struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Tool    string          `json:"tool,omitempty"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	if a.Type == "" && a.Role != "" {
		a.Type = a.Role // 旧版 role → type
	}
	m.Type = a.Type
	m.Tool = a.Tool
	// content: 优先当字符串，否则当 JSON 对象
	if len(a.Content) == 0 {
		m.Content = ""
	} else if a.Content[0] == '"' {
		var s string
		json.Unmarshal(a.Content, &s)
		m.Content = s
	} else {
		var obj map[string]interface{}
		if err := json.Unmarshal(a.Content, &obj); err == nil {
			m.Content = obj
		} else {
			m.Content = string(a.Content)
		}
	}
	return nil
}

// ========== 对话会话 ==========

type ChatSession struct {
	ID              string           `json:"id"`
	Agent           string           `json:"agent"` // AgentDef ID
	InstanceID      string           `json:"instance_id"`
	Provider        string           `json:"provider"`
	Project         string           `json:"project"`      // 绑定的项目名（可选）
	ProjectPath     string           `json:"project_path"` // 项目在本地磁盘的绝对路径
	Task            string           `json:"task"`
	Messages        []ChatMessage    `json:"messages"`
	CreatedAt       string           `json:"created_at"`
	UpdatedAt       string           `json:"updated_at"`                 // 最后修改时间
	Archived        bool             `json:"archived,omitempty"`         // 是否已归档
	Running         bool             `json:"running"`                    // 是否正在执行
	Todos           []TodoItem       `json:"todos,omitempty"`            // 当前会话的 TODO 列表
	PendingQuestion *PendingQuestion `json:"pending_question,omitempty"` // 待用户回答的问题（持久化）
	PendingApproval *PendingApproval `json:"pending_approval,omitempty"`
	BypassApproval  bool             `json:"bypass_approval,omitempty"`
	LimitExceeded   bool             `json:"limit_exceeded,omitempty"` // 达到最大轮次，前端应显示"继续推理"按钮
	Title           string           `json:"title,omitempty"`          // 自动生成的对话标题
	StopCh          chan struct{}    `json:"-"`
	CancelFunc      func()           `json:"-"`
}

// TodoItem 待办事项
type TodoItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"` // not-started / in-progress / completed
}

// PendingApproval 待审批的危险操作
type PendingApproval struct {
	ToolName  string `json:"tool_name"`
	Summary   string `json:"summary"`
	Detail    string `json:"detail"`
	WarnLevel string `json:"warn_level"`
}

// ========== YAML 配置结构 ==========

type AIYAML struct {
	Providers    []AIEndpoint `yaml:"providers"`
	DefaultModel string       `yaml:"default_model"`
}

type MCPYAML struct {
	Servers []MCPServer `yaml:"servers"`
}

type AgentYAML struct {
	Agents []AgentDef `yaml:"agents"`
}

// ========== 全局状态 ==========

var (
	Models        []AIModelInfo                   // 扁平化后的所有可用模型
	ModelIndex    = make(map[string]*AIModelInfo) // key -> *AIModelInfo
	MCPServers    []MCPServer
	AgentDefs     []AgentDef
	AgentSessions []*ChatSession

	sessionMu      sync.Mutex
	agentMu        sync.RWMutex                     // 保护 agentInstances 的并发读写
	agentInstances = make(map[string]*agentRuntime) // sessionID → LLM runtime

	// ask_question 暂停机制
	questionChannels   = make(map[string]chan string) // sessionID → answer channel
	questionChannelsMu sync.Mutex

	// 审批机制
	approvalChannels   = make(map[string]chan bool) // sessionID → approved channel
	approvalChannelsMu sync.Mutex

	// AskQuestionCallback 由 HTTP handler 设置，用于 ask_user 工具时暂停并等待用户输入
	AskQuestionCallback func(sessionID string, question string, header string, multiSelect bool, allowFreeform bool, options []AskOption) string

	// ApprovalCallback 由 HTTP handler 设置，用于危险工具审批
	// warnLevel: ""=安全, "warn:..."=有风险, "block:..."=严重危险
	// 返回 true 表示通过，false 表示拒绝
	ApprovalCallback func(sessionID string, toolName string, summary string, detail string, warnLevel string) bool

	// 项目路径解析
	AgentProjectPaths []string                       // 项目存放路径列表（从 config 注入）
	ModMetaLookup     func(name string) *ProjectMeta // 按项目名查找 meta（从 main 注入）
)

// AskOption ask_user 工具的选择项
type AskOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// PendingQuestion 持久化的待回答问题（服务器重启后可恢复）
type PendingQuestion struct {
	Header             string      `json:"header"`
	Question           string      `json:"question"`
	MultiSelect        bool        `json:"multiSelect"`
	AllowFreeformInput bool        `json:"allowFreeformInput"`
	Options            []AskOption `json:"options"`
}

// ========== 辅助 ==========

func FindModel(key string) *AIModelInfo {
	return ModelIndex[key]
}

func FindAgentDef(id string) *AgentDef {
	for i := range AgentDefs {
		if AgentDefs[i].ID == id {
			return &AgentDefs[i]
		}
	}
	return nil
}

// GetSubAgents 返回 Agent 的子 Agent 定义列表
func (a *AgentDef) GetSubAgents() []*AgentDef {
	var result []*AgentDef
	for _, subID := range a.SubAgents {
		if def := FindAgentDef(subID); def != nil {
			result = append(result, def)
		}
	}
	return result
}

func FindMCPServer(id string) *MCPServer {
	for i := range MCPServers {
		if MCPServers[i].ID == id {
			return &MCPServers[i]
		}
	}
	return nil
}

func InstanceCount(agentID string) int {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	count := 0
	for _, s := range AgentSessions {
		if s.Agent == agentID && s.Running {
			count++
		}
	}
	return count
}
