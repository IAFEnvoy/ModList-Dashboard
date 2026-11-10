package main

import (
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ========== 许可证库（data/license/） ==========
//
// 目录里每个文件就是一份许可证原文：文件名即许可证名（MIT、Apache-2.0…），
// 内容为完整原文。它同时是两个功能的唯一数据源：
//
//  1. 模组编辑器的许可证下拉（经 /api/config 的 license_options 下发）；
//  2. 右上角「许可证下载」下拉（经 /api/license/download 下载原文）。
//
// 目录缺失时会自动创建，但不会预置任何原文——放什么、放多少由使用者决定。
var licenseDir = filepath.Join("data", "license")

// licenseExt 允许的扩展名。约定文件名不带扩展名，但 .txt 也一并接受，
// 免得 Windows 上双击打不开时被迫改名；两者在列表里显示的名称相同。
const licenseExt = ".txt"

// initLicenseLibrary 确保许可证目录存在，并输出当前可用数量。
func initLicenseLibrary() {
	if err := os.MkdirAll(licenseDir, 0755); err != nil {
		log.Printf("[License] 初始化目录失败: %v", err)
		return
	}
	log.Printf("[License] 许可证库 %s：%d 个（文件名即许可证名）", licenseDir, len(licenseLibrary()))
}

// licenseEntry 目录里的一份许可证。
type licenseEntry struct {
	Name string // 许可证名（去掉 .txt）
	File string // 目录内的实际文件名
}

// licenseEntries 扫描许可证目录并按名称排序。
//
// 除了子目录、以 . 开头的隐藏文件，目录里的每个文件都视为一份许可证；
// 结尾的 .txt 会被去掉（MIT.txt 与 MIT 等价，两者同时存在时无扩展名的优先）。
// 注意不能用 filepath.Ext 判断扩展名：LGPL-3.0、Apache-2.0 这类名字本身就带点，
// Ext 会把 .0 当成扩展名，从而把最常用的许可证全部漏掉。
func licenseEntries() []licenseEntry {
	dirEntries, err := os.ReadDir(licenseDir)
	if err != nil {
		return nil
	}
	out := make([]licenseEntry, 0, len(dirEntries))
	seen := make(map[string]bool, len(dirEntries))
	for _, e := range dirEntries {
		if e.IsDir() {
			continue
		}
		file := e.Name()
		if strings.HasPrefix(file, ".") {
			continue
		}
		name := file
		if ext := filepath.Ext(file); strings.EqualFold(ext, licenseExt) {
			name = strings.TrimSuffix(file, ext)
		}
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, licenseEntry{Name: name, File: file})
	}
	sort.Slice(out, func(i, j int) bool {
		ni, nj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if ni != nj {
			return ni < nj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// licenseLibrary 返回许可证名列表，供编辑器下拉与下载菜单使用。
func licenseLibrary() []string {
	entries := licenseEntries()
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names
}

// lookupLicense 按许可证名查找目录里的文件。
// 只在扫描结果里比对，因此返回值一定是目录内已存在的文件，不存在路径穿越。
func lookupLicense(name string) (licenseEntry, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return licenseEntry{}, false
	}
	var folded *licenseEntry
	for _, e := range licenseEntries() {
		if e.Name == name {
			return e, true
		}
		if folded == nil && strings.EqualFold(e.Name, name) {
			matched := e
			folded = &matched
		}
	}
	if folded != nil {
		return *folded, true
	}
	return licenseEntry{}, false
}

// attachmentHeader 生成 Content-Disposition，把文件名里的非 ASCII / 引号等
// 换成下划线并额外给出 RFC 5987 的 filename*，避免许可证名带空格或中文时
// 产生非法的响应头。
func attachmentHeader(filename string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7E || r == '"' || r == '\\' || r == ';' {
			return '_'
		}
		return r
	}, filename)
	header := `attachment; filename="` + ascii + `"`
	if ascii != filename {
		header += "; filename*=UTF-8''" + url.PathEscape(filename)
	}
	return header
}

// handleLicenseDownload 下载许可证原文。
// GET /api/license/download?name=MIT
func handleLicenseDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	entry, ok := lookupLicense(r.URL.Query().Get("name"))
	if !ok {
		http.Error(w, `{"error":"unknown license"}`, http.StatusNotFound)
		return
	}

	data, err := os.ReadFile(filepath.Join(licenseDir, entry.File))
	if err != nil {
		http.Error(w, `{"error":"read error"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", attachmentHeader(entry.Name+licenseExt))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
