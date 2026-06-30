package tools

import (
	"fmt"
	"math"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// EnsureChapterExpanded verifies that a chapter is inside the currently
// expanded layered outline. Non-layered books and non-writing phases have no
// layered range constraint.
func EnsureChapterExpanded(st *store.Store, chapter int) error {
	if st == nil || chapter <= 0 {
		return nil
	}
	progress, err := st.Progress.Load()
	if err != nil {
		return fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if progress == nil || !progress.Layered || progress.Phase != domain.PhaseWriting {
		return nil
	}
	boundary, err := st.Outline.CheckArcBoundary(chapter)
	if err != nil {
		return fmt.Errorf("check layered outline: %w: %w", errs.ErrStoreRead, err)
	}
	if boundary != nil {
		return nil
	}
	return fmt.Errorf(
		"第 %d 章不在分层大纲范围内：写作必须先 expand_arc 扩展弧或 append_volume 追加卷；若全书已完结请调 save_foundation type=complete_book: %w",
		chapter, errs.ErrToolPrecondition)
}

// EnsureAdaptationChapterPlanned is the physical boundary for adaptation
// projects: writer-facing tools may only touch chapters in the confirmed plan.
func EnsureAdaptationChapterPlanned(st *store.Store, chapter int) error {
	if st == nil || chapter <= 0 || !st.Adaptation.Active() {
		return nil
	}
	plan, err := st.Adaptation.LoadPlan()
	if err != nil {
		return fmt.Errorf("load adaptation plan: %w: %w", errs.ErrStoreRead, err)
	}
	if plan == nil || plan.Status != domain.AdaptationPlanStatusConfirmed {
		return fmt.Errorf("改编计划尚未确认，不能进入写作: %w", errs.ErrToolPrecondition)
	}
	if _, ok := findAdaptationChapterPlan(plan, chapter); ok {
		return nil
	}
	return fmt.Errorf("改编计划中没有第 %d 章。当前 confirmed plan 只有 %d 章；如需增删/重排章节，请重新生成规模提案并确认: %w",
		chapter, len(plan.Chapters), errs.ErrToolPrecondition)
}

type adaptationWordContract struct {
	RewritePolicy       string  `json:"rewrite_policy"`
	Hard                bool    `json:"hard"`
	Scope               string  `json:"scope,omitempty"`
	Chapter             int     `json:"chapter"`
	SourceRunes         int     `json:"source_runes,omitempty"`
	TargetRunes         int     `json:"target_runes,omitempty"`
	TargetMinRunes      int     `json:"target_min_runes,omitempty"`
	TargetMaxRunes      int     `json:"target_max_runes,omitempty"`
	ActualRunes         int     `json:"actual_runes,omitempty"`
	SourceTotalRunes    int     `json:"source_total_runes,omitempty"`
	TargetTotalRunes    int     `json:"target_total_runes,omitempty"`
	TargetTotalMinRunes int     `json:"target_total_min_runes,omitempty"`
	TargetTotalMaxRunes int     `json:"target_total_max_runes,omitempty"`
	ProjectedTotalRunes int     `json:"projected_total_runes,omitempty"`
	TotalDeltaRunes     int     `json:"total_delta_runes,omitempty"`
	TotalDeltaRatio     float64 `json:"total_delta_ratio,omitempty"`
}

func buildAdaptationWordContract(st *store.Store, plan *domain.AdaptationPlan, chapterPlan domain.AdaptationChapterPlan, chapter, actualRunes int) adaptationWordContract {
	contract := adaptationWordContract{
		RewritePolicy:       domain.NormalizeAdaptationRewritePolicy(plan.RewritePolicy),
		Hard:                plan.RewritePolicy == domain.AdaptationRewritePreserveDetails,
		Chapter:             chapter,
		SourceRunes:         chapterPlan.SourceRunes,
		TargetRunes:         chapterPlan.TargetRunes,
		TargetMinRunes:      chapterPlan.TargetMinRunes,
		TargetMaxRunes:      chapterPlan.TargetMaxRunes,
		ActualRunes:         actualRunes,
		SourceTotalRunes:    plan.SourceTotalRunes,
		TargetTotalRunes:    plan.TargetTotalRunes,
		TargetTotalMinRunes: plan.TargetMinRunes,
		TargetTotalMaxRunes: plan.TargetMaxRunes,
	}
	if plan.RewritePolicy != domain.AdaptationRewritePreserveDetails {
		return contract
	}
	if plan.Granularity == domain.AdaptationGranularityFree {
		contract.Scope = "total"
	} else {
		contract.Scope = "chapter"
	}
	contract.ProjectedTotalRunes = projectedAdaptationTotalRunes(st, chapter, actualRunes)
	if plan.TargetTotalRunes > 0 {
		contract.TotalDeltaRunes = contract.ProjectedTotalRunes - plan.TargetTotalRunes
		contract.TotalDeltaRatio = float64(contract.TotalDeltaRunes) / float64(plan.TargetTotalRunes)
		contract.TotalDeltaRatio = math.Round(contract.TotalDeltaRatio*10000) / 10000
	}
	return contract
}

func adaptationWordContractIssues(st *store.Store, plan *domain.AdaptationPlan, chapterPlan domain.AdaptationChapterPlan, chapter, actualRunes int) []string {
	if plan == nil || plan.RewritePolicy != domain.AdaptationRewritePreserveDetails {
		return nil
	}
	contract := buildAdaptationWordContract(st, plan, chapterPlan, chapter, actualRunes)
	if contract.Scope != "total" && chapterPlan.TargetMinRunes > 0 && actualRunes < chapterPlan.TargetMinRunes {
		return []string{fmt.Sprintf("adaptation_word_contract: 第 %d 章 %d 字，低于硬区间 %d-%d 字",
			chapter, actualRunes, chapterPlan.TargetMinRunes, chapterPlan.TargetMaxRunes)}
	}
	if contract.Scope != "total" && chapterPlan.TargetMaxRunes > 0 && actualRunes > chapterPlan.TargetMaxRunes {
		return []string{fmt.Sprintf("adaptation_word_contract: 第 %d 章 %d 字，超过硬区间 %d-%d 字",
			chapter, actualRunes, chapterPlan.TargetMinRunes, chapterPlan.TargetMaxRunes)}
	}
	if !isLastAdaptationChapter(plan, chapter) {
		return nil
	}
	if plan.TargetMinRunes > 0 && contract.ProjectedTotalRunes < plan.TargetMinRunes {
		return []string{fmt.Sprintf("adaptation_word_contract: 当前总字数 %d，低于来源总字数硬区间 %d-%d",
			contract.ProjectedTotalRunes, plan.TargetMinRunes, plan.TargetMaxRunes)}
	}
	if plan.TargetMaxRunes > 0 && contract.ProjectedTotalRunes > plan.TargetMaxRunes {
		return []string{fmt.Sprintf("adaptation_word_contract: 当前总字数 %d，超过来源总字数硬区间 %d-%d",
			contract.ProjectedTotalRunes, plan.TargetMinRunes, plan.TargetMaxRunes)}
	}
	return nil
}

func projectedAdaptationTotalRunes(st *store.Store, chapter, actualRunes int) int {
	if st == nil {
		return actualRunes
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		return actualRunes
	}
	total := actualRunes
	for ch, count := range progress.ChapterWordCounts {
		if ch != chapter {
			total += count
		}
	}
	return total
}

func isLastAdaptationChapter(plan *domain.AdaptationPlan, chapter int) bool {
	if plan == nil || len(plan.Chapters) == 0 {
		return false
	}
	maxChapter := 0
	for _, item := range plan.Chapters {
		if item.Chapter > maxChapter {
			maxChapter = item.Chapter
		}
	}
	return chapter >= maxChapter
}
