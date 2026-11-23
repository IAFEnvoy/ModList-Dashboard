package bot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type recordedMessage struct {
	target Target
	text   string
}

type testSender struct {
	sent []recordedMessage
	fail string
}

func (*testSender) Validate(Target) error { return nil }
func (s *testSender) Send(_ context.Context, target Target, text string) error {
	if target.ID == s.fail {
		return errors.New("temporarily unavailable")
	}
	s.sent = append(s.sent, recordedMessage{target, text})
	return nil
}

func testConfig() Config {
	return Config{Enabled: true, NotifyNewIssues: true, NotifyReports: true,
		Targets: []Target{{Provider: "fake", Type: "room", ID: "one"}}}
}

func newTestService(t *testing.T, cfg Config, sender *testSender) *Service {
	t.Helper()
	s, err := New(cfg, filepath.Join(t.TempDir(), "notifications.json"), map[string]Sender{"fake": sender})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDisabledServiceAndUnknownProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disabled.json")
	s, err := New(Config{}, path, nil)
	if err != nil || s != nil {
		t.Fatalf("disabled service: %v %v", s, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("disabled module created state")
	}
	if s.WantsIssues() || s.PublishReport(Report{}) != nil {
		t.Fatal("disabled notification hooks must be no-ops")
	}
	if _, err := New(testConfig(), path, nil); err == nil {
		t.Fatal("unknown provider was accepted")
	}
}

func TestIssueBaselineDeltaAndRestartDoNotRepeatOldIssues(t *testing.T) {
	sender := &testSender{}
	s := newTestService(t, testConfig(), sender)
	initial := []Issue{{Number: 2, State: "closed"}, {Number: 10, State: "open"}}
	if err := s.SeedIssues("Owner/Repo", initial); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveIssues("Owner/Repo", "Project", initial); err != nil {
		t.Fatal(err)
	}
	updated := []Issue{{Number: 2, State: "open"}, {Number: 10, State: "open", Title: "edited"},
		{Number: 11, State: "open", Title: "new"}, {Number: 12, State: "closed"}, {Number: 11, State: "open", Title: "duplicate page entry"}}
	if err := s.ObserveIssues("owner/repo", "Project", updated); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 0 || len(s.state.Pending) != 1 || !strings.Contains(s.state.Pending[0].Parts[0], "#11") {
		t.Fatalf("producer sent synchronously or queued historical issues: %+v", s.state)
	}
	s.sendNext(context.Background(), time.Now())
	if len(sender.sent) != 1 || len(s.state.Pending) != 0 {
		t.Fatal("new issue was not sent")
	}
	restored, err := New(testConfig(), s.path, map[string]Sender{"fake": sender})
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.SeedIssues("owner/repo", initial); err != nil {
		t.Fatal(err)
	}
	startup := append(updated, Issue{Number: 13, State: "open"})
	if err := restored.ObserveIssues("OWNER/REPO", "Project", startup); err != nil {
		t.Fatal(err)
	}
	if len(restored.state.Pending) != 0 || restored.state.Baselines["owner/repo"] != 13 {
		t.Fatal("restart or stale cache repeated an issue notification")
	}
	if err := restored.ObserveIssues("owner/repo", "Project", append(startup, Issue{Number: 14, State: "open"})); err != nil {
		t.Fatal(err)
	}
	if len(restored.state.Pending) != 1 || !strings.Contains(restored.state.Pending[0].Parts[0], "#14") {
		t.Fatal("later refresh did not notify only the post-startup addition")
	}
}

func TestReportSummaryIsGeneratedOnceAndRetryDoesNotBlockOtherTargets(t *testing.T) {
	sender := &testSender{}
	cfg := testConfig()
	cfg.Targets = append(cfg.Targets, Target{Provider: "fake", Type: "room", ID: "two"})
	s := newTestService(t, cfg, sender)
	summaryCalls := 0
	s.summarizeReport = func(_ context.Context, content string) (string, error) {
		summaryCalls++
		if !strings.Contains(content, "完整报告") {
			t.Fatal("summary lost source report")
		}
		return "已确认问题存在，建议修复输入校验。", nil
	}
	report := Report{Repo: "owner/repo", Project: "项目", Number: 11, SessionID: "session", Content: strings.Repeat("完整报告🙂", 250)}
	if err := s.PublishReport(report); err != nil {
		t.Fatal(err)
	}
	if summaryCalls != 0 || s.state.Pending[0].Report == nil {
		t.Fatal("producer performed synchronous model work or lost draft")
	}
	// Preparing one summary updates every recipient before any message is sent.
	s.sendNext(context.Background(), time.Now())
	if summaryCalls != 1 || len(s.state.Pending[0].Parts) != 1 || len(s.state.Pending[1].Parts) != 1 {
		t.Fatal("report did not become one shared summary")
	}
	sender.fail = "one"
	s.sendNext(context.Background(), time.Now())
	s.sendNext(context.Background(), time.Now())
	if len(sender.sent) != 1 || sender.sent[0].target.ID != "two" {
		t.Fatal("one failed target blocked other recipients")
	}
	restored, err := New(cfg, s.path, map[string]Sender{"fake": sender}, WithReportSummarizer(func(context.Context, string) (string, error) {
		t.Error("restart regenerated stored summary")
		return "", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.state.Pending) != 1 || !restored.state.Pending[0].CompactReport || restored.state.Pending[0].Report != nil {
		t.Fatal("restart lost prepared summary or resent completed recipient")
	}
	if err := restored.PublishReport(report); err != nil {
		t.Fatal(err)
	}
	if len(restored.state.Pending) != 1 {
		t.Fatal("duplicate report was requeued")
	}
	sender.fail = ""
	restored.sendNext(context.Background(), time.Now().Add(time.Hour))
	if len(restored.state.Pending) != 0 || len(sender.sent) != 2 || sender.sent[1].target.ID != "one" || summaryCalls != 1 {
		t.Fatalf("report retry repeated or lost summaries: pending=%d sent=%d calls=%d", len(restored.state.Pending), len(sender.sent), summaryCalls)
	}
}

func TestRemovingTargetNeverRedirectsQueuedPrivateReport(t *testing.T) {
	sender := &testSender{}
	cfg := testConfig()
	s := newTestService(t, cfg, sender)
	if err := s.PublishReport(Report{SessionID: "private", Content: "private source analysis"}); err != nil {
		t.Fatal(err)
	}
	cfg.Targets[0].ID = "different-recipient"
	restored, err := New(cfg, s.path, map[string]Sender{"fake": sender})
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.state.Pending) != 0 {
		t.Fatal("old report retained or redirected to a changed target")
	}
}

func TestDisablingReportNotificationsAlsoStopsQueuedReports(t *testing.T) {
	sender := &testSender{}
	cfg := testConfig()
	s := newTestService(t, cfg, sender)
	if err := s.PublishReport(Report{SessionID: "queued-report", Content: "report"}); err != nil {
		t.Fatal(err)
	}
	cfg.NotifyReports = false
	restored, err := New(cfg, s.path, map[string]Sender{"fake": sender})
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.state.Pending) != 0 {
		t.Fatal("disabled report notifications retained a pending report")
	}
	if err := restored.PublishReport(Report{SessionID: "another-report", Content: "report"}); err != nil || len(restored.state.Pending) != 0 {
		t.Fatal("disabled report notifications enqueued a new report")
	}
}

func TestPersistenceFailureRollsBackBaselineAndQueue(t *testing.T) {
	s := newTestService(t, testConfig(), &testSender{})
	if err := s.SeedIssues("owner/repo", []Issue{{Number: 10}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveIssues("owner/repo", "Project", []Issue{{Number: 10}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveIssues("owner/repo", "Project", []Issue{{Number: 11, State: "open"}}); err == nil {
		t.Fatal("persistence failure was ignored")
	}
	if s.state.Baselines["owner/repo"] != 10 || len(s.state.Pending) != 0 {
		t.Fatal("failed save lost an issue notification")
	}
}

func TestSplitMessagePreservesUnicodeAndEntireReport(t *testing.T) {
	text := strings.Repeat("代码依据🙂\n", 1000)
	parts := splitMessage(text)
	var recovered strings.Builder
	for _, part := range parts {
		if !utf8.ValidString(part) || len(part) >= 2000 {
			t.Fatalf("invalid message chunk: %d bytes", len(part))
		}
		recovered.WriteString(part[strings.Index(part, "] ")+2:])
	}
	if recovered.String() != text {
		t.Fatal("message splitting truncated the report")
	}
}
