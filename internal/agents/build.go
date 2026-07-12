package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/agents/ctxpack"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/globalprompt"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/host/reminder"
	"github.com/voocel/ainovel-cli/internal/modelprofile"
	"github.com/voocel/ainovel-cli/internal/promptcompile"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
	"github.com/voocel/ainovel-cli/internal/userrules"
)

// agentToRole 把 subagent name 归一为 ModelSet 认得的 role 名。
// architect_short / architect_long 都共用同一个 architect role 配置。
// 跟 host.agentRoleName 同义，因为 build 与 host 互不依赖故各持一份。
func agentToRole(name string) string {
	if strings.HasPrefix(name, "architect_") {
		return "architect"
	}
	return name
}

// subagentMaxRetries 给所有 SubAgentConfig 与 Coordinator 统一的 LLM retry 上限。
// 退避策略：指数退避（受 maxDelay 上限约束），优先服从 server Retry-After。
// 配合 ToolsAreIdempotent=true 让 stream-idle / 503 / 短暂网络抖动这类 retryable
// 错误能在 subagent 层就近重试，而不是把整个 subagent 抛回 coordinator 重派发。
// 项目铁律一保证写类工具走 checkpoint+digest 幂等，重试是安全的。
const subagentMaxRetries = retrypolicy.MaxAttempts

func boundedAgentContextWindow(modelName string, modelWindow int, agent promptcompile.Agent) (int, int) {
	if modelWindow <= 0 {
		return modelWindow, bootstrap.CompactReserveTokens(modelWindow)
	}
	window := modelWindow
	profile := modelprofile.Resolve(modelName)
	if roleWindow := profile.ContextWindow(modelProfileRole(agent)); roleWindow > 0 {
		window = min(window, roleWindow)
	}
	reserve := bootstrap.CompactReserveTokens(window)
	slog.Info("角色运行时上下文窗口",
		"module", "context",
		"role", agent,
		"profile", profile.Name,
		"model", modelName,
		"model_window", modelWindow,
		"runtime_window", window,
		"reserve", reserve,
	)
	return window, reserve
}

func modelProfileRole(agent promptcompile.Agent) modelprofile.Role {
	switch agent {
	case promptcompile.AgentCoordinator:
		return modelprofile.RoleCoordinator
	case promptcompile.AgentArchitect:
		return modelprofile.RoleArchitect
	case promptcompile.AgentWriter:
		return modelprofile.RoleWriter
	case promptcompile.AgentEditor:
		return modelprofile.RoleEditor
	default:
		return ""
	}
}

// UsageRecorder 是 BuildCoordinator 可选的用量回调；签名与 OnMessage 一致，
// 每条 agent 消息都会调一次，由 Host 层负责聚合。nil 表示不追踪。
type UsageRecorder func(agentName string, msg agentcore.AgentMessage)

// FlowBoundaryHook runs synchronously after a Coordinator tool that advances
// the durable story state succeeds. Host uses it to queue the next flow
// instruction before the Coordinator gets another LLM turn.
type FlowBoundaryHook func(toolName string)

// ApplyThinking 把某具体角色的推理强度应用到 live agent（运行时 /model 调整用）。
// coordinator → Agent.SetThinkingLevel；architect → 两个 architect_* 子代理；
// writer/editor → 对应子代理。空 level = 沿用模型/provider 默认。其它 role 名忽略。
type ApplyThinking func(role string, level agentcore.ThinkingLevel)

// ParseThinkingLevel 把配置字符串转 agentcore.ThinkingLevel。
// "" 合法（= 不覆盖/继承）；其余须是 off/low/medium/high/xhigh/max 之一，
// 否则返回 error（启动时降级当空并 warn，运行时把 error 回显给用户）。
func ParseThinkingLevel(s string) (agentcore.ThinkingLevel, error) {
	lv := agentcore.NormalizeThinkingLevel(agentcore.ThinkingLevel(s))
	switch lv {
	case "", agentcore.ThinkingOff, agentcore.ThinkingLow, agentcore.ThinkingMedium,
		agentcore.ThinkingHigh, agentcore.ThinkingXHigh, agentcore.ThinkingMax:
		return lv, nil
	default:
		return "", fmt.Errorf("无效推理强度 %q（可选：off/low/medium/high/xhigh/max）", s)
	}
}

func ResolveThinkingForModel(model agentcore.ChatModel, level agentcore.ThinkingLevel) (agentcore.ThinkingLevel, bool) {
	return llm.ThinkingPolicyFor(model).Resolve(level)
}

func AvailableThinkingForModel(model agentcore.ChatModel) []agentcore.ThinkingLevel {
	return llm.ThinkingPolicyFor(model).Available
}

// roleThinking 解析某角色生效的推理强度；非法值降级为空（不覆盖）并 warn。
func roleThinking(cfg bootstrap.Config, role string) agentcore.ThinkingLevel {
	lv, err := ParseThinkingLevel(cfg.ResolveReasoningEffort(role))
	if err != nil {
		slog.Warn("忽略无效推理强度配置", "module", "agent", "role", role, "err", err)
		return ""
	}
	return lv
}

func resolvedRoleThinking(model agentcore.ChatModel, cfg bootstrap.Config, role string) agentcore.ThinkingLevel {
	resolved, _ := ResolveThinkingForModel(model, roleThinking(cfg, role))
	return resolved
}

// BuildCoordinator 组装 Coordinator Agent 及其 SubAgent。
// 返回 Agent、AskUserTool、WriterRestorePack、Coordinator 的 ContextEngine 引用，
// 以及 ApplyThinking 闭包——Host 层 /model 切换时需要直接调 SetContextWindow +
// SetReserveTokens 联动新模型的窗口（writer/architect/editor 走 ContextManagerFactory
// 自动重建，不需要 ref；只有常驻的 coordinator 需要），并通过 ApplyThinking 联动各角色
// 推理强度。Host 层通过 Agent.Subscribe 获取事件流,不再需要 emit 回调。
func BuildCoordinator(
	cfg bootstrap.Config,
	store *store.Store,
	models *bootstrap.ModelSet,
	bundle assets.Bundle,
	recordUsage UsageRecorder,
	onFlowBoundary FlowBoundaryHook,
) (*agentcore.Agent, *tools.AskUserTool, *ctxpack.WriterRestorePack, *corecontext.ContextEngine, ApplyThinking) {
	// 共享工具
	contextTool := tools.NewContextToolWithOptions(store, bundle.References, cfg.Style, tools.ContextToolOptions{
		SimulationMode: cfg.EffectiveSimulationMode(),
	})
	// 用户规则服务：归一化各来源 → 确定性合并 → 落盘本书快照。Coordinator 的
	// save_user_rules 工具复用它做运行中更新；归一化用 Default 模型（与 Host 开书侧一致）。
	userRulesSvc := userrules.NewService(store, models.Default, rules.DefaultOptions())
	readChapter := tools.NewReadChapterTool(store)
	askUser := tools.NewAskUserTool()
	completionGate := adapt.NewCompletionGate(store)

	architectTools := []agentcore.Tool{
		contextTool,
		tools.NewSaveFoundationTool(store, completionGate),
	}
	writerTools := []agentcore.Tool{
		contextTool,
		readChapter,
		tools.NewPlanChapterTool(store),
		newWriterDraftChapterTool(store),
		tools.NewEditChapterTool(store),
		tools.NewCheckConsistencyTool(store),
		tools.NewCheckAdaptationTool(store),
		tools.NewCommitChapterTool(store, completionGate),
	}
	editorTools := []agentcore.Tool{
		contextTool,
		readChapter,
		tools.NewSaveReviewTool(store),
		tools.NewSaveArcSummaryTool(store),
		tools.NewSaveVolumeSummaryTool(store),
	}

	// Provider failover 只记日志,不通知宿主
	reportFailover := func(ev bootstrap.FailoverEvent) {
		slog.Warn("provider 切换",
			"module", "agent",
			"role", ev.Role,
			"reason", ev.Reason,
			"from", fmt.Sprintf("%s/%s", ev.FromProvider, ev.FromModel),
			"to", fmt.Sprintf("%s/%s", ev.ToProvider, ev.ToModel),
			"err", ev.Err,
		)
	}

	// Architect 的一次自主 run 可能连续完成设定、骨架和首批细纲，不能在
	// run 中途换模型；以骨架规划阶段为主路由，未配置时继承 architect。
	architectModel := models.ForStageWithFailover(bootstrap.StageSkeleton, reportFailover)
	writerModel := models.ForStageWithFailover(bootstrap.StageWriting, reportFailover)
	editorModel := models.ForStageWithFailover(bootstrap.StageReview, reportFailover)
	coordinatorModel := models.ForRoleWithFailover("coordinator", reportFailover)
	architectModel = NewToolCallRepairModel(architectModel)
	writerModel = NewToolCallRepairModel(writerModel)
	editorModel = NewToolCallRepairModel(editorModel)
	coordinatorModel = NewToolCallRepairModel(coordinatorModel)

	// Coordinator 的 ContextManager 在 Agent 构造时一次性生成，按启动模型解析。
	// 运行中 /model 切换到更小窗口的模型时，建议用户显式配置 context_window 兜底。
	_, coordinatorModelName, _ := models.CurrentSelection("coordinator")
	coordinatorContextWindow, coordinatorSource := cfg.ResolveContextWindow(coordinatorModelName)
	// Writer 的 ContextManager 由工厂每次调用重建，窗口随模型 swap 动态跟随（见下方工厂）。
	_, writerModelName, _ := models.CurrentStageSelection(bootstrap.StageWriting)
	writerContextWindow, writerSource := cfg.ResolveContextWindow(writerModelName)
	bootstrap.LogContextWindowChoice("coordinator", coordinatorModelName, coordinatorContextWindow, coordinatorSource)
	bootstrap.LogContextWindowChoice("writer", writerModelName, writerContextWindow, writerSource)

	// modelLookup 写入 session 时给每条 assistant 消息附 _meta:{provider,model}，
	// 让 replay 不再依赖"当前 ModelSet"来反推历史 cost，运行中切换模型也能精确算。
	modelLookup := func(agentName string) (string, string) {
		role := agentToRole(agentName)
		if role == "architect" {
			provider, name, _ := models.CurrentStageSelection(bootstrap.StageSkeleton)
			return provider, name
		}
		if role == "writer" {
			provider, name, _ := models.CurrentStageSelection(bootstrap.StageWriting)
			return provider, name
		}
		if role == "editor" {
			provider, name, _ := models.CurrentStageSelection(bootstrap.StageReview)
			return provider, name
		}
		provider, name, _ := models.CurrentSelection(role)
		return provider, name
	}
	baseOnMsg := store.Sessions.SubAgentLogger(modelLookup)
	onMsg := func(agentName, task string, msg agentcore.AgentMessage) {
		baseOnMsg(agentName, task, msg)
		if recordUsage != nil {
			recordUsage(agentName, msg)
		}
	}
	baseCoordinatorLog := store.Sessions.CoordinatorLogger(modelLookup)
	coordinatorOnMessage := func(msg agentcore.AgentMessage) {
		baseCoordinatorLog(msg)
		if recordUsage != nil {
			recordUsage("coordinator", msg)
		}
	}

	architectStopGuardFactory := func(_, _ string) agentcore.StopGuard {
		return reminder.NewArchitectStopGuard(store)
	}
	architectThinking, _ := ResolveThinkingForModel(architectModel, roleThinking(cfg, "architect"))
	architectShort := subagent.Config{
		Name:               "architect_short",
		Description:        "短篇规划师：为单卷、单冲突、高密度故事生成紧凑设定与扁平大纲",
		Model:              architectModel,
		SystemPrompt:       globalprompt.Apply(bundle.Prompts.ArchitectShort),
		Tools:              architectTools,
		MaxTurns:           15,
		MaxRetries:         subagentMaxRetries,
		ThinkingLevel:      architectThinking,
		ToolsAreIdempotent: true,
		OnMessage:          onMsg,
		StopAfterToolResult: func(toolName string, result json.RawMessage) bool {
			r := decodeSaveFoundationResult(toolName, result)
			return r.Type == "outline" && r.FoundationReady
		},
		StopGuardFactory: architectStopGuardFactory,
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			modelWindow, _ := cfg.ResolveContextWindow(bootstrap.ModelName(model))
			window, reserve := boundedAgentContextWindow(bootstrap.ModelName(model), modelWindow, promptcompile.AgentArchitect)
			return newContextManager(contextManagerConfig{Model: model, ContextWindow: window, ReserveTokens: reserve, KeepRecentTokens: 14_000, Agent: "architect_short"})
		},
	}
	architectLong := subagent.Config{
		Name:                "architect_long",
		Description:         "长篇规划师：为连载型、可持续升级的故事生成分层设定与卷弧大纲",
		Model:               architectModel,
		SystemPrompt:        globalprompt.Apply(bundle.Prompts.ArchitectLong),
		Tools:               architectTools,
		MaxTurns:            20,
		MaxRetries:          subagentMaxRetries,
		ThinkingLevel:       architectThinking,
		ToolsAreIdempotent:  true,
		OnMessage:           onMsg,
		StopAfterToolResult: architectLongShouldStopAfterToolResult,
		StopGuardFactory:    architectStopGuardFactory,
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			modelWindow, _ := cfg.ResolveContextWindow(bootstrap.ModelName(model))
			window, reserve := boundedAgentContextWindow(bootstrap.ModelName(model), modelWindow, promptcompile.AgentArchitect)
			return newContextManager(contextManagerConfig{Model: model, ContextWindow: window, ReserveTokens: reserve, KeepRecentTokens: 14_000, Agent: "architect_long"})
		},
	}

	writerPrompt := bundle.Prompts.Writer
	if style, ok := bundle.Styles[cfg.Style]; ok {
		writerPrompt += "\n\n" + style
	}

	restore := &ctxpack.WriterRestorePack{}
	restore.Refresh(store)

	writer := subagent.Config{
		Name:               "writer",
		Description:        "创作者：自主完成一章的构思、写作、自审和提交",
		Model:              writerModel,
		SystemPrompt:       globalprompt.Apply(writerPrompt),
		Tools:              writerTools,
		MaxTurns:           30,
		MaxRetries:         subagentMaxRetries,
		ThinkingLevel:      resolvedRoleThinking(writerModel, cfg, "writer"),
		ToolsAreIdempotent: true,
		StopAfterTools:     []string{"commit_chapter"},
		OnMessage:          onMsg,
		StopGuardFactory: func(_, _ string) agentcore.StopGuard {
			return reminder.NewWriterStopGuard(store)
		},
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			// 每次 subagent(writer) 调用都会重建，从当前 runModel 读取最新模型名。
			// /model 切换 writer 后下一章自动用新窗口。
			modelWindow, _ := cfg.ResolveContextWindow(bootstrap.ModelName(model))
			window, reserve := boundedAgentContextWindow(bootstrap.ModelName(model), modelWindow, promptcompile.AgentWriter)
			return newContextManager(contextManagerConfig{
				Model:            model,
				ContextWindow:    window,
				ReserveTokens:    reserve,
				KeepRecentTokens: 12_000,
				Agent:            "writer",
				ToolMicrocompact: &corecontext.ToolResultMicrocompactConfig{
					IdleThreshold: 5 * time.Minute,
				},
				ExtraStrategies: []corecontext.Strategy{
					ctxpack.NewStoreSummaryCompact(ctxpack.StoreSummaryCompactConfig{
						Store:            store,
						KeepRecentTokens: 12_000,
					}),
				},
				Summary: &corecontext.FullSummaryConfig{
					PostSummaryHooks:    []corecontext.PostSummaryHook{restore.Hook()},
					SystemPrompt:        globalprompt.Apply(ctxpack.WriterSummarySystemPrompt),
					SummaryPrompt:       ctxpack.WriterSummaryPrompt,
					UpdateSummaryPrompt: ctxpack.WriterUpdateSummaryPrompt,
					TurnPrefixPrompt:    ctxpack.WriterTurnPrefixPrompt,
				},
			})
		},
	}

	editor := subagent.Config{
		Name:               "editor",
		Description:        "审阅者：阅读原文，从结构和审美两个层面发现问题",
		Model:              editorModel,
		SystemPrompt:       globalprompt.Apply(bundle.Prompts.Editor),
		Tools:              editorTools,
		MaxTurns:           20,
		MaxRetries:         subagentMaxRetries,
		ThinkingLevel:      resolvedRoleThinking(editorModel, cfg, "editor"),
		ToolsAreIdempotent: true,
		OnMessage:          onMsg,
		// 仅摘要类终态产物命中即停；save_review 不再硬停——StopAfterTool 退出会绕过
		// StopGuard（agentcore loop.go），若 save_review 硬停，"被派生成弧摘要却先复核"
		// 的 editor 会在 save_review 处被砍断、够不到 save_arc_summary。评审/摘要任务的
		// 收尾改由任务感知的 NewEditorStopGuard 把关。
		StopAfterToolResult: func(toolName string, _ json.RawMessage) bool {
			return toolName == "save_arc_summary" || toolName == "save_volume_summary"
		},
		StopGuardFactory: func(_, task string) agentcore.StopGuard {
			return reminder.NewEditorStopGuard(store, task)
		},
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			modelWindow, _ := cfg.ResolveContextWindow(bootstrap.ModelName(model))
			window, reserve := boundedAgentContextWindow(bootstrap.ModelName(model), modelWindow, promptcompile.AgentEditor)
			return newContextManager(contextManagerConfig{Model: model, ContextWindow: window, ReserveTokens: reserve, KeepRecentTokens: 18_000, Agent: "editor"})
		},
	}

	subagentTool := subagent.New(architectShort, architectLong, writer, editor)

	coordinatorContextWindow, coordinatorReserve := boundedAgentContextWindow(coordinatorModelName, coordinatorContextWindow, promptcompile.AgentCoordinator)
	coordinatorEngine := newContextManager(contextManagerConfig{
		Model:            coordinatorModel,
		ContextWindow:    coordinatorContextWindow,
		ReserveTokens:    coordinatorReserve,
		KeepRecentTokens: 8_000,
		Agent:            "coordinator",
		CommitOnProject:  true,
	})

	agent := agentcore.NewAgent(
		agentcore.WithModel(coordinatorModel),
		agentcore.WithSystemPrompt(globalprompt.Apply(bundle.Prompts.Coordinator)),
		agentcore.WithTools(subagentTool, contextTool, tools.NewSaveUserRulesTool(userRulesSvc), tools.NewReopenBookTool(store)),
		agentcore.WithMaxTurns(100_000),
		agentcore.WithOnMessage(coordinatorOnMessage),
		agentcore.WithToolsAreIdempotent(true),
		// subagent 是流程主通道；真实错误应显式返回给 Host，而不是在单次 run 内永久禁用工具。
		agentcore.WithMaxToolErrors(0),
		agentcore.WithMaxRetries(subagentMaxRetries),
		agentcore.WithContextManager(coordinatorEngine),
		agentcore.WithStopGuard(reminder.NewStopGuard(store, nil)),
		agentcore.WithMiddlewares(flowBoundaryMiddleware(onFlowBoundary)),
		// phase=complete 时硬拦截 subagent 派发，防止 Writer 死循环。
		agentcore.WithToolGate(combineToolGates(
			completePhaseGate(store),
			writerExpandedChapterGate(store),
		)),
	)
	// Coordinator 推理强度：无条件应用解析结果。未配置时为空（不发 thinking，用 provider
	// 默认），与各子代理（Config.ThinkingLevel 默认空）一致——避免覆盖 agentcore 默认
	// ThinkingLow 而对所有 provider 强制发 low（含会被强制开思考的 GLM/Ollama）。
	coordinatorThinking, _ := ResolveThinkingForModel(models.ForRole("coordinator"), roleThinking(cfg, "coordinator"))
	agent.SetThinkingLevel(coordinatorThinking)

	// 运行时联动各角色推理强度：coordinator 走 Agent，子代理走 subagentTool override。
	applyThinking := func(role string, level agentcore.ThinkingLevel) {
		switch role {
		case "coordinator":
			level, _ = ResolveThinkingForModel(models.ForRole("coordinator"), level)
			agent.SetThinkingLevel(level)
		case "architect":
			level, _ = ResolveThinkingForModel(models.ForRole("architect"), level)
			subagentTool.SetThinkingLevel("architect_short", level)
			subagentTool.SetThinkingLevel("architect_long", level)
		case "writer", "editor":
			level, _ = ResolveThinkingForModel(models.ForRole(role), level)
			subagentTool.SetThinkingLevel(role, level)
		}
	}

	return agent, askUser, restore, coordinatorEngine, applyThinking
}

func flowBoundaryMiddleware(onBoundary FlowBoundaryHook) agentcore.ToolMiddleware {
	return func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolExecuteFunc) (json.RawMessage, error) {
		out, err := next(ctx, call.Args)
		if err == nil && onBoundary != nil && isFlowBoundaryTool(call.Name) {
			onBoundary(call.Name)
		}
		return out, err
	}
}

func isFlowBoundaryTool(name string) bool {
	return name == "subagent" || name == "reopen_book"
}

// completePhaseGate 返回一个 ToolGate：phase=complete 时拒绝所有 subagent 派发。
// 防止 Coordinator LLM 在书完成后仍调用 Writer/Architect 导致死循环。
func completePhaseGate(st *store.Store) agentcore.ToolGate {
	return func(_ context.Context, req agentcore.GateRequest) (*agentcore.GateDecision, error) {
		if req.Call.Name != "subagent" {
			return nil, nil
		}
		// fail-open：Load 出错或 progress 为空时一律放行，不因瞬时读错误卡死正常派发。
		// 唯一代价是 complete 期恰逢读失败时死锁可能复现（概率极低，可接受）。
		progress, _ := st.Progress.Load()
		if progress != nil && progress.Phase == domain.PhaseComplete {
			return &agentcore.GateDecision{
				Allowed: false,
				Reason:  "全书已完成（phase=complete），不能直接派子代理。若用户要返工已写章节，请先调用 reopen_book(chapters=[...]) 把书重新打开进入返工态（之后会自动派 writer 重写）；若用户要新增剧情，告知需新建项目。",
			}, nil
		}
		return nil, nil
	}
}

func combineToolGates(gates ...agentcore.ToolGate) agentcore.ToolGate {
	return func(ctx context.Context, req agentcore.GateRequest) (*agentcore.GateDecision, error) {
		for _, gate := range gates {
			if gate == nil {
				continue
			}
			decision, err := gate(ctx, req)
			if err != nil {
				return nil, err
			}
			if decision != nil && !decision.Allowed {
				return decision, nil
			}
		}
		return nil, nil
	}
}

func writerExpandedChapterGate(st *store.Store) agentcore.ToolGate {
	return func(_ context.Context, req agentcore.GateRequest) (*agentcore.GateDecision, error) {
		if req.Call.Name != "subagent" {
			return nil, nil
		}
		var args struct {
			Agent string `json:"agent"`
			Task  string `json:"task"`
		}
		if err := json.Unmarshal(req.Call.Args, &args); err != nil || args.Agent != "writer" {
			return nil, nil
		}
		chapter := chapterFromTask(args.Task)
		if chapter <= 0 {
			chapter = writerFallbackChapter(st)
		}
		if chapter <= 0 {
			return nil, nil
		}
		if err := tools.EnsureAdaptationChapterPlanned(st, chapter); err != nil {
			return &agentcore.GateDecision{
				Allowed: false,
				Reason:  err.Error() + "。请重新生成并确认改编规模后再派 writer。",
			}, nil
		}
		if err := tools.EnsureChapterExpanded(st, chapter); err != nil {
			return &agentcore.GateDecision{
				Allowed: false,
				Reason:  err.Error() + "。请改派 architect_long，调用 save_foundation(type=expand_arc) 展开下一弧，或 type=append_volume 追加并展开下一卷后再派 writer。",
			}, nil
		}
		return nil, nil
	}
}

func writerFallbackChapter(st *store.Store) int {
	if st == nil {
		return 0
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		return 0
	}
	if len(progress.PendingRewrites) > 0 {
		return progress.PendingRewrites[0]
	}
	return progress.NextChapter()
}

var chapterTaskRe = regexp.MustCompile(`第\s*(\d+)\s*章`)

func chapterFromTask(task string) int {
	m := chapterTaskRe.FindStringSubmatch(task)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

type saveFoundationResult struct {
	Type            string `json:"type"`
	FoundationReady bool   `json:"foundation_ready"`
}

func decodeSaveFoundationResult(toolName string, result json.RawMessage) saveFoundationResult {
	if toolName != "save_foundation" {
		return saveFoundationResult{}
	}
	var r saveFoundationResult
	_ = json.Unmarshal(result, &r)
	return r
}

func architectLongShouldStopAfterToolResult(toolName string, result json.RawMessage) bool {
	r := decodeSaveFoundationResult(toolName, result)
	switch r.Type {
	case "expand_arc", "repair_arc", "complete_book":
		return true
	default:
		return false
	}
}

type writerDraftChapterTool struct {
	inner *tools.DraftChapterTool
	store *store.Store
}

func newWriterDraftChapterTool(st *store.Store) agentcore.Tool {
	return &writerDraftChapterTool{
		inner: tools.NewDraftChapterTool(st),
		store: st,
	}
}

func (t *writerDraftChapterTool) Name() string        { return t.inner.Name() }
func (t *writerDraftChapterTool) Description() string { return t.inner.Description() }
func (t *writerDraftChapterTool) Label() string       { return t.inner.Label() }

func (t *writerDraftChapterTool) ReadOnly(args json.RawMessage) bool {
	return t.inner.ReadOnly(args)
}

func (t *writerDraftChapterTool) ConcurrencySafe(args json.RawMessage) bool {
	return t.inner.ConcurrencySafe(args)
}

func (t *writerDraftChapterTool) StrictSchema() bool { return false }

func (t *writerDraftChapterTool) Schema() map[string]any {
	return toolSchemaWithoutRequired(t.inner.Schema(), "chapter")
}

func (t *writerDraftChapterTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return t.inner.Execute(ctx, args)
	}
	if hasPositiveChapter(raw["chapter"]) {
		return t.inner.Execute(ctx, args)
	}
	chapter := inferWriterDraftChapter(t.store)
	if chapter <= 0 {
		return nil, fmt.Errorf("chapter is required and cannot be inferred from current writing state")
	}
	encodedChapter, err := json.Marshal(chapter)
	if err != nil {
		return nil, err
	}
	raw["chapter"] = encodedChapter
	nextArgs, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("augment draft_chapter args: %w", err)
	}
	return t.inner.Execute(ctx, nextArgs)
}

func hasPositiveChapter(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var chapter int
	return json.Unmarshal(raw, &chapter) == nil && chapter > 0
}

func inferWriterDraftChapter(st *store.Store) int {
	if st == nil {
		return 0
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		return 0
	}
	if progress.InProgressChapter > 0 {
		return progress.InProgressChapter
	}
	if len(progress.PendingRewrites) == 1 {
		return progress.PendingRewrites[0]
	}
	if progress.Flow == domain.FlowRewriting || progress.Flow == domain.FlowPolishing {
		return 0
	}
	next := progress.NextChapter()
	if next <= 0 || (progress.TotalChapters > 0 && next > progress.TotalChapters) {
		return 0
	}
	return next
}

func toolSchemaWithoutRequired(schema map[string]any, field string) map[string]any {
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		out[key] = value
	}
	switch required := out["required"].(type) {
	case []string:
		out["required"] = removeRequiredString(required, field)
	case []any:
		next := make([]any, 0, len(required))
		for _, item := range required {
			if text, ok := item.(string); ok && text == field {
				continue
			}
			next = append(next, item)
		}
		out["required"] = next
	}
	return out
}

func removeRequiredString(required []string, field string) []string {
	next := make([]string, 0, len(required))
	for _, item := range required {
		if item != field {
			next = append(next, item)
		}
	}
	return next
}
