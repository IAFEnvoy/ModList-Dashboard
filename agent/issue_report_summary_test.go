package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
)

func TestReportSummaryUsesOneIsolatedToolFreeCall(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("config", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("config", "ai.yml"), []byte("default_model: test/model\n"), 0644); err != nil {
		t.Fatal(err)
	}
	previousModels, previousCache, previousSessions := ModelIndex, llmCache, AgentSessions
	t.Cleanup(func() { ModelIndex, llmCache, AgentSessions = previousModels, previousCache, previousSessions })
	info := AIModelInfo{Key: "test/model", Model: "model", APIKey: "test", MaxTokens: 8000}
	ModelIndex = map[string]*AIModelInfo{info.Key: &info}
	AgentSessions = []*ChatSession{{ID: "original", Messages: []ChatMessage{{Type: "tool_result", Content: "PRIVATE TOOL OUTPUT"}, {Type: "message", Content: "original report"}}}}
	const report = "# 报告\n已确认输入校验存在问题，建议补充边界检查。"
	const summary = "已确认输入校验存在问题，建议补充边界检查。"
	calls := 0
	model := issuePreprocessTestModel{generateWithOptions: func(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
		calls++
		var opts llms.CallOptions
		for _, option := range options {
			option(&opts)
		}
		if len(opts.Tools) != 0 || opts.ToolChoice != "none" || opts.MaxTokens != 512 {
			t.Fatal("summary call has tools or unbounded output")
		}
		if len(messages) != 2 || messages[0].Role != llms.ChatMessageTypeSystem || messages[1].Role != llms.ChatMessageTypeHuman {
			t.Fatal("summary reused Agent history")
		}
		if messages[1].Parts[0].(llms.TextContent).Text != report {
			t.Fatal("summary input was not exactly final report")
		}
		prompt := messages[0].Parts[0].(llms.TextContent).Text
		for _, rule := range []string{"200 字", "纯文本", "代码路径", "复现步骤", "不确定性", "待处理数据"} {
			if !strings.Contains(prompt, rule) {
				t.Errorf("missing summary requirement: %s", rule)
			}
		}
		return &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: summary}}}, nil
	}}
	llmCache = map[string]*agentRuntime{info.Key + ":" + toolsHash(nil): {llm: model, info: info}}
	got, err := SummarizeIssueReport(context.Background(), report)
	if err != nil || got != summary || calls != 1 {
		t.Fatalf("summary failed: %q calls=%d err=%v", got, calls, err)
	}
	if len(AgentSessions) != 1 || len(AgentSessions[0].Messages) != 2 || AgentSessions[0].Messages[1].Content != "original report" {
		t.Fatal("summary changed source conversation")
	}
	for _, test := range []struct {
		name     string
		response *llms.ContentResponse
		err      error
	}{
		{"network failure", nil, errors.New("model unavailable")},
		{"nil response", nil, nil},
		{"empty response", &llms.ContentResponse{}, nil},
		{"whitespace", &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: " "}}}, nil},
		{"tool request", &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: summary, ToolCalls: []llms.ToolCall{{ID: "unexpected"}}}}}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			attempts := 0
			llmCache[info.Key+":"+toolsHash(nil)].llm = issuePreprocessTestModel{generate: func(context.Context, []llms.MessageContent) (*llms.ContentResponse, error) {
				attempts++
				return test.response, test.err
			}}
			if result, err := SummarizeIssueReport(context.Background(), report); err == nil || result != "" || attempts != 1 {
				t.Fatalf("invalid summary accepted or retried: %q %v attempts=%d", result, err, attempts)
			}
		})
	}
}

func TestReportShareIsPublicAndExposesOnlyFinalReport(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	previousDB, previousSessions := db, AgentSessions
	db = database
	t.Cleanup(func() { database.Close(); db, AgentSessions = previousDB, previousSessions })
	if err := createSchema(); err != nil {
		t.Fatal(err)
	}
	const report = "# 最终报告\n问题已确认，建议修复输入校验。"
	session := &ChatSession{ID: "share-report", Agent: IssuePreprocessorID, Title: "Issue report", CreatedAt: time.Now().UTC().Format(time.RFC3339), Messages: []ChatMessage{
		{Type: "system", Content: "PRIVATE SYSTEM"},
		{Type: "attachment", Content: "PRIVATE ATTACHMENT"},
		{Type: "message", Content: "PRIVATE INTERMEDIATE"},
		{Type: "tool_result", Content: "PRIVATE TOOL OUTPUT"},
		{Type: "message", Content: report},
		{Type: "thinking", Content: "PRIVATE REASONING"},
	}}
	AgentSessions = []*ChatSession{session}
	if err := dbCreateSession(session); err != nil {
		t.Fatal(err)
	}
	id, err := CreateIssueReportShare(session.ID, report)
	if err != nil || id == "" {
		t.Fatalf("share creation failed: %s %v", id, err)
	}
	second, err := CreateIssueReportShare(session.ID, report)
	if err != nil || second != id {
		t.Fatal("same report created duplicate public shares")
	}
	w := httptest.NewRecorder()
	HandleAgentShareGet(w, httptest.NewRequest(http.MethodGet, "/api/agent/share/"+id, nil)) // No login token.
	var response struct {
		Messages []ChatMessage `json:"messages"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Messages) != 1 || response.Messages[0].Content != report || strings.Contains(w.Body.String(), "PRIVATE") {
		t.Fatalf("public share leaked conversation or required login: %d %s", w.Code, w.Body)
	}
	session.Running = true
	if _, err := CreateIssueReportShare(session.ID, report); err == nil {
		t.Fatal("running report was shared")
	}
	session.Running, session.LimitExceeded = false, true
	if _, err := CreateIssueReportShare(session.ID, report); err == nil {
		t.Fatal("incomplete report was shared")
	}
	session.LimitExceeded = false
	session.Messages = append(session.Messages, ChatMessage{Type: "message", Content: "PRIVATE FOLLOW-UP"})
	if reused, err := CreateIssueReportShare(session.ID, report); err != nil || reused != id {
		t.Fatal("later conversation changed original report share")
	}
	if _, err := CreateIssueReportShare(session.ID, "nonexistent report"); err == nil {
		t.Fatal("share silently selected an unrelated message")
	}
}
