package domain

import "testing"

func TestAdaptationOutlineQualityAuditInvalidatesWhenContractChanges(t *testing.T) {
	plan := AdaptationPlan{
		Granularity:  AdaptationGranularityArc,
		SourceEvents: []AdaptationEvent{{ID: "event-1", Description: "黑衣人拦路抢劫", Importance: AdaptationEventSupporting}},
		Chapters: []AdaptationChapterPlan{{
			Chapter:      1,
			OutlineEntry: OutlineEntry{Title: "冲突", CoreEvent: "黑衣人拦路抢劫。"},
			EventIDs:     []string{"event-1"},
		}},
	}
	MarkAdaptationOutlineQualityPassed(&plan)
	if !AdaptationOutlineQualityPassed(plan) {
		t.Fatal("fresh outline audit should be reusable")
	}
	plan.Chapters[0].EventIDs = nil
	if AdaptationOutlineQualityPassed(plan) {
		t.Fatal("changing event ownership must invalidate outline audit")
	}
}

func TestValidateArcEventOutlineThemesFindsLaterOwner(t *testing.T) {
	plan := AdaptationPlan{
		Granularity:  AdaptationGranularityArc,
		SourceEvents: []AdaptationEvent{{ID: "event-1", Description: "黑衣人拦路抢劫", Importance: AdaptationEventSupporting}},
		Chapters: []AdaptationChapterPlan{
			{Chapter: 1, OutlineEntry: OutlineEntry{CoreEvent: "主角返校与室友重逢。"}, EventIDs: []string{"event-1"}},
			{Chapter: 2, OutlineEntry: OutlineEntry{CoreEvent: "黑衣人拦路抢劫，主角出手制止。"}},
		},
	}
	issues := ValidateArcEventOutlineThemes(plan)
	if len(issues) != 1 || issues[0].TargetChapter != 1 || issues[0].AlternativeChapters[0] != 2 {
		t.Fatalf("unexpected theme mismatch: %+v", issues)
	}
}
