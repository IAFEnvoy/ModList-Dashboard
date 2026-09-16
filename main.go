package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"modlist-dashboard/agent"
	"modlist-dashboard/badge"
)

func main() {
	initFFmpeg()
	ensureConfigFiles()
	loadConfig("config/config.yml", &config)
	LoadProjectPaths(config.ProjectPaths)
	log.Printf("[Path] 项目工作目录: %v", ProjectPaths)
	// 注入到 agent 包
	agent.AgentProjectPaths = ProjectPaths
	agent.ModMetaLookup = func(name string) *agent.ProjectMeta {
		for _, m := range sortedModMetas() {
			if m.Name == name {
				return &agent.ProjectMeta{Name: m.Name, RepoGH: m.IDs.GH}
			}
		}
		return nil
	}
	loadConfig("config/auth.yml", &authConfig)
	if err := InitUserDB(); err != nil {
		log.Fatalf("[Auth] 初始化用户数据库失败: %v", err)
	}
	defer CloseUserDB()
	if err := InitModMetaDB(); err != nil {
		log.Fatalf("[DB] 初始化模组元数据数据库失败: %v", err)
	}
	defer CloseModMetaDB()
	loadModMetas()
	loadDifficultyCache()
	loadConfig("config/rate.yml", &rateConfig)
	loadStatusConfig()

	// 初始化 SQLite 数据库（必须在 agent.LoadAll() 之前）
	if err := agent.InitDB(); err != nil {
		log.Fatalf("[DB] 初始化失败: %v", err)
	}
	defer agent.CloseDB()

	agent.LoadAll()

	// 初始化网盘目录
	initDrive()

	// 注入徽章包依赖：模组元数据、缓存、上游抓取、配置与内嵌资源
	initBadge()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/mods", corsMiddleware(handleModsList))
	mux.HandleFunc("/api/mods/", corsMiddleware(handleModDetail))
	mux.HandleFunc("/api/admin/mods", corsMiddleware(withAuth(handleAdminMods)))
	mux.HandleFunc("/api/admin/mods/", corsMiddleware(withAuth(handleAdminMod)))
	mux.HandleFunc("/api/github/issue/", corsMiddleware(withAuth(handleGitHubIssue)))
	mux.HandleFunc("/api/github/pr/", corsMiddleware(withAuth(handleGitHubPR)))
	mux.HandleFunc("/api/github/", corsMiddleware(handleGitHub))
	mux.HandleFunc("/api/logo/", corsMiddleware(handleModLogo))
	mux.HandleFunc(badgePrefixPath(), corsMiddleware(badge.Handler))
	mux.HandleFunc("/api/status-order", corsMiddleware(handleStatusOrder))
	mux.HandleFunc("/api/config", corsMiddleware(handlePublicConfig))
	mux.HandleFunc("/api/auth/login", corsMiddleware(handleLogin))
	mux.HandleFunc("/api/auth/status", corsMiddleware(handleAuthStatus))
	mux.HandleFunc("/api/auth/password", corsMiddleware(withAuth(handleChangePassword)))
	mux.HandleFunc("/api/auth/users", corsMiddleware(withAuth(handleAuthUsers)))
	mux.HandleFunc("/api/auth/users/", corsMiddleware(withAuth(handleAuthUser)))
	mux.HandleFunc("/api/todos/counts", corsMiddleware(handleTodoCounts))
	mux.HandleFunc("/api/todos", corsMiddleware(handleTodos))
	mux.HandleFunc("/api/admin/flush-cache", corsMiddleware(withAuth(handleFlushCache)))
	mux.HandleFunc("/api/todos/", corsMiddleware(handleTodos))
	// Agent API — 需要登录
	mux.HandleFunc("/api/agent/info", corsMiddleware(withAuth(agent.HandleAgentInfo)))
	mux.HandleFunc("/api/agent/sessions", corsMiddleware(withAuth(agent.HandleAgentSessions)))
	mux.HandleFunc("/api/agent/sessions/", corsMiddleware(withAuth(agent.HandleAgentSessions)))
	mux.HandleFunc("/api/agent/chat/stream", corsMiddleware(withAuth(agent.HandleAgentChatStream)))
	mux.HandleFunc("/api/agent/compact", corsMiddleware(withAuth(agent.HandleAgentCompact)))
	mux.HandleFunc("/api/agent/mcp/refresh", corsMiddleware(withAuth(agent.HandleMCPRefresh)))
	mux.HandleFunc("/api/agent/answer", corsMiddleware(withAuth(agent.HandleAgentAnswer)))
	mux.HandleFunc("/api/agent/approval", corsMiddleware(withAuth(agent.HandleAgentApproval)))
	mux.HandleFunc("/api/agent/stop", corsMiddleware(withAuth(agent.HandleAgentStop)))
	mux.HandleFunc("/api/agent/search", corsMiddleware(withAuth(agent.HandleAgentSearch)))
	mux.HandleFunc("/api/agent/search/sessions", corsMiddleware(withAuth(agent.HandleAgentSearchSessions)))
	mux.HandleFunc("/api/agent/share", corsMiddleware(withAuth(agent.HandleAgentShare)))
	mux.HandleFunc("/api/agent/share/", corsMiddleware(agent.HandleAgentShareGet))
	mux.Handle("/share/", noCacheMiddleware(http.HandlerFunc(agent.HandleSharePage)))
	// Drive API — 需要登录
	mux.HandleFunc("/api/drive/list", corsMiddleware(withAuth(handleDriveList)))
	mux.HandleFunc("/api/drive/list/", corsMiddleware(withAuth(handleDriveList)))
	mux.HandleFunc("/api/drive/upload", corsMiddleware(withAuth(handleDriveUpload)))
	mux.HandleFunc("/api/drive/file", corsMiddleware(withAuth(handleDriveDownloadOrView)))
	mux.HandleFunc("/api/drive/mkdir", corsMiddleware(withAuth(handleDriveMkdir)))
	mux.HandleFunc("/api/drive/delete", corsMiddleware(withAuth(handleDriveDelete)))
	mux.HandleFunc("/api/drive/rename", corsMiddleware(withAuth(handleDriveRename)))
	mux.HandleFunc("/api/drive/save", corsMiddleware(withAuth(handleDriveSave)))
	mux.HandleFunc("/favicon.ico", handleFavicon)
	mux.Handle("/", noCacheMiddleware(http.HandlerFunc(handleStatic)))

	addr := fmt.Sprintf(":%d", config.Server.Port)
	log.Printf("[Auth] CurseForge API Key: %s", maskKey(authConfig.CurseForge.APIKey))
	log.Printf("[Auth] GitHub Tokens: %d 个账号已配置", len(authConfig.GitHub.Tokens))
	for owner := range authConfig.GitHub.Tokens {
		log.Printf("   - %s: %s", owner, maskKey(authConfig.gitHubToken(owner)))
	}
	log.Printf("[~] 重试策略: 最多%d次, 基础延迟%dms", config.Retry.attempts(), config.Retry.delayMs())

	cfLimiter = config.DataSources["curseforge"].limiter()
	ghLimiter = config.DataSources["github"].limiter()

	cacheLoaded := loadCacheFromDisk()
	if cacheLoaded > 0 {
		log.Printf("[Cache] 已从磁盘恢复 %d 条缓存", cacheLoaded)
	}

	cdLimiter = config.Codacy.limiter()
	if config.Codacy.Enabled && authConfig.Codacy.APIToken != "" && authConfig.Codacy.APIToken != "YOUR_CODACY_API_TOKEN" {
		log.Printf("[Codacy] Codacy: 已启用, 并发%d, 缓存TTL=%v", config.Codacy.Concurrency, config.Codacy.ttl())
		go startCodacyWorker()
	}

	// 缓存未命中时不阻塞请求（返回零值 + 后台异步加载）
	// 预热仅加速首次填充，不与请求耦合
	go prewarmCache()

	log.Printf("[Start] 模组看板启动于 http://localhost%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

// handleFavicon 让 /favicon.ico 也返回 webp 图标（静态页 head 中已通过 <link> 指向 /favicon.webp）
func handleFavicon(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(filepath.Join("static", "favicon.webp"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data)
}

// staticResponseWriter 拦截 http.FileServer 的 404，交由 handleStatic 输出自定义 404 页面
type staticResponseWriter struct {
	http.ResponseWriter
	notFound bool
}

func (w *staticResponseWriter) WriteHeader(code int) {
	if code == http.StatusNotFound {
		w.notFound = true
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *staticResponseWriter) Write(b []byte) (int, error) {
	if w.notFound {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

func (w *staticResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// handleStatic 提供静态文件；文件不存在时返回自定义 404 页面（带 404 状态码）
func handleStatic(w http.ResponseWriter, r *http.Request) {
	rec := &staticResponseWriter{ResponseWriter: w}
	http.FileServer(http.Dir("static")).ServeHTTP(rec, r)
	if rec.notFound {
		data, err := os.ReadFile(filepath.Join("static", "404.html"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		w.Write(data)
	}
}

func prewarmCache() {
	metas := sortedModMetas()
	total := len(metas)
	log.Printf("[Warm] 开始预热缓存 (%d 个模组)...", total)
	startTime := time.Now()

	ids := make([]string, 0, total)
	for _, meta := range metas {
		ids = append(ids, meta.ID)
	}

	ttl := time.Duration(config.Server.CacheTTLMin) * time.Minute
	if ttl == 0 {
		ttl = 30 * time.Minute
	}

	var wg sync.WaitGroup
	var completed atomic.Int32

	for _, id := range ids {
		wg.Add(1)
		go func(modID string) {
			defer wg.Done()
			meta, ok := getModMeta(modID)
			if !ok {
				return
			}

			var innerWg sync.WaitGroup
			innerWg.Add(2)
			go func() {
				defer innerWg.Done()
				getCachedCFDownloads(meta.IDs.CF, ttl)
			}()
			go func() {
				defer innerWg.Done()
				if meta.IDs.GH != "" {
					getGitHubInfoCached(meta.IDs.GH, ttl)
				}
			}()
			innerWg.Wait()

			n := completed.Add(1)
			log.Printf("   [%d/%d] [√] %s 已预热", n, total, meta.Name)
		}(id)
	}
	wg.Wait()

	elapsed := time.Since(startTime)
	log.Printf("[Warm] 缓存预热完成，耗时 %v", elapsed.Round(time.Millisecond))
}
