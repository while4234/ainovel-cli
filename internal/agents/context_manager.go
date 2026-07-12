package agents

import (
	"context"
	"log/slog"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
)

// contextManagerConfig 聚合 ContextManager 的全部配置参数。
type contextManagerConfig struct {
	Model            agentcore.ChatModel
	ContextWindow    int
	ReserveTokens    int
	KeepRecentTokens int
	Agent            string
	CommitOnProject  bool
	Summary          *corecontext.FullSummaryConfig
	ToolMicrocompact *corecontext.ToolResultMicrocompactConfig
	ExtraStrategies  []corecontext.Strategy
}

func newContextManager(cfg contextManagerConfig) *corecontext.ContextEngine {
	var sc corecontext.FullSummaryConfig
	if cfg.Summary != nil {
		sc = *cfg.Summary
	}
	sc.Model = summaryCompatibleModel(cfg.Model)
	if sc.KeepRecentTokens <= 0 {
		sc.KeepRecentTokens = cfg.KeepRecentTokens
	}

	var tc corecontext.ToolResultMicrocompactConfig
	if cfg.ToolMicrocompact != nil {
		tc = *cfg.ToolMicrocompact
	}

	strategies := []corecontext.Strategy{
		corecontext.NewToolResultMicrocompact(tc),
		corecontext.NewLightTrim(corecontext.LightTrimConfig{}),
	}
	strategies = append(strategies, cfg.ExtraStrategies...)
	strategies = append(strategies, corecontext.NewFullSummary(sc))

	engine := corecontext.NewEngine(corecontext.EngineConfig{
		ContextWindow:   cfg.ContextWindow,
		ReserveTokens:   cfg.ReserveTokens,
		CommitOnProject: cfg.CommitOnProject,
		Strategies:      strategies,
	})

	callback := contextRewriteCallback(cfg.Agent)
	engine.SetProjectHook(callback)
	engine.SetRecoverHook(callback)
	return engine
}

// summaryCompatibleModel keeps summary calls provider-neutral. Agentcore asks
// summaries to disable reasoning, but some OpenAI-compatible DeepSeek backends
// reject an explicit thinking=off field even though they accept the same call
// when the field is omitted. Appending ThinkingAuto restores the former,
// provider-default behavior while retaining all other call options.
func summaryCompatibleModel(model agentcore.ChatModel) agentcore.ChatModel {
	if model == nil {
		return nil
	}
	return &summaryModel{ChatModel: model}
}

type summaryModel struct {
	agentcore.ChatModel
}

func (m *summaryModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return m.ChatModel.Generate(ctx, messages, tools, append(opts, agentcore.WithThinking(agentcore.ThinkingAuto))...)
}

func (m *summaryModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return m.ChatModel.GenerateStream(ctx, messages, tools, append(opts, agentcore.WithThinking(agentcore.ThinkingAuto))...)
}

// contextRewriteCallback 创建上下文重写的日志回调。
// 新架构简化为只写 slog,不再写 runtime queue 和 UIEvent。
func contextRewriteCallback(agent string) func(corecontext.RewriteEvent) {
	return func(ev corecontext.RewriteEvent) {
		attrs := []any{
			"module", "context",
			"agent", agent,
			"reason", ev.Reason,
			"strategy", ev.Strategy,
			"committed", ev.Committed,
			"tokens_before", ev.TokensBefore,
			"tokens_after", ev.TokensAfter,
		}
		if info := ev.Info; info != nil {
			attrs = append(attrs,
				"msgs_before", info.MessagesBefore,
				"msgs_after", info.MessagesAfter,
				"compacted", info.CompactedCount,
				"kept", info.KeptCount,
				"duration_ms", info.Duration.Milliseconds(),
			)
		}
		slog.Warn("上下文重写", attrs...)
	}
}
