package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ========== GitHub 缓存入口 ==========

func getGitHubInfoCached(repoPath string, ttl time.Duration) *GitHubInfo {
	if repoPath == "" || !dsEnabled("github") {
		return nil
	}
	cacheKey := "gh:" + repoPath

	// 读取缓存（含过期状态）
	v, found, stale := readCacheStale(cacheKey)
	if found && v != nil {
		info := v.(*GitHubInfo)
		if stale {
			// 后台异步刷新
			log.Printf("[~] [%s] 缓存已过期，先返回旧数据并后台刷新", cacheKey)
			go func() {
				newInfo := fetchGitHubInfo(repoPath)
				if newInfo != nil {
					writeCache(cacheKey, newInfo, ttl)
				}
			}()
		}
		return info
	}

	// 缓存未命中 → 同步拉取（避免首次访问返回空）
	log.Printf("[~] [%s] 缓存未命中，同步拉取", cacheKey)
	info := fetchGitHubInfo(repoPath)
	if info != nil {
		writeCache(cacheKey, info, ttl)
	}
	return info
}

// ========== GitHub Rate Limit 自动检测（按 API Key / owner 共享） ==========

// ghRateInfo 从响应头中提取的速率限制信息
type ghRateInfo struct {
	Limit     int   // X-RateLimit-Limit
	Remaining int   // X-RateLimit-Remaining
	Reset     int64 // X-RateLimit-Reset (UTC unix seconds)
	RetrySecs int   // Retry-After (secondary rate limit)
}

// ghRateTracker 按 owner（API key）跟踪速率限制状态，所有 goroutine 共享
type ghRateTracker struct {
	blockedUntil  time.Time // 限速时阻塞到这个时间
	lastRemaining int       // 上次看到的剩余次数
	lastReqTime   time.Time // 上次请求时刻，用于 pacing
	lastLogPct    int       // 上次日志打印时的百分比，避免刷屏
}

var (
	ghRateMu  sync.Mutex
	ghRateMap = map[string]*ghRateTracker{}
)

// readRateLimitHeaders 从 HTTP 响应头读取 GitHub 速率限制信息
func readRateLimitHeaders(resp *http.Response) ghRateInfo {
	var rl ghRateInfo
	if v := resp.Header.Get("X-RateLimit-Limit"); v != "" {
		fmt.Sscanf(v, "%d", &rl.Limit)
	}
	if v := resp.Header.Get("X-RateLimit-Remaining"); v != "" {
		fmt.Sscanf(v, "%d", &rl.Remaining)
	}
	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		fmt.Sscanf(v, "%d", &rl.Reset)
	}
	if v := resp.Header.Get("Retry-After"); v != "" {
		fmt.Sscanf(v, "%d", &rl.RetrySecs)
	}
	return rl
}

// ghRateBeforeRequest 请求前检查：若被限速则等待；余量低时主动 pacing 防止耗尽
func ghRateBeforeRequest(owner string) {
	ghRateMu.Lock()
	tr := ghRateMap[owner]
	if tr == nil {
		ghRateMu.Unlock()
		return
	}
	blockedUntil := tr.blockedUntil
	lastRemaining := tr.lastRemaining
	lastReqTime := tr.lastReqTime
	ghRateMu.Unlock()

	// 1) 已被阻塞 → 等待解封
	wait := time.Until(blockedUntil)
	if wait > 0 {
		log.Printf("[Rate] GH/%s 被限速，等待 %v", owner, wait.Round(time.Second))
		time.Sleep(wait + 500*time.Millisecond)
		return
	}

	// 2) 余量 < 20% (1000/5000) 时主动 pacing：每请求间至少间隔
	if lastRemaining > 0 && lastRemaining < 1000 {
		var minGap time.Duration
		switch {
		case lastRemaining < 20:
			minGap = 2 * time.Second
		case lastRemaining < 100:
			minGap = 500 * time.Millisecond
		default:
			minGap = 100 * time.Millisecond
		}
		elapsed := time.Since(lastReqTime)
		if elapsed < minGap {
			time.Sleep(minGap - elapsed)
		}
	}
}

// isRateLimited 检查该 owner 当前是否处于限速阻塞状态（用于区分 403 原因）
func isRateLimited(owner string) bool {
	ghRateMu.Lock()
	defer ghRateMu.Unlock()
	tr := ghRateMap[owner]
	if tr == nil {
		return false
	}
	return time.Now().Before(tr.blockedUntil)
}

// ghRateAfterResponse 请求后更新共享状态
func ghRateAfterResponse(owner string, resp *http.Response) {
	rl := readRateLimitHeaders(resp)
	if rl.Limit <= 0 {
		return
	}

	ghRateMu.Lock()
	tr := ghRateMap[owner]
	if tr == nil {
		tr = &ghRateTracker{}
		ghRateMap[owner] = tr
	}
	tr.lastRemaining = rl.Remaining
	tr.lastReqTime = time.Now()

	// 主速率耗尽 → 设置阻塞标志
	if rl.Remaining == 0 && rl.Reset > 0 {
		resetTime := time.Unix(rl.Reset, 0)
		if resetTime.After(tr.blockedUntil) {
			tr.blockedUntil = resetTime
			log.Printf("[Rate] GH/%s 主速率耗尽，阻塞至 %s", owner, resetTime.Format("15:04:05"))
		}
	}
	// 二级速率限制
	if rl.RetrySecs > 0 {
		until := time.Now().Add(time.Duration(rl.RetrySecs) * time.Second)
		if until.After(tr.blockedUntil) {
			tr.blockedUntil = until
			log.Printf("[Rate] GH/%s 二级速率限制，阻塞 %ds", owner, rl.RetrySecs)
		}
	}

	remaining := rl.Remaining
	blockedUntil := tr.blockedUntil

	// 减量日志
	curPct := 0
	if rl.Limit > 0 {
		curPct = remaining * 100 / rl.Limit
	}
	shouldLog := false
	if remaining <= 10 || curPct < 10 {
		if curPct/2 != tr.lastLogPct/2 || tr.lastLogPct == 0 {
			shouldLog = true
		}
	} else if curPct < 50 {
		if curPct/10 != tr.lastLogPct/10 {
			shouldLog = true
		}
	}
	if shouldLog {
		tr.lastLogPct = curPct
	}
	ghRateMu.Unlock()

	if shouldLog {
		var resetStr string
		if rl.Reset > 0 {
			resetStr = time.Unix(rl.Reset, 0).Format("15:04:05")
		}
		log.Printf("[Rate] GH/%s 剩余 %d/%d (%d%%)，重置于 %s，阻塞至 %s",
			owner, remaining, rl.Limit, curPct,
			resetStr, blockedUntil.Format("15:04:05"))
	}
}

// setGHHeaders 统一设置 GitHub API 请求头
func setGHHeaders(req *http.Request, token string) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "ModList-Dashboard")
}

// ========== GitHub Issues/PRs 批量拉取 ==========

func fetchGitHubAll(owner, repo string) ([]GitHubItem, []GitHubItem) {
	token := authConfig.gitHubToken(owner)
	if token == "" {
		log.Printf("[!] 未配置 %s 的 GitHub Token，跳过 %s/%s", owner, owner, repo)
		return nil, nil
	}

	maxAttempts := config.Retry.attempts()
	baseDelay := config.Retry.delayMs()
	httpClient := &http.Client{Timeout: 30 * time.Second}

	ghRateBeforeRequest(owner)

	// Issues
	issuesURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues?state=all&per_page=100", owner, repo)
	var issues []GitHubItem

	for page := 1; page <= 10; page++ {
		url := issuesURL + fmt.Sprintf("&page=%d", page)
		var ok, lastPage bool
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			if attempt > 1 {
				delay := time.Duration(baseDelay<<(attempt-2)) * time.Millisecond
				time.Sleep(delay)
				log.Printf("[~] Issues 重试 %d/%d (%s/%s page=%d)", attempt, maxAttempts, owner, repo, page)
			}
			ghRateBeforeRequest(owner)

			req, _ := http.NewRequest("GET", url, nil)
			setGHHeaders(req, token)
			ghLimiter <- struct{}{}
			resp, err := httpClient.Do(req)
			<-ghLimiter
			if err != nil {
				log.Printf("[X] Issues 网络错误 (%s/%s page=%d): %v", owner, repo, page, err)
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			ghRateAfterResponse(owner, resp)

			if resp.StatusCode != 200 {
				if resp.StatusCode == 429 || (resp.StatusCode == 403 && isRateLimited(owner)) {
					ghRateBeforeRequest(owner)
					log.Printf("[Rate] Issues %d (%s/%s page=%d)，限速等待后重试", resp.StatusCode, owner, repo, page)
					attempt--
					continue
				}
				if resp.StatusCode == 403 {
					log.Printf("[X] Issues 403 (%s/%s page=%d): %s", owner, repo, page, string(body))
					return nil, nil
				}
				if !isRetryable(resp.StatusCode) {
					log.Printf("[X] Issues 返回 %d (%s/%s page=%d)，不重试", resp.StatusCode, owner, repo, page)
					return nil, nil
				}
				log.Printf("[X] Issues 返回 %d (%s/%s page=%d)，重试中", resp.StatusCode, owner, repo, page)
				continue
			}

			pageIssues, rawCount := parseIssuesFromBody(body)
			issues = append(issues, pageIssues...)
			// 用原始响应条数（不过滤 PR）判断是否最后一页，
			// 因为 GitHub /issues 端点混入了 PR，过滤后数量虚低
			if rawCount < 100 {
				lastPage = true
			}
			ok = true
			break
		}
		if !ok || lastPage {
			break
		}
	}

	// PRs
	pullsURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls?state=all&per_page=100", owner, repo)
	var prs []GitHubItem

	for page := 1; page <= 10; page++ {
		url := pullsURL + fmt.Sprintf("&page=%d", page)
		var ok, lastPage bool
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			if attempt > 1 {
				delay := time.Duration(baseDelay<<(attempt-2)) * time.Millisecond
				time.Sleep(delay)
				log.Printf("[~] PRs 重试 %d/%d (%s/%s page=%d)", attempt, maxAttempts, owner, repo, page)
			}
			ghRateBeforeRequest(owner)

			req, _ := http.NewRequest("GET", url, nil)
			setGHHeaders(req, token)
			ghLimiter <- struct{}{}
			resp, err := httpClient.Do(req)
			<-ghLimiter
			if err != nil {
				log.Printf("[X] PRs 网络错误 (%s/%s page=%d): %v", owner, repo, page, err)
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			ghRateAfterResponse(owner, resp)

			if resp.StatusCode != 200 {
				if resp.StatusCode == 429 || (resp.StatusCode == 403 && isRateLimited(owner)) {
					ghRateBeforeRequest(owner)
					log.Printf("[Rate] PRs %d (%s/%s page=%d)，限速等待后重试", resp.StatusCode, owner, repo, page)
					attempt--
					continue
				}
				if resp.StatusCode == 403 {
					log.Printf("[X] PRs 403 (%s/%s page=%d): %s", owner, repo, page, string(body))
					break
				}
				if !isRetryable(resp.StatusCode) {
					log.Printf("[X] PRs 返回 %d (%s/%s page=%d)，不重试", resp.StatusCode, owner, repo, page)
					break
				}
				log.Printf("[X] PRs 返回 %d (%s/%s page=%d)，重试中", resp.StatusCode, owner, repo, page)
				continue
			}

			pagePRs := parsePRsFromBody(body)
			prs = append(prs, pagePRs...)
			if len(pagePRs) < 100 {
				lastPage = true
			}
			ok = true
			break
		}
		if !ok || lastPage {
			break
		}
	}

	return issues, prs
}

func parseIssuesFromBody(body []byte) ([]GitHubItem, int) {
	var raw []struct {
		Number      int    `json:"number"`
		Title       string `json:"title"`
		State       string `json:"state"`
		HTMLURL     string `json:"html_url"`
		CreatedAt   string `json:"created_at"`
		StateReason string `json:"state_reason"`
		User        struct {
			Login string `json:"login"`
		} `json:"user"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		PullRequest *struct{} `json:"pull_request"`
	}
	json.Unmarshal(body, &raw)
	rawCount := len(raw)

	var issues []GitHubItem
	for _, r := range raw {
		if r.PullRequest != nil {
			continue
		}
		item := GitHubItem{
			Number: r.Number, Title: r.Title, State: r.State,
			StateReason: r.StateReason, URL: r.HTMLURL,
			CreatedAt: r.CreatedAt, User: r.User.Login,
		}
		for _, l := range r.Labels {
			item.Labels = append(item.Labels, l.Name)
		}
		issues = append(issues, item)
	}
	return issues, rawCount
}

func parsePRsFromBody(body []byte) []GitHubItem {
	var raw []struct {
		Number    int    `json:"number"`
		Title     string `json:"title"`
		State     string `json:"state"`
		Draft     bool   `json:"draft"`
		HTMLURL   string `json:"html_url"`
		CreatedAt string `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		MergedAt *string `json:"merged_at"`
	}
	json.Unmarshal(body, &raw)

	var prs []GitHubItem
	for _, r := range raw {
		item := GitHubItem{
			Number: r.Number, Title: r.Title, State: r.State,
			Draft: r.Draft, Merged: r.MergedAt != nil && *r.MergedAt != "",
			URL: r.HTMLURL, CreatedAt: r.CreatedAt,
			User: r.User.Login,
		}
		for _, l := range r.Labels {
			item.Labels = append(item.Labels, l.Name)
		}
		prs = append(prs, item)
	}
	return prs
}

func fetchGitHubInfo(repoPath string) *GitHubInfo {
	parts := strings.SplitN(repoPath, "/", 2)
	if len(parts) != 2 {
		return nil
	}
	issues, prs := fetchGitHubAll(parts[0], parts[1])

	openIssues := 0
	for _, i := range issues {
		if i.State == "open" {
			openIssues++
		}
	}
	openPRs := 0
	for _, p := range prs {
		if p.State == "open" {
			openPRs++
		}
	}
	return &GitHubInfo{OpenIssues: openIssues, OpenPRs: openPRs, Issues: issues, PullRequests: prs}
}

// ========== GitHub 仓库归档状态（本地配置） ==========

// isRepoArchived 根据 repo path 查找对应 mod 的 status，判断是否为归档状态
func isRepoArchived(owner, repo string) bool {
	repoPath := owner + "/" + repo
	for _, m := range sortedModMetas() {
		if m.IDs.GH == repoPath {
			return isStatusArchived(m.Status)
		}
	}
	return false
}

// markRepoArchived 根据本地模组状态为仓库内每条 Issue/PR 打上 archived 标记，
// 使前端能立即按“已归档”过滤，无需等待 GitHub 缓存过期或手动刷新。
// 同时按条目重新计算 open 数量：open_issues/open_prs 表示仓库真实打开的条目数，
// 不受本地归档影响（归档只用于前端列表过滤，避免缓存里存着过期的统计）。
func markRepoArchived(info *GitHubInfo, repoPath string) {
	if info == nil || repoPath == "" {
		return
	}
	owner, repo, ok := strings.Cut(repoPath, "/")
	archived := ok && isRepoArchived(owner, repo)
	openIssues, openPRs := 0, 0
	for i := range info.Issues {
		info.Issues[i].Archived = archived
		if info.Issues[i].State == "open" {
			openIssues++
		}
	}
	for i := range info.PullRequests {
		info.PullRequests[i].Archived = archived
		if info.PullRequests[i].State == "open" {
			openPRs++
		}
	}
	info.OpenIssues = openIssues
	info.OpenPRs = openPRs
}

// ========== GitHub Issue / PR 详情 ==========

func fetchGitHubIssueDetail(owner, repo string, issueNumber int) *GitHubIssueDetail {
	token := authConfig.gitHubToken(owner)
	if token == "" {
		log.Printf("[!] 未配置 %s 的 GitHub Token，无法获取 Issue 详情", owner)
		return nil
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d", owner, repo, issueNumber)
	ghRateBeforeRequest(owner)
	req, _ := http.NewRequest("GET", url, nil)
	setGHHeaders(req, token)

	ghLimiter <- struct{}{}
	resp, err := http.DefaultClient.Do(req)
	<-ghLimiter
	if err != nil {
		log.Printf("[X] GitHub Issue 详情网络错误 (%s/%s#%d): %v", owner, repo, issueNumber, err)
		return nil
	}
	defer resp.Body.Close()
	ghRateAfterResponse(owner, resp)

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[X] GitHub Issue 详情返回 %d (%s/%s#%d): %s", resp.StatusCode, owner, repo, issueNumber, string(body))
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	var raw struct {
		Number    int    `json:"number"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		State     string `json:"state"`
		HTMLURL   string `json:"html_url"`
		CreatedAt string `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		StateReason string `json:"state_reason"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		log.Printf("[X] GitHub Issue 详情解析失败: %v", err)
		return nil
	}
	d := &GitHubIssueDetail{
		Number:      raw.Number,
		Title:       raw.Title,
		Body:        raw.Body,
		State:       raw.State,
		StateReason: raw.StateReason,
		HTMLURL:     raw.HTMLURL,
		CreatedAt:   raw.CreatedAt,
		User:        raw.User.Login,
	}
	for _, l := range raw.Labels {
		d.Labels = append(d.Labels, l.Name)
	}
	return d
}

func fetchGitHubPRDetail(owner, repo string, prNumber int) *GitHubPRDetail {
	token := authConfig.gitHubToken(owner)
	if token == "" {
		log.Printf("[!] 未配置 %s 的 GitHub Token，无法获取 PR 详情", owner)
		return nil
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repo, prNumber)
	ghRateBeforeRequest(owner)
	req, _ := http.NewRequest("GET", url, nil)
	setGHHeaders(req, token)

	ghLimiter <- struct{}{}
	resp, err := http.DefaultClient.Do(req)
	<-ghLimiter
	if err != nil {
		log.Printf("[X] GitHub PR 详情网络错误 (%s/%s#%d): %v", owner, repo, prNumber, err)
		return nil
	}
	defer resp.Body.Close()
	ghRateAfterResponse(owner, resp)

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[X] GitHub PR 详情返回 %d (%s/%s#%d): %s", resp.StatusCode, owner, repo, prNumber, string(body))
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	var raw struct {
		Number    int    `json:"number"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		State     string `json:"state"`
		HTMLURL   string `json:"html_url"`
		CreatedAt string `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Base struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"base"`
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		log.Printf("[X] GitHub PR 详情解析失败: %v", err)
		return nil
	}
	d := &GitHubPRDetail{
		Number: raw.Number, Title: raw.Title, Body: raw.Body,
		State: raw.State, HTMLURL: raw.HTMLURL, CreatedAt: raw.CreatedAt,
		User: raw.User.Login, BaseRef: raw.Base.Ref, HeadRef: raw.Head.Ref,
	}
	for _, l := range raw.Labels {
		d.Labels = append(d.Labels, l.Name)
	}
	d.Diff = generateLocalDiff(owner, repo, raw.Base.SHA, raw.Head.SHA)
	if d.Diff == "" {
		log.Printf("[!] PR #%d 本地 diff 为空（可能仓库未克隆或 base/head 不存在）", prNumber)
	}
	return d
}

func generateLocalDiff(owner, repo, baseSHA, headSHA string) string {
	var repoPath string
	for _, root := range ProjectPaths {
		candidate := filepath.Join(root, repo)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			if _, err := os.Stat(filepath.Join(candidate, ".git")); err == nil {
				repoPath = candidate
				break
			}
		}
	}
	if repoPath == "" {
		log.Printf("[X] 找不到本地仓库 %s/%s (已检查 %v)", owner, repo, ProjectPaths)
		return ""
	}

	log.Printf("[Git] cd %s && git fetch origin %s %s", repoPath, baseSHA[:8], headSHA[:8])
	fetchCmd := exec.Command("git", "fetch", "origin", baseSHA, headSHA)
	fetchCmd.Dir = repoPath
	if out, err := fetchCmd.CombinedOutput(); err != nil {
		log.Printf("[X] git fetch 失败 (%s): %v\n%s", repoPath, err, string(out))
	} else {
		log.Printf("[Git] fetch 成功 (%d bytes output)", len(out))
	}

	log.Printf("[Git] cd %s && git diff %s %s", repoPath, baseSHA[:8], headSHA[:8])
	diffCmd := exec.Command("git", "diff", baseSHA, headSHA)
	diffCmd.Dir = repoPath
	diffOut, err := diffCmd.CombinedOutput()
	if err != nil {
		log.Printf("[X] git diff 失败 (%s): %v\n%s", repoPath, err, string(diffOut))
		return ""
	}

	diff := string(diffOut)
	const maxDiff = 200 * 1024
	if len(diff) > maxDiff {
		log.Printf("[!] PR diff 过大 (%d bytes)，截断至 %d bytes", len(diff), maxDiff)
		diff = diff[:maxDiff] + "\n\n... (diff 被截断，完整内容请查看 PR 页面)"
	}
	log.Printf("[√] PR diff 已生成 (%s/%s#%s: %d bytes)", owner, repo, baseSHA[:8], len(diff))
	return diff
}

// ========== GitHub Commit Hash（Codacy 用） ==========

// fetchLatestCommitHash 获取最新 commit hash（不缓存，每次调用都拉取）
func fetchLatestCommitHash(owner, repo string) string {
	token := authConfig.gitHubToken(owner)
	if token == "" {
		return ""
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits?per_page=1", owner, repo)
	req, _ := http.NewRequest("GET", url, nil)
	setGHHeaders(req, token)

	ghRateBeforeRequest(owner)
	ghLimiter <- struct{}{}
	resp, err := http.DefaultClient.Do(req)
	<-ghLimiter
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			ghRateAfterResponse(owner, resp)
			resp.Body.Close()
		}
		return ""
	}
	defer resp.Body.Close()
	ghRateAfterResponse(owner, resp)

	body, _ := io.ReadAll(resp.Body)
	var commits []struct {
		SHA string `json:"sha"`
	}
	json.Unmarshal(body, &commits)
	if len(commits) > 0 {
		return commits[0].SHA
	}
	return ""
}
