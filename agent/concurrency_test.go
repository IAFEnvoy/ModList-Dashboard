package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSessionPointersRemainStableWhenListGrows(t *testing.T) {
	oldSessions := AgentSessions
	defer func() { AgentSessions = oldSessions }()
	original := &ChatSession{ID: "original"}
	AgentSessions = []*ChatSession{original}
	for i := 0; i < 1000; i++ {
		AgentSessions = append(AgentSessions, &ChatSession{ID: "other"})
	}
	if got := GetSession("original"); got != original {
		t.Fatal("session pointer changed after slice growth")
	}
}

func TestExecutionContextsKeepSessionsIsolated(t *testing.T) {
	ids := []string{"session-a", "session-b"}
	var wg sync.WaitGroup
	errs := make(chan string, len(ids))
	for _, id := range ids {
		wg.Add(1)
		go func(sessionID string) {
			defer wg.Done()
			ctx := withExecSession(context.Background(), sessionID)
			if got := execSessionID(ctx); got != sessionID {
				errs <- got
				return
			}
			want := filepath.Join("data", "drive", "agent", sessionID)
			if got := driveAgentDir(ctx); got != want {
				errs <- got
			}
		}(id)
	}
	wg.Wait()
	close(errs)
	for got := range errs {
		t.Fatalf("session context crossed: %q", got)
	}
}

func TestResolveWithinRootRejectsSiblingTraversal(t *testing.T) {
	dir, err := os.MkdirTemp(".", ".agent-test-")
	if err != nil {
		t.Fatalf("create local temp dir: %v", err)
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolve local temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if _, err := resolveWithinRoot(root, filepath.Join("..", "agent-backup")); err == nil {
		t.Fatal("sibling traversal was accepted")
	}
	if _, err := resolveWithinRoot(root, "nested"); err != nil {
		t.Fatalf("nested path rejected: %v", err)
	}
}
