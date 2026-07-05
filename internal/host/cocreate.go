package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/globalprompt"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
	"github.com/voocel/ainovel-cli/internal/store"
)

// 冷启动共创：从零澄清需求，产出整本书的创作指令。
const coCreateSystemPrompt = `你是一个小说共创助手。你的任务不是直接开始写小说，而是通过多轮简短对话帮助用户澄清创作需求，并持续整理出一段可直接交给创作引擎的中文创作指令。

每一轮回复严格按以下 XML 格式输出，包含四个标签，依次出现，每个标签都必须有正确的开闭标签：

<reply>
给用户看的中文自然回复：先回应用户的输入，再最多提出 1 到 2 个当前最关键的问题。如果信息已足够开始创作，告诉用户可以按 Ctrl+S 开始。
</reply>

<draft>
当前完整的创作指令草稿，使用 Markdown：直接从二级标题开始，例如 "## 主题"、"## 关键要素"、"## 待澄清信息"；用项目符号列出要点。每一轮都要在已有结论上**累积更新**，吸收用户最新意图；即使本轮没有新增也要把完整草稿原样再写一次——不要省略、不要写"（保持上一轮）"之类的占位。
</draft>
` + coCreateProtocolTail

// 阶段共创：小说已写了一部分，规划"后续阶段"的走向。调用方需把当前故事状态摘要
// 追加到本 prompt 之后（"## 当前故事状态" 段），让模型在已写内容的基础上规划。
const stageCoCreateSystemPrompt = `你是一个小说"阶段共创"助手。这本小说已经写了一部分（进度见下方"当前故事状态"）。用户暂停下来，想和你一起规划"后续阶段"的走向，再继续创作。

你的任务不是续写正文，而是通过多轮简短对话帮用户想清楚后面这一段（接下来若干章 / 下一弧 / 下一卷）要往哪走，并持续整理出一段"后续方向 brief"，供创作引擎据此推进。

铁律：所有建议必须与"当前故事状态"里已发生的剧情、人物、伏笔一致，绝不推翻或忽略已写内容；只规划"后续怎么走"，不重新设计整本书。

每一轮回复严格按以下 XML 格式输出，包含四个标签，依次出现，每个标签都必须有正确的开闭标签：

<reply>
给用户看的中文自然回复：先回应用户的输入，再最多提出 1 到 2 个当前最关键的问题。如果后续方向已足够清晰，告诉用户可以按 Ctrl+S 把方向交给创作引擎、继续创作。
</reply>

<draft>
当前完整的"后续方向 brief"，使用 Markdown：直接从二级标题开始，例如 "## 后续走向"、"## 关键转折"、"## 要收的伏笔"、"## 节奏与篇幅"；用项目符号列出要点。每一轮都要在已有结论上**累积更新**，吸收用户最新意图；即使本轮没有新增也要把完整 brief 原样再写一次——不要省略、不要写"（保持上一轮）"之类的占位。
</draft>
` + coCreateProtocolTail

const adaptCoCreateSystemPrompt = `你是一个小说"改编共创"助手。用户已经提供了一本原小说，系统已完成原书切分和事实分析。你的任务不是续写原文，也不是从零原创一本新书，而是帮助用户明确"在主线尽量不走偏的情况下如何改编"。

你必须基于下方"全书改编资料包"提问和整理，不要凭空推翻原书主线；同时要把用户的关系线、女主戏份、虐心/纯爱等改编偏好落实成可执行 brief。

改编模式已在进入共创前由用户通过固定选项确认。第一条用户消息会给出当前生效的 mode_contract、granularity、由结构粒度固定的 rewrite_policy，以及 word_tolerance=0.xx 或 word_tolerance=disabled。你必须只把当前模式原样写入 draft，不要把模式选择作为问题再次询问，也不要自行改动：
- chapter：目标章节与原章节一一对应，固定 rewrite_policy=preserve_details。未受影响内容可复用原文，受改编目标影响的完整场景单元必须原创重写。
- arc：允许合并/拆分章节，固定 rewrite_policy=full_rewrite，word_tolerance=disabled。
- free：允许重构章节结构，固定 rewrite_policy=full_rewrite，word_tolerance=disabled。
- full_rewrite：正文完全重写，禁止直接搬运原文段落。
- preserve_details：仅适用于 chapter；原著细节优先，未受改编目标影响的剧情/段落允许复用原文，受影响部分再重写，并使用 source 字数容差。
- 上面是解释表，不是 draft 内容模板；draft 的"## 改编模式"只写第一条用户消息中的当前模式字段和当前模式说明，不要写 rewrite_policy_rule=chapter=>preserve_details;arc/free=>full_rewrite 这类所有模式混在一起的规则串。

每一轮回复严格按以下 XML 格式输出，包含四个标签，依次出现，每个标签都必须有正确的开闭标签：

<reply>
给用户看的中文自然回复：先回应用户的改编意图，再最多提出 1 到 2 个当前最关键的问题。如果改编目标已足够明确，告诉用户可以按 Ctrl+S 开始改编。
</reply>

<draft>
当前完整的"改编 brief"，使用 Markdown：直接从二级标题开始，例如 "## 改编模式"、"## 用户目标"、"## 主线保留规则"、"## 角色/关系改动"、"## 禁止偏离"、"## 逐章策略"。"## 改编模式" 中必须逐行写出 granularity=...、rewrite_policy=...、word_tolerance=...；arc/free 必须写 word_tolerance=disabled。每一轮都要在已有结论上累积更新，吸收用户最新意图；即使本轮没有新增也要把完整 brief 原样再写一次。
</draft>
` + coCreateProtocolTail

// coCreateProtocolTail 是两种共创模式共用的输出协议尾部（<ready> / <suggestions> + 输出规范）。
// 两模式只在开场语境与 <draft> 语义上不同，协议完全一致。
const coCreateProtocolTail = `
<ready>true|false</ready>

<suggestions>
1-3 条"用户接下来可能想说的话"，每行一条以 "- " 开头。这是用户卡壳时的引导，
按数字键填入输入框，用户可再编辑后发送。

要求：
- 站在用户口吻，像用户对你说的话，不要写成助手反问。
- 每条不超过 25 字，多样化句式，避免千篇一律。
- 给倾向 / 选择 / 补充意图，不要一句话替用户写完整设定。
</suggestions>

输出规范：
- 必须使用四个 XML 标签：<reply> / <draft> / <ready> / <suggestions>，每个都必须完整开闭。
- 标签名只能小写英文，不要改写成 <REPLY> / <REWRITE> / <回复> 等任何变体。
- 标签外不要添加任何说明、思考或代码围栏。
- <draft> 内允许多行 Markdown，直接换行书写，不需要任何转义。
- <ready> 只写 true 或 false，不要写 true|false。只要当前 <draft> 已经可以直接交给创作引擎执行，或你没有必须继续追问的关键问题，就必须填 true；只有还缺少会阻塞执行的核心信息时才填 false。
- <ready>true</ready> 时 <suggestions> 可以为空（保留空标签 <suggestions></suggestions> 即可）。`

// CoCreateProgressKind 标识流式回调的内容类型。
const (
	CoCreateProgressThinking = "thinking"
	CoCreateProgressReply    = "reply"
)

const (
	coCreateMaxAttempts              = retrypolicy.MaxAttempts
	coCreateMaxTokens                = 2048
	adaptCoCreateMaxTokens           = 8192
	coCreateSuggestionJudgeMaxTokens = 256
	coCreateModelRole                = "architect"
)

var coCreateRetrySleep = sleepBeforeCoCreateRetry

type coCreateModelIdentity struct {
	Provider string
	Model    string
}

func newCoCreateModelIdentity(model agentcore.ChatModel) coCreateModelIdentity {
	var identity coCreateModelIdentity
	if model == nil {
		return identity
	}
	if provider, ok := model.(interface{ ProviderName() string }); ok {
		identity.Provider = strings.TrimSpace(provider.ProviderName())
	}
	identity.Model = strings.TrimSpace(bootstrap.ModelName(model))
	return identity
}

func (i coCreateModelIdentity) label() string {
	switch {
	case i.Provider != "" && i.Model != "":
		return i.Provider + "/" + i.Model
	case i.Model != "":
		return i.Model
	case i.Provider != "":
		return i.Provider
	default:
		return ""
	}
}

func (i coCreateModelIdentity) wrapError(err error) error {
	if err == nil {
		return nil
	}
	if label := i.label(); label != "" {
		return fmt.Errorf("selected model %s: %w", label, err)
	}
	return err
}

// 四段式 XML 标签输出。XML 风格比方括号 marker 更鲁棒——Claude/GPT 训练数据里
// 大量 <thinking>...</thinking> 这类格式，模型几乎不会把 <reply> 改写成 <REWRITE>
// 或其他变体；闭合标签也让流式中段截断更精确（不依赖找下一个 marker 来断尾）。
const (
	tagReply       = "reply"
	tagDraft       = "draft"
	tagReady       = "ready"
	tagSuggestions = "suggestions"
)

func coCreateStream(ctx context.Context, models *bootstrap.ModelSet, sessions *store.SessionStore, timeout time.Duration, sysPrompt string, history []CoCreateMessage, onProgress func(kind, text string)) (reply CoCreateReply, err error) {
	return coCreateStreamWithMaxTokens(ctx, models, sessions, timeout, sysPrompt, history, coCreateMaxTokens, onProgress)
}

func coCreateStreamWithMaxTokens(ctx context.Context, models *bootstrap.ModelSet, sessions *store.SessionStore, timeout time.Duration, sysPrompt string, history []CoCreateMessage, maxTokens int, onProgress func(kind, text string)) (reply CoCreateReply, err error) {
	if len(history) == 0 {
		return CoCreateReply{}, fmt.Errorf("cocreate history is empty")
	}
	if timeout <= 0 {
		timeout = time.Duration(bootstrap.DefaultCoCreateTimeoutSeconds) * time.Second
	}
	if maxTokens <= 0 {
		maxTokens = coCreateMaxTokens
	}

	model := models.ForRole(coCreateModelRole)
	modelIdentity := newCoCreateModelIdentity(model)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	msgs := []agentcore.Message{agentcore.SystemMsg(globalprompt.Apply(sysPrompt))}
	for _, item := range history {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(item.Role)) {
		case "assistant":
			msgs = append(msgs, assistantMsg(content))
		default:
			msgs = append(msgs, agentcore.UserMsg(content))
		}
	}

	var raw, thinking strings.Builder
	var attempts int
	var retryErrors []string
	var stopReason agentcore.StopReason

	// 排查 "cocreate empty response" 等偶发问题需要看到模型实际返回什么。
	// 每轮全程落盘到 <output>/meta/sessions/cocreate.jsonl，与正式创作的 session 日志同位。
	start := time.Now()
	defer func() {
		if sessions == nil {
			return
		}
		_ = sessions.LogCoCreate(coCreateLogEntry{
			Time:             time.Now(),
			DurationMS:       time.Since(start).Milliseconds(),
			TimeoutSeconds:   int(timeout.Seconds()),
			ModelRole:        coCreateModelRole,
			SelectedProvider: modelIdentity.Provider,
			SelectedModel:    modelIdentity.Model,
			InputHistory:     history,
			Attempts:         attempts,
			RetryErrors:      retryErrors,
			RawResponse:      raw.String(),
			RawLen:           len([]rune(raw.String())),
			Thinking:         thinking.String(),
			ParsedReply:      reply.Message,
			ParsedDraft:      reply.Prompt,
			ParsedReady:      reply.Ready,
			ParsedSugs:       reply.Suggestions,
			StopReason:       string(stopReason),
			Error:            errString(err),
		})
	}()

	var streamCh <-chan agentcore.StreamEvent
	var streamed, done bool

retry:
	attempts++
	raw.Reset()
	thinking.Reset()
	stopReason = ""
	streamed = false
	done = false

	streamCh, err = model.GenerateStream(ctx, msgs, nil, agentcore.WithMaxTokens(maxTokens))
	if err != nil {
		if ok, sleepErr := prepareCoCreateRetry(ctx, err, attempts, onProgress, &retryErrors); sleepErr != nil {
			return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(sleepErr))
		} else if ok {
			goto retry
		}
		return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(err))
	}

	for ev := range streamCh {
		switch ev.Type {
		case agentcore.StreamEventThinkingDelta:
			thinking.WriteString(ev.Delta)
			if onProgress != nil {
				onProgress(CoCreateProgressThinking, thinking.String())
			}
		case agentcore.StreamEventTextDelta:
			streamed = true
			raw.WriteString(ev.Delta)
			if onProgress != nil {
				onProgress(CoCreateProgressReply, extractReplyPreview(raw.String()))
			}
		case agentcore.StreamEventDone:
			done = true
			stopReason = ev.StopReason
			if stopReason == "" {
				stopReason = ev.Message.StopReason
			}
			if !streamed {
				raw.WriteString(ev.Message.TextContent())
			}
		case agentcore.StreamEventError:
			if ev.Err != nil {
				if ok, sleepErr := prepareCoCreateRetry(ctx, ev.Err, attempts, onProgress, &retryErrors); sleepErr != nil {
					return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(sleepErr))
				} else if ok {
					goto retry
				}
				return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(ev.Err))
			}
			streamErr := fmt.Errorf("cocreate generate failed: %w", agentcore.ErrProviderNetwork)
			if ok, sleepErr := prepareCoCreateRetry(ctx, streamErr, attempts, onProgress, &retryErrors); sleepErr != nil {
				return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(sleepErr))
			} else if ok {
				goto retry
			}
			return CoCreateReply{}, modelIdentity.wrapError(streamErr)
		}
	}
	if !done {
		streamErr := fmt.Errorf("cocreate stream closed before done: %w", agentcore.ErrProviderNetwork)
		if ok, sleepErr := prepareCoCreateRetry(ctx, streamErr, attempts, onProgress, &retryErrors); sleepErr != nil {
			return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(sleepErr))
		} else if ok {
			goto retry
		}
		return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(streamErr))
	}
	if stopReason == agentcore.StopReasonLength {
		return CoCreateReply{}, fmt.Errorf("cocreate response truncated: stop_reason=%s", stopReason)
	}

	// Channel fallback：思考型模型（R1/GLM-Z1/QwQ 等）偶发把完整答案写进
	// reasoning_content 后没切回 final answer 通道，导致 raw 为空但 thinking 含
	// 完整四段。实测见 meta/sessions/cocreate.jsonl —— 直接拿 thinking 当 raw 解析，
	// 协议层已有降级处理（无 [REPLY] 标记时整段当 reply），救场后 UI 体验无差别。
	rawText := raw.String()
	if strings.TrimSpace(rawText) == "" {
		if t := strings.TrimSpace(thinking.String()); t != "" {
			rawText = t
		}
	}
	if err := rejectIncompleteCoCreateXML(rawText); err != nil {
		return CoCreateReply{}, err
	}
	reply, err = parseCoCreateResponse(rawText)
	if err == nil && len(reply.Suggestions) == 0 {
		reply.Suggestions = judgeCoCreateSuggestions(ctx, model, reply)
	}
	return reply, err
}

func judgeCoCreateSuggestions(ctx context.Context, model agentcore.ChatModel, reply CoCreateReply) []string {
	if model == nil || strings.TrimSpace(reply.Message) == "" {
		return nil
	}
	resp, err := model.Generate(ctx, []agentcore.Message{
		agentcore.SystemMsg(coCreateSuggestionJudgePrompt),
		agentcore.UserMsg(buildCoCreateSuggestionJudgeInput(reply.Message)),
	}, nil, agentcore.WithMaxTokens(coCreateSuggestionJudgeMaxTokens), agentcore.WithJSONMode())
	if err != nil || resp == nil {
		return nil
	}
	return parseSuggestionJudgeResponse(resp.Message.TextContent())
}

const coCreateSuggestionJudgePrompt = `你是小说共创 UI 的建议按钮判定器。你的任务是判断助手回复里是否真的包含适合显示为“用户下一句可以点击发送”的建议。

只输出 JSON，不要解释：
{"suggestions":["..."]}

严格规则：
- 只在助手明确给出用户可选择的下一步、倾向或补充意图时返回建议。
- 建议必须改写成用户口吻，像用户会对助手说的话；不要写成标题、名词短语或助手指令。
- 每条不超过 25 个中文字符，最多 3 条。
- 如果回复只是总结、规划说明、确认、泛泛询问“是否符合预期/是否需要调整”，返回空数组。
- 不要从剧情规划正文、卷标题、章节标题、主题名里硬抽按钮。`

func buildCoCreateSuggestionJudgeInput(reply string) string {
	return "助手回复：\n" + strings.TrimSpace(reply)
}

func prepareCoCreateRetry(ctx context.Context, err error, attempt int, onProgress func(kind, text string), retryErrors *[]string) (bool, error) {
	if !shouldRetryCoCreate(ctx, err, attempt) {
		return false, nil
	}
	if retryErrors != nil {
		*retryErrors = append(*retryErrors, err.Error())
	}
	clearCoCreateProgress(onProgress)
	if err := waitBeforeCoCreateRetry(ctx, attempt); err != nil {
		return false, err
	}
	return true, nil
}

func shouldRetryCoCreate(ctx context.Context, err error, attempt int) bool {
	if attempt >= coCreateMaxAttempts {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	return agentcore.IsFailoverEligible(err)
}

func clearCoCreateProgress(onProgress func(kind, text string)) {
	if onProgress == nil {
		return
	}
	onProgress(CoCreateProgressThinking, "")
	onProgress(CoCreateProgressReply, "")
}

func waitBeforeCoCreateRetry(ctx context.Context, attempt int) error {
	return coCreateRetrySleep(ctx, coCreateRetryDelay(attempt))
}

func coCreateRetryDelay(attempt int) time.Duration {
	return retrypolicy.Delay(attempt)
}

func sleepBeforeCoCreateRetry(ctx context.Context, delay time.Duration) error {
	return retrypolicy.Wait(ctx, delay)
}

func adaptSystemPrompt(st *store.Store) string {
	return adaptCoCreateSystemPrompt + "\n\n## 全书改编资料包\n\n" + adaptationDossierSnapshot(st)
}

func adaptationDossierSnapshot(st *store.Store) string {
	if st == nil || st.Adaptation == nil {
		return "尚未加载全书改编资料包。"
	}
	manifest, manifestErr := st.Adaptation.LoadSourceManifest()
	dossier, dossierErr := st.Adaptation.LoadCoCreateDossier()
	if manifestErr != nil || dossierErr != nil {
		return "全书改编资料包读取失败，请提醒用户重新点击原文分析。"
	}
	if manifest == nil {
		return "尚未加载原书快照。"
	}
	if dossier == nil || !store.CoCreateDossierMatchesManifest(*dossier, *manifest, adapt.CoCreateDossierPromptVersion, adapt.CoCreateDossierBatchSize) {
		return "全书改编资料包缺失或已过期，请提醒用户重新点击原文分析后再共创。"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "- 来源：%s\n", manifest.SourcePath)
	fmt.Fprintf(&sb, "- 原文章节数：%d\n", manifest.ChapterCount)
	fmt.Fprintf(&sb, "- 资料包批次：%d 批，每批约 %d 章\n", len(dossier.Batches), dossier.BatchSize)
	if strings.TrimSpace(dossier.Overview) != "" {
		fmt.Fprintf(&sb, "- 覆盖说明：%s\n", dossier.Overview)
	}
	writeDossierStrings(&sb, "### 全书主线与因果锚点", dossier.Mainline, 80)
	writeDossierSignals(&sb, "### 关系线信号", dossier.RelationshipMap, 80)
	writeDossierSignals(&sb, "### 女主相关信号", dossier.HeroineSignals, 80)
	writeDossierRisks(&sb, "### 女配暧昧/后宫风险", dossier.AmbiguityRisks, 80)
	writeDossierSignals(&sb, "### 情侣/暧昧进展节点", dossier.CoupleMilestones, 80)
	writeDossierStrings(&sb, "### 改编注意事项", dossier.AdaptationNotes, 80)
	return sb.String()
}

func writeDossierStrings(sb *strings.Builder, title string, values []string, max int) {
	values = trimDossierStrings(values, max)
	if len(values) == 0 {
		return
	}
	sb.WriteString("\n")
	sb.WriteString(title)
	sb.WriteString("\n")
	for _, value := range values {
		fmt.Fprintf(sb, "- %s\n", value)
	}
}

func writeDossierSignals(sb *strings.Builder, title string, values []domain.AdaptationRelationshipSignal, max int) {
	if len(values) == 0 {
		return
	}
	sb.WriteString("\n")
	sb.WriteString(title)
	sb.WriteString("\n")
	for i, value := range values {
		if max > 0 && i >= max {
			fmt.Fprintf(sb, "- 另有 %d 条同类信号已存入资料包，后续规划阶段可继续读取。\n", len(values)-max)
			break
		}
		fmt.Fprintf(sb, "- %s%s：%s", dossierChapterLabel(value.Chapters), dossierCharactersLabel(value.Characters), value.Summary)
		if strings.TrimSpace(value.Evidence) != "" {
			fmt.Fprintf(sb, "（证据：%s）", value.Evidence)
		}
		sb.WriteString("\n")
	}
}

func writeDossierRisks(sb *strings.Builder, title string, values []domain.AdaptationRelationshipRisk, max int) {
	if len(values) == 0 {
		return
	}
	sb.WriteString("\n")
	sb.WriteString(title)
	sb.WriteString("\n")
	for i, value := range values {
		if max > 0 && i >= max {
			fmt.Fprintf(sb, "- 另有 %d 条风险已存入资料包，后续规划阶段可继续读取。\n", len(values)-max)
			break
		}
		fmt.Fprintf(sb, "- %s%s：%s", dossierChapterLabel(value.Chapters), dossierCharactersLabel(value.Characters), value.Risk)
		if strings.TrimSpace(value.Evidence) != "" {
			fmt.Fprintf(sb, "（证据：%s）", value.Evidence)
		}
		if strings.TrimSpace(value.Suggestion) != "" {
			fmt.Fprintf(sb, "；处理建议：%s", value.Suggestion)
		}
		sb.WriteString("\n")
	}
}

func trimDossierStrings(values []string, max int) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

func dossierChapterLabel(chapters []int) string {
	if len(chapters) == 0 {
		return ""
	}
	if len(chapters) == 1 {
		return fmt.Sprintf("第 %d 章", chapters[0])
	}
	return fmt.Sprintf("第 %d-%d 章", chapters[0], chapters[len(chapters)-1])
}

func dossierCharactersLabel(characters []string) string {
	characters = trimDossierStrings(characters, 6)
	if len(characters) == 0 {
		return ""
	}
	if label := strings.Join(characters, "/"); label != "" {
		return " " + label
	}
	return ""
}

// coCreateLogEntry 是写入 meta/sessions/cocreate.jsonl 的一行结构。
// 字段命名贴近 jsonl 直查习惯（snake_case），方便 jq 过滤。
type coCreateLogEntry struct {
	Time             time.Time         `json:"time"`
	DurationMS       int64             `json:"duration_ms"`
	TimeoutSeconds   int               `json:"timeout_seconds,omitempty"`
	ModelRole        string            `json:"model_role,omitempty"`
	SelectedProvider string            `json:"selected_provider,omitempty"`
	SelectedModel    string            `json:"selected_model,omitempty"`
	InputHistory     []CoCreateMessage `json:"input_history"`
	Attempts         int               `json:"attempts,omitempty"`
	RetryErrors      []string          `json:"retry_errors,omitempty"`
	RawResponse      string            `json:"raw_response"`
	RawLen           int               `json:"raw_len"`
	Thinking         string            `json:"thinking,omitempty"`
	ParsedReply      string            `json:"parsed_reply"`
	ParsedDraft      string            `json:"parsed_draft"`
	ParsedReady      bool              `json:"parsed_ready"`
	ParsedSugs       []string          `json:"parsed_sugs,omitempty"`
	StopReason       string            `json:"stop_reason,omitempty"`
	Error            string            `json:"error,omitempty"`
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func assistantMsg(text string) agentcore.Message {
	return agentcore.Message{
		Role:      agentcore.RoleAssistant,
		Content:   []agentcore.ContentBlock{agentcore.TextBlock(text)},
		Timestamp: time.Now(),
	}
}

// parseCoCreateResponse 解析 XML 标签输出。模型若没遵守协议（直接说自然语言），
// 整段作为 reply 显示，draft 留空让 session 保留上一轮。
func parseCoCreateResponse(raw string) (CoCreateReply, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return CoCreateReply{}, fmt.Errorf("cocreate empty response")
	}

	reply, draft, ready, suggestions := splitCoCreateMarkers(raw)
	if reply == "" {
		// 模型没遵守 XML 协议：整段作为 reply。
		return CoCreateReply{Message: raw, Prompt: "", Ready: false, Raw: raw}, nil
	}
	return CoCreateReply{
		Message:     reply,
		Prompt:      draft,
		Ready:       ready,
		Suggestions: suggestions,
		Raw:         raw,
	}, nil
}

func rejectIncompleteCoCreateXML(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	for _, tag := range []string{tagReply, tagDraft, tagReady, tagSuggestions} {
		open := "<" + tag + ">"
		closeTag := "</" + tag + ">"
		hasOpen := strings.Contains(raw, open)
		hasClose := strings.Contains(raw, closeTag)
		if hasOpen && !hasClose {
			return fmt.Errorf("cocreate response incomplete: missing %s", closeTag)
		}
		if hasClose && !hasOpen {
			return fmt.Errorf("cocreate response incomplete: missing %s", open)
		}
	}
	return nil
}

// splitCoCreateMarkers 按四个 XML 标签切分文本。
// 标签可能缺失（流式中段或模型遗漏），缺失部分对应字段为空 / false / nil。
// 缺失闭标签时，extractTagContent 会取到字符串末尾，仍尽力解析。
func splitCoCreateMarkers(s string) (reply, draft string, ready bool, suggestions []string) {
	reply = extractTagContent(s, tagReply)
	draft = extractTagContent(s, tagDraft)
	readyStr := strings.ToLower(extractTagContent(s, tagReady))
	ready = readyStr == "true" || readyStr == "yes"
	suggestions = parseSuggestions(extractTagContent(s, tagSuggestions))
	return
}

// extractTagContent 从 s 中抠出 <tag>...</tag> 之间的文本。
// 三种偶发故障场景兜底，避免直接走降级丢字段：
//  1. 有开无闭（流式中段）→ 切到下一个已知开标签前
//  2. 无开有闭（模型 typo，如 <suggestions> 写成 <uggestions>）→ 从最近一个已知
//     完整闭合标签的结束位置开始，到 </tag> 之前
//  3. reply 完全无开标签（模型直接以自然语言开篇，末尾贴 </reply>）→ 从开头到 </reply>
func extractTagContent(s, tag string) string {
	open := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	oIdx := strings.Index(s, open)
	if oIdx >= 0 {
		rest := s[oIdx+len(open):]
		if cIdx := strings.Index(rest, closeTag); cIdx >= 0 {
			return strings.TrimSpace(rest[:cIdx])
		}
		// 有开无闭 → 切到下一个已知开标签前
		for _, other := range []string{"<reply>", "<draft>", "<ready>", "<suggestions>"} {
			if other == open {
				continue
			}
			if idx := strings.Index(rest, other); idx >= 0 {
				rest = rest[:idx]
			}
		}
		return strings.TrimSpace(rest)
	}

	// 无开有闭 → 从最近一个已知完整闭合标签的结束位置开始，到 </tag>。
	if cIdx := strings.Index(s, closeTag); cIdx >= 0 {
		prefix := s[:cIdx]
		start := 0
		for _, t := range []string{"</reply>", "</draft>", "</ready>", "</suggestions>"} {
			if t == closeTag {
				continue
			}
			if i := strings.LastIndex(prefix, t); i >= 0 {
				if end := i + len(t); end > start {
					start = end
				}
			}
		}
		return strings.TrimSpace(prefix[start:])
	}
	return ""
}

// parseSuggestions 把 <suggestions> 段每行抠出来，去掉 "- " / "* " / "1. " 等列表前缀。
// 最多保留 3 条；空行、过短（<2 字）、整行像 XML 标签的（typo 开标签兜底残留，
// 例如 <uggestions>）忽略。
func parseSuggestions(text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 整行像 XML 标签 → 跳过（防 typo 开标签污染）
		if strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") {
			continue
		}
		// 剥列表前缀
		switch {
		case strings.HasPrefix(line, "- "):
			line = strings.TrimSpace(line[2:])
		case strings.HasPrefix(line, "* "):
			line = strings.TrimSpace(line[2:])
		case isOrderedSuggestion(line):
			line = stripOrderedPrefix(line)
		}
		if len([]rune(line)) < 2 {
			continue
		}
		out = append(out, line)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

type coCreateSuggestionJudgeResponse struct {
	Suggestions []string `json:"suggestions"`
}

func parseSuggestionJudgeResponse(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	raw = extractJSONObject(raw)
	if raw == "" {
		return nil
	}
	var response coCreateSuggestionJudgeResponse
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		return nil
	}
	return normalizeCoCreateSuggestionTexts(response.Suggestions, 25)
}

func normalizeCoCreateSuggestionTexts(values []string, maxRunes int) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		text := cleanCoCreateSuggestionText(value)
		runes := len([]rune(text))
		if runes < 2 || (maxRunes > 0 && runes > maxRunes) || seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, text)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

func cleanCoCreateSuggestionText(text string) string {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "`")
	switch {
	case strings.HasPrefix(text, "- "):
		text = strings.TrimSpace(text[2:])
	case strings.HasPrefix(text, "* "):
		text = strings.TrimSpace(text[2:])
	case isOrderedSuggestion(text):
		text = stripOrderedPrefix(text)
	}
	text = strings.TrimSpace(text)
	text = strings.Trim(text, " \t\r\n:：,，、。；;？?")
	return text
}

func extractJSONObject(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) >= 2 {
			if strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
				lines = lines[1:]
			}
			if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
				lines = lines[:len(lines)-1]
			}
			text = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return ""
	}
	return strings.TrimSpace(text[start : end+1])
}

// isOrderedSuggestion 判断行首是否形如 "1. " / "12. "（数字+点+空格）。
func isOrderedSuggestion(line string) bool {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(line) && line[i] == '.' && line[i+1] == ' '
}

func stripOrderedPrefix(line string) string {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(line) {
		return line
	}
	return strings.TrimSpace(line[i+2:])
}

// extractReplyPreview 流式预览：raw 还在生长时给 UI 一段可显示的文本。
// 找到 <reply> 之后的内容，切到 </reply> 或下一个开标签 <draft> 之前。
// 模型半遵守（漏 <reply> 开标签）时，开头到 </reply> 或 <draft> 都算 reply。
func extractReplyPreview(raw string) string {
	trimmed := strings.TrimSpace(raw)
	open := "<" + tagReply + ">"
	closeTag := "</" + tagReply + ">"
	draftOpen := "<" + tagDraft + ">"

	rest := trimmed
	if rIdx := strings.Index(trimmed, open); rIdx >= 0 {
		rest = trimmed[rIdx+len(open):]
	}
	if cIdx := strings.Index(rest, closeTag); cIdx >= 0 {
		return strings.TrimSpace(rest[:cIdx])
	}
	if dIdx := strings.Index(rest, draftOpen); dIdx >= 0 {
		rest = rest[:dIdx]
	}
	return strings.TrimSpace(rest)
}
