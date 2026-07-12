package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
)

func TestWriterContextProjectionCommitsCompactedBaseline(t *testing.T) {
	mgr := newContextManager(contextManagerConfig{
		ContextWindow:    4_000,
		ReserveTokens:    2_000,
		KeepRecentTokens: 500,
		Agent:            "writer",
		CommitOnProject:  true,
	})
	messages := []agentcore.AgentMessage{agentcore.UserMsg("current chapter task")}
	for index := range 8 {
		callID := fmt.Sprintf("call-%d", index)
		messages = append(messages,
			agentcore.Message{
				Role: agentcore.RoleAssistant,
				Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{
					ID: callID, Name: "read_chapter", Args: []byte(fmt.Sprintf(`{"chapter":%d}`, index+1)),
				})},
			},
			agentcore.ToolResultMsg(callID, []byte(fmt.Sprintf(`"%s"`, strings.Repeat("chapter context ", 1_500))), false),
		)
	}

	projection, err := mgr.Project(context.Background(), messages)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if !projection.ShouldCommit {
		t.Fatal("writer compaction must commit so the next turn cannot restore the oversized history")
	}
	if len(projection.CommitMessages) == 0 {
		t.Fatal("committed writer projection must include the compacted messages")
	}
	snapshot := mgr.Snapshot()
	if snapshot == nil || snapshot.BaselineUsage == nil {
		t.Fatal("writer context snapshot must retain pre-compaction baseline usage")
	}
	if got, before := mgr.Usage().Tokens, snapshot.BaselineUsage.Tokens; got >= before {
		t.Fatalf("compacted usage=%d, baseline=%d; want compacted usage below baseline", got, before)
	}
}

func TestContextSummaryKeepsChineseToolResultsValidUTF8(t *testing.T) {
	model := &utf8CheckingSummaryModel{}
	mgr := corecontext.NewEngine(corecontext.EngineConfig{
		ContextWindow: 800,
		ReserveTokens: 400,
		Strategies: []corecontext.Strategy{corecontext.NewFullSummary(corecontext.FullSummaryConfig{
			Model:            model,
			KeepRecentTokens: 5_000,
		})},
	})
	messages := []agentcore.AgentMessage{agentcore.UserMsg("继续创作")}
	for index := range 8 {
		callID := fmt.Sprintf("novel-context-%d", index)
		messages = append(messages,
			agentcore.UserMsg(fmt.Sprintf("第 %d 轮", index+1)),
			agentcore.Message{
				Role: agentcore.RoleAssistant,
				Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{
					ID: callID, Name: "novel_context", Args: []byte(`{}`),
				})},
			},
			agentcore.ToolResultMsg(callID, []byte(fmt.Sprintf(`"%s"`, strings.Repeat("武林上下文", 400))), false),
		)
	}

	if _, err := mgr.Compact(context.Background(), messages, agentcore.CompactReasonManual); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if model.calls == 0 {
		t.Fatal("expected context summarization model to be called")
	}
}

func TestSummaryCompatibleModelOmitsForcedThinkingOff(t *testing.T) {
	model := &utf8CheckingSummaryModel{}
	wrapped := summaryCompatibleModel(model)
	if _, err := wrapped.Generate(context.Background(), nil, nil, agentcore.WithMaxTokens(800), agentcore.WithThinking(agentcore.ThinkingOff)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if model.callConfig.MaxTokens != 800 {
		t.Fatalf("max tokens = %d, want 800", model.callConfig.MaxTokens)
	}
	if model.callConfig.ThinkingLevel != agentcore.ThinkingAuto {
		t.Fatalf("thinking level = %q, want provider-default auto", model.callConfig.ThinkingLevel)
	}
}

type utf8CheckingSummaryModel struct {
	calls      int
	callConfig agentcore.CallConfig
}

func (m *utf8CheckingSummaryModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.calls++
	m.callConfig = agentcore.ResolveCallConfig(opts)
	for index, message := range messages {
		for _, block := range message.Content {
			if block.Type == agentcore.ContentText && !utf8.ValidString(block.Text) {
				return nil, fmt.Errorf("message %d contains invalid UTF-8", index)
			}
		}
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock("<summary>保留当前任务与关键上下文。</summary>")},
		StopReason: agentcore.StopReasonStop,
	}}, nil
}

func (m *utf8CheckingSummaryModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, errors.New("streaming is not used for context summaries")
}

func (m *utf8CheckingSummaryModel) SupportsTools() bool { return false }
