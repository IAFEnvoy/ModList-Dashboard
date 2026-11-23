package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"modlist-dashboard/agent"
	"modlist-dashboard/badge"
	"modlist-dashboard/bot"
)

func TestBotSettingsAreOptionalAndResolveCredentials(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bots.yml")
	service, err := newBotService(path, filepath.Join(root, "state.json"))
	if err != nil || service != nil {
		t.Fatalf("missing bot config enabled module: %v %v", service, err)
	}
	t.Setenv("TEST_BOT_ID", "test-app")
	t.Setenv("TEST_BOT_SECRET", "test-secret")
	if err := os.WriteFile(path, []byte("enabled: false\nproviders:\n  qq:\n    app_id: '${TEST_BOT_ID}'\n    app_secret: '${TEST_BOT_SECRET}'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := loadBotSettings(path)
	if err != nil || settings.Providers.QQ.AppID != "test-app" || settings.Providers.QQ.AppSecret != "test-secret" || !settings.NotifyNewIssues || !settings.NotifyReports {
		t.Fatal("bot configuration did not resolve environment/default settings")
	}
	if service, err := newBotService(path, filepath.Join(root, "state.json")); err != nil || service != nil {
		t.Fatal("disabled configuration initialized a bot")
	}
	if _, err := loadBotSettings(filepath.Join("config", "bots.yml.example")); err != nil {
		t.Fatalf("invalid committed bot template: %v", err)
	}
}

func TestEnabledQQBotStartsWithNoTargetsAndCreatesWebSocketReceiver(t *testing.T) {
	previous := qqBotReceiver
	t.Cleanup(func() { qqBotReceiver = previous })
	root := t.TempDir()
	path := filepath.Join(root, "bots.yml")
	if err := os.WriteFile(path, []byte("enabled: true\nproviders:\n  qq:\n    enabled: true\n    app_id: test-app\n    app_secret: test-secret\ntargets: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	service, err := newBotService(path, filepath.Join(root, "state.json"))
	if err != nil || service == nil || !service.ProviderEnabled("qq") || qqBotReceiver == nil {
		t.Fatalf("bot could not start before first binding: %v", err)
	}
	if service, err := newBotService(filepath.Join(root, "missing.yml"), filepath.Join(root, "disabled.json")); err != nil || service != nil || qqBotReceiver != nil {
		t.Fatal("disabled bot retained WebSocket receiver")
	}
}

type integrationBotSender struct{ sent chan string }

func (*integrationBotSender) Validate(bot.Target) error { return nil }
func (s *integrationBotSender) Send(_ context.Context, _ bot.Target, content string) error {
	s.sent <- content
	return nil
}

func TestBotHooksNotifyNewIssuesAndCompletedReportsIndependentlyOfPreprocessSwitch(t *testing.T) {
	meta := ModMeta{ID: "bot-hooks", Name: "Test Project"}
	meta.IDs.GH = "owner/repo"
	withTestMods(t, meta)
	oldBots, oldPreprocessor, oldSessions := botService, issuePreprocessor, agent.AgentSessions
	t.Cleanup(func() { botService, issuePreprocessor, agent.AgentSessions = oldBots, oldPreprocessor, oldSessions })
	sender := &integrationBotSender{sent: make(chan string, 4)}
	var err error
	botService, err = bot.New(bot.Config{Enabled: true, NotifyNewIssues: true, NotifyReports: true,
		Targets: []bot.Target{{Provider: "test", ID: "test-target"}}}, filepath.Join(t.TempDir(), "state.json"), map[string]bot.Sender{"test": sender},
		bot.WithReportSummarizer(func(_ context.Context, report string) (string, error) {
			if report != "confirmed report with solution" {
				t.Errorf("summary received non-report content: %q", report)
			}
			return "简短结论与建议", nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	issuePreprocessor = testIssuePreprocessor(t) // Automatic analysis is off.
	// Notification refresh must survive even if preprocessing initialization fails.
	prevConfig, prevAuth := config, authConfig
	cacheMu.Lock()
	prevCache := cache
	cache = make(map[string]cacheEntry)
	cacheMu.Unlock()
	t.Cleanup(func() {
		config, authConfig = prevConfig, prevAuth
		cacheMu.Lock()
		cache = prevCache
		cacheMu.Unlock()
	})
	config.Server.CacheTTLMin = 90
	config.DataSources = map[string]DataSourceConfig{"github": {Enabled: true}}
	authConfig.GitHub.Tokens = map[string]string{"owner": "test-token"}
	writeCacheDirect(badge.CacheKeyPrefix+meta.IDs.GH, &badge.GitHubInfo{Issues: []GitHubItem{{Number: 10, State: "open"}}}, 12*time.Minute)
	savedPreprocessor := issuePreprocessor
	issuePreprocessor = nil
	if delay := refreshDueGitHubRepositories(); delay > 12*time.Minute || delay < 11*time.Minute {
		t.Fatalf("bot-only refresh ignored cache expiry: %s", delay)
	}
	issuePreprocessor = savedPreprocessor
	observeGitHubIssues(meta.IDs.GH, &badge.GitHubInfo{Issues: []GitHubItem{{Number: 10, State: "open"}}})
	observeGitHubIssues(meta.IDs.GH, &badge.GitHubInfo{Issues: []GitHubItem{{Number: 10, State: "open"}, {Number: 11, State: "open", Title: "new issue"}}})
	if len(issuePreprocessor.snapshot().Reports) != 0 {
		t.Fatal("notifications turned on automatic analysis")
	}
	// Completing analysis must produce a second notification, with no tool data.
	if err := issuePreprocessor.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := issuePreprocessor.observeIssues(meta, []GitHubItem{{Number: 12, State: "open"}}); err != nil {
		t.Fatal(err)
	}
	issuePreprocessor.analyze = func(_ ModMeta, _ GitHubItem, _ func(string) error) (string, error) {
		agent.AgentSessions = []*agent.ChatSession{{ID: "bot-report", Agent: agent.IssuePreprocessorID,
			Messages: []agent.ChatMessage{{Type: "tool_result", Content: "PRIVATE TOOL OUTPUT"}, {Type: "message", Content: "confirmed report with solution"}}}}
		return "bot-report", nil
	}
	issuePreprocessor.processIssue(meta, GitHubItem{Number: 12, State: "open"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); botService.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	for _, expected := range []string{"new issue", "简短结论与建议"} {
		select {
		case content := <-sender.sent:
			if !strings.Contains(content, expected) || strings.Contains(content, "PRIVATE TOOL OUTPUT") {
				t.Fatalf("unexpected notification: %s", content)
			}
		case <-time.After(4 * time.Second):
			t.Fatalf("missing notification: %s", expected)
		}
	}
}
