package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
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
	wrapped := summaryCompatibleModel(model, "writer", nil)
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

func TestSummaryCompatibleModelRetriesEmptyContent(t *testing.T) {
	model := &utf8CheckingSummaryModel{emptyResponses: 1}
	var retries []int
	wrapped := summaryCompatibleModel(model, "coordinator", func(agent string, retry, maxRetries int, _ time.Duration) {
		if agent != "coordinator" || maxRetries != summaryMaxAttempts-1 {
			t.Fatalf("retry hook = agent %q max %d", agent, maxRetries)
		}
		retries = append(retries, retry)
	})
	summary := wrapped.(*summaryModel)
	summary.delay = func(int) time.Duration { return 0 }
	summary.wait = func(context.Context, time.Duration) error { return nil }

	response, err := wrapped.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if model.calls != 2 {
		t.Fatalf("calls = %d, want 2", model.calls)
	}
	if !summaryResponseHasContent(response) {
		t.Fatal("retry did not return the later non-empty summary")
	}
	if len(retries) != 1 || retries[0] != 1 {
		t.Fatalf("retries = %v, want [1]", retries)
	}
}

func TestSummaryCompatibleModelStopsRetryWhenCanceled(t *testing.T) {
	model := &utf8CheckingSummaryModel{emptyResponses: summaryMaxAttempts}
	ctx, cancel := context.WithCancel(context.Background())
	wrapped := summaryCompatibleModel(model, "coordinator", nil).(*summaryModel)
	wrapped.delay = func(int) time.Duration { return time.Second }
	wrapped.wait = func(context.Context, time.Duration) error {
		cancel()
		return ctx.Err()
	}

	if _, err := wrapped.Generate(ctx, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate error = %v, want context.Canceled", err)
	}
	if model.calls != 1 {
		t.Fatalf("calls = %d, want 1", model.calls)
	}
}

type utf8CheckingSummaryModel struct {
	calls          int
	callConfig     agentcore.CallConfig
	emptyResponses int
}

func (m *utf8CheckingSummaryModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.calls++
	m.callConfig = agentcore.ResolveCallConfig(opts)
	if m.calls <= m.emptyResponses {
		return &agentcore.LLMResponse{Message: agentcore.Message{Role: agentcore.RoleAssistant}}, nil
	}
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
