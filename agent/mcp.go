package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ========== MCP JSON-RPC 协议 ==========

type mcpRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type mcpToolsListResult struct {
	Tools []mcpToolDef `json:"tools"`
}

// mcpRuntime 单个 MCP 服务器的运行时状态（stdio 类型）
type mcpRuntime struct {
	ServerID string
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	scanner  *bufio.Scanner
	mu       sync.Mutex
	ready    bool
}

// mcpHttpRuntime HTTP/SSE 类型的 MCP 运行时
type mcpHttpRuntime struct {
	ServerID  string
	URL       string
	SessionID string // initialize 返回的 session ID
	client    *http.Client
	mu        sync.Mutex
	ready     bool
}

var (
	mcpRuntimes     = make(map[string]*mcpRuntime)
	mcpHttpRuntimes = make(map[string]*mcpHttpRuntime)
	mcpRuntimeMu    sync.Mutex
)

// ========== 环境检测 ==========

// CheckRuntimes 检测 Node.js 和 Python 是否可用
func CheckRuntimes() {
	log.Printf("[MCP] ====== 运行时环境检测 ======")

	nodeVer, hasNode := tryCommand("node", "--version")
	if hasNode {
		log.Printf("[MCP] ✓ Node.js: %s", strings.TrimSpace(nodeVer))
	} else {
		log.Printf("[MCP] ✗ Node.js 未安装或不在 PATH 中。请安装 Node.js 以使用外部 MCP 服务器。")
	}

	pythonVer, hasPython := tryCommand("python3", "--version")
	if !hasPython {
		pythonVer, hasPython = tryCommand("python", "--version")
	}
	if hasPython {
		log.Printf("[MCP] ✓ Python: %s", strings.TrimSpace(pythonVer))
	} else {
		log.Printf("[MCP] ✗ Python 未安装或不在 PATH 中。uvx/uv 需要 Python 环境。")
	}

	npxVer, hasNpx := tryCommand("npx", "--version")
	if hasNpx {
		log.Printf("[MCP] ✓ npx: %s", strings.TrimSpace(npxVer))
	}

	uvxVer, hasUvx := tryCommand("uvx", "--version")
	if hasUvx {
		log.Printf("[MCP] ✓ uvx: %s", strings.TrimSpace(uvxVer))
	}

	log.Printf("[MCP] =============================")
}

func tryCommand(name string, args ...string) (string, bool) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// ========== MCP 缓存 ==========

const mcpCacheFile = "cache/mcp_cache.json"

// mcpCacheEntry 缓存条目
type mcpCacheEntry struct {
	ID        string        `json:"id"`
	Tools     []MCPToolInfo `json:"tools"`
	Status    string        `json:"status"`
	StatusMsg string        `json:"status_msg"`
}

// SaveMCPCache 将 MCP 服务器状态保存到磁盘缓存
func SaveMCPCache() {
	entries := make([]mcpCacheEntry, 0, len(MCPServers))
	for _, srv := range MCPServers {
		entries = append(entries, mcpCacheEntry{
			ID:        srv.ID,
			Tools:     srv.Tools,
			Status:    srv.Status,
			StatusMsg: srv.StatusMsg,
		})
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		log.Printf("[MCP] 缓存序列化失败: %v", err)
		return
	}
	if err := os.WriteFile(mcpCacheFile, data, 0644); err != nil {
		log.Printf("[MCP] 缓存写入失败: %v", err)
		return
	}
	log.Printf("[MCP] 缓存已保存至 %s (%d 个服务器)", mcpCacheFile, len(entries))
}

// LoadMCPCache 从磁盘缓存恢复 MCP 工具和状态
func LoadMCPCache() bool {
	data, err := os.ReadFile(mcpCacheFile)
	if err != nil {
		return false
	}
	var entries []mcpCacheEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		log.Printf("[MCP] 缓存解析失败: %v", err)
		return false
	}
	if len(entries) == 0 {
		return false
	}
	// 构建 ID → entry 映射
	cacheMap := make(map[string]mcpCacheEntry, len(entries))
	for _, e := range entries {
		cacheMap[e.ID] = e
	}
	// 恢复状态
	restored := 0
	for i := range MCPServers {
		srv := &MCPServers[i]
		if entry, ok := cacheMap[srv.ID]; ok {
			srv.Tools = entry.Tools
			srv.Status = entry.Status
			srv.StatusMsg = entry.StatusMsg
			restored++
		}
	}
	log.Printf("[MCP] 缓存加载: 恢复了 %d/%d 个服务器", restored, len(MCPServers))
	return true
}

// ========== MCP 生命周期 ==========

// RefreshAllMCPs 重新检测并启动所有 MCP 服务器（强制重新发现，忽略缓存）
func RefreshAllMCPs() {
	// 先关闭旧的
	for id, rt := range mcpRuntimes {
		if rt.cmd != nil && rt.cmd.Process != nil {
			rt.cmd.Process.Kill()
		}
		delete(mcpRuntimes, id)
	}
	for id := range mcpHttpRuntimes {
		delete(mcpHttpRuntimes, id)
	}
	// 重新加载配置（清空已有 tools/status）
	loadMCPs("config/mcps.yml")
	// 重新初始化（强制逐台检测）
	initAllMCPsInternal(true)
	SaveMCPCache()
}

// InitAllMCPs 启动所有外部 MCP 服务器并发现工具（优先使用缓存）
func InitAllMCPs() {
	// 尝试从缓存恢复
	cached := LoadMCPCache()
	// 初始化：cached 时跳过 ok 的（节省时间），未缓存时逐台检测
	initAllMCPsInternal(cached)
	SaveMCPCache()
}

// initAllMCPsInternal 内部初始化逻辑
// skipOK: true 跳过 status==ok 的服务器（用于缓存恢复后补检测失败的）
func initAllMCPsInternal(skipOK bool) {
	CheckRuntimes()

	for i := range MCPServers {
		srv := &MCPServers[i]

		switch srv.Type {
		case "stdio":
			if srv.Command == "" {
				continue
			}
			// 缓存恢复后跳过已成功的服务器
			if skipOK && srv.Status == "ok" {
				continue
			}
			initStdioMCP(srv)

		case "streamableHttp", "sse":
			// 有 command → 自托管；纯 URL → 直接连接
			if srv.IsSelfHosted() {
				if skipOK && srv.Status == "ok" {
					createHttpRuntimeFromCache(srv)
					continue
				}
				initHttpMCP(srv)
			} else if srv.URL != "" {
				if skipOK && srv.Status == "ok" {
					createHttpRuntimeFromCache(srv)
					continue
				}
				initHttpMCP(srv)
			} else {
				srv.Status = "error"
				srv.StatusMsg = "需要 url 或 command (自托管)"
			}
		}
	}

	httpCount := len(mcpHttpRuntimes)
	log.Printf("[MCP] 服务器初始化完成: %d stdio + %d http 活跃", len(mcpRuntimes), httpCount)
}

// initStdioMCP 初始化 stdio 类型 MCP 服务器
func initStdioMCP(srv *MCPServer) {
	log.Printf("[MCP] 正在启动: %s (%s %s)", srv.Name, srv.Command, strings.Join(srv.Args, " "))

	rt, err := launchMCP(srv)
	if err != nil {
		log.Printf("[MCP] ✗ 启动失败 %s: %v", srv.ID, err)
		srv.Status = "error"
		srv.StatusMsg = err.Error()
		return
	}

	tools := discoverToolsWithTimeout(rt, 30*time.Second)
	if tools == nil {
		return // 已在内部设置 status
	}

	srv.Tools = tools
	srv.Status = "ok"
	srv.StatusMsg = fmt.Sprintf("%d tools", len(tools))
	log.Printf("[MCP] ✓ %s: 发现 %d 个工具: %v", srv.Name, len(tools), toolNames(tools))

	mcpRuntimeMu.Lock()
	mcpRuntimes[srv.ID] = rt
	mcpRuntimeMu.Unlock()
}

// discoverToolsWithTimeout 带超时的工具发现
func discoverToolsWithTimeout(rt *mcpRuntime, timeout time.Duration) []MCPToolInfo {
	toolCh := make(chan struct {
		tools []MCPToolInfo
		err   error
	}, 1)
	go func() {
		t, e := discoverTools(rt)
		toolCh <- struct {
			tools []MCPToolInfo
			err   error
		}{t, e}
	}()
	select {
	case res := <-toolCh:
		if res.err != nil {
			log.Printf("[MCP] ✗ 发现工具失败: %v", res.err)
		}
		return res.tools
	case <-time.After(timeout):
		log.Printf("[MCP] ✗ 发现工具超时 (after %v)", timeout)
		return nil
	}
}

// createHttpRuntimeFromCache 从缓存创建轻量 HTTP runtime（无需重新发现工具）
func createHttpRuntimeFromCache(srv *MCPServer) {
	mcpRuntimeMu.Lock()
	mcpHttpRuntimes[srv.ID] = &mcpHttpRuntime{
		ServerID: srv.ID,
		URL:      strings.TrimRight(srv.URL, "/"),
		client:   &http.Client{Timeout: 30 * time.Second},
		ready:    true,
	}
	mcpRuntimeMu.Unlock()
}

// initHttpMCP 初始化 streamableHttp / sse 类型 MCP 服务器
// 有 command → 自托管启动进程后连接 url；纯 URL → 直接连接
func initHttpMCP(srv *MCPServer) {
	// 自托管：先启动进程
	if srv.IsSelfHosted() {
		log.Printf("[MCP] 自动启动: %s (%s %s)", srv.Name, srv.Command, strings.Join(srv.Args, " "))

		rt, err := launchMCP(srv)
		if err != nil {
			log.Printf("[MCP] ✗ 启动失败 %s: %v", srv.ID, err)
			srv.Status = "error"
			srv.StatusMsg = err.Error()
			return
		}
		mcpRuntimeMu.Lock()
		mcpRuntimes[srv.ID] = rt
		mcpRuntimeMu.Unlock()

		// 等待 HTTP 服务就绪
		if !waitForHTTP(srv.URL, 10*time.Second) {
			srv.Status = "error"
			srv.StatusMsg = fmt.Sprintf("HTTP 服务启动超时: %s", srv.URL)
			log.Printf("[MCP] ✗ 启动超时 %s: %s", srv.ID, srv.URL)
			return
		}
	}

	url := srv.URL
	if url == "" {
		srv.Status = "error"
		srv.StatusMsg = "URL is required for " + srv.Type
		return
	}

	log.Printf("[MCP] 正在连接: %s (%s)", srv.Name, url)

	client := &http.Client{Timeout: 30 * time.Second}
	hrt := &mcpHttpRuntime{
		ServerID: srv.ID,
		URL:      strings.TrimRight(url, "/"),
		client:   client,
		ready:    true,
	}

	// 工具发现
	tools, err := discoverHttpTools(hrt)
	if err != nil {
		log.Printf("[MCP] ✗ 发现工具失败 %s: %v", srv.ID, err)
		srv.Status = "error"
		srv.StatusMsg = err.Error()
		return
	}

	srv.Tools = tools
	srv.Status = "ok"
	srv.StatusMsg = fmt.Sprintf("%d tools", len(tools))
	log.Printf("[MCP] ✓ %s: 发现 %d 个工具: %v", srv.Name, len(tools), toolNames(tools))

	mcpRuntimeMu.Lock()
	mcpHttpRuntimes[srv.ID] = hrt
	// 同时更新缓存 URL，方便后续刷新跳过 auto 启动
	mcpRuntimeMu.Unlock()
}

// waitForHTTP 轮询等待 HTTP 服务就绪
func waitForHTTP(url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Head(url)
		if err == nil {
			resp.Body.Close()
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func launchMCP(srv *MCPServer) (*mcpRuntime, error) {
	cmd := exec.Command(srv.Command, srv.Args...)
	if len(srv.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range srv.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return &mcpRuntime{
		ServerID: srv.ID,
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		scanner:  bufio.NewScanner(stdout),
	}, nil
}

func discoverTools(rt *mcpRuntime) ([]MCPToolInfo, error) {
	// 1. initialize
	initParams := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]string{
			"name":    "modlist-dashboard",
			"version": "1.0.0",
		},
	}
	if _, err := rt.sendRequest("initialize", initParams); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}

	// 2. tools/list
	resp, err := rt.sendRequest("tools/list", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}

	var list mcpToolsListResult
	if err := json.Unmarshal(resp, &list); err != nil {
		return nil, fmt.Errorf("parse tools: %w", err)
	}

	var tools []MCPToolInfo
	for _, t := range list.Tools {
		tools = append(tools, MCPToolInfo{
			ID:          t.Name,
			Description: t.Description,
		})
	}
	return tools, nil
}

func (rt *mcpRuntime) sendRequest(method string, params interface{}) (json.RawMessage, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	req := mcpRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	}
	data, _ := json.Marshal(req)
	data = append(data, '\n')

	if _, err := rt.stdin.Write(data); err != nil {
		return nil, err
	}

	// 使用共享 scanner 读取响应
	if !rt.scanner.Scan() {
		return nil, fmt.Errorf("no response from MCP server")
	}
	line := rt.scanner.Text()

	var resp mcpResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w (raw: %s)", err, line)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

// ========== HTTP MCP 传输 ==========

func discoverHttpTools(hrt *mcpHttpRuntime) ([]MCPToolInfo, error) {
	// 1. initialize — HTTP 响应可能带 Mcp-Session-Id header
	initParams := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]string{
			"name":    "modlist-dashboard",
			"version": "1.0.0",
		},
	}
	initResp, err := hrt.doRequest("initialize", initParams)
	if err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	// 检查并保存 session ID
	if initResp.sessionID != "" {
		hrt.SessionID = initResp.sessionID
		log.Printf("[MCP] %s: session established (%s)", hrt.ServerID, hrt.SessionID)
	}

	// 2. tools/list
	toolsResp, err := hrt.doRequest("tools/list", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}

	var list mcpToolsListResult
	if err := json.Unmarshal(toolsResp.result, &list); err != nil {
		return nil, fmt.Errorf("parse tools: %w", err)
	}

	var tools []MCPToolInfo
	for _, t := range list.Tools {
		tools = append(tools, MCPToolInfo{
			ID:          t.Name,
			Description: t.Description,
		})
	}
	return tools, nil
}

type httpMCPResponse struct {
	result    json.RawMessage
	sessionID string
}

// doRequest 发送 HTTP POST JSON-RPC 请求，返回结果和 session ID
func (hrt *mcpHttpRuntime) doRequest(method string, params interface{}) (*httpMCPResponse, error) {
	hrt.mu.Lock()
	defer hrt.mu.Unlock()

	req := mcpRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	}
	body, _ := json.Marshal(req)

	httpReq, err := http.NewRequest("POST", hrt.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	// 如果有 session，带上
	if hrt.SessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", hrt.SessionID)
	}

	resp, err := hrt.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	// 提取 session ID（initialize 或任何响应都可能带）
	sessionID := resp.Header.Get("Mcp-Session-Id")

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("http status %d: %s", resp.StatusCode, string(b))
	}

	// 处理 SSE (text/event-stream) 响应
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") {
		result, err := parseSSEResponse(resp.Body)
		if err != nil {
			return nil, err
		}
		return &httpMCPResponse{result: result, sessionID: sessionID}, nil
	}

	// 标准 JSON 响应
	var mcpResp mcpResponse
	if err := json.NewDecoder(resp.Body).Decode(&mcpResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if mcpResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", mcpResp.Error.Code, mcpResp.Error.Message)
	}
	return &httpMCPResponse{result: mcpResp.Result, sessionID: sessionID}, nil
}

// sendRequest 通过 HTTP POST 发送 JSON-RPC 请求（旧接口，工具调用用）
func (hrt *mcpHttpRuntime) sendRequest(method string, params interface{}) (json.RawMessage, error) {
	resp, err := hrt.doRequest(method, params)
	if err != nil {
		return nil, err
	}
	return resp.result, nil
}

// parseSSEResponse 解析 SSE 流响应
func parseSSEResponse(body io.Reader) (json.RawMessage, error) {
	scanner := bufio.NewScanner(body)
	var dataLines []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		}
	}
	if len(dataLines) == 0 {
		return nil, fmt.Errorf("no data in SSE response")
	}
	data := strings.Join(dataLines, "\n")

	var mcpResp mcpResponse
	if err := json.Unmarshal([]byte(data), &mcpResp); err != nil {
		return nil, fmt.Errorf("parse SSE data: %w", err)
	}
	if mcpResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", mcpResp.Error.Code, mcpResp.Error.Message)
	}
	return mcpResp.Result, nil
}

// CallMCPTool 调用外部 MCP 服务器的工具
func CallMCPTool(mcpID, toolName string, argsJSON string) (string, error) {
	mcpRuntimeMu.Lock()
	rt := mcpRuntimes[mcpID]
	hrt := mcpHttpRuntimes[mcpID]
	mcpRuntimeMu.Unlock()

	var args map[string]interface{}
	json.Unmarshal([]byte(argsJSON), &args)

	callParams := map[string]interface{}{
		"name":      toolName,
		"arguments": args,
	}

	req := mcpRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/call",
		Params:  callParams,
	}

	// stdio 传输
	if rt != nil {
		rt.mu.Lock()
		data, _ := json.Marshal(req)
		data = append(data, '\n')
		_, writeErr := rt.stdin.Write(data)

		var line string
		if writeErr == nil && rt.scanner.Scan() {
			line = rt.scanner.Text()
		}
		rt.mu.Unlock()

		if writeErr != nil {
			return "", writeErr
		}
		if line == "" {
			return "", fmt.Errorf("no response from MCP server")
		}

		var resp mcpResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			return fmt.Sprintf("(raw) %s", line), nil
		}
		if resp.Error != nil {
			return "", fmt.Errorf("MCP error: %s", resp.Error.Message)
		}
		pretty, _ := json.MarshalIndent(resp.Result, "", "  ")
		return string(pretty), nil
	}

	// HTTP 传输
	if hrt != nil {
		result, err := hrt.sendRequest("tools/call", callParams)
		if err != nil {
			return "", err
		}
		pretty, _ := json.MarshalIndent(result, "", "  ")
		return string(pretty), nil
	}

	return "", fmt.Errorf("MCP server %s is not running", mcpID)
}

// ========== 辅助 ==========

// MCPTool MCP 工具摘要
type MCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ServerID    string `json:"server_id"`
}

func toolNames(tools []MCPToolInfo) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.ID
	}
	return names
}

// ListActiveTools 返回当前配置下实际生效的工具列表
func ListActiveTools() []MCPTool {
	var tools []MCPTool
	for _, srv := range MCPServers {
		for _, t := range srv.Tools {
			tools = append(tools, MCPTool{
				Name:        t.ID,
				Description: t.Description,
				ServerID:    srv.ID,
			})
		}
	}
	return tools
}

// ConnectMCP 连接 MCP 服务器
func ConnectMCP(serverID string) error {
	srv := FindMCPServer(serverID)
	if srv == nil {
		return nil
	}
	if len(srv.Tools) > 0 {
		log.Printf("[MCP] ✓ 已连接: %s (已发现 %d 个工具)", srv.Name, len(srv.Tools))
		return nil
	}
	log.Printf("[MCP] ⚠ %s: 无可用工具", srv.Name)
	return nil
}

// DisconnectMCP 断开 MCP 连接
func DisconnectMCP(serverID string) {
	mcpRuntimeMu.Lock()
	rt := mcpRuntimes[serverID]
	hrt := mcpHttpRuntimes[serverID]
	delete(mcpRuntimes, serverID)
	delete(mcpHttpRuntimes, serverID)
	mcpRuntimeMu.Unlock()

	if rt != nil && rt.cmd != nil && rt.cmd.Process != nil {
		if err := rt.cmd.Process.Kill(); err != nil {
			log.Printf("[MCP] 断开 %s 时出错: %v", serverID, err)
		}
	}
	if hrt != nil {
		// HTTP runtime 无需特殊清理
	}
	log.Printf("[MCP] 断开: %s", serverID)
}
