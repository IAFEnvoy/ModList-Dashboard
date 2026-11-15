package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type delivery struct {
	ID          string    `json:"id"`
	Target      Target    `json:"target"`
	Parts       []string  `json:"parts"`
	NextPart    int       `json:"next_part"`
	Attempts    int       `json:"attempts"`
	NextAttempt time.Time `json:"next_attempt"`
}

type state struct {
	Baselines map[string]int  `json:"baselines"`
	Reports   map[string]bool `json:"reports"`
	Pending   []delivery      `json:"pending"`
	Bindings  []Binding       `json:"bindings,omitempty"`
	Codes     []bindingCode   `json:"binding_codes,omitempty"`
}

type Service struct {
	mu            sync.Mutex
	cfg           Config
	path          string
	senders       map[string]Sender
	state         state
	observedRepos map[string]bool // Fresh upstream snapshots in this process, not cached seeds.
	wake          chan struct{}
}

// New does not access the network. A disabled service does not create state.
func New(cfg Config, path string, senders map[string]Sender) (*Service, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if len(senders) == 0 {
		return nil, fmt.Errorf("bot: 请至少启用一个通知平台")
	}
	if cfg.DashboardURL != "" {
		u, err := url.Parse(cfg.DashboardURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("bot: dashboard_url 必须是无凭据、查询参数和片段的 HTTP(S) 地址")
		}
	}
	s := &Service{cfg: cfg, path: path, senders: make(map[string]Sender), observedRepos: make(map[string]bool), wake: make(chan struct{}, 1)}
	for name, sender := range senders {
		s.senders[name] = sender
	}
	seen := make(map[Target]bool)
	s.cfg.Targets = nil
	for _, target := range cfg.Targets {
		sender := s.senders[target.Provider]
		if sender == nil {
			return nil, fmt.Errorf("bot: 未启用的通知平台 %q", target.Provider)
		}
		if err := sender.Validate(target); err != nil {
			return nil, err
		}
		if !seen[target] {
			seen[target] = true
			s.cfg.Targets = append(s.cfg.Targets, target)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(data, &s.state); err != nil {
			return nil, fmt.Errorf("bot: 无法读取通知队列: %w", err)
		}
	}
	if s.state.Baselines == nil {
		s.state.Baselines = make(map[string]int)
	}
	if s.state.Reports == nil {
		s.state.Reports = make(map[string]bool)
	}
	for _, binding := range s.state.Bindings {
		if sender := s.senders[binding.Target.Provider]; sender != nil && sender.Validate(binding.Target) == nil {
			seen[binding.Target] = true
		}
	}
	// Queued messages must never be redirected to newly configured recipients.
	// Removed targets are discarded; other targets retain their send progress.
	kept := s.state.Pending[:0]
	for _, item := range s.state.Pending {
		eventEnabled := (cfg.NotifyNewIssues && strings.HasPrefix(item.ID, "issue:")) || (cfg.NotifyReports && strings.HasPrefix(item.ID, "report:"))
		if eventEnabled && seen[item.Target] && item.NextPart >= 0 && item.NextPart < len(item.Parts) {
			kept = append(kept, item)
		}
	}
	s.state.Pending = kept
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) WantsIssues() bool { return s != nil && s.cfg.NotifyNewIssues }

// SeedIssues initializes a missing baseline from a cached snapshot, even stale.
func (s *Service) SeedIssues(repo string, issues []Issue) error {
	return s.observeIssues(repo, "", issues, true)
}

func (s *Service) ObserveIssues(repo, project string, issues []Issue) error {
	return s.observeIssues(repo, project, issues, false)
}

func (s *Service) observeIssues(repo, project string, issues []Issue, seed bool) error {
	if !s.WantsIssues() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repo = strings.ToLower(repo)
	previous, initialized := s.state.Baselines[repo]
	observed := s.observedRepos[repo]
	if seed && initialized {
		return nil
	}
	latest, pendingCount := previous, len(s.state.Pending)
	seen := make(map[int]bool)
	for _, issue := range issues {
		if issue.Number > latest {
			latest = issue.Number
		}
		if !seed && observed && issue.Number > previous && issue.State == "open" && !seen[issue.Number] {
			seen[issue.Number] = true
			link := issue.URL
			if link == "" {
				link = fmt.Sprintf("https://github.com/%s/issues/%d", repo, issue.Number)
			}
			s.enqueueLocked(fmt.Sprintf("issue:%s#%d", repo, issue.Number), fmt.Sprintf("新增 Issue · %s\n%s #%d\n%s\n%s", project, repo, issue.Number, issue.Title, link))
		}
	}
	if initialized && latest == previous {
		if !seed {
			s.observedRepos[repo] = true
		}
		return nil
	}
	s.state.Baselines[repo] = latest
	if err := s.saveLocked(); err != nil {
		s.state.Pending = s.state.Pending[:pendingCount]
		if initialized {
			s.state.Baselines[repo] = previous
		} else {
			delete(s.state.Baselines, repo)
		}
		return err
	}
	if !seed {
		s.observedRepos[repo] = true
	}
	if len(s.state.Pending) > pendingCount {
		s.notify()
	}
	return nil
}

func (s *Service) PublishReport(report Report) error {
	if s == nil || !s.cfg.NotifyReports {
		return nil
	}
	if report.SessionID == "" || strings.TrimSpace(report.Content) == "" {
		return fmt.Errorf("bot: 报告缺少会话编号或正文")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Reports[report.SessionID] {
		return nil
	}
	pendingCount := len(s.state.Pending)
	text := fmt.Sprintf("Issue 预处理报告 · %s\n%s #%d\n会话: %s\n\n%s", report.Project, report.Repo, report.Number, report.SessionID, report.Content)
	if s.cfg.DashboardURL != "" {
		text += "\n\n报告会话历史（需登录）: " + strings.TrimRight(s.cfg.DashboardURL, "/") + "/#agents-chat"
	}
	s.enqueueLocked("report:"+report.SessionID, text)
	s.state.Reports[report.SessionID] = true
	if err := s.saveLocked(); err != nil {
		delete(s.state.Reports, report.SessionID)
		s.state.Pending = s.state.Pending[:pendingCount]
		return err
	}
	s.notify()
	return nil
}

func (s *Service) enqueueLocked(id, text string) {
	parts := splitMessage(text)
	for _, target := range s.targetsLocked() {
		s.state.Pending = append(s.state.Pending, delivery{ID: id, Target: target, Parts: parts})
	}
}

// Keep UTF-8 intact and leave room for part indicators in text messages.
func splitMessage(text string) []string {
	const maxBytes = 1800
	var chunks []string
	for len(text) > maxBytes {
		end := maxBytes
		for !utf8.RuneStart(text[end]) {
			end--
		}
		chunks = append(chunks, text[:end])
		text = text[end:]
	}
	chunks = append(chunks, text)
	if len(chunks) > 1 {
		for i := range chunks {
			chunks[i] = fmt.Sprintf("[%d/%d] %s", i+1, len(chunks), chunks[i])
		}
	}
	return chunks
}

func (s *Service) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run owns sending. Producers only persist events and wake this worker.
func (s *Service) Run(ctx context.Context) {
	for ctx.Err() == nil {
		timer := time.NewTimer(s.sendNext(ctx, time.Now()))
		select {
		case <-ctx.Done():
		case <-s.wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// One in-flight request per service; a failed part blocks later parts for the
// same target but does not prevent delivery to other targets.
func (s *Service) sendNext(ctx context.Context, now time.Time) time.Duration {
	s.mu.Lock()
	next, index := time.Minute, -1
	blocked := make(map[Target]bool)
	for i, item := range s.state.Pending {
		if blocked[item.Target] {
			continue
		}
		blocked[item.Target] = true
		if delay := item.NextAttempt.Sub(now); delay > 0 {
			if delay < next {
				next = delay
			}
			continue
		}
		index = i
		break
	}
	if index < 0 {
		s.mu.Unlock()
		return next
	}
	item := s.state.Pending[index]
	s.mu.Unlock()
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err := s.senders[item.Target.Provider].Send(requestCtx, item.Target, item.Parts[item.NextPart])
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	// Unbinding can remove queued deliveries while Send runs outside the lock.
	// Locate the original delivery again rather than using its former index.
	index = -1
	for i, pending := range s.state.Pending {
		if pending.ID == item.ID && pending.Target == item.Target && pending.NextPart == item.NextPart {
			index = i
			break
		}
	}
	if index < 0 {
		return time.Second
	}
	previous := append([]delivery(nil), s.state.Pending...)
	if err != nil {
		item.Attempts++
		backoff := time.Duration(1<<min(item.Attempts-1, 5)) * 10 * time.Second
		item.NextAttempt = time.Now().Add(backoff)
		s.state.Pending[index] = item
		log.Printf("[Bot] %s 通知发送失败 (%s, 第%d次)，%s后重试: %v", item.Target.Provider, item.ID, item.Attempts, backoff, err)
	} else {
		item.NextPart++
		item.Attempts, item.NextAttempt = 0, time.Time{}
		if item.NextPart == len(item.Parts) {
			s.state.Pending = append(s.state.Pending[:index], s.state.Pending[index+1:]...)
		} else {
			s.state.Pending[index] = item
		}
	}
	if err := s.saveLocked(); err != nil {
		s.state.Pending = previous
		log.Printf("[Bot] 无法保存通知进度: %v", err)
		return 10 * time.Second
	}
	return time.Second
}

func (s *Service) saveLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".bot-notifications-*")
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
