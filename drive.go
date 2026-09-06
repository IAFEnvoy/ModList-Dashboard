package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const driveRoot = "data/drive"

func resolvePathWithin(root, subPath string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	cleaned := filepath.Clean(filepath.FromSlash(strings.TrimSpace(subPath)))
	if filepath.IsAbs(cleaned) || filepath.VolumeName(cleaned) != "" {
		return "", fmt.Errorf("absolute paths are not allowed")
	}
	full := filepath.Join(rootAbs, cleaned)
	if !pathWithinRoot(rootAbs, full) {
		return "", fmt.Errorf("path escapes root")
	}

	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
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
	if !pathWithinRoot(rootReal, realExisting) {
		return "", fmt.Errorf("path resolves outside root")
	}
	return full, nil
}

func pathWithinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func initDrive() {
	if err := os.MkdirAll(driveRoot, 0755); err != nil {
		log.Printf("[Drive] 初始化目录失败: %v", err)
	}
}

// driveItem 网盘条目
type driveItem struct {
	Name      string `json:"name"`
	Type      string `json:"type"` // "file" or "dir"
	Size      int64  `json:"size,omitempty"`
	Path      string `json:"path"` // 相对于 driveRoot 的路径
	UpdatedAt string `json:"updated_at,omitempty"`
}

// handleDriveList 列出指定目录下的文件和文件夹
func handleDriveList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "GET" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	subPath := strings.TrimPrefix(r.URL.Path, "/api/drive/list")
	subPath = strings.TrimPrefix(subPath, "/")
	// 安全检查：防止路径穿越
	fullPath, err := resolvePathWithin(driveRoot, subPath)
	if err != nil {
		http.Error(w, `{"error":"invalid path"}`, 400)
		return
	}

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, `{"error":"not found"}`, 404)
		} else {
			http.Error(w, `{"error":"read error"}`, 500)
		}
		return
	}

	relPath := strings.TrimPrefix(filepath.Clean(fullPath), filepath.Clean(driveRoot))
	relPath = strings.ReplaceAll(relPath, "\\", "/")

	var items []driveItem
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		item := driveItem{
			Name:      e.Name(),
			Path:      filepath.ToSlash(filepath.Join(relPath, e.Name())),
			UpdatedAt: info.ModTime().UTC().Format("2006-01-02 15:04"),
		}
		if e.IsDir() {
			item.Type = "dir"
		} else {
			item.Type = "file"
			item.Size = info.Size()
		}
		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Type != items[j].Type {
			return items[i].Type == "dir"
		}
		return items[i].Name < items[j].Name
	})

	resp := map[string]interface{}{
		"path":  "/" + strings.Trim(relPath, "/"),
		"items": items,
	}
	json.NewEncoder(w).Encode(resp)
}

// handleDriveUpload 上传文件
func handleDriveUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}

	// 限制最大 100MB
	r.Body = http.MaxBytesReader(w, r.Body, 100<<20)
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		http.Error(w, `{"error":"file too large (max 100MB)"}`, 413)
		return
	}

	subPath := strings.TrimSpace(r.FormValue("path"))
	fullDir, err := resolvePathWithin(driveRoot, subPath)
	if err != nil {
		http.Error(w, `{"error":"invalid path"}`, 400)
		return
	}
	os.MkdirAll(fullDir, 0755)

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		http.Error(w, `{"error":"no files"}`, 400)
		return
	}

	var uploaded []string
	for _, fh := range files {
		// 过滤危险扩展名（可自行扩展）
		name := filepath.Base(fh.Filename)
		if name == "." || name == ".." || strings.Contains(name, "/") || strings.Contains(name, "\\") {
			continue
		}

		file, err := fh.Open()
		if err != nil {
			continue
		}

		dstPath := filepath.Join(fullDir, name)
		dst, err := os.Create(dstPath)
		if err != nil {
			file.Close()
			continue
		}

		io.Copy(dst, file)
		dst.Close()
		file.Close()
		uploaded = append(uploaded, name)
	}

	log.Printf("[Drive] 上传 %d 文件到 %s", len(uploaded), subPath)
	json.NewEncoder(w).Encode(map[string]interface{}{"uploaded": uploaded})
}

// handleDriveDownloadOrView 下载文件或查看文本内容
// GET /api/drive/file?path=...&action=download|view
func handleDriveDownloadOrView(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}

	filePath := r.URL.Query().Get("path")
	action := r.URL.Query().Get("action") // "download" or "view"

	fullPath, err := resolvePathWithin(driveRoot, filePath)
	if err != nil {
		http.Error(w, `{"error":"invalid path"}`, 400)
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		http.Error(w, `{"error":"not found"}`, 404)
		return
	}

	if action == "view" {
		// 文本预览：仅允许常见文本类型，限制 500KB
		ext := strings.ToLower(filepath.Ext(filePath))
		textExts := map[string]bool{
			".txt": true, ".md": true, ".json": true, ".yml": true, ".yaml": true,
			".xml": true, ".html": true, ".css": true, ".js": true, ".ts": true,
			".java": true, ".kt": true, ".py": true, ".go": true, ".rs": true,
			".c": true, ".cpp": true, ".h": true, ".cfg": true, ".toml": true,
			".ini": true, ".properties": true, ".log": true, ".csv": true,
			".gradle": true, ".sh": true, ".bat": true, ".ps1": true,
		}
		if !textExts[ext] && ext != "" {
			http.Error(w, `{"error":"preview not supported for this file type"}`, 415)
			return
		}
		if info.Size() > 500*1024 {
			http.Error(w, `{"error":"file too large to preview (max 500KB)"}`, 413)
			return
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			http.Error(w, `{"error":"read error"}`, 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"name":    filepath.Base(filePath),
			"content": string(data),
			"size":    info.Size(),
		})
		return
	}

	// download
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(filePath)+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, fullPath)
}

// handleDriveMkdir 创建文件夹
func handleDriveMkdir(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}

	var body struct {
		Path string `json:"path"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Path == "" {
		http.Error(w, `{"error":"bad request"}`, 400)
		return
	}

	fullPath, err := resolvePathWithin(driveRoot, body.Path)
	if err != nil {
		http.Error(w, `{"error":"invalid path"}`, 400)
		return
	}

	if err := os.MkdirAll(fullPath, 0755); err != nil {
		http.Error(w, `{"error":"create failed"}`, 500)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleDriveDelete 删除文件或文件夹
func handleDriveDelete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}

	var body struct {
		Path string `json:"path"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Path == "" {
		http.Error(w, `{"error":"bad request"}`, 400)
		return
	}

	fullPath, err := resolvePathWithin(driveRoot, body.Path)
	if err != nil {
		http.Error(w, `{"error":"invalid path"}`, 400)
		return
	}

	// 禁止删除根
	if filepath.Clean(fullPath) == filepath.Clean(driveRoot) {
		http.Error(w, `{"error":"cannot delete root"}`, 400)
		return
	}

	if err := os.RemoveAll(fullPath); err != nil {
		http.Error(w, `{"error":"delete failed"}`, 500)
		return
	}
	log.Printf("[Drive] 删除 %s", body.Path)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleDriveRename 重命名文件或文件夹
func handleDriveRename(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}

	var body struct {
		OldPath string `json:"old_path"`
		NewName string `json:"new_name"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.OldPath == "" || body.NewName == "" {
		http.Error(w, `{"error":"bad request"}`, 400)
		return
	}

	oldFull, err := resolvePathWithin(driveRoot, body.OldPath)
	if err != nil {
		http.Error(w, `{"error":"invalid path"}`, 400)
		return
	}
	// 禁止重命名根
	if filepath.Clean(oldFull) == filepath.Clean(driveRoot) {
		http.Error(w, `{"error":"cannot rename root"}`, 400)
		return
	}
	// 新名称不能包含路径分隔符
	newName := filepath.Base(body.NewName)
	if newName == "." || newName == ".." || newName != body.NewName {
		http.Error(w, `{"error":"invalid name"}`, 400)
		return
	}

	newFull := filepath.Join(filepath.Dir(oldFull), newName)
	if err := os.Rename(oldFull, newFull); err != nil {
		http.Error(w, `{"error":"rename failed"}`, 500)
		return
	}
	log.Printf("[Drive] 重命名 %s -> %s", body.OldPath, newName)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleDriveSave 保存文件内容（覆盖写入）
func handleDriveSave(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}

	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Path == "" {
		http.Error(w, `{"error":"bad request"}`, 400)
		return
	}

	fullPath, err := resolvePathWithin(driveRoot, body.Path)
	if err != nil {
		http.Error(w, `{"error":"invalid path"}`, 400)
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		http.Error(w, `{"error":"not a file"}`, 400)
		return
	}
	// 限制 10MB
	if len(body.Content) > 10*1024*1024 {
		http.Error(w, `{"error":"file too large (max 10MB)"}`, 413)
		return
	}

	if err := os.WriteFile(fullPath, []byte(body.Content), 0644); err != nil {
		http.Error(w, `{"error":"save failed"}`, 500)
		return
	}
	log.Printf("[Drive] 保存文件 %s (%d bytes)", body.Path, len(body.Content))
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
