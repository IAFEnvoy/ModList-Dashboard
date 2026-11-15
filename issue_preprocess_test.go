package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modlist-dashboard/agent"
	"modlist-dashboard/badge"
)

func testIssuePreprocessor(t *testing.T) *issuePreprocessService {
	t.Helper()
	s, err := newIssuePreprocessService(filepath.Join(t.TempDir(), "issue-preprocess.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestIssuePreprocessPersistsSwitchAndRecoversInterruptedTask(t *testing.T) {
	s := testIssuePreprocessor(t)
	if s.enabled() {
		t.Fatal("preprocessing must default to off")
	}
	if err := s.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := s.setReportLocked("owner/repo#1", issuePreprocessReport{Status: "running", SessionID: "session"}); err != nil {
		t.Fatal(err)
	}
	restored, err := newIssuePreprocessService(s.path)
	if err != nil {
		t.Fatal(err)
	}
	report := restored.snapshot().Reports["owner/repo#1"]
	if !restored.enabled() || report.Status != "failed" || report.SessionID != "session" || report.Error == "" {
		t.Fatalf("unexpected recovered state: %+v", restored.snapshot())
	}
	if err := restored.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	restored, err = newIssuePreprocessService(s.path)
	if err != nil || restored.enabled() {
		t.Fatalf("disabled switch was not persisted: %v", err)
	}
}

func TestIssuePreprocessDeduplicatesAndStopsNewTasksWhenDisabled(t *testing.T) {
	s := testIssuePreprocessor(t)
	meta := ModMeta{Name: "Project"}
	meta.IDs.GH = "Owner/Repo"
	item := GitHubItem{Number: 1, State: "open"}
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	calls := 0
	s.analyze = func(_ ModMeta, _ GitHubItem, onSession func(string) error) (string, error) {
		calls++
		if err := onSession("report-session"); err != nil {
			return "", err
		}
		close(started)
		<-release
		return "report-session", nil
	}
	s.processIssue(meta, item)
	if calls != 0 {
		t.Fatal("disabled worker started a task")
	}
	if err := s.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := s.observeIssues(meta, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.observeIssues(meta, []GitHubItem{item}); err != nil {
		t.Fatal(err)
	}
	go func(m ModMeta) { s.processIssue(m, item); close(done) }(meta)
	<-started
	meta.IDs.GH = "owner/repo"
	s.processIssue(meta, item)
	if err := s.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	s.processIssue(meta, GitHubItem{Number: 2, State: "open"})
	close(release)
	<-done
	if calls != 1 {
		t.Fatalf("analysis calls = %d, want 1", calls)
	}
	report := s.snapshot().Reports["owner/repo#1"]
	if report.Status != "completed" || report.SessionID != "report-session" {
		t.Fatalf("in-flight task failed to finish after disabling: %+v", report)
	}
	if err := s.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	s.processIssue(meta, item)
	if calls != 1 {
		t.Fatal("reenabling repeated a completed analysis")
	}
}

func TestIssuePreprocessSkipsClosedAndArchivedAndRetainsFailures(t *testing.T) {
	s := testIssuePreprocessor(t)
	if err := s.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	meta := ModMeta{}
	meta.IDs.GH = "owner/repo"
	if err := s.observeIssues(meta, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.observeIssues(meta, []GitHubItem{{Number: 1, State: "closed"}, {Number: 2, State: "open", Archived: true}, {Number: 3, State: "open"}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.analyze = func(_ ModMeta, _ GitHubItem, _ func(string) error) (string, error) {
		calls++
		return "partial-session", errors.New("model unavailable")
	}
	s.processIssue(meta, GitHubItem{Number: 1, State: "closed"})
	s.processIssue(meta, GitHubItem{Number: 2, State: "open", Archived: true})
	s.processIssue(meta, GitHubItem{Number: 3, State: "open"})
	s.processIssue(meta, GitHubItem{Number: 3, State: "open"})
	if calls != 1 || len(s.snapshot().Reports) != 1 {
		t.Fatalf("closed/archived/failed tasks repeated: calls=%d state=%+v", calls, s.snapshot())
	}
	report := s.snapshot().Reports["owner/repo#3"]
	if report.Status != "failed" || report.Error != "model unavailable" || report.SessionID != "partial-session" {
		t.Fatalf("failure was not preserved: %+v", report)
	}
}

func TestIssuePreprocessDoesNotEnableWhenSavingFails(t *testing.T) {
	s := testIssuePreprocessor(t)
	if err := os.Mkdir(s.path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := s.setEnabled(true); err == nil || s.enabled() {
		t.Fatal("failed persistence enabled the worker")
	}
}

func TestIssuePreprocessOnlyQueuesAddedIssuesAfterBaseline(t *testing.T) {
	s := testIssuePreprocessor(t)
	if err := s.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	meta := ModMeta{Name: "Example"}
	meta.IDs.GH = "Owner/Repo"
	initial := []GitHubItem{{Number: 1, State: "open"}, {Number: 5, State: "closed"}, {Number: 10, State: "open"}}
	if err := s.observeIssues(meta, initial); err != nil {
		t.Fatal(err)
	}
	if len(s.snapshot().Reports) != 0 {
		t.Fatal("first sync dispatched existing issues")
	}
	// Reopening #5 and editing #10 must not look like additions. Only the
	// added open issue #11 qualifies; #12 was already closed when discovered.
	refreshed := []GitHubItem{{Number: 11, State: "open"}, {Number: 12, State: "closed"}, {Number: 5, State: "open"}, {Number: 10, State: "open", Title: "edited"}}
	if err := s.observeIssues(meta, refreshed); err != nil {
		t.Fatal(err)
	}
	if got := s.snapshot().Reports; len(got) != 1 || got["owner/repo#11"].Status != "queued" {
		t.Fatalf("unexpected delta: %+v", got)
	}
	restored, err := newIssuePreprocessService(s.path)
	if err != nil {
		t.Fatal(err)
	}
	meta.IDs.GH = "owner/repo"
	if err := restored.observeIssues(meta, refreshed); err != nil {
		t.Fatal(err)
	}
	if len(restored.snapshot().Reports) != 1 || restored.state.LastIssueNumbers["owner/repo"] != 12 {
		t.Fatal("restart lost the persisted baseline or queue")
	}
	// Smaller snapshots (pagination changes or deleted issues) cannot move
	// the baseline backwards and later reclassify old issues as new.
	if err := restored.observeIssues(meta, initial); err != nil {
		t.Fatal(err)
	}
	if restored.state.LastIssueNumbers["owner/repo"] != 12 {
		t.Fatal("baseline moved backwards")
	}
}

func TestIssuePreprocessEmptyBaselineAndDisabledObservations(t *testing.T) {
	s := testIssuePreprocessor(t)
	meta := ModMeta{}
	meta.IDs.GH = "owner/repo"
	if err := s.observeIssues(meta, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.observeIssues(meta, []GitHubItem{{Number: 1, State: "open"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := s.observeIssues(meta, []GitHubItem{{Number: 1, State: "open"}, {Number: 2, State: "open"}}); err != nil {
		t.Fatal(err)
	}
	if got := s.snapshot().Reports; len(got) != 1 || got["owner/repo#2"].Status != "queued" {
		t.Fatalf("disabled observations were dispatched later: %+v", got)
	}
	other := ModMeta{}
	other.IDs.GH = "owner/empty"
	if err := s.observeIssues(other, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.observeIssues(other, []GitHubItem{{Number: 1, State: "open"}}); err != nil {
		t.Fatal(err)
	}
	if s.snapshot().Reports["owner/empty#1"].Status != "queued" {
		t.Fatal("first new issue in an empty repository was missed")
	}
}

func TestIssuePreprocessFirstSnapshotAfterRestartOnlyCalibratesBaseline(t *testing.T) {
	s := testIssuePreprocessor(t)
	if err := s.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	meta := ModMeta{}
	meta.IDs.GH = "owner/repo"
	if err := s.observeIssues(meta, []GitHubItem{{Number: 10, State: "open"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.setReportLocked("owner/repo#10", issuePreprocessReport{Status: "completed", SessionID: "old-report"}); err != nil {
		t.Fatal(err)
	}
	if err := s.observeIssues(meta, []GitHubItem{{Number: 11, State: "open"}}); err != nil {
		t.Fatal(err)
	}
	restarted, err := newIssuePreprocessService(s.path)
	if err != nil {
		t.Fatal(err)
	}
	first := []GitHubItem{{Number: 1, State: "open"}, {Number: 10, State: "open"}, {Number: 11, State: "open"}, {Number: 12, State: "open"}, {Number: 13, State: "open"}}
	if err := restarted.observeIssues(meta, first); err != nil {
		t.Fatal(err)
	}
	state := restarted.snapshot()
	if len(state.Reports) != 2 || state.Reports["owner/repo#10"].SessionID != "old-report" || state.Reports["owner/repo#11"].Status != "queued" || restarted.state.LastIssueNumbers[meta.IDs.GH] != 13 {
		t.Fatalf("startup snapshot queued old data or lost existing tasks: %+v", state)
	}
	select {
	case <-restarted.wake:
		t.Fatal("baseline-only snapshot triggered dispatch")
	default:
	}
	if err := restarted.observeIssues(meta, append(first, GitHubItem{Number: 14, State: "open"})); err != nil {
		t.Fatal(err)
	}
	if state := restarted.snapshot(); len(state.Reports) != 3 || state.Reports["owner/repo#14"].Status != "queued" {
		t.Fatalf("post-baseline event did not queue only the addition: %+v", state)
	}
}

func TestIssuePreprocessSchedulesAtExistingTTLExpiry(t *testing.T) {
	s := testIssuePreprocessor(t)
	prevService := issuePreprocessor
	issuePreprocessor = s
	t.Cleanup(func() { issuePreprocessor = prevService })
	prevConfig, prevAuth, prevMods := config, authConfig, modMetas
	cacheMu.Lock()
	prevCache := cache
	cache = make(map[string]cacheEntry)
	cacheMu.Unlock()
	t.Cleanup(func() {
		config, authConfig = prevConfig, prevAuth
		modMetaMu.Lock()
		modMetas = prevMods
		modMetaMu.Unlock()
		cacheMu.Lock()
		cache = prevCache
		cacheMu.Unlock()
	})
	config.Server.CacheTTLMin = 90
	config.DataSources = map[string]DataSourceConfig{"github": {Enabled: true}}
	authConfig.GitHub.Tokens = map[string]string{"owner": "test-token"}
	meta := ModMeta{ID: "example"}
	meta.IDs.GH = "owner/repo"
	modMetaMu.Lock()
	modMetas = map[string]ModMeta{meta.ID: meta}
	modMetaMu.Unlock()
	writeCacheDirect(badge.CacheKeyPrefix+meta.IDs.GH, &badge.GitHubInfo{Issues: []GitHubItem{{Number: 10, State: "open"}}}, 12*time.Minute)
	if err := s.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	delay := refreshDueGitHubRepositories()
	if delay > 12*time.Minute || delay < 12*time.Minute-time.Second {
		t.Fatalf("next refresh = %s, want the existing cache's remaining TTL", delay)
	}
	if s.observedRepos[meta.IDs.GH] || len(s.snapshot().Reports) != 0 {
		t.Fatal("reading cached data consumed the first upstream snapshot guard")
	}
	if err := s.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	if delay := refreshDueGitHubRepositories(); delay != 90*time.Minute {
		t.Fatalf("configured TTL = %s, want 90m", delay)
	}
}

type issueRefreshTestTransport func(*http.Request) (*http.Response, error)

func (f issueRefreshTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestGitHubTTLRefreshQueuesOnlyDeltaAndKeepsBaselineOnFailure(t *testing.T) {
	s := testIssuePreprocessor(t)
	meta := ModMeta{ID: "delta-test"}
	meta.IDs.GH = "delta-owner/repo"
	withTestMods(t, meta)
	prevService, prevConfig, prevAuth, prevTransport := issuePreprocessor, config, authConfig, http.DefaultTransport
	issuePreprocessor = s
	config.Server.CacheDir = filepath.Join(t.TempDir(), "cache")
	config.Server.CacheTTLMin = 90
	config.Retry.MaxAttempts = 1
	config.DataSources = map[string]DataSourceConfig{"github": {Enabled: true}}
	authConfig.GitHub.Tokens = map[string]string{"delta-owner": "test-token"}
	cacheMu.Lock()
	prevCache := cache
	cache = make(map[string]cacheEntry)
	cacheMu.Unlock()
	t.Cleanup(func() {
		issuePreprocessor, config, authConfig, http.DefaultTransport = prevService, prevConfig, prevAuth, prevTransport
		cacheMu.Lock()
		cache = prevCache
		cacheMu.Unlock()
	})
	var requests atomic.Int32
	var failure atomic.Bool
	var issueNumber atomic.Int32
	issueNumber.Store(11)
	http.DefaultTransport = issueRefreshTestTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		status, body := http.StatusOK, `[]`
		if strings.HasSuffix(r.URL.Path, "/issues") {
			body = fmt.Sprintf(`[{"number":%d,"title":"new","state":"open"},{"number":10,"title":"edited old","state":"open"},{"number":9,"state":"closed"}]`, issueNumber.Load())
			if failure.Load() {
				status, body = http.StatusInternalServerError, `{"message":"unavailable"}`
			}
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	if err := s.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	<-gitHubRefreshWake
	key := badge.CacheKeyPrefix + meta.IDs.GH
	old := &badge.GitHubInfo{Issues: []GitHubItem{{Number: 10, State: "open"}}}
	writeCacheDirect(key, old, -time.Second)
	failure.Store(true)
	if refreshed := refreshGitHubInfo(meta.IDs.GH, ttlCache()); refreshed != nil || s.observedRepos[meta.IDs.GH] {
		t.Fatal("failed startup fetch consumed the first successful snapshot guard")
	}
	failure.Store(false)
	if got := getGitHubInfoCached(meta.IDs.GH, ttlCache()); got != old {
		t.Fatal("TTL refresh stopped serving the old cached snapshot")
	}
	select {
	case <-gitHubRefreshWake:
	case <-time.After(3 * time.Second):
		t.Fatal("successful TTL refresh did not notify the preprocessing service")
	}
	if got := s.snapshot().Reports; len(got) != 0 || s.state.LastIssueNumbers[meta.IDs.GH] != 11 {
		t.Fatalf("first successful refresh after restart dispatched issues: %+v", got)
	}
	if got := getGitHubInfoCached(meta.IDs.GH, ttlCache()); got == old || requests.Load() != 3 {
		t.Fatalf("fresh cache unexpectedly refetched GitHub: requests=%d", requests.Load())
	}
	// Wait for this test's asynchronous disk-cache write before restoring the
	// global cache directory; no live configuration or source data is touched.
	waitForGitHubSnapshot(t, key, 11)
	issueNumber.Store(12)
	if refreshGitHubInfo(meta.IDs.GH, ttlCache()) == nil {
		t.Fatal("second refresh failed")
	}
	waitForGitHubSnapshot(t, key, 12)
	if got := s.snapshot().Reports; len(got) != 1 || got["delta-owner/repo#12"].Status != "queued" {
		t.Fatalf("later TTL refresh did not queue only its addition: %+v", got)
	}
	failure.Store(true)
	if refreshed := refreshGitHubInfo(meta.IDs.GH, ttlCache()); refreshed != nil {
		t.Fatal("failed issue fetch was published as a successful snapshot")
	}
	if s.state.LastIssueNumbers[meta.IDs.GH] != 12 || len(s.snapshot().Reports) != 1 {
		t.Fatal("failed refresh changed the issue baseline or dispatched historical issues")
	}
}

func waitForGitHubSnapshot(t *testing.T, key string, number int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := os.ReadFile(cacheFilePath(key))
		var disk struct {
			Info *badge.GitHubInfo `json:"gh_val"`
		}
		if err == nil && json.Unmarshal(data, &disk) == nil && disk.Info != nil {
			for _, issue := range disk.Info.Issues {
				if issue.Number == number {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("GitHub cache snapshot #%d was not persisted", number)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManualGitHubCacheClearTriggersOnlyDeltaAndKeepsReports(t *testing.T) {
	meta := ModMeta{ID: "manual-refresh", Name: "Manual Refresh"}
	meta.IDs.GH = "manual-owner/repo"
	withTestMods(t, meta)
	previousService, previousConfig, previousAuth, previousDB, previousTransport := issuePreprocessor, config, authConfig, userDB, http.DefaultTransport
	s := testIssuePreprocessor(t)
	issuePreprocessor = s
	config.Server.CacheDir = filepath.Join(t.TempDir(), "cache")
	config.Retry.MaxAttempts = 1
	config.DataSources = map[string]DataSourceConfig{"github": {Enabled: true}, "curseforge": {Enabled: false}}
	authConfig.GitHub.Tokens = map[string]string{"manual-owner": "test-token"}
	authConfig.Admin.Username, authConfig.Admin.Password = "manual-admin", "test-password"
	if err := initUserDBAt(filepath.Join(t.TempDir(), "users.db")); err != nil {
		t.Fatal(err)
	}
	cacheMu.Lock()
	previousCache := cache
	cache = make(map[string]cacheEntry)
	cacheMu.Unlock()
	t.Cleanup(func() {
		CloseUserDB()
		issuePreprocessor, config, authConfig, userDB, http.DefaultTransport = previousService, previousConfig, previousAuth, previousDB, previousTransport
		cacheMu.Lock()
		cache = previousCache
		cacheMu.Unlock()
	})
	var issueNumber atomic.Int32
	issueNumber.Store(10)
	http.DefaultTransport = issueRefreshTestTransport(func(r *http.Request) (*http.Response, error) {
		body := `[]`
		if strings.HasSuffix(r.URL.Path, "/issues") {
			body = fmt.Sprintf(`[{"number":%d,"state":"open"},{"number":1,"state":"open"}]`, issueNumber.Load())
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	if err := s.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	if refreshGitHubInfo(meta.IDs.GH, ttlCache()) == nil {
		t.Fatal("initial baseline fetch failed")
	}
	key := badge.CacheKeyPrefix + meta.IDs.GH
	waitForGitHubSnapshot(t, key, 10)
	if err := s.setReportLocked("manual-owner/repo#10", issuePreprocessReport{Status: "completed", SessionID: "existing-report"}); err != nil {
		t.Fatal(err)
	}
	request := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/admin/flush-cache", strings.NewReader(`{"source":"github"}`))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		withAuth(handleFlushCache)(w, r)
		return w
	}
	if w := request(""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous manual trigger accepted: %d", w.Code)
	}
	token, err := signToken("manual-admin")
	if err != nil {
		t.Fatal(err)
	}
	issueNumber.Store(11)
	if w := request(token); w.Code != http.StatusOK {
		t.Fatalf("manual refresh rejected: %d %s", w.Code, w.Body.String())
	}
	waitForGitHubSnapshot(t, key, 11)
	if got := s.snapshot().Reports; len(got) != 2 || got["manual-owner/repo#11"].Status != "queued" || got["manual-owner/repo#10"].SessionID != "existing-report" {
		t.Fatalf("manual refresh reset reports or queued historical issues: %+v", got)
	}
	if w := request(token); w.Code != http.StatusOK {
		t.Fatalf("repeated manual refresh rejected: %d", w.Code)
	}
	waitForGitHubSnapshot(t, key, 11)
	if got := s.snapshot().Reports; len(got) != 2 {
		t.Fatalf("repeated clear queued duplicate issues: %+v", got)
	}
}

func TestIssuePreprocessAPIRequiresAuthentication(t *testing.T) {
	s := testIssuePreprocessor(t)
	previous := issuePreprocessor
	issuePreprocessor = s
	t.Cleanup(func() { issuePreprocessor = previous })
	handler := corsMiddleware(withAuth(handleIssuePreprocess))
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPost} {
		for _, token := range []string{"", "Bearer invalid-token"} {
			r := httptest.NewRequest(method, "/api/admin/issue-preprocess", strings.NewReader(`{"enabled":true}`))
			r.Header.Set("Authorization", token)
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != http.StatusUnauthorized || s.enabled() {
				t.Fatalf("%s with token %q: status=%d enabled=%v", method, token, w.Code, s.enabled())
			}
		}
	}
	if _, err := os.Stat(s.path); !os.IsNotExist(err) {
		t.Fatal("unauthenticated request wrote settings")
	}
}

func TestIssuePreprocessAuthenticatedAPIValidatesAndRetries(t *testing.T) {
	s := testIssuePreprocessor(t)
	prevService, prevAuth, prevUserDB, prevModels := issuePreprocessor, authConfig, userDB, agent.ModelIndex
	issuePreprocessor = s
	authConfig.Admin.Username, authConfig.Admin.Password = "preprocess-admin", "test-password"
	if err := initUserDBAt(filepath.Join(t.TempDir(), "users.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		CloseUserDB()
		issuePreprocessor, authConfig, userDB, agent.ModelIndex = prevService, prevAuth, prevUserDB, prevModels
	})
	token, err := signToken("preprocess-admin")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/admin/issue-preprocess", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		withAuth(handleIssuePreprocess)(w, r)
		return w
	}
	if w := request(http.MethodGet, ""); w.Code != http.StatusOK {
		t.Fatalf("authenticated read failed: %d", w.Code)
	}
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":"true"}`} {
		if w := request(http.MethodPut, body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid body accepted: %s (%d)", body, w.Code)
		}
	}
	agent.ModelIndex = nil
	if w := request(http.MethodPut, `{"enabled":true}`); w.Code != http.StatusBadRequest || s.enabled() {
		t.Fatalf("enabled without a model: %d", w.Code)
	}
	if err := s.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := s.setReportLocked("owner/repo#1", issuePreprocessReport{Status: "failed", Error: "network"}); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodPost, `{"repo":"Owner/Repo","number":1}`); w.Code != http.StatusNoContent {
		t.Fatalf("retry failed: %d %s", w.Code, w.Body.String())
	}
	if s.snapshot().Reports["owner/repo#1"].Status != "queued" {
		t.Fatal("retry did not queue failed task")
	}
	if w := request(http.MethodPut, `{"enabled":false}`); w.Code != http.StatusOK || s.enabled() {
		t.Fatalf("authenticated disable failed: %d", w.Code)
	}
	if w := request(http.MethodDelete, ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unsupported method accepted: %d", w.Code)
	}
}
