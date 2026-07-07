package adapt

import (
	"context"
	"encoding/json"
	"errors"
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
	adaptationPlannerPromptName              = "adaptation-planner"
	adaptationPlannerPromptVersion           = "v1"
	adaptationPlannerMaxTokens               = 8192
	adaptationPlannerSkeletonMaxTokens       = 4096
	adaptationPlannerChunkedMinChapters      = 18
	adaptationPlannerRecommendedBatchMax     = 4
	adaptationPlannerSourceMapExpansionMax   = 6
	adaptationPlannerSourceChunkedMin        = adaptationPlannerRecommendedBatchMax * 2
	adaptationPlannerTargetChapterMax        = 5000
	adaptationPlannerModelChapterTargetRunes = domain.AdaptationModelChapterTargetRunes
	adaptationPlannerModelChapterMaxRunes    = domain.AdaptationModelChapterMaxRunes
	adaptationPlannerModelChapterTolerance   = domain.AdaptationModelChapterTolerance
	adaptationPlannerRevisionBatchMax        = 8
	adaptationPlannerRevisionExpansionMax    = 12
	adaptationPlannerRepairMaxAttempts       = 2
	adaptationPlannerGenerateMaxAttempts     = retrypolicy.MaxAttempts
	adaptationProposalRuntimeVersion         = 1
	adaptationSourceFoundationVersion        = 1
	adaptationSourceFoundationPromptVersion  = "source-foundation-merge-v1"
	sourceFoundationBatchKindReports         = "reports"
	sourceFoundationBatchKindSummary         = "summary"
)

var plannerRetrySleep = retrypolicy.Wait

var (
	targetChapterRangePattern        = regexp.MustCompile(`(\d{1,3})\s*(?:[-~～—–－至到]|\s+)\s*(\d{1,3})\s*(?:个)?(?:章节|章)`)
	targetChapterSinglePattern       = regexp.MustCompile(`(\d{1,3})\s*(多|余|左右|上下)?\s*(?:个)?(?:章节|章)`)
	targetChapterChineseLoosePattern = regexp.MustCompile(`([一二两三四五六七八九])([一二两三四五六七八九])十\s*(?:个)?(?:章节|章)`)
	targetChapterChinesePattern      = regexp.MustCompile(`([一二两三四五六七八九十百]{1,8})(多|余|左右|上下)?\s*(?:个)?(?:章节|章)`)
)

type Deps struct {
	Store                      *store.Store
	LLM                        imp.LLMChat
	Prompts                    Prompts
	ModelCallMaxAttempts       int
	StructureRepairMaxAttempts int
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

	if handled, err := ensurePreparedSourceDossierIfReady(ctx, deps, sourcePath, emit); err != nil {
		return err
	} else if handled {
		return nil
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
	reportsChanged, err := repairReusableSourceReports(deps.Store.Adaptation, manifest, emit)
	if err != nil {
		return err
	}
	if reports, err := deps.Store.Adaptation.LoadSourceReports(); err != nil {
		return fmt.Errorf("load source reports for co-create dossier batches: %w", err)
	} else if _, err := ensureCoCreateDossierBatches(ctx, deps, manifest, reports, emit); err != nil {
		return fmt.Errorf("ensure co-create dossier batches: %w", err)
	}

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
			structuredCallOptionsWithDeps(deps, StageChapter, chapterNum, total, emit))
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
		if shouldRefreshCoCreateDossierBatches(chapterNum, manifest) {
			reports, err := deps.Store.Adaptation.LoadSourceReports()
			if err != nil {
				return fmt.Errorf("load source reports for co-create dossier batches: %w", err)
			}
			if _, err := ensureCoCreateDossierBatches(ctx, deps, manifest, reports, emit); err != nil {
				return fmt.Errorf("ensure co-create dossier batches: %w", err)
			}
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
	shouldMergeFoundation := true
	if foundation != nil && !sourceChanged && !reportsChanged {
		reportSignature := sourceReportsSignature(reports)
		promptSignature := sourceFoundationPromptSignature(deps.Prompts.FoundationMerge)
		shouldMergeFoundation = !sourceFoundationCurrent(foundation, manifest, reportSignature, promptSignature, imp.DefaultFoundationMergeRunes)
	}
	if !shouldMergeFoundation {
		emit(StageFoundation, total, total, "源书 foundation 已存在，跳过聚合", nil)
	} else {
		emit(StageFoundation, total, total, "聚合逐章事实，生成源书 foundation...", nil)
		fr, err := mergeSourceFoundationResumable(ctx, deps, manifest, reports, imp.DefaultFoundationMergeRunes, emit)
		if err != nil {
			return fmt.Errorf("merge source foundation: %w", err)
		}
		foundation := sourceFoundationWithMetadata(toSourceFoundation(fr), manifest, reports, deps.Prompts.FoundationMerge, imp.DefaultFoundationMergeRunes)
		if err := deps.Store.Adaptation.SaveSourceFoundation(foundation); err != nil {
			return fmt.Errorf("save source foundation: %w", err)
		}
	}
	if _, err := EnsureCoCreateDossier(ctx, deps, manifest, reports, emit); err != nil {
		return fmt.Errorf("ensure co-create dossier: %w", err)
	}
	emit(StageDone, total, total, fmt.Sprintf("原书分析完成：%d 章快照已保存", total), nil)
	return nil
}

func ensurePreparedSourceDossierIfReady(ctx context.Context, deps Deps, sourcePath string, emit func(Stage, int, int, string, error)) (bool, error) {
	manifest, err := deps.Store.Adaptation.LoadSourceManifest()
	if err != nil {
		return false, fmt.Errorf("load source manifest: %w", err)
	}
	if manifest == nil || manifest.ChapterCount <= 0 || !sameSourcePath(manifest.SourcePath, sourcePath) {
		return false, nil
	}
	reports, err := deps.Store.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		return false, fmt.Errorf("load complete source reports: %w", err)
	}
	if len(reports) != manifest.ChapterCount {
		return false, nil
	}
	foundation, err := deps.Store.Adaptation.LoadSourceFoundation()
	if err != nil {
		return false, fmt.Errorf("load source foundation: %w", err)
	}
	if foundation == nil {
		return false, nil
	}
	current, err := deps.Store.Adaptation.CoCreateDossierCurrent(CoCreateDossierPromptVersion, CoCreateDossierBatchSize, CoCreateDossierBatchRuneLimit)
	if err != nil {
		return false, fmt.Errorf("check co-create dossier: %w", err)
	}
	if current {
		return false, nil
	}
	if _, err := EnsureCoCreateDossier(ctx, deps, manifest, reports, emit); err != nil {
		return true, err
	}
	return true, nil
}

func mergeSourceFoundationResumable(
	ctx context.Context,
	deps Deps,
	manifest *domain.AdaptationSourceManifest,
	reports []domain.AdaptationSourceReport,
	batchRuneLimit int,
	emit func(Stage, int, int, string, error),
) (*imp.FoundationResult, error) {
	if deps.Store == nil || deps.LLM == nil {
		return nil, fmt.Errorf("deps incomplete")
	}
	if manifest == nil || manifest.ChapterCount <= 0 {
		return nil, fmt.Errorf("source manifest is required")
	}
	if len(reports) == 0 {
		return nil, fmt.Errorf("source reports are required")
	}
	if emit == nil {
		emit = func(Stage, int, int, string, error) {}
	}
	if batchRuneLimit <= 0 {
		batchRuneLimit = imp.DefaultFoundationMergeRunes
	}
	total := len(reports)
	promptSignature := sourceFoundationPromptSignature(deps.Prompts.FoundationMerge)
	sourceSignature := store.AdaptationSourceSignature(*manifest)
	opts := structuredCallOptionsWithDeps(deps, StageFoundation, total, total, emit)
	reportBatches := imp.FoundationMergeReportBatches(reports, batchRuneLimit)
	partials := make([]imp.FoundationMergePartial, 0, len(reportBatches))
	for i, batchReports := range reportBatches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		index := i + 1
		from := batchReports[0].Chapter
		to := batchReports[len(batchReports)-1].Chapter
		inputSignature := sourceReportsSignature(batchReports)
		existing, err := deps.Store.Adaptation.LoadSourceFoundationBatch(0, index)
		if err != nil {
			return nil, fmt.Errorf("load source foundation batch %d: %w", index, err)
		}
		if sourceFoundationBatchCurrent(existing, sourceFoundationBatchKindReports, 0, index, from, to, sourceSignature, inputSignature, promptSignature, batchRuneLimit) {
			partials = append(partials, imp.FoundationMergePartial{
				Index:          index,
				From:           existing.SourceFrom,
				To:             existing.SourceTo,
				InputSignature: existing.InputSignature,
				Result:         toFoundationResult(&existing.Foundation),
			})
			emit(StageFoundation, to, total, fmt.Sprintf("reuse source foundation checkpoint %d/%d: chapters %d-%d", index, len(reportBatches), from, to), nil)
			continue
		}

		emit(StageFoundation, to, total, fmt.Sprintf("merge source foundation batch %d/%d: chapters %d-%d", index, len(reportBatches), from, to), nil)
		result, err := imp.MergeFoundationFromReports(ctx, deps.LLM, deps.Prompts.FoundationMerge, batchReports, opts)
		if err != nil {
			return nil, fmt.Errorf("merge source foundation batch %d/%d (chapters %d-%d): %w", index, len(reportBatches), from, to, err)
		}
		batch := sourceFoundationBatchFromResult(sourceFoundationBatchKindReports, 0, index, from, to, sourceSignature, inputSignature, promptSignature, batchRuneLimit, manifest, result)
		if err := deps.Store.Adaptation.SaveSourceFoundationBatch(batch); err != nil {
			return nil, fmt.Errorf("save source foundation batch %d: %w", index, err)
		}
		partials = append(partials, imp.FoundationMergePartial{
			Index:          index,
			From:           from,
			To:             to,
			InputSignature: inputSignature,
			Result:         result,
		})
	}

	result, err := mergeSourceFoundationPartialsResumable(ctx, deps, manifest, partials, total, batchRuneLimit, sourceSignature, promptSignature, emit)
	if err != nil {
		return nil, err
	}
	result.Volumes = imp.BuildSourceOutlineFromReports(reports)
	if got := len(domain.FlattenOutline(result.Volumes)); got != len(reports) {
		return nil, fmt.Errorf("generated source outline chapter count mismatch: got %d, want %d", got, len(reports))
	}
	if result.Compass != nil && result.Compass.LastUpdated == 0 {
		result.Compass.LastUpdated = len(reports)
	}
	return result, nil
}

func mergeSourceFoundationPartialsResumable(
	ctx context.Context,
	deps Deps,
	manifest *domain.AdaptationSourceManifest,
	partials []imp.FoundationMergePartial,
	totalReports int,
	batchRuneLimit int,
	sourceSignature string,
	promptSignature string,
	emit func(Stage, int, int, string, error),
) (*imp.FoundationResult, error) {
	if len(partials) == 0 {
		return nil, fmt.Errorf("no source foundation batches to merge")
	}
	if len(partials) == 1 {
		return partials[0].Result, nil
	}
	opts := structuredCallOptionsWithDeps(deps, StageFoundation, totalReports, totalReports, emit)
	current := partials
	level := 1
	for len(current) > 1 {
		groups := imp.FoundationMergePartialBatches(current, batchRuneLimit)
		next := make([]imp.FoundationMergePartial, 0, len(groups))
		for i, group := range groups {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			index := i + 1
			from := group[0].From
			to := group[len(group)-1].To
			inputSignature := sourceFoundationPartialGroupSignature(level, group)
			existing, err := deps.Store.Adaptation.LoadSourceFoundationBatch(level, index)
			if err != nil {
				return nil, fmt.Errorf("load source foundation summary level %d batch %d: %w", level, index, err)
			}
			if sourceFoundationBatchCurrent(existing, sourceFoundationBatchKindSummary, level, index, from, to, sourceSignature, inputSignature, promptSignature, batchRuneLimit) {
				next = append(next, imp.FoundationMergePartial{
					Index:          index,
					From:           existing.SourceFrom,
					To:             existing.SourceTo,
					InputSignature: existing.InputSignature,
					Result:         toFoundationResult(&existing.Foundation),
				})
				emit(StageFoundation, to, totalReports, fmt.Sprintf("reuse source foundation summary checkpoint L%d %d/%d: chapters %d-%d", level, index, len(groups), from, to), nil)
				continue
			}

			emit(StageFoundation, to, totalReports, fmt.Sprintf("merge source foundation summary L%d %d/%d: chapters %d-%d", level, index, len(groups), from, to), nil)
			result, err := imp.MergeFoundationPartials(ctx, deps.LLM, deps.Prompts.FoundationMerge, group, totalReports, opts)
			if err != nil {
				return nil, fmt.Errorf("merge source foundation summary level %d batch %d/%d (chapters %d-%d): %w", level, index, len(groups), from, to, err)
			}
			batch := sourceFoundationBatchFromResult(sourceFoundationBatchKindSummary, level, index, from, to, sourceSignature, inputSignature, promptSignature, batchRuneLimit, manifest, result)
			if err := deps.Store.Adaptation.SaveSourceFoundationBatch(batch); err != nil {
				return nil, fmt.Errorf("save source foundation summary level %d batch %d: %w", level, index, err)
			}
			next = append(next, imp.FoundationMergePartial{
				Index:          index,
				From:           from,
				To:             to,
				InputSignature: inputSignature,
				Result:         result,
			})
		}
		current = next
		level++
	}
	return current[0].Result, nil
}

func sourceFoundationBatchFromResult(
	kind string,
	level int,
	index int,
	from int,
	to int,
	sourceSignature string,
	inputSignature string,
	promptSignature string,
	batchRuneLimit int,
	manifest *domain.AdaptationSourceManifest,
	result *imp.FoundationResult,
) domain.AdaptationSourceFoundationBatch {
	foundation := sourceFoundationWithMetadata(toSourceFoundation(result), manifest, nil, "", batchRuneLimit)
	foundation.ReportSignature = inputSignature
	foundation.PromptVersion = promptSignature
	return domain.AdaptationSourceFoundationBatch{
		Version:            adaptationSourceFoundationVersion,
		Kind:               kind,
		Level:              level,
		Index:              index,
		SourceFrom:         from,
		SourceTo:           to,
		SourcePath:         foundation.SourcePath,
		SourceChapterCount: foundation.SourceChapterCount,
		SourceSignature:    sourceSignature,
		InputSignature:     inputSignature,
		PromptVersion:      promptSignature,
		BatchRuneLimit:     batchRuneLimit,
		GeneratedAt:        foundation.GeneratedAt,
		Foundation:         foundation,
	}
}

func sourceFoundationCurrent(
	foundation *domain.AdaptationSourceFoundation,
	manifest *domain.AdaptationSourceManifest,
	reportSignature string,
	promptSignature string,
	batchRuneLimit int,
) bool {
	if !sourceFoundationUsable(foundation) || manifest == nil {
		return false
	}
	hasMetadata := foundation.Version > 0 ||
		strings.TrimSpace(foundation.SourceSignature) != "" ||
		strings.TrimSpace(foundation.ReportSignature) != "" ||
		strings.TrimSpace(foundation.PromptVersion) != "" ||
		foundation.BatchRuneLimit > 0
	if !hasMetadata {
		return len(domain.FlattenOutline(foundation.Volumes)) == manifest.ChapterCount
	}
	return foundation.Version == adaptationSourceFoundationVersion &&
		foundation.SourceChapterCount == manifest.ChapterCount &&
		strings.TrimSpace(foundation.SourceSignature) == store.AdaptationSourceSignature(*manifest) &&
		strings.TrimSpace(foundation.ReportSignature) == strings.TrimSpace(reportSignature) &&
		strings.TrimSpace(foundation.PromptVersion) == strings.TrimSpace(promptSignature) &&
		foundation.BatchRuneLimit == batchRuneLimit
}

func sourceFoundationBatchCurrent(
	batch *domain.AdaptationSourceFoundationBatch,
	kind string,
	level int,
	index int,
	from int,
	to int,
	sourceSignature string,
	inputSignature string,
	promptSignature string,
	batchRuneLimit int,
) bool {
	if batch == nil || !sourceFoundationUsable(&batch.Foundation) {
		return false
	}
	return batch.Version == adaptationSourceFoundationVersion &&
		strings.TrimSpace(batch.Kind) == kind &&
		batch.Level == level &&
		batch.Index == index &&
		batch.SourceFrom == from &&
		batch.SourceTo == to &&
		strings.TrimSpace(batch.SourceSignature) == strings.TrimSpace(sourceSignature) &&
		strings.TrimSpace(batch.InputSignature) == strings.TrimSpace(inputSignature) &&
		strings.TrimSpace(batch.PromptVersion) == strings.TrimSpace(promptSignature) &&
		batch.BatchRuneLimit == batchRuneLimit
}

func sourceFoundationWithMetadata(
	foundation domain.AdaptationSourceFoundation,
	manifest *domain.AdaptationSourceManifest,
	reports []domain.AdaptationSourceReport,
	systemPrompt string,
	batchRuneLimit int,
) domain.AdaptationSourceFoundation {
	foundation.Version = adaptationSourceFoundationVersion
	if strings.TrimSpace(foundation.GeneratedAt) == "" {
		foundation.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if manifest != nil {
		foundation.SourcePath = manifest.SourcePath
		foundation.SourceChapterCount = manifest.ChapterCount
		foundation.SourceSignature = store.AdaptationSourceSignature(*manifest)
	}
	if reports != nil {
		foundation.ReportSignature = sourceReportsSignature(reports)
	}
	if strings.TrimSpace(systemPrompt) != "" {
		foundation.PromptVersion = sourceFoundationPromptSignature(systemPrompt)
	}
	foundation.BatchRuneLimit = batchRuneLimit
	return foundation
}

func sourceFoundationUsable(foundation *domain.AdaptationSourceFoundation) bool {
	return foundation != nil &&
		strings.TrimSpace(foundation.Premise) != "" &&
		len(foundation.Characters) > 0
}

func sourceFoundationPromptSignature(systemPrompt string) string {
	return adaptationSourceFoundationPromptVersion + ":" + store.TextSHA256(strings.TrimSpace(systemPrompt))
}

func sourceReportsSignature(reports []domain.AdaptationSourceReport) string {
	ordered := append([]domain.AdaptationSourceReport(nil), reports...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Chapter < ordered[j].Chapter
	})
	data, err := json.Marshal(struct {
		Version int                             `json:"version"`
		Reports []domain.AdaptationSourceReport `json:"reports"`
	}{Version: 1, Reports: ordered})
	if err != nil {
		return store.TextSHA256(fmt.Sprintf("%+v", ordered))
	}
	return store.TextSHA256(string(data))
}

func sourceFoundationPartialGroupSignature(level int, partials []imp.FoundationMergePartial) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "level=%d\n", level)
	for _, partial := range partials {
		fmt.Fprintf(&sb, "%d:%d-%d:%s:%s\n",
			partial.Index,
			partial.From,
			partial.To,
			partial.InputSignature,
			sourceFoundationContentSignature(partial.Result),
		)
	}
	return store.TextSHA256(sb.String())
}

func sourceFoundationContentSignature(result *imp.FoundationResult) string {
	data, err := json.Marshal(toSourceFoundation(result))
	if err != nil {
		return store.TextSHA256(fmt.Sprintf("%+v", result))
	}
	return store.TextSHA256(string(data))
}

func shouldRefreshCoCreateDossierBatches(chapter int, manifest *domain.AdaptationSourceManifest) bool {
	if chapter <= 0 || manifest == nil || chapter >= manifest.ChapterCount {
		return false
	}
	for _, spec := range dossierBatchSpecs(*manifest, CoCreateDossierBatchSize) {
		if spec.SourceTo == chapter {
			return true
		}
	}
	return false
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

	var legacyReports []domain.AdaptationSourceReport
	legacySourceText := make(map[int]string)
	if existing != nil {
		legacyReports, err = adaptation.LoadSourceReports()
		if err != nil {
			return nil, false, fmt.Errorf("load legacy source reports: %w", err)
		}
		for _, source := range existing.Chapters {
			text, _, err := adaptation.LoadSourceChapter(source.Chapter)
			if err != nil {
				return nil, false, fmt.Errorf("load legacy source chapter %d: %w", source.Chapter, err)
			}
			if strings.TrimSpace(text) != "" {
				legacySourceText[source.Chapter] = text
			}
		}
		if _, err := adaptation.Backup("source-snapshot-change"); err != nil {
			return nil, false, fmt.Errorf("backup adaptation store before source reset: %w", err)
		}
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
	if _, err := migrateLegacySourceReportsAfterSnapshotChange(adaptation, legacyReports, legacySourceText, next, chapters); err != nil {
		return nil, false, err
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
		reportHasReusableAnalysis(report)
}

func repairReusableSourceReports(adaptation *store.AdaptationStore, manifest *domain.AdaptationSourceManifest, emit func(Stage, int, int, string, error)) (bool, error) {
	if adaptation == nil || manifest == nil {
		return false, nil
	}
	changed := false
	for _, source := range manifest.Chapters {
		report, err := adaptation.LoadSourceReport(source.Chapter)
		if err != nil {
			return false, fmt.Errorf("load source report %d for migration: %w", source.Chapter, err)
		}
		if reusableSourceReport(report, source.SHA256) {
			continue
		}
		if !legacyReusableSourceReport(report, source) {
			continue
		}
		next := migratedSourceReport(*report, source)
		if err := adaptation.SaveSourceReport(next); err != nil {
			return false, fmt.Errorf("save migrated source report %d: %w", source.Chapter, err)
		}
		changed = true
		if emit != nil {
			emit(StageChapter, source.Chapter, manifest.ChapterCount, fmt.Sprintf("沿用第 %d/%d 章旧分析报告：%s", source.Chapter, manifest.ChapterCount, source.Title), nil)
		}
	}
	if changed {
		if reports, err := adaptation.LoadSourceReports(); err == nil {
			_ = adaptation.SaveSourceReports(reports)
		}
	}
	return changed, nil
}

func migrateLegacySourceReportsAfterSnapshotChange(adaptation *store.AdaptationStore, reports []domain.AdaptationSourceReport, oldSourceText map[int]string, manifest domain.AdaptationSourceManifest, chapters []imp.Chapter) (int, error) {
	if adaptation == nil || len(reports) == 0 || len(chapters) == 0 {
		return 0, nil
	}
	reportByChapter := make(map[int]domain.AdaptationSourceReport, len(reports))
	for _, report := range reports {
		if report.Chapter <= 0 || !reportHasReusableAnalysis(&report) {
			continue
		}
		reportByChapter[report.Chapter] = report
	}
	migrated := 0
	for i, source := range manifest.Chapters {
		if i >= len(chapters) {
			break
		}
		report, ok := reportByChapter[source.Chapter]
		if !ok || !legacyReusableSourceReport(&report, source) {
			continue
		}
		if !sourceTextsSimilar(oldSourceText[source.Chapter], chapters[i].Content) {
			continue
		}
		if err := adaptation.SaveSourceReport(migratedSourceReport(report, source)); err != nil {
			return migrated, fmt.Errorf("save migrated source report %d after source reset: %w", source.Chapter, err)
		}
		migrated++
	}
	if migrated > 0 {
		if nextReports, err := adaptation.LoadSourceReports(); err == nil {
			_ = adaptation.SaveSourceReports(nextReports)
		}
	}
	return migrated, nil
}

func legacyReusableSourceReport(report *domain.AdaptationSourceReport, source domain.AdaptationSource) bool {
	return report != nil &&
		report.Chapter == source.Chapter &&
		reportHasReusableAnalysis(report) &&
		reportTitleMatchesSource(report.Title, source.Title)
}

func migratedSourceReport(report domain.AdaptationSourceReport, source domain.AdaptationSource) domain.AdaptationSourceReport {
	report.Chapter = source.Chapter
	report.Title = source.Title
	report.SourceSHA256 = source.SHA256
	return report
}

func reportHasReusableAnalysis(report *domain.AdaptationSourceReport) bool {
	return report != nil &&
		strings.TrimSpace(report.Summary) != "" &&
		len(report.KeyEvents) > 0
}

func reportTitleMatchesSource(reportTitle, sourceTitle string) bool {
	return normalizeSourceReportTitle(reportTitle) == normalizeSourceReportTitle(sourceTitle)
}

func normalizeSourceReportTitle(title string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(title)), "")
}

func sourceTextsSimilar(oldText, newText string) bool {
	oldText = normalizeSourceTextForReuse(oldText)
	newText = normalizeSourceTextForReuse(newText)
	if oldText == "" || newText == "" {
		return false
	}
	if oldText == newText {
		return true
	}
	shorter, longer := oldText, newText
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	if len(shorter) < 200 {
		return false
	}
	ratio := float64(len(shorter)) / float64(len(longer))
	if ratio >= 0.90 && strings.Contains(longer, shorter) {
		return true
	}
	return ratio >= 0.90 && commonPrefixBytes(shorter, longer) >= int(float64(len(shorter))*0.90)
}

func normalizeSourceTextForReuse(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), "")
}

func commonPrefixBytes(a, b string) int {
	max := len(a)
	if len(b) < max {
		max = len(b)
	}
	for i := 0; i < max; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return max
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

func structuredCallOptionsWithDeps(deps Deps, stage Stage, current, total int, emit func(Stage, int, int, string, error)) imp.StructuredCallOptions {
	opts := structuredCallOptions(stage, current, total, emit)
	opts.MaxAttempts = deps.modelCallMaxAttempts()
	return opts
}

func (d Deps) modelCallMaxAttempts() int {
	if d.ModelCallMaxAttempts > 0 {
		return d.ModelCallMaxAttempts
	}
	return adaptationPlannerGenerateMaxAttempts
}

func (d Deps) structureRepairMaxAttempts() int {
	if d.StructureRepairMaxAttempts > 0 {
		return d.StructureRepairMaxAttempts
	}
	return adaptationPlannerRepairMaxAttempts
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

func prepareProposalPlannerInputs(ctx context.Context, deps Deps, opts ProposalOptions) (ProposalOptions, *domain.AdaptationSourceManifest, []domain.AdaptationSourceReport, *domain.AdaptationSourceFoundation, error) {
	if deps.Store == nil {
		return opts, nil, nil, nil, fmt.Errorf("store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	opts.Brief = strings.TrimSpace(opts.Brief)
	if opts.Brief == "" {
		return opts, nil, nil, nil, fmt.Errorf("adaptation brief is required")
	}
	granularity, ok := domain.StrictAdaptationGranularity(opts.Granularity)
	if !ok {
		return opts, nil, nil, nil, fmt.Errorf("adaptation mode must be one of chapter, arc, free")
	}
	opts.Granularity = granularity
	opts.RewritePolicy = domain.AdaptationRewritePolicyForGranularity(opts.Granularity)
	opts.WordTolerance = normalizeProposalWordTolerance(opts.Granularity, opts.WordTolerance)
	manifest, reports, err := ValidatePreparedSource(deps.Store, opts.SourcePath)
	if err != nil {
		return opts, nil, nil, nil, err
	}
	sourceFoundation, err := deps.Store.Adaptation.LoadSourceFoundation()
	if err != nil {
		return opts, nil, nil, nil, fmt.Errorf("load source foundation: %w", err)
	}
	if sourceFoundation == nil {
		return opts, nil, nil, nil, fmt.Errorf("source foundation missing; import source first")
	}
	return opts, manifest, reports, sourceFoundation, nil
}

func BuildAdaptationProposalContext(ctx context.Context, deps Deps, opts ProposalOptions) (*domain.AdaptationPlan, error) {
	opts, manifest, reports, sourceFoundation, err := prepareProposalPlannerInputs(ctx, deps, opts)
	if err != nil {
		return nil, err
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

func BuildAdaptationProposalVolumesContext(ctx context.Context, deps Deps, opts ProposalOptions) (*ProposalStageResult, error) {
	opts, manifest, reports, sourceFoundation, err := prepareProposalPlannerInputs(ctx, deps, opts)
	if err != nil {
		return nil, err
	}
	emitAdaptProgress(opts.EmitProgress, StagePlan, 0, 0, "改编规划准备完成，正在判断是否需要分卷审核", nil)
	targetChapterCount := plannerTargetChapterCount(opts, manifest)
	if !shouldUseChunkedPlanner(opts, manifest, targetChapterCount) {
		proposal, err := BuildAdaptationProposalContext(ctx, deps, opts)
		if err != nil {
			return nil, err
		}
		return &ProposalStageResult{Proposal: proposal}, nil
	}
	skeleton, runtime, err := buildPlannerVolumeSkeleton(ctx, deps, opts, reports, manifest, sourceFoundation, targetChapterCount)
	if err != nil {
		return nil, fmt.Errorf("build %s adaptation volume review: %w", opts.Granularity, err)
	}
	review := volumeReviewFromSkeleton(opts, manifest, skeleton)
	emitAdaptProgress(opts.EmitProgress, StagePlan, len(review.Volumes), len(review.Volumes), fmt.Sprintf("分卷剧情已生成，正在保存：%d 卷", len(review.Volumes)), nil)
	if err := deps.Store.Adaptation.SaveVolumeReview(review); err != nil {
		return nil, fmt.Errorf("save adaptation volume review: %w", err)
	}
	if runtime != nil {
		runtime.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
		runtime.CompletedBatches = nil
		if err := savePlannerProposalRuntime(deps, runtime); err != nil {
			return nil, fmt.Errorf("save proposal runtime skeleton: %w", err)
		}
	}
	emitAdaptProgress(opts.EmitProgress, StageDone, len(review.Volumes), len(review.Volumes), fmt.Sprintf("分卷剧情已保存，等待审核：%d 卷", len(review.Volumes)), nil)
	return &ProposalStageResult{VolumeReview: &review}, nil
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
	if opts.VolumeIndex > 0 {
		return reviseAdaptationProposalVolumeContext(ctx, deps, opts, *proposal, from, to, manifest, sourceFoundation, reports, systemPrompt)
	}
	updated := cloneAdaptationPlan(*proposal)
	updated.Volumes = normalizeAdaptationProposalVolumes(updated.Volumes, len(updated.Chapters))
	allowExpansion := shouldAllowProposalRevisionExpansion(*proposal, opts, from, to)
	totalBatches := revisionBatchCount(from, to, adaptationPlannerRevisionBatchMax)
	batchOrdinal := 0
	for chunkFrom := from; chunkFrom <= to; chunkFrom += adaptationPlannerRevisionBatchMax {
		chunkTo := min(to, chunkFrom+adaptationPlannerRevisionBatchMax-1)
		batchOrdinal++
		batch := proposalRevisionBatch(updated, chunkFrom, chunkTo)
		expansionMaxTo := chunkTo
		if allowExpansion && chunkTo == to {
			expansionMaxTo = to + adaptationPlannerRevisionExpansionMax
		}
		revisionPrompt, err := buildAdaptationProposalRevisionUserPrompt(opts, updated, batch, expansionMaxTo, manifest, sourceFoundation, reportsForPlannerBatch(reports, batch))
		if err != nil {
			return nil, err
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal, totalBatches, fmt.Sprintf("请求修订第 %d/%d 批：第 %d-%d 章", batchOrdinal, totalBatches, chunkFrom, chunkTo), nil)
		revisionText, err := generatePlannerText(
			ctx,
			deps.LLM,
			systemPrompt,
			revisionPrompt,
			adaptationPlannerMaxTokens,
			opts.EmitProgress,
			batchOrdinal,
			totalBatches,
			fmt.Sprintf("修订第 %d/%d 批", batchOrdinal, totalBatches),
			deps.modelCallMaxAttempts(),
		)
		if err != nil {
			return nil, fmt.Errorf("planner revision %d-%d llm generate: %w", chunkFrom, chunkTo, err)
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal, totalBatches, fmt.Sprintf("修订模型已返回第 %d/%d 批，正在解析校验", batchOrdinal, totalBatches), nil)
		revisedChapters, err := collectProposalRevisionBatchChaptersWithRepair(
			ctx,
			deps.LLM,
			systemPrompt,
			revisionPrompt,
			revisionText,
			batch,
			expansionMaxTo,
			plannerBatchChapterValidator(proposalOptionsFromPlan(updated), manifest, batch),
			opts.EmitProgress,
			batchOrdinal,
			totalBatches,
			fmt.Sprintf("修订第 %d/%d 批", batchOrdinal, totalBatches),
			deps.structureRepairMaxAttempts(),
			deps.modelCallMaxAttempts(),
		)
		if err != nil {
			return nil, fmt.Errorf("planner revision %d-%d: %w", chunkFrom, chunkTo, err)
		}
		revisedTo, err := replaceProposalChapterRange(&updated, chunkFrom, chunkTo, expansionMaxTo, revisedChapters)
		if err != nil {
			return nil, err
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal, totalBatches, fmt.Sprintf("修订第 %d/%d 批完成：第 %d-%d 章", batchOrdinal, totalBatches, chunkFrom, revisedTo), nil)
	}
	return finalizeRevisedAdaptationProposal(deps, opts, *proposal, updated, from, to, reports, manifest, totalBatches, totalBatches)
}

func ReviseAdaptationVolumeReviewContext(ctx context.Context, deps Deps, opts ProposalRevisionOptions) (*domain.AdaptationVolumeReview, error) {
	if deps.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if deps.LLM == nil {
		return nil, fmt.Errorf("planner llm is required for adaptation volume review revision")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	opts.Instruction = strings.TrimSpace(opts.Instruction)
	if opts.Instruction == "" {
		return nil, fmt.Errorf("revision instruction is required")
	}
	if opts.VolumeIndex <= 0 {
		return nil, fmt.Errorf("volume_index must name one volume")
	}
	review, err := deps.Store.Adaptation.LoadVolumeReview()
	if err != nil {
		return nil, fmt.Errorf("load adaptation volume review: %w", err)
	}
	if review == nil || len(review.Volumes) == 0 {
		return nil, fmt.Errorf("adaptation volume review is required")
	}
	manifest, reports, err := ValidatePreparedSource(deps.Store, review.SourcePath)
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
	originalBatch, err := volumeReviewBatch(*review, opts.VolumeIndex)
	if err != nil {
		return nil, err
	}
	systemPrompt := strings.TrimSpace(deps.Prompts.Planner)
	if systemPrompt == "" {
		systemPrompt = "# Adaptation Planner\n\nReturn only JSON for the requested volume review revision."
	}
	expansionMaxTo := originalBatch.TargetTo + adaptationPlannerRevisionExpansionMax
	emitAdaptProgress(opts.EmitProgress, StagePlan, 0, 0, fmt.Sprintf("请求第 %d 卷剧情修正：第 %d-%d 章", opts.VolumeIndex, originalBatch.TargetFrom, originalBatch.TargetTo), nil)
	revisionPrompt, err := buildAdaptationVolumeReviewRevisionPrompt(opts, *review, originalBatch, expansionMaxTo, manifest, sourceFoundation, reportsForPlannerBatch(reports, originalBatch))
	if err != nil {
		return nil, err
	}
	revisionText, err := generatePlannerText(
		ctx,
		deps.LLM,
		systemPrompt,
		revisionPrompt,
		adaptationPlannerSkeletonMaxTokens,
		opts.EmitProgress,
		0,
		0,
		fmt.Sprintf("第 %d 卷剧情修正", opts.VolumeIndex),
		deps.modelCallMaxAttempts(),
	)
	if err != nil {
		return nil, fmt.Errorf("planner volume review revision llm generate: %w", err)
	}
	revisedBatch, err := collectProposalVolumeRevisionSkeletonWithRepair(ctx, deps.LLM, systemPrompt, revisionPrompt, revisionText, originalBatch, expansionMaxTo, true, manifest, opts.EmitProgress, deps.structureRepairMaxAttempts(), deps.modelCallMaxAttempts())
	if err != nil {
		return nil, fmt.Errorf("planner volume review revision skeleton: %w", err)
	}
	updated := cloneAdaptationVolumeReview(*review)
	applyVolumeReviewBatchRevision(&updated, originalBatch, revisedBatch)
	if err := validateAdaptationVolumeReview(updated, manifest); err != nil {
		return nil, err
	}
	if updated.Planner == nil {
		updated.Planner = &domain.AdaptationPlannerMeta{}
	}
	updated.Planner.Notes = append(updated.Planner.Notes,
		fmt.Sprintf("volume review revised for volume %d: %s", opts.VolumeIndex, opts.Instruction),
	)
	if err := deps.Store.Adaptation.SaveVolumeReview(updated); err != nil {
		return nil, fmt.Errorf("save revised adaptation volume review: %w", err)
	}
	emitAdaptProgress(opts.EmitProgress, StageDone, len(updated.Volumes), len(updated.Volumes), fmt.Sprintf("第 %d 卷剧情已修订，等待审核", opts.VolumeIndex), nil)
	return &updated, nil
}

func BuildAdaptationProposalDetailsContext(ctx context.Context, deps Deps, opts ProposalDetailsOptions) (*domain.AdaptationPlan, error) {
	if deps.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if deps.LLM == nil {
		return nil, fmt.Errorf("planner llm is required for adaptation proposal details")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	review, err := deps.Store.Adaptation.LoadVolumeReview()
	if err != nil {
		return nil, fmt.Errorf("load adaptation volume review: %w", err)
	}
	if review == nil || len(review.Volumes) == 0 {
		return nil, fmt.Errorf("adaptation volume review is required")
	}
	proposalOpts := proposalOptionsFromVolumeReview(*review)
	proposalOpts.EmitProgress = opts.EmitProgress
	manifest, reports, err := ValidatePreparedSource(deps.Store, review.SourcePath)
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
	skeleton := plannerSkeletonFromVolumeReview(*review)
	if err := normalizePlannerSkeleton(&skeleton, proposalOpts, manifest, review.TargetChapterCount); err != nil {
		return nil, fmt.Errorf("volume review skeleton invalid: %w", err)
	}
	runtime, _, err := loadPlannerProposalRuntime(deps, proposalOpts, manifest, review.TargetChapterCount, opts.EmitProgress)
	if err != nil {
		return nil, err
	}
	if runtime == nil {
		runtime = newPlannerProposalRuntime(proposalOpts, manifest, review.TargetChapterCount)
	}
	if runtime.Skeleton != nil && !plannerRuntimeOutlineMatchesSkeleton(runtime.Skeleton, skeleton) {
		emitAdaptProgress(opts.EmitProgress, StagePlan, 0, 0, "分卷剧情已变化，清除旧章节细纲断点后重新生成", nil)
		runtime.CompletedBatches = nil
	}
	runtime.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
	if err := savePlannerProposalRuntime(deps, runtime); err != nil {
		return nil, fmt.Errorf("save proposal runtime skeleton: %w", err)
	}
	proposal, err := buildPlanFromPlannerSkeletonDetails(ctx, deps, proposalOpts, reports, manifest, sourceFoundation, skeleton, runtime)
	if err != nil {
		return nil, err
	}
	emitAdaptProgress(opts.EmitProgress, StagePlan, len(proposal.Chapters), len(proposal.Chapters), fmt.Sprintf("改编章节细纲已生成，正在保存：%d 章", len(proposal.Chapters)), nil)
	if err := deps.Store.Adaptation.SaveProposal(proposal); err != nil {
		return nil, fmt.Errorf("save adaptation proposal: %w", err)
	}
	emitAdaptProgress(opts.EmitProgress, StageDone, len(proposal.Chapters), len(proposal.Chapters), fmt.Sprintf("改编提案已保存：%d 章", len(proposal.Chapters)), nil)
	return &proposal, nil
}

func reviseAdaptationProposalVolumeContext(
	ctx context.Context,
	deps Deps,
	opts ProposalRevisionOptions,
	proposal domain.AdaptationPlan,
	from int,
	to int,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	reports []domain.AdaptationSourceReport,
	systemPrompt string,
) (*domain.AdaptationPlan, error) {
	updated := cloneAdaptationPlan(proposal)
	updated.Volumes = normalizeAdaptationProposalVolumes(updated.Volumes, len(updated.Chapters))
	originalBatch := proposalRevisionVolumeBatch(updated, opts.VolumeIndex, from, to)
	allowExpansion := shouldAllowProposalRevisionExpansion(proposal, opts, from, to)
	expansionMaxTo := to
	if allowExpansion {
		expansionMaxTo = to + adaptationPlannerRevisionExpansionMax
	}
	emitAdaptProgress(opts.EmitProgress, StagePlan, 0, 0, fmt.Sprintf("请求第 %d 卷剧情重规划：第 %d-%d 章", opts.VolumeIndex, from, to), nil)
	skeletonPrompt, err := buildAdaptationProposalVolumeRevisionSkeletonPrompt(opts, updated, originalBatch, expansionMaxTo, manifest, sourceFoundation, reportsForPlannerBatch(reports, originalBatch))
	if err != nil {
		return nil, err
	}
	skeletonText, err := generatePlannerText(
		ctx,
		deps.LLM,
		systemPrompt,
		skeletonPrompt,
		adaptationPlannerSkeletonMaxTokens,
		opts.EmitProgress,
		0,
		0,
		fmt.Sprintf("第 %d 卷剧情重规划", opts.VolumeIndex),
		deps.modelCallMaxAttempts(),
	)
	if err != nil {
		return nil, fmt.Errorf("planner volume revision skeleton llm generate: %w", err)
	}
	revisedBatch, err := collectProposalVolumeRevisionSkeletonWithRepair(ctx, deps.LLM, systemPrompt, skeletonPrompt, skeletonText, originalBatch, expansionMaxTo, allowExpansion, manifest, opts.EmitProgress, deps.structureRepairMaxAttempts(), deps.modelCallMaxAttempts())
	if err != nil {
		return nil, fmt.Errorf("planner volume revision skeleton: %w", err)
	}
	revisedBatch.Notes = append(revisedBatch.Notes, "revision instruction: "+opts.Instruction)
	revisedSkeleton := plannerSkeleton{
		Granularity:        updated.Granularity,
		Status:             domain.AdaptationPlanStatusProposal,
		RewritePolicy:      updated.RewritePolicy,
		Brief:              updated.Brief,
		TargetChapterCount: len(updated.Chapters) + max(0, revisedBatch.TargetTo-to),
		MainlineRules:      append([]string(nil), updated.MainlineRules...),
		RelationshipGoals:  append([]string(nil), updated.RelationshipGoals...),
		Batches:            []plannerSkeletonBatch{revisedBatch},
		Planner:            clonePlannerRuntimeMeta(updated.Planner),
	}
	detailBatches := plannerDetailBatches([]plannerSkeletonBatch{revisedBatch}, adaptationPlannerRecommendedBatchMax)
	emitAdaptProgress(opts.EmitProgress, StagePlan, 0, len(detailBatches), fmt.Sprintf("第 %d 卷剧情重规划完成：第 %d-%d 章，正在生成详细章节提纲", opts.VolumeIndex, revisedBatch.TargetFrom, revisedBatch.TargetTo), nil)
	revisedChapters := make([]domain.AdaptationChapterPlan, 0, revisedBatch.TargetTo-revisedBatch.TargetFrom+1)
	detailOpts := proposalOptionsFromPlan(updated)
	for idx, detailBatch := range detailBatches {
		batchPrompt, err := buildAdaptationPlannerBatchUserPrompt(detailOpts, manifest, sourceFoundation, revisedSkeleton, detailBatch, reportsForPlannerBatch(reports, detailBatch), revisedChapters)
		if err != nil {
			return nil, err
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, idx+1, len(detailBatches), fmt.Sprintf("请求第 %d 卷章节详情第 %d/%d 批：第 %d-%d 章", opts.VolumeIndex, idx+1, len(detailBatches), detailBatch.TargetFrom, detailBatch.TargetTo), nil)
		batchText, err := generatePlannerText(
			ctx,
			deps.LLM,
			systemPrompt,
			batchPrompt,
			adaptationPlannerMaxTokens,
			opts.EmitProgress,
			idx+1,
			len(detailBatches),
			fmt.Sprintf("第 %d 卷章节详情第 %d/%d 批", opts.VolumeIndex, idx+1, len(detailBatches)),
			deps.modelCallMaxAttempts(),
		)
		if err != nil {
			return nil, fmt.Errorf("planner volume revision detail %d-%d llm generate: %w", detailBatch.TargetFrom, detailBatch.TargetTo, err)
		}
		batchChapters, err := collectPlannerBatchChaptersWithRepair(
			ctx,
			deps.LLM,
			systemPrompt,
			batchPrompt,
			batchText,
			detailBatch,
			plannerBatchChapterValidator(detailOpts, manifest, detailBatch),
			opts.EmitProgress,
			idx+1,
			len(detailBatches),
			fmt.Sprintf("第 %d 卷章节详情第 %d/%d 批", opts.VolumeIndex, idx+1, len(detailBatches)),
			deps.structureRepairMaxAttempts(),
			deps.modelCallMaxAttempts(),
		)
		if err != nil {
			return nil, fmt.Errorf("planner volume revision detail %d-%d: %w", detailBatch.TargetFrom, detailBatch.TargetTo, err)
		}
		revisedChapters = append(revisedChapters, batchChapters...)
		emitAdaptProgress(opts.EmitProgress, StagePlan, idx+1, len(detailBatches), fmt.Sprintf("第 %d 卷章节详情第 %d/%d 批完成：第 %d-%d 章", opts.VolumeIndex, idx+1, len(detailBatches), detailBatch.TargetFrom, detailBatch.TargetTo), nil)
	}
	if _, err := replaceProposalChapterRange(&updated, from, to, revisedBatch.TargetTo, revisedChapters); err != nil {
		return nil, err
	}
	applyProposalVolumeRevisionMetadata(&updated, opts.VolumeIndex, revisedBatch)
	return finalizeRevisedAdaptationProposal(deps, opts, proposal, updated, from, to, reports, manifest, len(detailBatches), len(detailBatches))
}

func finalizeRevisedAdaptationProposal(
	deps Deps,
	opts ProposalRevisionOptions,
	original domain.AdaptationPlan,
	updated domain.AdaptationPlan,
	from int,
	to int,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	progressCurrent int,
	progressTotal int,
) (*domain.AdaptationPlan, error) {
	validateOpts := proposalOptionsFromPlan(updated)
	updated.SourceTotalRunes = 0
	updated.TargetTotalRunes = 0
	updated.TargetMinRunes = 0
	updated.TargetMaxRunes = 0
	emitAdaptProgress(opts.EmitProgress, StagePlan, progressCurrent, progressTotal, "修订章节已合并，正在校验完整提案", nil)
	if err := validatePlannerProposal(&updated, validateOpts, reports, manifest, deps.LLM); err != nil {
		return nil, fmt.Errorf("revised adaptation proposal invalid: %w", err)
	}
	updated.Volumes = normalizeAdaptationProposalVolumes(updated.Volumes, len(updated.Chapters))
	if !proposalRevisionChanged(original, updated) {
		return nil, fmt.Errorf("revision produced no proposal changes; please make the instruction more specific or request added ending chapters")
	}
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
	targetChapterCount := plannerTargetChapterCount(opts, manifest)
	if shouldUseChunkedPlanner(opts, manifest, targetChapterCount) {
		return buildPlanFromPlannerChunked(ctx, deps, opts, reports, manifest, sourceFoundation, targetChapterCount)
	}
	return buildPlanFromPlannerSingle(ctx, deps, opts, reports, manifest, sourceFoundation)
}

func shouldUseChunkedPlanner(opts ProposalOptions, manifest *domain.AdaptationSourceManifest, targetChapterCount int) bool {
	switch domain.NormalizeAdaptationGranularity(opts.Granularity) {
	case domain.AdaptationGranularityChapter:
		return false
	case domain.AdaptationGranularityArc, domain.AdaptationGranularityFree:
		if targetChapterCount >= adaptationPlannerChunkedMinChapters {
			return true
		}
		return plannerInputRequiresChunking(manifest)
	default:
		return false
	}
}

func plannerInputRequiresChunking(manifest *domain.AdaptationSourceManifest) bool {
	if manifest == nil {
		return false
	}
	if manifest.ChapterCount >= adaptationPlannerSourceChunkedMin {
		return true
	}
	return plannerManifestTotalRunes(manifest) >= CoCreateDossierBatchRuneLimit
}

func plannerManifestTotalRunes(manifest *domain.AdaptationSourceManifest) int {
	if manifest == nil {
		return 0
	}
	total := 0
	for _, chapter := range manifest.Chapters {
		if chapter.Runes > 0 {
			total += chapter.Runes
		}
	}
	return total
}

func plannerTargetChapterCount(opts ProposalOptions, manifest *domain.AdaptationSourceManifest) int {
	if explicit := normalizeTargetChapterCount(opts.TargetChapterCount, inferTargetChapterCount(opts.Brief)); explicit > 0 {
		return explicit
	}
	if manifest == nil || manifest.ChapterCount < adaptationPlannerSourceChunkedMin {
		return 0
	}
	switch domain.NormalizeAdaptationGranularity(opts.Granularity) {
	case domain.AdaptationGranularityArc, domain.AdaptationGranularityFree:
		return max(manifest.ChapterCount, adaptationPlannerChunkedMinChapters)
	default:
		return 0
	}
}

func plannerTargetChapterHintRole(opts ProposalOptions, manifest *domain.AdaptationSourceManifest, targetChapterHint int) string {
	if targetChapterHint <= 0 {
		return ""
	}
	if explicit := normalizeTargetChapterCount(opts.TargetChapterCount, inferTargetChapterCount(opts.Brief)); explicit > 0 {
		return "explicit_target_scale"
	}
	if manifest != nil && manifest.ChapterCount > 0 && targetChapterHint == manifest.ChapterCount {
		return "source_scale_minimum"
	}
	return "long_form_scale_hint"
}

func plannerSkeletonRequestMessage(opts ProposalOptions, manifest *domain.AdaptationSourceManifest, targetChapterHint int) string {
	switch plannerTargetChapterHintRole(opts, manifest, targetChapterHint) {
	case "source_scale_minimum":
		return fmt.Sprintf("请求长篇改编骨架规划：源书 %d 章，模型将决定目标章数", targetChapterHint)
	case "explicit_target_scale":
		return fmt.Sprintf("请求长篇改编骨架规划：目标规模参考 %d 章", targetChapterHint)
	default:
		return fmt.Sprintf("请求长篇改编骨架规划：长篇规模参考 %d 章", targetChapterHint)
	}
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

func proposalRevisionVolumeBatch(plan domain.AdaptationPlan, volumeIndex, from, to int) plannerSkeletonBatch {
	batch := proposalRevisionBatch(plan, from, to)
	batch.Index = volumeIndex
	for _, volume := range normalizeAdaptationProposalVolumes(plan.Volumes, len(plan.Chapters)) {
		if volume.Index != volumeIndex {
			continue
		}
		batch.Title = volume.Title
		batch.Theme = volume.Theme
		batch.Goal = volume.Goal
		batch.Summary = volume.Summary
		if volume.SourceFrom > 0 {
			batch.SourceFrom = volume.SourceFrom
		}
		if volume.SourceTo > 0 {
			batch.SourceTo = volume.SourceTo
		}
		return batch
	}
	return batch
}

func volumeReviewBatch(review domain.AdaptationVolumeReview, volumeIndex int) (plannerSkeletonBatch, error) {
	volumes := normalizeAdaptationProposalVolumes(review.Volumes, review.TargetChapterCount)
	for _, volume := range volumes {
		if volume.Index != volumeIndex {
			continue
		}
		return plannerSkeletonBatch{
			Index:              volume.Index,
			Title:              volume.Title,
			Theme:              volume.Theme,
			Goal:               volume.Goal,
			Summary:            volume.Summary,
			TargetFrom:         volume.TargetFrom,
			TargetTo:           volume.TargetTo,
			TargetChapterCount: volume.TargetTo - volume.TargetFrom + 1,
			SourceFrom:         volume.SourceFrom,
			SourceTo:           volume.SourceTo,
		}, nil
	}
	return plannerSkeletonBatch{}, fmt.Errorf("volume %d not found in adaptation volume review", volumeIndex)
}

func cloneAdaptationVolumeReview(review domain.AdaptationVolumeReview) domain.AdaptationVolumeReview {
	out := review
	out.MainlineRules = append([]string(nil), review.MainlineRules...)
	out.RelationshipGoals = append([]string(nil), review.RelationshipGoals...)
	out.Volumes = append([]domain.AdaptationVolumePlan(nil), review.Volumes...)
	out.Planner = clonePlannerRuntimeMeta(review.Planner)
	return out
}

func applyVolumeReviewBatchRevision(review *domain.AdaptationVolumeReview, original, revised plannerSkeletonBatch) {
	if review == nil {
		return
	}
	delta := revised.TargetTo - original.TargetTo
	for idx := range review.Volumes {
		volume := &review.Volumes[idx]
		switch {
		case volume.Index == original.Index:
			if title := strings.TrimSpace(revised.Title); title != "" {
				volume.Title = title
			}
			if theme := strings.TrimSpace(revised.Theme); theme != "" {
				volume.Theme = theme
			}
			if goal := strings.TrimSpace(revised.Goal); goal != "" {
				volume.Goal = goal
			}
			if summary := strings.TrimSpace(revised.Summary); summary != "" {
				volume.Summary = summary
			}
			volume.TargetFrom = revised.TargetFrom
			volume.TargetTo = revised.TargetTo
			if revised.SourceFrom > 0 {
				volume.SourceFrom = revised.SourceFrom
			}
			if revised.SourceTo > 0 {
				volume.SourceTo = revised.SourceTo
			}
		case volume.TargetFrom > original.TargetTo:
			volume.TargetFrom += delta
			volume.TargetTo += delta
		}
	}
	review.TargetChapterCount += delta
	if review.TargetChapterCount < 0 {
		review.TargetChapterCount = revised.TargetTo
	}
	review.Volumes = normalizeAdaptationProposalVolumes(review.Volumes, review.TargetChapterCount)
	review.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
}

func validateAdaptationVolumeReview(review domain.AdaptationVolumeReview, manifest *domain.AdaptationSourceManifest) error {
	if review.Status == "" {
		review.Status = domain.AdaptationPlanStatusVolumeReview
	}
	if review.Status != domain.AdaptationPlanStatusVolumeReview {
		return fmt.Errorf("volume review status=%q, want volume_review", review.Status)
	}
	if strings.TrimSpace(review.Brief) == "" {
		return fmt.Errorf("volume review brief is empty")
	}
	if review.TargetChapterCount <= 0 {
		return fmt.Errorf("volume review target_chapter_count must be > 0")
	}
	if !adaptationVolumesCoverChapters(review.Volumes, review.TargetChapterCount) {
		return fmt.Errorf("volume review volumes must continuously cover chapters 1-%d", review.TargetChapterCount)
	}
	if manifest != nil && manifest.ChapterCount > 0 {
		for _, volume := range review.Volumes {
			if volume.SourceFrom <= 0 || volume.SourceTo < volume.SourceFrom || volume.SourceTo > manifest.ChapterCount {
				return fmt.Errorf("volume %d has invalid source range %d-%d", volume.Index, volume.SourceFrom, volume.SourceTo)
			}
		}
	}
	return nil
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

func proposalOptionsFromVolumeReview(review domain.AdaptationVolumeReview) ProposalOptions {
	granularity := domain.NormalizeAdaptationGranularity(review.Granularity)
	return ProposalOptions{
		Brief:              strings.TrimSpace(review.Brief),
		SourcePath:         strings.TrimSpace(review.SourcePath),
		Granularity:        granularity,
		RewritePolicy:      domain.AdaptationRewritePolicyForGranularity(granularity),
		WordTolerance:      review.WordTolerance,
		TargetChapterCount: review.TargetChapterCount,
	}
}

func plannerSkeletonFromVolumeReview(review domain.AdaptationVolumeReview) plannerSkeleton {
	batches := make([]plannerSkeletonBatch, 0, len(review.Volumes))
	for _, volume := range normalizeAdaptationProposalVolumes(review.Volumes, review.TargetChapterCount) {
		batches = append(batches, plannerSkeletonBatch{
			Index:              volume.Index,
			Title:              volume.Title,
			Theme:              volume.Theme,
			Goal:               volume.Goal,
			Summary:            volume.Summary,
			TargetFrom:         volume.TargetFrom,
			TargetTo:           volume.TargetTo,
			TargetChapterCount: volume.TargetTo - volume.TargetFrom + 1,
			SourceFrom:         volume.SourceFrom,
			SourceTo:           volume.SourceTo,
		})
	}
	return plannerSkeleton{
		Granularity:        review.Granularity,
		Status:             domain.AdaptationPlanStatusProposal,
		RewritePolicy:      review.RewritePolicy,
		Brief:              review.Brief,
		TargetChapterCount: review.TargetChapterCount,
		MainlineRules:      append([]string(nil), review.MainlineRules...),
		RelationshipGoals:  append([]string(nil), review.RelationshipGoals...),
		Batches:            batches,
		Planner:            clonePlannerRuntimeMeta(review.Planner),
	}
}

func replaceProposalChapterRange(plan *domain.AdaptationPlan, from, to, maxTo int, chapters []domain.AdaptationChapterPlan) (int, error) {
	if plan == nil {
		return 0, fmt.Errorf("proposal is nil")
	}
	if maxTo < to {
		maxTo = to
	}
	minCount := to - from + 1
	maxCount := maxTo - from + 1
	if len(chapters) < minCount || len(chapters) > maxCount {
		if minCount == maxCount {
			return 0, fmt.Errorf("revised chapter count=%d, want %d", len(chapters), minCount)
		}
		return 0, fmt.Errorf("revised chapter count=%d, want %d-%d", len(chapters), minCount, maxCount)
	}
	revisedTo := from + len(chapters) - 1
	for idx := range chapters {
		want := from + idx
		if chapters[idx].Chapter != want {
			return 0, fmt.Errorf("revised chapter %d at index %d, want %d", chapters[idx].Chapter, idx, want)
		}
	}
	delta := len(chapters) - minCount
	next := make([]domain.AdaptationChapterPlan, 0, len(plan.Chapters)+delta)
	for _, existing := range plan.Chapters {
		if existing.Chapter < from {
			next = append(next, cloneAdaptationChapterPlan(existing))
		}
	}
	for _, revised := range chapters {
		next = append(next, cloneAdaptationChapterPlan(revised))
	}
	for _, existing := range plan.Chapters {
		if existing.Chapter <= to {
			continue
		}
		shifted := cloneAdaptationChapterPlan(existing)
		shiftAdaptationChapterPlanNumber(&shifted, delta)
		next = append(next, shifted)
	}
	sort.SliceStable(next, func(i, j int) bool {
		return next[i].Chapter < next[j].Chapter
	})
	plan.Chapters = next
	shiftProposalVolumesForReplacement(plan, from, to, revisedTo, delta)
	return revisedTo, nil
}

func shiftAdaptationChapterPlanNumber(chapter *domain.AdaptationChapterPlan, delta int) {
	if chapter == nil || delta == 0 {
		return
	}
	chapter.Chapter += delta
	if chapter.OutlineEntry.Chapter > 0 {
		chapter.OutlineEntry.Chapter += delta
	}
}

func shiftProposalVolumesForReplacement(plan *domain.AdaptationPlan, from, to, revisedTo, delta int) {
	if plan == nil || len(plan.Volumes) == 0 {
		return
	}
	for idx := range plan.Volumes {
		volume := &plan.Volumes[idx]
		switch {
		case volume.TargetFrom == from && volume.TargetTo == to:
			volume.TargetTo = revisedTo
		case volume.TargetFrom > to:
			volume.TargetFrom += delta
			volume.TargetTo += delta
		case volume.TargetFrom <= from && volume.TargetTo >= to:
			volume.TargetTo += delta
		case volume.TargetTo > to:
			volume.TargetTo += delta
		}
		if volume.TargetFrom > 0 && volume.TargetTo >= volume.TargetFrom {
			sourceFrom, sourceTo := sourceRangeForProposalChapters(plan.Chapters, volume.TargetFrom, volume.TargetTo)
			if sourceFrom > 0 {
				volume.SourceFrom = sourceFrom
			}
			if sourceTo > 0 {
				volume.SourceTo = sourceTo
			}
		}
	}
}

func applyProposalVolumeRevisionMetadata(plan *domain.AdaptationPlan, volumeIndex int, batch plannerSkeletonBatch) {
	if plan == nil || volumeIndex <= 0 {
		return
	}
	for idx := range plan.Volumes {
		if plan.Volumes[idx].Index != volumeIndex {
			continue
		}
		volume := &plan.Volumes[idx]
		if title := strings.TrimSpace(batch.Title); title != "" {
			volume.Title = title
		}
		if theme := strings.TrimSpace(batch.Theme); theme != "" {
			volume.Theme = theme
		}
		if goal := strings.TrimSpace(batch.Goal); goal != "" {
			volume.Goal = goal
		}
		if summary := strings.TrimSpace(batch.Summary); summary != "" {
			volume.Summary = summary
		}
		volume.TargetFrom = batch.TargetFrom
		volume.TargetTo = batch.TargetTo
		if batch.SourceFrom > 0 {
			volume.SourceFrom = batch.SourceFrom
		}
		if batch.SourceTo > 0 {
			volume.SourceTo = batch.SourceTo
		}
		return
	}
}

func shouldAllowProposalRevisionExpansion(proposal domain.AdaptationPlan, opts ProposalRevisionOptions, from, to int) bool {
	if len(proposal.Chapters) == 0 {
		return false
	}
	if from <= 0 || from > to {
		return false
	}
	if opts.VolumeIndex > 0 {
		return true
	}
	if opts.VolumeIndex <= 0 && to != len(proposal.Chapters) {
		return false
	}
	return proposalRevisionInstructionRequestsExpansion(opts.Instruction)
}

func proposalRevisionInstructionRequestsExpansion(instruction string) bool {
	text := strings.ToLower(strings.TrimSpace(instruction))
	if text == "" {
		return false
	}
	keywords := []string{
		"add chapter",
		"add chapters",
		"append chapter",
		"append chapters",
		"extra chapter",
		"extra chapters",
		"new chapter",
		"new chapters",
		"extend",
		"expand",
		"epilogue",
		"ending",
		"finale",
		"补充",
		"新增",
		"添加",
		"增加",
		"扩展",
		"扩写",
		"加章",
		"补章",
		"结尾",
		"尾声",
		"终章",
		"收束",
	}
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}

func normalizeProposalVolumeExpansionDecision(decision string) string {
	text := strings.ToLower(strings.TrimSpace(decision))
	if text == "" {
		return ""
	}
	keepWords := []string{
		"keep", "same", "unchanged", "no expansion", "no change", "remain", "fixed",
		"保持", "不变", "不扩", "无需", "原章数", "维持",
	}
	for _, word := range keepWords {
		if strings.Contains(text, word) {
			return "keep"
		}
	}
	expandWords := []string{
		"expand", "expanded", "increase", "increased", "add", "append", "extra", "more", "new",
		"扩章", "增加", "新增", "添加", "加章", "补章", "扩展", "扩写",
	}
	for _, word := range expandWords {
		if strings.Contains(text, word) {
			return "expand"
		}
	}
	return text
}

func proposalRevisionChanged(original, updated domain.AdaptationPlan) bool {
	if len(original.Chapters) != len(updated.Chapters) {
		return true
	}
	for idx := range original.Chapters {
		if !adaptationChapterPlansEqual(original.Chapters[idx], updated.Chapters[idx]) {
			return true
		}
	}
	return false
}

func adaptationChapterPlansEqual(left, right domain.AdaptationChapterPlan) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftJSON) == string(rightJSON)
}

func buildAdaptationProposalRevisionUserPrompt(
	opts ProposalRevisionOptions,
	proposal domain.AdaptationPlan,
	batch plannerSkeletonBatch,
	expansionMaxTo int,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	reports []domain.AdaptationSourceReport,
) (string, error) {
	if expansionMaxTo < batch.TargetTo {
		expansionMaxTo = batch.TargetTo
	}
	expansionAllowed := expansionMaxTo > batch.TargetTo
	selected := proposalChaptersInRange(proposal.Chapters, batch.TargetFrom, batch.TargetTo)
	before := proposalChapterByNumber(proposal.Chapters, batch.TargetFrom-1)
	after := proposalChapterByNumber(proposal.Chapters, batch.TargetTo+1)
	requirements := []string{
		"Return exactly one JSON object and no prose.",
		"The top-level JSON object must be {\"chapters\":[...]} and must not be a single chapter object.",
		"Keep source_chapters anchors valid and preserve essential source events unless the user's instruction explicitly changes emphasis.",
		"Maintain continuity with neighbor_before and neighbor_after.",
		"Every returned chapter must include chapter, title, core_event, hook, scenes, source_chapters, source_range, word_budget, preserve_events, required_changes, and forbidden_moves.",
	}
	outputContract := fmt.Sprintf(
		"Return exactly %d chapter objects, numbered with integer chapter values from %d through %d.",
		batch.TargetTo-batch.TargetFrom+1,
		batch.TargetFrom,
		batch.TargetTo,
	)
	if expansionAllowed {
		requirements = append(requirements,
			"Return the complete selected range and any appended ending chapters as one continuous chapter list.",
			"Existing selected chapters must keep their original chapter numbers.",
			"Append new ending chapters only if needed by the user's instruction.",
			"Appended chapters must continue sequentially after original_target_to and must not exceed target_to_max.",
			"Do not leave chapter-number gaps.",
		)
		outputContract = fmt.Sprintf(
			"Return at least %d and at most %d chapter objects, numbered continuously from %d through the final returned chapter. The final returned chapter must be between %d and %d.",
			batch.TargetTo-batch.TargetFrom+1,
			expansionMaxTo-batch.TargetFrom+1,
			batch.TargetFrom,
			batch.TargetTo,
			expansionMaxTo,
		)
	} else {
		requirements = append(requirements,
			"Return only the selected target chapters, but return the complete selected range.",
			"Do not change chapter numbers or chapter count.",
			"Use integer chapter values from target_from through target_to.",
		)
	}
	input := struct {
		Instruction       string                             `json:"instruction"`
		TargetFrom        int                                `json:"target_from"`
		TargetTo          int                                `json:"target_to"`
		ExpansionAllowed  bool                               `json:"expansion_allowed,omitempty"`
		OriginalTargetTo  int                                `json:"original_target_to,omitempty"`
		TargetToMax       int                                `json:"target_to_max,omitempty"`
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
		ExpansionAllowed:  expansionAllowed,
		OriginalTargetTo:  batch.TargetTo,
		TargetToMax:       expansionMaxTo,
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
		Requirements:      requirements,
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal proposal revision input: %w", err)
	}
	return fmt.Sprintf(
		"Revise the selected adaptation proposal chapters using the user's instruction. Keep the rest of the proposal unchanged.\n\n"+
			"Output contract: return exactly one JSON object, no prose and no markdown. The top-level object must be {\"chapters\":[...]}.\n"+
			"%s\n"+
			"Invalid shapes: {\"chapter\":%d,...}; {\"summary\":\"...\"}; {\"key_turns\":[...]}; markdown text outside JSON.\n\n"+
			"Revision input:\n```json\n%s\n```",
		outputContract,
		batch.TargetFrom,
		string(raw),
	), nil
}

func buildAdaptationProposalVolumeRevisionSkeletonPrompt(
	opts ProposalRevisionOptions,
	proposal domain.AdaptationPlan,
	volume plannerSkeletonBatch,
	expansionMaxTo int,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	reports []domain.AdaptationSourceReport,
) (string, error) {
	if expansionMaxTo < volume.TargetTo {
		expansionMaxTo = volume.TargetTo
	}
	expansionAllowed := expansionMaxTo > volume.TargetTo
	selected := proposalChaptersInRange(proposal.Chapters, volume.TargetFrom, volume.TargetTo)
	before := proposalChapterByNumber(proposal.Chapters, volume.TargetFrom-1)
	after := proposalChapterByNumber(proposal.Chapters, volume.TargetTo+1)
	requirements := []string{
		"Return exactly one JSON object and no prose.",
		"Do not wrap the JSON in markdown fences.",
		"Return only a high-level revised volume/batch skeleton; do not include chapter details.",
		"The top-level object must contain a batches array with exactly one batch.",
		"That batch must keep target_from equal to the original volume target_from.",
		"That batch must include target_from, target_to, source_from, source_to, title, theme or goal, and summary.",
		"source_from and source_to must stay within the analyzed source manifest.",
		"Use the user's revision instruction to re-plan the volume's plot structure before detailed chapter planning.",
	}
	if expansionAllowed {
		requirements = append(requirements,
			`You must decide whether the revision needs more chapter slots. Set expansion_decision to "expand" or "keep".`,
			`Use "expand" when the requested story change needs added chapters, extra relationship beats, daily romance scenes, epilogue-like life stages, marriage, pregnancy, childbirth, or other new plot space.`,
			`Use "keep" only when the requested change can be fully handled inside the current chapter count without compressing or losing the user's intent.`,
			`If expansion_decision is "expand", increase target_to for this volume; target_to must not exceed target_to_max.`,
			`If expansion_decision is "keep", target_to must remain original_target_to.`,
			"Do not leave gaps; later volumes will be shifted by the application.",
		)
	} else {
		requirements = append(requirements,
			"Do not change target_to or chapter count for this volume.",
		)
	}
	input := struct {
		Instruction       string                             `json:"instruction"`
		ExpansionAllowed  bool                               `json:"expansion_allowed"`
		OriginalTargetTo  int                                `json:"original_target_to"`
		TargetToMax       int                                `json:"target_to_max"`
		Granularity       string                             `json:"granularity"`
		RewritePolicy     string                             `json:"rewrite_policy"`
		Brief             string                             `json:"brief"`
		MainlineRules     []string                           `json:"mainline_rules,omitempty"`
		RelationshipGoals []string                           `json:"relationship_goals,omitempty"`
		CurrentVolume     plannerSkeletonBatch               `json:"current_volume"`
		AllVolumes        []domain.AdaptationVolumePlan      `json:"all_volumes,omitempty"`
		NeighborBefore    *domain.AdaptationChapterPlan      `json:"neighbor_before,omitempty"`
		NeighborAfter     *domain.AdaptationChapterPlan      `json:"neighbor_after,omitempty"`
		SelectedChapters  []domain.AdaptationChapterPlan     `json:"selected_chapters"`
		SourceManifest    *domain.AdaptationSourceManifest   `json:"source_manifest"`
		SourceFoundation  *domain.AdaptationSourceFoundation `json:"source_foundation"`
		SourceReports     []domain.AdaptationSourceReport    `json:"source_reports"`
		Requirements      []string                           `json:"requirements"`
	}{
		Instruction:       strings.TrimSpace(opts.Instruction),
		ExpansionAllowed:  expansionAllowed,
		OriginalTargetTo:  volume.TargetTo,
		TargetToMax:       expansionMaxTo,
		Granularity:       proposal.Granularity,
		RewritePolicy:     proposal.RewritePolicy,
		Brief:             proposal.Brief,
		MainlineRules:     append([]string(nil), proposal.MainlineRules...),
		RelationshipGoals: append([]string(nil), proposal.RelationshipGoals...),
		CurrentVolume:     volume,
		AllVolumes:        normalizeAdaptationProposalVolumes(proposal.Volumes, len(proposal.Chapters)),
		NeighborBefore:    before,
		NeighborAfter:     after,
		SelectedChapters:  selected,
		SourceManifest:    manifest,
		SourceFoundation:  sourceFoundation,
		SourceReports:     reports,
		Requirements:      requirements,
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal volume revision skeleton input: %w", err)
	}
	return fmt.Sprintf(
		"Re-plan the selected adaptation proposal volume before detailed chapter planning.\n\n"+
			"Output contract: return exactly one JSON object, no prose and no markdown. The top-level object must contain a batches array with exactly one revised volume batch. Do not return chapter details here.\n"+
			"Required JSON shape: {\"granularity\":\"%s\",\"status\":\"proposal\",\"rewrite_policy\":\"%s\",\"brief\":\"...\",\"target_chapter_count\":%d,\"batches\":[{\"index\":%d,\"title\":\"...\",\"theme\":\"...\",\"expansion_decision\":\"expand|keep\",\"expansion_reason\":\"...\",\"target_from\":%d,\"target_to\":%d,\"source_from\":%d,\"source_to\":%d,\"summary\":\"...\"}]}.\n\n"+
			"Volume revision input:\n```json\n%s\n```",
		proposal.Granularity,
		proposal.RewritePolicy,
		volume.TargetTo-volume.TargetFrom+1,
		volume.Index,
		volume.TargetFrom,
		volume.TargetTo,
		volume.SourceFrom,
		volume.SourceTo,
		string(raw),
	), nil
}

func buildAdaptationVolumeReviewRevisionPrompt(
	opts ProposalRevisionOptions,
	review domain.AdaptationVolumeReview,
	volume plannerSkeletonBatch,
	expansionMaxTo int,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	reports []domain.AdaptationSourceReport,
) (string, error) {
	if expansionMaxTo < volume.TargetTo {
		expansionMaxTo = volume.TargetTo
	}
	requirements := []string{
		"Return exactly one JSON object and no prose.",
		"Do not wrap the JSON in markdown fences.",
		"Return only a high-level revised volume/batch skeleton; do not include chapter details.",
		"The top-level object must contain a batches array with exactly one batch.",
		"That batch must keep target_from equal to the original volume target_from.",
		"That batch must include target_from, target_to, source_from, source_to, title, theme or goal, summary, and expansion_decision.",
		"source_from and source_to must stay within the analyzed source manifest.",
		"Use the user's revision instruction to re-plan this volume's plot structure before detailed chapter planning.",
		`You must decide whether the revision needs more chapter slots. Set expansion_decision to "expand" or "keep".`,
		`Use "expand" when the requested story change needs added chapters, extra relationship beats, daily romance scenes, epilogue-like life stages, marriage, pregnancy, childbirth, or other new plot space.`,
		`Use "keep" only when the requested change can be fully handled inside the current chapter count without compressing or losing the user's intent.`,
		`If expansion_decision is "expand", increase target_to for this volume; target_to must not exceed target_to_max.`,
		`If expansion_decision is "keep", target_to must remain original_target_to.`,
		"Do not leave gaps; later volumes will be shifted by the application.",
	}
	input := struct {
		Instruction       string                             `json:"instruction"`
		ExpansionAllowed  bool                               `json:"expansion_allowed"`
		OriginalTargetTo  int                                `json:"original_target_to"`
		TargetToMax       int                                `json:"target_to_max"`
		Granularity       string                             `json:"granularity"`
		RewritePolicy     string                             `json:"rewrite_policy"`
		Brief             string                             `json:"brief"`
		MainlineRules     []string                           `json:"mainline_rules,omitempty"`
		RelationshipGoals []string                           `json:"relationship_goals,omitempty"`
		CurrentVolume     plannerSkeletonBatch               `json:"current_volume"`
		AllVolumes        []domain.AdaptationVolumePlan      `json:"all_volumes"`
		SourceManifest    *domain.AdaptationSourceManifest   `json:"source_manifest"`
		SourceFoundation  *domain.AdaptationSourceFoundation `json:"source_foundation"`
		SourceReports     []domain.AdaptationSourceReport    `json:"source_reports"`
		Requirements      []string                           `json:"requirements"`
	}{
		Instruction:       strings.TrimSpace(opts.Instruction),
		ExpansionAllowed:  true,
		OriginalTargetTo:  volume.TargetTo,
		TargetToMax:       expansionMaxTo,
		Granularity:       review.Granularity,
		RewritePolicy:     review.RewritePolicy,
		Brief:             review.Brief,
		MainlineRules:     append([]string(nil), review.MainlineRules...),
		RelationshipGoals: append([]string(nil), review.RelationshipGoals...),
		CurrentVolume:     volume,
		AllVolumes:        normalizeAdaptationProposalVolumes(review.Volumes, review.TargetChapterCount),
		SourceManifest:    manifest,
		SourceFoundation:  sourceFoundation,
		SourceReports:     reports,
		Requirements:      requirements,
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal volume review revision input: %w", err)
	}
	return fmt.Sprintf(
		"Revise the selected adaptation volume review before detailed chapter planning.\n\n"+
			"Output contract: return exactly one JSON object, no prose and no markdown. The top-level object must contain a batches array with exactly one revised volume batch. Do not return chapter details here.\n"+
			"Required JSON shape: {\"granularity\":\"%s\",\"status\":\"volume_review\",\"rewrite_policy\":\"%s\",\"brief\":\"...\",\"target_chapter_count\":%d,\"batches\":[{\"index\":%d,\"title\":\"...\",\"theme\":\"...\",\"expansion_decision\":\"expand|keep\",\"expansion_reason\":\"...\",\"target_from\":%d,\"target_to\":%d,\"source_from\":%d,\"source_to\":%d,\"summary\":\"...\"}]}.\n\n"+
			"Volume review revision input:\n```json\n%s\n```",
		review.Granularity,
		review.RewritePolicy,
		volume.TargetTo-volume.TargetFrom+1,
		volume.Index,
		volume.TargetFrom,
		volume.TargetTo,
		volume.SourceFrom,
		volume.SourceTo,
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
		if value > adaptationPlannerTargetChapterMax {
			return adaptationPlannerTargetChapterMax
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
		if precededByChapterAnchorPrefix(brief, match[0]) {
			continue
		}
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
		if precededByChapterAnchorPrefix(brief, match[0]) {
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
		if precededByChapterAnchorPrefix(brief, match[0]) {
			continue
		}
		high := parseChineseChapterNumber(parseRegexText(brief, match, 2) + "十")
		best = max(best, high)
	}
	for _, match := range targetChapterChinesePattern.FindAllStringSubmatchIndex(brief, -1) {
		if precededByChapterAnchorPrefix(brief, match[0]) {
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

func precededByChapterAnchorPrefix(text string, start int) bool {
	if precededByOrdinalPrefix(text, start) {
		return true
	}
	if start <= 0 || start > len(text) {
		return false
	}
	prefix := strings.ToLower(strings.TrimRightFunc(text[:start], unicode.IsSpace))
	return hasChapterAnchorPrefix(prefix) || precededByChapterAnchorRangeContinuation(prefix)
}

func hasChapterAnchorPrefix(prefix string) bool {
	return strings.HasSuffix(prefix, "第") || strings.HasSuffix(prefix, "ch") || strings.HasSuffix(prefix, "chapter")
}

func precededByChapterAnchorRangeContinuation(prefix string) bool {
	if prefix == "" {
		return false
	}
	trimmed := strings.TrimRightFunc(prefix, unicode.IsSpace)
	if trimmed == "" {
		return false
	}
	last, size := utf8.DecodeLastRuneInString(trimmed)
	if !isChapterRangeSeparator(last) {
		return false
	}
	beforeSeparator := strings.TrimRightFunc(trimmed[:len(trimmed)-size], unicode.IsSpace)
	beforeNumber := strings.TrimRightFunc(beforeSeparator, func(r rune) bool {
		return r >= '0' && r <= '9'
	})
	if beforeNumber == beforeSeparator {
		return false
	}
	return hasChapterAnchorPrefix(strings.TrimRightFunc(beforeNumber, unicode.IsSpace))
}

func isChapterRangeSeparator(r rune) bool {
	switch r {
	case '-', '~', '～', '—', '–', '－', '至', '到':
		return true
	default:
		return false
	}
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
		_ = deps.Store.Adaptation.ClearProposalRuntime()
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
	ExpansionDecision  string   `json:"expansion_decision,omitempty"`
	ExpansionReason    string   `json:"expansion_reason,omitempty"`
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
	b.ExpansionDecision = firstJSONString(object, b.ExpansionDecision, "expansion_decision", "expansionDecision", "chapter_count_decision", "chapterCountDecision")
	b.ExpansionReason = firstJSONString(object, b.ExpansionReason, "expansion_reason", "expansionReason", "chapter_count_reason", "chapterCountReason")
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
	skeleton, runtime, err := buildPlannerVolumeSkeleton(ctx, deps, opts, reports, manifest, sourceFoundation, targetChapterHint)
	if err != nil {
		return zero, err
	}
	return buildPlanFromPlannerSkeletonDetails(ctx, deps, opts, reports, manifest, sourceFoundation, skeleton, runtime)
}

func buildPlannerVolumeSkeleton(
	ctx context.Context,
	deps Deps,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	targetChapterHint int,
) (plannerSkeleton, *domain.AdaptationProposalRuntime, error) {
	var zero plannerSkeleton
	if deps.LLM == nil {
		return zero, nil, fmt.Errorf("planner llm is required for %s adaptation proposals", opts.Granularity)
	}
	systemPrompt := strings.TrimSpace(deps.Prompts.Planner)
	if systemPrompt == "" {
		systemPrompt = "# Adaptation Planner\n\nReturn only JSON for the requested adaptation planning step."
	}
	runtime, runtimeSkeleton, err := loadPlannerProposalRuntime(deps, opts, manifest, targetChapterHint, opts.EmitProgress)
	if err != nil {
		return zero, nil, err
	}
	var skeleton plannerSkeleton
	if runtimeSkeleton != nil {
		skeleton = *runtimeSkeleton
		emitAdaptProgress(opts.EmitProgress, StagePlan, 0, 0, fmt.Sprintf("Resuming proposal skeleton runtime: %d target chapters", skeleton.TargetChapterCount), nil)
	} else {
		dossier, err := EnsureCoCreateDossier(ctx, deps, manifest, reports, opts.EmitProgress)
		if err != nil {
			return zero, nil, fmt.Errorf("prepare planner source map: %w", err)
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, 0, 0, plannerSkeletonRequestMessage(opts, manifest, targetChapterHint), nil)
		skeleton, err = buildPlannerVolumeSkeletonFromSourceMap(ctx, deps, opts, manifest, sourceFoundation, dossier, runtime, targetChapterHint, systemPrompt)
		if err != nil {
			return zero, nil, err
		}
		runtime.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
		runtime.CompletedBatches = nil
		runtime.SkeletonBatches = nil
		if err := savePlannerProposalRuntime(deps, runtime); err != nil {
			return zero, nil, fmt.Errorf("save proposal runtime skeleton: %w", err)
		}
	}
	return skeleton, runtime, nil
}

func buildPlannerVolumeSkeletonFromSourceMap(
	ctx context.Context,
	deps Deps,
	opts ProposalOptions,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	dossier *domain.AdaptationCoCreateDossier,
	runtime *domain.AdaptationProposalRuntime,
	targetChapterHint int,
	systemPrompt string,
) (plannerSkeleton, error) {
	sourceMap := plannerSourceMapFromDossier(dossier, manifest)
	if len(sourceMap) == 0 {
		return plannerSkeleton{}, fmt.Errorf("planner source map is empty")
	}
	batches := make([]plannerSkeletonBatch, 0, len(sourceMap))
	for sourceIndex, entry := range sourceMap {
		if err := ctx.Err(); err != nil {
			return plannerSkeleton{}, err
		}
		if reused, ok := plannerRuntimeSkeletonBatchesForSource(runtime, entry); ok {
			batches = append(batches, reused...)
			emitAdaptProgress(opts.EmitProgress, StagePlan, sourceIndex+1, len(sourceMap), fmt.Sprintf("复用骨架规划第 %d/%d 批：原书第 %d-%d 章", sourceIndex+1, len(sourceMap), entry.SourceFrom, entry.SourceTo), nil)
			continue
		}
		prompt, err := buildAdaptationPlannerSkeletonUserPrompt(opts, manifest, sourceFoundation, []plannerSourceMapEntry{entry}, targetChapterHint)
		if err != nil {
			return plannerSkeleton{}, err
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, sourceIndex+1, len(sourceMap), fmt.Sprintf("请求骨架规划第 %d/%d 批：原书第 %d-%d 章", sourceIndex+1, len(sourceMap), entry.SourceFrom, entry.SourceTo), nil)
		text, err := generatePlannerText(
			ctx,
			deps.LLM,
			systemPrompt,
			prompt,
			adaptationPlannerSkeletonMaxTokens,
			opts.EmitProgress,
			sourceIndex+1,
			len(sourceMap),
			fmt.Sprintf("骨架规划第 %d/%d 批", sourceIndex+1, len(sourceMap)),
			deps.modelCallMaxAttempts(),
		)
		if err != nil {
			return plannerSkeleton{}, fmt.Errorf("planner skeleton batch %d llm generate: %w", entry.Index, err)
		}
		local, err := collectPlannerSourceMapSkeletonBatches(ctx, deps.LLM, systemPrompt, prompt, text, entry, opts.EmitProgress, sourceIndex+1, len(sourceMap), deps.structureRepairMaxAttempts(), deps.modelCallMaxAttempts())
		if err != nil {
			return plannerSkeleton{}, fmt.Errorf("planner skeleton batch %d: %w", entry.Index, err)
		}
		nextTarget := nextPlannerSkeletonTarget(batches)
		offsetPlannerSkeletonBatches(local, nextTarget, len(batches)+1)
		batches = append(batches, local...)
		upsertPlannerProposalRuntimeSkeletonBatches(runtime, entry, local)
		if err := savePlannerProposalRuntime(deps, runtime); err != nil {
			return plannerSkeleton{}, fmt.Errorf("save proposal runtime skeleton batch %d: %w", entry.Index, err)
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, sourceIndex+1, len(sourceMap), fmt.Sprintf("骨架规划第 %d/%d 批完成：新增目标第 %d-%d 章", sourceIndex+1, len(sourceMap), local[0].TargetFrom, local[len(local)-1].TargetTo), nil)
	}
	skeleton := plannerSkeleton{
		Granularity:        opts.Granularity,
		Status:             domain.AdaptationPlanStatusProposal,
		RewritePolicy:      opts.RewritePolicy,
		Brief:              opts.Brief,
		TargetChapterCount: nextPlannerSkeletonTarget(batches) - 1,
		Batches:            batches,
		Planner: &domain.AdaptationPlannerMeta{
			Prompt:        adaptationPlannerPromptName,
			PromptVersion: adaptationPlannerPromptVersion + "-source-map",
			GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
			Notes: domain.TextList{
				fmt.Sprintf("source-map skeleton: %d source batches; model chose %d target chapters", len(sourceMap), nextPlannerSkeletonTarget(batches)-1),
			},
		},
	}
	if err := normalizePlannerSkeleton(&skeleton, opts, manifest, targetChapterHint); err != nil {
		return plannerSkeleton{}, fmt.Errorf("planner skeleton: %w", err)
	}
	return skeleton, nil
}

func collectPlannerSourceMapSkeletonBatches(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	initialText string,
	entry plannerSourceMapEntry,
	emit ProgressEmitter,
	current int,
	total int,
	maxRepairAttempts int,
	maxModelCallAttempts int,
) ([]plannerSkeletonBatch, error) {
	if maxRepairAttempts <= 0 {
		maxRepairAttempts = adaptationPlannerRepairMaxAttempts
	}
	text := initialText
	var lastErr error
	qualityAttempts := 0
	structureAttempts := 0
	for {
		skeleton, err := parsePlannerSourceMapSkeleton(text)
		if err == nil {
			batches, berr := normalizePlannerSourceMapSkeletonBatches(skeleton.Batches, entry)
			if berr == nil {
				return batches, nil
			}
			var budgetErr *plannerChapterBudgetQualityError
			if errors.As(berr, &budgetErr) {
				lastErr = budgetErr
				if qualityAttempts >= maxRepairAttempts {
					accepted, acceptErr := normalizePlannerSourceMapSkeletonBatchesAllowBudgetDeviation(skeleton.Batches, entry)
					if acceptErr == nil {
						emitAdaptProgress(emit, StagePlan, current, total, fmt.Sprintf("骨架规划第 %d/%d 批章节预算连续偏离预期，已按模型改编判断继续：%v", current, total, lastErr), lastErr)
						return accepted, nil
					}
					return nil, acceptErr
				}
				qualityAttempts++
				emitAdaptProgress(emit, StagePlan, current, total, fmt.Sprintf("骨架规划第 %d/%d 批章节预算偏离预期，质量重试第 %d/%d 次：%v", current, total, qualityAttempts, maxRepairAttempts, lastErr), lastErr)
				reconsidered, err := retryPlannerSkeletonChapterBudget(ctx, llm, systemPrompt, originalPrompt, text, lastErr, emit, current, total, maxModelCallAttempts)
				if err != nil {
					return nil, err
				}
				text = reconsidered
				continue
			}
			err = berr
		}
		lastErr = err
		if !plannerSkeletonErrorRepairable(err) || structureAttempts >= maxRepairAttempts {
			return nil, lastErr
		}
		structureAttempts++
		emitAdaptProgress(emit, StagePlan, current, total, fmt.Sprintf("骨架规划第 %d/%d 批结构无效，正在修复第 %d/%d 次：%v", current, total, structureAttempts, maxRepairAttempts, lastErr), lastErr)
		repaired, err := repairPlannerSkeletonText(ctx, llm, systemPrompt, originalPrompt, text, lastErr, emit, current, total, maxModelCallAttempts)
		if err != nil {
			return nil, err
		}
		qualityAttempts = 0
		text = repaired
	}
}

func parsePlannerSourceMapSkeleton(text string) (plannerSkeleton, error) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return plannerSkeleton{}, fmt.Errorf("planner source-map skeleton must be one JSON object with no prose or markdown")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil {
		return plannerSkeleton{}, fmt.Errorf("parse planner source-map skeleton JSON: %w", err)
	}
	rawBatches := object["batches"]
	if len(rawBatches) == 0 {
		return plannerSkeleton{}, fmt.Errorf("planner source-map skeleton missing top-level batches array")
	}
	if rawBatches[0] != '[' {
		return plannerSkeleton{}, fmt.Errorf("planner source-map skeleton batches must be an array")
	}
	var skeleton plannerSkeleton
	if err := json.Unmarshal([]byte(trimmed), &skeleton); err != nil {
		return plannerSkeleton{}, fmt.Errorf("decode planner source-map skeleton JSON: %w", err)
	}
	if len(skeleton.Batches) == 0 {
		return plannerSkeleton{}, fmt.Errorf("planner source-map skeleton has empty batches array")
	}
	return skeleton, nil
}

type plannerChapterBudgetQualityError struct {
	BatchIndex  int
	Count       int
	MinCount    int
	MaxCount    int
	SourceFrom  int
	SourceTo    int
	SourceRunes int
	Direction   string
}

func (e *plannerChapterBudgetQualityError) Error() string {
	if e == nil {
		return "chapter budget quality review required"
	}
	switch e.Direction {
	case "low":
		if e.SourceRunes > 0 {
			return fmt.Sprintf("source-map range %d-%d has %d source_runes but skeleton produced %d target chapters; expected at least %d target chapters to keep each chapter within %d runes",
				e.SourceFrom, e.SourceTo, e.SourceRunes, e.Count, e.MinCount, adaptationPlannerModelChapterMaxRunes)
		}
		return fmt.Sprintf("batch %d chapter_count=%d is below expected review floor %d for source range %d-%d", e.BatchIndex, e.Count, e.MinCount, e.SourceFrom, e.SourceTo)
	case "high":
		return fmt.Sprintf("batch %d chapter_count=%d is above expected review ceiling %d for source range %d-%d", e.BatchIndex, e.Count, e.MaxCount, e.SourceFrom, e.SourceTo)
	default:
		return fmt.Sprintf("batch %d chapter_count=%d needs budget quality review for source range %d-%d", e.BatchIndex, e.Count, e.SourceFrom, e.SourceTo)
	}
}

func plannerChapterBudgetRepairInstructions(err error) []string {
	var budgetErr *plannerChapterBudgetQualityError
	if !errors.As(err, &budgetErr) || budgetErr == nil {
		return nil
	}
	switch budgetErr.Direction {
	case "low":
		instructions := []string{
			fmt.Sprintf("The previous budget review says source range %d-%d may be under-planned: if this range keeps, expands, or closely rewrites the source material, the sum of chapter_count across returned batches covering that range should be at least %d.", budgetErr.SourceFrom, budgetErr.SourceTo, budgetErr.MinCount),
			"If this adaptation intentionally deletes, merges, or compresses the source material, a lower chapter_count is allowed, but the batch summary must explicitly state that compression/deletion rationale.",
		}
		if budgetErr.SourceRunes > 0 {
			instructions = append(instructions, fmt.Sprintf("That range has source_runes=%d, so the source-size estimate points to about %d target chapters when preserving detail; treat this as a review target, not a hard lower bound when the plan clearly compresses or deletes content.", budgetErr.SourceRunes, budgetErr.MinCount))
		}
		return instructions
	case "high":
		return []string{
			fmt.Sprintf("The previous budget review says source range %d-%d is over-planned: reduce chapter_count toward at most %d unless the batch summary gives a concrete added-plot or long-chapter split reason.", budgetErr.SourceFrom, budgetErr.SourceTo, budgetErr.MaxCount),
			"Do not repeat the full source budget into every target chapter; each target chapter should own a distinct slice of the adapted material.",
		}
	default:
		return []string{
			fmt.Sprintf("Rebalance chapter_count for source range %d-%d according to the previous budget review error.", budgetErr.SourceFrom, budgetErr.SourceTo),
		}
	}
}

func plannerSourceMapBudgetNotes(entries []plannerSourceMapEntry) []string {
	notes := make([]string, 0, len(entries))
	for _, entry := range entries {
		minTargetCount := plannerSourceMapBudgetMinTargetChapters(entry)
		if minTargetCount <= 1 || entry.SourceRunes <= adaptationPlannerModelChapterMaxRunes {
			continue
		}
		notes = append(notes, fmt.Sprintf(
			"source_map entry %d range %d-%d has source_runes=%d; if this range keeps, expands, or closely rewrites source detail, returned batches covering this range should total at least %d chapter_count so target chapters can stay within %d runes. If the adaptation intentionally deletes, merges, or compresses this material, a lower chapter_count is allowed, but batch summaries must state the compression/deletion rationale.",
			entry.Index,
			entry.SourceFrom,
			entry.SourceTo,
			entry.SourceRunes,
			minTargetCount,
			adaptationPlannerModelChapterMaxRunes,
		))
	}
	return notes
}

func normalizePlannerSourceMapSkeletonBatches(batches []plannerSkeletonBatch, entry plannerSourceMapEntry) ([]plannerSkeletonBatch, error) {
	return normalizePlannerSourceMapSkeletonBatchesWithOptions(batches, entry, false)
}

func normalizePlannerSourceMapSkeletonBatchesAllowBudgetDeviation(batches []plannerSkeletonBatch, entry plannerSourceMapEntry) ([]plannerSkeletonBatch, error) {
	return normalizePlannerSourceMapSkeletonBatchesWithOptions(batches, entry, true)
}

func normalizePlannerSourceMapSkeletonBatchesWithOptions(batches []plannerSkeletonBatch, entry plannerSourceMapEntry, allowBudgetDeviation bool) ([]plannerSkeletonBatch, error) {
	if len(batches) == 0 {
		return nil, fmt.Errorf("no batches")
	}
	out := make([]plannerSkeletonBatch, 0, len(batches))
	for idx, batch := range batches {
		if batch.SourceFrom <= 0 || batch.SourceTo <= 0 {
			return nil, fmt.Errorf("batch %d must include source_from and source_to", idx+1)
		}
		if batch.SourceTo < entry.SourceFrom || batch.SourceFrom > entry.SourceTo {
			continue
		}
		if strings.TrimSpace(batch.Title) == "" {
			return nil, fmt.Errorf("batch %d title is empty", idx+1)
		}
		if strings.TrimSpace(batch.Theme) == "" && strings.TrimSpace(batch.Goal) == "" {
			return nil, fmt.Errorf("batch %d theme or goal is required", idx+1)
		}
		if strings.TrimSpace(batch.Summary) == "" {
			return nil, fmt.Errorf("batch %d summary is empty", idx+1)
		}
		if batch.SourceFrom < entry.SourceFrom || batch.SourceTo > entry.SourceTo || batch.SourceTo < batch.SourceFrom {
			return nil, fmt.Errorf("batch %d source range %d-%d outside source-map range %d-%d", idx+1, batch.SourceFrom, batch.SourceTo, entry.SourceFrom, entry.SourceTo)
		}
		count := batch.TargetChapterCount
		if batch.TargetFrom > 0 && batch.TargetTo >= batch.TargetFrom {
			if count <= 0 {
				count = batch.TargetTo - batch.TargetFrom + 1
			}
		}
		if count <= 0 {
			return nil, fmt.Errorf("batch %d chapter_count must be > 0", idx+1)
		}
		sourceSpan := batch.SourceTo - batch.SourceFrom + 1
		minCount, maxCount := plannerSourceMapChapterBudgetReviewRange(sourceSpan)
		hardMinCount := plannerSourceMapBudgetMinTargetChaptersForRange(entry, batch.SourceFrom, batch.SourceTo)
		minCount = max(minCount, hardMinCount)
		maxCount = max(maxCount, minCount)
		if !allowBudgetDeviation && count < hardMinCount {
			return nil, &plannerChapterBudgetQualityError{
				BatchIndex: idx + 1,
				Count:      count,
				MinCount:   hardMinCount,
				MaxCount:   maxCount,
				SourceFrom: batch.SourceFrom,
				SourceTo:   batch.SourceTo,
				SourceRunes: plannerSourceMapEstimatedRunesForRange(
					entry,
					batch.SourceFrom,
					batch.SourceTo,
				),
				Direction: "low",
			}
		}
		if !allowBudgetDeviation && count < minCount {
			return nil, &plannerChapterBudgetQualityError{
				BatchIndex: idx + 1,
				Count:      count,
				MinCount:   minCount,
				MaxCount:   maxCount,
				SourceFrom: batch.SourceFrom,
				SourceTo:   batch.SourceTo,
				Direction:  "low",
			}
		}
		if !allowBudgetDeviation && count > maxCount {
			return nil, &plannerChapterBudgetQualityError{
				BatchIndex: idx + 1,
				Count:      count,
				MinCount:   minCount,
				MaxCount:   maxCount,
				SourceFrom: batch.SourceFrom,
				SourceTo:   batch.SourceTo,
				Direction:  "high",
			}
		}
		batch.TargetChapterCount = count
		batch.TargetFrom = 0
		batch.TargetTo = 0
		out = append(out, batch)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("source-map range %d-%d returned no in-range batches", entry.SourceFrom, entry.SourceTo)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SourceFrom == out[j].SourceFrom {
			if out[i].SourceTo == out[j].SourceTo {
				return out[i].Index < out[j].Index
			}
			return out[i].SourceTo < out[j].SourceTo
		}
		return out[i].SourceFrom < out[j].SourceFrom
	})
	coveredTo := entry.SourceFrom - 1
	for _, batch := range out {
		if batch.SourceFrom < coveredTo {
			return nil, fmt.Errorf("source-map range %d-%d overlaps at source chapter %d", entry.SourceFrom, entry.SourceTo, batch.SourceFrom)
		}
		if batch.SourceFrom > coveredTo+1 {
			return nil, fmt.Errorf("source-map range %d-%d has gap before source chapter %d", entry.SourceFrom, entry.SourceTo, batch.SourceFrom)
		}
		if batch.SourceTo <= coveredTo {
			return nil, fmt.Errorf("source-map range %d-%d does not advance past source chapter %d", entry.SourceFrom, entry.SourceTo, coveredTo)
		}
		coveredTo = batch.SourceTo
	}
	if coveredTo < entry.SourceTo {
		return nil, fmt.Errorf("source-map range %d-%d ends coverage at %d", entry.SourceFrom, entry.SourceTo, coveredTo)
	}
	targetCount := plannerSkeletonBatchTargetCount(out)
	minTargetCount := plannerSourceMapBudgetMinTargetChapters(entry)
	if !allowBudgetDeviation && minTargetCount > 0 && targetCount < minTargetCount {
		return nil, &plannerChapterBudgetQualityError{
			Count:       targetCount,
			MinCount:    minTargetCount,
			SourceFrom:  entry.SourceFrom,
			SourceTo:    entry.SourceTo,
			SourceRunes: entry.SourceRunes,
			Direction:   "low",
		}
	}
	return out, nil
}

func plannerSourceMapChapterBudgetReviewRange(sourceSpan int) (int, int) {
	if sourceSpan < 1 {
		sourceSpan = 1
	}
	minCount := 1
	if sourceSpan >= adaptationPlannerSourceChunkedMin {
		minCount = max(1, sourceSpan/10)
	}
	maxCount := max(adaptationPlannerRecommendedBatchMax, sourceSpan*adaptationPlannerSourceMapExpansionMax)
	return minCount, maxCount
}

func plannerSkeletonBatchTargetCount(batches []plannerSkeletonBatch) int {
	total := 0
	for _, batch := range batches {
		if batch.TargetChapterCount > 0 {
			total += batch.TargetChapterCount
			continue
		}
		if batch.TargetTo >= batch.TargetFrom {
			total += batch.TargetTo - batch.TargetFrom + 1
		}
	}
	return total
}

func plannerSourceMapBudgetMinTargetChapters(entry plannerSourceMapEntry) int {
	if entry.SourceRunes <= adaptationPlannerModelChapterMaxRunes {
		return 0
	}
	return ceilPositiveDiv(entry.SourceRunes, adaptationPlannerModelChapterMaxRunes)
}

func plannerSourceMapBudgetMinTargetChaptersForRange(entry plannerSourceMapEntry, from, to int) int {
	estimatedRunes := plannerSourceMapEstimatedRunesForRange(entry, from, to)
	if estimatedRunes <= adaptationPlannerModelChapterMaxRunes {
		return 0
	}
	return ceilPositiveDiv(estimatedRunes, adaptationPlannerModelChapterMaxRunes)
}

func plannerSourceMapEstimatedRunesForRange(entry plannerSourceMapEntry, from, to int) int {
	if entry.SourceRunes <= 0 || from <= 0 || to < from {
		return 0
	}
	if from == entry.SourceFrom && to == entry.SourceTo {
		return entry.SourceRunes
	}
	sourceCount := entry.SourceTo - entry.SourceFrom + 1
	if sourceCount <= 0 {
		return 0
	}
	rangeCount := to - from + 1
	return ceilPositiveDiv(entry.SourceRunes*rangeCount, sourceCount)
}

func ceilPositiveDiv(value, divisor int) int {
	if value <= 0 || divisor <= 0 {
		return 0
	}
	return (value + divisor - 1) / divisor
}

func nextPlannerSkeletonTarget(batches []plannerSkeletonBatch) int {
	next := 1
	for _, batch := range batches {
		if batch.TargetTo >= next {
			next = batch.TargetTo + 1
		}
	}
	return next
}

func offsetPlannerSkeletonBatches(batches []plannerSkeletonBatch, targetFrom int, batchIndexFrom int) {
	nextTarget := targetFrom
	for idx := range batches {
		count := batches[idx].TargetChapterCount
		if count <= 0 && batches[idx].TargetTo >= batches[idx].TargetFrom {
			count = batches[idx].TargetTo - batches[idx].TargetFrom + 1
		}
		batches[idx].Index = batchIndexFrom + idx
		batches[idx].TargetFrom = nextTarget
		batches[idx].TargetTo = nextTarget + count - 1
		batches[idx].TargetChapterCount = count
		nextTarget = batches[idx].TargetTo + 1
	}
}

func buildPlanFromPlannerSkeletonDetails(
	ctx context.Context,
	deps Deps,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	skeleton plannerSkeleton,
	runtime *domain.AdaptationProposalRuntime,
) (domain.AdaptationPlan, error) {
	var zero domain.AdaptationPlan
	if runtime == nil {
		runtime = newPlannerProposalRuntime(opts, manifest, skeleton.TargetChapterCount)
		runtime.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
	}
	systemPrompt := strings.TrimSpace(deps.Prompts.Planner)
	if systemPrompt == "" {
		systemPrompt = "# Adaptation Planner\n\nReturn only JSON for the requested adaptation planning step."
	}
	detailBatches := plannerDetailBatches(skeleton.Batches, adaptationPlannerRecommendedBatchMax)
	emitAdaptProgress(opts.EmitProgress, StagePlan, 0, len(detailBatches), fmt.Sprintf("骨架规划完成：%d 章，%d 个模型规划段，拆为 %d 个详情子批次", skeleton.TargetChapterCount, len(skeleton.Batches), len(detailBatches)), nil)

	chapters := make([]domain.AdaptationChapterPlan, 0, skeleton.TargetChapterCount)
	for batchOrdinal, batch := range detailBatches {
		validateBatch := plannerBatchChapterValidator(opts, manifest, batch)
		if batchChapters, ok := plannerRuntimeBatchChapters(runtime, batch); ok {
			if err := validateBatch(batchChapters); err == nil {
				chapters = append(chapters, batchChapters...)
				emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal+1, len(detailBatches), fmt.Sprintf("Reused proposal detail batch %d/%d: target %d-%d", batchOrdinal+1, len(detailBatches), batch.TargetFrom, batch.TargetTo), nil)
				continue
			} else {
				removePlannerProposalRuntimeBatch(runtime, batch)
				if saveErr := savePlannerProposalRuntime(deps, runtime); saveErr != nil {
					return zero, fmt.Errorf("save proposal runtime after invalid reused batch %d: %w", batch.Index, saveErr)
				}
				emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal+1, len(detailBatches), fmt.Sprintf("Discarded invalid proposal detail batch %d/%d: target %d-%d", batchOrdinal+1, len(detailBatches), batch.TargetFrom, batch.TargetTo), err)
			}
		}
		batchPrompt, err := buildAdaptationPlannerBatchUserPrompt(opts, manifest, sourceFoundation, skeleton, batch, reportsForPlannerBatch(reports, batch), chapters)
		if err != nil {
			return zero, err
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal+1, len(detailBatches), fmt.Sprintf("请求章节详情第 %d/%d 批：第 %d-%d 章", batchOrdinal+1, len(detailBatches), batch.TargetFrom, batch.TargetTo), nil)
		batchText, err := generatePlannerText(
			ctx,
			deps.LLM,
			systemPrompt,
			batchPrompt,
			adaptationPlannerMaxTokens,
			opts.EmitProgress,
			batchOrdinal+1,
			len(detailBatches),
			fmt.Sprintf("章节详情第 %d/%d 批", batchOrdinal+1, len(detailBatches)),
			deps.modelCallMaxAttempts(),
		)
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
			validateBatch,
			opts.EmitProgress,
			batchOrdinal+1,
			len(detailBatches),
			fmt.Sprintf("章节详情第 %d/%d 批", batchOrdinal+1, len(detailBatches)),
			deps.structureRepairMaxAttempts(),
			deps.modelCallMaxAttempts(),
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
		upsertPlannerProposalRuntimeBatch(runtime, batch, batchChapters)
		if err := savePlannerProposalRuntime(deps, runtime); err != nil {
			return zero, fmt.Errorf("save proposal runtime batch %d: %w", batch.Index, err)
		}
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
		if updateErr := preparePlannerRuntimeAfterValidationError(deps, runtime, err, opts.EmitProgress); updateErr != nil {
			return zero, fmt.Errorf("%w (also failed to update proposal runtime: %v)", err, updateErr)
		}
		return zero, err
	}
	return proposal, nil
}

func volumeReviewFromSkeleton(opts ProposalOptions, manifest *domain.AdaptationSourceManifest, skeleton plannerSkeleton) domain.AdaptationVolumeReview {
	review := domain.AdaptationVolumeReview{
		Status:             domain.AdaptationPlanStatusVolumeReview,
		UpdatedAt:          time.Now().UTC().Format(time.RFC3339),
		Brief:              strings.TrimSpace(opts.Brief),
		SourcePath:         plannerProposalRuntimeSourcePath(opts, manifest),
		SourceChapterCount: plannerProposalRuntimeSourceChapterCount(manifest),
		Granularity:        strings.TrimSpace(opts.Granularity),
		RewritePolicy:      strings.TrimSpace(opts.RewritePolicy),
		WordTolerance:      opts.WordTolerance,
		TargetChapterCount: skeleton.TargetChapterCount,
		MainlineRules:      append([]string(nil), skeleton.MainlineRules...),
		RelationshipGoals:  append([]string(nil), skeleton.RelationshipGoals...),
		Volumes:            adaptationVolumesFromSkeleton(skeleton),
		Planner:            clonePlannerRuntimeMeta(skeleton.Planner),
	}
	if review.Planner == nil {
		review.Planner = &domain.AdaptationPlannerMeta{}
	}
	review.Planner.Prompt = adaptationPlannerPromptName
	review.Planner.PromptVersion = adaptationPlannerPromptVersion + "-volume-review"
	if strings.TrimSpace(review.Planner.GeneratedAt) == "" {
		review.Planner.GeneratedAt = review.UpdatedAt
	}
	return review
}

func loadPlannerProposalRuntime(
	deps Deps,
	opts ProposalOptions,
	manifest *domain.AdaptationSourceManifest,
	targetChapterHint int,
	emit ProgressEmitter,
) (*domain.AdaptationProposalRuntime, *plannerSkeleton, error) {
	runtime := newPlannerProposalRuntime(opts, manifest, targetChapterHint)
	existing, err := deps.Store.Adaptation.LoadProposalRuntime()
	if err != nil {
		return nil, nil, fmt.Errorf("load proposal runtime: %w", err)
	}
	if existing == nil {
		return runtime, nil, nil
	}
	if !plannerProposalRuntimeMatches(existing, opts, manifest, targetChapterHint) {
		emitAdaptProgress(emit, StagePlan, 0, 0, "Discarding stale proposal runtime checkpoint", nil)
		if err := deps.Store.Adaptation.ClearProposalRuntime(); err != nil {
			return nil, nil, fmt.Errorf("clear stale proposal runtime: %w", err)
		}
		return runtime, nil, nil
	}
	if existing.Skeleton == nil {
		return existing, nil, nil
	}
	skeleton := plannerSkeletonFromRuntime(existing)
	if err := normalizePlannerSkeleton(&skeleton, opts, manifest, targetChapterHint); err != nil {
		emitAdaptProgress(emit, StagePlan, 0, 0, fmt.Sprintf("Discarding invalid proposal runtime skeleton: %v", err), err)
		if clearErr := deps.Store.Adaptation.ClearProposalRuntime(); clearErr != nil {
			return nil, nil, fmt.Errorf("clear invalid proposal runtime: %w", clearErr)
		}
		return runtime, nil, nil
	}
	existing.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
	return existing, &skeleton, nil
}

func newPlannerProposalRuntime(opts ProposalOptions, manifest *domain.AdaptationSourceManifest, targetChapterHint int) *domain.AdaptationProposalRuntime {
	sourcePath := plannerProposalRuntimeSourcePath(opts, manifest)
	return &domain.AdaptationProposalRuntime{
		Version:            adaptationProposalRuntimeVersion,
		Brief:              strings.TrimSpace(opts.Brief),
		SourcePath:         sourcePath,
		SourceChapterCount: plannerProposalRuntimeSourceChapterCount(manifest),
		Granularity:        strings.TrimSpace(opts.Granularity),
		RewritePolicy:      strings.TrimSpace(opts.RewritePolicy),
		WordTolerance:      opts.WordTolerance,
		TargetChapterCount: targetChapterHint,
	}
}

func plannerProposalRuntimeMatches(runtime *domain.AdaptationProposalRuntime, opts ProposalOptions, manifest *domain.AdaptationSourceManifest, targetChapterHint int) bool {
	if runtime == nil || runtime.Version != adaptationProposalRuntimeVersion {
		return false
	}
	if strings.TrimSpace(runtime.Brief) != strings.TrimSpace(opts.Brief) {
		return false
	}
	if strings.TrimSpace(runtime.Granularity) != strings.TrimSpace(opts.Granularity) {
		return false
	}
	if strings.TrimSpace(runtime.RewritePolicy) != strings.TrimSpace(opts.RewritePolicy) {
		return false
	}
	if math.Abs(runtime.WordTolerance-opts.WordTolerance) > 0.000001 {
		return false
	}
	if !plannerProposalRuntimeTargetMatches(runtime, opts, targetChapterHint) {
		return false
	}
	if runtime.SourceChapterCount != plannerProposalRuntimeSourceChapterCount(manifest) {
		return false
	}
	return sameSourcePath(runtime.SourcePath, plannerProposalRuntimeSourcePath(opts, manifest))
}

func plannerProposalRuntimeTargetMatches(runtime *domain.AdaptationProposalRuntime, opts ProposalOptions, targetChapterHint int) bool {
	if runtime == nil {
		return false
	}
	if explicit := normalizeTargetChapterCount(opts.TargetChapterCount, inferTargetChapterCount(opts.Brief)); explicit > 0 {
		return runtime.TargetChapterCount == explicit
	}
	if runtime.Skeleton != nil || len(runtime.SkeletonBatches) > 0 || len(runtime.CompletedBatches) > 0 {
		return runtime.TargetChapterCount > 0
	}
	return runtime.TargetChapterCount == targetChapterHint
}

func plannerProposalRuntimeSourcePath(opts ProposalOptions, manifest *domain.AdaptationSourceManifest) string {
	if manifest != nil && strings.TrimSpace(manifest.SourcePath) != "" {
		return strings.TrimSpace(manifest.SourcePath)
	}
	return strings.TrimSpace(opts.SourcePath)
}

func plannerProposalRuntimeSourceChapterCount(manifest *domain.AdaptationSourceManifest) int {
	if manifest == nil {
		return 0
	}
	return manifest.ChapterCount
}

func savePlannerProposalRuntime(deps Deps, runtime *domain.AdaptationProposalRuntime) error {
	if deps.Store == nil {
		return fmt.Errorf("store is required")
	}
	if runtime == nil {
		return nil
	}
	runtime.Version = adaptationProposalRuntimeVersion
	runtime.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return deps.Store.Adaptation.SaveProposalRuntime(*runtime)
}

func plannerSkeletonFromRuntime(runtime *domain.AdaptationProposalRuntime) plannerSkeleton {
	if runtime == nil || runtime.Skeleton == nil {
		return plannerSkeleton{}
	}
	outline := runtime.Skeleton
	batches := make([]plannerSkeletonBatch, 0, len(outline.Batches))
	for _, batch := range outline.Batches {
		batches = append(batches, plannerSkeletonBatch{
			Index:              batch.Index,
			Title:              batch.Title,
			Theme:              batch.Theme,
			Goal:               batch.Goal,
			Summary:            batch.Summary,
			TargetFrom:         batch.TargetFrom,
			TargetTo:           batch.TargetTo,
			TargetChapterCount: batch.TargetChapterCount,
			SourceFrom:         batch.SourceFrom,
			SourceTo:           batch.SourceTo,
			SourceChapters:     append([]int(nil), batch.SourceChapters...),
			Notes:              append([]string(nil), batch.Notes...),
		})
	}
	return plannerSkeleton{
		Granularity:        runtime.Granularity,
		Status:             domain.AdaptationPlanStatusProposal,
		RewritePolicy:      runtime.RewritePolicy,
		Brief:              runtime.Brief,
		TargetChapterCount: outline.TargetChapterCount,
		MainlineRules:      append([]string(nil), outline.MainlineRules...),
		RelationshipGoals:  append([]string(nil), outline.RelationshipGoals...),
		Batches:            batches,
		Planner:            clonePlannerRuntimeMeta(outline.Planner),
	}
}

func plannerRuntimeOutlineFromSkeleton(skeleton plannerSkeleton) *domain.AdaptationProposalRuntimeOutline {
	batches := make([]domain.AdaptationProposalRuntimeSkeletonBatch, 0, len(skeleton.Batches))
	for _, batch := range skeleton.Batches {
		batches = append(batches, domain.AdaptationProposalRuntimeSkeletonBatch{
			Index:              batch.Index,
			Title:              batch.Title,
			Theme:              batch.Theme,
			Goal:               batch.Goal,
			Summary:            batch.Summary,
			TargetFrom:         batch.TargetFrom,
			TargetTo:           batch.TargetTo,
			TargetChapterCount: batch.TargetChapterCount,
			SourceFrom:         batch.SourceFrom,
			SourceTo:           batch.SourceTo,
			SourceChapters:     append([]int(nil), batch.SourceChapters...),
			Notes:              append([]string(nil), batch.Notes...),
		})
	}
	return &domain.AdaptationProposalRuntimeOutline{
		TargetChapterCount: skeleton.TargetChapterCount,
		MainlineRules:      append([]string(nil), skeleton.MainlineRules...),
		RelationshipGoals:  append([]string(nil), skeleton.RelationshipGoals...),
		Batches:            batches,
		Planner:            clonePlannerRuntimeMeta(skeleton.Planner),
	}
}

func plannerRuntimeOutlineMatchesSkeleton(outline *domain.AdaptationProposalRuntimeOutline, skeleton plannerSkeleton) bool {
	if outline == nil {
		return false
	}
	expected := plannerRuntimeOutlineFromSkeleton(skeleton)
	if expected == nil || outline.TargetChapterCount != expected.TargetChapterCount {
		return false
	}
	if len(outline.Batches) != len(expected.Batches) {
		return false
	}
	for idx := range outline.Batches {
		if !plannerRuntimeSkeletonBatchMatches(outline.Batches[idx], expected.Batches[idx]) {
			return false
		}
	}
	return true
}

func plannerRuntimeSkeletonBatchMatches(a, b domain.AdaptationProposalRuntimeSkeletonBatch) bool {
	return a.Index == b.Index &&
		a.TargetFrom == b.TargetFrom &&
		a.TargetTo == b.TargetTo &&
		a.TargetChapterCount == b.TargetChapterCount &&
		a.SourceFrom == b.SourceFrom &&
		a.SourceTo == b.SourceTo
}

func plannerRuntimeBatchChapters(runtime *domain.AdaptationProposalRuntime, batch plannerSkeletonBatch) ([]domain.AdaptationChapterPlan, bool) {
	if runtime == nil {
		return nil, false
	}
	for _, completed := range runtime.CompletedBatches {
		if !plannerRuntimeBatchMatches(completed, batch) {
			continue
		}
		chapters := make([]domain.AdaptationChapterPlan, 0, len(completed.Chapters))
		for _, chapter := range completed.Chapters {
			chapters = append(chapters, cloneAdaptationChapterPlan(chapter))
		}
		normalized, err := normalizePlannerBatchChapters(chapters, batch)
		if err == nil {
			return normalized, true
		}
	}
	return nil, false
}

func plannerRuntimeBatchMatches(completed domain.AdaptationProposalRuntimeBatch, batch plannerSkeletonBatch) bool {
	return completed.TargetFrom == batch.TargetFrom &&
		completed.TargetTo == batch.TargetTo &&
		completed.SourceFrom == batch.SourceFrom &&
		completed.SourceTo == batch.SourceTo
}

func removePlannerProposalRuntimeBatch(runtime *domain.AdaptationProposalRuntime, batch plannerSkeletonBatch) {
	if runtime == nil || len(runtime.CompletedBatches) == 0 {
		return
	}
	out := runtime.CompletedBatches[:0]
	for _, completed := range runtime.CompletedBatches {
		if plannerRuntimeBatchMatches(completed, batch) {
			continue
		}
		out = append(out, completed)
	}
	runtime.CompletedBatches = out
}

func preparePlannerRuntimeAfterValidationError(deps Deps, runtime *domain.AdaptationProposalRuntime, validationErr error, emit ProgressEmitter) error {
	if runtime == nil {
		return nil
	}
	var budgetErr *plannerProposalBudgetSplitError
	if errors.As(validationErr, &budgetErr) && budgetErr != nil {
		removed := removePlannerProposalRuntimeBatchesForSourceRange(runtime, budgetErr.SourceFrom, budgetErr.SourceTo)
		if removed > 0 {
			emitAdaptProgress(emit, StagePlan, 0, 0, fmt.Sprintf("Retained proposal runtime and discarded %d completed detail batch(es) covering source range %d-%d", removed, budgetErr.SourceFrom, budgetErr.SourceTo), validationErr)
			return savePlannerProposalRuntime(deps, runtime)
		}
	}
	emitAdaptProgress(emit, StagePlan, 0, 0, "Retained proposal runtime after final validation failure for retry", validationErr)
	return savePlannerProposalRuntime(deps, runtime)
}

func removePlannerProposalRuntimeBatchesForSourceRange(runtime *domain.AdaptationProposalRuntime, sourceFrom, sourceTo int) int {
	if runtime == nil || len(runtime.CompletedBatches) == 0 || sourceFrom <= 0 || sourceTo < sourceFrom {
		return 0
	}
	out := runtime.CompletedBatches[:0]
	removed := 0
	for _, completed := range runtime.CompletedBatches {
		if completed.SourceFrom <= sourceTo && completed.SourceTo >= sourceFrom {
			removed++
			continue
		}
		out = append(out, completed)
	}
	runtime.CompletedBatches = out
	return removed
}

func plannerRuntimeSkeletonBatchesForSource(runtime *domain.AdaptationProposalRuntime, entry plannerSourceMapEntry) ([]plannerSkeletonBatch, bool) {
	if runtime == nil || len(runtime.SkeletonBatches) == 0 {
		return nil, false
	}
	batches := make([]plannerSkeletonBatch, 0)
	for _, completed := range runtime.SkeletonBatches {
		if completed.SourceFrom < entry.SourceFrom || completed.SourceTo > entry.SourceTo {
			continue
		}
		batches = append(batches, plannerSkeletonBatch{
			Index:              completed.Index,
			Title:              completed.Title,
			Theme:              completed.Theme,
			Goal:               completed.Goal,
			Summary:            completed.Summary,
			TargetFrom:         completed.TargetFrom,
			TargetTo:           completed.TargetTo,
			TargetChapterCount: completed.TargetChapterCount,
			SourceFrom:         completed.SourceFrom,
			SourceTo:           completed.SourceTo,
			SourceChapters:     append([]int(nil), completed.SourceChapters...),
			Notes:              append([]string(nil), completed.Notes...),
		})
	}
	if len(batches) == 0 {
		return nil, false
	}
	sort.SliceStable(batches, func(i, j int) bool {
		if batches[i].SourceFrom == batches[j].SourceFrom {
			if batches[i].SourceTo == batches[j].SourceTo {
				return batches[i].TargetFrom < batches[j].TargetFrom
			}
			return batches[i].SourceTo < batches[j].SourceTo
		}
		return batches[i].SourceFrom < batches[j].SourceFrom
	})
	if _, err := normalizePlannerSourceMapSkeletonBatches(batches, entry); err != nil {
		return nil, false
	}
	return batches, true
}

func upsertPlannerProposalRuntimeSkeletonBatches(runtime *domain.AdaptationProposalRuntime, entry plannerSourceMapEntry, batches []plannerSkeletonBatch) {
	if runtime == nil {
		return
	}
	out := runtime.SkeletonBatches[:0]
	for _, completed := range runtime.SkeletonBatches {
		if completed.SourceFrom >= entry.SourceFrom && completed.SourceTo <= entry.SourceTo {
			continue
		}
		out = append(out, completed)
	}
	for _, batch := range batches {
		out = append(out, domain.AdaptationProposalRuntimeSkeletonBatch{
			Index:              batch.Index,
			Title:              batch.Title,
			Theme:              batch.Theme,
			Goal:               batch.Goal,
			Summary:            batch.Summary,
			TargetFrom:         batch.TargetFrom,
			TargetTo:           batch.TargetTo,
			TargetChapterCount: batch.TargetChapterCount,
			SourceFrom:         batch.SourceFrom,
			SourceTo:           batch.SourceTo,
			SourceChapters:     append([]int(nil), batch.SourceChapters...),
			Notes:              append([]string(nil), batch.Notes...),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TargetFrom == out[j].TargetFrom {
			return out[i].SourceFrom < out[j].SourceFrom
		}
		return out[i].TargetFrom < out[j].TargetFrom
	})
	runtime.SkeletonBatches = out
	runtime.TargetChapterCount = plannerRuntimeSkeletonTargetChapterCount(out)
}

func plannerRuntimeSkeletonTargetChapterCount(batches []domain.AdaptationProposalRuntimeSkeletonBatch) int {
	total := 0
	for _, batch := range batches {
		if batch.TargetTo > total {
			total = batch.TargetTo
		}
	}
	return total
}

func upsertPlannerProposalRuntimeBatch(runtime *domain.AdaptationProposalRuntime, batch plannerSkeletonBatch, chapters []domain.AdaptationChapterPlan) {
	if runtime == nil {
		return
	}
	out := runtime.CompletedBatches[:0]
	for _, completed := range runtime.CompletedBatches {
		if plannerRuntimeBatchMatches(completed, batch) {
			continue
		}
		out = append(out, completed)
	}
	storedChapters := make([]domain.AdaptationChapterPlan, 0, len(chapters))
	for _, chapter := range chapters {
		storedChapters = append(storedChapters, cloneAdaptationChapterPlan(chapter))
	}
	out = append(out, domain.AdaptationProposalRuntimeBatch{
		Index:       batch.Index,
		TargetFrom:  batch.TargetFrom,
		TargetTo:    batch.TargetTo,
		SourceFrom:  batch.SourceFrom,
		SourceTo:    batch.SourceTo,
		CompletedAt: time.Now().UTC().Format(time.RFC3339),
		Chapters:    storedChapters,
	})
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TargetFrom == out[j].TargetFrom {
			return out[i].TargetTo < out[j].TargetTo
		}
		return out[i].TargetFrom < out[j].TargetFrom
	})
	runtime.CompletedBatches = out
}

func clonePlannerRuntimeMeta(planner *domain.AdaptationPlannerMeta) *domain.AdaptationPlannerMeta {
	if planner == nil {
		return nil
	}
	out := *planner
	out.Notes = append(domain.TextList(nil), planner.Notes...)
	return &out
}

func plannerSkeletonErrorRepairable(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return !strings.Contains(message, "ignores long-form scale hint")
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

func generatePlannerText(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	userPrompt string,
	maxTokens int,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxAttemptsOverride ...int,
) (string, error) {
	return generatePlannerTextForStage(ctx, StagePlan, llm, systemPrompt, userPrompt, maxTokens, emit, current, total, label, maxAttemptsOverride...)
}

func generatePlannerTextForStage(
	ctx context.Context,
	stage Stage,
	llm imp.LLMChat,
	systemPrompt string,
	userPrompt string,
	maxTokens int,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxAttemptsOverride ...int,
) (string, error) {
	if llm == nil {
		return "", fmt.Errorf("planner llm is nil")
	}
	if stage == "" {
		stage = StagePlan
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = "改编规划模型调用"
	}
	messages := []agentcore.Message{
		agentcore.SystemMsg(systemPrompt),
		agentcore.UserMsg(userPrompt),
	}
	callOpts := []agentcore.CallOption{agentcore.WithMaxTokens(maxTokens), agentcore.WithJSONMode()}
	var lastErr error
	maxAttempts := adaptationPlannerGenerateMaxAttempts
	if len(maxAttemptsOverride) > 0 && maxAttemptsOverride[0] > 0 {
		maxAttempts = maxAttemptsOverride[0]
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		resp, err := llm.Generate(ctx, messages, nil, callOpts...)
		if err == nil && resp == nil {
			err = fmt.Errorf("planner llm returned nil response")
		}
		if err == nil {
			text := resp.Message.TextContent()
			if strings.TrimSpace(text) != "" {
				return text, nil
			}
			err = fmt.Errorf("planner llm returned empty response")
		}
		lastErr = err
		if !shouldRetryPlannerGenerate(ctx, err, attempt, maxAttempts) {
			return "", err
		}
		nextAttempt := attempt + 1
		displayErr := retrypolicy.SanitizeProviderError(err)
		emitAdaptProgress(
			emit,
			stage,
			current,
			total,
			fmt.Sprintf("%s模型调用失败，准备重试 %d/%d：%s", label, nextAttempt, maxAttempts, displayErr),
			fmt.Errorf("%s", displayErr),
		)
		if err := plannerRetrySleep(ctx, retrypolicy.Delay(attempt)); err != nil {
			return "", err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("planner llm generate exhausted")
	}
	return "", fmt.Errorf("planner llm generate exhausted %d attempts: %w", maxAttempts, lastErr)
}

func shouldRetryPlannerGenerate(ctx context.Context, err error, attempt, maxAttempts int) bool {
	if err == nil || ctx.Err() != nil || attempt >= maxAttempts {
		return false
	}
	if agentcore.IsFailoverEligible(err) {
		return true
	}
	if retrypolicy.IsProviderGatewayError(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "nil response") ||
		strings.Contains(msg, "empty response") ||
		strings.Contains(msg, "system is busy") ||
		strings.Contains(msg, "try again later") ||
		strings.Contains(msg, "overloaded") ||
		strings.Contains(msg, "temporarily unavailable") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "429") ||
		strings.Contains(msg, "503")
}

type plannerBatchChapterValidatorFunc func([]domain.AdaptationChapterPlan) error

func plannerBatchChapterValidator(opts ProposalOptions, manifest *domain.AdaptationSourceManifest, batch plannerSkeletonBatch) plannerBatchChapterValidatorFunc {
	opts.WordTolerance = normalizeProposalWordTolerance(opts.Granularity, opts.WordTolerance)
	sourceRunesByChapter := sourceRunesByChapter(manifest)
	chapterCount := 0
	if manifest != nil {
		chapterCount = manifest.ChapterCount
	}
	return func(chapters []domain.AdaptationChapterPlan) error {
		for idx := range chapters {
			chapter := &chapters[idx]
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
			fillPlannerChapterWordBudgetDefaults(chapter, sourceRunesByChapter, opts.WordTolerance)
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
				if sourceChapter <= 0 || sourceChapter > chapterCount {
					return fmt.Errorf("planner chapter %d references invalid source chapter %d", chapter.Chapter, sourceChapter)
				}
				if seenInChapter[sourceChapter] {
					return fmt.Errorf("planner chapter %d repeats source chapter %d", chapter.Chapter, sourceChapter)
				}
				seenInChapter[sourceChapter] = true
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
			if chapter.SourceRange.From <= 0 || chapter.SourceRange.To < chapter.SourceRange.From || chapter.SourceRange.To > chapterCount {
				return fmt.Errorf("planner chapter %d has invalid source_range %d-%d", chapter.Chapter, chapter.SourceRange.From, chapter.SourceRange.To)
			}
			for _, sourceChapter := range chapter.SourceChapters {
				if sourceChapter < chapter.SourceRange.From || sourceChapter > chapter.SourceRange.To {
					return fmt.Errorf("planner chapter %d source chapter %d falls outside source_range %d-%d", chapter.Chapter, sourceChapter, chapter.SourceRange.From, chapter.SourceRange.To)
				}
			}
		}
		return validatePlannerBatchChapterBudgetGroups(chapters, opts, sourceRunesByChapter, batch)
	}
}

func collectPlannerBatchChaptersWithRepair(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	initialText string,
	batch plannerSkeletonBatch,
	validate plannerBatchChapterValidatorFunc,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxRepairAttempts int,
	maxModelCallAttempts int,
) ([]domain.AdaptationChapterPlan, error) {
	if maxRepairAttempts <= 0 {
		maxRepairAttempts = adaptationPlannerRepairMaxAttempts
	}
	text := initialText
	var lastErr error
	for attempt := 0; ; attempt++ {
		chapters, missing, partial, parseErr := parsePlannerBatchPartial(text, batch)
		if partial && len(missing) == 0 {
			if validate == nil {
				return chapters, nil
			}
			if err := validate(chapters); err == nil {
				return chapters, nil
			} else {
				lastErr = err
			}
		}
		if partial && len(missing) > 0 {
			missingErr := parseErr
			if missingErr == nil {
				missingErr = fmt.Errorf("missing chapters %s for target range %d-%d", formatPlannerChapterList(missing), batch.TargetFrom, batch.TargetTo)
			}
			filled, fillErr := fillMissingPlannerBatchChapters(ctx, llm, systemPrompt, originalPrompt, text, batch, chapters, missing, missingErr, emit, current, total, label, maxRepairAttempts, maxModelCallAttempts)
			if fillErr == nil {
				if validate == nil {
					return filled, nil
				}
				if err := validate(filled); err == nil {
					return filled, nil
				} else {
					lastErr = err
				}
			}
			if fillErr != nil {
				lastErr = fillErr
			}
		} else {
			if lastErr == nil {
				lastErr = parseErr
			}
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("planner batch returned no usable chapters")
		}
		if attempt >= maxRepairAttempts {
			return nil, lastErr
		}
		emitAdaptProgress(emit, StagePlan, current, total, fmt.Sprintf("%s不能直接使用，正在整批修复第 %d/%d 次：%v", label, attempt+1, maxRepairAttempts, lastErr), lastErr)
		repairedText, err := repairPlannerBatchText(ctx, llm, systemPrompt, originalPrompt, text, batch, lastErr, emit, current, total, label, maxModelCallAttempts)
		if err != nil {
			return nil, err
		}
		text = repairedText
	}
}

func collectProposalVolumeRevisionSkeletonWithRepair(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	initialText string,
	originalBatch plannerSkeletonBatch,
	expansionMaxTo int,
	allowExpansion bool,
	manifest *domain.AdaptationSourceManifest,
	emit ProgressEmitter,
	maxRepairAttempts int,
	maxModelCallAttempts int,
) (plannerSkeletonBatch, error) {
	if maxRepairAttempts <= 0 {
		maxRepairAttempts = adaptationPlannerRepairMaxAttempts
	}
	text := initialText
	var lastErr error
	for attempt := 0; ; attempt++ {
		batch, err := parseProposalVolumeRevisionSkeleton(text, originalBatch, expansionMaxTo, allowExpansion, manifest)
		if err == nil {
			return batch, nil
		}
		lastErr = err
		if attempt >= maxRepairAttempts {
			return plannerSkeletonBatch{}, lastErr
		}
		emitAdaptProgress(emit, StagePlan, attempt+1, maxRepairAttempts, fmt.Sprintf("卷剧情重规划返回不符合结构，正在修复第 %d/%d 次：%v", attempt+1, maxRepairAttempts, lastErr), lastErr)
		repairedText, repairErr := repairProposalVolumeRevisionSkeletonText(ctx, llm, systemPrompt, originalPrompt, text, originalBatch, expansionMaxTo, allowExpansion, lastErr, emit, maxModelCallAttempts)
		if repairErr != nil {
			return plannerSkeletonBatch{}, repairErr
		}
		text = repairedText
	}
}

func parseProposalVolumeRevisionSkeleton(text string, originalBatch plannerSkeletonBatch, expansionMaxTo int, allowExpansion bool, manifest *domain.AdaptationSourceManifest) (plannerSkeletonBatch, error) {
	skeleton, err := parsePlannerSkeleton(text)
	if err != nil {
		return plannerSkeletonBatch{}, err
	}
	return normalizeProposalVolumeRevisionSkeletonBatch(skeleton, originalBatch, expansionMaxTo, allowExpansion, manifest)
}

func normalizeProposalVolumeRevisionSkeletonBatch(skeleton plannerSkeleton, originalBatch plannerSkeletonBatch, expansionMaxTo int, allowExpansion bool, manifest *domain.AdaptationSourceManifest) (plannerSkeletonBatch, error) {
	if manifest == nil || manifest.ChapterCount <= 0 {
		return plannerSkeletonBatch{}, fmt.Errorf("source manifest missing")
	}
	if len(skeleton.Batches) != 1 {
		return plannerSkeletonBatch{}, fmt.Errorf("volume revision skeleton must contain exactly one batch, got %d", len(skeleton.Batches))
	}
	if expansionMaxTo < originalBatch.TargetTo {
		expansionMaxTo = originalBatch.TargetTo
	}
	batch := skeleton.Batches[0]
	if batch.Index <= 0 {
		batch.Index = originalBatch.Index
	}
	if batch.TargetFrom <= 0 {
		batch.TargetFrom = originalBatch.TargetFrom
	}
	if batch.TargetTo <= 0 && batch.TargetChapterCount > 0 {
		batch.TargetTo = batch.TargetFrom + batch.TargetChapterCount - 1
	}
	if batch.TargetTo <= 0 {
		batch.TargetTo = originalBatch.TargetTo
	}
	if batch.TargetFrom != originalBatch.TargetFrom {
		return plannerSkeletonBatch{}, fmt.Errorf("volume revision target_from=%d, want %d", batch.TargetFrom, originalBatch.TargetFrom)
	}
	if batch.TargetTo < originalBatch.TargetTo {
		return plannerSkeletonBatch{}, fmt.Errorf("volume revision target_to=%d shrinks original target_to %d", batch.TargetTo, originalBatch.TargetTo)
	}
	if !allowExpansion && batch.TargetTo != originalBatch.TargetTo {
		return plannerSkeletonBatch{}, fmt.Errorf("volume revision changed chapter count without an expansion request")
	}
	if batch.TargetTo > expansionMaxTo {
		return plannerSkeletonBatch{}, fmt.Errorf("volume revision target_to=%d exceeds max %d", batch.TargetTo, expansionMaxTo)
	}
	if allowExpansion {
		decision := normalizeProposalVolumeExpansionDecision(batch.ExpansionDecision)
		if decision == "" {
			return plannerSkeletonBatch{}, fmt.Errorf("volume revision skeleton missing expansion_decision")
		}
		batch.ExpansionDecision = decision
		switch decision {
		case "expand":
			if batch.TargetTo <= originalBatch.TargetTo {
				return plannerSkeletonBatch{}, fmt.Errorf("volume revision model chose expansion but target_to=%d did not exceed original target_to %d", batch.TargetTo, originalBatch.TargetTo)
			}
		case "keep":
			if batch.TargetTo != originalBatch.TargetTo {
				return plannerSkeletonBatch{}, fmt.Errorf("volume revision model chose keep but changed target_to from %d to %d", originalBatch.TargetTo, batch.TargetTo)
			}
		default:
			return plannerSkeletonBatch{}, fmt.Errorf("volume revision expansion_decision=%q, want expand or keep", batch.ExpansionDecision)
		}
	}
	batch.TargetChapterCount = batch.TargetTo - batch.TargetFrom + 1
	if batch.SourceFrom <= 0 || batch.SourceTo <= 0 {
		minSource, maxSource := minMaxPositive(batch.SourceChapters)
		if batch.SourceFrom <= 0 {
			batch.SourceFrom = firstPositiveInt(minSource, originalBatch.SourceFrom)
		}
		if batch.SourceTo <= 0 {
			batch.SourceTo = firstPositiveInt(maxSource, originalBatch.SourceTo)
		}
	}
	if batch.SourceFrom <= 0 {
		batch.SourceFrom = originalBatch.SourceFrom
	}
	if batch.SourceTo <= 0 {
		batch.SourceTo = originalBatch.SourceTo
	}
	if batch.SourceFrom <= 0 || batch.SourceTo < batch.SourceFrom || batch.SourceTo > manifest.ChapterCount {
		return plannerSkeletonBatch{}, fmt.Errorf("volume revision has invalid source range %d-%d", batch.SourceFrom, batch.SourceTo)
	}
	if strings.TrimSpace(batch.Title) == "" {
		batch.Title = originalBatch.Title
	}
	if strings.TrimSpace(batch.Summary) == "" {
		batch.Summary = originalBatch.Summary
	}
	return batch, nil
}

func repairProposalVolumeRevisionSkeletonText(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	previousText string,
	originalBatch plannerSkeletonBatch,
	expansionMaxTo int,
	allowExpansion bool,
	previousErr error,
	emit ProgressEmitter,
	maxModelCallAttempts int,
) (string, error) {
	requirements := []string{
		"Return exactly one JSON skeleton object and no prose.",
		"The JSON must have a top-level batches array with exactly one batch.",
		fmt.Sprintf("The batch target_from must be %d.", originalBatch.TargetFrom),
		fmt.Sprintf("The batch target_to must be at least %d.", originalBatch.TargetTo),
		"Do not return chapter details or a chapters array.",
		"Do not return markdown or explanations.",
	}
	if allowExpansion {
		requirements = append(requirements,
			`The batch must include expansion_decision as either "expand" or "keep".`,
			fmt.Sprintf(`If expansion_decision is "expand", target_to must be greater than %d and must not exceed %d.`, originalBatch.TargetTo, expansionMaxTo),
			fmt.Sprintf(`If expansion_decision is "keep", target_to must remain %d.`, originalBatch.TargetTo),
		)
	} else {
		requirements = append(requirements, fmt.Sprintf("The batch target_to must remain %d.", originalBatch.TargetTo))
	}
	repairPrompt := buildPlannerRepairPrompt(
		fmt.Sprintf("volume revision skeleton %d", originalBatch.Index),
		originalPrompt,
		previousText,
		previousErr,
		requirements,
	)
	text, err := generatePlannerText(ctx, llm, systemPrompt, repairPrompt, adaptationPlannerSkeletonMaxTokens, emit, 0, 0, "卷剧情重规划修复", maxModelCallAttempts)
	if err != nil {
		return "", fmt.Errorf("planner volume revision skeleton repair llm generate: %w", err)
	}
	return text, nil
}

func collectProposalRevisionBatchChaptersWithRepair(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	initialText string,
	batch plannerSkeletonBatch,
	expansionMaxTo int,
	validate plannerBatchChapterValidatorFunc,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxRepairAttempts int,
	maxModelCallAttempts int,
) ([]domain.AdaptationChapterPlan, error) {
	if maxRepairAttempts <= 0 {
		maxRepairAttempts = adaptationPlannerRepairMaxAttempts
	}
	if expansionMaxTo <= batch.TargetTo {
		expansionMaxTo = batch.TargetTo
	}
	text := initialText
	var lastErr error
	for attempt := 0; ; attempt++ {
		chapters, missing, partial, parseErr := parseProposalRevisionBatchPartial(text, batch, expansionMaxTo)
		if partial && len(missing) == 0 {
			if validate == nil {
				return chapters, nil
			}
			if err := validate(chapters); err == nil {
				return chapters, nil
			} else {
				lastErr = err
			}
		}
		if partial && len(missing) > 0 {
			missingErr := parseErr
			if missingErr == nil {
				missingErr = fmt.Errorf("missing chapters %s for revision range %d-%d", formatPlannerChapterList(missing), batch.TargetFrom, max(batch.TargetTo, maxChapterInPlans(chapters)))
			}
			fillBatch := batch
			fillBatch.TargetTo = max(batch.TargetTo, maxChapterInPlans(chapters))
			filled, fillErr := fillMissingPlannerBatchChapters(ctx, llm, systemPrompt, originalPrompt, text, fillBatch, chapters, missing, missingErr, emit, current, total, label, maxRepairAttempts, maxModelCallAttempts)
			if fillErr == nil {
				if validate == nil {
					return filled, nil
				}
				if err := validate(filled); err == nil {
					return filled, nil
				} else {
					lastErr = err
				}
			}
			if fillErr != nil {
				lastErr = fillErr
			}
		} else {
			if lastErr == nil {
				lastErr = parseErr
			}
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("planner revision returned no usable chapters")
		}
		if attempt >= maxRepairAttempts {
			return nil, lastErr
		}
		emitAdaptProgress(emit, StagePlan, current, total, fmt.Sprintf("%s不能直接使用，正在整批修复第 %d/%d 次：%v", label, attempt+1, maxRepairAttempts, lastErr), lastErr)
		repairedText, err := repairProposalRevisionBatchText(ctx, llm, systemPrompt, originalPrompt, text, batch, expansionMaxTo, lastErr, emit, current, total, label, maxModelCallAttempts)
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

func parseProposalRevisionBatchPartial(text string, batch plannerSkeletonBatch, expansionMaxTo int) ([]domain.AdaptationChapterPlan, []int, bool, error) {
	plan, err := parsePlannerProposal(text)
	if err != nil {
		return nil, nil, false, err
	}
	chapters, missing, err := normalizeProposalRevisionBatchChapterSubset(plan.Chapters, batch, expansionMaxTo)
	if err == nil {
		return chapters, missing, true, nil
	}
	return nil, nil, false, err
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
	maxRepairAttempts int,
	maxModelCallAttempts int,
) ([]domain.AdaptationChapterPlan, error) {
	if maxRepairAttempts <= 0 {
		maxRepairAttempts = adaptationPlannerRepairMaxAttempts
	}
	currentChapters := append([]domain.AdaptationChapterPlan(nil), existing...)
	currentMissing := append([]int(nil), missing...)
	feedbackText := previousText
	lastErr := previousErr
	if lastErr == nil {
		lastErr = fmt.Errorf("missing chapters %s", formatPlannerChapterList(currentMissing))
	}
	for attempt := 0; attempt < maxRepairAttempts; attempt++ {
		emitAdaptProgress(emit, StagePlan, current, total, fmt.Sprintf("%s缺少章节 %s，正在补齐第 %d/%d 次", label, formatPlannerChapterList(currentMissing), attempt+1, maxRepairAttempts), lastErr)
		fillText, err := repairPlannerMissingChaptersText(ctx, llm, systemPrompt, originalPrompt, feedbackText, batch, currentChapters, currentMissing, lastErr, emit, current, total, label, maxModelCallAttempts)
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
	emit ProgressEmitter,
	current int,
	total int,
	maxModelCallAttempts int,
) (string, error) {
	repairPrompt := buildPlannerRepairPrompt("skeleton", originalPrompt, previousText, previousErr, []string{
		"Return exactly one JSON skeleton object and no prose.",
		"The JSON must have a top-level batches array.",
		"Each batch must have chapter_count, source_from, source_to, title, theme or goal, and summary.",
		"Do not calculate target_from or target_to in this source-map skeleton step; the host will assign continuous target chapter ranges from chapter_count.",
		"If the previous error says source_runes needs more target chapters or chapter_count is below the review floor, increase chapter_count so each target chapter stays within the model chapter budget.",
		"Do not return only overall_arc, key_turns, pair, notes, markdown, or explanation.",
	})
	text, err := generatePlannerText(ctx, llm, systemPrompt, repairPrompt, adaptationPlannerSkeletonMaxTokens, emit, current, total, "骨架规划修复", maxModelCallAttempts)
	if err != nil {
		return "", fmt.Errorf("planner skeleton repair llm generate: %w", err)
	}
	return text, nil
}

func retryPlannerSkeletonChapterBudget(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	previousText string,
	previousErr error,
	emit ProgressEmitter,
	current int,
	total int,
	maxModelCallAttempts int,
) (string, error) {
	instructions := []string{
		"Return exactly one JSON skeleton object and no prose.",
		"The JSON must have a top-level batches array.",
		"Keep the returned source ranges as a strict partition of the requested source-map range with no gaps and no overlaps.",
		"Each batch must have chapter_count, source_from, source_to, title, theme or goal, and summary.",
		"Do not calculate target_from or target_to; the host will assign continuous target chapter ranges from chapter_count.",
		"Reconsider chapter_count for every batch. A very high count is allowed when added plot, relationship arcs, transition scenes, or long source chapters need splitting. A very low count is allowed when the adaptation intentionally deletes, merges, or compresses source material.",
		"Keep a short reason for any unusually high or low chapter budget in the batch summary.",
	}
	instructions = append(instructions, plannerChapterBudgetRepairInstructions(previousErr)...)
	repairPrompt := buildPlannerRepairPrompt("skeleton chapter budget quality review", originalPrompt, previousText, previousErr, instructions)
	text, err := generatePlannerText(ctx, llm, systemPrompt, repairPrompt, adaptationPlannerSkeletonMaxTokens, emit, current, total, "骨架章节预算质量复核", maxModelCallAttempts)
	if err != nil {
		return "", fmt.Errorf("planner skeleton chapter budget review llm generate: %w", err)
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
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxModelCallAttempts int,
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
	text, err := generatePlannerText(ctx, llm, systemPrompt, repairPrompt, adaptationPlannerMaxTokens, emit, current, total, label+"整批修复", maxModelCallAttempts)
	if err != nil {
		return "", fmt.Errorf("planner batch repair llm generate: %w", err)
	}
	return text, nil
}

func repairProposalRevisionBatchText(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	previousText string,
	batch plannerSkeletonBatch,
	expansionMaxTo int,
	previousErr error,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxModelCallAttempts int,
) (string, error) {
	if expansionMaxTo < batch.TargetTo {
		expansionMaxTo = batch.TargetTo
	}
	repairPrompt := buildPlannerRepairPrompt(
		fmt.Sprintf("revision batch %d", batch.Index),
		originalPrompt,
		previousText,
		previousErr,
		[]string{
			"Return exactly one JSON object and no prose.",
			fmt.Sprintf("The top-level object must be shaped exactly like {\"chapters\":[...]} with chapters starting at %d.", batch.TargetFrom),
			fmt.Sprintf("Return the full original revision range %d through %d.", batch.TargetFrom, batch.TargetTo),
			fmt.Sprintf("If ending chapters are appended, they must continue sequentially after %d and must not exceed %d.", batch.TargetTo, expansionMaxTo),
			"Do not return a single chapter object. Return the full revised range plus any appended ending chapters.",
			"Every chapter must include chapter, title, core_event, hook, scenes, source_chapters, source_range, word_budget, preserve_events, required_changes, and forbidden_moves.",
			"Do not return only summaries, key_turns, overall_arc, markdown, or explanation.",
		},
	)
	text, err := generatePlannerText(ctx, llm, systemPrompt, repairPrompt, adaptationPlannerMaxTokens, emit, current, total, label+"整批修复", maxModelCallAttempts)
	if err != nil {
		return "", fmt.Errorf("planner revision repair llm generate: %w", err)
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
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxModelCallAttempts int,
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
	text, err := generatePlannerText(ctx, llm, systemPrompt, repairPrompt, adaptationPlannerMaxTokens, emit, current, total, label+"缺章补齐", maxModelCallAttempts)
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

type plannerSourceManifestSummary struct {
	SourcePath   string                      `json:"source_path,omitempty"`
	ChapterCount int                         `json:"chapter_count"`
	TotalRunes   int                         `json:"total_runes,omitempty"`
	AverageRunes int                         `json:"average_runes,omitempty"`
	FirstChapter plannerSourceChapterSummary `json:"first_chapter,omitempty"`
	LastChapter  plannerSourceChapterSummary `json:"last_chapter,omitempty"`
}

type plannerSourceChapterSummary struct {
	Chapter int    `json:"chapter,omitempty"`
	Title   string `json:"title,omitempty"`
	Runes   int    `json:"runes,omitempty"`
}

type plannerChapterBudgetPolicy struct {
	TargetRunes int      `json:"target_runes"`
	MaxRunes    int      `json:"max_runes"`
	Tolerance   float64  `json:"tolerance"`
	Notes       []string `json:"notes"`
}

func plannerChapterBudgetPolicyForGranularity(granularity string) *plannerChapterBudgetPolicy {
	if domain.AdaptationRewritePolicyForGranularity(granularity) != domain.AdaptationRewriteFullRewrite {
		return nil
	}
	return &plannerChapterBudgetPolicy{
		TargetRunes: adaptationPlannerModelChapterTargetRunes,
		MaxRunes:    adaptationPlannerModelChapterMaxRunes,
		Tolerance:   adaptationPlannerModelChapterTolerance,
		Notes: []string{
			"For arc/free full-rewrite plans, choose enough target chapters so each chapter can stay within max_runes.",
			"When one long source chapter or source range is split into multiple target chapters, divide its source runes and story beats across those targets instead of assigning the full source length to every target chapter.",
			"Set each word_budget.target_runes near target_runes when possible, and never set word_budget.max_runes above max_runes.",
		},
	}
}

type plannerSourceMapEntry struct {
	Index               int                   `json:"index"`
	SourceFrom          int                   `json:"source_from"`
	SourceTo            int                   `json:"source_to"`
	SourceRunes         int                   `json:"source_runes,omitempty"`
	PlotPhase           string                `json:"plot_phase,omitempty"`
	KeyCausality        []string              `json:"key_causality,omitempty"`
	PlotThreads         []string              `json:"plot_threads,omitempty"`
	CharacterArcs       []string              `json:"character_arcs,omitempty"`
	WorldConstraints    []string              `json:"world_constraints,omitempty"`
	MajorCharacters     []string              `json:"major_characters,omitempty"`
	RelationshipSignals []plannerSourceSignal `json:"relationship_signals,omitempty"`
	HeroineSignals      []plannerSourceSignal `json:"heroine_signals,omitempty"`
	AmbiguityRisks      []plannerSourceRisk   `json:"ambiguity_risks,omitempty"`
	CoupleMilestones    []plannerSourceSignal `json:"couple_milestones,omitempty"`
}

type plannerSourceSignal struct {
	Chapters   []int    `json:"chapters,omitempty"`
	Characters []string `json:"characters,omitempty"`
	Type       string   `json:"type,omitempty"`
	Summary    string   `json:"summary"`
	Evidence   string   `json:"evidence,omitempty"`
}

type plannerSourceRisk struct {
	Chapters   []int    `json:"chapters,omitempty"`
	Characters []string `json:"characters,omitempty"`
	Severity   string   `json:"severity,omitempty"`
	Risk       string   `json:"risk"`
	Evidence   string   `json:"evidence,omitempty"`
	Suggestion string   `json:"suggestion,omitempty"`
}

type plannerSourceFoundationSummary struct {
	Premise    string                           `json:"premise,omitempty"`
	Characters []domain.Character               `json:"characters,omitempty"`
	WorldRules []domain.WorldRule               `json:"world_rules,omitempty"`
	Volumes    []plannerFoundationVolumeSummary `json:"volumes,omitempty"`
	Compass    *domain.StoryCompass             `json:"compass,omitempty"`
}

type plannerFoundationVolumeSummary struct {
	Index int                           `json:"index"`
	Title string                        `json:"title,omitempty"`
	Theme string                        `json:"theme,omitempty"`
	Arcs  []plannerFoundationArcSummary `json:"arcs,omitempty"`
}

type plannerFoundationArcSummary struct {
	Index             int    `json:"index"`
	Title             string `json:"title,omitempty"`
	Goal              string `json:"goal,omitempty"`
	EstimatedChapters int    `json:"estimated_chapters,omitempty"`
}

type plannerSourceReportExcerpt struct {
	Chapter        int                   `json:"chapter"`
	Title          string                `json:"title,omitempty"`
	Summary        string                `json:"summary,omitempty"`
	Characters     []string              `json:"characters,omitempty"`
	CharacterFacts []string              `json:"character_facts,omitempty"`
	KeyEvents      []string              `json:"key_events,omitempty"`
	WorldRules     []string              `json:"world_rules,omitempty"`
	HookType       string                `json:"hook_type,omitempty"`
	DominantStrand string                `json:"dominant_strand,omitempty"`
	Relationships  []plannerRelationNote `json:"relationships,omitempty"`
	StateChanges   []plannerStateNote    `json:"state_changes,omitempty"`
}

type plannerRelationNote struct {
	CharacterA string `json:"character_a,omitempty"`
	CharacterB string `json:"character_b,omitempty"`
	Relation   string `json:"relation,omitempty"`
}

type plannerStateNote struct {
	Entity   string `json:"entity,omitempty"`
	Field    string `json:"field,omitempty"`
	OldValue string `json:"old_value,omitempty"`
	NewValue string `json:"new_value,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func plannerManifestSummary(manifest *domain.AdaptationSourceManifest) plannerSourceManifestSummary {
	if manifest == nil {
		return plannerSourceManifestSummary{}
	}
	summary := plannerSourceManifestSummary{
		SourcePath:   strings.TrimSpace(manifest.SourcePath),
		ChapterCount: manifest.ChapterCount,
	}
	for _, chapter := range manifest.Chapters {
		summary.TotalRunes += chapter.Runes
		if summary.FirstChapter.Chapter == 0 {
			summary.FirstChapter = plannerSourceChapterSummary{Chapter: chapter.Chapter, Title: clipText(chapter.Title, 80), Runes: chapter.Runes}
		}
		summary.LastChapter = plannerSourceChapterSummary{Chapter: chapter.Chapter, Title: clipText(chapter.Title, 80), Runes: chapter.Runes}
	}
	if summary.ChapterCount <= 0 {
		summary.ChapterCount = len(manifest.Chapters)
	}
	if summary.ChapterCount > 0 {
		summary.AverageRunes = summary.TotalRunes / summary.ChapterCount
	}
	return summary
}

func plannerSourceMapFromDossier(dossier *domain.AdaptationCoCreateDossier, manifest *domain.AdaptationSourceManifest) []plannerSourceMapEntry {
	if dossier == nil {
		return nil
	}
	sourceRunesByChapter := sourceRunesByChapter(manifest)
	entries := make([]plannerSourceMapEntry, 0, len(dossier.Batches))
	for _, batch := range dossier.Batches {
		entry := plannerSourceMapEntry{
			Index:               batch.Index,
			SourceFrom:          batch.SourceFrom,
			SourceTo:            batch.SourceTo,
			SourceRunes:         sourceRunesForRange(sourceRunesByChapter, batch.SourceFrom, batch.SourceTo),
			PlotPhase:           clipText(batch.PlotPhase, 220),
			KeyCausality:        clippedStringList(batch.KeyCausality, 6, 160),
			PlotThreads:         clippedStringList(batch.PlotThreads, 6, 150),
			CharacterArcs:       clippedStringList(batch.CharacterArcs, 6, 150),
			WorldConstraints:    clippedStringList(batch.WorldConstraints, 5, 150),
			MajorCharacters:     clippedStringList(batch.MajorCharacters, 16, 60),
			RelationshipSignals: plannerSignals(batch.RelationshipSignals, 5),
			HeroineSignals:      plannerSignals(batch.HeroineSignals, 5),
			AmbiguityRisks:      plannerRisks(batch.AmbiguityRisks, 4),
			CoupleMilestones:    plannerSignals(batch.CoupleMilestones, 5),
		}
		entries = append(entries, entry)
	}
	return entries
}

func plannerSourceReportExcerpts(reports []domain.AdaptationSourceReport) []plannerSourceReportExcerpt {
	excerpts := make([]plannerSourceReportExcerpt, 0, len(reports))
	for _, report := range reports {
		excerpts = append(excerpts, plannerSourceReportExcerpt{
			Chapter:        report.Chapter,
			Title:          clipText(report.Title, 80),
			Summary:        clipText(report.Summary, 220),
			Characters:     clippedStringList(report.Characters, 12, 60),
			CharacterFacts: clippedStringList(report.CharacterFacts, 4, 120),
			KeyEvents:      clippedStringList(report.KeyEvents, 5, 120),
			WorldRules:     clippedStringList(report.WorldRules, 4, 120),
			HookType:       clipText(report.HookType, 60),
			DominantStrand: clipText(report.DominantStrand, 80),
			Relationships:  plannerRelationNotes(report.Relationships, 6),
			StateChanges:   plannerStateNotes(report.StateChanges, 6),
		})
	}
	return excerpts
}

func clippedStringList(values []string, maxItems, maxRunes int) []string {
	values = limitStrings(trimmedNonEmpty(values), maxItems)
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, clipText(value, maxRunes))
	}
	return out
}

func plannerSignals(values []domain.AdaptationRelationshipSignal, maxItems int) []plannerSourceSignal {
	values = limitSignals(values, maxItems)
	out := make([]plannerSourceSignal, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value.Summary) == "" {
			continue
		}
		out = append(out, plannerSourceSignal{
			Chapters:   normalizeChapterRefs(value.Chapters),
			Characters: clippedStringList(value.Characters, 6, 60),
			Type:       clipText(value.Type, 60),
			Summary:    clipText(value.Summary, 140),
			Evidence:   clipText(value.Evidence, 120),
		})
	}
	return out
}

func plannerRisks(values []domain.AdaptationRelationshipRisk, maxItems int) []plannerSourceRisk {
	values = limitRisks(values, maxItems)
	out := make([]plannerSourceRisk, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value.Risk) == "" {
			continue
		}
		out = append(out, plannerSourceRisk{
			Chapters:   normalizeChapterRefs(value.Chapters),
			Characters: clippedStringList(value.Characters, 6, 60),
			Severity:   clipText(value.Severity, 40),
			Risk:       clipText(value.Risk, 140),
			Evidence:   clipText(value.Evidence, 120),
			Suggestion: clipText(value.Suggestion, 120),
		})
	}
	return out
}

func plannerRelationNotes(values []domain.RelationshipEntry, maxItems int) []plannerRelationNote {
	out := make([]plannerRelationNote, 0, min(len(values), maxItems))
	for _, value := range values {
		if maxItems > 0 && len(out) >= maxItems {
			break
		}
		if strings.TrimSpace(value.Relation) == "" {
			continue
		}
		out = append(out, plannerRelationNote{
			CharacterA: clipText(value.CharacterA, 60),
			CharacterB: clipText(value.CharacterB, 60),
			Relation:   clipText(value.Relation, 120),
		})
	}
	return out
}

func plannerStateNotes(values []domain.StateChange, maxItems int) []plannerStateNote {
	out := make([]plannerStateNote, 0, min(len(values), maxItems))
	for _, value := range values {
		if maxItems > 0 && len(out) >= maxItems {
			break
		}
		if strings.TrimSpace(value.Entity) == "" && strings.TrimSpace(value.Field) == "" {
			continue
		}
		out = append(out, plannerStateNote{
			Entity:   clipText(value.Entity, 60),
			Field:    clipText(value.Field, 60),
			OldValue: clipText(value.OldValue, 80),
			NewValue: clipText(value.NewValue, 80),
			Reason:   clipText(value.Reason, 120),
		})
	}
	return out
}

func plannerSourceFoundationDigest(sourceFoundation *domain.AdaptationSourceFoundation) *plannerSourceFoundationSummary {
	if sourceFoundation == nil {
		return nil
	}
	digest := &plannerSourceFoundationSummary{
		Premise:    strings.TrimSpace(sourceFoundation.Premise),
		Characters: append([]domain.Character(nil), sourceFoundation.Characters...),
		WorldRules: append([]domain.WorldRule(nil), sourceFoundation.WorldRules...),
		Volumes:    make([]plannerFoundationVolumeSummary, 0, len(sourceFoundation.Volumes)),
	}
	for _, volume := range sourceFoundation.Volumes {
		nextVolume := plannerFoundationVolumeSummary{
			Index: volume.Index,
			Title: strings.TrimSpace(volume.Title),
			Theme: strings.TrimSpace(volume.Theme),
			Arcs:  make([]plannerFoundationArcSummary, 0, len(volume.Arcs)),
		}
		for _, arc := range volume.Arcs {
			nextVolume.Arcs = append(nextVolume.Arcs, plannerFoundationArcSummary{
				Index:             arc.Index,
				Title:             strings.TrimSpace(arc.Title),
				Goal:              strings.TrimSpace(arc.Goal),
				EstimatedChapters: arc.EstimatedChapters,
			})
		}
		digest.Volumes = append(digest.Volumes, nextVolume)
	}
	if sourceFoundation.Compass != nil {
		compass := *sourceFoundation.Compass
		compass.OpenThreads = append([]string(nil), sourceFoundation.Compass.OpenThreads...)
		digest.Compass = &compass
	}
	return digest
}

func buildAdaptationPlannerSkeletonUserPrompt(
	opts ProposalOptions,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	sourceMap []plannerSourceMapEntry,
	targetChapterHint int,
) (string, error) {
	input := struct {
		Brief                string                          `json:"brief"`
		Granularity          string                          `json:"granularity"`
		RewritePolicy        string                          `json:"rewrite_policy"`
		WordTolerance        float64                         `json:"word_tolerance"`
		TargetChapterHint    int                             `json:"target_chapter_hint,omitempty"`
		TargetChapterRole    string                          `json:"target_chapter_hint_role,omitempty"`
		RecommendedBatchMax  int                             `json:"recommended_batch_max"`
		ChapterBudgetPolicy  *plannerChapterBudgetPolicy     `json:"chapter_budget_policy,omitempty"`
		SourceManifest       plannerSourceManifestSummary    `json:"source_manifest"`
		SourceFoundation     *plannerSourceFoundationSummary `json:"source_foundation"`
		SourceMap            []plannerSourceMapEntry         `json:"source_map"`
		SourceMapNotes       []string                        `json:"source_map_notes"`
		SourceMapBudgetNotes []string                        `json:"source_map_budget_notes,omitempty"`
		Requirements         []string                        `json:"requirements"`
	}{
		Brief:               opts.Brief,
		Granularity:         opts.Granularity,
		RewritePolicy:       opts.RewritePolicy,
		WordTolerance:       opts.WordTolerance,
		TargetChapterHint:   targetChapterHint,
		TargetChapterRole:   plannerTargetChapterHintRole(opts, manifest, targetChapterHint),
		RecommendedBatchMax: adaptationPlannerRecommendedBatchMax,
		ChapterBudgetPolicy: plannerChapterBudgetPolicyForGranularity(opts.Granularity),
		SourceManifest:      plannerManifestSummary(manifest),
		SourceFoundation:    plannerSourceFoundationDigest(sourceFoundation),
		SourceMap:           sourceMap,
		SourceMapNotes: []string{
			"source_map is a compact, resumable dossier built from source-report batches; use it instead of raw per-chapter reports for this skeleton step.",
			"Each source_map entry covers an inclusive source range and preserves high-level causality, plot threads, character arcs, world constraints, and relationship signals.",
		},
		SourceMapBudgetNotes: plannerSourceMapBudgetNotes(sourceMap),
		Requirements: []string{
			"Return exactly one JSON skeleton object and no prose.",
			"Do not wrap the JSON in markdown fences.",
			"Do not return the final AdaptationPlan here.",
			"Do not include a chapters array in the skeleton step; chapter details are generated in later batch calls.",
			"Choose how many target chapters this source-map range needs, then divide it into one or more model-planned batches/volumes.",
			"If chapter_budget_policy is present, source_map.source_runes must drive splitting: a long single source chapter still needs multiple target chapters when one target would exceed chapter_budget_policy.max_runes.",
			"Read source_map_budget_notes before choosing chapter_count. Treat those notes as first-pass budget guidance, not repair-only feedback.",
			"If target_chapter_hint_role is source_scale_minimum, treat target_chapter_hint as anti-shrink long-form scale guidance, not an exact final chapter count.",
			"Choose final target_chapter_count after analyzing source_map and the user's additions; increase above target_chapter_hint when added plot, relationship, or transition arcs require more chapters.",
			"If target_chapter_hint_role is explicit_target_scale, honor that requested scale unless the source map and user changes make a different count necessary; explain the choice in batch summaries.",
			"Use source_map ranges to preserve the full-book structure without requesting raw source_reports.",
			"Each batch must include chapter_count, source_from, source_to, title, theme/goal, and summary.",
			"Do not calculate target_from or target_to in this source-map skeleton step; the host will assign continuous target chapter ranges from chapter_count.",
			"Choose skeleton batches by coherent story arc, volume beat, or major plot movement; they do not need to match recommended_batch_max exactly.",
			"The host will split oversized skeleton batches into detail calls of about recommended_batch_max target chapters.",
			"The source ranges across returned batches must strictly partition the provided source_map range: cover every source chapter once, with no gaps and no overlaps.",
		},
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal planner skeleton input: %w", err)
	}
	return "Plan one source-map portion of the high-level long-form adaptation skeleton. Use the current model to decide how many target chapters this source range needs; do not mechanically mirror or compress source chapters.\n\n" +
		"Output contract: return exactly one JSON object, no prose and no markdown. The top-level object must contain a batches array. Do not return chapter details in this step. The host will concatenate all source-map portions and renumber target chapters globally.\n\n" +
		"Required JSON shape:\n" +
		"{\"granularity\":\"...\",\"status\":\"proposal\",\"rewrite_policy\":\"...\",\"brief\":\"...\",\"target_chapter_count\":60,\"mainline_rules\":[],\"relationship_goals\":[],\"batches\":[{\"index\":1,\"title\":\"...\",\"theme\":\"...\",\"chapter_count\":8,\"source_from\":1,\"source_to\":3,\"summary\":\"...\"}]}.\n\n" +
		"Planning input:\n```json\n" + string(raw) + "\n```", nil
}

type plannerPreviousChapterContext struct {
	Chapter        int      `json:"chapter"`
	Title          string   `json:"title,omitempty"`
	CoreEvent      string   `json:"core_event,omitempty"`
	Hook           string   `json:"hook,omitempty"`
	Scenes         []string `json:"scenes,omitempty"`
	SourceChapters []int    `json:"source_chapters,omitempty"`
}

func plannerPreviousChapterContexts(chapters []domain.AdaptationChapterPlan, maxItems int) []plannerPreviousChapterContext {
	if maxItems <= 0 || len(chapters) == 0 {
		return nil
	}
	start := len(chapters) - maxItems
	if start < 0 {
		start = 0
	}
	out := make([]plannerPreviousChapterContext, 0, len(chapters)-start)
	for _, chapter := range chapters[start:] {
		out = append(out, plannerPreviousChapterContext{
			Chapter:        chapter.Chapter,
			Title:          clipText(chapter.Title, 80),
			CoreEvent:      clipText(chapter.CoreEvent, 160),
			Hook:           clipText(chapter.Hook, 120),
			Scenes:         clippedStringList(chapter.Scenes, 4, 80),
			SourceChapters: append([]int(nil), chapter.SourceChapters...),
		})
	}
	return out
}

func buildAdaptationPlannerBatchUserPrompt(
	opts ProposalOptions,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	skeleton plannerSkeleton,
	batch plannerSkeletonBatch,
	reports []domain.AdaptationSourceReport,
	previousChapters []domain.AdaptationChapterPlan,
) (string, error) {
	input := struct {
		Brief                  string                          `json:"brief"`
		Granularity            string                          `json:"granularity"`
		RewritePolicy          string                          `json:"rewrite_policy"`
		WordTolerance          float64                         `json:"word_tolerance"`
		ExpectedChapters       int                             `json:"expected_chapters"`
		ChapterBudgetPolicy    *plannerChapterBudgetPolicy     `json:"chapter_budget_policy,omitempty"`
		SourceManifest         plannerSourceManifestSummary    `json:"source_manifest"`
		SourceFoundation       *plannerSourceFoundationSummary `json:"source_foundation"`
		Skeleton               plannerSkeleton                 `json:"skeleton"`
		Batch                  plannerSkeletonBatch            `json:"batch"`
		PreviousDetailChapters []plannerPreviousChapterContext `json:"previous_detail_chapters,omitempty"`
		SourceReports          []plannerSourceReportExcerpt    `json:"source_reports"`
		SourceReportNotes      []string                        `json:"source_report_notes"`
		Requirements           []string                        `json:"requirements"`
	}{
		Brief:                  opts.Brief,
		Granularity:            opts.Granularity,
		RewritePolicy:          opts.RewritePolicy,
		WordTolerance:          opts.WordTolerance,
		ExpectedChapters:       batch.TargetTo - batch.TargetFrom + 1,
		ChapterBudgetPolicy:    plannerChapterBudgetPolicyForGranularity(opts.Granularity),
		SourceManifest:         plannerManifestSummary(manifest),
		SourceFoundation:       plannerSourceFoundationDigest(sourceFoundation),
		Skeleton:               skeleton,
		Batch:                  batch,
		PreviousDetailChapters: plannerPreviousChapterContexts(previousChapters, adaptationPlannerRecommendedBatchMax),
		SourceReports:          plannerSourceReportExcerpts(reports),
		SourceReportNotes: []string{
			"source_reports are clipped excerpts for the requested source range, not full raw reports.",
			"Use source_range and source_chapters as factual anchors; do not copy source prose.",
		},
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
			"If chapter_budget_policy is present, keep every word_budget.max_runes within chapter_budget_policy.max_runes; split the batch's source beats across the requested target chapters instead of giving each chapter the full source-range budget.",
			"Use the user's adaptation brief and the skeleton batch goal; do not ignore earlier user planning.",
			"Use previous_detail_chapters only for continuity, callbacks, and handoff hooks; do not duplicate already generated chapters.",
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
	if batch, ok := decodePlannerSkeletonBatchJSON(data); ok {
		skeleton.Batches = []plannerSkeletonBatch{batch}
		if skeleton.TargetChapterCount <= 0 {
			skeleton.TargetChapterCount = batch.TargetChapterCount
		}
		if skeleton.TargetChapterCount <= 0 && batch.TargetTo >= batch.TargetFrom {
			skeleton.TargetChapterCount = batch.TargetTo - batch.TargetFrom + 1
		}
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

func decodePlannerSkeletonBatchJSON(data []byte) (plannerSkeletonBatch, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || !plannerSkeletonBatchObjectShape(object) {
		return plannerSkeletonBatch{}, false
	}
	var batch plannerSkeletonBatch
	if err := json.Unmarshal(data, &batch); err != nil {
		return plannerSkeletonBatch{}, false
	}
	return batch, true
}

func plannerSkeletonBatchObjectShape(object map[string]json.RawMessage) bool {
	if len(object) == 0 || len(object["batches"]) > 0 {
		return false
	}
	hasTarget := len(object["target_from"]) > 0 || len(object["target_to"]) > 0 || len(object["target_range"]) > 0
	hasSource := len(object["source_from"]) > 0 || len(object["source_to"]) > 0 || len(object["source_range"]) > 0 || len(object["source_chapters"]) > 0
	hasStory := len(object["title"]) > 0 || len(object["theme"]) > 0 || len(object["goal"]) > 0 || len(object["summary"]) > 0
	return hasTarget && hasSource && hasStory
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
	if plannerTargetChapterHintRole(opts, manifest, targetChapterHint) == "explicit_target_scale" && targetChapterHint >= adaptationPlannerChunkedMinChapters && skeleton.TargetChapterCount > 0 {
		minAccepted := targetChapterHint * 4 / 5
		if minAccepted < adaptationPlannerChunkedMinChapters {
			minAccepted = adaptationPlannerChunkedMinChapters
		}
		if skeleton.TargetChapterCount < minAccepted {
			return fmt.Errorf("target_chapter_count=%d ignores long-form scale hint %d", skeleton.TargetChapterCount, targetChapterHint)
		}
	}
	nextTarget := 1
	for idx := range skeleton.Batches {
		batch := &skeleton.Batches[idx]
		if batch.Index <= 0 {
			batch.Index = idx + 1
		}
		count := batch.TargetChapterCount
		if count <= 0 {
			if batch.TargetFrom <= 0 || batch.TargetTo < batch.TargetFrom {
				return fmt.Errorf("batch %d must include chapter_count or a valid target range", batch.Index)
			}
			count = batch.TargetTo - batch.TargetFrom + 1
		}
		batch.TargetChapterCount = count
		batch.TargetFrom = nextTarget
		batch.TargetTo = nextTarget + count - 1
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
		skeleton.TargetChapterCount = lastTarget
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

func normalizeProposalRevisionBatchChapterSubset(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch, expansionMaxTo int) ([]domain.AdaptationChapterPlan, []int, error) {
	if len(chapters) == 0 {
		return nil, nil, fmt.Errorf("no chapters")
	}
	if expansionMaxTo < batch.TargetTo {
		expansionMaxTo = batch.TargetTo
	}
	out := normalizeProposalRevisionBatchChapterNumbers(chapters, batch, expansionMaxTo)
	byChapter := make(map[int]domain.AdaptationChapterPlan, len(out))
	for _, chapter := range out {
		if chapter.Chapter < batch.TargetFrom || chapter.Chapter > expansionMaxTo {
			return nil, nil, fmt.Errorf("chapter %d outside revision range %d-%d", chapter.Chapter, batch.TargetFrom, expansionMaxTo)
		}
		if _, exists := byChapter[chapter.Chapter]; exists {
			return nil, nil, fmt.Errorf("duplicate chapter %d in revision range %d-%d", chapter.Chapter, batch.TargetFrom, expansionMaxTo)
		}
		byChapter[chapter.Chapter] = chapter
	}
	expectedTo := max(batch.TargetTo, maxChapterInMap(byChapter))
	return sortedPlannerBatchChapters(byChapter), missingProposalRevisionChapters(byChapter, batch.TargetFrom, expectedTo), nil
}

func normalizeProposalRevisionBatchChapterNumbers(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch, expansionMaxTo int) []domain.AdaptationChapterPlan {
	out := append([]domain.AdaptationChapterPlan(nil), chapters...)
	if batch.TargetFrom <= 1 || len(out) == 0 {
		return out
	}
	if expansionMaxTo < batch.TargetTo {
		expansionMaxTo = batch.TargetTo
	}
	wantCount := expansionMaxTo - batch.TargetFrom + 1
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

func missingProposalRevisionChapters(byChapter map[int]domain.AdaptationChapterPlan, from, to int) []int {
	var missing []int
	for chapter := from; chapter <= to; chapter++ {
		if _, exists := byChapter[chapter]; !exists {
			missing = append(missing, chapter)
		}
	}
	return missing
}

func salvageProposalRevisionBatchChapterSubset(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch, expansionMaxTo int) ([]domain.AdaptationChapterPlan, []int) {
	if expansionMaxTo < batch.TargetTo {
		expansionMaxTo = batch.TargetTo
	}
	out := normalizeProposalRevisionBatchChapterNumbers(chapters, batch, expansionMaxTo)
	byChapter := make(map[int]domain.AdaptationChapterPlan, len(out))
	for _, chapter := range out {
		if chapter.Chapter < batch.TargetFrom || chapter.Chapter > expansionMaxTo {
			continue
		}
		if _, exists := byChapter[chapter.Chapter]; exists {
			continue
		}
		byChapter[chapter.Chapter] = chapter
	}
	expectedTo := max(batch.TargetTo, maxChapterInMap(byChapter))
	return sortedPlannerBatchChapters(byChapter), missingProposalRevisionChapters(byChapter, batch.TargetFrom, expectedTo)
}

func maxChapterInPlans(chapters []domain.AdaptationChapterPlan) int {
	maxChapter := 0
	for _, chapter := range chapters {
		if chapter.Chapter > maxChapter {
			maxChapter = chapter.Chapter
		}
	}
	return maxChapter
}

func maxChapterInMap(chapters map[int]domain.AdaptationChapterPlan) int {
	maxChapter := 0
	for chapter := range chapters {
		if chapter > maxChapter {
			maxChapter = chapter
		}
	}
	return maxChapter
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
		Brief               string                             `json:"brief"`
		Granularity         string                             `json:"granularity"`
		RewritePolicy       string                             `json:"rewrite_policy"`
		WordTolerance       float64                            `json:"word_tolerance"`
		ChapterBudgetPolicy *plannerChapterBudgetPolicy        `json:"chapter_budget_policy,omitempty"`
		SourceManifest      *domain.AdaptationSourceManifest   `json:"source_manifest"`
		SourceFoundation    *domain.AdaptationSourceFoundation `json:"source_foundation"`
		SourceReports       []domain.AdaptationSourceReport    `json:"source_reports"`
		Requirements        []string                           `json:"requirements"`
	}{
		Brief:               opts.Brief,
		Granularity:         opts.Granularity,
		RewritePolicy:       opts.RewritePolicy,
		WordTolerance:       opts.WordTolerance,
		ChapterBudgetPolicy: plannerChapterBudgetPolicyForGranularity(opts.Granularity),
		SourceManifest:      manifest,
		SourceFoundation:    sourceFoundation,
		SourceReports:       reports,
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
			"If chapter_budget_policy is present, long source chapters must be split into enough target chapters so no target chapter budget exceeds chapter_budget_policy.max_runes.",
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

	for i := range proposal.Chapters {
		chapter := &proposal.Chapters[i]
		sourceRangeExplicit := chapter.SourceRange.From > 0 || chapter.SourceRange.To > 0
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
		fillPlannerChapterWordBudgetDefaults(chapter, sourceRunesByChapter, opts.WordTolerance)
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
		if sourceRangeExplicit {
			chapter.SourceChapters = expandSourceChaptersForRange(chapter.SourceChapters, chapter.SourceRange.From, chapter.SourceRange.To)
			for sourceChapter := chapter.SourceRange.From; sourceChapter <= chapter.SourceRange.To; sourceChapter++ {
				covered[sourceChapter] = true
			}
		}
	}
	for sourceChapter := 1; sourceChapter <= manifest.ChapterCount; sourceChapter++ {
		if !covered[sourceChapter] {
			return fmt.Errorf("planner proposal does not cover source chapter %d", sourceChapter)
		}
	}
	budgetNormalized, err := normalizePlannerProposalChapterBudgets(proposal.Chapters, opts, sourceRunesByChapter)
	if err != nil {
		return err
	}
	if budgetNormalized {
		proposal.TargetTotalRunes = 0
		proposal.TargetMinRunes = 0
		proposal.TargetMaxRunes = 0
	}
	targetTotalRunes := 0
	targetMinRunes := 0
	targetMaxRunes := 0
	for i := range proposal.Chapters {
		chapter := &proposal.Chapters[i]
		if err := validatePlannerWordBudget(chapter, opts.WordTolerance); err != nil {
			return err
		}
		targetTotalRunes += chapter.WordBudget.TargetRunes
		targetMinRunes += chapter.WordBudget.MinRunes
		targetMaxRunes += chapter.WordBudget.MaxRunes
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

type plannerChapterBudgetGroup struct {
	Indexes     []int
	SourceFrom  int
	SourceTo    int
	SourceRunes int
}

type plannerProposalBudgetSplitError struct {
	FirstChapter int
	SourceFrom   int
	SourceTo     int
	SourceRunes  int
	MinChapters  int
}

func (e *plannerProposalBudgetSplitError) Error() string {
	if e == nil {
		return "planner source range needs more target chapters before assigning word_budget"
	}
	return fmt.Sprintf("planner chapter %d source_range %d-%d has %d source_runes; split this source range into at least %d target chapters before assigning word_budget",
		e.FirstChapter, e.SourceFrom, e.SourceTo, e.SourceRunes, e.MinChapters)
}

func normalizePlannerProposalChapterBudgets(chapters []domain.AdaptationChapterPlan, opts ProposalOptions, sourceRunesByChapter map[int]int) (bool, error) {
	policy := plannerChapterBudgetPolicyForGranularity(opts.Granularity)
	if policy == nil {
		return false, nil
	}
	normalized := false
	groups := plannerChapterBudgetGroups(chapters, sourceRunesByChapter)
	for _, group := range groups {
		if len(group.Indexes) == 0 || !plannerBudgetGroupNeedsNormalization(chapters, group, *policy) {
			continue
		}
		minChapters := 0
		if group.SourceRunes > policy.MaxRunes {
			minChapters = ceilPositiveDiv(group.SourceRunes, policy.MaxRunes)
		}
		if minChapters > len(group.Indexes) {
			first := chapters[group.Indexes[0]].Chapter
			return false, &plannerProposalBudgetSplitError{
				FirstChapter: first,
				SourceFrom:   group.SourceFrom,
				SourceTo:     group.SourceTo,
				SourceRunes:  group.SourceRunes,
				MinChapters:  minChapters,
			}
		}
		applyPlannerBudgetGroup(chapters, group, *policy)
		normalized = true
	}
	return normalized, nil
}

func validatePlannerBatchChapterBudgetGroups(chapters []domain.AdaptationChapterPlan, opts ProposalOptions, sourceRunesByChapter map[int]int, batch plannerSkeletonBatch) error {
	policy := plannerChapterBudgetPolicyForGranularity(opts.Granularity)
	if policy == nil {
		return nil
	}
	groups := plannerChapterBudgetGroups(chapters, sourceRunesByChapter)
	for _, group := range groups {
		if len(group.Indexes) == 0 || !plannerBudgetGroupNeedsNormalization(chapters, group, *policy) {
			continue
		}
		if group.SourceRunes <= policy.MaxRunes {
			continue
		}
		minChapters := ceilPositiveDiv(group.SourceRunes, policy.MaxRunes)
		if minChapters <= len(group.Indexes) {
			continue
		}
		if plannerBatchBudgetGroupMayContinue(chapters, group, batch) {
			continue
		}
		first := chapters[group.Indexes[0]].Chapter
		return &plannerProposalBudgetSplitError{
			FirstChapter: first,
			SourceFrom:   group.SourceFrom,
			SourceTo:     group.SourceTo,
			SourceRunes:  group.SourceRunes,
			MinChapters:  minChapters,
		}
	}
	return nil
}

func plannerBatchBudgetGroupMayContinue(chapters []domain.AdaptationChapterPlan, group plannerChapterBudgetGroup, batch plannerSkeletonBatch) bool {
	if len(chapters) == 0 || len(group.Indexes) != len(chapters) {
		return false
	}
	if batch.TargetTo <= 0 || batch.TargetFrom <= 0 {
		return true
	}
	for _, index := range group.Indexes {
		if chapters[index].Chapter < batch.TargetFrom || chapters[index].Chapter > batch.TargetTo {
			return false
		}
	}
	return true
}

func plannerChapterBudgetGroups(chapters []domain.AdaptationChapterPlan, sourceRunesByChapter map[int]int) map[string]plannerChapterBudgetGroup {
	groups := make(map[string]plannerChapterBudgetGroup)
	for index := range chapters {
		chapter := &chapters[index]
		key, from, to := plannerChapterBudgetGroupKey(*chapter)
		group := groups[key]
		group.Indexes = append(group.Indexes, index)
		if group.SourceFrom == 0 || from < group.SourceFrom {
			group.SourceFrom = from
		}
		if to > group.SourceTo {
			group.SourceTo = to
		}
		if group.SourceRunes <= 0 {
			group.SourceRunes = sourceRunesForRange(sourceRunesByChapter, from, to)
		}
		groups[key] = group
	}
	return groups
}

func plannerChapterBudgetGroupKey(chapter domain.AdaptationChapterPlan) (string, int, int) {
	if chapter.SourceRange.From > 0 && chapter.SourceRange.To >= chapter.SourceRange.From {
		return fmt.Sprintf("range:%d:%d", chapter.SourceRange.From, chapter.SourceRange.To), chapter.SourceRange.From, chapter.SourceRange.To
	}
	from, to := minMaxPositive(chapter.SourceChapters)
	if from > 0 && to >= from {
		return fmt.Sprintf("anchors:%d:%d:%s", from, to, intListKey(chapter.SourceChapters)), from, to
	}
	return fmt.Sprintf("chapter:%d", chapter.Chapter), 0, 0
}

func intListKey(values []int) string {
	clean := appendSortedUniqueInts(nil, values...)
	parts := make([]string, 0, len(clean))
	for _, value := range clean {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}

func appendSortedUniqueInts(base []int, values ...int) []int {
	seen := make(map[int]bool, len(base)+len(values))
	out := make([]int, 0, len(base)+len(values))
	for _, value := range append(base, values...) {
		if value <= 0 || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func plannerBudgetGroupNeedsNormalization(chapters []domain.AdaptationChapterPlan, group plannerChapterBudgetGroup, policy plannerChapterBudgetPolicy) bool {
	for _, index := range group.Indexes {
		chapter := chapters[index]
		if chapter.TargetRunes > policy.MaxRunes || chapter.TargetMaxRunes > policy.MaxRunes {
			return true
		}
		if chapter.WordBudget != nil &&
			(chapter.WordBudget.TargetRunes > policy.MaxRunes ||
				chapter.WordBudget.MaxRunes > policy.MaxRunes ||
				(len(group.Indexes) > 1 && chapter.WordBudget.SourceRunes > policy.MaxRunes)) {
			return true
		}
	}
	return false
}

func applyPlannerBudgetGroup(chapters []domain.AdaptationChapterPlan, group plannerChapterBudgetGroup, policy plannerChapterBudgetPolicy) {
	count := len(group.Indexes)
	if count == 0 {
		return
	}
	totalRunes := group.SourceRunes
	if totalRunes <= 0 {
		totalRunes = policy.TargetRunes * count
	}
	for offset, index := range group.Indexes {
		sourceRunes := splitRunesForIndex(totalRunes, count, offset)
		targetRunes := sourceRunes
		if targetRunes <= 0 {
			targetRunes = policy.TargetRunes
		}
		if targetRunes > policy.MaxRunes {
			targetRunes = policy.MaxRunes
		}
		minRunes, maxRunes := modelChapterBudgetRange(targetRunes, policy)
		chapter := &chapters[index]
		chapter.SourceRunes = sourceRunes
		chapter.TargetRunes = targetRunes
		chapter.TargetMinRunes = minRunes
		chapter.TargetMaxRunes = maxRunes
		chapter.WordBudget = &domain.AdaptationChapterWordBudget{
			SourceRunes: sourceRunes,
			TargetRunes: targetRunes,
			MinRunes:    minRunes,
			MaxRunes:    maxRunes,
			Tolerance:   policy.Tolerance,
		}
	}
}

func splitRunesForIndex(totalRunes, count, index int) int {
	if totalRunes <= 0 || count <= 0 || index < 0 {
		return 0
	}
	base := totalRunes / count
	remainder := totalRunes % count
	if index < remainder {
		return base + 1
	}
	return base
}

func modelChapterBudgetRange(targetRunes int, policy plannerChapterBudgetPolicy) (int, int) {
	minRunes, maxRunes := runeRange(targetRunes, policy.Tolerance)
	if maxRunes > policy.MaxRunes {
		maxRunes = policy.MaxRunes
	}
	if minRunes > targetRunes {
		minRunes = targetRunes
	}
	if maxRunes < targetRunes {
		maxRunes = targetRunes
	}
	if minRunes <= 0 {
		minRunes = targetRunes
	}
	if maxRunes <= 0 {
		maxRunes = targetRunes
	}
	return minRunes, maxRunes
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

func fillPlannerChapterWordBudgetDefaults(chapter *domain.AdaptationChapterPlan, sourceRunesByChapter map[int]int, tolerance float64) {
	if chapter == nil {
		return
	}
	sourceRunes := chapter.SourceRunes
	if sourceRunes <= 0 && chapter.WordBudget != nil {
		sourceRunes = chapter.WordBudget.SourceRunes
	}
	if sourceRunes <= 0 {
		sourceRunes = sourceRunesForChapterAnchors(chapter, sourceRunesByChapter)
	}
	targetRunes := chapter.TargetRunes
	if targetRunes <= 0 && chapter.WordBudget != nil {
		targetRunes = chapter.WordBudget.TargetRunes
	}
	if targetRunes <= 0 {
		targetRunes = sourceRunes
	}
	minRunes := chapter.TargetMinRunes
	if minRunes <= 0 && chapter.WordBudget != nil {
		minRunes = chapter.WordBudget.MinRunes
	}
	maxRunes := chapter.TargetMaxRunes
	if maxRunes <= 0 && chapter.WordBudget != nil {
		maxRunes = chapter.WordBudget.MaxRunes
	}
	if minRunes <= 0 || maxRunes <= 0 {
		if tolerance > 0 {
			defaultMin, defaultMax := runeRange(targetRunes, tolerance)
			if minRunes <= 0 {
				minRunes = defaultMin
			}
			if maxRunes <= 0 {
				maxRunes = defaultMax
			}
		} else {
			if minRunes <= 0 {
				minRunes = targetRunes
			}
			if maxRunes <= 0 {
				maxRunes = targetRunes
			}
		}
	}
	if sourceRunes <= 0 || targetRunes <= 0 || minRunes <= 0 || maxRunes <= 0 {
		return
	}
	if chapter.WordBudget == nil {
		chapter.WordBudget = &domain.AdaptationChapterWordBudget{}
	}
	if chapter.WordBudget.SourceRunes <= 0 {
		chapter.WordBudget.SourceRunes = sourceRunes
	}
	if chapter.WordBudget.TargetRunes <= 0 {
		chapter.WordBudget.TargetRunes = targetRunes
	}
	if chapter.WordBudget.MinRunes <= 0 {
		chapter.WordBudget.MinRunes = minRunes
	}
	if chapter.WordBudget.MaxRunes <= 0 {
		chapter.WordBudget.MaxRunes = maxRunes
	}
	if chapter.SourceRunes <= 0 {
		chapter.SourceRunes = sourceRunes
	}
}

func expandSourceChaptersForRange(chapters []int, from, to int) []int {
	if from <= 0 || to < from {
		return append([]int(nil), chapters...)
	}
	seen := make(map[int]bool, len(chapters)+to-from+1)
	out := make([]int, 0, len(chapters)+to-from+1)
	for _, chapter := range chapters {
		if chapter <= 0 || seen[chapter] {
			continue
		}
		seen[chapter] = true
		out = append(out, chapter)
	}
	for chapter := from; chapter <= to; chapter++ {
		if seen[chapter] {
			continue
		}
		seen[chapter] = true
		out = append(out, chapter)
	}
	sort.Ints(out)
	return out
}

func sourceRunesForChapterAnchors(chapter *domain.AdaptationChapterPlan, sourceRunesByChapter map[int]int) int {
	if chapter == nil || len(sourceRunesByChapter) == 0 {
		return 0
	}
	total := 0
	if chapter.SourceRange.From > 0 && chapter.SourceRange.To >= chapter.SourceRange.From {
		total = sourceRunesForRange(sourceRunesByChapter, chapter.SourceRange.From, chapter.SourceRange.To)
		if total > 0 {
			return total
		}
	}
	seen := map[int]bool{}
	for _, sourceChapter := range chapter.SourceChapters {
		if sourceChapter <= 0 || seen[sourceChapter] {
			continue
		}
		seen[sourceChapter] = true
		total += sourceRunesByChapter[sourceChapter]
	}
	if total > 0 {
		return total
	}
	return 0
}

func sourceRunesForRange(sourceRunesByChapter map[int]int, from, to int) int {
	if len(sourceRunesByChapter) == 0 || from <= 0 || to < from {
		return 0
	}
	total := 0
	for sourceChapter := from; sourceChapter <= to; sourceChapter++ {
		total += sourceRunesByChapter[sourceChapter]
	}
	return total
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
	reports, err := st.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		return nil, nil, fmt.Errorf("load source reports: %w", err)
	}
	if len(reports) != manifest.ChapterCount {
		return nil, nil, fmt.Errorf("source reports incomplete or stale; analyze source first")
	}
	if sourcePath = strings.TrimSpace(sourcePath); sourcePath != "" {
		absPath, err := filepath.Abs(sourcePath)
		if err == nil {
			sourcePath = absPath
		}
		if !sameSourcePath(manifest.SourcePath, sourcePath) {
			chapters, err := imp.SplitFile(sourcePath)
			if err != nil {
				return nil, nil, fmt.Errorf("split selected adaptation source: %w", err)
			}
			next := buildSourceManifest(sourcePath, chapters)
			if !sourceManifestMatches(manifest, next) {
				return nil, nil, fmt.Errorf("selected adaptation source has not been analyzed; run adaptation source analysis first")
			}
		}
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
