package bot

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestReportSummaryBodyLimitExcludesHeaderAndPublicLink(t *testing.T) {
	cfg := testConfig()
	cfg.DashboardURL = "https://dashboard.example.com/" + strings.Repeat("long-base/", 12)
	s := newTestService(t, cfg, &testSender{})
	text := s.reportMessage(Report{Project: "Test Project", Number: 123}, strings.Repeat("中🙂", 140), "public-share")
	lines := strings.Split(text, "\n")
	if len(lines) != 3 || utf8.RuneCountInString(lines[1]) != 200 || utf8.RuneCountInString(text) <= 200 {
		t.Fatalf("incorrect body-only limit: %q", text)
	}
	if !strings.Contains(lines[0], "Test Project #123") || !strings.Contains(lines[2], "/share/public-share") || strings.Contains(text, "需登录") || strings.Contains(text, "#agents-chat") {
		t.Fatalf("wrong public report link/header: %s", text)
	}
}

func TestReportSanitizesMarkdownPathsAndOverlongModelOutput(t *testing.T) {
	const formatted = "<think>PRIVATE REASONING</think>\n## **已确认问题**\n- 建议修复 `src/main/java/Example.java:42` 的边界检查。\n1. [更多说明](https://example.com)\n```java\nSECRET CODE\n```\n" + "`C:\\source\\Main.go:12`"
	text := compactReportText(formatted, 200)
	for _, unwanted := range []string{"#", "*", "`", "[", "]", "<think>", "PRIVATE", "SECRET", "http", ".java", ".go", "C:", "src/"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("retained formatting or details %q: %q", unwanted, text)
		}
	}
	if !strings.Contains(text, "已确认问题") || !strings.Contains(text, "边界检查") || !utf8.ValidString(text) {
		t.Fatalf("lost summary meaning: %q", text)
	}
}

func TestReportSummaryAndPublicShareArePreparedOnceAndSurviveRestart(t *testing.T) {
	cfg := testConfig()
	cfg.DashboardURL = "https://dashboard.example.com"
	cfg.Targets = append(cfg.Targets, Target{Provider: "fake", Type: "room", ID: "two"})
	sender := &testSender{}
	summaryCalls, shareCalls := 0, 0
	s, err := New(cfg, filepath.Join(t.TempDir(), "state.json"), map[string]Sender{"fake": sender}, WithReportSummarizer(func(context.Context, string) (string, error) {
		summaryCalls++
		return "**已确认问题存在**，建议增加输入检查。", nil
	}), WithReportShareCreator(func(sid, report string) (string, error) {
		shareCalls++
		if sid != "report-session" {
			t.Fatal("wrong share session")
		}
		return "public-share", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishReport(Report{Project: "项目", Number: 1, SessionID: "report-session", Content: strings.Repeat("完整报告", 3000)}); err != nil {
		t.Fatal(err)
	}
	s.sendNext(context.Background(), time.Now())
	if summaryCalls != 1 || shareCalls != 1 || len(sender.sent) != 0 {
		t.Fatal("summary/share was generated per recipient or report sent before persistence")
	}
	for _, pending := range s.state.Pending {
		if pending.Report != nil || !pending.CompactReport || len(pending.Parts) != 1 || !strings.Contains(pending.Parts[0], "/share/public-share") {
			t.Fatalf("invalid prepared report: %+v", pending)
		}
	}
	restored, err := New(cfg, s.path, s.senders, WithReportSummarizer(func(context.Context, string) (string, error) {
		t.Error("regenerated persisted summary")
		return "", nil
	}), WithReportShareCreator(func(string, string) (string, error) { t.Error("regenerated persisted share"); return "", nil }))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		restored.sendNext(context.Background(), time.Now())
	}
	if len(sender.sent) != 2 || sender.sent[0].text != sender.sent[1].text || len(restored.state.Pending) != 0 {
		t.Fatal("recipients did not receive exactly one matching notification")
	}
}

func TestLegacyLongReportQueueBecomesOneSummary(t *testing.T) {
	sender := &testSender{}
	s := newTestService(t, testConfig(), sender)
	full := fmt.Sprintf("Issue 预处理报告 · 项目\nowner/repo #12\n会话: legacy\n\n%s\n\n报告会话历史（需登录）: https://old.example/#agents-chat", strings.Repeat("长报告🙂", 2000))
	s.state.Pending = []delivery{{ID: "report:legacy", Target: s.cfg.Targets[0], Parts: splitMessage(full), NextPart: 2}}
	if err := s.saveLocked(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	restored, err := New(testConfig(), s.path, s.senders, WithReportSummarizer(func(_ context.Context, report string) (string, error) {
		calls++
		if report != strings.Repeat("长报告🙂", 2000) {
			t.Fatal("legacy report reconstruction lost content or retained old links")
		}
		return "报告已完成，建议修复输入检查。", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		restored.sendNext(context.Background(), time.Now())
	}
	if calls != 1 || len(sender.sent) != 1 || strings.Contains(sender.sent[0].text, "[3/") || !strings.Contains(sender.sent[0].text, "项目 #12") {
		t.Fatalf("legacy queue still floods messages: %+v", sender.sent)
	}
}

func TestFailedSummarySendsShortFallbackAndCancellationKeepsDraft(t *testing.T) {
	sender := &testSender{}
	s := newTestService(t, testConfig(), sender)
	s.summarizeReport = func(context.Context, string) (string, error) { return "", errors.New("model unavailable") }
	if err := s.PublishReport(Report{SessionID: "failed-summary", Content: strings.Repeat("原始报告正文", 500)}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		s.sendNext(context.Background(), time.Now())
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].text, "暂时无法生成摘要") || strings.Contains(sender.sent[0].text, "原始报告正文") {
		t.Fatal("summary failure sent full report or lost completion notification")
	}
	if err := s.PublishReport(Report{SessionID: "cancelled", Content: "report"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.summarizeReport = func(ctx context.Context, _ string) (string, error) { cancel(); return "", ctx.Err() }
	s.sendNext(ctx, time.Now())
	if len(s.state.Pending) != 1 || s.state.Pending[0].Report == nil || len(sender.sent) != 1 {
		t.Fatal("shutdown lost pending report or sent a cancellation fallback")
	}
}

func TestUnbindDuringSummaryDoesNotRestoreRemovedRecipients(t *testing.T) {
	cfg := testConfig()
	cfg.Targets = nil
	s := newTestService(t, cfg, &testSender{})
	first, second := Target{Provider: "fake", Type: "room", ID: "first"}, Target{Provider: "fake", Type: "room", ID: "second"}
	bindTestTarget(t, s, "alice", first)
	bindTestTarget(t, s, "bob", second)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.summarizeReport = func(context.Context, string) (string, error) { close(started); <-release; return "摘要", nil }
	if err := s.PublishReport(Report{SessionID: "shared-report", Content: "report"}); err != nil {
		t.Fatal(err)
	}
	go func() { defer close(done); s.sendNext(context.Background(), time.Now()) }()
	<-started
	if err := s.Unbind("alice", first); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	if len(s.state.Pending) != 1 || s.state.Pending[0].Target != second || len(s.state.Pending[0].Parts) != 1 {
		t.Fatal("summary restored unbound target")
	}
}
