package agent

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
)

func TestIssuePreprocessReportTextExcludesToolsAndInProgressSessions(t *testing.T) {
	previous := AgentSessions
	t.Cleanup(func() { AgentSessions = previous })
	AgentSessions = []*ChatSession{{ID: "report", Agent: IssuePreprocessorID, Messages: []ChatMessage{
		{Type: "attachment", Content: "private attachment"},
		{Type: "message", Content: "initial thoughts"},
		{Type: "tool_result", Content: map[string]string{"content": "private tool result"}},
		{Type: "message", Content: "final report"},
		{Type: "thinking", Content: "private reasoning"},
	}}}
	if got := IssuePreprocessReportText("report"); got != "final report" {
		t.Fatalf("extracted report = %q", got)
	}
	AgentSessions[0].Running = true
	if got := IssuePreprocessReportText("report"); got != "" {
		t.Fatal("unfinished report was exposed")
	}
	AgentSessions[0].Running, AgentSessions[0].Agent = false, "other-agent"
	if got := IssuePreprocessReportText("report"); got != "" {
		t.Fatal("another agent's messages were exposed")
	}
}

func TestIssuePreprocessRejectsWritesShellMCPAndDelegation(t *testing.T) {
	prev := AgentSessions
	AgentSessions = []*ChatSession{{ID: "readonly-issue", Agent: IssuePreprocessorID, BypassApproval: true}}
	t.Cleanup(func() { AgentSessions = prev })
	ctx := withExecSession(context.Background(), "readonly-issue")
	for _, tool := range []string{"write_file", "create_file", "create_directory", "replace_string_in_file", "multi_replace_string_in_file", "shell_exec", "drive_write", "drive_delete", "drive_mkdir", "github__create_issue", "ask_user", "manage_todo_list"} {
		if _, err := ExecuteTool(ctx, tool, `{}`, t.TempDir()); err == nil {
			t.Fatalf("read-only session allowed %s", tool)
		}
		if RequestApproval(ctx, tool, `{}`) {
			t.Fatalf("approval bypass allowed %s", tool)
		}
	}
	if _, err := spawnSubAgent(ctx, nil, "modify code", "", ""); err == nil {
		t.Fatal("read-only session allowed a subagent")
	}
	AgentSessions[0].BypassApproval = false
	for _, name := range []string{"fetch_webpage", "download_file"} {
		if !RequestApproval(ctx, name, `{}`) {
			t.Fatalf("automatic read-only analysis would wait for approval of %s", name)
		}
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("package example\n"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := ExecuteTool(ctx, "read_file", `{"path":"source.go","startLine":1,"endLine":2}`, root)
	if err != nil || !strings.Contains(result, "package example") {
		t.Fatalf("read-only session cannot inspect source: %q %v", result, err)
	}
}

type issuePreprocessTestModel struct {
	generate            func(context.Context, []llms.MessageContent) (*llms.ContentResponse, error)
	generateWithOptions func(context.Context, []llms.MessageContent, ...llms.CallOption) (*llms.ContentResponse, error)
}

func (m issuePreprocessTestModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, opts ...llms.CallOption) (*llms.ContentResponse, error) {
	if m.generateWithOptions != nil {
		return m.generateWithOptions(ctx, messages, opts...)
	}
	return m.generate(ctx, messages)
}

func (m issuePreprocessTestModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	return "", errors.New("unexpected text-only model call")
}

func TestIssuePreprocessProducesPersistentReportWithoutChangingSources(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.Mkdir("config", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("config", "ai.yml"), []byte("default_model: test/model\n"), 0644); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(root, "source.go")
	const source = "package example\n// bug: missing bounds check\n"
	if err := os.WriteFile(sourcePath, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	testDB, err := sql.Open("sqlite", filepath.Join(root, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	prevDB, prevDefs, prevSessions, prevModels, prevCache, prevInstances, prevBroadcasters := db, AgentDefs, AgentSessions, ModelIndex, llmCache, agentInstances, sessionBroadcasters
	db, AgentDefs, AgentSessions = testDB, []AgentDef{issuePreprocessorDef()}, nil
	info := AIModelInfo{Key: "test/model", Model: "model", APIKey: "test", MaxTokens: 1024, MaxContextLength: 64000}
	ModelIndex = map[string]*AIModelInfo{info.Key: &info}
	agentInstances = make(map[string]*agentRuntime)
	sessionBroadcasters = make(map[string]*SSEBroadcaster)
	t.Cleanup(func() {
		testDB.Close()
		db, AgentDefs, AgentSessions, ModelIndex, llmCache, agentInstances, sessionBroadcasters = prevDB, prevDefs, prevSessions, prevModels, prevCache, prevInstances, prevBroadcasters
	})
	if err := createSchema(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	const report = "# Issue 预处理报告\n已确认存在：source.go:2 缺少边界检查。\n解决方案：添加边界检查。\n验证计划：检查空输入和越界输入。"
	model := issuePreprocessTestModel{generate: func(_ context.Context, _ []llms.MessageContent) (*llms.ContentResponse, error) {
		calls++
		choice := &llms.ContentChoice{}
		switch calls {
		case 1:
			// A model may hallucinate a capability that was never advertised.
			choice.ToolCalls = []llms.ToolCall{{ID: "blocked", Type: "function", FunctionCall: &llms.FunctionCall{Name: "write_file", Arguments: `{"path":"source.go","content":"modified"}`}}}
		case 2:
			choice.ToolCalls = []llms.ToolCall{{ID: "read", Type: "function", FunctionCall: &llms.FunctionCall{Name: "read_file", Arguments: `{"path":"source.go","startLine":1,"endLine":2}`}}}
		default:
			choice.Content = report
		}
		return &llms.ContentResponse{Choices: []*llms.ContentChoice{choice}}, nil
	}}
	tools := BuildTools(&AgentDefs[0])
	llmCache = map[string]*agentRuntime{info.Key + ":" + toolsHash(tools): {llm: model, tools: tools, info: info}}
	linked := ""
	sid, err := RunIssuePreprocess(IssuePreprocessInput{Project: "example", ProjectPath: root, Title: "Issue #1", Attachment: "Investigate missing bounds check"}, func(sid string) error { linked = sid; return nil })
	if err != nil || sid == "" || linked != sid || calls != 3 {
		t.Fatalf("report failed: sid=%q linked=%q calls=%d err=%v", sid, linked, calls, err)
	}
	stored := dbGetSession(sid)
	if stored == nil || stored.Agent != IssuePreprocessorID || GetSession(sid).Running {
		t.Fatal("report session was not persisted or finished")
	}
	found := false
	for _, msg := range stored.Messages {
		if msg.Type == "message" && msgContent(msg) == report {
			found = true
		}
	}
	if !found {
		t.Fatal("final report missing from persisted messages")
	}
	got, err := os.ReadFile(sourcePath)
	if err != nil || string(got) != source {
		t.Fatalf("source changed: %q %v", got, err)
	}
}

func TestIssuePreprocessDoesNotTreatPartialToolResultsAsReport(t *testing.T) {
	prev := AgentSessions
	AgentSessions = []*ChatSession{{ID: "partial", Agent: IssuePreprocessorID, CreatedAt: time.Now().Format(time.RFC3339)}}
	t.Cleanup(func() { AgentSessions = prev })
	calls := 0
	model := issuePreprocessTestModel{generate: func(_ context.Context, _ []llms.MessageContent) (*llms.ContentResponse, error) {
		calls++
		if calls == 1 {
			return &llms.ContentResponse{Choices: []*llms.ContentChoice{{ToolCalls: []llms.ToolCall{{ID: "read", FunctionCall: &llms.FunctionCall{Name: "read_file", Arguments: `{}`}}}}}}, nil
		}
		return nil, errors.New("model connection lost")
	}}
	ctx := withExecSession(context.Background(), "partial")
	_, _, _, _, err := runAgentLoop(ctx, &agentRuntime{llm: model}, nil, 0.3, t.TempDir(), "test/model", 5, nil)
	if err == nil {
		t.Fatal("partial tool output was incorrectly accepted as a completed report")
	}
}

func TestIssuePreprocessCapabilitiesCannotBeBroadenedByConfig(t *testing.T) {
	prev := AgentDefs
	t.Cleanup(func() { AgentDefs = prev })
	path := filepath.Join(t.TempDir(), "agents.yml")
	if err := os.WriteFile(path, []byte("agents:\n  - id: issue-preprocessor\n    mcps: [shell, read_write]\n    sub_agents: [bug-fixer]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	loadAgents(path)
	def := FindAgentDef(IssuePreprocessorID)
	if def == nil || len(def.SubAgents) != 0 {
		t.Fatal("built-in role was overridden by config")
	}
	def.MCPs = []string{"shell", "read_write", "drive", "external"}
	for _, tool := range BuildTools(def) {
		switch tool.Function.Name {
		case "read_file", "read_files", "list_files", "view_image", "file_search", "grep_search", "fetch_webpage", "download_file":
		default:
			t.Fatalf("unexpected exposed capability: %s", tool.Function.Name)
		}
	}
}
