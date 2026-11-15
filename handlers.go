package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"modlist-dashboard/agent"
	"modlist-dashboard/badge"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func handleModsList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(buildModsDashboard())
}

func handleModDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	id := r.URL.Path[len("/api/mods/"):]
	meta, ok := getModMeta(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode(buildSingleModDashboard(meta))
}

// handleAdminMods exposes raw metadata for the authenticated editor.
func handleAdminMods(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodGet {
		json.NewEncoder(w).Encode(sortedModMetas())
		return
	}
	var meta ModMeta
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
		writeJSONError(w, "invalid metadata JSON", http.StatusBadRequest)
		return
	}
	if !validModID(meta.ID) {
		writeJSONError(w, "invalid id", http.StatusBadRequest)
		return
	}
	if _, exists := getModMeta(meta.ID); exists {
		writeJSONError(w, "mod already exists", http.StatusConflict)
		return
	}
	if err := saveModMetaToDB("", meta); err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	replaceModMeta(meta)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(meta)
}

func handleAdminMod(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/mods/")
	if strings.HasSuffix(rest, "/logo") {
		id := strings.TrimSuffix(rest, "/logo")
		id = strings.Trim(id, "/")
		if id == "" || strings.Contains(id, "/") {
			writeJSONError(w, "invalid mod id", http.StatusBadRequest)
			return
		}
		handleAdminModLogo(w, r, id)
		return
	}
	id := rest
	if id == "" || strings.Contains(id, "/") {
		writeJSONError(w, "invalid mod id", http.StatusBadRequest)
		return
	}
	if _, exists := getModMeta(id); !exists {
		writeJSONError(w, "mod not found", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		meta, _ := getModMeta(id)
		json.NewEncoder(w).Encode(meta)
	case http.MethodPut, http.MethodPatch:
		var meta ModMeta
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
			writeJSONError(w, "invalid metadata JSON", http.StatusBadRequest)
			return
		}
		if !validModID(meta.ID) {
			writeJSONError(w, "invalid id", http.StatusBadRequest)
			return
		}
		if meta.ID != id {
			if _, exists := getModMeta(meta.ID); exists {
				writeJSONError(w, "mod already exists", http.StatusConflict)
				return
			}
		}
		if err := saveModMetaToDB(id, meta); err != nil {
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if meta.ID != id {
			removeModMeta(id)
			renameDifficulty(id, meta.ID)
		}
		replaceModMeta(meta)
		json.NewEncoder(w).Encode(meta)
	case http.MethodDelete:
		if err := deleteModMetaFromDB(id); err != nil {
			writeJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		removeModMeta(id)
		clearDifficulty(id)
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": id})
	default:
		writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// PUT /api/admin/mods/<mod_id>/logo — 上传图片（二进制 body，Content-Type 为图片类型）
func handleAdminModLogo(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPut {
		writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := getModMeta(id); !ok {
		writeJSONError(w, "mod not found", http.StatusNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil || len(data) == 0 {
		writeJSONError(w, "empty image data", http.StatusBadRequest)
		return
	}
	mime := r.Header.Get("Content-Type")
	_, ext := normalizeImageMime(mime)
	if ext == "" {
		writeJSONError(w, "不支持的图片类型，请上传 webp/png/jpeg/gif/avif/svg", http.StatusBadRequest)
		return
	}
	storeMime := mime
	if ffmpegAvailable && ext != "svg" && ext != "webp" {
		if converted, convErr := convertImageToWebp(data, ext); convErr != nil {
			log.Printf("[Logo] 模组 %s 转 webp 失败，直接保存原图: %v", id, convErr)
		} else {
			storeMime = "image/webp"
			data = converted
			log.Printf("[Logo] 模组 %s 已通过 ffmpeg 转为 webp 后入库", id)
		}
	}
	if err := upsertModLogo(id, data, storeMime); err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, storeExt := normalizeImageMime(storeMime)
	logoName := id + "." + storeExt
	if meta, ok := getModMeta(id); ok {
		meta.Logo = logoName
		replaceModMeta(meta)
	}
	json.NewEncoder(w).Encode(map[string]string{"id": id, "logo": logoName})
}

// GET /api/logo/<mod_id> — 从数据库输出模组 logo
func handleModLogo(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/logo/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	data, mime, found, err := readModLogo(id)
	if err != nil || !found {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func writeJSONError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func handleGitHub(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path[len("/api/github/"):]
	parts := splitPath(path, 3)
	if len(parts) < 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	owner, repo := parts[0], parts[1]
	subType := ""
	if len(parts) > 2 {
		subType = parts[2]
	}
	ttl := ttlCache()
	info := getGitHubInfoCached(owner+"/"+repo, ttl)
	if info == nil {
		json.NewEncoder(w).Encode(badge.GitHubInfo{})
		return
	}
	if subType == "issues" {
		json.NewEncoder(w).Encode(info.Issues)
	} else if subType == "pulls" {
		json.NewEncoder(w).Encode(info.PullRequests)
	} else {
		json.NewEncoder(w).Encode(info)
	}
}

// handleGitHubIssue 获取单个 Issue 详情（含 body）
func handleGitHubIssue(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path[len("/api/github/issue/"):]
	parts := splitPath(path, 3)
	if len(parts) < 3 {
		http.Error(w, "invalid path, need owner/repo/number", http.StatusBadRequest)
		return
	}
	owner, repo := parts[0], parts[1]
	issueNumber, err := strconv.Atoi(parts[2])
	if err != nil {
		http.Error(w, "invalid issue number", http.StatusBadRequest)
		return
	}
	detail := fetchGitHubIssueDetail(owner, repo, issueNumber)
	if detail == nil {
		http.Error(w, `{"error":"failed to fetch issue detail"}`, http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(detail)
}

// handleGitHubPR 获取单个 PR 详情（含 diff）
func handleGitHubPR(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path[len("/api/github/pr/"):]
	parts := splitPath(path, 3)
	if len(parts) < 3 {
		http.Error(w, "invalid path, need owner/repo/number", http.StatusBadRequest)
		return
	}
	owner, repo := parts[0], parts[1]
	prNumber, err := strconv.Atoi(parts[2])
	if err != nil {
		http.Error(w, "invalid pr number", http.StatusBadRequest)
		return
	}
	detail := fetchGitHubPRDetail(owner, repo, prNumber)
	if detail == nil {
		http.Error(w, `{"error":"failed to fetch pr detail"}`, http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(detail)
}

func handleStatusOrder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	data, _ := os.ReadFile(filepath.Join("config", "status_config.json"))
	w.Write(data)
}

func handlePublicConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	statuses, loaders, loaderColors, versionOptions := configurationEditorOptions()
	presets := loadEditorFieldPresets()
	statusColors := make(map[string]string, len(statuses))
	for _, option := range statuses {
		statusColors[option.Name] = option.Color
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"login_enabled":        authEnabled(),
		"statuses":             statusNames(statuses),
		"status_colors":        statusColors,
		"loaders":              loaders,
		"loader_colors":        loaderColors,
		"version_options":      versionOptions,
		"license_options":      licenseLibrary(),
		"architecture_options": presets.ArchitectureOptions,
		"tag_options":          presets.TagOptions,
	})
}

type modStatusOption struct {
	Name  string
	Color string
}

type modVersionOption struct {
	Value string `json:"value"`
	Emoji string `json:"emoji"`
	Text  string `json:"text"`
}

func statusNames(options []modStatusOption) []string {
	names := make([]string, 0, len(options))
	for _, option := range options {
		names = append(names, option.Name)
	}
	return names
}

// configurationEditorOptions 读取 config/configuration.json：
// colors.status 与 colors.loader 提供模组状态和加载器及其颜色（保持书写顺序），
// icons.versions 提供各加载器可选的支持状态及对应 emoji。
func configurationEditorOptions() ([]modStatusOption, []string, map[string]string, []modVersionOption) {
	file, err := os.Open(filepath.Join("config", "configuration.json"))
	if err != nil {
		return nil, nil, nil, nil
	}
	defer file.Close()

	dec := json.NewDecoder(file)
	if _, err := dec.Token(); err != nil { // 根对象 {
		return nil, nil, nil, nil
	}
	var statuses []modStatusOption
	var loaders []string
	var loaderColors map[string]string
	var versionOptions []modVersionOption
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return statuses, loaders, loaderColors, versionOptions
		}
		key, _ := keyToken.(string)
		switch key {
		case "colors":
			loaders, loaderColors, statuses = decodeColorSections(dec)
		case "icons":
			versionOptions = decodeIconSections(dec)
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return statuses, loaders, loaderColors, versionOptions
			}
		}
	}
	return statuses, loaders, loaderColors, versionOptions
}

func decodeColorSections(dec *json.Decoder) ([]string, map[string]string, []modStatusOption) {
	var loaders []string
	var loaderColors map[string]string
	var statuses []modStatusOption
	if _, err := dec.Token(); err != nil { // colors {
		return loaders, loaderColors, statuses
	}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return loaders, loaderColors, statuses
		}
		key, _ := keyToken.(string)
		switch key {
		case "loader":
			keys, values := decodeStringObject(dec)
			loaders = keys
			loaderColors = values
		case "status":
			keys, colors := decodeStringObject(dec)
			for _, name := range keys {
				statuses = append(statuses, modStatusOption{Name: name, Color: colors[name]})
			}
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return loaders, loaderColors, statuses
			}
		}
	}
	if _, err := dec.Token(); err != nil { // }
		return loaders, loaderColors, statuses
	}
	return loaders, loaderColors, statuses
}

// decodeStringObject 读取一个 value 为字符串的对象，返回按书写顺序排列的键。
func decodeStringObject(dec *json.Decoder) ([]string, map[string]string) {
	values := make(map[string]string)
	var keys []string
	if _, err := dec.Token(); err != nil { // {
		return keys, values
	}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return keys, values
		}
		key, _ := keyToken.(string)
		var value string
		if err := dec.Decode(&value); err != nil {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return keys, values
			}
			continue
		}
		keys = append(keys, key)
		values[key] = value
	}
	if _, err := dec.Token(); err != nil { // }
		return keys, values
	}
	return keys, values
}

func decodeIconSections(dec *json.Decoder) []modVersionOption {
	var options []modVersionOption
	if _, err := dec.Token(); err != nil { // icons {
		return options
	}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return options
		}
		key, _ := keyToken.(string)
		if key != "versions" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return options
			}
			continue
		}
		if _, err := dec.Token(); err != nil { // versions {
			return options
		}
		for dec.More() {
			valueToken, err := dec.Token()
			if err != nil {
				return options
			}
			value, _ := valueToken.(string)
			var meta struct {
				Emoji string `json:"emoji"`
				Text  string `json:"text"`
			}
			if err := dec.Decode(&meta); err != nil {
				return options
			}
			if value == "" {
				continue
			}
			options = append(options, modVersionOption{Value: value, Emoji: meta.Emoji, Text: meta.Text})
		}
		if _, err := dec.Token(); err != nil { // }
			return options
		}
		return options
	}
	return options
}

func splitPath(s string, n int) []string {
	result := make([]string, 0, n)
	for i := 0; i < n-1; i++ {
		idx := strings.IndexByte(s, '/')
		if idx < 0 {
			result = append(result, s)
			return result
		}
		result = append(result, s[:idx])
		s = s[idx+1:]
	}
	result = append(result, s)
	return result
}

func buildModsDashboard() []ModDashboard {
	ttl := time.Duration(config.Server.CacheTTLMin) * time.Minute
	if ttl == 0 {
		ttl = 30 * time.Minute
	}

	type idxResult struct {
		idx int
		md  ModDashboard
	}

	metas := sortedModMetas()
	ids := make([]string, 0, len(metas))
	for _, meta := range metas {
		ids = append(ids, meta.ID)
	}
	total := len(ids)
	results := make([]idxResult, total)
	var wg sync.WaitGroup
	var completed atomic.Int32

	startTime := time.Now()

	for i, id := range ids {
		wg.Add(1)
		go func(idx int, modID string) {
			defer wg.Done()
			meta, ok := getModMeta(modID)
			if !ok {
				return
			}
			diff := finalDifficulty(meta)

			var cfDownloads int64
			var ghInfo *badge.GitHubInfo
			var innerWg sync.WaitGroup
			innerWg.Add(2)

			go func() {
				defer innerWg.Done()
				cfDownloads = getCachedCFDownloads(meta.IDs.CF, ttl)
			}()
			go func() {
				defer innerWg.Done()
				if meta.IDs.GH != "" {
					ghInfo = getGitHubInfoCached(meta.IDs.GH, ttl)
				}
			}()
			innerWg.Wait()

			if meta.IDs.GH != "" {
				markRepoArchived(ghInfo, meta.IDs.GH)
			}

			totalDl := cfDownloads
			var popularity float64
			if totalDl > 0 {
				popularity = math.Log10(float64(totalDl))
			}

			results[idx] = idxResult{
				idx: idx,
				md: ModDashboard{
					ModMeta:        meta,
					Difficulty:     diff,
					CFDownloads:    cfDownloads,
					TotalDownloads: totalDl,
					Popularity:     popularity,
					GitHub:         ghInfo,
					HasCodacy:      meta.IDs.GH != "" && codacyScoreFileExists(strings.SplitN(meta.IDs.GH, "/", 2)[0], strings.SplitN(meta.IDs.GH, "/", 2)[1]),
				},
			}

			completed.Add(1)
		}(i, id)
	}
	wg.Wait()

	_ = time.Since(startTime)

	ordered := make([]ModDashboard, len(ids))
	for _, r := range results {
		ordered[r.idx] = r.md
	}
	return ordered
}

func buildSingleModDashboard(meta ModMeta) ModDashboard {
	diff := finalDifficulty(meta)

	ttl := time.Duration(config.Server.CacheTTLMin) * time.Minute
	if ttl == 0 {
		ttl = 30 * time.Minute
	}

	var cfDownloads int64
	var ghInfo *badge.GitHubInfo
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		cfDownloads = getCachedCFDownloads(meta.IDs.CF, ttl)
	}()
	go func() {
		defer wg.Done()
		if meta.IDs.GH != "" {
			ghInfo = getGitHubInfoCached(meta.IDs.GH, ttl)
		}
	}()
	wg.Wait()

	if meta.IDs.GH != "" {
		markRepoArchived(ghInfo, meta.IDs.GH)
	}

	total := cfDownloads
	var popularity float64
	if total > 0 {
		popularity = math.Log10(float64(total))
	}

	return ModDashboard{
		ModMeta:        meta,
		Difficulty:     diff,
		CFDownloads:    cfDownloads,
		TotalDownloads: total,
		Popularity:     popularity,
		GitHub:         ghInfo,
		HasCodacy:      meta.IDs.GH != "" && codacyScoreFileExists(strings.SplitN(meta.IDs.GH, "/", 2)[0], strings.SplitN(meta.IDs.GH, "/", 2)[1]),
	}
}

// ========== 代办事项 API ==========

var todoMu sync.Mutex

func loadTodos() TodoFileData {
	todoMu.Lock()
	defer todoMu.Unlock()
	return loadTodosUnlocked()
}

func loadTodosUnlocked() TodoFileData {
	data, _ := os.ReadFile(todoFile)
	var d TodoFileData
	_ = json.Unmarshal(data, &d)
	if d.Items == nil {
		d.Items = []TodoItem{}
	}
	return d
}

func saveTodosUnlocked(d TodoFileData) error {
	raw, _ := json.MarshalIndent(d, "", "  ")
	return os.WriteFile(todoFile, raw, 0644)
}

func updateTodoItem(id, title, content string, project *string) (TodoItem, bool, error) {
	todoMu.Lock()
	defer todoMu.Unlock()
	d := loadTodosUnlocked()
	for i := range d.Items {
		if d.Items[i].ID != id {
			continue
		}
		if title != "" || content != "" || project != nil {
			if title != "" {
				d.Items[i].Title = title
			}
			if content != "" {
				d.Items[i].Content = content
			}
			if project != nil {
				d.Items[i].Project = *project
			}
		} else {
			d.Items[i].Resolved = !d.Items[i].Resolved
		}
		return d.Items[i], true, saveTodosUnlocked(d)
	}
	return TodoItem{}, false, nil
}

func deleteTodoItem(id string) (bool, error) {
	todoMu.Lock()
	defer todoMu.Unlock()
	d := loadTodosUnlocked()
	for i := range d.Items {
		if d.Items[i].ID != id {
			continue
		}
		d.Items = append(d.Items[:i], d.Items[i+1:]...)
		return true, saveTodosUnlocked(d)
	}
	return false, nil
}

func appendTodoItem(item TodoItem) error {
	todoMu.Lock()
	defer todoMu.Unlock()
	d := loadTodosUnlocked()
	d.Items = append(d.Items, item)
	return saveTodosUnlocked(d)
}

func handleTodoCounts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	d := loadTodos()
	bugs := 0
	todos := 0
	for _, item := range d.Items {
		if item.Resolved {
			continue
		}
		switch item.Type {
		case "bug":
			bugs++
		case "todo":
			todos++
		}
	}
	json.NewEncoder(w).Encode(map[string]int{"bugs": bugs, "todos": todos})
}

func handleTodos(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == "GET" {
		d := loadTodos()
		itemType := r.URL.Query().Get("type")
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		per, _ := strconv.Atoi(r.URL.Query().Get("per"))
		if page <= 0 {
			page = 1
		}
		if per <= 0 {
			per = 20
		}
		projectFilter := r.URL.Query().Get("project")

		var filtered []TodoItem
		for _, item := range d.Items {
			if itemType != "" && item.Type != itemType {
				continue
			}
			if projectFilter != "" {
				if projectFilter == "__none__" {
					if item.Project != "" {
						continue
					}
				} else if item.Project != projectFilter {
					continue
				}
			}
			filtered = append(filtered, item)
		}
		// Sort: unresolved first (by created_at desc), resolved last (by created_at desc)
		sort.SliceStable(filtered, func(i, j int) bool {
			a, b := &filtered[i], &filtered[j]
			if a.Resolved != b.Resolved {
				return !a.Resolved
			}
			return a.CreatedAt > b.CreatedAt
		})
		total := len(filtered)
		start := (page - 1) * per
		end := start + per
		if start > total {
			start = total
		}
		if end > total {
			end = total
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"items": filtered[start:end],
			"total": total,
			"page":  page,
			"per":   per,
		})
		return
	}

	// PUT/PATCH toggles resolved
	if r.Method == "PUT" {
		_, _, ok := checkAuth(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id := r.URL.Path[len("/api/todos/"):]
		var body struct {
			Title   string  `json:"title"`
			Content string  `json:"content"`
			Project *string `json:"project"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		item, found, err := updateTodoItem(id, body.Title, body.Content, body.Project)
		if err != nil {
			http.Error(w, "save failed", http.StatusInternalServerError)
			return
		}
		if found {
			json.NewEncoder(w).Encode(item)
			return
		}
		http.NotFound(w, r)
		return
	}

	if r.Method == "DELETE" {
		_, _, ok := checkAuth(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id := r.URL.Path[len("/api/todos/"):]
		found, err := deleteTodoItem(id)
		if err != nil {
			http.Error(w, "save failed", http.StatusInternalServerError)
			return
		}
		if found {
			json.NewEncoder(w).Encode(map[string]string{"deleted": id})
			return
		}
		http.NotFound(w, r)
		return
	}

	if r.Method == "POST" {
		_, _, ok := checkAuth(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body struct {
			Type    string `json:"type"`
			Title   string `json:"title"`
			Content string `json:"content"`
			Project string `json:"project"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Title == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body.Type != "bug" && body.Type != "todo" {
			http.Error(w, "type must be bug or todo", http.StatusBadRequest)
			return
		}
		id := make([]byte, 8)
		rand.Read(id)
		item := TodoItem{
			ID:        hex.EncodeToString(id),
			Type:      body.Type,
			Title:     body.Title,
			Content:   body.Content,
			Project:   body.Project,
			Resolved:  false,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if err := appendTodoItem(item); err != nil {
			http.Error(w, "save failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(item)
		return
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// ========== 缓存刷新 ==========

func handleFlushCache(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _, ok := checkAuth(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Source string `json:"source"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	// 每个数据源对应一个或多个缓存键前缀。
	// ghdl:（GitHub Release 下载量）与 gh:（仓库 Issues/PR）相互独立，但同属 github 数据源。
	prefixes := map[string][]string{
		"curseforge": {"cf:"},
		"github":     {badge.CacheKeyPrefix, "ghdl:"},
		"codacy":     {"codacy:"},
	}
	dirMap := map[string]string{"cf:": "curseforge", badge.CacheKeyPrefix: "github", "ghdl:": "github", "codacy:": "codacy"}

	flushByPrefix := func(prefix string) int {
		count := 0
		cacheMu.Lock()
		for k := range cache {
			if strings.HasPrefix(k, prefix) {
				delete(cache, k)
				count++
			}
		}
		cacheMu.Unlock()
		if dir, ok := dirMap[prefix]; ok {
			cacheDir := filepath.Join(config.Server.cacheDir(), dir)
			os.RemoveAll(cacheDir) // 递归删除整个平台缓存目录
		}
		return count
	}

	cleared := 0
	if body.Source == "all" {
		for _, list := range prefixes {
			for _, prefix := range list {
				cleared += flushByPrefix(prefix)
			}
		}
	} else if list, ok := prefixes[body.Source]; ok {
		for _, prefix := range list {
			cleared += flushByPrefix(prefix)
		}
	} else if body.Source != "db" && body.Source != "reload" {
		http.Error(w, `{"error":"invalid source"}`, http.StatusBadRequest)
		return
	}

	// 后台重新预热
	if body.Source == "all" || body.Source == "github" {
		// A manual refresh uses the same successful-snapshot event as TTL
		// refreshes. Only cache data is cleared; comparison/task state is kept.
		notifyGitHubRefresh()
	}
	if body.Source == "all" || body.Source == "curseforge" || body.Source == "github" {
		go prewarmCache()
	}
	if body.Source == "all" || body.Source == "codacy" {
		if config.Codacy.Enabled && authConfig.Codacy.APIToken != "" && authConfig.Codacy.APIToken != "YOUR_CODACY_API_TOKEN" {
			go startCodacyWorker()
		}
	}

	msg := fmt.Sprintf("已清除 %s 的 %d 条缓存，正在后台重新拉取", body.Source, cleared)
	if body.Source == "all" || body.Source == "github" {
		msg += "；已触发 GitHub 增量检查，保留 Issue 基线和处理记录"
	}

	if body.Source == "reload" {
		loadConfig("config/config.yml", &config)
		loadConfig("config/auth.yml", &authConfig)
		loadModMetas()
		loadDifficultyCache()
		loadConfig("config/rate.yml", &rateConfig)
		agent.LoadAll()
		notifyGitHubRefresh()
		if issuePreprocessor != nil {
			issuePreprocessor.notify()
		}
		cleared = len(sortedModMetas())
		msg = fmt.Sprintf("已重载本地配置（%d 个模组元数据 + rate.yml + mods.db 维护难度 + AI/Agent/MCP）", cleared)
	}
	if body.Source == "db" {
		if err := loadModMetasFromDB(); err != nil {
			http.Error(w, `{"error":"从 mods.db 重新加载失败: "`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		loadDifficultyCache()
		cleared = len(sortedModMetas())
		msg = fmt.Sprintf("已清理数据库缓存，已从 mods.db 重新加载 %d 个模组元数据", cleared)
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"source":  body.Source,
		"cleared": cleared,
		"message": msg,
	})
}
