package tools

import (
	"fmt"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/stylestat"
)

type contextBuildState struct {
	chapter         int
	profile         domain.ContextProfile
	progress        *domain.Progress
	runMeta         *domain.RunMeta
	currentEntry    *domain.OutlineEntry
	chapterPlan     *domain.ChapterPlan
	storyThreads    []domain.RecallItem
	foreshadow      []domain.ForeshadowEntry
	relationships   []domain.RelationshipEntry
	allStateChanges []domain.StateChange
	styleRules      *domain.WritingStyleRules
	purpose         chapterContextPurpose
}

type chapterContextPurpose string

const (
	chapterContextWriting   chapterContextPurpose = "writing"
	chapterContextRewriting chapterContextPurpose = "rewriting"
	chapterContextPolishing chapterContextPurpose = "polishing"
)

func resolveChapterContextPurpose(progress *domain.Progress, chapter int) chapterContextPurpose {
	if progress == nil || !slices.Contains(progress.PendingRewrites, chapter) {
		return chapterContextWriting
	}
	if progress.Flow == domain.FlowPolishing {
		return chapterContextPolishing
	}
	return chapterContextRewriting
}

type chapterContextEnvelope struct {
	Working    map[string]any
	Episodic   map[string]any
	References map[string]any
	Selected   map[string]any
}

type architectContextEnvelope struct {
	Planning   map[string]any
	Foundation map[string]any
	References map[string]any
}

func newChapterContextEnvelope() chapterContextEnvelope {
	return chapterContextEnvelope{
		Working:    make(map[string]any),
		Episodic:   make(map[string]any),
		References: make(map[string]any),
		Selected:   make(map[string]any),
	}
}

func newArchitectContextEnvelope() architectContextEnvelope {
	return architectContextEnvelope{
		Planning:   make(map[string]any),
		Foundation: make(map[string]any),
		References: make(map[string]any),
	}
}

func (e chapterContextEnvelope) apply(result map[string]any) {
	// 合并而非替换：Execute 的章节路径会先后 apply 两个信封（seed + buildChapterContext），
	// 整体赋值会让第二次 apply 丢弃 seed 的容器内容，working_memory.* 等 canonical
	// 路径随之失效（prompt 指针指向空气，模型只能靠顶层镜像模糊容错）。
	mergeEnvelopeSection(result, "working_memory", e.Working)
	mergeEnvelopeSection(result, "episodic_memory", e.Episodic)
	mergeEnvelopeSection(result, "reference_pack", e.References)
	if len(e.Selected) > 0 {
		mergeEnvelopeSection(result, "selected_memory", e.Selected)
	}
	// Chapter consumers use the canonical memory sections. Mirroring the same
	// payload at the top level used to duplicate large references and contracts
	// in every Writer request (20 KiB of references twice in a mature project).
}

// mergeEnvelopeSection 把 section 合并进 result[key] 的既有容器；容器不存在时直接挂载。
func mergeEnvelopeSection(result map[string]any, key string, section map[string]any) {
	if existing, ok := result[key].(map[string]any); ok {
		for k, v := range section {
			existing[k] = v
		}
		return
	}
	result[key] = section
}

func (e architectContextEnvelope) apply(result map[string]any) {
	result["planning_memory"] = e.Planning
	result["foundation_memory"] = e.Foundation
	result["reference_pack"] = e.References
	// Architect consumers use the canonical memory sections. Top-level mirrors
	// duplicated the complete outline, foundation and reference payload, adding
	// more than 50 KiB to a mature project's model request.
}

// buildProgressStatus 仅在 Coordinator 调用（不传 chapter）时返回进度摘要,
// Writer 不需要这些信息,避免干扰写作。
func (t *ContextTool) buildProgressStatus(result map[string]any) {
	progress, err := t.store.Progress.Load()
	if err != nil || progress == nil {
		return
	}
	status := map[string]any{
		"phase":              string(progress.Phase),
		"flow":               string(progress.Flow),
		"completed_chapters": len(progress.CompletedChapters),
		"total_chapters":     progress.TotalChapters,
		"next_chapter":       progress.NextChapter(),
		"total_word_count":   progress.TotalWordCount,
	}
	if progress.InProgressChapter > 0 {
		status["in_progress_chapter"] = progress.InProgressChapter
	}
	if len(progress.PendingRewrites) > 0 {
		status["pending_rewrites"] = progress.PendingRewrites
		status["rewrite_reason"] = progress.RewriteReason
	}
	if progress.Layered {
		status["layered"] = true
		status["current_volume"] = progress.CurrentVolume
		status["current_arc"] = progress.CurrentArc
	}
	if progress.Phase == domain.PhaseComplete {
		status["finished"] = true
	}
	result["progress_status"] = status
}

// buildUserRules 把合并后的 Bundle 注入 working_memory.user_rules（canonical 路径）。
//
// 单点注入：writer / editor / architect / coordinator 任一路径调用 novel_context
// 都能在 working_memory.user_rules 拿到一致的偏好。architect 路径原本没有 working_memory，
// 由本函数按需新建（仅装 user_rules）；chapter > 0 路径下 working_memory 已存在，直接嵌入。
//
// 即便 Bundle 为空也注入，保持字段稳定，避免 LLM 看到 user_rules=null 而走异常分支。
//
// 注入策略：只给 LLM 看 structured + preferences——这两项才是创作时需要遵循的偏好。
// sources / conflicts 是诊断信息（用户冲突排查），不进 LLM；由 CLI 启动诊断面板按需展示。
func (t *ContextTool) buildUserRules(result map[string]any) {
	snap, err := t.store.UserRules.Load()
	if err != nil || snap == nil {
		// 快照未生成（老书首次/异常）：退到代码内置默认，保证机械底线（字数/禁语/疲劳词）始终存在。
		def := rules.BuildSnapshot([]rules.Candidate{rules.SystemDefaults()})
		snap = &def
	}
	working, ok := result["working_memory"].(map[string]any)
	if !ok {
		working = map[string]any{}
		result["working_memory"] = working
	}
	working["user_rules"] = compactUserRulesPayload(snap.Payload())
}

func (t *ContextTool) buildWordBudget(result map[string]any, chapter int) {
	meta, err := t.store.RunMeta.Load()
	if err != nil || meta == nil || meta.WordBudget == nil || meta.WordBudget.TargetTotalWords <= 0 {
		return
	}
	progress, perr := t.store.Progress.Load()
	if perr != nil {
		return
	}
	payload, ok := meta.WordBudget.Runtime(progress, chapter)
	if !ok {
		return
	}
	working, ok := result["working_memory"].(map[string]any)
	if !ok {
		working = map[string]any{}
		result["working_memory"] = working
	}
	working["word_budget"] = payload
	result["word_budget"] = payload
}

func (t *ContextTool) buildSimulationProfile(result map[string]any, sectionKey string, warn func(string, error)) {
	profile, err := t.store.Simulation.Load()
	if err != nil {
		warn("simulation_profile", err)
		return
	}
	compact := t.compactArchitectSimulationProfile(profile)
	if compact == nil {
		return
	}
	section, ok := result[sectionKey].(map[string]any)
	if !ok {
		section = map[string]any{}
		result[sectionKey] = section
	}
	section["simulation_profile"] = compact
	result["simulation_profile"] = true
	if t.simulationMode == contextSimulationModeReinforced {
		result["simulation_mode"] = contextSimulationModeReinforced
	}
}

func (t *ContextTool) compactArchitectSimulationProfile(profile *domain.SimulationProfile) *domain.SimulationCompactProfile {
	compact := t.compactSimulationProfile(profile)
	if compact == nil {
		return nil
	}
	// Planning needs structural imitation only. Prose lexicon, sentence-level
	// style, pacing and non-Architect role instructions belong to chapter work.
	return &domain.SimulationCompactProfile{
		Version:     compact.Version,
		Mode:        compact.Mode,
		SourceCount: compact.SourceCount,
		PlotDesign: domain.SimulationPlotDesign{
			OpeningPatterns:      compactStringList(compact.PlotDesign.OpeningPatterns, 1, 60),
			EscalationPatterns:   compactStringList(compact.PlotDesign.EscalationPatterns, 1, 60),
			TurningPointPatterns: compactStringList(compact.PlotDesign.TurningPointPatterns, 1, 60),
			PayoffPatterns:       compactStringList(compact.PlotDesign.PayoffPatterns, 1, 60),
		},
		HookDesign: domain.SimulationHookDesign{
			HookTypes:           compactStringList(compact.HookDesign.HookTypes, 1, 60),
			Placement:           compactStringList(compact.HookDesign.Placement, 1, 60),
			CliffhangerPatterns: compactStringList(compact.HookDesign.CliffhangerPatterns, 1, 60),
			PayoffRules:         compactStringList(compact.HookDesign.PayoffRules, 1, 60),
		},
		ReaderEngagement: domain.SimulationReaderEngagement{
			Methods:            compactStringList(compact.ReaderEngagement.Methods, 1, 60),
			EmotionalDrivers:   compactStringList(compact.ReaderEngagement.EmotionalDrivers, 1, 60),
			ProgressionRewards: compactStringList(compact.ReaderEngagement.ProgressionRewards, 1, 60),
			AntiPatterns:       compactStringList(compact.ReaderEngagement.AntiPatterns, 1, 60),
		},
		RoleGuidance: domain.SimulationRoleGuidance{
			Architect: compactStringList(compact.RoleGuidance.Architect, 1, 60),
		},
	}
}

func (t *ContextTool) compactSimulationProfile(profile *domain.SimulationProfile) *domain.SimulationCompactProfile {
	if t.simulationMode == contextSimulationModeReinforced {
		return domain.CompactSimulationProfileForMode(profile, contextSimulationModeReinforced)
	}
	return domain.CompactSimulationProfile(profile)
}

func (t *ContextTool) buildAdaptationChapterContext(result map[string]any, chapter int, warn func(string, error)) {
	plan, err := t.store.Adaptation.LoadPlan()
	warn("adaptation_plan", err)
	if err != nil || plan == nil {
		return
	}
	chapterPlan, ok := findAdaptationChapterPlan(plan, chapter)
	if !ok {
		return
	}
	working, ok := result["working_memory"].(map[string]any)
	if !ok {
		working = map[string]any{}
		result["working_memory"] = working
	}
	result["adaptation_mode"] = true
	actualRunes := 0
	if _, words, draftErr := t.store.Drafts.LoadChapterContent(chapter); draftErr == nil {
		actualRunes = words
	} else {
		warn("adaptation_draft_words", draftErr)
	}
	working["adaptation"] = compactAdaptationPlanSummary(plan)
	working["adaptation_effective_mode"] = buildAdaptationEffectiveMode(plan, chapterPlan)
	working["adaptation_contract"] = compactAdaptationChapterPlan(chapterPlan)
	working["adaptation_word_contract"] = buildAdaptationWordContract(t.store, plan, chapterPlan, chapter, actualRunes)
	working["adaptation_source_coverage"] = map[string]any{
		"chapter":         chapterPlan.Chapter,
		"source_chapters": chapterPlan.SourceChapters,
		"source_range":    chapterPlan.SourceRange,
		"source_segments": chapterPlan.SourceSegments,
		"event_ids":       chapterPlan.EventIDs,
		"is_added":        chapterPlan.IsAdded,
		"coverage_note":   chapterPlan.CoverageNote,
		"source_role":     adaptationSourceRoleForGranularity(plan.Granularity),
	}
	rules := plan.Rules
	if len(rules) == 0 && strings.TrimSpace(plan.Brief) != "" {
		rules = domain.CompileAdaptationRules(plan.Brief, plan.Granularity)
	}
	if activeRules := domain.ApplicableAdaptationRules(rules, plan.Granularity, chapter); len(activeRules) > 0 {
		working["adaptation_active_rules"] = compactActiveAdaptationRules(activeRules)
	}

	reports, reportErr := t.store.Adaptation.LoadSourceReports()
	warn("adaptation_source_reports", reportErr)
	if reportErr == nil && len(reports) > 0 {
		working["source_ref_reports"] = compactSourceReportsForContext(reports, chapterPlan.SourceChapters)
	}
}

func compactActiveAdaptationRules(rules []domain.AdaptationRule) []map[string]any {
	const (
		maxRuleTextRunes = 300
	)
	selected := domain.SelectAdaptationPromptRules(rules, domain.AdaptationPromptMaxRules, domain.AdaptationPromptMaxForbiddenRules)
	out := make([]map[string]any, 0, len(selected))
	for _, rule := range selected {
		text := strings.TrimSpace(rule.Text)
		payload := map[string]any{
			"rule_id": rule.ID,
			"kind":    rule.Kind,
			"text":    truncateRunes(text, maxRuleTextRunes),
		}
		if len([]rune(text)) > maxRuleTextRunes {
			payload["truncated"] = true
		}
		out = append(out, payload)
	}
	return out
}

func (t *ContextTool) buildAdaptationPlanningContext(result map[string]any, warn func(string, error)) {
	plan, err := t.store.Adaptation.LoadPlan()
	warn("adaptation_plan", err)
	if err != nil || plan == nil {
		return
	}
	section, ok := result["planning_memory"].(map[string]any)
	if !ok {
		section = map[string]any{}
		result["planning_memory"] = section
	}
	result["adaptation_mode"] = true
	summary := compactAdaptationPlanSummary(plan)
	summary["target_chapters"] = len(plan.Chapters)
	if manifest, manifestErr := t.store.Adaptation.LoadSourceManifest(); manifestErr == nil && manifest != nil {
		summary["source_path"] = manifest.SourcePath
		summary["source_chapters"] = manifest.ChapterCount
	} else {
		warn("adaptation_source_manifest", manifestErr)
	}
	section["adaptation_plan"] = summary
}

func buildAdaptationEffectiveMode(plan *domain.AdaptationPlan, chapterPlan domain.AdaptationChapterPlan) map[string]any {
	if plan == nil {
		return nil
	}
	granularity := domain.NormalizeAdaptationGranularity(plan.Granularity)
	rewritePolicy := domain.AdaptationRewritePolicyForGranularity(granularity)
	mode := map[string]any{
		"granularity":                  granularity,
		"rewrite_policy":               rewritePolicy,
		"mode_contract":                granularity + "/" + rewritePolicy,
		"current_mode_only":            true,
		"preserve_details_applicable":  granularity == domain.AdaptationGranularityChapter && rewritePolicy == domain.AdaptationRewritePreserveDetails,
		"source_chapters":              chapterPlan.SourceChapters,
		"source_range":                 chapterPlan.SourceRange,
		"source_reference_policy":      adaptationSourceReferencePolicy(granularity),
		"source_mapping_meaning":       adaptationSourceMappingMeaning(granularity),
		"source_read_instruction":      adaptationSourceReadInstruction(granularity),
		"writer_instruction":           adaptationModeWriterInstruction(granularity),
		"budget_instruction":           adaptationBudgetWriterInstruction(granularity),
		"legacy_rewrite_policy_notice": adaptationLegacyRewritePolicyNotice(plan.Brief),
	}
	switch granularity {
	case domain.AdaptationGranularityFree:
		mode["must_not"] = []string{
			"不要把 source_chapters/source_range 理解为本章对应原著第几章",
			"不要把本章称为 preserve_details 策略",
			"不要因为存在 source refs 就反复读取原文章节",
			"不要让原著旧结局覆盖已经确认的新提案、新大纲和已写剧情",
		}
	case domain.AdaptationGranularityArc:
		mode["must_not"] = []string{
			"不要把 source_chapters/source_range 理解为逐字复用许可",
			"不要把 full_rewrite 写成 preserve_details",
			"不要搬运原文段落",
		}
	default:
		mode["must_not"] = []string{
			"不要只写改动片段",
			"不要把改编内容写成提示性括注或补丁标签",
		}
	}
	return mode
}

func adaptationSourceRoleForGranularity(granularity string) string {
	switch domain.NormalizeAdaptationGranularity(granularity) {
	case domain.AdaptationGranularityFree:
		return "background_anchor_only"
	case domain.AdaptationGranularityArc:
		return "mainline_anchor"
	default:
		return "ordered_source_segment_ownership"
	}
}

func adaptationSourceReferencePolicy(granularity string) string {
	switch domain.NormalizeAdaptationGranularity(granularity) {
	case domain.AdaptationGranularityFree:
		return "optional_background_anchor"
	case domain.AdaptationGranularityArc:
		return "mainline_anchor"
	default:
		return "required_source_segment"
	}
}

func adaptationSourceMappingMeaning(granularity string) string {
	switch domain.NormalizeAdaptationGranularity(granularity) {
	case domain.AdaptationGranularityFree:
		return "后台覆盖率与必要事实锚点；不表示目标章对应原著章节"
	case domain.AdaptationGranularityArc:
		return "主线与卷弧覆盖锚点；不要求目标章与原文章节一一对应"
	default:
		return "短来源章通常对应一个目标章；长来源章由多个有序 SourceSegment 连续承接"
	}
}

func adaptationSourceReadInstruction(granularity string) string {
	switch domain.NormalizeAdaptationGranularity(granularity) {
	case domain.AdaptationGranularityFree:
		return "不要因为 source_chapters/source_range 存在就读取原文；只有缺少必要事实时才按需读取一次 source anchor"
	case domain.AdaptationGranularityArc:
		return "必要时读取 source anchors 核对主线因果；读取后仍必须写 full_rewrite 原创正文"
	default:
		return "写作前按 source_chapters 读取原文并对照事实"
	}
}

func adaptationModeWriterInstruction(granularity string) string {
	switch domain.NormalizeAdaptationGranularity(granularity) {
	case domain.AdaptationGranularityFree:
		return "当前章按 free/full_rewrite 写作：以改编提案、章节细纲和已写新剧情为准，source refs 只是背景锚点"
	case domain.AdaptationGranularityArc:
		return "当前章按 arc/full_rewrite 写作：保留主线因果与弧线功能，用新章节组织写原创正文"
	default:
		return "当前章按 chapter/preserve_details 写作：逐章对照原文，未受影响内容可承接，受影响完整场景单元原创重写"
	}
}

func adaptationBudgetWriterInstruction(granularity string) string {
	if domain.NormalizeAdaptationGranularity(granularity) == domain.AdaptationGranularityChapter {
		return "当前章按 preserve_details 执行字数硬契约；超出或不足硬区间先修正文，再重新检查。"
	}
	return "当前章按 full_rewrite 执行：word_budget 是提案规划参考而非正文硬上限；完整正文只要质量和契约通过，适度超过 max_runes 可以保留并提交，不要仅为压回预估值而重写。只有明显超过 soft_max_runes 才报告预算规划异常，后续只重规划预算，不改剧情。"
}

func adaptationLegacyRewritePolicyNotice(brief string) string {
	if strings.Contains(brief, "rewrite_policy_rule=") {
		return "brief 中的 rewrite_policy_rule 是历史模式映射说明；当前章只执行 adaptation_effective_mode.mode_contract"
	}
	return ""
}

func adaptationWordToleranceForContext(plan *domain.AdaptationPlan) any {
	if plan == nil || domain.AdaptationRewritePolicyForGranularity(plan.Granularity) != domain.AdaptationRewritePreserveDetails {
		return "disabled"
	}
	return plan.WordTolerance
}

func selectSourceReports(reports []domain.AdaptationSourceReport, refs []int) []domain.AdaptationSourceReport {
	if len(reports) == 0 || len(refs) == 0 {
		return nil
	}
	want := make(map[int]struct{}, len(refs))
	for _, ref := range refs {
		want[ref] = struct{}{}
	}
	var selected []domain.AdaptationSourceReport
	for _, report := range reports {
		if _, ok := want[report.Chapter]; ok {
			selected = append(selected, report)
		}
	}
	return selected
}

func (t *ContextTool) buildBaseContext(result map[string]any, state contextBuildState, warn func(string, error)) {
	longform := state.usesWindowedOutline()
	t.buildPremiseContext(result, longform, warn)
	t.buildOutlineContext(result, state, longform, warn)
	t.buildWorldRuleContext(result, longform, warn)
}

func (t *ContextTool) buildPremiseContext(result map[string]any, compact bool, warn func(string, error)) {
	premise, err := t.store.Outline.LoadPremise()
	if err != nil || premise == "" {
		warn("premise", err)
		return
	}
	if !compact {
		result["premise"] = truncateRunes(premise, 5000)
	}
	if sections := parsePremiseSections(premise); len(sections) > 0 {
		if compact {
			result["premise_sections"] = compactPremiseSections(sections, 600)
		} else {
			result["premise_sections"] = compactPremiseSections(sections, 1200)
		}
	}
	tier := domain.PlanningTier("")
	if meta, err := t.store.RunMeta.Load(); err == nil && meta != nil {
		tier = meta.PlanningTier
	}
	result["premise_structure"] = premiseStructure(premise, tier)
}

func (t *ContextTool) buildOutlineContext(result map[string]any, state contextBuildState, windowed bool, warn func(string, error)) {
	if !windowed {
		if outline, err := t.store.Outline.LoadOutline(); err == nil && outline != nil {
			result["outline"] = outline
		} else {
			warn("outline", err)
		}
		return
	}
	outline, err := t.loadCanonicalOutline()
	if err != nil {
		warn("outline", err)
		return
	}
	if len(outline) == 0 {
		return
	}
	from := max(state.chapter-nearbyOutlineBeforeChapters, 1)
	to := min(state.chapter+nearbyOutlineAfterChapters, len(outline))
	nearby := compactOutlineEntries(outlineEntriesInRange(outline, from, to))
	if len(nearby) > 0 {
		result["nearby_outline"] = nearby
	}
	result["outline_scope"] = map[string]any{
		"mode":                 "windowed",
		"chapter":              state.chapter,
		"from":                 from,
		"to":                   to,
		"total_chapters":       len(outline),
		"full_outline_omitted": true,
	}
	t.attachCurrentArcOutline(result, state, outline)
}

func (t *ContextTool) buildWorldRuleContext(result map[string]any, compact bool, warn func(string, error)) {
	rules, err := t.store.World.LoadWorldRules()
	if err != nil || len(rules) == 0 {
		warn("world_rules", err)
		return
	}
	result["world_rules"] = compactWorldRules(rules, 40)
}

func (state contextBuildState) usesWindowedOutline() bool {
	if state.progress != nil && state.progress.TotalChapters > 50 {
		return true
	}
	return state.profile.Layered
}

func compactPremiseSections(sections map[string]string, maxRunes int) map[string]string {
	if len(sections) == 0 {
		return nil
	}
	out := make(map[string]string, len(sections))
	for key, value := range sections {
		out[key] = truncateRunes(value, maxRunes)
	}
	return out
}

func compactWorldRules(rules []domain.WorldRule, maxRules int) []domain.WorldRule {
	if len(rules) == 0 || maxRules <= 0 {
		return nil
	}
	limit := min(len(rules), maxRules)
	out := make([]domain.WorldRule, 0, limit)
	for _, rule := range rules[:limit] {
		rule.Rule = truncateRunes(rule.Rule, 220)
		rule.Boundary = truncateRunes(rule.Boundary, 160)
		out = append(out, rule)
	}
	return out
}

func (t *ContextTool) loadCanonicalOutline() ([]domain.OutlineEntry, error) {
	flat, flatErr := t.store.Outline.LoadOutline()
	if flatErr != nil {
		return nil, flatErr
	}
	if len(flat) > 0 {
		return normalizeOutlineEntries(flat), nil
	}
	layered, layeredErr := t.store.Outline.LoadLayeredOutline()
	if layeredErr != nil {
		return nil, layeredErr
	}
	return domain.FlattenOutline(layered), nil
}

func normalizeOutlineEntries(entries []domain.OutlineEntry) []domain.OutlineEntry {
	out := make([]domain.OutlineEntry, len(entries))
	for i, entry := range entries {
		if entry.Chapter <= 0 {
			entry.Chapter = i + 1
		}
		out[i] = entry
	}
	return out
}

func outlineEntriesInRange(entries []domain.OutlineEntry, from, to int) []domain.OutlineEntry {
	if from > to || len(entries) == 0 {
		return nil
	}
	out := make([]domain.OutlineEntry, 0, to-from+1)
	for _, entry := range entries {
		if entry.Chapter >= from && entry.Chapter <= to {
			out = append(out, entry)
		}
	}
	return out
}

func (t *ContextTool) buildOutlineRangeContext(result map[string]any, from, to int, warn func(string, error)) error {
	if from <= 0 || to <= 0 {
		return fmt.Errorf("outline_range requires positive from and to")
	}
	if from > to {
		from, to = to, from
	}
	if to-from+1 > maxOutlineRangeChapters {
		to = from + maxOutlineRangeChapters - 1
	}
	outline, err := t.loadCanonicalOutline()
	if err != nil {
		warn("outline", err)
		return nil
	}
	if len(outline) == 0 {
		result["outline"] = []domain.OutlineEntry{}
		result["outline_scope"] = map[string]any{
			"mode":           "outline_range",
			"from":           from,
			"to":             to,
			"total_chapters": 0,
		}
		return nil
	}
	if from > len(outline) {
		from = len(outline)
	}
	if to > len(outline) {
		to = len(outline)
	}
	entries := compactOutlineEntries(outlineEntriesInRange(outline, from, to))
	result["outline"] = entries
	result["outline_scope"] = map[string]any{
		"mode":                 "outline_range",
		"from":                 from,
		"to":                   to,
		"returned_chapters":    len(entries),
		"total_chapters":       len(outline),
		"full_outline_omitted": len(entries) < len(outline),
	}
	return nil
}

func (t *ContextTool) attachCurrentArcOutline(result map[string]any, state contextBuildState, fallback []domain.OutlineEntry) {
	volumes, err := t.store.Outline.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		attachFlatArcCompact(result, state, fallback)
		return
	}

	chapter := 1
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			arcStart := chapter
			chapters := make([]domain.OutlineEntry, 0, len(arc.Chapters))
			for _, entry := range arc.Chapters {
				entry.Chapter = chapter
				chapters = append(chapters, entry)
				chapter++
			}
			arcEnd := chapter - 1
			if state.chapter < arcStart || state.chapter > arcEnd {
				continue
			}
			payload := map[string]any{
				"volume":       volume.Index,
				"volume_title": volume.Title,
				"arc":          arc.Index,
				"arc_title":    arc.Title,
				"arc_goal":     arc.Goal,
				"from":         arcStart,
				"to":           arcEnd,
			}
			from := max(state.chapter-nearbyOutlineBeforeChapters, arcStart)
			to := min(state.chapter+nearbyOutlineAfterChapters, arcEnd)
			payload["chapters"] = compactOutlineEntries(outlineEntriesInRange(chapters, from, to))
			payload["total_arc_chapters"] = len(chapters)
			payload["compacted"] = true
			result["arc_outline_compact"] = payload
			return
		}
	}
	attachFlatArcCompact(result, state, fallback)
}

func attachFlatArcCompact(result map[string]any, state contextBuildState, outline []domain.OutlineEntry) {
	if len(outline) == 0 {
		return
	}
	from := max(state.chapter-nearbyOutlineBeforeChapters, 1)
	to := min(state.chapter+nearbyOutlineAfterChapters, len(outline))
	result["arc_outline_compact"] = map[string]any{
		"mode":           "flat",
		"from":           from,
		"to":             to,
		"total_chapters": len(outline),
		"chapters":       compactOutlineEntries(outlineEntriesInRange(outline, from, to)),
	}
}

func (t *ContextTool) prepareChapterContext(chapter int, envelope *chapterContextEnvelope, warn func(string, error)) contextBuildState {
	state := contextBuildState{
		chapter: chapter,
		profile: domain.NewContextProfile(0),
	}

	progress, err := t.store.Progress.Load()
	warn("progress", err)
	runMeta, err := t.store.RunMeta.Load()
	warn("run_meta", err)
	state.progress = progress
	state.runMeta = runMeta
	state.purpose = resolveChapterContextPurpose(progress, chapter)

	if runMeta != nil && runMeta.PlanningTier != "" {
		envelope.Episodic["planning_tier"] = runMeta.PlanningTier
	}
	if progress != nil && progress.TotalChapters > 0 {
		state.profile = domain.NewContextProfile(progress.TotalChapters)
	}
	if progress == nil || !progress.Layered {
		state.profile.Layered = false
	}

	currentEntry, currentEntryErr := t.store.Outline.GetChapterOutline(chapter)
	if currentEntryErr == nil {
		envelope.Working["current_chapter_outline"] = compactOutlineEntry(*currentEntry)
	} else {
		warn("current_chapter_outline", currentEntryErr)
	}
	state.currentEntry = currentEntry
	if state.purpose != chapterContextPolishing {
		t.attachFutureChapterPromises(envelope, chapter, warn)
	}

	chapterPlan, chapterPlanErr := t.store.Drafts.LoadChapterPlan(chapter)
	if chapterPlanErr == nil && chapterPlan != nil {
		compactPlan := compactChapterPlan(*chapterPlan)
		if len(chapterPlan.Contract.RequiredBeats) > 0 ||
			len(chapterPlan.Contract.ForbiddenMoves) > 0 ||
			len(chapterPlan.Contract.ContinuityChecks) > 0 ||
			len(chapterPlan.Contract.EvaluationFocus) > 0 ||
			chapterPlan.Contract.EmotionTarget != "" ||
			len(chapterPlan.Contract.PayoffPoints) > 0 ||
			chapterPlan.Contract.HookGoal != "" {
			envelope.Working["chapter_contract"] = compactChapterContract(chapterPlan.Contract)
			// Contract has a dedicated canonical field. Do not serialize it again
			// inside chapter_plan.
			compactPlan.Contract = domain.ChapterContract{}
		}
		if state.purpose != chapterContextPolishing {
			envelope.Working["chapter_plan"] = compactPlan
		}
	} else {
		warn("chapter_plan", chapterPlanErr)
	}
	state.chapterPlan = chapterPlan

	// 是否正在重写本章：决定 novel_context 是否补"重写专用"事实。
	isRewrite := progress != nil && slices.Contains(progress.PendingRewrites, chapter)

	// 暴露 draft 是否已存在的事实：让 writer 被重派时能自行判断跳过重写还是覆盖。
	// 只暴露 exists + word_count，不注入正文（正文让 writer 按需用 read_chapter 拉）。
	if _, draftWords, draftErr := t.store.Drafts.LoadChapterContent(chapter); draftErr == nil && draftWords > 0 {
		envelope.Working["chapter_draft"] = map[string]any{
			"exists":     true,
			"word_count": draftWords,
		}
	} else if draftErr != nil {
		warn("chapter_draft", draftErr)
	}

	// 重写时把"为什么改 + 改哪里"交给 writer：理由来自返工队列，具体批评来自本章评审
	// （selectReviewLessons 只召回 chapter-1..chapter-3，恰好漏掉本章本身，writer 又无读评审的工具）。
	// 正文不在此注入——保持"正文按需 read_chapter 拉"的约定不破。
	if isRewrite {
		brief := map[string]any{"reason": progress.RewriteReason}
		if review, reviewErr := t.store.World.LoadReview(chapter); reviewErr == nil && review != nil {
			if review.Summary != "" {
				brief["review_summary"] = truncateRunes(review.Summary, maxContextSummaryRunes)
			}
			if len(review.Issues) > 0 {
				brief["issues"] = compactReviewIssues(review.Issues, 5)
			}
			if len(review.ContractMisses) > 0 {
				brief["contract_misses"] = compactStringList(review.ContractMisses, maxContextContractItems, maxContextContractItemRunes)
			}
		} else if reviewErr != nil {
			warn("rewrite_review", reviewErr)
		}
		envelope.Working["rewrite_brief"] = brief
	}

	foreshadow, foreshadowErr := t.store.World.LoadActiveForeshadow()
	warn("foreshadow_ledger", foreshadowErr)
	state.foreshadow = foreshadow

	relationships, relErr := t.store.World.LoadRelationships()
	warn("relationship_state", relErr)
	if len(relationships) > 0 {
		envelope.Episodic["relationship_state"] = compactRelationshipEntries(relationships, chapter, 12)
	}
	state.relationships = relationships

	allStateChanges, scErr := t.store.World.LoadStateChanges()
	warn("recent_state_changes", scErr)
	state.allStateChanges = allStateChanges
	if len(allStateChanges) > 0 {
		start := max(chapter-2, 1)
		var recent []domain.StateChange
		for _, c := range allStateChanges {
			if c.Chapter >= start && c.Chapter < chapter {
				recent = append(recent, c)
			}
		}
		if len(recent) > 0 {
			envelope.Episodic["recent_state_changes"] = compactStateChanges(recent, 12)
		}
	}

	styleRules, styleErr := t.store.World.LoadStyleRules()
	warn("style_rules", styleErr)
	state.styleRules = styleRules
	state.storyThreads = t.selectStoryThreads(state)
	if len(state.storyThreads) > 0 && len(state.storyThreads) < storyThreadRecallMinSelected {
		state.storyThreads = nil
	}

	return state
}

const futureChapterPromiseWindow = 2

// attachFutureChapterPromises exposes only the next few story promises. This
// gives Writer and Editor an explicit ownership boundary without loading the
// full outline or forbidding legitimate foreshadowing.
func (t *ContextTool) attachFutureChapterPromises(envelope *chapterContextEnvelope, chapter int, warn func(string, error)) {
	if envelope == nil || chapter <= 0 {
		return
	}
	promises := make([]map[string]any, 0, futureChapterPromiseWindow)
	for next := chapter + 1; next <= chapter+futureChapterPromiseWindow; next++ {
		entry, err := t.store.Outline.GetChapterOutline(next)
		if err != nil {
			if next == chapter+1 {
				warn("future_chapter_promises", err)
			}
			break
		}
		compact := compactOutlineEntry(*entry)
		promises = append(promises, map[string]any{
			"chapter":    entry.Chapter,
			"title":      strings.TrimSpace(compact.Title),
			"core_event": strings.TrimSpace(compact.CoreEvent),
			"hook":       strings.TrimSpace(compact.Hook),
		})
	}
	if len(promises) > 0 {
		envelope.Working["future_chapter_promises"] = promises
	}
}

func (t *ContextTool) buildChapterContext(result map[string]any, state contextBuildState, warn func(string, error)) {
	envelope := newChapterContextEnvelope()
	result["memory_policy"] = domain.NewChapterMemoryPolicy(state.progress, state.profile, state.currentEntry != nil)

	if state.purpose == chapterContextPolishing {
		t.loadPolishingCharacters(envelope.Episodic, warn)
	} else if state.profile.Layered {
		t.loadLayeredCharacters(envelope.Episodic, state.chapter, warn)
	} else {
		t.loadFilteredCharacters(envelope.Episodic, state.chapter, warn)
	}

	if state.purpose != chapterContextPolishing {
		t.buildChapterEpisodicMemory(&envelope, state, warn)
		t.buildChapterWorkingMemory(&envelope, state, warn)
	}
	t.buildChapterReferencePack(&envelope, state)
	if state.purpose != chapterContextPolishing {
		t.buildChapterSelectedMemory(&envelope, state, warn)
	}
	t.buildStyleStats(&envelope, state)
	envelope.apply(result)
}

func (t *ContextTool) buildChapterSimulationProfile(result map[string]any, purpose chapterContextPurpose, warn func(string, error)) {
	profile, err := t.store.Simulation.Load()
	if err != nil {
		warn("simulation_profile", err)
		return
	}
	compact := t.compactSimulationProfile(profile)
	if compact == nil {
		return
	}

	// Writer preserves prose voice, sentence density and Writer-specific
	// guidance. Plot, hook and reader-retention design are owned by the signed
	// chapter contract and are not duplicated from the simulation profile.
	chapterProfile := &domain.SimulationCompactProfile{
		Version:       compact.Version,
		Mode:          compact.Mode,
		SourceCount:   compact.SourceCount,
		Style:         compact.Style,
		PacingDensity: compact.PacingDensity,
		RoleGuidance: domain.SimulationRoleGuidance{
			Writer: compact.RoleGuidance.Writer,
		},
	}
	// One representative signal from each style/pacing category is enough at
	// chapter execution time: the signed chapter contract owns plot and hook
	// design, while deterministic validators own exact prose findings.
	itemLimit := 1
	chapterProfile.Style = compactPolishingSimulationStyle(chapterProfile.Style, itemLimit)
	chapterProfile.PacingDensity = compactPolishingSimulationPacing(chapterProfile.PacingDensity, itemLimit)
	chapterProfile.RoleGuidance.Writer = compactStringList(chapterProfile.RoleGuidance.Writer, itemLimit, 60)
	working, ok := result["working_memory"].(map[string]any)
	if !ok {
		working = map[string]any{}
		result["working_memory"] = working
	}
	working["simulation_profile"] = chapterProfile
	result["simulation_profile"] = true
	if t.simulationMode == contextSimulationModeReinforced {
		result["simulation_mode"] = contextSimulationModeReinforced
	}
}

func compactPolishingSimulationStyle(style domain.SimulationStyle, maxItems int) domain.SimulationStyle {
	style.NarrativeVoice = compactStringList(style.NarrativeVoice, maxItems, 60)
	style.SentenceRhythm = compactStringList(style.SentenceRhythm, maxItems, 60)
	style.ProseTexture = compactStringList(style.ProseTexture, maxItems, 60)
	style.Perspective = compactStringList(style.Perspective, maxItems, 60)
	style.Mood = compactStringList(style.Mood, maxItems, 60)
	style.DoNotCopy = compactStringList(style.DoNotCopy, maxItems, 60)
	return style
}

func compactPolishingSimulationPacing(pacing domain.SimulationPacingDensity, maxItems int) domain.SimulationPacingDensity {
	pacing.SceneDensity = compactStringList(pacing.SceneDensity, maxItems, 60)
	pacing.InformationRelease = compactStringList(pacing.InformationRelease, maxItems, 60)
	pacing.DialogueActionRatio = compactStringList(pacing.DialogueActionRatio, maxItems, 60)
	pacing.CompressionRules = compactStringList(pacing.CompressionRules, maxItems, 60)
	return pacing
}

func (t *ContextTool) loadPolishingCharacters(result map[string]any, warn func(string, error)) {
	snapshots, err := t.store.Characters.LoadLatestSnapshots()
	if err != nil {
		warn("character_snapshots", err)
		return
	}
	if len(snapshots) > 0 {
		result["character_snapshots"] = compactCharacterSnapshots(snapshots, 8)
	}
}

// buildStyleStats 对全部已完成章节做全书级风格统计，注入 episodic_memory.style_stats。
// 弧内评审窗口对"章均几十次的句式 tic、章末形态同构、跨章复读"天然失明，只有
// 全书统计能暴露——统计归代码（确定性），裁定归 LLM（editor 在 aesthetic 维度
// 按数字判分，writer 据此自避免）。章数不足时 stylestat 返回 nil，不注入。
func (t *ContextTool) buildStyleStats(envelope *chapterContextEnvelope, state contextBuildState) {
	if state.progress == nil || len(state.progress.CompletedChapters) == 0 {
		return
	}
	completed := slices.Clone(state.progress.CompletedChapters)
	slices.Sort(completed)
	chapters := make([]string, 0, len(completed))
	for _, ch := range completed {
		// 个别章读取失败跳过：统计是 best-effort 事实，不因单章缺失放弃全书视野
		if text, err := t.store.Drafts.LoadChapterText(ch); err == nil && text != "" {
			chapters = append(chapters, text)
		}
	}

	var titles []string
	if outline, err := t.store.Outline.LoadOutline(); err == nil {
		for _, entry := range outline {
			titles = append(titles, entry.Title)
		}
	}

	stats := stylestat.Compute(stylestat.Input{
		Chapters:  chapters,
		Titles:    titles,
		Stopwords: t.styleStopwords(),
	})
	if stats == nil {
		return
	}
	envelope.Episodic["style_stats"] = stats
}

// styleStopwords 收集角色名与别名供短语挖掘过滤——出场人名天然高频，不是文风问题。
func (t *ContextTool) styleStopwords() []string {
	var words []string
	if chars, err := t.store.Characters.Load(); err == nil {
		for _, c := range chars {
			words = append(words, c.Name)
			words = append(words, c.Aliases...)
		}
	}
	if cast, err := t.store.Cast.RecentActive(50); err == nil {
		for _, e := range cast {
			words = append(words, e.Name)
			words = append(words, e.Aliases...)
		}
	}
	return words
}

func (t *ContextTool) buildChapterWorkingMemory(envelope *chapterContextEnvelope, state contextBuildState, warn func(string, error)) {
	// The current contract and nearby chapter outlines already establish the
	// structural position. Recent chapter summaries provide continuity without
	// also loading redundant volume and arc summaries.
	if summaries, err := t.store.Summaries.LoadRecentSummaries(state.chapter, min(state.profile.SummaryWindow, 4)); err == nil && len(summaries) > 0 {
		compact := compactChapterSummaries(summaries)
		if len(compact) > 3 {
			compact = compact[len(compact)-3:]
		}
		for i := range compact {
			compact[i].Summary = truncateRunes(compact[i].Summary, 120)
			compact[i].KeyEvents = compactStringList(compact[i].KeyEvents, 3, 120)
		}
		envelope.Working["recent_summaries"] = compact
	} else {
		warn("recent_summaries", err)
	}

	if state.chapter > 1 {
		if prevText, err := t.store.Drafts.LoadChapterText(state.chapter - 1); err == nil && prevText != "" {
			runes := []rune(prevText)
			if len(runes) > 600 {
				runes = runes[len(runes)-600:]
			}
			envelope.Working["previous_tail"] = string(runes)
		}
	}
}

func (t *ContextTool) buildChapterSelectedMemory(envelope *chapterContextEnvelope, state contextBuildState, warn func(string, error)) {
	if len(state.storyThreads) > 0 {
		envelope.Selected["story_threads"] = state.storyThreads
	}
	if lessons := t.selectReviewLessons(state.chapter, warn); len(lessons) > 0 {
		envelope.Selected["review_lessons"] = lessons
	}
}

func (t *ContextTool) buildChapterEpisodicMemory(envelope *chapterContextEnvelope, state contextBuildState, warn func(string, error)) {
	if len(state.foreshadow) > 0 && len(state.storyThreads) == 0 {
		envelope.Episodic["foreshadow_ledger"] = compactForeshadowEntries(state.foreshadow, 12)
	}

	// 配角名册：召回最近活跃的次要角色，让 Writer 在引入旧角色时能保持口吻/定位一致
	// 不召回所有条目（长篇会膨胀），只给最近活跃的前 N 个，按 LastSeenChapter 倒序
	_, hasSnapshots := envelope.Episodic["character_snapshots"]
	if recentCast, err := t.store.Cast.RecentActive(15); !hasSnapshots && err == nil && len(recentCast) > 0 {
		simplified := make([]map[string]any, 0, len(recentCast))
		for _, e := range recentCast {
			item := map[string]any{
				"name":             e.Name,
				"first_seen":       e.FirstSeenChapter,
				"last_seen":        e.LastSeenChapter,
				"appearance_count": e.AppearanceCount,
			}
			if e.BriefRole != "" {
				item["brief_role"] = e.BriefRole
			}
			if len(e.Aliases) > 0 {
				item["aliases"] = compactStringList(e.Aliases, 6, 40)
			}
			simplified = append(simplified, item)
		}
		envelope.Episodic["recent_cast"] = simplified
	} else if err != nil {
		warn("recent_cast", err)
	}

	if state.progress != nil && state.progress.TotalChapters > 30 && state.currentEntry != nil {
		if related := t.buildRelatedChapters(
			state.chapter,
			state.currentEntry,
			state.foreshadow,
			state.relationships,
			state.allStateChanges,
		); len(related) > 0 {
			envelope.Episodic["related_chapters"] = related
		}
	}

}

func (t *ContextTool) buildChapterReferencePack(envelope *chapterContextEnvelope, state contextBuildState) {
	if state.styleRules != nil {
		envelope.References["style_rules"] = compactWritingStyleRules(state.styleRules)
	} else {
		var maxCompleted int
		if state.progress != nil {
			maxCompleted = maxCompletedChapter(state.progress.CompletedChapters)
		}
		if anchors := t.store.Drafts.ExtractStyleAnchors(3, maxCompleted); len(anchors) > 0 {
			envelope.References["style_anchors"] = compactStringList(anchors, 3, 300)
		}

		if state.currentEntry != nil {
			var voiceSamples []map[string]any
			chars, _ := t.store.Characters.Load()
			for _, c := range chars {
				if c.Tier == "secondary" || c.Tier == "decorative" {
					continue
				}
				samples := t.store.Drafts.ExtractDialogue(c.Name, c.Aliases, 3, maxCompleted)
				if len(samples) > 0 {
					voiceSamples = append(voiceSamples, map[string]any{
						"character": c.Name,
						"samples":   compactStringList(samples, 3, 180),
					})
				}
				if len(voiceSamples) >= 5 {
					break
				}
			}
			if len(voiceSamples) > 0 {
				envelope.References["voice_samples"] = voiceSamples
			}
		}
	}

	envelope.References["references"] = t.writerReferences(state.chapter, state.purpose)
}

func (t *ContextTool) buildArchitectContext(result map[string]any, warn func(string, error)) {
	envelope := newArchitectContextEnvelope()
	result["memory_policy"] = domain.NewArchitectMemoryPolicy()
	t.buildArchitectPlanning(&envelope, warn)
	t.buildArchitectFoundation(&envelope, warn)
	t.buildArchitectReferences(&envelope, warn)
	envelope.apply(result)
}

func (t *ContextTool) buildArchitectPlanning(envelope *architectContextEnvelope, warn func(string, error)) {
	runMeta, err := t.store.RunMeta.Load()
	warn("run_meta", err)
	if runMeta != nil && runMeta.PlanningTier != "" {
		envelope.Planning["planning_tier"] = runMeta.PlanningTier
	}
	if runMeta != nil && runMeta.PlanningReview != nil {
		if contract := newCreativeBriefContract(runMeta.PlanningReview.Brief); contract != nil {
			envelope.Planning["creative_brief"] = contract
		}
	}

	var layered []domain.VolumeOutline
	progress, _ := t.store.Progress.Load()
	if l, err := t.store.Outline.LoadLayeredOutline(); err == nil && len(l) > 0 {
		layered = l
		envelope.Planning["layered_outline"] = compactLayeredOutlineForPlanning(layered, progress)
		var skeletonArcs []map[string]any
		for _, v := range layered {
			for _, a := range v.Arcs {
				if !a.IsExpanded() {
					skeletonArcs = append(skeletonArcs, map[string]any{
						"volume":             v.Index,
						"arc":                a.Index,
						"title":              truncateRunes(a.Title, 80),
						"goal":               truncateRunes(a.Goal, maxContextSummaryRunes),
						"estimated_chapters": a.EstimatedChapters,
					})
				}
			}
		}
		if len(skeletonArcs) > 0 {
			envelope.Planning["skeleton_arcs"] = skeletonArcs
		}
	} else {
		warn("layered_outline", err)
	}

	var compass *domain.StoryCompass
	if c, err := t.store.Outline.LoadCompass(); err == nil && c != nil {
		compass = c
		envelope.Planning["compass"] = compass
	} else {
		warn("compass", err)
	}
	if volSummaries, err := t.store.Summaries.LoadAllVolumeSummaries(); err == nil && len(volSummaries) > 0 {
		envelope.Planning["volume_summaries"] = compactVolumeSummaries(volSummaries, 2)
	} else {
		warn("volume_summaries", err)
	}

	// completion_signals 把"全书是否该结尾"的关键事实集中呈现，
	// 让架构师在裁定 complete_book / append_volume 时一眼看到对照面。
	// 散落在 progress / compass / foreshadow / layered_outline 里靠 LLM 脑算容易漏。
	envelope.Planning["completion_signals"] = t.completionSignals(layered, compass)
}

func (t *ContextTool) completionSignals(layered []domain.VolumeOutline, compass *domain.StoryCompass) map[string]any {
	signals := map[string]any{}
	if progress, _ := t.store.Progress.Load(); progress != nil {
		signals["completed_chapters"] = len(progress.CompletedChapters)
		signals["total_word_count"] = progress.TotalWordCount
		signals["phase"] = string(progress.Phase)
	}
	if len(layered) > 0 {
		signals["planned_chapters"] = len(domain.FlattenOutline(layered))
		signals["volumes_total"] = len(layered)
	}
	if compass != nil {
		if compass.EstimatedScale != "" {
			signals["compass_estimated_scale"] = compass.EstimatedScale
		}
		signals["open_threads_count"] = len(compass.OpenThreads)
	}
	if active, err := t.store.World.LoadActiveForeshadow(); err == nil {
		signals["active_foreshadow_count"] = len(active)
	}
	return signals
}

func (t *ContextTool) buildArchitectFoundation(envelope *architectContextEnvelope, warn func(string, error)) {
	if premise, err := t.store.Outline.LoadPremise(); err == nil && premise != "" {
		if sections := parsePremiseSections(premise); len(sections) > 0 {
			envelope.Foundation["premise_sections"] = compactPremiseSections(sections, 900)
		}
		tier := domain.PlanningTier("")
		if meta, err := t.store.RunMeta.Load(); err == nil && meta != nil {
			tier = meta.PlanningTier
		}
		envelope.Foundation["premise_structure"] = premiseStructure(premise, tier)
	} else {
		warn("premise", err)
	}

	if chars, err := t.store.Characters.Load(); err == nil && chars != nil {
		envelope.Foundation["characters"] = compactCharacters(chars, maxContextCharacters)
	} else {
		warn("characters", err)
	}

	if snapshots, err := t.store.Characters.LoadLatestSnapshots(); err == nil && len(snapshots) > 0 {
		envelope.Foundation["character_snapshots"] = compactCharacterSnapshots(snapshots, maxContextCharacterSnapshots)
	} else {
		warn("character_snapshots", err)
	}
	if rules, err := t.store.World.LoadWorldRules(); err == nil && len(rules) > 0 {
		envelope.Foundation["world_rules"] = compactWorldRules(rules, 40)
	} else {
		warn("world_rules", err)
	}
	if foreshadow, err := t.store.World.LoadActiveForeshadow(); err == nil && len(foreshadow) > 0 {
		envelope.Foundation["foreshadow_ledger"] = compactForeshadowEntries(foreshadow, maxContextForeshadowEntries)
	} else {
		warn("foreshadow_ledger", err)
	}
	envelope.Foundation["foundation_status"] = t.foundationStatus()
}

func (t *ContextTool) buildArchitectReferences(envelope *architectContextEnvelope, warn func(string, error)) {
	if styleRules, err := t.store.World.LoadStyleRules(); err == nil && styleRules != nil {
		envelope.References["style_rules"] = compactWritingStyleRules(styleRules)
	} else {
		warn("style_rules", err)
	}

	envelope.References["references"] = t.architectReferences()
}
