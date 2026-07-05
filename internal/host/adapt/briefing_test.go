package adapt

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestBuildCoCreateIntentInfersGoalFromUserRequest(t *testing.T) {
	heroineOnly := BuildCoCreateIntent("只增加女主戏份和男女主互动，不清理女配", domain.AdaptationGranularityArc, domain.AdaptationRewriteFullRewrite, 0)
	if !containsText(heroineOnly.Goals, "heroine presence") {
		t.Fatalf("heroine-only goals = %v, want heroine presence goal", heroineOnly.Goals)
	}
	if containsText(heroineOnly.Goals, "strict single-heroine") {
		t.Fatalf("heroine-only goals = %v, should not force strict single-heroine cleanup", heroineOnly.Goals)
	}

	strictSingle := BuildCoCreateIntent("后宫改严格单女主，女配不能有暧昧和身体接触", domain.AdaptationGranularityFree, domain.AdaptationRewriteFullRewrite, 0)
	if !containsText(strictSingle.Goals, "strict single-heroine") {
		t.Fatalf("strict-single goals = %v, want strict single-heroine goal", strictSingle.Goals)
	}
}

func TestCoCreateBriefingTriggerReasonUsesLongNovelThresholds(t *testing.T) {
	if reason := CoCreateBriefingTriggerReason(domain.AdaptationCoCreateDossier{SourceChapterCount: 40, Batches: make([]domain.AdaptationCoCreateDossierBatch, 1)}); reason != "" {
		t.Fatalf("short dossier trigger = %q, want empty", reason)
	}
	if reason := CoCreateBriefingTriggerReason(domain.AdaptationCoCreateDossier{SourceChapterCount: 321, Batches: make([]domain.AdaptationCoCreateDossierBatch, 9)}); !strings.Contains(reason, "source_chapter_count") {
		t.Fatalf("long dossier trigger = %q, want source chapter threshold", reason)
	}
}

func TestNormalizeBriefingDecisionsRejectsVagueQuestions(t *testing.T) {
	decisions := normalizeBriefingDecisions([]domain.AdaptationBriefingDecision{
		{
			ID:       "bad",
			Question: "是否符合预期？",
			Evidence: "chapter 1",
			Impact:   "none",
			Options: []domain.AdaptationDecisionOption{
				{ID: "a", Label: "Yes"},
				{ID: "b", Label: "No"},
			},
		},
		{
			ID:       "good",
			Question: "Should the side confession be removed or rewritten as ordinary trust?",
			Evidence: "chapter 90 confession",
			Impact:   "changes single-heroine cleanup in late arcs",
			Options: []domain.AdaptationDecisionOption{
				{ID: "a", Label: "Remove"},
				{ID: "b", Label: "Rewrite as trust"},
			},
		},
	}, "q", 8)
	if len(decisions) != 1 || decisions[0].ID != "good" {
		t.Fatalf("decisions = %+v, want only the concrete question", decisions)
	}
}

func containsText(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
