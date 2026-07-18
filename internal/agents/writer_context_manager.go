package agents

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
)

// writerContextManager treats validation receipts as durable phase boundaries.
// Once a validation tool has inspected the chapter, source payloads from the
// preceding phase no longer belong in every later provider request. The
// chapter itself and its contract remain durable in the Store and can be read
// again if a receipt asks for a repair.
type writerContextManager struct {
	*corecontext.ContextEngine
	phase *writerValidationPhaseStrategy
}

func newWriterContextManager(engine *corecontext.ContextEngine, cfg corecontext.ToolResultMicrocompactConfig) *writerContextManager {
	return &writerContextManager{
		ContextEngine: engine,
		phase:         newWriterValidationPhaseStrategy(cfg),
	}
}

func (m *writerContextManager) Project(ctx context.Context, msgs []agentcore.AgentMessage) (agentcore.ContextProjection, error) {
	view, result, err := m.phase.Apply(ctx, msgs, msgs, corecontext.Budget{})
	if err != nil {
		return agentcore.ContextProjection{}, err
	}
	if !result.Applied {
		return m.ContextEngine.Project(ctx, msgs)
	}

	projection, err := m.ContextEngine.Project(ctx, view)
	if err != nil {
		return agentcore.ContextProjection{}, err
	}
	if projection.Messages == nil {
		projection.Messages = view
	}
	projection.ShouldCommit = true
	projection.CommitMessages = projection.Messages
	logWriterPhaseRewrite("validation_boundary", msgs, projection.Messages)
	return projection, nil
}

func (m *writerContextManager) RecoverOverflow(ctx context.Context, msgs []agentcore.AgentMessage, cause error) (agentcore.ContextRecoveryResult, error) {
	view, result, err := m.phase.ForceApply(ctx, msgs, msgs, corecontext.Budget{})
	if err != nil {
		return agentcore.ContextRecoveryResult{}, err
	}
	if !result.Applied {
		return m.ContextEngine.RecoverOverflow(ctx, msgs, cause)
	}

	m.ContextEngine.Sync(view)
	logWriterPhaseRewrite("production_boundary", msgs, view)
	return agentcore.ContextRecoveryResult{
		View:           view,
		CommitMessages: view,
		Usage:          m.ContextEngine.Usage(),
		Changed:        true,
		ShouldCommit:   true,
		Strategy:       result.Name,
		CompactedCount: countCompactedToolResults(view),
		KeptCount:      2,
	}, nil
}

type writerValidationPhaseStrategy struct {
	keepRecent     int
	clearedMessage string
}

func newWriterValidationPhaseStrategy(cfg corecontext.ToolResultMicrocompactConfig) *writerValidationPhaseStrategy {
	if cfg.KeepRecent <= 0 {
		cfg.KeepRecent = 2
	}
	if cfg.ClearedMessage == "" {
		cfg.ClearedMessage = "[Prior Writer phase cleared; durable project data remains available through tools.]"
	}
	return &writerValidationPhaseStrategy{keepRecent: cfg.KeepRecent, clearedMessage: cfg.ClearedMessage}
}

func (s *writerValidationPhaseStrategy) Name() string { return "writer_validation_phase" }

func (s *writerValidationPhaseStrategy) Apply(ctx context.Context, transcript, view []agentcore.AgentMessage, budget corecontext.Budget) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	if !hasWriterValidationReceipt(view) && !hasPersistedWriterDraftReceipt(view) {
		return view, corecontext.StrategyResult{Name: s.Name()}, nil
	}
	return s.compact(ctx, transcript, view, budget)
}

func (s *writerValidationPhaseStrategy) ForceApply(ctx context.Context, transcript, view []agentcore.AgentMessage, budget corecontext.Budget) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	return s.compact(ctx, transcript, view, budget)
}

func (s *writerValidationPhaseStrategy) compact(ctx context.Context, transcript, view []agentcore.AgentMessage, budget corecontext.Budget) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	_ = ctx
	_ = transcript
	_ = budget
	return compactWriterPhase(view, s.keepRecent, s.clearedMessage)
}

func hasWriterValidationReceipt(msgs []agentcore.AgentMessage) bool {
	return hasToolResult(msgs, func(name string) bool {
		switch name {
		case "check_consistency", "check_adaptation", "check_de_ai":
			return true
		default:
			return false
		}
	})
}

func hasPersistedWriterDraftReceipt(msgs []agentcore.AgentMessage) bool {
	return hasToolResult(msgs, isPersistedWriterDraftTool)
}

func isPersistedWriterDraftTool(name string) bool {
	switch name {
	case "draft_chapter", "edit_chapter", "repair_de_ai_batch":
		return true
	default:
		return false
	}
}

type writerToolResultCandidate struct {
	resultIndex    int
	assistantIndex int
	callID         string
	toolName       string
	key            string
	alreadyCleared bool
}

func compactWriterPhase(view []agentcore.AgentMessage, keepRecent int, clearedMessage string) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	out := cloneWriterMessages(view)
	candidates := collectWriterToolResults(out)
	if len(candidates) == 0 {
		return view, corecontext.StrategyResult{Name: "writer_validation_phase"}, nil
	}

	protected := protectRecentWriterResults(candidates, keepRecent)
	compactCalls := make(map[string]struct{})
	applied := false
	for _, candidate := range candidates {
		if candidate.alreadyCleared {
			compactCalls[candidate.callID] = struct{}{}
			continue
		}
		if isPersistedWriterDraftTool(candidate.toolName) {
			compactCalls[candidate.callID] = struct{}{}
		}
		if _, keep := protected[candidate.resultIndex]; keep {
			continue
		}
		message := out[candidate.resultIndex].(agentcore.Message)
		message.Content = []agentcore.ContentBlock{agentcore.TextBlock(clearedMessage)}
		message.Metadata = cloneWriterMetadata(message.Metadata)
		message.Metadata["compacted_tool_result"] = true
		message.Metadata["compacted_tool_name"] = candidate.toolName
		out[candidate.resultIndex] = message
		compactCalls[candidate.callID] = struct{}{}
		applied = true
	}

	argsChanged := compactWriterToolCalls(out, compactCalls)
	applied = applied || argsChanged
	if !applied {
		return view, corecontext.StrategyResult{Name: "writer_validation_phase"}, nil
	}
	return out, corecontext.StrategyResult{
		Applied:     true,
		TokensSaved: max(0, corecontext.EstimateTotal(view)-corecontext.EstimateTotal(out)),
		Name:        "writer_validation_phase",
	}, nil
}

func collectWriterToolResults(msgs []agentcore.AgentMessage) []writerToolResultCandidate {
	type pendingCall struct {
		assistantIndex int
		toolName       string
		key            string
	}
	pending := make(map[string]pendingCall)
	var candidates []writerToolResultCandidate
	for index, item := range msgs {
		message, ok := item.(agentcore.Message)
		if !ok {
			continue
		}
		if message.Role == agentcore.RoleAssistant {
			for _, call := range message.ToolCalls() {
				pending[call.ID] = pendingCall{assistantIndex: index, toolName: call.Name, key: call.Name + "\x00" + string(call.Args)}
			}
			continue
		}
		if message.Role != agentcore.RoleTool {
			continue
		}
		callID, _ := message.Metadata["tool_call_id"].(string)
		call := pending[callID]
		if call.toolName == "" {
			continue
		}
		candidates = append(candidates, writerToolResultCandidate{
			resultIndex:    index,
			assistantIndex: call.assistantIndex,
			callID:         callID,
			toolName:       call.toolName,
			key:            call.key,
			alreadyCleared: message.Metadata["compacted_tool_result"] == true,
		})
	}
	return candidates
}

func protectRecentWriterResults(candidates []writerToolResultCandidate, keepRecent int) map[int]struct{} {
	protected := make(map[int]struct{}, keepRecent)
	seen := make(map[string]struct{}, keepRecent)
	for index := len(candidates) - 1; index >= 0 && len(protected) < keepRecent; index-- {
		candidate := candidates[index]
		if candidate.alreadyCleared {
			continue
		}
		if _, duplicate := seen[candidate.key]; duplicate {
			continue
		}
		seen[candidate.key] = struct{}{}
		protected[candidate.resultIndex] = struct{}{}
	}
	return protected
}

func compactWriterToolCalls(msgs []agentcore.AgentMessage, callIDs map[string]struct{}) bool {
	changed := false
	for index, item := range msgs {
		message, ok := item.(agentcore.Message)
		if !ok || message.Role != agentcore.RoleAssistant {
			continue
		}
		content := append([]agentcore.ContentBlock(nil), message.Content...)
		messageChanged := false
		allCallsCompacted := true
		hasCall := false
		for blockIndex, block := range content {
			if block.Type != agentcore.ContentToolCall || block.ToolCall == nil {
				continue
			}
			hasCall = true
			if _, compact := callIDs[block.ToolCall.ID]; !compact {
				allCallsCompacted = false
				continue
			}
			if string(block.ToolCall.Args) == `{"_context_compacted":true}` {
				continue
			}
			call := *block.ToolCall
			call.Args = json.RawMessage(`{"_context_compacted":true}`)
			call.ArgsInvalid = false
			call.ArgsRawText = ""
			call.ArgsParseError = ""
			content[blockIndex] = agentcore.ToolCallBlock(call)
			messageChanged = true
		}
		if hasCall && allCallsCompacted {
			for blockIndex, block := range content {
				switch block.Type {
				case agentcore.ContentText:
					if block.Text != "" {
						content[blockIndex] = agentcore.TextBlock("")
						messageChanged = true
					}
				case agentcore.ContentThinking:
					if block.Thinking != "" {
						content[blockIndex] = agentcore.ThinkingBlock("")
						messageChanged = true
					}
				}
			}
		}
		if messageChanged {
			message.Content = content
			message.Metadata = cloneWriterMetadata(message.Metadata)
			message.Metadata["compacted_tool_args"] = true
			msgs[index] = message
			changed = true
		}
	}
	return changed
}

func cloneWriterMessages(msgs []agentcore.AgentMessage) []agentcore.AgentMessage {
	out := append([]agentcore.AgentMessage(nil), msgs...)
	for index, item := range out {
		message, ok := item.(agentcore.Message)
		if !ok {
			continue
		}
		message.Content = append([]agentcore.ContentBlock(nil), message.Content...)
		message.Metadata = cloneWriterMetadata(message.Metadata)
		out[index] = message
	}
	return out
}

func cloneWriterMetadata(metadata map[string]any) map[string]any {
	clone := make(map[string]any, len(metadata)+2)
	for key, value := range metadata {
		clone[key] = value
	}
	return clone
}

func hasToolResult(msgs []agentcore.AgentMessage, matches func(string) bool) bool {
	pending := make(map[string]string)
	for _, item := range msgs {
		message, ok := item.(agentcore.Message)
		if !ok {
			continue
		}
		if message.Role == agentcore.RoleAssistant {
			for _, call := range message.ToolCalls() {
				pending[call.ID] = call.Name
			}
			continue
		}
		if message.Role != agentcore.RoleTool {
			continue
		}
		callID, _ := message.Metadata["tool_call_id"].(string)
		if matches(pending[callID]) {
			return true
		}
	}
	return false
}

func countCompactedToolResults(msgs []agentcore.AgentMessage) int {
	count := 0
	for _, item := range msgs {
		message, ok := item.(agentcore.Message)
		if ok && message.Metadata["compacted_tool_result"] == true {
			count++
		}
	}
	return count
}

func logWriterPhaseRewrite(reason string, before, after []agentcore.AgentMessage) {
	slog.Warn("Writer validation phase advanced",
		"module", "context",
		"agent", "writer",
		"reason", reason,
		"strategy", "writer_validation_phase",
		"tokens_before", corecontext.EstimateTotal(before),
		"tokens_after", corecontext.EstimateTotal(after),
		"compacted", countCompactedToolResults(after),
	)
}
