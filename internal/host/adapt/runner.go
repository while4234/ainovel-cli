package adapt

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

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

	if err := deps.Store.Adaptation.Reset(); err != nil {
		return fmt.Errorf("reset adaptation store: %w", err)
	}

	sources := make([]domain.AdaptationSource, 0, total)
	for i, ch := range chapters {
		source, err := deps.Store.Adaptation.SaveSourceChapter(i+1, ch.Title, ch.Content)
		if err != nil {
			return fmt.Errorf("save source chapter %d: %w", i+1, err)
		}
		sources = append(sources, source)
	}
	manifest := domain.AdaptationSourceManifest{
		SourcePath:   sourcePath,
		ChapterCount: total,
		Chapters:     sources,
	}
	if err := deps.Store.Adaptation.SaveSourceManifest(manifest); err != nil {
		return fmt.Errorf("save source manifest: %w", err)
	}

	emit(StageFoundation, 0, total, "反推原书 foundation 快照...", nil)
	fr, err := imp.ReverseFoundation(ctx, deps.LLM, deps.Prompts.Foundation, chapters)
	if err != nil {
		return fmt.Errorf("reverse source foundation: %w", err)
	}
	if err := deps.Store.Adaptation.SaveSourceFoundation(toSourceFoundation(fr)); err != nil {
		return fmt.Errorf("save source foundation: %w", err)
	}

	reports := make([]domain.AdaptationSourceReport, 0, total)
	charactersBlock := charactersBlock(fr.Characters)
	for i, ch := range chapters {
		if err := ctx.Err(); err != nil {
			return err
		}
		chapterNum := i + 1
		emit(StageChapter, chapterNum, total, fmt.Sprintf("分析原文第 %d/%d 章：%s", chapterNum, total, ch.Title), nil)
		analysis, err := imp.AnalyzeChapter(ctx, deps.LLM, deps.Prompts.Analyzer,
			chapterNum, ch.Title, ch.Content, fr.Premise, charactersBlock, nil)
		if err != nil {
			return fmt.Errorf("analyze source chapter %d: %w", chapterNum, err)
		}
		reports = append(reports, toSourceReport(chapterNum, ch.Title, analysis))
	}
	if err := deps.Store.Adaptation.SaveSourceReports(reports); err != nil {
		return fmt.Errorf("save source reports: %w", err)
	}
	emit(StageDone, total, total, fmt.Sprintf("原书分析完成：%d 章快照已保存", total), nil)
	return nil
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
		KeyEvents:      append([]string(nil), analysis.KeyEvents...),
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
