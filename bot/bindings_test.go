package bot

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func bindTestTarget(t *testing.T, s *Service, owner string, target Target) string {
	t.Helper()
	code, _, err := s.NewBindingCode(owner, target.Provider, target.Type)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Bind(code, target); err != nil {
		t.Fatal(err)
	}
	return code
}

func TestBindingCodesArePrivateExpiringAndSingleUseAcrossRestart(t *testing.T) {
	cfg := testConfig()
	cfg.Targets = nil
	s := newTestService(t, cfg, &testSender{})
	target := Target{Provider: "fake", Type: "room", ID: "group"}
	old, _, err := s.NewBindingCode("alice", "fake", "room")
	if err != nil {
		t.Fatal(err)
	}
	code, expires, err := s.NewBindingCode("alice", "fake", "room")
	if err != nil || len(code) != 26 || time.Until(expires) < 9*time.Minute {
		t.Fatalf("bad code: %v", err)
	}
	if err := s.Bind(old, target); !errors.Is(err, ErrBindingCode) {
		t.Fatal("regenerated code remained usable")
	}
	if err := s.Bind(code, Target{Provider: "fake", Type: "different", ID: "group"}); !errors.Is(err, ErrBindingCode) {
		t.Fatal("code accepted wrong target type")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), code) || strings.Contains(string(data), old) {
		t.Fatal("plaintext token persisted")
	}
	if status := s.BindingStatus("bob"); status.PendingExpiresAt != nil || len(status.Bindings) != 0 {
		t.Fatal("other user saw private binding state")
	}
	restored, err := New(cfg, s.path, s.senders)
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if restored.Bind(strings.ToLower(code), target) == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("one-time code succeeded %d times", successes.Load())
	}
	if status := restored.BindingStatus("alice"); len(status.Bindings) != 1 || status.PendingExpiresAt != nil {
		t.Fatalf("bad binding state: %+v", status)
	}
	restarted, err := New(cfg, s.path, s.senders)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Bind(code, Target{Provider: "fake", Type: "room", ID: "other"}); !errors.Is(err, ErrBindingCode) {
		t.Fatal("restart revived consumed code")
	}
	if err := restarted.Unbind("bob", target); !errors.Is(err, ErrBindingNotFound) {
		t.Fatal("other owner unbound target")
	}
	if err := restarted.Unbind("alice", target); err != nil {
		t.Fatal(err)
	}
	if len(restarted.BindingStatus("alice").Bindings) != 0 {
		t.Fatal("unbind did not remove binding")
	}
	code, _, err = restarted.NewBindingCode("alice", "fake", "room")
	if err != nil {
		t.Fatal(err)
	}
	restarted.state.Codes[0].ExpiresAt = time.Now().Add(-time.Second)
	if err := restarted.Bind(code, target); !errors.Is(err, ErrBindingCode) {
		t.Fatal("expired code accepted")
	}
}

func TestBindingUpdatesAreAtomicOnStorageFailure(t *testing.T) {
	cfg := testConfig()
	cfg.Targets = nil
	s := newTestService(t, cfg, &testSender{})
	first := Target{Provider: "fake", Type: "room", ID: "first"}
	bindTestTarget(t, s, "alice", first)
	code, _, err := s.NewBindingCode("alice", "fake", "room")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishReport(Report{SessionID: "queued", Content: "future report"}); err != nil {
		t.Fatal(err)
	}
	path := s.path
	s.path = t.TempDir() // Cannot atomically replace a directory with a file.
	if err := s.Bind(code, Target{Provider: "fake", Type: "room", ID: "second"}); err == nil {
		t.Fatal("failed bind ignored save failure")
	}
	if len(s.state.Bindings) != 1 || len(s.state.Codes) != 1 {
		t.Fatal("failed bind consumed code or retained binding")
	}
	if _, _, err := s.NewBindingCode("alice", "fake", "room"); err == nil {
		t.Fatal("failed generation ignored save failure")
	}
	if s.state.Codes[0].Hash != codeHash(code) {
		t.Fatal("failed generation invalidated original code")
	}
	if err := s.Unbind("alice", first); err == nil {
		t.Fatal("failed unbind ignored save failure")
	}
	if len(s.state.Bindings) != 1 || len(s.state.Pending) != 1 {
		t.Fatal("failed unbind removed binding/queue")
	}
	s.path = path
	if err := s.Bind(code, Target{Provider: "fake", Type: "room", ID: "second"}); err != nil {
		t.Fatal(err)
	}
}

func TestDynamicTargetsOnlyReceiveFutureEventsAndDeduplicateStaticTargets(t *testing.T) {
	cfg := testConfig()
	cfg.Targets = nil
	sender := &testSender{}
	s := newTestService(t, cfg, sender)
	if err := s.PublishReport(Report{SessionID: "before", Content: "old report"}); err != nil {
		t.Fatal(err)
	}
	target := Target{Provider: "fake", Type: "room", ID: "new-group"}
	bindTestTarget(t, s, "alice", target)
	if len(s.state.Pending) != 0 {
		t.Fatal("new binding received backlog")
	}
	if err := s.PublishReport(Report{SessionID: "after", Content: "new report"}); err != nil {
		t.Fatal(err)
	}
	if len(s.state.Pending) != 1 || s.state.Pending[0].Target != target {
		t.Fatal("dynamic binding not queued")
	}
	cfg.Targets = []Target{target}
	restored, err := New(cfg, s.path, s.senders)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.state.Pending) != 1 {
		t.Fatal("restart lost dynamic target pending message")
	}
	if status := restored.BindingStatus("alice"); len(status.Bindings) != 1 || !status.Bindings[0].Configured {
		t.Fatal("configuration overlap not exposed to binding owner")
	}
	if err := restored.PublishReport(Report{SessionID: "both", Content: "dedup"}); err != nil {
		t.Fatal(err)
	}
	if len(restored.state.Pending) != 2 {
		t.Fatal("static and dynamic target not deduplicated")
	}
	if err := restored.Unbind("alice", target); err != nil {
		t.Fatal(err)
	}
	if len(restored.state.Pending) != 2 {
		t.Fatal("unbind should preserve configured static target queue")
	}
	code, _, err := restored.NewBindingCode("bob", "fake", "room")
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Bind(code, target); !errors.Is(err, ErrAlreadyBound) {
		t.Fatal("static target was bound again")
	}
}

type blockingBindingSender struct{ started, release chan struct{} }

func (*blockingBindingSender) Validate(Target) error { return nil }
func (s *blockingBindingSender) Send(context.Context, Target, string) error {
	close(s.started)
	<-s.release
	return nil
}

func TestUnbindDuringSendDoesNotCorruptAnotherTargetsQueue(t *testing.T) {
	cfg := testConfig()
	cfg.Targets = nil
	sender := &blockingBindingSender{make(chan struct{}), make(chan struct{})}
	s, err := New(cfg, t.TempDir()+"/state.json", map[string]Sender{"fake": sender})
	if err != nil {
		t.Fatal(err)
	}
	first := Target{Provider: "fake", Type: "room", ID: "first"}
	second := Target{Provider: "fake", Type: "room", ID: "second"}
	bindTestTarget(t, s, "alice", first)
	bindTestTarget(t, s, "bob", second)
	if err := s.PublishReport(Report{SessionID: "shared", Content: "report"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); s.sendNext(context.Background(), time.Now()) }()
	<-sender.started
	if err := s.Unbind("alice", first); err != nil {
		t.Fatal(err)
	}
	close(sender.release)
	<-done
	if len(s.state.Pending) != 1 || s.state.Pending[0].Target != second || s.state.Pending[0].NextPart != 0 {
		t.Fatal("in-flight completion corrupted another target's pending report")
	}
}
