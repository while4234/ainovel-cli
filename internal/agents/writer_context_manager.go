package agents

import (
	"context"
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
	delegate *corecontext.ToolResultMicrocompactStrategy
}

func newWriterValidationPhaseStrategy(cfg corecontext.ToolResultMicrocompactConfig) *writerValidationPhaseStrategy {
	return &writerValidationPhaseStrategy{delegate: corecontext.NewToolResultMicrocompact(cfg)}
}

func (s *writerValidationPhaseStrategy) Name() string { return "writer_validation_phase" }

func (s *writerValidationPhaseStrategy) Apply(ctx context.Context, transcript, view []agentcore.AgentMessage, budget corecontext.Budget) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	if !hasWriterValidationReceipt(view) {
		return view, corecontext.StrategyResult{Name: s.Name()}, nil
	}
	return s.compact(ctx, transcript, view, budget)
}

func (s *writerValidationPhaseStrategy) ForceApply(ctx context.Context, transcript, view []agentcore.AgentMessage, budget corecontext.Budget) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	return s.compact(ctx, transcript, view, budget)
}

func (s *writerValidationPhaseStrategy) compact(ctx context.Context, transcript, view []agentcore.AgentMessage, budget corecontext.Budget) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	next, result, err := s.delegate.Apply(ctx, transcript, view, budget)
	result.Name = s.Name()
	return next, result, err
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
