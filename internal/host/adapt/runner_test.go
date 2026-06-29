package adapt

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestBuildPlanFromBriefSupportsGranularities(t *testing.T) {
	reports := []domain.AdaptationSourceReport{
		{Chapter: 1, Title: "起", KeyEvents: []string{"主角入局"}},
		{Chapter: 2, Title: "承", KeyEvents: []string{"女主登场"}},
	}
	cases := []struct {
		name  string
		brief string
		want  string
	}{
		{name: "chapter default", brief: "逐章改写，主线不要走偏", want: domain.AdaptationGranularityChapter},
		{name: "arc", brief: "允许按弧合并拆分章节，但保留主线", want: domain.AdaptationGranularityArc},
		{name: "free", brief: "自由重构章节结构，核心命运不变", want: domain.AdaptationGranularityFree},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := BuildPlanFromBrief(tc.brief, reports)
			if plan.Granularity != tc.want {
				t.Fatalf("granularity=%s, want %s", plan.Granularity, tc.want)
			}
			if len(plan.Chapters) != len(reports) {
				t.Fatalf("chapters=%d, want %d", len(plan.Chapters), len(reports))
			}
			if got := plan.Chapters[0].SourceChapters; len(got) != 1 || got[0] != 1 {
				t.Fatalf("source refs not preserved: %+v", got)
			}
			if len(plan.Chapters[0].PreserveEvents) == 0 {
				t.Fatalf("preserve events should come from source reports")
			}
		})
	}
}

func TestPrepareRunWorksAfterResetGenerated(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	source, err := st.Adaptation.SaveSourceChapter(1, "Opening", "source chapter body")
	if err != nil {
		t.Fatalf("SaveSourceChapter: %v", err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 1,
		Chapters:     []domain.AdaptationSource{source},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	if err := st.Adaptation.SaveSourceFoundation(testSourceFoundation()); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	if err := st.Adaptation.SaveSourceReports([]domain.AdaptationSourceReport{
		{Chapter: 1, Title: "Opening", KeyEvents: []string{"Ari accepts the call"}},
	}); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}
	if err := st.Adaptation.SavePlan(domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Brief:       "old generated plan",
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "Old", SourceChapters: []int{1}},
		},
	}); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if err := st.Adaptation.SaveCheck(domain.AdaptationCheck{
		Chapter:     1,
		DraftSHA256: store.TextSHA256("old draft"),
		Passed:      true,
		CheckedAt:   "2026-06-29T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveCheck: %v", err)
	}
	if err := st.Adaptation.ResetGenerated(); err != nil {
		t.Fatalf("ResetGenerated: %v", err)
	}

	brief := "arc rewrite with warmer relationship beats"
	plan, err := PrepareRun(context.Background(), Deps{Store: st}, brief)
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	if plan.Brief != brief || plan.Granularity != domain.AdaptationGranularityArc {
		t.Fatalf("plan mismatch: %+v", plan)
	}
	if len(plan.Chapters) != 1 || len(plan.Chapters[0].SourceChapters) != 1 || plan.Chapters[0].SourceChapters[0] != 1 {
		t.Fatalf("chapter plan should come from source reports: %+v", plan.Chapters)
	}

	savedPlan, err := st.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if savedPlan == nil || savedPlan.Brief != brief {
		t.Fatalf("saved plan mismatch: %+v", savedPlan)
	}
	if check, err := st.Adaptation.LoadCheck(1); err != nil || check != nil {
		t.Fatalf("old checks should stay removed: check=%+v err=%v", check, err)
	}
	premise, err := st.Outline.LoadPremise()
	if err != nil {
		t.Fatalf("LoadPremise: %v", err)
	}
	if !strings.Contains(premise, brief) {
		t.Fatalf("adaptation brief should be persisted into premise: %q", premise)
	}
	sourceText, loadedSource, err := st.Adaptation.LoadSourceChapter(1)
	if err != nil {
		t.Fatalf("LoadSourceChapter: %v", err)
	}
	if sourceText != "source chapter body" || loadedSource == nil {
		t.Fatalf("source snapshot should remain available: text=%q source=%+v", sourceText, loadedSource)
	}
}

func testSourceFoundation() domain.AdaptationSourceFoundation {
	return domain.AdaptationSourceFoundation{
		Premise: "# Source Book\n\nA compact source premise.",
		Characters: []domain.Character{
			{Name: "Ari", Role: "lead", Description: "keeps the plot moving", Arc: "chooses courage", Traits: []string{"focused"}},
		},
		WorldRules: []domain.WorldRule{
			{Category: "society", Rule: "No supernatural shortcuts", Boundary: "events stay grounded"},
		},
		Volumes: []domain.VolumeOutline{
			{
				Index: 1,
				Title: "Volume One",
				Theme: "Ari chooses the road",
				Arcs: []domain.ArcOutline{
					{
						Index: 1,
						Title: "Call",
						Goal:  "Ari commits",
						Chapters: []domain.OutlineEntry{
							{Title: "Opening", CoreEvent: "Ari accepts the call", Hook: "a promise is made", Scenes: []string{"station"}},
						},
					},
				},
			},
		},
		Compass: &domain.StoryCompass{
			EndingDirection: "Ari keeps the promise",
			OpenThreads:     []string{"who sent the call"},
			EstimatedScale:  "1 chapter",
		},
	}
}
