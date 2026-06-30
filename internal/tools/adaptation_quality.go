package tools

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

const (
	adaptationSimilarityShingleRunes = 12
	adaptationSimilarityMinRunes     = 1000
	adaptationSimilarityLimit        = 0.985
)

var adaptationParentheticalResidueRe = regexp.MustCompile(`[（(][^）)\n]{0,48}(内心独白|心理活动|改编补充|改编说明|改编补丁|仅为示意|实际融入动作)[^）)\n]{0,96}[）)]`)

var adaptationForbiddenFragments = []string{
	"内心独白仅为示意",
	"实际融入动作",
	"仅为示意",
	"内心独白：",
	"内心独白:",
	"心理活动：",
	"心理活动:",
	"改编补充：",
	"改编补充:",
	"改编说明",
	"改编补丁",
}

func adaptationDraftQualityStatus(st *store.Store, chapter int, content string) ([]string, bool) {
	if st == nil || chapter <= 0 || !st.Adaptation.Active() {
		return nil, false
	}
	plan, err := st.Adaptation.LoadPlan()
	if err != nil || plan == nil || plan.Status != domain.AdaptationPlanStatusConfirmed {
		return nil, false
	}
	if plan.RewritePolicy != domain.AdaptationRewritePreserveDetails {
		return nil, false
	}
	chapterPlan, ok := findAdaptationChapterPlan(plan, chapter)
	if !ok {
		return nil, false
	}
	return adaptationDraftQualityIssues(st, plan, chapterPlan, chapter, content), true
}

func adaptationDraftQualityIssues(st *store.Store, plan *domain.AdaptationPlan, chapterPlan domain.AdaptationChapterPlan, chapter int, content string) []string {
	if plan == nil || plan.RewritePolicy != domain.AdaptationRewritePreserveDetails {
		return nil
	}
	var issues []string
	issues = append(issues, adaptationResidueIssues(content)...)
	issues = append(issues, adaptationSourceSimilarityIssues(st, chapterPlan, chapter, content)...)
	return issues
}

func adaptationResidueIssues(content string) []string {
	if hit := adaptationParentheticalResidueRe.FindString(content); hit != "" {
		return []string{fmt.Sprintf("adaptation_quality: draft contains parenthetical patch label %q; rewrite it as normal prose", shortenRunes(hit, 80))}
	}
	for _, fragment := range adaptationForbiddenFragments {
		if strings.Contains(content, fragment) {
			return []string{fmt.Sprintf("adaptation_quality: draft contains patch label %q; rewrite it as normal prose", fragment)}
		}
	}
	return nil
}

func adaptationSourceSimilarityIssues(st *store.Store, chapterPlan domain.AdaptationChapterPlan, chapter int, content string) []string {
	if st == nil || !adaptationRequiresVisibleChanges(chapterPlan) {
		return nil
	}
	source := loadAdaptationSourceText(st, chapterPlan.SourceChapters)
	sourceRunes := compactNonSpaceRunes(source)
	draftRunes := compactNonSpaceRunes(content)
	if len(sourceRunes) < adaptationSimilarityMinRunes || len(draftRunes) < adaptationSimilarityMinRunes {
		return nil
	}
	similarity, ok := shingleJaccard(sourceRunes, draftRunes, adaptationSimilarityShingleRunes)
	if !ok || similarity < adaptationSimilarityLimit {
		return nil
	}
	return []string{fmt.Sprintf(
		"adaptation_source_similarity: chapter %d is %.1f%% similar to source refs despite required_changes; rewrite affected full scene units as new prose",
		chapter, similarity*100,
	)}
}

func adaptationChangeEvidenceIssues(plan *domain.AdaptationPlan, chapterPlan domain.AdaptationChapterPlan, evidence []domain.AdaptationChangeEvidence) []string {
	if plan == nil ||
		plan.RewritePolicy != domain.AdaptationRewritePreserveDetails ||
		!adaptationRequiresVisibleChanges(chapterPlan) {
		return nil
	}
	if len(evidence) == 0 {
		return []string{"adaptation_change_evidence: preserve_details with required_changes must provide change_evidence"}
	}
	var issues []string
	for i, item := range evidence {
		if strings.TrimSpace(item.Change) == "" {
			issues = append(issues, fmt.Sprintf("adaptation_change_evidence: item %d missing change", i+1))
		}
		if strings.TrimSpace(item.Integration) == "" {
			issues = append(issues, fmt.Sprintf("adaptation_change_evidence: item %d missing integration", i+1))
		}
		if item.SourceChapter <= 0 && strings.TrimSpace(item.SourceAnchor) == "" {
			issues = append(issues, fmt.Sprintf("adaptation_change_evidence: item %d needs source_chapter or source_anchor", i+1))
		}
	}
	return issues
}

func adaptationRequiresVisibleChanges(chapterPlan domain.AdaptationChapterPlan) bool {
	for _, change := range chapterPlan.RequiredChanges {
		if strings.TrimSpace(change) != "" {
			return true
		}
	}
	return false
}

func adaptationQualityRepairStep(issues []string, chapter int) string {
	for _, issue := range issues {
		switch {
		case strings.Contains(issue, "adaptation_quality"):
			return fmt.Sprintf("do not commit chapter %d; call draft_chapter(mode=\"write\") and 删除所有 meta labels, writing the material as normal narration, action, dialogue, or subtext", chapter)
		case strings.Contains(issue, "adaptation_source_similarity"):
			return fmt.Sprintf("do not commit chapter %d; read source refs, keep unaffected paragraphs if needed, and rewrite every required-change scene unit as original prose", chapter)
		case strings.Contains(issue, "adaptation_change_evidence"):
			return fmt.Sprintf("do not commit chapter %d; call check_adaptation with a non-empty change_evidence JSON array. Each item must include source_chapter or source_anchor, change, and integration. Do not put evidence only in summary", chapter)
		}
	}
	return ""
}

func adaptationProseQualityRepairStep(issues []string, chapter int) string {
	for _, issue := range issues {
		if strings.Contains(issue, "adaptation_quality") || strings.Contains(issue, "adaptation_source_similarity") {
			return adaptationQualityRepairStep([]string{issue}, chapter)
		}
	}
	return ""
}

func cleanChangeEvidence(items []domain.AdaptationChangeEvidence) []domain.AdaptationChangeEvidence {
	out := make([]domain.AdaptationChangeEvidence, 0, len(items))
	for _, item := range items {
		item.SourceAnchor = strings.TrimSpace(item.SourceAnchor)
		item.Change = strings.TrimSpace(item.Change)
		item.Integration = strings.TrimSpace(item.Integration)
		if item.SourceChapter <= 0 && item.SourceAnchor == "" && item.Change == "" && item.Integration == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func loadAdaptationSourceText(st *store.Store, refs []int) string {
	if st == nil || len(refs) == 0 {
		return ""
	}
	var parts []string
	for _, ref := range refs {
		text, _, err := st.Adaptation.LoadSourceChapter(ref)
		if err != nil {
			continue
		}
		text = strings.TrimSpace(text)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func compactNonSpaceRunes(text string) []rune {
	out := make([]rune, 0, len(text))
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func shingleJaccard(a, b []rune, size int) (float64, bool) {
	if size <= 0 || len(a) < size || len(b) < size {
		return 0, false
	}
	left := make(map[string]struct{}, len(a)-size+1)
	for i := 0; i <= len(a)-size; i++ {
		left[string(a[i:i+size])] = struct{}{}
	}
	seenRight := make(map[string]struct{}, len(b)-size+1)
	common := 0
	union := len(left)
	for i := 0; i <= len(b)-size; i++ {
		shingle := string(b[i : i+size])
		if _, ok := seenRight[shingle]; ok {
			continue
		}
		seenRight[shingle] = struct{}{}
		if _, ok := left[shingle]; ok {
			common++
			continue
		}
		union++
	}
	if union == 0 {
		return 0, false
	}
	return float64(common) / float64(union), true
}

func shortenRunes(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if limit <= 0 || len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "..."
}
