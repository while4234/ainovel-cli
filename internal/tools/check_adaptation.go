package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// CheckAdaptationTool records the writer's adaptation review for the current draft.
type CheckAdaptationTool struct {
	store *store.Store
}

func NewCheckAdaptationTool(store *store.Store) *CheckAdaptationTool {
	return &CheckAdaptationTool{store: store}
}

func (t *CheckAdaptationTool) Name() string { return "check_adaptation" }
func (t *CheckAdaptationTool) Description() string {
	return "改编模式专用：对照 source refs、改编计划和当前草稿，记录主线保持/改编目标校验结果。必须在 commit_chapter 前调用"
}
func (t *CheckAdaptationTool) Label() string { return "改编校验" }

func (t *CheckAdaptationTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *CheckAdaptationTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *CheckAdaptationTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("要校验的目标章节号")).Required(),
		schema.Property("passed", schema.Bool("是否确认草稿已经满足主线保持和改编目标")).Required(),
		schema.Property("summary", schema.String("校验结论摘要：说明保留了哪些主线事件、落实了哪些改编要求")),
		schema.Property("issues", schema.Array("未满足的问题；只要有问题就会记录为 fail", schema.String(""))),
	)
}

func (t *CheckAdaptationTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter int      `json:"chapter"`
		Passed  bool     `json:"passed"`
		Summary string   `json:"summary"`
		Issues  []string `json:"issues"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}

	plan, err := t.store.Adaptation.LoadPlan()
	if err != nil {
		return nil, fmt.Errorf("load adaptation plan: %w: %w", errs.ErrStoreRead, err)
	}
	if plan == nil {
		return nil, fmt.Errorf("当前项目不是改编模式，无法调用 check_adaptation: %w", errs.ErrToolPrecondition)
	}
	chapterPlan, ok := findAdaptationChapterPlan(plan, a.Chapter)
	if !ok {
		return nil, fmt.Errorf("改编计划中没有第 %d 章，无法校验: %w", a.Chapter, errs.ErrToolPrecondition)
	}

	content, wordCount, err := t.store.Drafts.LoadChapterContent(a.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load draft: %w: %w", errs.ErrStoreRead, err)
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("no draft found for chapter %d: %w", a.Chapter, errs.ErrToolPrecondition)
	}

	var missingSourceRefs []int
	sourceRefs := make(map[int]map[string]any)
	for _, ref := range chapterPlan.SourceChapters {
		text, source, readErr := t.store.Adaptation.LoadSourceChapter(ref)
		if readErr != nil {
			return nil, fmt.Errorf("load source chapter %d: %w: %w", ref, errs.ErrStoreRead, readErr)
		}
		if source == nil || strings.TrimSpace(text) == "" {
			missingSourceRefs = append(missingSourceRefs, ref)
			continue
		}
		sourceRefs[ref] = map[string]any{
			"title": source.Title,
			"runes": source.Runes,
		}
	}

	issues := cleanIssueList(a.Issues)
	if len(missingSourceRefs) > 0 {
		issues = append(issues, fmt.Sprintf("source refs missing: %v", missingSourceRefs))
	}
	passed := a.Passed && len(issues) == 0
	digest := store.TextSHA256(content)
	check := domain.AdaptationCheck{
		Chapter:     a.Chapter,
		DraftSHA256: digest,
		Passed:      passed,
		Summary:     strings.TrimSpace(a.Summary),
		Issues:      issues,
		CheckedAt:   time.Now().Format(time.RFC3339),
	}
	if err := t.store.Adaptation.SaveCheck(check); err != nil {
		return nil, fmt.Errorf("save adaptation check: %w: %w", errs.ErrStoreWrite, err)
	}

	return json.Marshal(map[string]any{
		"chapter":          a.Chapter,
		"passed":           passed,
		"draft_sha256":     digest,
		"word_count":       wordCount,
		"issues":           issues,
		"source_refs":      sourceRefs,
		"next_step":        adaptationCheckNextStep(passed),
		"chapter_plan":     chapterPlan,
		"plan_granularity": plan.Granularity,
	})
}

func adaptationCheckNextStep(passed bool) string {
	if passed {
		return "改编校验通过：可以继续 check_consistency（如尚未执行）并 commit_chapter。"
	}
	return "改编校验失败：先按 issues 修正草稿，再重新调用 check_adaptation。"
}

func cleanIssueList(items []string) []string {
	var out []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func findAdaptationChapterPlan(plan *domain.AdaptationPlan, chapter int) (domain.AdaptationChapterPlan, bool) {
	if plan == nil {
		return domain.AdaptationChapterPlan{}, false
	}
	for _, item := range plan.Chapters {
		if item.Chapter == chapter {
			return item, true
		}
	}
	return domain.AdaptationChapterPlan{}, false
}
