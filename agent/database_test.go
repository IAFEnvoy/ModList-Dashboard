package agent

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/glebarez/sqlite"
)

func TestMessagePersistencePreservesAppendOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	testDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer testDB.Close()
	oldDB := db
	db = testDB
	defer func() { db = oldDB }()
	if err := createSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	session := &ChatSession{
		ID:        "ordered",
		Agent:     "test",
		Provider:  "test",
		CreatedAt: now,
		Messages:  []ChatMessage{{Type: "system", Content: "system"}},
	}
	if err := dbCreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, content := range []string{"first", "second", "third"} {
		dbAppendMessage(session.ID, ChatMessage{Type: "message", Content: content})
	}
	rows, err := db.Query("SELECT content FROM messages WHERE session_id=? AND type='message' ORDER BY seq", session.ID)
	if err != nil {
		t.Fatalf("query messages: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			t.Fatalf("scan message: %v", err)
		}
		got = append(got, content)
	}
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("message count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("message %d = %q, want %q", i, got[i], want[i])
		}
	}
}
