package agent

import (
	"log"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadAll 从 YAML 文件加载 AI 提供商、MCP 服务器和 Agent 列表
func LoadAll() {
	InvalidateLLMCache()
	loadAI("config/ai.yml")
	loadMCPs("config/mcps.yml")
	loadAgents("config/agents.yml")
	LoadSessions() // 恢复历史会话

	// 启动外部 MCP 服务器并发现工具
	InitAllMCPs()

	log.Printf("[Agent] Agent 系统: %d 个可用模型, %d 个 MCP 服务器, %d 个 Agent 类型, %d 个历史会话",
		len(Models), len(MCPServers), len(AgentDefs), len(AgentSessions))
}

func loadYAML(path string, v interface{}) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[!] 无法读取 %s: %v", path, err)
		return
	}
	if err := yaml.Unmarshal(data, v); err != nil {
		log.Printf("[!] 解析 %s 失败: %v", path, err)
	}
}

func loadAI(path string) {
	var cfg AIYAML
	loadYAML(path, &cfg)

	// 扁平化：把 endpoint + model 合并为 AIModelInfo
	Models = nil
	ModelIndex = make(map[string]*AIModelInfo)
	for _, ep := range cfg.Providers {
		apiKey := resolveEnv(ep.APIKey)
		for _, m := range ep.Models {
			key := ep.ID + "/" + m.ID
			ctxLen := m.MaxContextLength
			if ctxLen <= 0 {
				ctxLen = defaultMaxContextLength(m.ID, ep.Type)
			}
			info := AIModelInfo{
				Key:              key,
				Name:             m.Name,
				ProviderName:     ep.Name,
				Type:             ep.Type,
				BaseURL:          ep.BaseURL,
				APIKey:           apiKey,
				Model:            m.ID, // 传给 API 的 model 名
				MaxTokens:        m.MaxTokens,
				MaxContextLength: ctxLen,
				ThinkingEnabled:  m.ThinkingEnabled,
				ReasoningEffort:  m.ReasoningEffort,
			}
			Models = append(Models, info)
			ModelIndex[key] = &info
		}
	}

	if cfg.DefaultModel != "" {
		log.Printf("[Agent] 默认模型: %s", cfg.DefaultModel)
	}
}

func loadMCPs(path string) {
	var cfg MCPYAML
	loadYAML(path, &cfg)
	for i := range cfg.Servers {
		for k, v := range cfg.Servers[i].Env {
			cfg.Servers[i].Env[k] = resolveEnv(v)
		}
	}
	MCPServers = cfg.Servers
}

func loadAgents(path string) {
	var cfg AgentYAML
	loadYAML(path, &cfg)
	// Reserve the built-in read-only role even if a local config uses this ID.
	filtered := cfg.Agents[:0]
	for _, def := range cfg.Agents {
		if def.ID != IssuePreprocessorID {
			filtered = append(filtered, def)
		}
	}
	cfg.Agents = append(filtered, issuePreprocessorDef())
	for i := range cfg.Agents {
		if cfg.Agents[i].MaxInstances <= 0 {
			cfg.Agents[i].MaxInstances = 3
		}
		// 加载提示词
		if cfg.Agents[i].AgentMDFile != "" {
			data, err := os.ReadFile(cfg.Agents[i].AgentMDFile)
			if err != nil {
				log.Printf("[!] 无法读取提示词文件 %s: %v", cfg.Agents[i].AgentMDFile, err)
			} else {
				cfg.Agents[i].AgentMD = string(data)
				log.Printf("[Agent] Agent %s: 已加载提示词 %s (%d 字符)", cfg.Agents[i].ID, cfg.Agents[i].AgentMDFile, len(cfg.Agents[i].AgentMD))
			}
		}
		// 检查提示词是否包含工具占位符
		cfg.Agents[i].PromptWarnings = checkPromptToolPlaceholders(&cfg.Agents[i])
	}
	AgentDefs = cfg.Agents
}

// resolveEnv 解析 ${VAR_NAME} 格式的环境变量
func resolveEnv(s string) string {
	if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
		name := s[2 : len(s)-1]
		return os.Getenv(name)
	}
	return s
}

// DefaultModelKey 返回默认模型 key（provider_id/model_id）
func DefaultModelKey() string {
	var cfg AIYAML
	data, err := os.ReadFile("config/ai.yml")
	if err != nil {
		return ""
	}
	yaml.Unmarshal(data, &cfg)
	if cfg.DefaultModel != "" {
		return cfg.DefaultModel
	}
	if len(Models) > 0 {
		return Models[0].Key
	}
	return ""
}
