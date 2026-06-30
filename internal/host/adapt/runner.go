package adapt

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/store"
)

type Deps struct {
	Store   *store.Store
	LLM     imp.LLMChat
	Prompts Prompts
}

func RunSource(ctx context.Context, deps Deps, opts Options) (<-chan Event, error) {
	if deps.Store == nil || deps.LLM == nil {
		return nil, fmt.Errorf("deps incomplete")
	}
	if strings.TrimSpace(opts.SourcePath) == "" {
		return nil, fmt.Errorf("source path is required")
	}

	events := make(chan Event, 32)
	go func() {
		defer close(events)
		emit := func(stage Stage, current, total int, msg string, err error) {
			ev := Event{Time: time.Now(), Stage: stage, Current: current, Total: total, Message: msg, Err: err}
			select {
			case events <- ev:
			case <-ctx.Done():
			}
		}
		if err := PrepareSource(ctx, deps, opts.SourcePath, emit); err != nil {
			emit(StageError, 0, 0, "改编源书分析失败", err)
			return
		}
	}()
	return events, nil
}

func PrepareSource(ctx context.Context, deps Deps, sourcePath string, emit func(Stage, int, int, string, error)) error {
	if deps.Store == nil || deps.LLM == nil {
		return fmt.Errorf("deps incomplete")
	}
	if emit == nil {
		emit = func(Stage, int, int, string, error) {}
	}
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return fmt.Errorf("source path is required")
	}
	absPath, err := filepath.Abs(sourcePath)
	if err == nil {
		sourcePath = absPath
	}

	emit(StageSplitting, 0, 0, "切分原文章节...", nil)
	chapters, err := imp.SplitFile(sourcePath)
	if err != nil {
		return fmt.Errorf("split source: %w", err)
	}
	if len(chapters) == 0 {
		return fmt.Errorf("未识别到任何章节，请确认文件为分章小说文本")
	}
	total := len(chapters)
	emit(StageSplitting, 0, total, fmt.Sprintf("原文切分完成：%d 章", total), nil)

	manifest, sourceChanged, err := ensureSourceSnapshot(deps.Store.Adaptation, sourcePath, chapters)
	if err != nil {
		return err
	}
	if !sourceChanged {
		emit(StageSplitting, total, total, "源书快照匹配，继续使用已有分析产物", nil)
	}

	reportsChanged := false
	for i, ch := range chapters {
		if err := ctx.Err(); err != nil {
			return err
		}
		chapterNum := i + 1
		source := manifest.Chapters[i]
		existing, err := deps.Store.Adaptation.LoadSourceReport(chapterNum)
		if err != nil {
			return fmt.Errorf("load source report %d: %w", chapterNum, err)
		}
		if reusableSourceReport(existing, source.SHA256) {
			emit(StageChapter, chapterNum, total, fmt.Sprintf("跳过第 %d/%d 章，单章分析已完成：%s", chapterNum, total, ch.Title), nil)
			continue
		}
		emit(StageChapter, chapterNum, total, fmt.Sprintf("分析原文第 %d/%d 章：%s", chapterNum, total, ch.Title), nil)
		analysis, err := imp.AnalyzeChapterWithOptions(ctx, deps.LLM, deps.Prompts.Analyzer,
			chapterNum, ch.Title, ch.Content, "", "", nil,
			structuredCallOptions(StageChapter, chapterNum, total, emit))
		if err != nil {
			return fmt.Errorf("analyze source chapter %d: %w", chapterNum, err)
		}
		report := toSourceReport(chapterNum, ch.Title, analysis)
		report.SourceSHA256 = source.SHA256
		if err := deps.Store.Adaptation.SaveSourceReport(report); err != nil {
			return fmt.Errorf("save source report %d: %w", chapterNum, err)
		}
		reportsChanged = true
		if reports, err := deps.Store.Adaptation.LoadSourceReports(); err == nil {
			_ = deps.Store.Adaptation.SaveSourceReports(reports)
		}
	}
	reports, err := deps.Store.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		return fmt.Errorf("load complete source reports: %w", err)
	}
	if len(reports) != total {
		return fmt.Errorf("source reports incomplete: got %d, want %d", len(reports), total)
	}
	if err := deps.Store.Adaptation.SaveSourceReports(reports); err != nil {
		return fmt.Errorf("save source reports: %w", err)
	}
	foundation, err := deps.Store.Adaptation.LoadSourceFoundation()
	if err != nil {
		return fmt.Errorf("load source foundation: %w", err)
	}
	if foundation != nil && !sourceChanged && !reportsChanged {
		emit(StageFoundation, total, total, "源书 foundation 已存在，跳过聚合", nil)
	} else {
		emit(StageFoundation, total, total, "聚合逐章事实，生成源书 foundation...", nil)
		fr, err := imp.MergeFoundationFromReports(ctx, deps.LLM, deps.Prompts.FoundationMerge, reports,
			structuredCallOptions(StageFoundation, total, total, emit))
		if err != nil {
			return fmt.Errorf("merge source foundation: %w", err)
		}
		if err := deps.Store.Adaptation.SaveSourceFoundation(toSourceFoundation(fr)); err != nil {
			return fmt.Errorf("save source foundation: %w", err)
		}
	}
	emit(StageDone, total, total, fmt.Sprintf("原书分析完成：%d 章快照已保存", total), nil)
	return nil
}

func ensureSourceSnapshot(adaptation *store.AdaptationStore, sourcePath string, chapters []imp.Chapter) (*domain.AdaptationSourceManifest, bool, error) {
	next := buildSourceManifest(sourcePath, chapters)
	existing, err := adaptation.LoadSourceManifest()
	if err != nil {
		return nil, false, fmt.Errorf("load source manifest: %w", err)
	}
	if sourceManifestMatches(existing, next) {
		return existing, false, nil
	}

	if err := adaptation.Reset(); err != nil {
		return nil, false, fmt.Errorf("reset adaptation store: %w", err)
	}
	sources := make([]domain.AdaptationSource, 0, len(chapters))
	for i, ch := range chapters {
		source, err := adaptation.SaveSourceChapter(i+1, ch.Title, ch.Content)
		if err != nil {
			return nil, false, fmt.Errorf("save source chapter %d: %w", i+1, err)
		}
		sources = append(sources, source)
	}
	next.Chapters = sources
	if err := adaptation.SaveSourceManifest(next); err != nil {
		return nil, false, fmt.Errorf("save source manifest: %w", err)
	}
	return &next, true, nil
}

func buildSourceManifest(sourcePath string, chapters []imp.Chapter) domain.AdaptationSourceManifest {
	sources := make([]domain.AdaptationSource, 0, len(chapters))
	for i, ch := range chapters {
		content := strings.TrimSpace(ch.Content)
		chapter := i + 1
		sources = append(sources, domain.AdaptationSource{
			Chapter: chapter,
			Title:   strings.TrimSpace(ch.Title),
			SHA256:  store.TextSHA256(content),
			Path:    store.SourceChapterRelPath(chapter),
			Runes:   utf8.RuneCountInString(content),
		})
	}
	return domain.AdaptationSourceManifest{
		SourcePath:   sourcePath,
		ChapterCount: len(chapters),
		Chapters:     sources,
	}
}

func sourceManifestMatches(existing *domain.AdaptationSourceManifest, next domain.AdaptationSourceManifest) bool {
	if existing == nil || existing.ChapterCount != next.ChapterCount || len(existing.Chapters) != len(next.Chapters) {
		return false
	}
	for i := range next.Chapters {
		if existing.Chapters[i].Chapter != next.Chapters[i].Chapter || existing.Chapters[i].SHA256 != next.Chapters[i].SHA256 {
			return false
		}
	}
	return true
}

func reusableSourceReport(report *domain.AdaptationSourceReport, sourceSHA256 string) bool {
	return report != nil &&
		strings.TrimSpace(report.SourceSHA256) != "" &&
		report.SourceSHA256 == sourceSHA256 &&
		strings.TrimSpace(report.Summary) != "" &&
		len(report.KeyEvents) > 0
}

func structuredCallOptions(stage Stage, current, total int, emit func(Stage, int, int, string, error)) imp.StructuredCallOptions {
	maxTokens := 4096
	if stage == StageFoundation {
		maxTokens = 8192
	}
	return imp.StructuredCallOptions{
		MaxAttempts: 3,
		MaxTokens:   maxTokens,
		OnRetry: func(ev imp.StructuredRetryEvent) {
			if emit == nil {
				return
			}
			emit(stage, current, total, fmt.Sprintf("重试 %d/%d：%v", ev.Attempt, ev.MaxAttempts, ev.Err), ev.Err)
		},
	}
}

func PrepareRun(ctx context.Context, deps Deps, brief string) (*domain.AdaptationPlan, error) {
	brief = strings.TrimSpace(brief)
	if brief == "" {
		return nil, fmt.Errorf("adaptation brief is required")
	}
	sourceFoundation, err := deps.Store.Adaptation.LoadSourceFoundation()
	if err != nil {
		return nil, fmt.Errorf("load source foundation: %w", err)
	}
	if sourceFoundation == nil {
		return nil, fmt.Errorf("source foundation missing; import source first")
	}
	reports, err := deps.Store.Adaptation.LoadSourceReports()
	if err != nil {
		return nil, fmt.Errorf("load source reports: %w", err)
	}
	if len(reports) == 0 {
		return nil, fmt.Errorf("source reports missing; import source first")
	}

	plan := BuildPlanFromBrief(brief, reports)
	if err := deps.Store.Adaptation.SavePlan(plan); err != nil {
		return nil, fmt.Errorf("save adaptation plan: %w", err)
	}
	fr := toFoundationResult(sourceFoundation)
	fr.Premise = adaptationPremise(fr.Premise, brief, plan)
	if err := imp.PersistFoundation(ctx, deps.Store, planningTier(len(plan.Chapters)), fr); err != nil {
		return nil, fmt.Errorf("persist adaptation foundation: %w", err)
	}
	return &plan, nil
}

func BuildPlanFromBrief(brief string, reports []domain.AdaptationSourceReport) domain.AdaptationPlan {
	granularity := inferGranularity(brief)
	plan := domain.AdaptationPlan{
		Granularity: granularity,
		Brief:       strings.TrimSpace(brief),
		MainlineRules: []string{
			"保留原书核心事件的因果顺序，不凭空跳过主线转折。",
			"每章写作前先读取 source refs，对照必须保留事件和禁止偏离事项。",
			"改动关系线时必须用场景行动承接，不能破坏原书主线动机。",
		},
		RelationshipGoals: extractRelationshipGoals(brief),
		Chapters:          make([]domain.AdaptationChapterPlan, 0, len(reports)),
	}
	for _, report := range reports {
		plan.Chapters = append(plan.Chapters, domain.AdaptationChapterPlan{
			Chapter:         report.Chapter,
			Title:           report.Title,
			SourceChapters:  []int{report.Chapter},
			PreserveEvents:  append([]string(nil), report.KeyEvents...),
			RequiredChanges: []string{strings.TrimSpace(brief)},
			ForbiddenMoves: []string{
				"不要遗漏原章关键事件。",
				"不要改变原章核心因果顺序，除非 brief 明确要求。",
				"不要把原文直接同义替换成新正文。",
			},
		})
	}
	return plan
}

func inferGranularity(brief string) string {
	lower := strings.ToLower(brief)
	switch {
	case strings.Contains(lower, "free") || strings.Contains(brief, "自由") || strings.Contains(brief, "重构"):
		return domain.AdaptationGranularityFree
	case strings.Contains(lower, "arc") || strings.Contains(brief, "弧") || strings.Contains(brief, "合并") || strings.Contains(brief, "拆分"):
		return domain.AdaptationGranularityArc
	default:
		return domain.AdaptationGranularityChapter
	}
}

func extractRelationshipGoals(brief string) []string {
	brief = strings.TrimSpace(brief)
	if brief == "" {
		return nil
	}
	keywords := []string{"女主", "男主", "感情", "纯爱", "单女主", "虐", "关系", "互动"}
	for _, keyword := range keywords {
		if strings.Contains(brief, keyword) {
			return []string{brief}
		}
	}
	return nil
}

func toSourceFoundation(fr *imp.FoundationResult) domain.AdaptationSourceFoundation {
	if fr == nil {
		return domain.AdaptationSourceFoundation{}
	}
	return domain.AdaptationSourceFoundation{
		Premise:    fr.Premise,
		Characters: append([]domain.Character(nil), fr.Characters...),
		WorldRules: append([]domain.WorldRule(nil), fr.WorldRules...),
		Volumes:    append([]domain.VolumeOutline(nil), fr.Volumes...),
		Compass:    fr.Compass,
	}
}

func toFoundationResult(fr *domain.AdaptationSourceFoundation) *imp.FoundationResult {
	if fr == nil {
		return nil
	}
	return &imp.FoundationResult{
		Premise:    fr.Premise,
		Characters: append([]domain.Character(nil), fr.Characters...),
		WorldRules: append([]domain.WorldRule(nil), fr.WorldRules...),
		Volumes:    append([]domain.VolumeOutline(nil), fr.Volumes...),
		Compass:    fr.Compass,
	}
}

func toSourceReport(chapter int, title string, analysis *imp.ChapterAnalysis) domain.AdaptationSourceReport {
	if analysis == nil {
		return domain.AdaptationSourceReport{Chapter: chapter, Title: title}
	}
	return domain.AdaptationSourceReport{
		Chapter:        chapter,
		Title:          title,
		Summary:        analysis.Summary,
		Characters:     append([]string(nil), analysis.Characters...),
		CharacterFacts: append([]string(nil), analysis.CharacterFacts...),
		KeyEvents:      append([]string(nil), analysis.KeyEvents...),
		WorldRules:     append([]string(nil), analysis.WorldRules...),
		HookType:       analysis.HookType,
		DominantStrand: analysis.DominantStrand,
		Timeline:       append([]domain.TimelineEvent(nil), analysis.TimelineEvents...),
		Foreshadow:     append([]domain.ForeshadowUpdate(nil), analysis.ForeshadowUpdates...),
		Relationships:  append([]domain.RelationshipEntry(nil), analysis.RelationshipChanges...),
		StateChanges:   append([]domain.StateChange(nil), analysis.StateChanges...),
	}
}

func charactersBlock(chars []domain.Character) string {
	var sb strings.Builder
	for _, c := range chars {
		fmt.Fprintf(&sb, "- **%s**（%s）：%s\n", c.Name, c.Role, oneLine(c.Description))
	}
	return sb.String()
}

func adaptationPremise(sourcePremise, brief string, plan domain.AdaptationPlan) string {
	var sb strings.Builder
	sourcePremise = strings.TrimSpace(sourcePremise)
	if sourcePremise == "" {
		sb.WriteString("# 改编作品\n")
	} else {
		sb.WriteString(sourcePremise)
		sb.WriteString("\n")
	}
	sb.WriteString("\n## 改编契约\n\n")
	fmt.Fprintf(&sb, "- 改编粒度：%s\n", plan.Granularity)
	fmt.Fprintf(&sb, "- 用户 brief：%s\n", strings.TrimSpace(brief))
	for _, rule := range plan.MainlineRules {
		fmt.Fprintf(&sb, "- 主线规则：%s\n", rule)
	}
	return sb.String()
}

func planningTier(total int) domain.PlanningTier {
	switch {
	case total <= 25:
		return domain.PlanningTierShort
	case total <= 80:
		return domain.PlanningTierMid
	default:
		return domain.PlanningTierLong
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) > 200 {
		return string(runes[:200]) + "..."
	}
	return s
}
