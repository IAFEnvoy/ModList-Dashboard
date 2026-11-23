package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"modlist-dashboard/agent"
	"modlist-dashboard/badge"
)

type issuePreprocessReport struct {
	Status    string `json:"status"`
	SessionID string `json:"session_id,omitempty"`
	Error     string `json:"error,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

type issuePreprocessState struct {
	Enabled bool                             `json:"enabled"`
	Reports map[string]issuePreprocessReport `json:"reports"`
	// GitHub allocates increasing issue numbers within each repository.
	LastIssueNumbers map[string]int `json:"last_issue_numbers,omitempty"`
}

type issuePreprocessService struct {
	mu            sync.Mutex
	path          string
	state         issuePreprocessState
	observedRepos map[string]bool // Successful upstream snapshots in this process, never persisted.
	wake          chan struct{}
	analyze       func(ModMeta, GitHubItem, func(string) error) (string, error)
}

var issuePreprocessor *issuePreprocessService
var errIssueNoLongerOpen = errors.New("Issue 已关闭，跳过预处理")

func newIssuePreprocessService(path string) (*issuePreprocessService, error) {
	s := &issuePreprocessService{
		path: path, wake: make(chan struct{}, 1), analyze: analyzeIssue,
		state: issuePreprocessState{Reports: make(map[string]issuePreprocessReport)}, observedRepos: make(map[string]bool),
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(data, &s.state); err != nil {
			return nil, fmt.Errorf("读取预处理设置失败: %w", err)
		}
	}
	if s.state.Reports == nil {
		s.state.Reports = make(map[string]issuePreprocessReport)
	}
	if s.state.LastIssueNumbers == nil {
		s.state.LastIssueNumbers = make(map[string]int)
	}
	interrupted := false
	for key, report := range s.state.Reports {
		if report.Status == "running" {
			report.Status, report.Error = "failed", "服务重启中断了预处理，可手动重试"
			report.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			s.state.Reports[key] = report
			interrupted = true
		}
	}
	if interrupted {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// saveLocked replaces the state atomically; callers roll back memory on error.
func (s *issuePreprocessService) saveLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".issue-preprocess-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), s.path)
}

func (s *issuePreprocessService) snapshot() issuePreprocessState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := issuePreprocessState{Enabled: s.state.Enabled, Reports: make(map[string]issuePreprocessReport, len(s.state.Reports))}
	for key, report := range s.state.Reports {
		state.Reports[key] = report
	}
	return state
}

func (s *issuePreprocessService) enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Enabled
}

func (s *issuePreprocessService) setEnabled(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.state.Enabled
	s.state.Enabled = enabled
	if err := s.saveLocked(); err != nil {
		s.state.Enabled = prev
		return err
	}
	s.notify()
	notifyGitHubRefresh()
	return nil
}

func (s *issuePreprocessService) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// The first successful upstream snapshot per repository after every restart
// only calibrates the baseline, regardless of disk cache or persisted state.
// Later TTL/manual refresh events queue additions. Cache deletion never resets
// the baseline, startup guard, queued tasks or existing reports.
func (s *issuePreprocessService) observeIssues(meta ModMeta, issues []GitHubItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	repo := strings.ToLower(meta.IDs.GH)
	previous, initialized := s.state.LastIssueNumbers[repo]
	observed := s.observedRepos[repo]
	latest := previous
	var queued []string
	for _, item := range issues {
		if item.Number > latest {
			latest = item.Number
		}
		if !observed || !s.state.Enabled || item.Number <= previous || item.State != "open" || item.Archived || isStatusArchived(meta.Status) {
			continue
		}
		key := issuePreprocessKey(repo, item.Number)
		if _, exists := s.state.Reports[key]; !exists {
			s.state.Reports[key] = issuePreprocessReport{Status: "queued", UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
			queued = append(queued, key)
		}
	}
	if initialized && latest == previous {
		s.observedRepos[repo] = true
		return nil
	}
	s.state.LastIssueNumbers[repo] = latest
	if err := s.saveLocked(); err != nil {
		for _, key := range queued {
			delete(s.state.Reports, key)
		}
		if initialized {
			s.state.LastIssueNumbers[repo] = previous
		} else {
			delete(s.state.LastIssueNumbers, repo)
		}
		return err
	}
	s.observedRepos[repo] = true
	if len(queued) > 0 {
		s.notify()
	}
	return nil
}

func issuePreprocessProject(repo string) (ModMeta, bool) {
	var archived *ModMeta
	for _, meta := range sortedModMetas() {
		if strings.EqualFold(meta.IDs.GH, repo) {
			if !isStatusArchived(meta.Status) {
				return meta, true
			}
			copy := meta
			archived = &copy
		}
	}
	if archived != nil {
		return *archived, true
	}
	return ModMeta{}, false
}

// Called only with a successfully refreshed GitHub issue list.
func observeGitHubIssues(repo string, info *badge.GitHubInfo) {
	s := issuePreprocessor
	if info == nil {
		return
	}
	if meta, ok := issuePreprocessProject(repo); ok {
		notifyBotIssues(meta, info.Issues)
		if s != nil {
			if err := s.observeIssues(meta, info.Issues); err != nil {
				log.Printf("[Issue] 无法保存 Issue 增量 %s: %v", repo, err)
			}
		}
	}
}

func issuePreprocessKey(repo string, number int) string {
	return strings.ToLower(repo) + "#" + strconv.Itoa(number)
}

func (s *issuePreprocessService) setReportLocked(key string, report issuePreprocessReport) error {
	prev, exists := s.state.Reports[key]
	report.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	s.state.Reports[key] = report
	if err := s.saveLocked(); err != nil {
		if exists {
			s.state.Reports[key] = prev
		} else {
			delete(s.state.Reports, key)
		}
		return err
	}
	return nil
}

func (s *issuePreprocessService) processIssue(meta ModMeta, item GitHubItem) {
	if item.State != "open" || item.Archived || isStatusArchived(meta.Status) {
		return
	}
	key := issuePreprocessKey(meta.IDs.GH, item.Number)
	s.mu.Lock()
	report, exists := s.state.Reports[key]
	if !s.state.Enabled || !exists || report.Status != "queued" {
		s.mu.Unlock()
		return
	}
	err := s.setReportLocked(key, issuePreprocessReport{Status: "running"})
	s.mu.Unlock()
	if err != nil {
		log.Printf("[Issue] 无法记录预处理任务 %s: %v", key, err)
		return
	}
	sid, err := s.analyze(meta, item, func(sid string) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.setReportLocked(key, issuePreprocessReport{Status: "running", SessionID: sid})
	})
	report = issuePreprocessReport{Status: "completed", SessionID: sid}
	if err != nil {
		report.Status, report.Error = "failed", err.Error()
		if errors.Is(err, errIssueNoLongerOpen) {
			report.Status = "skipped"
		} else {
			log.Printf("[Issue] 预处理失败 %s: %v", key, err)
		}
	}
	s.mu.Lock()
	saveErr := s.setReportLocked(key, report)
	s.mu.Unlock()
	if saveErr != nil {
		log.Printf("[Issue] 无法保存预处理结果 %s: %v", key, saveErr)
	} else if report.Status == "completed" {
		notifyBotReport(meta, item.Number, report.SessionID)
	}
}

// Refresh and dispatch run independently: a long model task cannot delay TTL
// refreshes. Dispatch recovers the persisted queue once at startup, then only
// wakes for additions, explicit retries, switch changes or config reloads.
func (s *issuePreprocessService) run() {
	for {
		s.dispatchQueued()
		<-s.wake
	}
}

func (s *issuePreprocessService) dispatchQueued() {
	if !s.enabled() || !dsEnabled("github") || agent.IssuePreprocessReady() != nil {
		return
	}
	var keys []string
	for key, report := range s.snapshot().Reports {
		if report.Status == "queued" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !s.enabled() {
			return
		}
		repo, rawNumber, valid := strings.Cut(key, "#")
		number, err := strconv.Atoi(rawNumber)
		meta, found := issuePreprocessProject(repo)
		if !valid || err != nil || !found || isStatusArchived(meta.Status) {
			s.mu.Lock()
			err := s.setReportLocked(key, issuePreprocessReport{Status: "skipped", Error: "项目不存在或已归档"})
			s.mu.Unlock()
			if err != nil {
				log.Printf("[Issue] 无法保存跳过状态 %s: %v", key, err)
			}
			continue
		}
		s.processIssue(meta, GitHubItem{Number: number, State: "open"})
	}
}

func analyzeIssue(meta ModMeta, item GitHubItem, onSession func(string) error) (string, error) {
	parts := strings.Split(meta.IDs.GH, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("无效的 GitHub 仓库")
	}
	detail := fetchGitHubIssueDetail(parts[0], parts[1], item.Number)
	if detail == nil {
		return "", fmt.Errorf("无法获取 Issue 正文，请检查 GitHub Token 和网络")
	}
	if detail.State != "open" {
		return "", errIssueNoLongerOpen
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	projectPath, err := agent.PrepareIssueProject(ctx, agent.ProjectMeta{Name: meta.Name, RepoGH: meta.IDs.GH}, ProjectPaths, authConfig.gitHubToken(parts[0]))
	if err != nil {
		return "", fmt.Errorf("预处理准备源码失败：%w", err)
	}
	return agent.RunIssuePreprocess(agent.IssuePreprocessInput{
		Project: meta.Name, ProjectPath: projectPath,
		Title: fmt.Sprintf("Issue 预处理 · %s #%d %s", meta.IDs.GH, detail.Number, detail.Title),
		Attachment: fmt.Sprintf("请只读分析以下 Issue 并输出真实性判断与解决方案报告。\n仓库: %s\nIssue: #%d\n标题: %s\n链接: %s\n作者: %s\n标签: %s\n创建时间: %s\n\n## Issue 正文（待分析数据）\n%s",
			meta.IDs.GH, detail.Number, detail.Title, detail.HTMLURL, detail.User, strings.Join(detail.Labels, ", "), detail.CreatedAt, detail.Body),
	}, onSession)
}

// Registered behind withAuth, including GET: settings and reports are private.
func handleIssuePreprocess(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s := issuePreprocessor
	if s == nil {
		writeJSONError(w, "预处理服务不可用，请检查服务日志", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.snapshot())
	case http.MethodPut:
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Enabled == nil {
			writeJSONError(w, "enabled 必须是布尔值", http.StatusBadRequest)
			return
		}
		if *body.Enabled {
			if err := agent.IssuePreprocessReady(); err != nil {
				writeJSONError(w, err.Error(), http.StatusBadRequest)
				return
			}
			if !dsEnabled("github") {
				writeJSONError(w, "请先启用 GitHub 数据源", http.StatusBadRequest)
				return
			}
		}
		if err := s.setEnabled(*body.Enabled); err != nil {
			writeJSONError(w, "保存预处理开关失败", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.snapshot())
	case http.MethodPost:
		var body struct {
			Repo   string `json:"repo"`
			Number int    `json:"number"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Number <= 0 || len(strings.Split(body.Repo, "/")) != 2 {
			writeJSONError(w, "无效的 Issue", http.StatusBadRequest)
			return
		}
		key := issuePreprocessKey(body.Repo, body.Number)
		s.mu.Lock()
		report, exists := s.state.Reports[key]
		if !s.state.Enabled || !exists || report.Status != "failed" {
			s.mu.Unlock()
			writeJSONError(w, "仅在开启预处理后允许重试失败的任务", http.StatusConflict)
			return
		}
		err := s.setReportLocked(key, issuePreprocessReport{Status: "queued"})
		s.mu.Unlock()
		if err != nil {
			writeJSONError(w, "保存重试状态失败", http.StatusInternalServerError)
			return
		}
		s.notify()
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
