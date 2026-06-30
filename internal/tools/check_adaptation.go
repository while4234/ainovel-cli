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

// CheckAdaptationTool records the adaptation review for the current draft.
type CheckAdaptationTool struct {
	store *store.Store
}

func NewCheckAdaptationTool(store *store.Store) *CheckAdaptationTool {
	return &CheckAdaptationTool{store: store}
}

func (t *CheckAdaptationTool) Name() string { return "check_adaptation" }
func (t *CheckAdaptationTool) Description() string {
	return "Adaptation-only gate: compare source refs, adaptation plan, and current draft before commit_chapter."
}
func (t *CheckAdaptationTool) Label() string { return "adaptation check" }

func (t *CheckAdaptationTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *CheckAdaptationTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *CheckAdaptationTool) Schema() map[string]any {
	changeEvidenceSchema := schema.Object(
		schema.Property("source_chapter", schema.Int("source chapter number for the changed scene")),
		schema.Property("source_anchor", schema.String("short source anchor or scene reference")),
		schema.Property("change", schema.String("required adaptation change that was applied")).Required(),
		schema.Property("integration", schema.String("how the change appears inside normal prose, not as a note")),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("target chapter number")).Required(),
		schema.Property("passed", schema.Bool("whether the draft satisfies the adaptation contract")).Required(),
		schema.Property("summary", schema.String("review summary: preserved source events and implemented changes")),
		schema.Property("issues", schema.Array("unmet requirements; any issue makes the check fail", schema.String(""))),
		schema.Property("change_evidence", schema.Array("preserve_details only: evidence that required changes were integrated into prose", changeEvidenceSchema)),
	)
}

func (t *CheckAdaptationTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter        int                               `json:"chapter"`
		Passed         bool                              `json:"passed"`
		Summary        string                            `json:"summary"`
		Issues         []string                          `json:"issues"`
		ChangeEvidence []domain.AdaptationChangeEvidence `json:"change_evidence"`
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
		return nil, fmt.Errorf("current project is not in adaptation mode: %w", errs.ErrToolPrecondition)
	}
	if plan.Status != domain.AdaptationPlanStatusConfirmed {
		return nil, fmt.Errorf("adaptation plan is not confirmed: %w", errs.ErrToolPrecondition)
	}
	chapterPlan, ok := findAdaptationChapterPlan(plan, a.Chapter)
	if !ok {
		return nil, fmt.Errorf("adaptation plan has no 第 %d 章: %w", a.Chapter, errs.ErrToolPrecondition)
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
	contract := buildAdaptationWordContract(t.store, plan, chapterPlan, a.Chapter, wordCount)
	issues = append(issues, adaptationWordContractIssues(t.store, plan, chapterPlan, a.Chapter, wordCount)...)
	issues = append(issues, adaptationDraftQualityIssues(t.store, plan, chapterPlan, a.Chapter, content)...)
	changeEvidence := cleanChangeEvidence(a.ChangeEvidence)
	issues = append(issues, adaptationChangeEvidenceIssues(plan, chapterPlan, changeEvidence)...)

	passed := a.Passed && len(issues) == 0
	digest := store.TextSHA256(content)
	check := domain.AdaptationCheck{
		Chapter:        a.Chapter,
		DraftSHA256:    digest,
		Passed:         passed,
		Summary:        strings.TrimSpace(a.Summary),
		Issues:         issues,
		ChangeEvidence: changeEvidence,
		CheckedAt:      time.Now().Format(time.RFC3339),
	}
	if err := t.store.Adaptation.SaveCheck(check); err != nil {
		return nil, fmt.Errorf("save adaptation check: %w: %w", errs.ErrStoreWrite, err)
	}

	return json.Marshal(map[string]any{
		"chapter":                  a.Chapter,
		"passed":                   passed,
		"draft_sha256":             digest,
		"word_count":               wordCount,
		"issues":                   issues,
		"change_evidence":          changeEvidence,
		"source_refs":              sourceRefs,
		"next_step":                adaptationCheckNextStep(passed, issues, contract, a.Chapter),
		"chapter_plan":             chapterPlan,
		"plan_granularity":         plan.Granularity,
		"rewrite_policy":           plan.RewritePolicy,
		"adaptation_word_contract": contract,
	})
}

func adaptationCheckNextStep(passed bool, issues []string, contract adaptationWordContract, chapter int) string {
	if passed {
		return "adaptation check passed; continue with check_consistency if needed, then commit_chapter."
	}
	if repair := adaptationProseQualityRepairStep(issues, chapter); repair != "" {
		return "adaptation check failed: " + repair
	}
	if repair := adaptationWordContractRepairStep(contract, issues, chapter); repair != "" {
		return "adaptation check failed: " + repair + " Then call check_adaptation again."
	}
	if repair := adaptationQualityRepairStep(issues, chapter); repair != "" {
		return "adaptation check failed: " + repair
	}
	return "adaptation check failed: fix issues, then call check_adaptation again."
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
