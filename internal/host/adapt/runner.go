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

	var proposal domain.AdaptationPlan
	if opts.Granularity == domain.AdaptationGranularityChapter {
		proposal = buildPlanFromInputs(opts, reports, manifest, domain.AdaptationPlanStatusProposal)
	} else {
		proposal, err = buildPlanFromPlanner(ctx, deps, opts, reports, manifest, sourceFoundation)
		if err != nil {
			return nil, fmt.Errorf("build %s adaptation proposal from planner: %w", opts.Granularity, err)
		}
	}
	if err := deps.Store.Adaptation.SaveProposal(proposal); err != nil {
		return nil, fmt.Errorf("save adaptation proposal: %w", err)
	}
	return &proposal, nil
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
	skeletonText, err := generatePlannerText(ctx, deps.LLM, systemPrompt, skeletonPrompt, adaptationPlannerSkeletonMaxTokens)
	if err != nil {
		return zero, fmt.Errorf("planner skeleton llm generate: %w", err)
	}
	skeleton, err := parsePlannerSkeleton(skeletonText)
	if err != nil {
		return zero, fmt.Errorf("planner skeleton: %w", err)
	}
	if err := normalizePlannerSkeleton(&skeleton, opts, manifest, targetChapterHint); err != nil {
		return zero, fmt.Errorf("planner skeleton: %w", err)
	}

	chapters := make([]domain.AdaptationChapterPlan, 0, skeleton.TargetChapterCount)
	for _, batch := range skeleton.Batches {
		batchPrompt, err := buildAdaptationPlannerBatchUserPrompt(opts, manifest, sourceFoundation, skeleton, batch, reportsForPlannerBatch(reports, batch))
		if err != nil {
			return zero, err
		}
		batchText, err := generatePlannerText(ctx, deps.LLM, systemPrompt, batchPrompt, adaptationPlannerMaxTokens)
		if err != nil {
			return zero, fmt.Errorf("planner batch %d llm generate: %w", batch.Index, err)
		}
		batchPlan, err := parsePlannerProposal(batchText)
		if err != nil {
			return zero, fmt.Errorf("planner batch %d: %w", batch.Index, err)
		}
		batchChapters, err := normalizePlannerBatchChapters(batchPlan.Chapters, batch)
		if err != nil {
			return zero, fmt.Errorf("planner batch %d: %w", batch.Index, err)
		}
		chapters = append(chapters, batchChapters...)
	}

	proposal := domain.AdaptationPlan{
		Granularity:       opts.Granularity,
		Status:            domain.AdaptationPlanStatusProposal,
		RewritePolicy:     opts.RewritePolicy,
		Brief:             opts.Brief,
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
			"Do not return the final AdaptationPlan here.",
			"Choose the target chapter count and divide it into model-planned batches/volumes.",
			"If target_chapter_hint is present, honor that long-form scale instead of shrinking the proposal to a short outline.",
			"Each batch must include target_from, target_to, source_from, source_to, title, theme/goal, and summary.",
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
		"Return JSON shaped like {\"granularity\":\"...\",\"status\":\"proposal\",\"rewrite_policy\":\"...\",\"brief\":\"...\",\"target_chapter_count\":60,\"mainline_rules\":[],\"relationship_goals\":[],\"batches\":[{\"index\":1,\"title\":\"...\",\"theme\":\"...\",\"target_from\":1,\"target_to\":8,\"source_from\":1,\"source_to\":3,\"summary\":\"...\"}]}.\n\n" +
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
		SourceManifest:   manifest,
		SourceFoundation: sourceFoundation,
		Skeleton:         skeleton,
		Batch:            batch,
		SourceReports:    reports,
		Requirements: []string{
			"Return exactly one JSON object and no prose.",
			"Return only the chapters for the requested batch.",
			"Use absolute target chapter numbers from batch.target_from through batch.target_to.",
			"Every returned chapter must include title, core_event, hook, scenes, source_chapters, source_range, word_budget, preserve_events, required_changes, and forbidden_moves.",
			"Every source_chapters value must be within the batch source range and valid for the analyzed source.",
			"Added/bridging chapters must still include source_chapters anchors.",
			"Use the user's adaptation brief and the skeleton batch goal; do not ignore earlier user planning.",
		},
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal planner batch input: %w", err)
	}
	return fmt.Sprintf("Expand model-planned adaptation batch %d into concrete chapter plans.\n\nPlanning input:\n```json\n%s\n```", batch.Index, string(raw)), nil
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
	if len(chapters) == 0 {
		return nil, fmt.Errorf("no chapters")
	}
	wantCount := batch.TargetTo - batch.TargetFrom + 1
	if len(chapters) != wantCount {
		return nil, fmt.Errorf("chapter count=%d, want %d for target range %d-%d", len(chapters), wantCount, batch.TargetFrom, batch.TargetTo)
	}
	out := append([]domain.AdaptationChapterPlan(nil), chapters...)
	if out[0].Chapter == 1 && batch.TargetFrom > 1 {
		offset := batch.TargetFrom - 1
		for idx := range out {
			out[idx].Chapter += offset
		}
	}
	for idx := range out {
		wantChapter := batch.TargetFrom + idx
		if out[idx].Chapter != wantChapter {
			return nil, fmt.Errorf("chapter %d at batch index %d, want %d", out[idx].Chapter, idx, wantChapter)
		}
	}
	return out, nil
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
			if parsed, err := strconv.Atoi(strings.TrimSpace(text)); err == nil && parsed > 0 {
				return parsed
			}
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
			"status must be proposal; rewrite_policy must be full_rewrite for arc/free.",
			"Target chapters must be numbered continuously from 1.",
			"Every target chapter must include legal source_chapters anchors within the analyzed source range.",
			"Every source chapter must be covered by at least one target chapter.",
			"Added chapters must still include source_chapters anchors.",
			"Every chapter must include non-empty core_event, hook, scenes, and word_budget.",
		},
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal planner input: %w", err)
	}
	return "Use the following analyzed source foundation and reports to plan the adaptation proposal.\n\n```json\n" +
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
