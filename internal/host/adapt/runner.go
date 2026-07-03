package adapt

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
	"github.com/voocel/ainovel-cli/internal/store"
)

const DefaultWordTolerance = 0.15

const (
	adaptationPlannerPromptName          = "adaptation-planner"
	adaptationPlannerPromptVersion       = "v1"
	adaptationPlannerMaxTokens           = 8192
	adaptationPlannerSkeletonMaxTokens   = 4096
	adaptationPlannerChunkedMinChapters  = 18
	adaptationPlannerRecommendedBatchMax = 8
	adaptationPlannerRevisionBatchMax    = 8
	adaptationPlannerRepairMaxAttempts   = 2
)

var (
	targetChapterRangePattern        = regexp.MustCompile(`(\d{1,3})\s*(?:[-~～—–－至到]|\s+)\s*(\d{1,3})\s*(?:个)?(?:章节|章)`)
	targetChapterSinglePattern       = regexp.MustCompile(`(\d{1,3})\s*(多|余|左右|上下)?\s*(?:个)?(?:章节|章)`)
	targetChapterChineseLoosePattern = regexp.MustCompile(`([一二两三四五六七八九])([一二两三四五六七八九])十\s*(?:个)?(?:章节|章)`)
	targetChapterChinesePattern      = regexp.MustCompile(`([一二两三四五六七八九十百]{1,8})(多|余|左右|上下)?\s*(?:个)?(?:章节|章)`)
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
	if !sameSourcePath(existing.SourcePath, next.SourcePath) {
		return false
	}
	for i := range next.Chapters {
		if existing.Chapters[i].Chapter != next.Chapters[i].Chapter || existing.Chapters[i].SHA256 != next.Chapters[i].SHA256 {
			return false
		}
	}
	return true
}

func sameSourcePath(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return a == b
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
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
		MaxAttempts: retrypolicy.MaxAttempts,
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
	proposal, err := BuildAdaptationProposal(deps, ProposalOptions{
		Brief:         brief,
		Granularity:   inferGranularity(brief),
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	})
	if err != nil {
		return nil, err
	}
	return ConfirmAdaptationProposal(ctx, deps, *proposal)
}

func BuildPlanFromBrief(brief string, reports []domain.AdaptationSourceReport) domain.AdaptationPlan {
	return buildPlanFromInputs(ProposalOptions{
		Brief:         brief,
		Granularity:   inferGranularity(brief),
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}, reports, nil, domain.AdaptationPlanStatusConfirmed)
}

func BuildAdaptationProposal(deps Deps, opts ProposalOptions) (*domain.AdaptationPlan, error) {
	return BuildAdaptationProposalContext(context.Background(), deps, opts)
}

func BuildAdaptationProposalContext(ctx context.Context, deps Deps, opts ProposalOptions) (*domain.AdaptationPlan, error) {
	if deps.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	opts.Brief = strings.TrimSpace(opts.Brief)
	if opts.Brief == "" {
		return nil, fmt.Errorf("adaptation brief is required")
	}
	granularity, ok := domain.StrictAdaptationGranularity(opts.Granularity)
	if !ok {
		return nil, fmt.Errorf("adaptation mode must be one of chapter, arc, free")
	}
	opts.Granularity = granularity
	opts.RewritePolicy = domain.AdaptationRewritePolicyForGranularity(opts.Granularity)
	opts.WordTolerance = normalizeProposalWordTolerance(opts.Granularity, opts.WordTolerance)
	manifest, reports, err := ValidatePreparedSource(deps.Store, opts.SourcePath)
	if err != nil {
		return nil, err
	}
	sourceFoundation, err := deps.Store.Adaptation.LoadSourceFoundation()
	if err != nil {
		return nil, fmt.Errorf("load source foundation: %w", err)
	}
	if sourceFoundation == nil {
		return nil, fmt.Errorf("source foundation missing; import source first")
	}
	emitAdaptProgress(opts.EmitProgress, StagePlan, 0, 0, "改编规划准备完成，正在选择提案生成方式", nil)

	var proposal domain.AdaptationPlan
	if opts.Granularity == domain.AdaptationGranularityChapter {
		emitAdaptProgress(opts.EmitProgress, StagePlan, len(reports), len(reports), "按逐章模式生成改编提案", nil)
		proposal = buildPlanFromInputs(opts, reports, manifest, domain.AdaptationPlanStatusProposal)
	} else {
		proposal, err = buildPlanFromPlanner(ctx, deps, opts, reports, manifest, sourceFoundation)
		if err != nil {
			return nil, fmt.Errorf("build %s adaptation proposal from planner: %w", opts.Granularity, err)
		}
	}
	emitAdaptProgress(opts.EmitProgress, StagePlan, len(proposal.Chapters), len(proposal.Chapters), fmt.Sprintf("改编提案已生成，正在保存：%d 章", len(proposal.Chapters)), nil)
	if err := deps.Store.Adaptation.SaveProposal(proposal); err != nil {
		return nil, fmt.Errorf("save adaptation proposal: %w", err)
	}
	emitAdaptProgress(opts.EmitProgress, StageDone, len(proposal.Chapters), len(proposal.Chapters), fmt.Sprintf("改编提案已保存：%d 章", len(proposal.Chapters)), nil)
	return &proposal, nil
}

func ReviseAdaptationProposal(ctx context.Context, deps Deps, opts ProposalRevisionOptions) (*domain.AdaptationPlan, error) {
	return ReviseAdaptationProposalContext(ctx, deps, opts)
}

func ReviseAdaptationProposalContext(ctx context.Context, deps Deps, opts ProposalRevisionOptions) (*domain.AdaptationPlan, error) {
	if deps.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if deps.LLM == nil {
		return nil, fmt.Errorf("planner llm is required for adaptation proposal revision")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	opts.Instruction = strings.TrimSpace(opts.Instruction)
	if opts.Instruction == "" {
		return nil, fmt.Errorf("revision instruction is required")
	}
	proposal, err := deps.Store.Adaptation.LoadProposal()
	if err != nil {
		return nil, fmt.Errorf("load adaptation proposal: %w", err)
	}
	if proposal == nil || len(proposal.Chapters) == 0 {
		return nil, fmt.Errorf("adaptation proposal is required")
	}
	from, to, err := resolveProposalRevisionRange(*proposal, opts)
	if err != nil {
		return nil, err
	}
	manifest, reports, err := ValidatePreparedSource(deps.Store, "")
	if err != nil {
		return nil, err
	}
	sourceFoundation, err := deps.Store.Adaptation.LoadSourceFoundation()
	if err != nil {
		return nil, fmt.Errorf("load source foundation: %w", err)
	}
	if sourceFoundation == nil {
		return nil, fmt.Errorf("source foundation missing; analyze source first")
	}
	emitAdaptProgress(opts.EmitProgress, StagePlan, from, to, fmt.Sprintf("准备修订改编提案：第 %d-%d 章", from, to), nil)

	systemPrompt := strings.TrimSpace(deps.Prompts.Planner)
	if systemPrompt == "" {
		systemPrompt = "# Adaptation Planner\n\nReturn only JSON for the requested proposal revision."
	}
	updated := cloneAdaptationPlan(*proposal)
	updated.Volumes = normalizeAdaptationProposalVolumes(updated.Volumes, len(updated.Chapters))
	totalBatches := revisionBatchCount(from, to, adaptationPlannerRevisionBatchMax)
	batchOrdinal := 0
	for chunkFrom := from; chunkFrom <= to; chunkFrom += adaptationPlannerRevisionBatchMax {
		chunkTo := min(to, chunkFrom+adaptationPlannerRevisionBatchMax-1)
		batchOrdinal++
		batch := proposalRevisionBatch(updated, chunkFrom, chunkTo)
		revisionPrompt, err := buildAdaptationProposalRevisionUserPrompt(opts, updated, batch, manifest, sourceFoundation, reportsForPlannerBatch(reports, batch))
		if err != nil {
			return nil, err
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal, totalBatches, fmt.Sprintf("请求修订第 %d/%d 批：第 %d-%d 章", batchOrdinal, totalBatches, chunkFrom, chunkTo), nil)
		revisionText, err := generatePlannerText(ctx, deps.LLM, systemPrompt, revisionPrompt, adaptationPlannerMaxTokens)
		if err != nil {
			return nil, fmt.Errorf("planner revision %d-%d llm generate: %w", chunkFrom, chunkTo, err)
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal, totalBatches, fmt.Sprintf("修订模型已返回第 %d/%d 批，正在解析校验", batchOrdinal, totalBatches), nil)
		revisedChapters, err := collectPlannerBatchChaptersWithRepair(
			ctx,
			deps.LLM,
			systemPrompt,
			revisionPrompt,
			revisionText,
			batch,
			opts.EmitProgress,
			batchOrdinal,
			totalBatches,
			fmt.Sprintf("修订第 %d/%d 批", batchOrdinal, totalBatches),
		)
		if err != nil {
			return nil, fmt.Errorf("planner revision %d-%d: %w", chunkFrom, chunkTo, err)
		}
		if err := replaceProposalChapterRange(&updated, chunkFrom, chunkTo, revisedChapters); err != nil {
			return nil, err
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal, totalBatches, fmt.Sprintf("修订第 %d/%d 批完成：第 %d-%d 章", batchOrdinal, totalBatches, chunkFrom, chunkTo), nil)
	}
	validateOpts := proposalOptionsFromPlan(updated)
	updated.SourceTotalRunes = 0
	updated.TargetTotalRunes = 0
	updated.TargetMinRunes = 0
	updated.TargetMaxRunes = 0
	emitAdaptProgress(opts.EmitProgress, StagePlan, totalBatches, totalBatches, "修订章节已合并，正在校验完整提案", nil)
	if err := validatePlannerProposal(&updated, validateOpts, reports, manifest, deps.LLM); err != nil {
		return nil, fmt.Errorf("revised adaptation proposal invalid: %w", err)
	}
	updated.Volumes = normalizeAdaptationProposalVolumes(updated.Volumes, len(updated.Chapters))
	if updated.Planner == nil {
		updated.Planner = &domain.AdaptationPlannerMeta{}
	}
	updated.Planner.Notes = append(updated.Planner.Notes,
		fmt.Sprintf("proposal revised for target %s (%d-%d): %s", firstNonEmptyString(strings.TrimSpace(opts.Target), fmt.Sprintf("%d-%d", from, to)), from, to, opts.Instruction),
	)
	if err := deps.Store.Adaptation.SaveProposal(updated); err != nil {
		return nil, fmt.Errorf("save revised adaptation proposal: %w", err)
	}
	emitAdaptProgress(opts.EmitProgress, StageDone, len(updated.Chapters), len(updated.Chapters), fmt.Sprintf("改编提案修订已保存：%d 章", len(updated.Chapters)), nil)
	return &updated, nil
}

func buildPlanFromPlanner(
	ctx context.Context,
	deps Deps,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
) (domain.AdaptationPlan, error) {
	targetChapterCount := normalizeTargetChapterCount(opts.TargetChapterCount, inferTargetChapterCount(opts.Brief))
	if targetChapterCount >= adaptationPlannerChunkedMinChapters {
		return buildPlanFromPlannerChunked(ctx, deps, opts, reports, manifest, sourceFoundation, targetChapterCount)
	}
	return buildPlanFromPlannerSingle(ctx, deps, opts, reports, manifest, sourceFoundation)
}

func cloneAdaptationPlan(plan domain.AdaptationPlan) domain.AdaptationPlan {
	out := plan
	out.MainlineRules = append([]string(nil), plan.MainlineRules...)
	out.RelationshipGoals = append([]string(nil), plan.RelationshipGoals...)
	out.Volumes = append([]domain.AdaptationVolumePlan(nil), plan.Volumes...)
	out.Chapters = make([]domain.AdaptationChapterPlan, len(plan.Chapters))
	for i := range plan.Chapters {
		out.Chapters[i] = cloneAdaptationChapterPlan(plan.Chapters[i])
	}
	if plan.Planner != nil {
		planner := *plan.Planner
		planner.Notes = append(domain.TextList(nil), plan.Planner.Notes...)
		out.Planner = &planner
	}
	return out
}

func cloneAdaptationChapterPlan(chapter domain.AdaptationChapterPlan) domain.AdaptationChapterPlan {
	out := chapter
	out.SourceChapters = append([]int(nil), chapter.SourceChapters...)
	out.Scenes = append([]string(nil), chapter.Scenes...)
	out.PreserveEvents = append([]string(nil), chapter.PreserveEvents...)
	out.RequiredChanges = append([]string(nil), chapter.RequiredChanges...)
	out.ForbiddenMoves = append([]string(nil), chapter.ForbiddenMoves...)
	if chapter.WordBudget != nil {
		budget := *chapter.WordBudget
		out.WordBudget = &budget
	}
	return out
}

func resolveProposalRevisionRange(proposal domain.AdaptationPlan, opts ProposalRevisionOptions) (int, int, error) {
	chapterCount := len(proposal.Chapters)
	if chapterCount == 0 {
		return 0, 0, fmt.Errorf("adaptation proposal has no chapters")
	}
	if opts.VolumeIndex > 0 {
		return revisionRangeFromVolume(proposal, opts.VolumeIndex)
	}
	if opts.VolumeIndex < 0 {
		return 1, chapterCount, nil
	}
	if opts.FromChapter > 0 || opts.ToChapter > 0 {
		from := opts.FromChapter
		to := opts.ToChapter
		if to <= 0 {
			to = from
		}
		return normalizeRevisionChapterRange(from, to, chapterCount)
	}
	target := strings.TrimSpace(opts.Target)
	if target == "" {
		return 0, 0, fmt.Errorf("revision target is required")
	}
	lower := strings.ToLower(target)
	if target == "全卷" || target == "全部卷" || strings.Contains(lower, "all volumes") || strings.Contains(lower, "all-volumes") || strings.Contains(lower, "all_volumes") {
		return 1, chapterCount, nil
	}
	if strings.Contains(target, "卷") || strings.Contains(lower, "volume") || strings.HasPrefix(lower, "vol") {
		index := parseFlexiblePositiveInt(target)
		if index <= 0 {
			return 0, 0, fmt.Errorf("revision volume target %q is invalid", target)
		}
		return revisionRangeFromVolume(proposal, index)
	}
	numbers := positiveIntsFromText(target)
	if len(numbers) == 0 {
		return 0, 0, fmt.Errorf("revision target %q must name a chapter, range, or volume", target)
	}
	from := numbers[0]
	to := from
	if len(numbers) > 1 {
		to = numbers[1]
	}
	return normalizeRevisionChapterRange(from, to, chapterCount)
}

func revisionRangeFromVolume(proposal domain.AdaptationPlan, index int) (int, int, error) {
	volumes := normalizeAdaptationProposalVolumes(proposal.Volumes, len(proposal.Chapters))
	for _, volume := range volumes {
		if volume.Index == index {
			return normalizeRevisionChapterRange(volume.TargetFrom, volume.TargetTo, len(proposal.Chapters))
		}
	}
	return 0, 0, fmt.Errorf("volume %d not found in adaptation proposal", index)
}

func normalizeRevisionChapterRange(from, to, chapterCount int) (int, int, error) {
	if from <= 0 {
		return 0, 0, fmt.Errorf("revision chapter range must start at a positive chapter")
	}
	if to <= 0 {
		to = from
	}
	if from > to {
		from, to = to, from
	}
	if to > chapterCount {
		return 0, 0, fmt.Errorf("revision chapter range %d-%d exceeds proposal chapter count %d", from, to, chapterCount)
	}
	return from, to, nil
}

func positiveIntsFromText(text string) []int {
	var numbers []int
	var token strings.Builder
	flush := func() {
		if token.Len() == 0 {
			return
		}
		if value := parseFlexiblePositiveInt(token.String()); value > 0 {
			numbers = append(numbers, value)
		}
		token.Reset()
	}
	for _, r := range text {
		if (r >= '0' && r <= '9') || isChineseChapterNumberRune(r) {
			token.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return numbers
}

func proposalRevisionBatch(plan domain.AdaptationPlan, from, to int) plannerSkeletonBatch {
	sourceFrom, sourceTo := sourceRangeForProposalChapters(plan.Chapters, from, to)
	return plannerSkeletonBatch{
		Index:              1,
		TargetFrom:         from,
		TargetTo:           to,
		TargetChapterCount: to - from + 1,
		SourceFrom:         sourceFrom,
		SourceTo:           sourceTo,
		Title:              fmt.Sprintf("revision %d-%d", from, to),
		Summary:            "revise the selected proposal chapters",
	}
}

func sourceRangeForProposalChapters(chapters []domain.AdaptationChapterPlan, from, to int) (int, int) {
	sourceFrom, sourceTo := 0, 0
	for _, chapter := range chapters {
		if chapter.Chapter < from || chapter.Chapter > to {
			continue
		}
		values := append([]int(nil), chapter.SourceChapters...)
		if chapter.SourceRange.From > 0 {
			values = append(values, chapter.SourceRange.From)
		}
		if chapter.SourceRange.To > 0 {
			values = append(values, chapter.SourceRange.To)
		}
		minSource, maxSource := minMaxPositive(values)
		if minSource > 0 && (sourceFrom == 0 || minSource < sourceFrom) {
			sourceFrom = minSource
		}
		if maxSource > sourceTo {
			sourceTo = maxSource
		}
	}
	return sourceFrom, sourceTo
}

func proposalOptionsFromPlan(plan domain.AdaptationPlan) ProposalOptions {
	granularity := domain.NormalizeAdaptationGranularity(plan.Granularity)
	return ProposalOptions{
		Brief:         strings.TrimSpace(plan.Brief),
		Granularity:   granularity,
		RewritePolicy: domain.AdaptationRewritePolicyForGranularity(granularity),
		WordTolerance: plan.WordTolerance,
	}
}

func replaceProposalChapterRange(plan *domain.AdaptationPlan, from, to int, chapters []domain.AdaptationChapterPlan) error {
	if plan == nil {
		return fmt.Errorf("proposal is nil")
	}
	if len(chapters) != to-from+1 {
		return fmt.Errorf("revised chapter count=%d, want %d", len(chapters), to-from+1)
	}
	for idx := range chapters {
		want := from + idx
		if chapters[idx].Chapter != want {
			return fmt.Errorf("revised chapter %d at index %d, want %d", chapters[idx].Chapter, idx, want)
		}
		replaced := false
		for existingIdx := range plan.Chapters {
			if plan.Chapters[existingIdx].Chapter == want {
				plan.Chapters[existingIdx] = cloneAdaptationChapterPlan(chapters[idx])
				replaced = true
				break
			}
		}
		if !replaced {
			return fmt.Errorf("proposal chapter %d not found", want)
		}
	}
	return nil
}

func buildAdaptationProposalRevisionUserPrompt(
	opts ProposalRevisionOptions,
	proposal domain.AdaptationPlan,
	batch plannerSkeletonBatch,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	reports []domain.AdaptationSourceReport,
) (string, error) {
	selected := proposalChaptersInRange(proposal.Chapters, batch.TargetFrom, batch.TargetTo)
	before := proposalChapterByNumber(proposal.Chapters, batch.TargetFrom-1)
	after := proposalChapterByNumber(proposal.Chapters, batch.TargetTo+1)
	input := struct {
		Instruction       string                             `json:"instruction"`
		TargetFrom        int                                `json:"target_from"`
		TargetTo          int                                `json:"target_to"`
		Granularity       string                             `json:"granularity"`
		RewritePolicy     string                             `json:"rewrite_policy"`
		Brief             string                             `json:"brief"`
		MainlineRules     []string                           `json:"mainline_rules,omitempty"`
		RelationshipGoals []string                           `json:"relationship_goals,omitempty"`
		Volumes           []domain.AdaptationVolumePlan      `json:"volumes,omitempty"`
		NeighborBefore    *domain.AdaptationChapterPlan      `json:"neighbor_before,omitempty"`
		NeighborAfter     *domain.AdaptationChapterPlan      `json:"neighbor_after,omitempty"`
		SelectedChapters  []domain.AdaptationChapterPlan     `json:"selected_chapters"`
		SourceManifest    *domain.AdaptationSourceManifest   `json:"source_manifest"`
		SourceFoundation  *domain.AdaptationSourceFoundation `json:"source_foundation"`
		SourceReports     []domain.AdaptationSourceReport    `json:"source_reports"`
		Requirements      []string                           `json:"requirements"`
	}{
		Instruction:       strings.TrimSpace(opts.Instruction),
		TargetFrom:        batch.TargetFrom,
		TargetTo:          batch.TargetTo,
		Granularity:       proposal.Granularity,
		RewritePolicy:     proposal.RewritePolicy,
		Brief:             proposal.Brief,
		MainlineRules:     append([]string(nil), proposal.MainlineRules...),
		RelationshipGoals: append([]string(nil), proposal.RelationshipGoals...),
		Volumes:           normalizeAdaptationProposalVolumes(proposal.Volumes, len(proposal.Chapters)),
		NeighborBefore:    before,
		NeighborAfter:     after,
		SelectedChapters:  selected,
		SourceManifest:    manifest,
		SourceFoundation:  sourceFoundation,
		SourceReports:     reports,
		Requirements: []string{
			"Return exactly one JSON object and no prose.",
			"The top-level JSON object must be {\"chapters\":[...]} and must not be a single chapter object.",
			"Return only the selected target chapters, but return the complete selected range.",
			"Do not change chapter numbers or chapter count.",
			"Use integer chapter values from target_from through target_to.",
			"Keep source_chapters anchors valid and preserve essential source events unless the user's instruction explicitly changes emphasis.",
			"Maintain continuity with neighbor_before and neighbor_after.",
			"Every returned chapter must include chapter, title, core_event, hook, scenes, source_chapters, source_range, word_budget, preserve_events, required_changes, and forbidden_moves.",
		},
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal proposal revision input: %w", err)
	}
	return fmt.Sprintf(
		"Revise the selected adaptation proposal chapters using the user's instruction. Keep the rest of the proposal unchanged.\n\n"+
			"Output contract: return exactly one JSON object, no prose and no markdown. The top-level object must be {\"chapters\":[...]}.\n"+
			"Return exactly %d chapter objects, numbered with integer chapter values from %d through %d.\n"+
			"Invalid shapes: {\"chapter\":%d,...}; {\"summary\":\"...\"}; {\"key_turns\":[...]}; markdown text outside JSON.\n\n"+
			"Revision input:\n```json\n%s\n```",
		batch.TargetTo-batch.TargetFrom+1,
		batch.TargetFrom,
		batch.TargetTo,
		batch.TargetFrom,
		string(raw),
	), nil
}

func proposalChaptersInRange(chapters []domain.AdaptationChapterPlan, from, to int) []domain.AdaptationChapterPlan {
	out := make([]domain.AdaptationChapterPlan, 0, to-from+1)
	for _, chapter := range chapters {
		if chapter.Chapter >= from && chapter.Chapter <= to {
			out = append(out, cloneAdaptationChapterPlan(chapter))
		}
	}
	return out
}

func proposalChapterByNumber(chapters []domain.AdaptationChapterPlan, number int) *domain.AdaptationChapterPlan {
	if number <= 0 {
		return nil
	}
	for _, chapter := range chapters {
		if chapter.Chapter == number {
			copy := cloneAdaptationChapterPlan(chapter)
			return &copy
		}
	}
	return nil
}

func normalizeTargetChapterCount(values ...int) int {
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if value > 120 {
			return 120
		}
		return value
	}
	return 0
}

func inferTargetChapterCount(brief string) int {
	brief = strings.TrimSpace(brief)
	if brief == "" {
		return 0
	}
	best := 0
	for _, match := range targetChapterRangePattern.FindAllStringSubmatchIndex(brief, -1) {
		from := parseRegexInt(brief, match, 1)
		to := parseRegexInt(brief, match, 2)
		if from <= 0 || to <= 0 {
			continue
		}
		if from > to {
			from, to = to, from
		}
		best = max(best, to)
	}
	for _, match := range targetChapterSinglePattern.FindAllStringSubmatchIndex(brief, -1) {
		if precededByOrdinalPrefix(brief, match[0]) {
			continue
		}
		value := parseRegexInt(brief, match, 1)
		if value <= 0 {
			continue
		}
		if parseRegexText(brief, match, 2) != "" {
			value += 5
		}
		best = max(best, value)
	}
	for _, match := range targetChapterChineseLoosePattern.FindAllStringSubmatchIndex(brief, -1) {
		if precededByOrdinalPrefix(brief, match[0]) {
			continue
		}
		high := parseChineseChapterNumber(parseRegexText(brief, match, 2) + "十")
		best = max(best, high)
	}
	for _, match := range targetChapterChinesePattern.FindAllStringSubmatchIndex(brief, -1) {
		if precededByOrdinalPrefix(brief, match[0]) {
			continue
		}
		value := parseChineseChapterNumber(parseRegexText(brief, match, 1))
		if value <= 0 {
			continue
		}
		if parseRegexText(brief, match, 2) != "" {
			value += 5
		}
		best = max(best, value)
	}
	return normalizeTargetChapterCount(best)
}

func parseRegexInt(text string, match []int, group int) int {
	value, _ := strconv.Atoi(parseRegexText(text, match, group))
	return value
}

func parseRegexText(text string, match []int, group int) string {
	offset := group * 2
	if offset+1 >= len(match) || match[offset] < 0 || match[offset+1] < 0 {
		return ""
	}
	return strings.TrimSpace(text[match[offset]:match[offset+1]])
}

func precededByOrdinalPrefix(text string, start int) bool {
	if start <= 0 || start > len(text) {
		return false
	}
	prefix := strings.TrimRightFunc(text[:start], unicode.IsSpace)
	r, _ := utf8.DecodeLastRuneInString(prefix)
	return r == '第'
}

func parseChineseChapterNumber(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	if strings.Contains(text, "百") {
		parts := strings.SplitN(text, "百", 2)
		hundreds := parseChineseDigit(parts[0])
		if hundreds <= 0 {
			hundreds = 1
		}
		return hundreds*100 + parseChineseChapterNumber(parts[1])
	}
	if strings.Contains(text, "十") {
		parts := strings.SplitN(text, "十", 2)
		tens := parseChineseDigit(parts[0])
		if tens <= 0 {
			tens = 1
		}
		return tens*10 + parseChineseDigit(parts[1])
	}
	return parseChineseDigit(text)
}

func parseChineseDigit(text string) int {
	switch strings.TrimSpace(text) {
	case "":
		return 0
	case "一":
		return 1
	case "二", "两":
		return 2
	case "三":
		return 3
	case "四":
		return 4
	case "五":
		return 5
	case "六":
		return 6
	case "七":
		return 7
	case "八":
		return 8
	case "九":
		return 9
	default:
		return 0
	}
}

func buildPlanFromPlannerSingle(
	ctx context.Context,
	deps Deps,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
) (domain.AdaptationPlan, error) {
	var zero domain.AdaptationPlan
	if deps.LLM == nil {
		return zero, fmt.Errorf("planner llm is required for %s adaptation proposals", opts.Granularity)
	}
	systemPrompt := strings.TrimSpace(deps.Prompts.Planner)
	if systemPrompt == "" {
		systemPrompt = "# Adaptation Planner\n\nReturn only one JSON adaptation plan proposal."
	}
	userPrompt, err := buildAdaptationPlannerUserPrompt(opts, reports, manifest, sourceFoundation)
	if err != nil {
		return zero, err
	}
	resp, err := deps.LLM.Generate(ctx, []agentcore.Message{
		agentcore.SystemMsg(systemPrompt),
		agentcore.UserMsg(userPrompt),
	}, nil, agentcore.WithMaxTokens(adaptationPlannerMaxTokens), agentcore.WithJSONMode())
	if err != nil {
		return zero, fmt.Errorf("planner llm generate: %w", err)
	}
	if resp == nil {
		return zero, fmt.Errorf("planner llm returned nil response")
	}
	proposal, err := parsePlannerProposal(resp.Message.TextContent())
	if err != nil {
		return zero, plannerUnusableOutputError{err: err}
	}
	if err := validatePlannerProposal(&proposal, opts, reports, manifest, deps.LLM); err != nil {
		return zero, err
	}
	return proposal, nil
}

type plannerSkeleton struct {
	Granularity        string                        `json:"granularity"`
	Status             string                        `json:"status"`
	RewritePolicy      string                        `json:"rewrite_policy"`
	Brief              string                        `json:"brief"`
	TargetChapterCount int                           `json:"target_chapter_count"`
	MainlineRules      []string                      `json:"mainline_rules,omitempty"`
	RelationshipGoals  []string                      `json:"relationship_goals,omitempty"`
	Batches            []plannerSkeletonBatch        `json:"batches"`
	Planner            *domain.AdaptationPlannerMeta `json:"planner,omitempty"`
}

type plannerSkeletonBatch struct {
	Index              int      `json:"index"`
	Title              string   `json:"title,omitempty"`
	Theme              string   `json:"theme,omitempty"`
	Goal               string   `json:"goal,omitempty"`
	Summary            string   `json:"summary,omitempty"`
	TargetFrom         int      `json:"target_from"`
	TargetTo           int      `json:"target_to"`
	TargetChapterCount int      `json:"chapter_count,omitempty"`
	SourceFrom         int      `json:"source_from"`
	SourceTo           int      `json:"source_to"`
	SourceChapters     []int    `json:"source_chapters,omitempty"`
	Notes              []string `json:"notes,omitempty"`
}

func (b *plannerSkeletonBatch) UnmarshalJSON(data []byte) error {
	type batchAlias plannerSkeletonBatch
	var raw batchAlias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*b = plannerSkeletonBatch(raw)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil
	}
	b.TargetFrom = firstJSONInt(object, b.TargetFrom, "target_from", "targetStart", "target_start", "start_chapter", "from_chapter", "first_chapter")
	b.TargetTo = firstJSONInt(object, b.TargetTo, "target_to", "targetEnd", "target_end", "end_chapter", "to_chapter", "last_chapter")
	b.TargetChapterCount = firstJSONInt(object, b.TargetChapterCount, "chapter_count", "target_chapter_count", "chapters", "count")
	b.SourceFrom = firstJSONInt(object, b.SourceFrom, "source_from", "sourceStart", "source_start", "source_chapter_from")
	b.SourceTo = firstJSONInt(object, b.SourceTo, "source_to", "sourceEnd", "source_end", "source_chapter_to")
	if rawRange := object["target_range"]; len(rawRange) > 0 {
		var r struct {
			From  int `json:"from"`
			To    int `json:"to"`
			Start int `json:"start"`
			End   int `json:"end"`
		}
		if err := json.Unmarshal(rawRange, &r); err == nil {
			if b.TargetFrom <= 0 {
				b.TargetFrom = firstPositiveInt(r.From, r.Start)
			}
			if b.TargetTo <= 0 {
				b.TargetTo = firstPositiveInt(r.To, r.End)
			}
		}
	}
	if rawRange := object["source_range"]; len(rawRange) > 0 {
		var r struct {
			From  int `json:"from"`
			To    int `json:"to"`
			Start int `json:"start"`
			End   int `json:"end"`
		}
		if err := json.Unmarshal(rawRange, &r); err == nil {
			if b.SourceFrom <= 0 {
				b.SourceFrom = firstPositiveInt(r.From, r.Start)
			}
			if b.SourceTo <= 0 {
				b.SourceTo = firstPositiveInt(r.To, r.End)
			}
		}
	}
	return nil
}

func buildPlanFromPlannerChunked(
	ctx context.Context,
	deps Deps,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	targetChapterHint int,
) (domain.AdaptationPlan, error) {
	var zero domain.AdaptationPlan
	if deps.LLM == nil {
		return zero, fmt.Errorf("planner llm is required for %s adaptation proposals", opts.Granularity)
	}
	systemPrompt := strings.TrimSpace(deps.Prompts.Planner)
	if systemPrompt == "" {
		systemPrompt = "# Adaptation Planner\n\nReturn only JSON for the requested adaptation planning step."
	}
	skeletonPrompt, err := buildAdaptationPlannerSkeletonUserPrompt(opts, reports, manifest, sourceFoundation, targetChapterHint)
	if err != nil {
		return zero, err
	}
	emitAdaptProgress(opts.EmitProgress, StagePlan, 0, 0, fmt.Sprintf("请求长篇改编骨架规划：目标约 %d 章", targetChapterHint), nil)
	skeletonText, err := generatePlannerText(ctx, deps.LLM, systemPrompt, skeletonPrompt, adaptationPlannerSkeletonMaxTokens)
	if err != nil {
		return zero, fmt.Errorf("planner skeleton llm generate: %w", err)
	}
	emitAdaptProgress(opts.EmitProgress, StagePlan, 0, 0, "长篇骨架模型已返回，正在解析分卷/分批结构", nil)
	var skeleton plannerSkeleton
	for attempt := 0; ; attempt++ {
		skeleton, err = parsePlannerSkeleton(skeletonText)
		if err == nil {
			err = normalizePlannerSkeleton(&skeleton, opts, manifest, targetChapterHint)
		}
		if err == nil {
			break
		}
		if !plannerSkeletonErrorRepairable(err) || attempt >= adaptationPlannerRepairMaxAttempts {
			return zero, fmt.Errorf("planner skeleton: %w", err)
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, attempt+1, adaptationPlannerRepairMaxAttempts, fmt.Sprintf("骨架规划返回不符合结构，正在修复第 %d 次：%v", attempt+1, err), err)
		skeletonText, err = repairPlannerSkeletonText(ctx, deps.LLM, systemPrompt, skeletonPrompt, skeletonText, err)
		if err != nil {
			return zero, fmt.Errorf("planner skeleton: %w", err)
		}
	}
	detailBatches := plannerDetailBatches(skeleton.Batches, adaptationPlannerRecommendedBatchMax)
	emitAdaptProgress(opts.EmitProgress, StagePlan, 0, len(detailBatches), fmt.Sprintf("骨架规划完成：%d 章，%d 个模型规划段，拆为 %d 个详情子批次", skeleton.TargetChapterCount, len(skeleton.Batches), len(detailBatches)), nil)

	chapters := make([]domain.AdaptationChapterPlan, 0, skeleton.TargetChapterCount)
	for batchOrdinal, batch := range detailBatches {
		batchPrompt, err := buildAdaptationPlannerBatchUserPrompt(opts, manifest, sourceFoundation, skeleton, batch, reportsForPlannerBatch(reports, batch))
		if err != nil {
			return zero, err
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal+1, len(detailBatches), fmt.Sprintf("请求章节详情第 %d/%d 批：第 %d-%d 章", batchOrdinal+1, len(detailBatches), batch.TargetFrom, batch.TargetTo), nil)
		batchText, err := generatePlannerText(ctx, deps.LLM, systemPrompt, batchPrompt, adaptationPlannerMaxTokens)
		if err != nil {
			return zero, fmt.Errorf("planner batch %d llm generate: %w", batch.Index, err)
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal+1, len(detailBatches), fmt.Sprintf("章节详情第 %d/%d 批已返回，正在解析校验", batchOrdinal+1, len(detailBatches)), nil)
		batchChapters, err := collectPlannerBatchChaptersWithRepair(
			ctx,
			deps.LLM,
			systemPrompt,
			batchPrompt,
			batchText,
			batch,
			opts.EmitProgress,
			batchOrdinal+1,
			len(detailBatches),
			fmt.Sprintf("章节详情第 %d/%d 批", batchOrdinal+1, len(detailBatches)),
		)
		if err != nil {
			return zero, fmt.Errorf("planner batch %d: %w", batch.Index, err)
		}
		if len(batchChapters) == 0 {
			if err != nil {
				return zero, fmt.Errorf("planner batch %d: %w", batch.Index, err)
			}
			return zero, fmt.Errorf("planner batch %d: no chapters", batch.Index)
		}
		chapters = append(chapters, batchChapters...)
		emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal+1, len(detailBatches), fmt.Sprintf("章节详情第 %d/%d 批完成：第 %d-%d 章", batchOrdinal+1, len(detailBatches), batch.TargetFrom, batch.TargetTo), nil)
	}

	proposal := domain.AdaptationPlan{
		Granularity:       opts.Granularity,
		Status:            domain.AdaptationPlanStatusProposal,
		RewritePolicy:     opts.RewritePolicy,
		Brief:             opts.Brief,
		Volumes:           adaptationVolumesFromSkeleton(skeleton),
		WordTolerance:     opts.WordTolerance,
		MainlineRules:     append([]string(nil), skeleton.MainlineRules...),
		RelationshipGoals: append([]string(nil), skeleton.RelationshipGoals...),
		Chapters:          chapters,
		Planner:           skeleton.Planner,
	}
	if proposal.Planner == nil {
		proposal.Planner = &domain.AdaptationPlannerMeta{}
	}
	proposal.Planner.Prompt = adaptationPlannerPromptName
	proposal.Planner.PromptVersion = adaptationPlannerPromptVersion + "-chunked"
	proposal.Planner.Notes = append(proposal.Planner.Notes,
		fmt.Sprintf("chunked planner: %d target chapters across %d model-planned batches", skeleton.TargetChapterCount, len(skeleton.Batches)),
	)
	if err := validatePlannerProposal(&proposal, opts, reports, manifest, deps.LLM); err != nil {
		return zero, err
	}
	return proposal, nil
}

func plannerSkeletonErrorRepairable(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return !strings.Contains(message, "ignores requested long-form target")
}

func emitAdaptProgress(emit ProgressEmitter, stage Stage, current int, total int, msg string, err error) {
	if emit == nil {
		return
	}
	emit(stage, current, total, msg, err)
}

func revisionBatchCount(from, to, batchMax int) int {
	if batchMax <= 0 {
		batchMax = adaptationPlannerRevisionBatchMax
	}
	if from > to {
		from, to = to, from
	}
	count := to - from + 1
	if count <= 0 {
		return 0
	}
	return (count + batchMax - 1) / batchMax
}

func plannerDetailBatches(batches []plannerSkeletonBatch, batchMax int) []plannerSkeletonBatch {
	if batchMax <= 0 {
		batchMax = adaptationPlannerRecommendedBatchMax
	}
	var out []plannerSkeletonBatch
	for _, batch := range batches {
		if batch.TargetFrom <= 0 || batch.TargetTo < batch.TargetFrom {
			continue
		}
		for from := batch.TargetFrom; from <= batch.TargetTo; from += batchMax {
			to := min(batch.TargetTo, from+batchMax-1)
			sub := batch
			sub.Index = len(out) + 1
			sub.TargetFrom = from
			sub.TargetTo = to
			sub.TargetChapterCount = to - from + 1
			out = append(out, sub)
		}
	}
	return out
}

func generatePlannerText(ctx context.Context, llm imp.LLMChat, systemPrompt string, userPrompt string, maxTokens int) (string, error) {
	resp, err := llm.Generate(ctx, []agentcore.Message{
		agentcore.SystemMsg(systemPrompt),
		agentcore.UserMsg(userPrompt),
	}, nil, agentcore.WithMaxTokens(maxTokens), agentcore.WithJSONMode())
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("planner llm returned nil response")
	}
	return resp.Message.TextContent(), nil
}

func collectPlannerBatchChaptersWithRepair(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	initialText string,
	batch plannerSkeletonBatch,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
) ([]domain.AdaptationChapterPlan, error) {
	text := initialText
	var lastErr error
	for attempt := 0; ; attempt++ {
		chapters, missing, partial, parseErr := parsePlannerBatchPartial(text, batch)
		if partial && len(missing) == 0 {
			return chapters, nil
		}
		if partial && len(missing) > 0 {
			missingErr := parseErr
			if missingErr == nil {
				missingErr = fmt.Errorf("missing chapters %s for target range %d-%d", formatPlannerChapterList(missing), batch.TargetFrom, batch.TargetTo)
			}
			filled, fillErr := fillMissingPlannerBatchChapters(ctx, llm, systemPrompt, originalPrompt, text, batch, chapters, missing, missingErr, emit, current, total, label)
			if fillErr == nil {
				return filled, nil
			}
			lastErr = fillErr
		} else {
			lastErr = parseErr
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("planner batch returned no usable chapters")
		}
		if attempt >= adaptationPlannerRepairMaxAttempts {
			return nil, lastErr
		}
		emitAdaptProgress(emit, StagePlan, current, total, fmt.Sprintf("%s不能直接使用，正在整批修复第 %d/%d 次：%v", label, attempt+1, adaptationPlannerRepairMaxAttempts, lastErr), lastErr)
		repairedText, err := repairPlannerBatchText(ctx, llm, systemPrompt, originalPrompt, text, batch, lastErr)
		if err != nil {
			return nil, err
		}
		text = repairedText
	}
}

func parsePlannerBatchPartial(text string, batch plannerSkeletonBatch) ([]domain.AdaptationChapterPlan, []int, bool, error) {
	plan, err := parsePlannerProposal(text)
	if err != nil {
		return nil, nil, false, err
	}
	chapters, missing, err := normalizePlannerBatchChapterSubset(plan.Chapters, batch)
	if err == nil {
		return chapters, missing, true, nil
	}
	salvaged, salvageMissing := salvagePlannerBatchChapterSubset(plan.Chapters, batch)
	if len(salvaged) == 0 {
		return nil, nil, false, err
	}
	return salvaged, salvageMissing, true, err
}

func fillMissingPlannerBatchChapters(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	previousText string,
	batch plannerSkeletonBatch,
	existing []domain.AdaptationChapterPlan,
	missing []int,
	previousErr error,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
) ([]domain.AdaptationChapterPlan, error) {
	currentChapters := append([]domain.AdaptationChapterPlan(nil), existing...)
	currentMissing := append([]int(nil), missing...)
	feedbackText := previousText
	lastErr := previousErr
	if lastErr == nil {
		lastErr = fmt.Errorf("missing chapters %s", formatPlannerChapterList(currentMissing))
	}
	for attempt := 0; attempt < adaptationPlannerRepairMaxAttempts; attempt++ {
		emitAdaptProgress(emit, StagePlan, current, total, fmt.Sprintf("%s缺少章节 %s，正在补齐第 %d/%d 次", label, formatPlannerChapterList(currentMissing), attempt+1, adaptationPlannerRepairMaxAttempts), lastErr)
		fillText, err := repairPlannerMissingChaptersText(ctx, llm, systemPrompt, originalPrompt, feedbackText, batch, currentChapters, currentMissing, lastErr)
		if err != nil {
			lastErr = err
			feedbackText = ""
			continue
		}
		incoming, stillMissing, parseErr := parsePlannerMissingChapterResponse(fillText, batch, currentMissing)
		if len(incoming) > 0 {
			merged, mergedMissing, mergeErr := mergePlannerBatchChapterSubsets(currentChapters, incoming, batch)
			if mergeErr != nil {
				lastErr = mergeErr
			} else {
				currentChapters = merged
				if len(mergedMissing) < len(stillMissing) || len(stillMissing) == 0 {
					currentMissing = mergedMissing
				} else {
					currentMissing = stillMissing
				}
				if len(currentMissing) == 0 {
					return currentChapters, nil
				}
				lastErr = fmt.Errorf("missing chapters still %s after repair", formatPlannerChapterList(currentMissing))
			}
		} else if parseErr != nil {
			lastErr = parseErr
		} else {
			lastErr = fmt.Errorf("missing repair returned no requested chapters")
		}
		if parseErr != nil {
			lastErr = parseErr
		}
		feedbackText = fillText
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("missing chapters %s were not repaired", formatPlannerChapterList(currentMissing))
	}
	return nil, lastErr
}

func parsePlannerMissingChapterResponse(text string, batch plannerSkeletonBatch, missing []int) ([]domain.AdaptationChapterPlan, []int, error) {
	plan, err := parsePlannerProposal(text)
	if err != nil {
		return nil, append([]int(nil), missing...), err
	}
	normalized := normalizePlannerBatchChapterNumbers(plan.Chapters, batch)
	allowed := make(map[int]struct{}, len(missing))
	for _, chapter := range missing {
		allowed[chapter] = struct{}{}
	}
	found := make(map[int]domain.AdaptationChapterPlan, len(missing))
	var wrong []int
	for _, chapter := range normalized {
		if _, ok := allowed[chapter.Chapter]; !ok {
			wrong = append(wrong, chapter.Chapter)
			continue
		}
		if _, exists := found[chapter.Chapter]; exists {
			return nil, append([]int(nil), missing...), fmt.Errorf("duplicate missing chapter %d in repair response", chapter.Chapter)
		}
		found[chapter.Chapter] = chapter
	}
	accepted := sortedPlannerBatchChapters(found)
	stillMissing := make([]int, 0, len(missing))
	for _, chapter := range missing {
		if _, ok := found[chapter]; !ok {
			stillMissing = append(stillMissing, chapter)
		}
	}
	if len(accepted) == 0 {
		if len(wrong) > 0 {
			return nil, stillMissing, fmt.Errorf("missing repair returned wrong chapters %s, want %s", formatPlannerChapterList(wrong), formatPlannerChapterList(missing))
		}
		return nil, stillMissing, fmt.Errorf("missing repair returned no requested chapters, want %s", formatPlannerChapterList(missing))
	}
	if len(stillMissing) > 0 {
		return accepted, stillMissing, fmt.Errorf("missing repair returned partial chapters; still missing %s", formatPlannerChapterList(stillMissing))
	}
	return accepted, nil, nil
}

func repairPlannerSkeletonText(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	previousText string,
	previousErr error,
) (string, error) {
	repairPrompt := buildPlannerRepairPrompt("skeleton", originalPrompt, previousText, previousErr, []string{
		"Return exactly one JSON skeleton object and no prose.",
		"The JSON must have a top-level batches array.",
		"Each batch must have target_from, target_to, source_from, source_to, title, theme or goal, and summary.",
		"Do not return only overall_arc, key_turns, pair, notes, markdown, or explanation.",
	})
	text, err := generatePlannerText(ctx, llm, systemPrompt, repairPrompt, adaptationPlannerSkeletonMaxTokens)
	if err != nil {
		return "", fmt.Errorf("planner skeleton repair llm generate: %w", err)
	}
	return text, nil
}

func repairPlannerBatchText(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	previousText string,
	batch plannerSkeletonBatch,
	previousErr error,
) (string, error) {
	repairPrompt := buildPlannerRepairPrompt(
		fmt.Sprintf("batch %d", batch.Index),
		originalPrompt,
		previousText,
		previousErr,
		[]string{
			"Return exactly one JSON object and no prose.",
			fmt.Sprintf("The top-level object must be shaped exactly like {\"chapters\":[...]} with exactly chapters %d through %d.", batch.TargetFrom, batch.TargetTo),
			"Do not return a single chapter object. Do not return only the missing chapter. Return the full requested batch.",
			"Every chapter must include chapter, title, core_event, hook, scenes, source_chapters, source_range, word_budget, preserve_events, required_changes, and forbidden_moves.",
			"Do not return only summaries, key_turns, overall_arc, markdown, or explanation.",
		},
	)
	text, err := generatePlannerText(ctx, llm, systemPrompt, repairPrompt, adaptationPlannerMaxTokens)
	if err != nil {
		return "", fmt.Errorf("planner batch repair llm generate: %w", err)
	}
	return text, nil
}

func repairPlannerMissingChaptersText(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	previousText string,
	batch plannerSkeletonBatch,
	existing []domain.AdaptationChapterPlan,
	missing []int,
	previousErr error,
) (string, error) {
	input := struct {
		Step             string                         `json:"step"`
		Error            string                         `json:"error"`
		MissingChapters  []int                          `json:"missing_chapters"`
		Batch            plannerSkeletonBatch           `json:"batch"`
		ExistingChapters []domain.AdaptationChapterPlan `json:"existing_chapters"`
		PreviousOutput   string                         `json:"previous_output"`
		Requirements     []string                       `json:"requirements"`
	}{
		Step:             fmt.Sprintf("batch %d missing chapters", batch.Index),
		Error:            fmt.Sprint(previousErr),
		MissingChapters:  append([]int(nil), missing...),
		Batch:            batch,
		ExistingChapters: append([]domain.AdaptationChapterPlan(nil), existing...),
		PreviousOutput:   truncatePlannerFeedback(previousText),
		Requirements: []string{
			"Return exactly one JSON object and no prose.",
			"The top-level object must be shaped exactly like {\"chapters\":[...]}",
			"Return only the chapters listed in missing_chapters; do not repeat existing_chapters.",
			"Keep the missing chapters continuous with existing_chapters and the batch goal.",
			"Every returned chapter must include chapter, title, core_event, hook, scenes, source_chapters, source_range, word_budget, preserve_events, required_changes, and forbidden_moves.",
			"Use integer absolute target chapter numbers from missing_chapters.",
		},
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		raw = []byte(`{"error":"marshal missing chapter repair input failed"}`)
	}
	repairPrompt := "The previous planner response produced a usable partial batch but omitted required chapter plans. Fill only the missing chapters using the original planning request and the already accepted chapter plans below.\n\n" +
		"Original planning request:\n```text\n" + originalPrompt + "\n```\n\n" +
		"Missing chapter repair input:\n```json\n" + string(raw) + "\n```"
	text, err := generatePlannerText(ctx, llm, systemPrompt, repairPrompt, adaptationPlannerMaxTokens)
	if err != nil {
		return "", fmt.Errorf("planner missing chapter repair llm generate: %w", err)
	}
	return text, nil
}

func buildPlannerRepairPrompt(step string, originalPrompt string, previousText string, previousErr error, requirements []string) string {
	input := struct {
		Step           string   `json:"step"`
		Error          string   `json:"error"`
		Requirements   []string `json:"requirements"`
		PreviousOutput string   `json:"previous_output"`
	}{
		Step:           step,
		Error:          fmt.Sprint(previousErr),
		Requirements:   requirements,
		PreviousOutput: truncatePlannerFeedback(previousText),
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		raw = []byte(`{"error":"marshal repair input failed"}`)
	}
	return "The previous planner response could not be used by the application schema. Repair the response using the original planning request and the error feedback below.\n\n" +
		"Original planning request:\n```text\n" + originalPrompt + "\n```\n\n" +
		"Repair feedback:\n```json\n" + string(raw) + "\n```"
}

func truncatePlannerFeedback(text string) string {
	text = strings.TrimSpace(text)
	const maxRunes = 6000
	if len([]rune(text)) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes]) + "\n...[truncated]"
}

func buildAdaptationPlannerSkeletonUserPrompt(
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	targetChapterHint int,
) (string, error) {
	input := struct {
		Brief               string                             `json:"brief"`
		Granularity         string                             `json:"granularity"`
		RewritePolicy       string                             `json:"rewrite_policy"`
		WordTolerance       float64                            `json:"word_tolerance"`
		TargetChapterHint   int                                `json:"target_chapter_hint,omitempty"`
		RecommendedBatchMax int                                `json:"recommended_batch_max"`
		SourceManifest      *domain.AdaptationSourceManifest   `json:"source_manifest"`
		SourceFoundation    *domain.AdaptationSourceFoundation `json:"source_foundation"`
		SourceReports       []domain.AdaptationSourceReport    `json:"source_reports"`
		Requirements        []string                           `json:"requirements"`
	}{
		Brief:               opts.Brief,
		Granularity:         opts.Granularity,
		RewritePolicy:       opts.RewritePolicy,
		WordTolerance:       opts.WordTolerance,
		TargetChapterHint:   targetChapterHint,
		RecommendedBatchMax: adaptationPlannerRecommendedBatchMax,
		SourceManifest:      manifest,
		SourceFoundation:    sourceFoundation,
		SourceReports:       reports,
		Requirements: []string{
			"Return exactly one JSON skeleton object and no prose.",
			"Do not wrap the JSON in markdown fences.",
			"Do not return the final AdaptationPlan here.",
			"Do not include a chapters array in the skeleton step; chapter details are generated in later batch calls.",
			"Choose the target chapter count and divide it into model-planned batches/volumes.",
			"If target_chapter_hint is present, honor that long-form scale instead of shrinking the proposal to a short outline.",
			"Each batch must include target_from, target_to, source_from, source_to, title, theme/goal, and summary.",
			"target_from and target_to must be integers, not labels like \"第1章\".",
			"Keep each batch small enough for a later detail call, preferably no more than recommended_batch_max target chapters.",
			"All target chapter ranges must be continuous from 1 through target_chapter_count.",
			"The source ranges across batches must collectively cover every source chapter.",
		},
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal planner skeleton input: %w", err)
	}
	return "Plan the high-level long-form adaptation skeleton first. Use the current model to choose the volume/batch structure; do not mechanically split chapters.\n\n" +
		"Output contract: return exactly one JSON object, no prose and no markdown. The top-level object must contain a batches array. Do not return chapter details in this step.\n\n" +
		"Required JSON shape:\n" +
		"{\"granularity\":\"...\",\"status\":\"proposal\",\"rewrite_policy\":\"...\",\"brief\":\"...\",\"target_chapter_count\":60,\"mainline_rules\":[],\"relationship_goals\":[],\"batches\":[{\"index\":1,\"title\":\"...\",\"theme\":\"...\",\"target_from\":1,\"target_to\":8,\"source_from\":1,\"source_to\":3,\"summary\":\"...\"}]}.\n\n" +
		"Planning input:\n```json\n" + string(raw) + "\n```", nil
}

func buildAdaptationPlannerBatchUserPrompt(
	opts ProposalOptions,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	skeleton plannerSkeleton,
	batch plannerSkeletonBatch,
	reports []domain.AdaptationSourceReport,
) (string, error) {
	input := struct {
		Brief            string                             `json:"brief"`
		Granularity      string                             `json:"granularity"`
		RewritePolicy    string                             `json:"rewrite_policy"`
		WordTolerance    float64                            `json:"word_tolerance"`
		ExpectedChapters int                                `json:"expected_chapters"`
		SourceManifest   *domain.AdaptationSourceManifest   `json:"source_manifest"`
		SourceFoundation *domain.AdaptationSourceFoundation `json:"source_foundation"`
		Skeleton         plannerSkeleton                    `json:"skeleton"`
		Batch            plannerSkeletonBatch               `json:"batch"`
		SourceReports    []domain.AdaptationSourceReport    `json:"source_reports"`
		Requirements     []string                           `json:"requirements"`
	}{
		Brief:            opts.Brief,
		Granularity:      opts.Granularity,
		RewritePolicy:    opts.RewritePolicy,
		WordTolerance:    opts.WordTolerance,
		ExpectedChapters: batch.TargetTo - batch.TargetFrom + 1,
		SourceManifest:   manifest,
		SourceFoundation: sourceFoundation,
		Skeleton:         skeleton,
		Batch:            batch,
		SourceReports:    reports,
		Requirements: []string{
			"Return exactly one JSON object and no prose.",
			"The top-level JSON object must be shaped exactly like {\"chapters\":[...]} and must not be a single chapter object.",
			"Return only the chapters for the requested batch, but return the complete requested batch.",
			"chapters length must equal expected_chapters.",
			"Use absolute target chapter numbers from batch.target_from through batch.target_to.",
			"Every chapter field must be an integer absolute target chapter number, not a string label like \"第1章\".",
			"Every returned chapter must include chapter, title, core_event, hook, scenes, source_chapters, source_range, word_budget, preserve_events, required_changes, and forbidden_moves.",
			"Every source_chapters value must be within the batch source range and valid for the analyzed source.",
			"Added/bridging chapters must still include source_chapters anchors.",
			"Use the user's adaptation brief and the skeleton batch goal; do not ignore earlier user planning.",
		},
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal planner batch input: %w", err)
	}
	expected := batch.TargetTo - batch.TargetFrom + 1
	return fmt.Sprintf(
		"Expand model-planned adaptation batch %d into concrete chapter plans.\n\n"+
			"Output contract: return exactly one JSON object, no prose and no markdown. The top-level object must be {\"chapters\":[...]}.\n"+
			"Return exactly %d chapter objects, numbered with integer chapter values from %d through %d. Do not return one standalone chapter object. Do not return only a summary, outline, or key_turns.\n\n"+
			"Minimal valid shape:\n"+
			"{\"chapters\":[{\"chapter\":%d,\"title\":\"...\",\"core_event\":\"...\",\"hook\":\"...\",\"scenes\":[\"...\"],\"source_chapters\":[%d],\"source_range\":{\"from\":%d,\"to\":%d},\"word_budget\":{\"source_runes\":1000,\"target_runes\":1500,\"min_runes\":1400,\"max_runes\":1600,\"tolerance\":0.15},\"preserve_events\":[\"...\"],\"required_changes\":[\"...\"],\"forbidden_moves\":[\"...\"]}]}\n\n"+
			"Invalid shapes: {\"chapter\":%d,...}; {\"summary\":\"...\"}; {\"key_turns\":[...]}; markdown text outside JSON.\n\n"+
			"Planning input:\n```json\n%s\n```",
		batch.Index,
		expected,
		batch.TargetFrom,
		batch.TargetTo,
		batch.TargetFrom,
		batch.SourceFrom,
		batch.SourceFrom,
		batch.SourceTo,
		batch.TargetFrom,
		string(raw),
	), nil
}

func parsePlannerSkeleton(text string) (plannerSkeleton, error) {
	segments, err := extractPlannerJSONSegments(text)
	if err != nil {
		return plannerSkeleton{}, fmt.Errorf("extract planner skeleton JSON: %w", err)
	}
	var first plannerSkeleton
	var firstShape string
	var firstErr error
	for _, segment := range segments {
		skeleton, err := decodePlannerSkeletonJSON([]byte(segment))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if firstShape == "" {
			first = skeleton
			firstShape = plannerProposalShapeSummary([]byte(segment))
		}
		if len(skeleton.Batches) > 0 {
			return skeleton, nil
		}
	}
	if firstErr != nil && firstShape == "" {
		return plannerSkeleton{}, fmt.Errorf("parse planner skeleton JSON: %w", firstErr)
	}
	if firstShape != "" {
		return first, fmt.Errorf("planner skeleton has no batches (%s)", firstShape)
	}
	return plannerSkeleton{}, fmt.Errorf("planner skeleton has no decodable JSON object")
}

func decodePlannerSkeletonJSON(data []byte) (plannerSkeleton, error) {
	var skeleton plannerSkeleton
	if err := json.Unmarshal(data, &skeleton); err != nil {
		return skeleton, err
	}
	fillPlannerSkeletonAliases(data, &skeleton)
	if len(skeleton.Batches) > 0 {
		return skeleton, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return skeleton, nil
	}
	envelopeKeys := append([]string{}, plannerEnvelopeKeys...)
	envelopeKeys = append(envelopeKeys, "skeleton", "structure")
	for _, key := range envelopeKeys {
		raw := envelope[key]
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		nested, err := decodePlannerSkeletonJSON(raw)
		if err == nil && len(nested.Batches) > 0 {
			return nested, nil
		}
	}
	return skeleton, nil
}

func fillPlannerSkeletonAliases(data []byte, skeleton *plannerSkeleton) {
	if skeleton == nil {
		return
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return
	}
	skeleton.TargetChapterCount = firstJSONInt(object, skeleton.TargetChapterCount,
		"target_chapter_count", "targetChapterCount", "total_chapters", "chapter_count", "chapters_count", "target_count")
	for _, key := range []string{"batches", "chunks", "parts", "volumes", "arcs", "segments"} {
		raw := object[key]
		if len(raw) == 0 || raw[0] != '[' {
			continue
		}
		var batches []plannerSkeletonBatch
		if err := json.Unmarshal(raw, &batches); err != nil {
			continue
		}
		if len(batches) > 0 {
			skeleton.Batches = batches
			return
		}
	}
}

func normalizePlannerSkeleton(skeleton *plannerSkeleton, opts ProposalOptions, manifest *domain.AdaptationSourceManifest, targetChapterHint int) error {
	if skeleton == nil {
		return fmt.Errorf("nil skeleton")
	}
	if manifest == nil || manifest.ChapterCount <= 0 {
		return fmt.Errorf("source manifest missing")
	}
	if strings.TrimSpace(skeleton.Granularity) == "" {
		skeleton.Granularity = opts.Granularity
	}
	if strings.TrimSpace(skeleton.Status) == "" {
		skeleton.Status = domain.AdaptationPlanStatusProposal
	}
	if strings.TrimSpace(skeleton.RewritePolicy) == "" {
		skeleton.RewritePolicy = opts.RewritePolicy
	}
	skeleton.Brief = opts.Brief
	if skeleton.Granularity != opts.Granularity {
		return fmt.Errorf("granularity=%q, want %q", skeleton.Granularity, opts.Granularity)
	}
	if skeleton.Status != domain.AdaptationPlanStatusProposal {
		return fmt.Errorf("status=%q, want proposal", skeleton.Status)
	}
	if skeleton.RewritePolicy != opts.RewritePolicy {
		return fmt.Errorf("rewrite_policy=%q, want %q", skeleton.RewritePolicy, opts.RewritePolicy)
	}
	if len(skeleton.Batches) == 0 {
		return fmt.Errorf("no batches")
	}
	if skeleton.TargetChapterCount <= 0 {
		skeleton.TargetChapterCount = targetChapterHint
	}
	if targetChapterHint >= adaptationPlannerChunkedMinChapters && skeleton.TargetChapterCount > 0 {
		minAccepted := targetChapterHint * 4 / 5
		if minAccepted < adaptationPlannerChunkedMinChapters {
			minAccepted = adaptationPlannerChunkedMinChapters
		}
		if skeleton.TargetChapterCount < minAccepted {
			return fmt.Errorf("target_chapter_count=%d ignores requested long-form target %d", skeleton.TargetChapterCount, targetChapterHint)
		}
	}
	nextTarget := 1
	for idx := range skeleton.Batches {
		batch := &skeleton.Batches[idx]
		if batch.Index <= 0 {
			batch.Index = idx + 1
		}
		if batch.TargetFrom <= 0 {
			batch.TargetFrom = nextTarget
		}
		if batch.TargetTo <= 0 && batch.TargetChapterCount > 0 {
			batch.TargetTo = batch.TargetFrom + batch.TargetChapterCount - 1
		}
		if batch.TargetTo < batch.TargetFrom {
			return fmt.Errorf("batch %d has invalid target range %d-%d", batch.Index, batch.TargetFrom, batch.TargetTo)
		}
		if batch.TargetFrom != nextTarget {
			return fmt.Errorf("batch %d target range starts at %d, want %d", batch.Index, batch.TargetFrom, nextTarget)
		}
		if batch.TargetChapterCount <= 0 {
			batch.TargetChapterCount = batch.TargetTo - batch.TargetFrom + 1
		}
		if batch.TargetChapterCount != batch.TargetTo-batch.TargetFrom+1 {
			return fmt.Errorf("batch %d chapter_count conflicts with target range", batch.Index)
		}
		if batch.SourceFrom <= 0 || batch.SourceTo <= 0 {
			minSource, maxSource := minMaxPositive(batch.SourceChapters)
			if batch.SourceFrom <= 0 {
				batch.SourceFrom = minSource
			}
			if batch.SourceTo <= 0 {
				batch.SourceTo = maxSource
			}
		}
		if batch.SourceFrom <= 0 || batch.SourceTo < batch.SourceFrom || batch.SourceTo > manifest.ChapterCount {
			return fmt.Errorf("batch %d has invalid source range %d-%d", batch.Index, batch.SourceFrom, batch.SourceTo)
		}
		nextTarget = batch.TargetTo + 1
	}
	lastTarget := nextTarget - 1
	if skeleton.TargetChapterCount <= 0 {
		skeleton.TargetChapterCount = lastTarget
	}
	if lastTarget != skeleton.TargetChapterCount {
		return fmt.Errorf("batch target ranges end at %d, want target_chapter_count %d", lastTarget, skeleton.TargetChapterCount)
	}
	return nil
}

func normalizePlannerBatchChapters(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch) ([]domain.AdaptationChapterPlan, error) {
	out, missing, err := normalizePlannerBatchChapterSubset(chapters, batch)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		wantCount := batch.TargetTo - batch.TargetFrom + 1
		return nil, fmt.Errorf("chapter count=%d, want %d for target range %d-%d; missing chapters %s", len(out), wantCount, batch.TargetFrom, batch.TargetTo, formatPlannerChapterList(missing))
	}
	return out, nil
}

func normalizePlannerBatchChapterSubset(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch) ([]domain.AdaptationChapterPlan, []int, error) {
	if len(chapters) == 0 {
		return nil, nil, fmt.Errorf("no chapters")
	}
	out := normalizePlannerBatchChapterNumbers(chapters, batch)
	byChapter := make(map[int]domain.AdaptationChapterPlan, len(out))
	for _, chapter := range out {
		if chapter.Chapter < batch.TargetFrom || chapter.Chapter > batch.TargetTo {
			return nil, nil, fmt.Errorf("chapter %d outside target range %d-%d", chapter.Chapter, batch.TargetFrom, batch.TargetTo)
		}
		if _, exists := byChapter[chapter.Chapter]; exists {
			return nil, nil, fmt.Errorf("duplicate chapter %d in target range %d-%d", chapter.Chapter, batch.TargetFrom, batch.TargetTo)
		}
		byChapter[chapter.Chapter] = chapter
	}
	return sortedPlannerBatchChapters(byChapter), missingPlannerBatchChapters(byChapter, batch), nil
}

func normalizePlannerBatchChapterNumbers(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch) []domain.AdaptationChapterPlan {
	out := append([]domain.AdaptationChapterPlan(nil), chapters...)
	if batch.TargetFrom <= 1 || len(out) == 0 {
		return out
	}
	wantCount := batch.TargetTo - batch.TargetFrom + 1
	allRelative := true
	for _, chapter := range out {
		if chapter.Chapter < 1 || chapter.Chapter > wantCount {
			allRelative = false
			break
		}
	}
	if allRelative {
		offset := batch.TargetFrom - 1
		for idx := range out {
			out[idx].Chapter += offset
		}
	}
	return out
}

func sortedPlannerBatchChapters(byChapter map[int]domain.AdaptationChapterPlan) []domain.AdaptationChapterPlan {
	chapters := make([]domain.AdaptationChapterPlan, 0, len(byChapter))
	for _, chapter := range byChapter {
		chapters = append(chapters, chapter)
	}
	sort.Slice(chapters, func(i, j int) bool {
		return chapters[i].Chapter < chapters[j].Chapter
	})
	return chapters
}

func missingPlannerBatchChapters(byChapter map[int]domain.AdaptationChapterPlan, batch plannerSkeletonBatch) []int {
	var missing []int
	for chapter := batch.TargetFrom; chapter <= batch.TargetTo; chapter++ {
		if _, exists := byChapter[chapter]; !exists {
			missing = append(missing, chapter)
		}
	}
	return missing
}

func salvagePlannerBatchChapterSubset(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch) ([]domain.AdaptationChapterPlan, []int) {
	out := normalizePlannerBatchChapterNumbers(chapters, batch)
	byChapter := make(map[int]domain.AdaptationChapterPlan, len(out))
	for _, chapter := range out {
		if chapter.Chapter < batch.TargetFrom || chapter.Chapter > batch.TargetTo {
			continue
		}
		if _, exists := byChapter[chapter.Chapter]; exists {
			continue
		}
		byChapter[chapter.Chapter] = chapter
	}
	return sortedPlannerBatchChapters(byChapter), missingPlannerBatchChapters(byChapter, batch)
}

func mergePlannerBatchChapterSubsets(existing []domain.AdaptationChapterPlan, incoming []domain.AdaptationChapterPlan, batch plannerSkeletonBatch) ([]domain.AdaptationChapterPlan, []int, error) {
	current, _, err := normalizePlannerBatchChapterSubset(existing, batch)
	if err != nil {
		return nil, nil, err
	}
	next, _, err := normalizePlannerBatchChapterSubset(incoming, batch)
	if err != nil {
		return current, nil, err
	}
	byChapter := make(map[int]domain.AdaptationChapterPlan, len(current)+len(next))
	for _, chapter := range current {
		byChapter[chapter.Chapter] = chapter
	}
	for _, chapter := range next {
		if _, exists := byChapter[chapter.Chapter]; exists {
			continue
		}
		byChapter[chapter.Chapter] = chapter
	}
	merged := sortedPlannerBatchChapters(byChapter)
	return merged, missingPlannerBatchChapters(byChapter, batch), nil
}

func formatPlannerChapterList(chapters []int) string {
	if len(chapters) == 0 {
		return "[]"
	}
	parts := make([]string, len(chapters))
	for idx, chapter := range chapters {
		parts[idx] = strconv.Itoa(chapter)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func adaptationVolumesFromSkeleton(skeleton plannerSkeleton) []domain.AdaptationVolumePlan {
	volumes := make([]domain.AdaptationVolumePlan, 0, len(skeleton.Batches))
	for _, batch := range skeleton.Batches {
		title := strings.TrimSpace(batch.Title)
		theme := strings.TrimSpace(batch.Theme)
		goal := strings.TrimSpace(batch.Goal)
		summary := strings.TrimSpace(batch.Summary)
		if title == "" {
			title = fmt.Sprintf("第 %d-%d 章", batch.TargetFrom, batch.TargetTo)
		}
		volumes = append(volumes, domain.AdaptationVolumePlan{
			Index:      batch.Index,
			Title:      title,
			Theme:      theme,
			Goal:       goal,
			Summary:    summary,
			TargetFrom: batch.TargetFrom,
			TargetTo:   batch.TargetTo,
			SourceFrom: batch.SourceFrom,
			SourceTo:   batch.SourceTo,
		})
	}
	return normalizeAdaptationProposalVolumes(volumes, skeleton.TargetChapterCount)
}

func normalizeAdaptationProposalVolumes(volumes []domain.AdaptationVolumePlan, chapterCount int) []domain.AdaptationVolumePlan {
	if len(volumes) == 0 || chapterCount <= 0 {
		return nil
	}
	out := make([]domain.AdaptationVolumePlan, 0, len(volumes))
	for _, volume := range volumes {
		if volume.TargetFrom <= 0 || volume.TargetTo < volume.TargetFrom || volume.TargetTo > chapterCount {
			continue
		}
		if volume.Index <= 0 {
			volume.Index = len(out) + 1
		}
		volume.Title = strings.TrimSpace(volume.Title)
		volume.Theme = strings.TrimSpace(volume.Theme)
		volume.Goal = strings.TrimSpace(volume.Goal)
		volume.Summary = strings.TrimSpace(volume.Summary)
		if volume.Title == "" {
			volume.Title = fmt.Sprintf("第 %d-%d 章", volume.TargetFrom, volume.TargetTo)
		}
		out = append(out, volume)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TargetFrom == out[j].TargetFrom {
			return out[i].Index < out[j].Index
		}
		return out[i].TargetFrom < out[j].TargetFrom
	})
	for i := range out {
		if out[i].Index <= 0 {
			out[i].Index = i + 1
		}
	}
	return out
}

func adaptationVolumesCoverChapters(volumes []domain.AdaptationVolumePlan, chapterCount int) bool {
	if len(volumes) == 0 || chapterCount <= 0 {
		return false
	}
	next := 1
	for _, volume := range normalizeAdaptationProposalVolumes(volumes, chapterCount) {
		if volume.TargetFrom != next {
			return false
		}
		next = volume.TargetTo + 1
	}
	return next == chapterCount+1
}

func reportsForPlannerBatch(reports []domain.AdaptationSourceReport, batch plannerSkeletonBatch) []domain.AdaptationSourceReport {
	out := make([]domain.AdaptationSourceReport, 0, len(reports))
	for _, report := range reports {
		if report.Chapter >= batch.SourceFrom && report.Chapter <= batch.SourceTo {
			out = append(out, report)
		}
	}
	return out
}

func hasAnyRawKey(object map[string]json.RawMessage, keys ...string) bool {
	for _, key := range keys {
		if _, ok := object[key]; ok {
			return true
		}
	}
	return false
}

func firstJSONRaw(object map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, key := range keys {
		if raw := object[key]; len(raw) > 0 {
			return raw
		}
	}
	return nil
}

func firstJSONInt(object map[string]json.RawMessage, current int, keys ...string) int {
	if current > 0 {
		return current
	}
	for _, key := range keys {
		raw := object[key]
		if len(raw) == 0 {
			continue
		}
		var number int
		if err := json.Unmarshal(raw, &number); err == nil && number > 0 {
			return number
		}
		var floatNumber float64
		if err := json.Unmarshal(raw, &floatNumber); err == nil && floatNumber > 0 {
			return int(math.Round(floatNumber))
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			if parsed := parseFlexiblePositiveInt(text); parsed > 0 {
				return parsed
			}
		}
	}
	return current
}

func parseFlexiblePositiveInt(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	if parsed, err := strconv.Atoi(text); err == nil && parsed > 0 {
		return parsed
	}
	var digits strings.Builder
	for _, r := range text {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
			continue
		}
		if digits.Len() > 0 {
			break
		}
	}
	if digits.Len() > 0 {
		if parsed, err := strconv.Atoi(digits.String()); err == nil && parsed > 0 {
			return parsed
		}
	}
	var chinese strings.Builder
	for _, r := range text {
		if isChineseChapterNumberRune(r) {
			chinese.WriteRune(r)
			continue
		}
		if chinese.Len() > 0 {
			break
		}
	}
	if chinese.Len() > 0 {
		return parseChineseChapterNumber(chinese.String())
	}
	return 0
}

func isChineseChapterNumberRune(r rune) bool {
	switch r {
	case '一', '二', '两', '三', '四', '五', '六', '七', '八', '九', '十', '百':
		return true
	default:
		return false
	}
}

func firstJSONString(object map[string]json.RawMessage, current string, keys ...string) string {
	if strings.TrimSpace(current) != "" {
		return current
	}
	for _, key := range keys {
		raw := object[key]
		if len(raw) == 0 {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			value = strings.TrimSpace(value)
			if value != "" {
				return value
			}
		}
	}
	return current
}

func firstJSONStringArray(object map[string]json.RawMessage, current []string, keys ...string) []string {
	if len(current) > 0 {
		return current
	}
	for _, key := range keys {
		raw := object[key]
		if len(raw) == 0 {
			continue
		}
		var values []string
		if err := json.Unmarshal(raw, &values); err == nil && len(values) > 0 {
			return values
		}
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			value = strings.TrimSpace(value)
			if value != "" {
				return []string{value}
			}
		}
	}
	return current
}

func firstJSONIntArray(object map[string]json.RawMessage, current []int, keys ...string) []int {
	if len(current) > 0 {
		return current
	}
	for _, key := range keys {
		raw := object[key]
		if len(raw) == 0 {
			continue
		}
		var values []int
		if err := json.Unmarshal(raw, &values); err == nil && len(values) > 0 {
			return values
		}
		var value int
		if err := json.Unmarshal(raw, &value); err == nil && value > 0 {
			return []int{value}
		}
	}
	return current
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func minMaxPositive(values []int) (int, int) {
	minValue, maxValue := 0, 0
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if minValue == 0 || value < minValue {
			minValue = value
		}
		if value > maxValue {
			maxValue = value
		}
	}
	return minValue, maxValue
}

type plannerUnusableOutputError struct {
	err error
}

func (e plannerUnusableOutputError) Error() string {
	return e.err.Error()
}

func (e plannerUnusableOutputError) Unwrap() error {
	return e.err
}

func buildAdaptationPlannerUserPrompt(
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
) (string, error) {
	input := struct {
		Brief            string                             `json:"brief"`
		Granularity      string                             `json:"granularity"`
		RewritePolicy    string                             `json:"rewrite_policy"`
		WordTolerance    float64                            `json:"word_tolerance"`
		SourceManifest   *domain.AdaptationSourceManifest   `json:"source_manifest"`
		SourceFoundation *domain.AdaptationSourceFoundation `json:"source_foundation"`
		SourceReports    []domain.AdaptationSourceReport    `json:"source_reports"`
		Requirements     []string                           `json:"requirements"`
	}{
		Brief:            opts.Brief,
		Granularity:      opts.Granularity,
		RewritePolicy:    opts.RewritePolicy,
		WordTolerance:    opts.WordTolerance,
		SourceManifest:   manifest,
		SourceFoundation: sourceFoundation,
		SourceReports:    reports,
		Requirements: []string{
			"Return exactly one JSON AdaptationPlan object and no prose.",
			"Do not wrap the JSON in markdown fences.",
			"The top-level JSON object must contain a chapters array and must not be a single chapter object.",
			"status must be proposal; rewrite_policy must be full_rewrite for arc/free.",
			"Target chapters must be numbered continuously from 1.",
			"Every chapter field must be an integer, not a string label like \"第1章\".",
			"Every target chapter must include legal source_chapters anchors within the analyzed source range.",
			"Every source chapter must be covered by at least one target chapter.",
			"Added chapters must still include source_chapters anchors.",
			"Every chapter must include chapter, title, non-empty core_event, hook, scenes, source_chapters, source_range, word_budget, preserve_events, required_changes, and forbidden_moves.",
		},
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal planner input: %w", err)
	}
	return "Use the following analyzed source foundation and reports to plan the adaptation proposal.\n\n" +
		"Output contract: return exactly one JSON object, no prose and no markdown. The top-level object must contain a chapters array.\n" +
		"Required shape: {\"granularity\":\"...\",\"status\":\"proposal\",\"rewrite_policy\":\"...\",\"brief\":\"...\",\"chapters\":[{\"chapter\":1,\"title\":\"...\",\"core_event\":\"...\",\"hook\":\"...\",\"scenes\":[\"...\"],\"source_chapters\":[1],\"source_range\":{\"from\":1,\"to\":1},\"word_budget\":{\"source_runes\":1000,\"target_runes\":1500,\"min_runes\":1400,\"max_runes\":1600,\"tolerance\":0.15},\"preserve_events\":[\"...\"],\"required_changes\":[\"...\"],\"forbidden_moves\":[\"...\"]}]}.\n" +
		"Invalid shapes: {\"chapter\":1,...}; {\"summary\":\"...\"}; {\"key_turns\":[...]}; markdown text outside JSON.\n\n" +
		"Planning input:\n```json\n" +
		string(raw) + "\n```", nil
}

func parsePlannerProposal(text string) (domain.AdaptationPlan, error) {
	segments, err := extractPlannerJSONSegments(text)
	if err != nil {
		return domain.AdaptationPlan{}, fmt.Errorf("extract planner proposal JSON: %w", err)
	}
	var firstProposal domain.AdaptationPlan
	var firstShape string
	var firstErr error
	var looseChapters []domain.AdaptationChapterPlan
	for _, segment := range segments {
		proposal, err := decodePlannerProposalJSON([]byte(segment))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if firstShape == "" {
			firstShape = plannerProposalShapeSummary([]byte(segment))
			firstProposal = proposal
		}
		if len(proposal.Chapters) > 0 {
			return proposal, nil
		}
		if chapter, ok := decodePlannerChapterJSON([]byte(segment)); ok {
			looseChapters = append(looseChapters, chapter)
		}
	}
	if len(looseChapters) > 0 {
		return domain.AdaptationPlan{Chapters: looseChapters}, nil
	}
	if firstErr != nil && firstShape == "" {
		return domain.AdaptationPlan{}, fmt.Errorf("parse planner proposal JSON: %w", firstErr)
	}
	if firstShape != "" {
		return firstProposal, fmt.Errorf("planner proposal has no chapters (%s)", firstShape)
	}
	return domain.AdaptationPlan{}, fmt.Errorf("planner proposal has no decodable JSON object")
}

var plannerEnvelopeKeys = []string{
	"proposal",
	"adaptation_proposal",
	"adaptationProposal",
	"adaptation_plan",
	"adaptationPlan",
	"plan",
	"result",
	"data",
	"output",
	"draft",
}

var plannerChapterAliasKeys = []string{
	"chapters",
	"chapter_plans",
	"chapterPlans",
	"target_chapters",
	"targetChapters",
	"target_chapter_plans",
	"targetChapterPlans",
	"planned_chapters",
	"plannedChapters",
	"adaptation_chapters",
	"adaptationChapters",
	"adapted_chapters",
	"adaptedChapters",
	"rewrite_chapters",
	"rewriteChapters",
	"chapter_outline",
	"chapterOutline",
	"chapter_outlines",
	"chapterOutlines",
	"outline_chapters",
	"outlineChapters",
	"planned_outline",
	"plannedOutline",
	"target_outline",
	"targetOutline",
	"chapter_proposals",
	"chapterProposals",
	"sections",
	"outline",
}

func decodePlannerProposalJSON(data []byte) (domain.AdaptationPlan, error) {
	var proposal domain.AdaptationPlan
	if err := json.Unmarshal(data, &proposal); err != nil {
		return proposal, err
	}
	fillPlannerChapterAliases(data, &proposal)
	if len(proposal.Chapters) > 0 {
		return proposal, nil
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return proposal, nil
	}
	for _, key := range plannerEnvelopeKeys {
		raw := envelope[key]
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		nested, err := decodePlannerProposalJSON(raw)
		if err == nil && len(nested.Chapters) > 0 {
			return nested, nil
		}
	}
	return proposal, nil
}

func decodePlannerChapterJSON(data []byte) (domain.AdaptationChapterPlan, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || !plannerChapterObjectShape(object) {
		return domain.AdaptationChapterPlan{}, false
	}
	var chapter domain.AdaptationChapterPlan
	_ = json.Unmarshal(data, &chapter)
	fillPlannerSingleChapterAliases(object, &chapter)
	if chapter.Chapter <= 0 {
		return domain.AdaptationChapterPlan{}, false
	}
	if strings.TrimSpace(chapter.Title) == "" &&
		strings.TrimSpace(chapter.CoreEvent) == "" &&
		strings.TrimSpace(chapter.Hook) == "" &&
		len(chapter.Scenes) == 0 {
		return domain.AdaptationChapterPlan{}, false
	}
	return chapter, true
}

func plannerChapterObjectShape(object map[string]json.RawMessage) bool {
	if len(object) == 0 || !hasAnyRawKey(object, "chapter", "Chapter") {
		return false
	}
	return hasAnyRawKey(object,
		"title", "Title",
		"core_event", "coreEvent", "CoreEvent",
		"hook", "Hook",
		"scenes", "Scenes",
		"source_chapters", "sourceChapters", "SourceChapters",
		"source_range", "sourceRange", "SourceRange",
		"word_budget", "wordBudget", "WordBudget",
		"preserve_events", "preserveEvents", "PreserveEvents",
		"required_changes", "requiredChanges", "RequiredChanges",
		"forbidden_moves", "forbiddenMoves", "ForbiddenMoves",
	)
}

func fillPlannerSingleChapterAliases(object map[string]json.RawMessage, chapter *domain.AdaptationChapterPlan) {
	if chapter == nil {
		return
	}
	chapter.Chapter = firstJSONInt(object, chapter.Chapter, "chapter", "Chapter")
	chapter.Title = firstJSONString(object, chapter.Title, "title", "Title")
	chapter.CoreEvent = firstJSONString(object, chapter.CoreEvent, "core_event", "coreEvent", "CoreEvent")
	chapter.Hook = firstJSONString(object, chapter.Hook, "hook", "Hook")
	chapter.Scenes = firstJSONStringArray(object, chapter.Scenes, "scenes", "Scenes")
	chapter.SourceChapters = firstJSONIntArray(object, chapter.SourceChapters, "source_chapters", "sourceChapters", "SourceChapters")
	chapter.PreserveEvents = firstJSONStringArray(object, chapter.PreserveEvents, "preserve_events", "preserveEvents", "PreserveEvents")
	chapter.RequiredChanges = firstJSONStringArray(object, chapter.RequiredChanges, "required_changes", "requiredChanges", "RequiredChanges")
	chapter.ForbiddenMoves = firstJSONStringArray(object, chapter.ForbiddenMoves, "forbidden_moves", "forbiddenMoves", "ForbiddenMoves")
	if chapter.WordBudget == nil {
		if raw := firstJSONRaw(object, "word_budget", "wordBudget", "WordBudget"); len(raw) > 0 {
			var budget domain.AdaptationChapterWordBudget
			if err := json.Unmarshal(raw, &budget); err == nil {
				chapter.WordBudget = &budget
			}
		}
	}
	if chapter.SourceRange.From == 0 && chapter.SourceRange.To == 0 {
		if raw := firstJSONRaw(object, "source_range", "sourceRange", "SourceRange"); len(raw) > 0 {
			var sourceRange domain.SourceRange
			if err := json.Unmarshal(raw, &sourceRange); err == nil {
				chapter.SourceRange = sourceRange
			}
		}
	}
}

func fillPlannerChapterAliases(data []byte, proposal *domain.AdaptationPlan) {
	if proposal == nil || len(proposal.Chapters) > 0 {
		return
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return
	}
	for _, key := range plannerChapterAliasKeys {
		raw := object[key]
		if len(raw) == 0 || raw[0] != '[' {
			continue
		}
		var chapters []domain.AdaptationChapterPlan
		if err := json.Unmarshal(raw, &chapters); err != nil {
			continue
		}
		if len(chapters) > 0 {
			proposal.Chapters = chapters
			return
		}
	}
}

func plannerProposalShapeSummary(data []byte) string {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return "invalid object"
	}
	keys := sortedRawMessageKeys(object)
	parts := []string{"top-level keys: " + strings.Join(keys, ",")}
	for _, key := range plannerEnvelopeKeys {
		raw := object[key]
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(raw, &nested); err == nil {
			parts = append(parts, key+" keys: "+strings.Join(sortedRawMessageKeys(nested), ","))
		}
	}
	return strings.Join(parts, "; ")
}

func sortedRawMessageKeys(object map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func extractPlannerJSONSegments(text string) ([]string, error) {
	text = strings.TrimSpace(strings.TrimPrefix(strings.ToValidUTF8(text, "\uFFFD"), "\uFEFF"))
	var firstInvalid string
	var segments []string
	for start := 0; start < len(text); start++ {
		if text[start] != '{' {
			continue
		}
		end, ok := scanPlannerJSONEnd(text[start:])
		if !ok {
			continue
		}
		candidate := strings.TrimSpace(text[start : start+end])
		if json.Valid([]byte(candidate)) {
			segments = append(segments, candidate)
			continue
		}
		if firstInvalid == "" {
			firstInvalid = candidate
		}
	}
	if len(segments) > 0 {
		return segments, nil
	}
	if firstInvalid != "" {
		return []string{firstInvalid}, nil
	}
	return nil, fmt.Errorf("no complete JSON object found")
}

func scanPlannerJSONEnd(s string) (int, bool) {
	stack := []byte{'}'}
	inString := false
	escaped := false
	for i := 1; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) == 0 || c != stack[len(stack)-1] {
				return 0, false
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

func validatePlannerProposal(
	proposal *domain.AdaptationPlan,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	llm imp.LLMChat,
) error {
	if proposal == nil {
		return fmt.Errorf("planner proposal is nil")
	}
	if manifest == nil || manifest.ChapterCount <= 0 {
		return fmt.Errorf("source manifest missing")
	}
	fillMissingPlannerProposalConstants(proposal, opts)
	if strings.TrimSpace(proposal.Granularity) != opts.Granularity {
		return fmt.Errorf("planner granularity=%q, want %q", proposal.Granularity, opts.Granularity)
	}
	if strings.TrimSpace(proposal.Status) != domain.AdaptationPlanStatusProposal {
		return fmt.Errorf("planner status=%q, want proposal", proposal.Status)
	}
	if strings.TrimSpace(proposal.RewritePolicy) != opts.RewritePolicy {
		return fmt.Errorf("planner rewrite_policy=%q, want %q", proposal.RewritePolicy, opts.RewritePolicy)
	}
	if strings.TrimSpace(proposal.Brief) != opts.Brief {
		return fmt.Errorf("planner brief does not match requested brief")
	}
	if len(proposal.Chapters) == 0 {
		return fmt.Errorf("planner proposal has no chapters")
	}
	opts.WordTolerance = normalizeProposalWordTolerance(opts.Granularity, opts.WordTolerance)

	sourceRunesByChapter := sourceRunesByChapter(manifest)
	covered := make(map[int]bool, manifest.ChapterCount)
	sourceTotalRunes := 0
	for _, report := range reports {
		sourceTotalRunes += sourceRunesForReport(report, sourceRunesByChapter)
	}

	targetTotalRunes := 0
	targetMinRunes := 0
	targetMaxRunes := 0
	for i := range proposal.Chapters {
		chapter := &proposal.Chapters[i]
		if chapter.Chapter != i+1 {
			return fmt.Errorf("planner target chapters must be continuous: got chapter %d at index %d", chapter.Chapter, i)
		}
		if strings.TrimSpace(chapter.Title) == "" {
			return fmt.Errorf("planner chapter %d title is empty", chapter.Chapter)
		}
		if strings.TrimSpace(chapter.CoreEvent) == "" {
			return fmt.Errorf("planner chapter %d core_event is empty", chapter.Chapter)
		}
		if strings.TrimSpace(chapter.Hook) == "" {
			return fmt.Errorf("planner chapter %d hook is empty", chapter.Chapter)
		}
		if len(trimmedNonEmpty(chapter.Scenes)) == 0 {
			return fmt.Errorf("planner chapter %d scenes are empty", chapter.Chapter)
		}
		chapter.Scenes = trimmedNonEmpty(chapter.Scenes)
		if err := validatePlannerWordBudget(chapter, opts.WordTolerance); err != nil {
			return err
		}
		if len(chapter.SourceChapters) == 0 {
			if chapter.IsAdded {
				return fmt.Errorf("planner added chapter %d has no source anchors", chapter.Chapter)
			}
			return fmt.Errorf("planner chapter %d has no source coverage", chapter.Chapter)
		}
		minSource, maxSource := 0, 0
		seenInChapter := map[int]bool{}
		for _, sourceChapter := range chapter.SourceChapters {
			if sourceChapter <= 0 || sourceChapter > manifest.ChapterCount {
				return fmt.Errorf("planner chapter %d references invalid source chapter %d", chapter.Chapter, sourceChapter)
			}
			if seenInChapter[sourceChapter] {
				return fmt.Errorf("planner chapter %d repeats source chapter %d", chapter.Chapter, sourceChapter)
			}
			seenInChapter[sourceChapter] = true
			covered[sourceChapter] = true
			if minSource == 0 || sourceChapter < minSource {
				minSource = sourceChapter
			}
			if sourceChapter > maxSource {
				maxSource = sourceChapter
			}
		}
		if chapter.SourceRange.From == 0 && chapter.SourceRange.To == 0 {
			chapter.SourceRange = domain.SourceRange{From: minSource, To: maxSource}
		}
		if chapter.SourceRange.From <= 0 || chapter.SourceRange.To < chapter.SourceRange.From || chapter.SourceRange.To > manifest.ChapterCount {
			return fmt.Errorf("planner chapter %d has invalid source_range %d-%d", chapter.Chapter, chapter.SourceRange.From, chapter.SourceRange.To)
		}
		for _, sourceChapter := range chapter.SourceChapters {
			if sourceChapter < chapter.SourceRange.From || sourceChapter > chapter.SourceRange.To {
				return fmt.Errorf("planner chapter %d source chapter %d falls outside source_range %d-%d", chapter.Chapter, sourceChapter, chapter.SourceRange.From, chapter.SourceRange.To)
			}
		}
		targetTotalRunes += chapter.WordBudget.TargetRunes
		targetMinRunes += chapter.WordBudget.MinRunes
		targetMaxRunes += chapter.WordBudget.MaxRunes
	}
	for sourceChapter := 1; sourceChapter <= manifest.ChapterCount; sourceChapter++ {
		if !covered[sourceChapter] {
			return fmt.Errorf("planner proposal does not cover source chapter %d", sourceChapter)
		}
	}
	if err := validatePlannerProposalTotal("target_total_runes", proposal.TargetTotalRunes, targetTotalRunes); err != nil {
		return err
	}
	if err := validatePlannerProposalTotal("target_min_runes", proposal.TargetMinRunes, targetMinRunes); err != nil {
		return err
	}
	if err := validatePlannerProposalTotal("target_max_runes", proposal.TargetMaxRunes, targetMaxRunes); err != nil {
		return err
	}

	proposal.Status = domain.AdaptationPlanStatusProposal
	proposal.Granularity = opts.Granularity
	proposal.RewritePolicy = domain.AdaptationRewriteFullRewrite
	proposal.Brief = opts.Brief
	proposal.WordTolerance = opts.WordTolerance
	proposal.SourceTotalRunes = sourceTotalRunes
	proposal.TargetTotalRunes = targetTotalRunes
	proposal.TargetMinRunes = targetMinRunes
	proposal.TargetMaxRunes = targetMaxRunes
	proposal.Volumes = normalizeAdaptationProposalVolumes(proposal.Volumes, len(proposal.Chapters))
	if proposal.Planner == nil {
		proposal.Planner = &domain.AdaptationPlannerMeta{}
	}
	if strings.TrimSpace(proposal.Planner.Prompt) == "" {
		proposal.Planner.Prompt = adaptationPlannerPromptName
	}
	if strings.TrimSpace(proposal.Planner.PromptVersion) == "" {
		proposal.Planner.PromptVersion = adaptationPlannerPromptVersion
	}
	if strings.TrimSpace(proposal.Planner.GeneratedAt) == "" {
		proposal.Planner.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if strings.TrimSpace(proposal.Planner.Model) == "" {
		if namer, ok := llm.(interface{ ModelName() string }); ok {
			proposal.Planner.Model = namer.ModelName()
		}
	}
	return nil
}

func validatePlannerWordBudget(chapter *domain.AdaptationChapterPlan, tolerance float64) error {
	if chapter.WordBudget == nil {
		return fmt.Errorf("planner chapter %d word_budget is missing", chapter.Chapter)
	}
	if chapter.WordBudget.TargetRunes <= 0 {
		return fmt.Errorf("planner chapter %d word_budget.target_runes must be > 0", chapter.Chapter)
	}
	if chapter.WordBudget.MinRunes <= 0 {
		return fmt.Errorf("planner chapter %d word_budget.min_runes must be > 0", chapter.Chapter)
	}
	if chapter.WordBudget.MaxRunes < chapter.WordBudget.MinRunes {
		return fmt.Errorf("planner chapter %d word_budget max < min", chapter.Chapter)
	}
	if chapter.WordBudget.TargetRunes < chapter.WordBudget.MinRunes || chapter.WordBudget.TargetRunes > chapter.WordBudget.MaxRunes {
		return fmt.Errorf("planner chapter %d word_budget.target_runes must be within min_runes..max_runes", chapter.Chapter)
	}
	if chapter.WordBudget.Tolerance <= 0 {
		chapter.WordBudget.Tolerance = tolerance
	}
	if chapter.SourceRunes <= 0 {
		chapter.SourceRunes = chapter.WordBudget.SourceRunes
	}
	if err := validatePlannerChapterBudgetField(chapter.Chapter, "target_runes", &chapter.TargetRunes, "word_budget.target_runes", chapter.WordBudget.TargetRunes); err != nil {
		return err
	}
	if err := validatePlannerChapterBudgetField(chapter.Chapter, "target_min_runes", &chapter.TargetMinRunes, "word_budget.min_runes", chapter.WordBudget.MinRunes); err != nil {
		return err
	}
	if err := validatePlannerChapterBudgetField(chapter.Chapter, "target_max_runes", &chapter.TargetMaxRunes, "word_budget.max_runes", chapter.WordBudget.MaxRunes); err != nil {
		return err
	}
	return nil
}

func validatePlannerChapterBudgetField(chapter int, field string, legacy *int, nestedField string, nestedValue int) error {
	if legacy == nil {
		return nil
	}
	if *legacy > 0 {
		if *legacy != nestedValue {
			return fmt.Errorf("planner chapter %d %s=%d conflicts with %s=%d", chapter, field, *legacy, nestedField, nestedValue)
		}
		return nil
	}
	*legacy = nestedValue
	return nil
}

func fillMissingPlannerProposalConstants(proposal *domain.AdaptationPlan, opts ProposalOptions) {
	if proposal == nil {
		return
	}
	if strings.TrimSpace(proposal.Granularity) == "" {
		proposal.Granularity = opts.Granularity
	}
	if strings.TrimSpace(proposal.Status) == "" {
		proposal.Status = domain.AdaptationPlanStatusProposal
	}
	if strings.TrimSpace(proposal.RewritePolicy) == "" {
		proposal.RewritePolicy = opts.RewritePolicy
	}
	proposal.Brief = opts.Brief
}

func normalizeProposalWordTolerance(granularity string, wordTolerance float64) float64 {
	if domain.AdaptationRewritePolicyForGranularity(granularity) != domain.AdaptationRewritePreserveDetails {
		return 0
	}
	if wordTolerance <= 0 {
		return DefaultWordTolerance
	}
	return wordTolerance
}

func validatePlannerProposalTotal(field string, provided int, derived int) error {
	if provided > 0 && provided != derived {
		return fmt.Errorf("planner %s=%d conflicts with derived chapter total %d", field, provided, derived)
	}
	return nil
}

func trimmedNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func ValidatePreparedSource(st *store.Store, sourcePath string) (*domain.AdaptationSourceManifest, []domain.AdaptationSourceReport, error) {
	if st == nil {
		return nil, nil, fmt.Errorf("store is required")
	}
	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil {
		return nil, nil, fmt.Errorf("load source manifest: %w", err)
	}
	if manifest == nil || manifest.ChapterCount <= 0 || len(manifest.Chapters) != manifest.ChapterCount {
		return nil, nil, fmt.Errorf("source manifest missing or incomplete; analyze source first")
	}
	if sourcePath = strings.TrimSpace(sourcePath); sourcePath != "" {
		absPath, err := filepath.Abs(sourcePath)
		if err == nil {
			sourcePath = absPath
		}
		chapters, err := imp.SplitFile(sourcePath)
		if err != nil {
			return nil, nil, fmt.Errorf("split selected adaptation source: %w", err)
		}
		next := buildSourceManifest(sourcePath, chapters)
		if !sourceManifestMatches(manifest, next) {
			return nil, nil, fmt.Errorf("selected adaptation source has not been analyzed; run adaptation source analysis first")
		}
	}
	reports, err := st.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		return nil, nil, fmt.Errorf("load source reports: %w", err)
	}
	if len(reports) != manifest.ChapterCount {
		return nil, nil, fmt.Errorf("source reports incomplete or stale; analyze source first")
	}
	foundation, err := st.Adaptation.LoadSourceFoundation()
	if err != nil {
		return nil, nil, fmt.Errorf("load source foundation: %w", err)
	}
	if foundation == nil {
		return nil, nil, fmt.Errorf("source foundation missing; analyze source first")
	}
	return manifest, reports, nil
}

func ConfirmAdaptationProposal(ctx context.Context, deps Deps, proposal domain.AdaptationPlan) (*domain.AdaptationPlan, error) {
	if deps.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if strings.TrimSpace(proposal.Brief) == "" {
		return nil, fmt.Errorf("adaptation proposal brief is required")
	}
	if len(proposal.Chapters) == 0 {
		return nil, fmt.Errorf("adaptation proposal has no chapters")
	}
	sourceFoundation, err := deps.Store.Adaptation.LoadSourceFoundation()
	if err != nil {
		return nil, fmt.Errorf("load source foundation: %w", err)
	}
	if sourceFoundation == nil {
		return nil, fmt.Errorf("source foundation missing; import source first")
	}

	proposal.Status = domain.AdaptationPlanStatusConfirmed
	proposal.Granularity = domain.NormalizeAdaptationGranularity(proposal.Granularity)
	proposal.RewritePolicy = domain.AdaptationRewritePolicyForGranularity(proposal.Granularity)
	if err := deps.Store.Adaptation.SavePlan(proposal); err != nil {
		return nil, fmt.Errorf("save adaptation plan: %w", err)
	}
	_ = deps.Store.Adaptation.ClearProposal()
	fr := toFoundationResult(sourceFoundation)
	fr.Premise = adaptationPremise(fr.Premise, proposal.Brief, proposal)
	fr.Volumes = adaptationTargetVolumes(proposal)
	if fr.Compass == nil {
		fr.Compass = &domain.StoryCompass{
			EndingDirection: strings.TrimSpace(proposal.Brief),
			EstimatedScale:  fmt.Sprintf("%d chapters", len(proposal.Chapters)),
		}
	}
	if err := imp.PersistFoundation(ctx, deps.Store, planningTier(len(proposal.Chapters)), fr); err != nil {
		return nil, fmt.Errorf("persist adaptation foundation: %w", err)
	}
	return &proposal, nil
}

func adaptationTargetVolumes(plan domain.AdaptationPlan) []domain.VolumeOutline {
	entries := adaptationTargetOutline(plan)
	if len(entries) == 0 {
		return nil
	}
	volumes := normalizeAdaptationProposalVolumes(plan.Volumes, len(entries))
	if adaptationVolumesCoverChapters(volumes, len(entries)) {
		out := make([]domain.VolumeOutline, 0, len(volumes))
		for _, volume := range volumes {
			volumeEntries := append([]domain.OutlineEntry(nil), entries[volume.TargetFrom-1:volume.TargetTo]...)
			title := firstNonEmptyString(volume.Title, fmt.Sprintf("Volume %d", volume.Index))
			goal := firstNonEmptyString(volume.Goal, volume.Summary, volume.Theme, strings.TrimSpace(plan.Brief))
			out = append(out, domain.VolumeOutline{
				Index: volume.Index,
				Title: title,
				Theme: firstNonEmptyString(volume.Theme, volume.Summary, strings.TrimSpace(plan.Brief)),
				Arcs: []domain.ArcOutline{{
					Index:    1,
					Title:    title,
					Goal:     goal,
					Chapters: volumeEntries,
				}},
			})
		}
		return out
	}
	return []domain.VolumeOutline{{
		Index: 1,
		Title: "Adaptation",
		Theme: firstNonEmptyString(strings.TrimSpace(plan.Brief), "Confirmed adaptation plan"),
		Arcs: []domain.ArcOutline{{
			Index:    1,
			Title:    firstNonEmptyString(plan.Granularity, "adaptation"),
			Goal:     strings.TrimSpace(plan.Brief),
			Chapters: entries,
		}},
	}}
}

func adaptationTargetOutline(plan domain.AdaptationPlan) []domain.OutlineEntry {
	entries := make([]domain.OutlineEntry, 0, len(plan.Chapters))
	for idx, chapter := range plan.Chapters {
		number := chapter.Chapter
		if number <= 0 {
			number = idx + 1
		}
		title := firstNonEmptyString(chapter.Title, chapter.OutlineEntry.Title, fmt.Sprintf("Chapter %d", number))
		coreEvent := firstNonEmptyString(chapter.CoreEvent, chapter.CoverageNote, strings.Join(chapter.PreserveEvents, "；"))
		entries = append(entries, domain.OutlineEntry{
			Chapter:   number,
			Title:     title,
			CoreEvent: coreEvent,
			Hook:      chapter.Hook,
			Scenes:    append([]string(nil), chapter.Scenes...),
		})
	}
	return entries
}

func buildPlanFromInputs(opts ProposalOptions, reports []domain.AdaptationSourceReport, manifest *domain.AdaptationSourceManifest, status string) domain.AdaptationPlan {
	opts.Brief = strings.TrimSpace(opts.Brief)
	opts.Granularity = domain.NormalizeAdaptationGranularity(firstNonEmptyString(opts.Granularity, inferGranularity(opts.Brief)))
	opts.RewritePolicy = domain.AdaptationRewritePolicyForGranularity(opts.Granularity)
	opts.WordTolerance = normalizeWordToleranceForRewritePolicy(opts.RewritePolicy, opts.WordTolerance)

	sourceRunesByChapter := sourceRunesByChapter(manifest)
	sourceTotalRunes := 0
	for _, report := range reports {
		sourceTotalRunes += sourceRunesForReport(report, sourceRunesByChapter)
	}

	plan := domain.AdaptationPlan{
		Granularity:      opts.Granularity,
		Status:           domain.NormalizeAdaptationPlanStatus(status),
		RewritePolicy:    opts.RewritePolicy,
		Brief:            opts.Brief,
		WordTolerance:    opts.WordTolerance,
		SourceTotalRunes: sourceTotalRunes,
		TargetTotalRunes: sourceTotalRunes,
		MainlineRules: []string{
			"保留原书核心事件的因果顺序，不凭空跳过主线转折。",
			"每章写作前先读取 source refs，对照必须保留事件和禁止偏离事项。",
			"改动关系线时必须用场景行动承接，不能破坏原书主线动机。",
		},
		RelationshipGoals: extractRelationshipGoals(opts.Brief),
		Chapters:          make([]domain.AdaptationChapterPlan, 0, len(reports)),
	}
	if opts.RewritePolicy == domain.AdaptationRewritePreserveDetails {
		plan.TargetMinRunes, plan.TargetMaxRunes = runeRange(sourceTotalRunes, opts.WordTolerance)
		plan.MainlineRules = append(plan.MainlineRules,
			"原著细节优先：未受改编目标影响的剧情、场景和段落允许复用原文；受影响部分再重写。",
			"字数契约为来源字数 ±15%（或用户显式容差），超出硬区间必须重新规划或重写。",
		)
	} else {
		plan.MainlineRules = append(plan.MainlineRules,
			"完全重写：不得直接搬运原文段落或逐段同义替换；只锁定来源映射、主线事件和用户改编目标。",
		)
	}
	for _, report := range reports {
		plan.Chapters = append(plan.Chapters, buildChapterPlan(report, opts, sourceRunesByChapter))
	}
	if plan.TargetMinRunes <= 0 {
		plan.TargetMinRunes = plan.TargetTotalRunes
	}
	if plan.TargetMaxRunes <= 0 {
		plan.TargetMaxRunes = plan.TargetTotalRunes
	}
	return plan
}

func buildPlannerFallbackPlan(opts ProposalOptions, reports []domain.AdaptationSourceReport, manifest *domain.AdaptationSourceManifest, plannerErr error) domain.AdaptationPlan {
	plan := buildPlanFromInputs(opts, reports, manifest, domain.AdaptationPlanStatusProposal)
	plan.Planner = &domain.AdaptationPlannerMeta{
		Prompt:        adaptationPlannerPromptName,
		PromptVersion: adaptationPlannerPromptVersion,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Notes: domain.TextList{
			"planner output was unusable; generated a deterministic proposal from analyzed source reports",
			"planner error: " + plannerErr.Error(),
		},
	}
	return plan
}

func buildChapterPlan(report domain.AdaptationSourceReport, opts ProposalOptions, sourceRunesByChapter map[int]int) domain.AdaptationChapterPlan {
	sourceChapters := []int{report.Chapter}
	sourceRunes := sourceRunesForReport(report, sourceRunesByChapter)
	chapterPlan := domain.AdaptationChapterPlan{
		Chapter:         report.Chapter,
		Title:           report.Title,
		SourceChapters:  sourceChapters,
		SourceRunes:     sourceRunes,
		TargetRunes:     sourceRunes,
		SourceRange:     domain.SourceRange{From: report.Chapter, To: report.Chapter},
		CoverageNote:    coverageNote(opts.Granularity, report.Chapter, report.Chapter),
		PreserveEvents:  append([]string(nil), report.KeyEvents...),
		RequiredChanges: []string{opts.Brief},
		ForbiddenMoves: []string{
			"不要遗漏原章关键事件。",
			"不要改变原章核心因果顺序，除非 brief 明确要求。",
		},
	}
	if opts.RewritePolicy == domain.AdaptationRewritePreserveDetails {
		chapterPlan.TargetMinRunes, chapterPlan.TargetMaxRunes = runeRange(sourceRunes, opts.WordTolerance)
		chapterPlan.ForbiddenMoves = append(chapterPlan.ForbiddenMoves,
			"不要无故删除未受改编目标影响的原文细节。",
		)
	} else {
		chapterPlan.ForbiddenMoves = append(chapterPlan.ForbiddenMoves,
			"不要把原文直接同义替换成新正文。",
		)
	}
	if chapterPlan.TargetMinRunes <= 0 {
		chapterPlan.TargetMinRunes = chapterPlan.TargetRunes
	}
	if chapterPlan.TargetMaxRunes <= 0 {
		chapterPlan.TargetMaxRunes = chapterPlan.TargetRunes
	}
	chapterPlan.WordBudget = &domain.AdaptationChapterWordBudget{
		SourceRunes: sourceRunes,
		TargetRunes: chapterPlan.TargetRunes,
		MinRunes:    chapterPlan.TargetMinRunes,
		MaxRunes:    chapterPlan.TargetMaxRunes,
		Tolerance:   opts.WordTolerance,
	}
	return chapterPlan
}

func sourceRunesByChapter(manifest *domain.AdaptationSourceManifest) map[int]int {
	if manifest == nil {
		return nil
	}
	out := make(map[int]int, len(manifest.Chapters))
	for _, source := range manifest.Chapters {
		out[source.Chapter] = source.Runes
	}
	return out
}

func sourceRunesForReport(report domain.AdaptationSourceReport, sourceRunesByChapter map[int]int) int {
	if sourceRunesByChapter == nil {
		return 0
	}
	return sourceRunesByChapter[report.Chapter]
}

func runeRange(sourceRunes int, tolerance float64) (int, int) {
	if sourceRunes <= 0 {
		return 0, 0
	}
	if tolerance <= 0 {
		tolerance = DefaultWordTolerance
	}
	minRunes := int(math.Round(float64(sourceRunes) * (1 - tolerance)))
	maxRunes := int(math.Round(float64(sourceRunes) * (1 + tolerance)))
	if minRunes < 0 {
		minRunes = 0
	}
	if maxRunes < minRunes {
		maxRunes = minRunes
	}
	return minRunes, maxRunes
}

func normalizeWordToleranceForRewritePolicy(rewritePolicy string, tolerance float64) float64 {
	if domain.NormalizeAdaptationRewritePolicy(rewritePolicy) != domain.AdaptationRewritePreserveDetails {
		return 0
	}
	if tolerance <= 0 {
		return DefaultWordTolerance
	}
	return tolerance
}

func coverageNote(granularity string, from, to int) string {
	if from == to {
		if granularity == domain.AdaptationGranularityChapter {
			return fmt.Sprintf("目标章与原文第 %d 章一一对应。", from)
		}
		return fmt.Sprintf("目标章覆盖原文第 %d 章。", from)
	}
	return fmt.Sprintf("目标章覆盖原文第 %d-%d 章。", from, to)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
	fmt.Fprintf(&sb, "- 契约状态：%s\n", plan.Status)
	fmt.Fprintf(&sb, "- 改编粒度：%s\n", plan.Granularity)
	fmt.Fprintf(&sb, "- 改写策略：%s\n", plan.RewritePolicy)
	if plan.SourceTotalRunes > 0 {
		fmt.Fprintf(&sb, "- 来源总字数：%d 字\n", plan.SourceTotalRunes)
	}
	if plan.TargetMinRunes > 0 || plan.TargetMaxRunes > 0 {
		fmt.Fprintf(&sb, "- 目标总字数硬区间：%d-%d 字\n", plan.TargetMinRunes, plan.TargetMaxRunes)
	}
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
