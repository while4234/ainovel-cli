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

func TestWriterToolResultsCompactByValidationPhase(t *testing.T) {
	messages := []agentcore.AgentMessage{agentcore.UserMsg("polish chapter 39")}
	for index, toolName := range []string{"novel_context", "read_chapter", "check_consistency", "check_de_ai"} {
		callID := fmt.Sprintf("phase-%d", index)
		messages = append(messages,
			agentcore.Message{
				Role: agentcore.RoleAssistant,
				Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{
					ID: callID, Name: toolName, Args: []byte(`{"chapter":39}`),
				})},
			},
			agentcore.ToolResultMsg(callID, []byte(fmt.Sprintf(`"%s"`, strings.Repeat(toolName+" evidence ", 300))), false),
		)
	}
	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.Apply(context.Background(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Applied {
		t.Fatal("expected older Writer phase results to be compacted")
	}
	for _, index := range []int{2, 4} {
		message := view[index].(agentcore.Message)
		if message.Metadata["compacted_tool_result"] != true {
			t.Fatalf("tool result at index %d was not compacted: %+v", index, message.Metadata)
		}
	}
	for _, index := range []int{6, 8} {
		message := view[index].(agentcore.Message)
		if message.Metadata["compacted_tool_result"] == true {
			t.Fatalf("recent validation result at index %d was compacted", index)
		}
	}
}

func TestWriterPhaseKeepsContextAndDraftUntilValidation(t *testing.T) {
	messages := writerPhaseMessages(t, "novel_context", "read_chapter")
	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || countCompactedToolResults(view) != 0 {
		t.Fatalf("pre-validation evidence must remain intact: result=%+v compacted=%d", result, countCompactedToolResults(view))
	}
}

func TestWriterPhaseDeduplicatesRepeatedCurrentContextBeforeBoundary(t *testing.T) {
	messages := writerPhaseMessages(t, "novel_context", "novel_context")
	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || countCompactedToolResults(view) != 1 {
		t.Fatalf("duplicate current context result=%+v compacted=%d", result, countCompactedToolResults(view))
	}
	if first := view[2].(agentcore.Message); first.Metadata["compacted_tool_result"] == true {
		t.Fatal("the original authoritative work package must be retained")
	}
	if duplicate := view[4].(agentcore.Message); duplicate.Metadata["compacted_tool_result"] != true {
		t.Fatal("the later duplicate work package must be cleared with its lookup rationale")
	}
}

func TestWriterPhaseBoundsRepeatedContinuityReadsBeforeBoundary(t *testing.T) {
	messages := []agentcore.AgentMessage{agentcore.UserMsg("write chapter 41")}
	for index, spec := range []struct {
		name string
		args string
	}{
		{name: "novel_context", args: `{"chapter":41}`},
		{name: "read_chapter", args: `{"chapter":39}`},
		{name: "read_chapter", args: `{"chapter":40}`},
	} {
		callID := fmt.Sprintf("continuity-%d", index)
		messages = append(messages,
			agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
				agentcore.ThinkingBlock(strings.Repeat("historical lookup rationale ", 200)),
				agentcore.ToolCallBlock(agentcore.ToolCall{ID: callID, Name: spec.name, Args: []byte(spec.args)}),
			}},
			agentcore.ToolResultMsg(callID, []byte(fmt.Sprintf(`"%s"`, strings.Repeat(spec.name+" evidence ", 300))), false),
		)
	}

	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatal("a second normal continuity read must advance the bounded evidence slot")
	}
	if got := countCompactedToolResults(view); got != 1 {
		t.Fatalf("compacted results=%d, want only the older continuity read", got)
	}
	if compacted := view[4].(agentcore.Message); compacted.Metadata["compacted_tool_result"] != true {
		t.Fatalf("older continuity result was retained: %+v", compacted.Metadata)
	}
	if contextResult := view[2].(agentcore.Message); contextResult.Metadata["compacted_tool_result"] == true {
		t.Fatal("active novel_context must remain available while drafting")
	}
	if newestRead := view[6].(agentcore.Message); newestRead.Metadata["compacted_tool_result"] == true {
		t.Fatal("newest continuity tail must remain available")
	}
}

func TestWriterPhaseKeepsDistinctAdaptationSourceReads(t *testing.T) {
	messages := []agentcore.AgentMessage{agentcore.UserMsg("adapt chapter")}
	for index, chapter := range []int{12, 13} {
		callID := fmt.Sprintf("source-%d", index)
		args := []byte(fmt.Sprintf(`{"source":"source","chapter":%d}`, chapter))
		messages = append(messages,
			agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{ID: callID, Name: "read_chapter", Args: args})}},
			agentcore.ToolResultMsg(callID, []byte(`"source evidence"`), false),
		)
	}

	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || countCompactedToolResults(view) != 0 {
		t.Fatalf("distinct adaptation source reads must remain intact: result=%+v", result)
	}
}

func TestWriterManagerCommitsValidationPhaseBeforeProviderProjection(t *testing.T) {
	messages := writerPhaseMessages(t, "novel_context", "read_chapter", "check_consistency")
	engine := newContextManager(contextManagerConfig{
		ContextWindow:    96_000,
		ReserveTokens:    8_000,
		KeepRecentTokens: 12_000,
		Agent:            "writer",
		CommitOnProject:  true,
	})
	manager := newWriterContextManager(engine, *writerToolResultMicrocompactConfig())
	projection, err := manager.Project(t.Context(), messages)
	if err != nil {
		t.Fatal(err)
	}
	if !projection.ShouldCommit || len(projection.CommitMessages) == 0 {
		t.Fatalf("validation boundary must commit before provider call: %+v", projection)
	}
	if countCompactedToolResults(projection.CommitMessages) != 1 {
		t.Fatalf("committed compacted results=%d, want 1", countCompactedToolResults(projection.CommitMessages))
	}
}

func TestWriterManagerUsesPhaseEvictionForOverflowRecovery(t *testing.T) {
	messages := writerPhaseMessages(t, "novel_context", "read_chapter", "check_consistency")
	engine := newContextManager(contextManagerConfig{
		ContextWindow:    96_000,
		ReserveTokens:    8_000,
		KeepRecentTokens: 12_000,
		Agent:            "writer",
		CommitOnProject:  true,
	})
	manager := newWriterContextManager(engine, *writerToolResultMicrocompactConfig())
	recovery, err := manager.RecoverOverflow(t.Context(), messages, errors.New("compiled request crossed production boundary"))
	if err != nil {
		t.Fatal(err)
	}
	if !recovery.Changed || !recovery.ShouldCommit || recovery.Strategy != "writer_validation_phase" {
		t.Fatalf("unexpected recovery: %+v", recovery)
	}
	if countCompactedToolResults(recovery.View) != 2 {
		t.Fatalf("recovered compacted results=%d, want 2", countCompactedToolResults(recovery.View))
	}
}

func TestWriterPhaseCompactsPersistedDraftArgumentsImmediately(t *testing.T) {
	callID := "draft-write"
	messages := []agentcore.AgentMessage{
		agentcore.UserMsg("polish chapter"),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ThinkingBlock(strings.Repeat("old drafting rationale ", 500)),
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID: callID, Name: "draft_chapter", Args: []byte(fmt.Sprintf(`{"chapter":39,"content":%q}`, strings.Repeat("chapter prose ", 1_200))),
			}),
		}},
		agentcore.ToolResultMsg(callID, []byte(`{"written":true,"chapter":39}`), false),
	}
	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	before := corecontext.EstimateTotal(messages)
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatal("persisted draft arguments must be evicted before the next provider call")
	}
	assistant := view[1].(agentcore.Message)
	calls := assistant.ToolCalls()
	if len(calls) != 1 || string(calls[0].Args) != `{"_context_compacted":true}` {
		t.Fatalf("draft call args were not compacted: %+v", calls)
	}
	if assistant.ThinkingContent() != "" {
		t.Fatal("completed drafting rationale must not cross the persisted-write boundary")
	}
	if after := corecontext.EstimateTotal(view); after >= before-5_000 {
		t.Fatalf("draft phase saved only %d tokens, want a whole-payload reduction", before-after)
	}
	second, secondResult, err := strategy.Apply(t.Context(), view, view, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if secondResult.Applied || corecontext.EstimateTotal(second) != corecontext.EstimateTotal(view) {
		t.Fatalf("phase compaction must be idempotent: %+v", secondResult)
	}
}

func TestWriterPhaseFinishesLegacyClearedCallArguments(t *testing.T) {
	messages := writerPhaseMessages(t, "novel_context", "read_chapter", "check_consistency")
	legacyResult := messages[2].(agentcore.Message)
	legacyResult.Metadata["compacted_tool_result"] = true
	messages[2] = legacyResult
	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatal("legacy cleared result still has an oversized originating call")
	}
	call := view[1].(agentcore.Message).ToolCalls()[0]
	if string(call.Args) != `{"_context_compacted":true}` {
		t.Fatalf("legacy call args=%s", call.Args)
	}
}

func TestWriterOverflowRecoveryEvictsWholePriorPhase(t *testing.T) {
	messages := []agentcore.AgentMessage{agentcore.UserMsg(strings.Repeat("baseline ", 2_200))}
	for index, toolName := range []string{"novel_context", "read_chapter", "check_consistency"} {
		callID := fmt.Sprintf("boundary-%d", index)
		repetitions := 900
		if toolName == "read_chapter" {
			repetitions = 400
		}
		if toolName == "check_consistency" {
			repetitions = 80
		}
		messages = append(messages,
			agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{ID: callID, Name: toolName, Args: []byte(`{"chapter":39}`)})}},
			agentcore.ToolResultMsg(callID, []byte(fmt.Sprintf(`"%s"`, strings.Repeat(toolName+" evidence ", repetitions))), false),
		)
	}

	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.ForceApply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || countCompactedToolResults(view) != 2 {
		t.Fatalf("forced phase eviction result=%+v compacted=%d", result, countCompactedToolResults(view))
	}
	compiled, err := compileAgentInput(toLLMMessages(t, view), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled) >= 40*1024 {
		t.Fatalf("post-phase request=%d bytes, want at least 20 KiB production headroom", len(compiled))
	}
}

func writerPhaseMessages(t *testing.T, tools ...string) []agentcore.AgentMessage {
	t.Helper()
	messages := []agentcore.AgentMessage{agentcore.UserMsg("polish chapter")}
	for index, toolName := range tools {
		callID := fmt.Sprintf("phase-helper-%d", index)
		messages = append(messages,
			agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{ID: callID, Name: toolName, Args: []byte(`{"chapter":39}`)})}},
			agentcore.ToolResultMsg(callID, []byte(`"evidence"`), false),
		)
	}
	return messages
}

func toLLMMessages(t *testing.T, messages []agentcore.AgentMessage) []agentcore.Message {
	t.Helper()
	converted := corecontext.NewEngine(corecontext.EngineConfig{ContextWindow: 96_000}).ConvertToLLM(messages)
	if len(converted) == 0 {
		t.Fatal("converted Writer prompt is empty")
	}
	return converted
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
