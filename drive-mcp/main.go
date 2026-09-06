// drive-mcp — 云盘 MCP 服务器（stdio JSON-RPC）
// AI 可读写 agent/<session_id>/ 目录下的文件，无需审批
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

var driveRoot = ""

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *mcpError   `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type listDirResult struct {
	Entries []dirEntry `json:"entries"`
}

type dirEntry struct {
	Name  string `json:"name"`
	Type  string `json:"type"` // "file" or "dir"
	Size  int64  `json:"size,omitempty"`
	Bytes string `json:"bytes,omitempty"`
}

type readFileResult struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Size    int    `json:"size"`
}

func main() {
	if len(os.Args) > 1 {
		driveRoot = filepath.Clean(os.Args[1])
	} else {
		driveRoot = "data/drive"
	}
	os.MkdirAll(driveRoot, 0755)

	log.SetOutput(io.Discard) // stdio is the MCP channel, no logging to stdout

	r := bufio.NewScanner(os.Stdin)
	r.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB buffer

	for r.Scan() {
		var req mcpRequest
		if json.Unmarshal(r.Bytes(), &req) != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			writeResponse(req.ID, map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{"tools": map[string]bool{}},
				"serverInfo":      map[string]string{"name": "drive-mcp", "version": "1.0"},
			})
		case "notifications/initialized":
			// no-op
		case "tools/list":
			writeTools(req.ID)
		case "tools/call":
			handleToolCall(req.ID, req.Params)
		default:
			writeError(req.ID, -32601, "Method not found: "+req.Method)
		}
	}
}

func writeTools(id int) {
	tools := []map[string]interface{}{
		{
			"name":        "drive_list",
			"description": "列出 agent/<session_id>/ 目录下的文件。path 参数相对于 agent 工作目录，留空列出根目录。",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "相对于 agent 目录的路径，留空列出根目录"},
				},
			},
		},
		{
			"name":        "drive_read",
			"description": "读取 agent/<session_id>/ 目录下的文本文件内容。",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "相对于 agent 目录的文件路径"},
				},
				"required": []string{"path"},
			},
		},
		{
			"name":        "drive_write",
			"description": "写入文件到 agent/<session_id>/ 目录下，支持创建新文件或覆盖已有文件。",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":    map[string]interface{}{"type": "string", "description": "相对于 agent 目录的文件路径"},
					"content": map[string]interface{}{"type": "string", "description": "文件内容"},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			"name":        "drive_mkdir",
			"description": "在 agent/<session_id>/ 目录下创建文件夹。",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "相对于 agent 目录的文件夹路径"},
				},
				"required": []string{"path"},
			},
		},
		{
			"name":        "drive_delete",
			"description": "删除 agent/<session_id>/ 目录下的文件或文件夹。",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "相对于 agent 目录的路径"},
				},
				"required": []string{"path"},
			},
		},
	}
	writeResponse(id, map[string]interface{}{"tools": tools})
}

var toolParams struct {
	Name      string `json:"name"`
	Arguments struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	} `json:"arguments"`
}

func handleToolCall(id int, params json.RawMessage) {
	toolParams.Arguments.Path = ""
	toolParams.Arguments.Content = ""
	if json.Unmarshal(params, &toolParams) != nil {
		writeError(id, -32602, "Invalid params")
		return
	}

	// 所有路径都限定在 driveRoot 下
	agentPath := filepath.Clean(toolParams.Arguments.Path)
	fullPath := filepath.Join(driveRoot, agentPath)
	if !strings.HasPrefix(fullPath, filepath.Clean(driveRoot)) {
		writeError(id, -32000, "Access denied: path outside drive root")
		return
	}

	switch toolParams.Name {
	case "drive_list":
		handleList(id, fullPath, agentPath)
	case "drive_read":
		handleRead(id, fullPath)
	case "drive_write":
		handleWrite(id, fullPath, toolParams.Arguments.Content)
	case "drive_mkdir":
		handleMkdir(id, fullPath)
	case "drive_delete":
		handleDelete(id, fullPath)
	default:
		writeError(id, -32601, "Unknown tool: "+toolParams.Name)
	}
}

func handleList(id int, fullPath, agentPath string) {
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		writeError(id, -32000, fmt.Sprintf("Cannot list: %v", err))
		return
	}
	var result []dirEntry
	for _, e := range entries {
		info, _ := e.Info()
		entry := dirEntry{
			Name: e.Name(),
			Type: "file",
			Size: 0,
		}
		if e.IsDir() {
			entry.Type = "dir"
		} else {
			entry.Size = info.Size()
			entry.Bytes = formatSize(info.Size())
		}
		result = append(result, entry)
	}
	writeResponse(id, listDirResult{Entries: result})
}

func handleRead(id int, fullPath string) {
	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		writeError(id, -32000, "File not found or is directory")
		return
	}
	// 限制 1MB
	if info.Size() > 1024*1024 {
		writeError(id, -32000, "File too large (max 1MB)")
		return
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		writeError(id, -32000, fmt.Sprintf("Read error: %v", err))
		return
	}
	writeResponse(id, readFileResult{
		Path:    toolParams.Arguments.Path,
		Content: string(data),
		Size:    len(data),
	})
}

func handleWrite(id int, fullPath, content string) {
	// 确保父目录存在
	os.MkdirAll(filepath.Dir(fullPath), 0755)
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		writeError(id, -32000, fmt.Sprintf("Write error: %v", err))
		return
	}
	writeResponse(id, map[string]string{"status": "ok", "path": toolParams.Arguments.Path, "size": fmt.Sprintf("%d bytes", len(content))})
}

func handleMkdir(id int, fullPath string) {
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		writeError(id, -32000, fmt.Sprintf("Mkdir error: %v", err))
		return
	}
	writeResponse(id, map[string]string{"status": "ok", "path": toolParams.Arguments.Path})
}

func handleDelete(id int, fullPath string) {
	if filepath.Clean(fullPath) == filepath.Clean(driveRoot) {
		writeError(id, -32000, "Cannot delete root")
		return
	}
	if err := os.RemoveAll(fullPath); err != nil {
		writeError(id, -32000, fmt.Sprintf("Delete error: %v", err))
		return
	}
	writeResponse(id, map[string]string{"status": "ok", "path": toolParams.Arguments.Path})
}

func writeResponse(id int, result interface{}) {
	writeJSON(mcpResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeError(id int, code int, message string) {
	writeJSON(mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: code, Message: message}})
}

func writeJSON(v interface{}) {
	data, _ := json.Marshal(v)
	fmt.Println(string(data))
}

func formatSize(bytes int64) string {
	if bytes <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	size := float64(bytes)
	for size >= 1024 && i < len(units)-1 {
		size /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", size, units[i])
}
