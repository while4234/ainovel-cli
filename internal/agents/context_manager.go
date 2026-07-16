package agents

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
)

const summaryMaxAttempts = 3

type SummaryRetryHook func(agent string, retry, maxRetries int, delay time.Duration)

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
	OnSummaryRetry   SummaryRetryHook
}

func newContextManager(cfg contextManagerConfig) *corecontext.ContextEngine {
	var sc corecontext.FullSummaryConfig
	if cfg.Summary != nil {
		sc = *cfg.Summary
	}
	sc.Model = summaryCompatibleModel(cfg.Model, cfg.Agent, cfg.OnSummaryRetry)
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
func summaryCompatibleModel(model agentcore.ChatModel, agent string, onRetry SummaryRetryHook) agentcore.ChatModel {
	if model == nil {
		return nil
	}
	return &summaryModel{
		ChatModel: model,
		agent:     agent,
		onRetry:   onRetry,
		delay:     retrypolicy.Delay,
		wait:      retrypolicy.Wait,
	}
}

type summaryModel struct {
	agentcore.ChatModel
	agent   string
	onRetry SummaryRetryHook
	delay   func(int) time.Duration
	wait    func(context.Context, time.Duration) error
}

func (m *summaryModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	opts = append(opts, agentcore.WithThinking(agentcore.ThinkingAuto))
	var lastResponse *agentcore.LLMResponse
	for attempt := 1; attempt <= summaryMaxAttempts; attempt++ {
		response, err := m.ChatModel.Generate(ctx, messages, tools, opts...)
		if err != nil {
			return nil, err
		}
		lastResponse = response
		if summaryResponseHasContent(response) {
			return response, nil
		}
		if attempt == summaryMaxAttempts {
			break
		}

		retry := attempt
		delay := m.delay(retry)
		if m.onRetry != nil {
			m.onRetry(m.agent, retry, summaryMaxAttempts-1, delay)
		}
		if err := m.wait(ctx, delay); err != nil {
			return nil, err
		}
	}
	if lastResponse == nil {
		return nil, fmt.Errorf("summary model returned nil response")
	}
	// Return the final empty response so agentcore preserves its established
	// "summarization returned empty content" terminal diagnostic.
	return lastResponse, nil
}

func (m *summaryModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return m.ChatModel.GenerateStream(ctx, messages, tools, append(opts, agentcore.WithThinking(agentcore.ThinkingAuto))...)
}

func summaryResponseHasContent(response *agentcore.LLMResponse) bool {
	if response == nil {
		return false
	}
	text := strings.TrimSpace(response.Message.TextContent())
	start := strings.Index(text, "<analysis>")
	end := strings.Index(text, "</analysis>")
	if start >= 0 && end >= start {
		text = strings.TrimSpace(text[:start] + text[end+len("</analysis>"):])
	}
	return text != ""
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
