package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
)

func TestPreprocessReservesFinalRoundForReportWithEvidenceAndNoTools(t *testing.T) {
	prev := AgentSessions
	AgentSessions = []*ChatSession{{ID: "budget", Agent: IssuePreprocessorID}}
	t.Cleanup(func() { AgentSessions = prev })
	root := t.TempDir()
	const source = "package example\n// evidence: missing bounds check\n"
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	def := issuePreprocessorDef()
	tools := BuildTools(&def)
	calls := 0
	const report = "## 真实性结论\n信息不足；source.go:2 提供部分线索。\n## 解决方案\n检查边界条件。\n## 验证计划\n由维护者复现。"
	model := issuePreprocessTestModel{generateWithOptions: func(_ context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
		calls++
		var opts llms.CallOptions
		for _, option := range options {
			option(&opts)
		}
		reminders := 0
		for _, message := range messages {
			for _, part := range message.Parts {
				if text, ok := part.(llms.TextContent); ok && strings.Contains(text.Text, "预处理轮数预算") {
					reminders++
				}
			}
		}
		if reminders != 1 {
			t.Fatalf("budget reminders accumulated in history: %d", reminders)
		}
		budget := messages[len(messages)-1].Parts[0].(llms.TextContent).Text
		if !strings.Contains(budget, fmt.Sprintf("第 %d/%d 轮", calls, issuePreprocessMaxRounds)) {
			t.Fatalf("wrong budget: %s", budget)
		}
		if calls == issuePreprocessMaxRounds {
			if len(opts.Tools) != 0 || opts.ToolChoice != "none" || !strings.Contains(budget, "最终报告阶段") {
				t.Fatal("last round still allowed tools")
			}
			foundEvidence := false
			for _, message := range messages {
				for _, part := range message.Parts {
					if result, ok := part.(llms.ToolCallResponse); ok && strings.Contains(result.Content, "missing bounds check") {
						foundEvidence = true
					}
				}
			}
			if !foundEvidence {
				t.Fatal("final report lost collected evidence")
			}
			return &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: report}}}, nil
		}
		if len(opts.Tools) == 0 || opts.ToolChoice == "none" {
			t.Fatal("tools disabled before final round")
		}
		if calls == issuePreprocessMaxRounds-5 && !strings.Contains(budget, "不再扩展检索范围") {
			t.Fatal("missing early wrap-up reminder")
		}
		return &llms.ContentResponse{Choices: []*llms.ContentChoice{{ToolCalls: []llms.ToolCall{{ID: fmt.Sprintf("read-%d", calls), Type: "function", FunctionCall: &llms.FunctionCall{Name: "read_file", Arguments: `{"path":"source.go","startLine":1,"endLine":2}`}}}}}}, nil
	}}
	ctx := withExecSession(context.Background(), "budget")
	reply, blocks, _, _, err := runAgentLoop(ctx, &agentRuntime{llm: model, tools: tools}, nil, 0.3, root, "test/model", issuePreprocessMaxRounds, func(string) {})
	if err != nil || reply != report || calls != issuePreprocessMaxRounds {
		t.Fatalf("finalization failed: reply=%q calls=%d err=%v", reply, calls, err)
	}
	if len(blocks) != 2*(issuePreprocessMaxRounds-1)+1 || msgContent(blocks[len(blocks)-1]) != report {
		t.Fatal("tool trace or final report missing")
	}
	got, _ := os.ReadFile(filepath.Join(root, "source.go"))
	if string(got) != source {
		t.Fatal("preprocessing modified sources")
	}
}

func TestPreprocessFinalizationRejectsFurtherToolsAndPartialOutputs(t *testing.T) {
	prev := AgentSessions
	AgentSessions = []*ChatSession{{ID: "final", Agent: IssuePreprocessorID}}
	t.Cleanup(func() { AgentSessions = prev })
	for _, test := range []struct {
		name   string
		choice *llms.ContentChoice
	}{
		{"tool request", &llms.ContentChoice{Content: "还需要进一步检索", ToolCalls: []llms.ToolCall{{ID: "late", Type: "function", FunctionCall: &llms.FunctionCall{Name: "list_files", Arguments: `{}`}}}}},
		{"delegation request", &llms.ContentChoice{Content: "CALL: assistant 请继续调查"}},
		{"empty output", &llms.ContentChoice{Content: "  "}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls, emitted := 0, 0
			model := issuePreprocessTestModel{generate: func(context.Context, []llms.MessageContent) (*llms.ContentResponse, error) {
				calls++
				return &llms.ContentResponse{Choices: []*llms.ContentChoice{test.choice}}, nil
			}}
			ctx := withExecSession(context.Background(), "final")
			reply, blocks, _, _, err := runAgentLoop(ctx, &agentRuntime{llm: model}, nil, 0.3, t.TempDir(), "test/model", 1, func(string) { emitted++ })
			if err == nil || reply != "" || calls != 1 || len(blocks) != 0 || emitted != 0 {
				t.Fatalf("unfinished output accepted or tools executed: %q %+v %v", reply, blocks, err)
			}
		})
	}
}

func TestPreprocessReservesTimeForReportBeforeTimeout(t *testing.T) {
	prev := AgentSessions
	AgentSessions = []*ChatSession{{ID: "time-budget", Agent: IssuePreprocessorID}}
	t.Cleanup(func() { AgentSessions = prev })
	calls := 0
	model := issuePreprocessTestModel{generateWithOptions: func(_ context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
		calls++
		var opts llms.CallOptions
		for _, option := range options {
			option(&opts)
		}
		if len(opts.Tools) != 0 || opts.ToolChoice != "none" {
			t.Fatal("timeout budget did not disable tools")
		}
		if !strings.Contains(messages[len(messages)-1].Parts[0].(llms.TextContent).Text, "最终报告阶段") {
			t.Fatal("missing deadline wrap-up instruction")
		}
		return &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: "信息不足，已查询的范围有限，请维护者按所列步骤验证。"}}}, nil
	}}
	ctx, cancel := context.WithTimeout(withExecSession(context.Background(), "time-budget"), 20*time.Second)
	defer cancel()
	if _, _, _, _, err := runAgentLoop(ctx, &agentRuntime{llm: model}, nil, 0.3, t.TempDir(), "test/model", 30, nil); err != nil || calls != 1 {
		t.Fatalf("deadline finalization failed: %v", err)
	}
}

func TestOrdinaryAgentsKeepTheirToolBudget(t *testing.T) {
	prev := AgentSessions
	AgentSessions = []*ChatSession{{ID: "ordinary", Agent: "assistant"}}
	t.Cleanup(func() { AgentSessions = prev })
	calls := 0
	model := issuePreprocessTestModel{generateWithOptions: func(_ context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
		calls++
		var opts llms.CallOptions
		for _, option := range options {
			option(&opts)
		}
		if opts.ToolChoice == "none" || len(messages) != 0 {
			t.Fatal("preprocessing budget leaked into ordinary Agent")
		}
		return &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: "ordinary reply"}}}, nil
	}}
	ctx := withExecSession(context.Background(), "ordinary")
	if _, _, _, _, err := runAgentLoop(ctx, &agentRuntime{llm: model}, nil, 0.3, t.TempDir(), "test/model", 1, nil); err != nil || calls != 1 {
		t.Fatal(err)
	}
}

func TestFailedPreprocessKeepsSessionAndToolTraceForConversation(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.Mkdir("config", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("config", "ai.yml"), []byte("default_model: test/model\n"), 0644); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", filepath.Join(root, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	prevDB, prevDefs, prevSessions, prevModels, prevCache, prevInstances, prevBroadcasters := db, AgentDefs, AgentSessions, ModelIndex, llmCache, agentInstances, sessionBroadcasters
	db, AgentDefs, AgentSessions = database, []AgentDef{issuePreprocessorDef()}, nil
	AgentDefs[0].MaxRounds = 2
	info := AIModelInfo{Key: "test/model", Model: "model", APIKey: "test", MaxTokens: 1024, MaxContextLength: 64000}
	ModelIndex = map[string]*AIModelInfo{info.Key: &info}
	agentInstances, sessionBroadcasters = make(map[string]*agentRuntime), make(map[string]*SSEBroadcaster)
	t.Cleanup(func() {
		database.Close()
		db, AgentDefs, AgentSessions, ModelIndex, llmCache, agentInstances, sessionBroadcasters = prevDB, prevDefs, prevSessions, prevModels, prevCache, prevInstances, prevBroadcasters
	})
	if err := createSchema(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	model := issuePreprocessTestModel{generate: func(context.Context, []llms.MessageContent) (*llms.ContentResponse, error) {
		calls++
		return &llms.ContentResponse{Choices: []*llms.ContentChoice{{ToolCalls: []llms.ToolCall{{ID: fmt.Sprintf("list-%d", calls), Type: "function", FunctionCall: &llms.FunctionCall{Name: "list_files", Arguments: `{"path":"."}`}}}}}}, nil
	}}
	tools := BuildTools(&AgentDefs[0])
	llmCache = map[string]*agentRuntime{info.Key + ":" + toolsHash(tools): {llm: model, tools: tools, info: info}}
	linked := ""
	sid, err := RunIssuePreprocess(IssuePreprocessInput{Project: "example", ProjectPath: root, Title: "Budget failure", Attachment: "Investigate"}, func(sid string) error { linked = sid; return nil })
	if !errors.Is(err, errMaxRoundsExceeded) || sid == "" || linked != sid {
		t.Fatalf("failure lost session link: sid=%s linked=%s err=%v", sid, linked, err)
	}
	session := dbGetSession(sid)
	if session == nil || !session.LimitExceeded || GetSession(sid).Running {
		t.Fatal("failed session is not available for inspection and continuation")
	}
	toolCalls, results := 0, 0
	for _, message := range session.Messages {
		if message.Type == "tool_call" {
			toolCalls++
		}
		if message.Type == "tool_result" {
			results++
		}
	}
	if toolCalls != 1 || results != 1 {
		t.Fatalf("lost tool conversation or executed final tool: calls=%d results=%d", toolCalls, results)
	}
	if IssuePreprocessReportText(sid) != "" {
		t.Fatal("failed task exposed a completed report")
	}
}
