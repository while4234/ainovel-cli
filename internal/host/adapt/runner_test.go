package adapt

import (
	"context"
	"fmt"
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
	report := domain.AdaptationSourceReport{Chapter: 1, Title: "Opening", SourceSHA256: source.SHA256, Summary: "Ari starts", KeyEvents: []string{"Ari accepts the call"}}
	if err := st.Adaptation.SaveSourceReport(report); err != nil {
		t.Fatalf("SaveSourceReport: %v", err)
	}
	if err := st.Adaptation.SaveSourceReports([]domain.AdaptationSourceReport{report}); err != nil {
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

	brief := "chapter rewrite with warmer relationship beats"
	plan, err := PrepareRun(context.Background(), Deps{Store: st}, brief)
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	if plan.Brief != brief || plan.Granularity != domain.AdaptationGranularityChapter {
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

func TestConfirmAdaptationProposalPersistsTargetOutlinesAndProgress(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Adaptation.SaveSourceFoundation(testSourceFoundation()); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	proposal := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityArc,
		Status:        domain.AdaptationPlanStatusProposal,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Brief:         "merge three source chapters into two target chapters",
		Chapters: []domain.AdaptationChapterPlan{
			{
				Chapter:        1,
				Title:          "Merged Opening",
				SourceChapters: []int{1, 2},
				OutlineEntry: domain.OutlineEntry{
					CoreEvent: "Ari combines the first two source turns.",
					Hook:      "A shared clue reframes both turns.",
					Scenes:    []string{"station", "archive"},
				},
			},
			{
				Chapter:        2,
				Title:          "Target Turn",
				SourceChapters: []int{3},
				OutlineEntry: domain.OutlineEntry{
					CoreEvent: "Ari pays off the third source turn.",
					Hook:      "The next door opens.",
					Scenes:    []string{"roof"},
				},
			},
		},
	}

	confirmed, err := ConfirmAdaptationProposal(context.Background(), Deps{Store: st}, proposal)
	if err != nil {
		t.Fatalf("ConfirmAdaptationProposal: %v", err)
	}
	if confirmed.Status != domain.AdaptationPlanStatusConfirmed {
		t.Fatalf("confirmed status=%q, want confirmed", confirmed.Status)
	}
	flat, err := st.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("LoadOutline: %v", err)
	}
	if len(flat) != 2 || flat[0].Title != "Merged Opening" || flat[1].Title != "Target Turn" {
		t.Fatalf("flat outline should come from target plan: %+v", flat)
	}
	layered, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("LoadLayeredOutline: %v", err)
	}
	if got := domain.TotalChapters(layered); got != 2 {
		t.Fatalf("layered target chapters=%d, want 2: %+v", got, layered)
	}
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	if progress == nil || progress.TotalChapters != 2 || len(progress.CompletedChapters) != 0 {
		t.Fatalf("progress should be reset to target count: %+v", progress)
	}
	savedPlan, err := st.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if savedPlan == nil || savedPlan.Status != domain.AdaptationPlanStatusConfirmed || len(savedPlan.Chapters) != 2 {
		t.Fatalf("confirmed plan not saved: %+v", savedPlan)
	}
}

func TestBuildAdaptationProposalChapterPreserveDetailsUsesSourceRuneRanges(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	source1, err := st.Adaptation.SaveSourceChapter(1, "One", strings.Repeat("一", 10))
	if err != nil {
		t.Fatalf("SaveSourceChapter 1: %v", err)
	}
	source2, err := st.Adaptation.SaveSourceChapter(2, "Two", strings.Repeat("二", 20))
	if err != nil {
		t.Fatalf("SaveSourceChapter 2: %v", err)
	}
	source3, err := st.Adaptation.SaveSourceChapter(3, "Three", strings.Repeat("三", 30))
	if err != nil {
		t.Fatalf("SaveSourceChapter 3: %v", err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 3,
		Chapters:     []domain.AdaptationSource{source1, source2, source3},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	if err := st.Adaptation.SaveSourceFoundation(testSourceFoundation()); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	reports := []domain.AdaptationSourceReport{
		{Chapter: 1, Title: "One", SourceSHA256: source1.SHA256, Summary: "one", KeyEvents: []string{"event one"}},
		{Chapter: 2, Title: "Two", SourceSHA256: source2.SHA256, Summary: "two", KeyEvents: []string{"event two"}},
		{Chapter: 3, Title: "Three", SourceSHA256: source3.SHA256, Summary: "three", KeyEvents: []string{"event three"}},
	}
	for _, report := range reports {
		if err := st.Adaptation.SaveSourceReport(report); err != nil {
			t.Fatalf("SaveSourceReport %d: %v", report.Chapter, err)
		}
	}
	if err := st.Adaptation.SaveSourceReports(reports); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}

	proposal, err := BuildAdaptationProposal(Deps{Store: st}, ProposalOptions{
		Brief:         "逐章保留原著细节",
		Granularity:   domain.AdaptationGranularityChapter,
		RewritePolicy: domain.AdaptationRewritePreserveDetails,
		WordTolerance: 0.15,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if proposal.Status != domain.AdaptationPlanStatusProposal {
		t.Fatalf("status=%s, want proposal", proposal.Status)
	}
	if st.Adaptation.Active() {
		t.Fatal("proposal should not activate adaptation project")
	}
	if len(proposal.Chapters) != 3 {
		t.Fatalf("chapters=%d, want 3", len(proposal.Chapters))
	}
	wantRanges := []struct {
		source int
		min    int
		max    int
	}{
		{source: 10, min: 9, max: 12},
		{source: 20, min: 17, max: 23},
		{source: 30, min: 26, max: 35},
	}
	for i, want := range wantRanges {
		chapter := proposal.Chapters[i]
		if chapter.Chapter != i+1 || len(chapter.SourceChapters) != 1 || chapter.SourceChapters[0] != i+1 {
			t.Fatalf("chapter mapping mismatch at %d: %+v", i, chapter)
		}
		if chapter.SourceRunes != want.source || chapter.TargetRunes != want.source {
			t.Fatalf("source/target runes mismatch at %d: %+v", i, chapter)
		}
		if chapter.TargetMinRunes != want.min || chapter.TargetMaxRunes != want.max {
			t.Fatalf("range mismatch at %d: got %d-%d want %d-%d", i, chapter.TargetMinRunes, chapter.TargetMaxRunes, want.min, want.max)
		}
	}
	if proposal.SourceTotalRunes != 60 || proposal.TargetTotalRunes != 60 || proposal.TargetMinRunes != 51 || proposal.TargetMaxRunes != 69 {
		t.Fatalf("total range mismatch: %+v", proposal)
	}
}

func TestBuildAdaptationProposalArcUsesPlannerForFewerTargetChapters(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30})
	brief := "arc restructure"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "arc restructure",
		"chapters": [
			{
				"chapter": 1,
				"title": "Merged opening",
				"core_event": "Ari combines the first two source turns.",
				"hook": "A shared clue reframes both turns.",
				"scenes": ["station", "archive"],
				"source_chapters": [1, 2],
				"source_range": {"from": 1, "to": 2},
				"word_budget": {"source_words": 30, "target_words": 35, "min_words": 30, "max_words": 40, "tolerance": 0.15}
			},
			{
				"chapter": 2,
				"title": "New turn",
				"core_event": "Ari pays off the third source turn.",
				"hook": "The choice opens the next door.",
				"scenes": ["roof"],
				"source_chapters": [3],
				"source_range": {"from": 3, "to": 3},
				"word_budget": {"source_runes": 30, "target_runes": 32, "min_runes": 28, "max_runes": 38, "tolerance": 0.15}
			}
		]
	}`}}}

	proposal, err := BuildAdaptationProposal(Deps{
		Store: st,
		LLM:   llm,
		Prompts: Prompts{
			Planner: "planner system prompt",
		},
	}, ProposalOptions{
		Brief:       brief,
		Granularity: domain.AdaptationGranularityArc,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 1 {
		t.Fatalf("planner calls=%d, want 1", llm.calls)
	}
	if len(llm.got) != 1 || !strings.Contains(llm.got[0][0].TextContent(), "planner system prompt") {
		t.Fatalf("planner prompt not sent: %+v", llm.got)
	}
	plannerInput := llm.got[0][1].TextContent()
	if !strings.Contains(plannerInput, `"source_foundation"`) || !strings.Contains(plannerInput, `"source_reports"`) {
		t.Fatalf("planner input should include source foundation and reports: %s", plannerInput)
	}
	if proposal.Status != domain.AdaptationPlanStatusProposal || proposal.RewritePolicy != domain.AdaptationRewriteFullRewrite {
		t.Fatalf("proposal mode fields mismatch: %+v", proposal)
	}
	if len(proposal.Chapters) != 2 {
		t.Fatalf("chapters=%d, want planner-provided 2", len(proposal.Chapters))
	}
	if got := proposal.Chapters[0].SourceChapters; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("merged source anchors not preserved: %+v", got)
	}
	if proposal.TargetTotalRunes != 67 {
		t.Fatalf("target total=%d, want summed planner budget 67", proposal.TargetTotalRunes)
	}
	if st.Adaptation.Active() {
		t.Fatal("proposal should not activate adaptation project")
	}
	savedPlan, err := st.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if savedPlan != nil {
		t.Fatalf("BuildAdaptationProposal should not save confirmed plan: %+v", savedPlan)
	}
}

func TestBuildAdaptationProposalFreeUsesPlannerForMoreTargetChapters(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20})
	brief := "free restructure"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "free restructure",
		"planner": {"prompt": "adaptation-planner", "prompt_version": "v1", "model": "fake"},
		"chapters": [
			{
				"chapter": 1,
				"title": "Opening focus",
				"core_event": "Ari reframes the first source turn.",
				"hook": "The clue points inward.",
				"scenes": ["station"],
				"source_chapters": [1],
				"word_budget": {"source_runes": 10, "target_runes": 12, "min_runes": 10, "max_runes": 15, "tolerance": 0.15}
			},
			{
				"chapter": 2,
				"title": "Inserted bridge",
				"core_event": "Ari makes the missing emotional choice visible.",
				"hook": "The bridge forces a confession.",
				"scenes": ["alley"],
				"source_chapters": [1],
				"is_added": true,
				"word_budget": {"source_runes": 0, "target_runes": 10, "min_runes": 8, "max_runes": 12, "tolerance": 0.15}
			},
			{
				"chapter": 3,
				"title": "Second turn",
				"core_event": "Ari resolves the second source turn.",
				"hook": "The cost is named.",
				"scenes": ["archive"],
				"source_chapters": [2],
				"word_budget": {"source_runes": 20, "target_runes": 22, "min_runes": 18, "max_runes": 25, "tolerance": 0.15}
			}
		]
	}`}}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       brief,
		Granularity: domain.AdaptationGranularityFree,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if len(proposal.Chapters) != 3 {
		t.Fatalf("chapters=%d, want planner-provided 3", len(proposal.Chapters))
	}
	if !proposal.Chapters[1].IsAdded || len(proposal.Chapters[1].SourceChapters) != 1 || proposal.Chapters[1].SourceChapters[0] != 1 {
		t.Fatalf("added chapter should keep source anchor: %+v", proposal.Chapters[1])
	}
	if proposal.Chapters[1].SourceRange.From != 1 || proposal.Chapters[1].SourceRange.To != 1 {
		t.Fatalf("added chapter source range should be derived from anchor: %+v", proposal.Chapters[1].SourceRange)
	}
	if proposal.Planner == nil || proposal.Planner.Prompt != "adaptation-planner" || proposal.Planner.Model != "fake" {
		t.Fatalf("planner metadata not preserved: %+v", proposal.Planner)
	}
}

func TestBuildAdaptationProposalFillsMissingPlannerConstants(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20})
	brief := "arc restructure"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"brief": "model-side summary",
		"chapters": [
			{
				"chapter": 1,
				"title": "Merged opening",
				"core_event": "Ari combines both source turns.",
				"hook": "A shared clue reframes both turns.",
				"scenes": ["station", "archive"],
				"source_chapters": [1, 2],
				"source_range": {"from": 1, "to": 2},
				"word_budget": {"source_runes": 30, "target_runes": 35, "min_runes": 30, "max_runes": 40, "tolerance": 0.15}
			}
		]
	}`}}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       brief,
		Granularity: domain.AdaptationGranularityArc,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if proposal.Granularity != domain.AdaptationGranularityArc ||
		proposal.Status != domain.AdaptationPlanStatusProposal ||
		proposal.RewritePolicy != domain.AdaptationRewriteFullRewrite ||
		proposal.Brief != brief {
		t.Fatalf("proposal constants were not restored: %+v", proposal)
	}
}

func TestBuildAdaptationProposalFallsBackWhenPlannerHasNoChapters(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20})
	brief := "free restructure"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"generated_at": "2026-07-02T00:00:00Z",
		"model": "fake",
		"notes": "metadata only",
		"prompt": "adaptation-planner",
		"prompt_version": "v1"
	}`}}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       brief,
		Granularity: domain.AdaptationGranularityFree,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 1 {
		t.Fatalf("planner calls=%d, want 1", llm.calls)
	}
	if proposal.Status != domain.AdaptationPlanStatusProposal ||
		proposal.Granularity != domain.AdaptationGranularityFree ||
		proposal.RewritePolicy != domain.AdaptationRewriteFullRewrite ||
		proposal.Brief != brief {
		t.Fatalf("fallback proposal constants mismatch: %+v", proposal)
	}
	if len(proposal.Chapters) != 2 {
		t.Fatalf("fallback chapters=%d, want source chapter count", len(proposal.Chapters))
	}
	if proposal.Chapters[0].WordBudget == nil || proposal.Chapters[0].WordBudget.TargetRunes <= 0 {
		t.Fatalf("fallback chapter should include word budget: %+v", proposal.Chapters[0])
	}
	if proposal.Planner == nil || !strings.Contains(strings.Join([]string(proposal.Planner.Notes), "\n"), "deterministic proposal") {
		t.Fatalf("fallback planner notes missing: %+v", proposal.Planner)
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved == nil || len(saved.Chapters) != 2 {
		t.Fatalf("fallback proposal not saved: %+v", saved)
	}
}

func TestParsePlannerProposalAcceptsNestedChapterAliases(t *testing.T) {
	proposal, err := parsePlannerProposal(`{
		"adaptation_proposal": {
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free rewrite",
			"planner": {"notes": "singleton note"},
			"chapter_plans": [
				{
					"chapter": 1,
					"title": "Opening",
					"core_event": "A new opening reframes the source.",
					"hook": "The last image unsettles the lead.",
					"scenes": ["room", "street"],
					"source_chapters": [1],
					"source_range": {"from": 1, "to": 1},
					"word_budget": {"source_runes": 10, "target_runes": 20, "min_runes": 10, "max_runes": 30}
				}
			]
		}
	}`)
	if err != nil {
		t.Fatalf("parsePlannerProposal: %v", err)
	}
	if proposal.Granularity != domain.AdaptationGranularityFree || len(proposal.Chapters) != 1 {
		t.Fatalf("nested proposal not decoded: %+v", proposal)
	}
	if proposal.Chapters[0].Title != "Opening" || proposal.Chapters[0].CoreEvent == "" {
		t.Fatalf("chapter alias not decoded: %+v", proposal.Chapters[0])
	}
	if proposal.Planner == nil || len(proposal.Planner.Notes) != 1 || proposal.Planner.Notes[0] != "singleton note" {
		t.Fatalf("planner string note not decoded: %+v", proposal.Planner)
	}
}

func TestParsePlannerProposalAcceptsTargetChapterPlanAlias(t *testing.T) {
	proposal, err := parsePlannerProposal(`{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "free rewrite",
		"targetChapterPlans": [
			{
				"chapter": 1,
				"title": "Opening",
				"core_event": "A new opening reframes the source.",
				"hook": "The last image unsettles the lead.",
				"scenes": ["room", "street"],
				"source_chapters": [1],
				"source_range": {"from": 1, "to": 1},
				"word_budget": {"source_runes": 10, "target_runes": 20, "min_runes": 10, "max_runes": 30}
			}
		]
	}`)
	if err != nil {
		t.Fatalf("parsePlannerProposal: %v", err)
	}
	if len(proposal.Chapters) != 1 || proposal.Chapters[0].Title != "Opening" {
		t.Fatalf("targetChapterPlans alias not decoded: %+v", proposal.Chapters)
	}
}

func TestParsePlannerProposalSkipsLeadingMetadataObject(t *testing.T) {
	proposal, err := parsePlannerProposal(`{"prompt":"adaptation-planner","prompt_version":"v1","model":"fake","notes":"metadata only"}
	{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "free rewrite",
		"chapters": [
			{
				"chapter": 1,
				"title": "Opening",
				"core_event": "A new opening reframes the source.",
				"hook": "The last image unsettles the lead.",
				"scenes": ["room", "street"],
				"source_chapters": [1],
				"source_range": {"from": 1, "to": 1},
				"word_budget": {"source_runes": 10, "target_runes": 20, "min_runes": 10, "max_runes": 30}
			}
		]
	}`)
	if err != nil {
		t.Fatalf("parsePlannerProposal: %v", err)
	}
	if len(proposal.Chapters) != 1 || proposal.Chapters[0].Title != "Opening" {
		t.Fatalf("leading metadata object should be skipped: %+v", proposal.Chapters)
	}
}

func TestBuildAdaptationProposalRejectsInvalidPlannerOutput(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20})
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"granularity": "arc",
		"status": "confirmed",
		"rewrite_policy": "full_rewrite",
		"brief": "arc restructure",
		"chapters": [
			{
				"chapter": 1,
				"title": "Invalid",
				"core_event": "Ari moves.",
				"hook": "A bad status is rejected.",
				"scenes": ["station"],
				"source_chapters": [1, 2],
				"word_budget": {"source_runes": 30, "target_runes": 30, "min_runes": 25, "max_runes": 35, "tolerance": 0.15}
			}
		]
	}`}}}

	if _, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       "arc restructure",
		Granularity: domain.AdaptationGranularityArc,
	}); err == nil {
		t.Fatal("BuildAdaptationProposal should reject invalid planner output")
	}
	proposal, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if proposal != nil {
		t.Fatalf("invalid planner output should not save proposal: %+v", proposal)
	}
}

func TestBuildAdaptationProposalRejectsInvalidPlannerWordBudgets(t *testing.T) {
	cases := []struct {
		name          string
		planFields    string
		chapterFields string
		wordBudget    string
		wantErr       string
	}{
		{
			name:       "proposal target total conflicts with chapter budgets",
			planFields: `"target_total_runes": 31`,
			wordBudget: `"source_runes": 30, "target_runes": 30, "min_runes": 25, "max_runes": 35, "tolerance": 0.15`,
			wantErr:    "target_total_runes",
		},
		{
			name:          "legacy chapter target conflicts with nested budget",
			chapterFields: `"target_runes": 31`,
			wordBudget:    `"source_runes": 30, "target_runes": 30, "min_runes": 25, "max_runes": 35, "tolerance": 0.15`,
			wantErr:       "target_runes",
		},
		{
			name:          "legacy chapter target min conflicts with nested budget",
			chapterFields: `"target_min_runes": 24`,
			wordBudget:    `"source_runes": 30, "target_runes": 30, "min_runes": 25, "max_runes": 35, "tolerance": 0.15`,
			wantErr:       "target_min_runes",
		},
		{
			name:          "legacy chapter target max conflicts with nested budget",
			chapterFields: `"target_max_runes": 36`,
			wordBudget:    `"source_runes": 30, "target_runes": 30, "min_runes": 25, "max_runes": 35, "tolerance": 0.15`,
			wantErr:       "target_max_runes",
		},
		{
			name:       "nested target falls outside nested min max",
			wordBudget: `"source_runes": 30, "target_runes": 40, "min_runes": 25, "max_runes": 35, "tolerance": 0.15`,
			wantErr:    "within min_runes..max_runes",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := store.NewStore(t.TempDir())
			if err := st.Init(); err != nil {
				t.Fatalf("Init: %v", err)
			}
			seedPreparedAdaptationSource(t, st, []int{10, 20})
			llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: plannerBudgetProposalJSON(tc.planFields, tc.chapterFields, tc.wordBudget)}}}

			if _, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
				Brief:       "arc restructure",
				Granularity: domain.AdaptationGranularityArc,
			}); err == nil {
				t.Fatal("BuildAdaptationProposal should reject invalid planner word budget")
			} else if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error=%q, want substring %q", err.Error(), tc.wantErr)
			}
			proposal, err := st.Adaptation.LoadProposal()
			if err != nil {
				t.Fatalf("LoadProposal: %v", err)
			}
			if proposal != nil {
				t.Fatalf("invalid planner word budget should not save proposal: %+v", proposal)
			}
		})
	}
}

func plannerBudgetProposalJSON(planFields string, chapterFields string, wordBudget string) string {
	if strings.TrimSpace(planFields) != "" {
		planFields = strings.TrimSpace(planFields) + ","
	}
	if strings.TrimSpace(chapterFields) != "" {
		chapterFields = strings.TrimSpace(chapterFields) + ","
	}
	return fmt.Sprintf(`{
		%s
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "arc restructure",
		"chapters": [
			{
				%s
				"chapter": 1,
				"title": "Budgeted",
				"core_event": "Ari merges both source turns.",
				"hook": "The budget contradiction must be caught.",
				"scenes": ["station"],
				"source_chapters": [1, 2],
				"source_range": {"from": 1, "to": 2},
				"word_budget": {%s}
			}
		]
	}`, planFields, chapterFields, wordBudget)
}

func seedPreparedAdaptationSource(t *testing.T, st *store.Store, runeCounts []int) []domain.AdaptationSourceReport {
	t.Helper()
	sources := make([]domain.AdaptationSource, 0, len(runeCounts))
	reports := make([]domain.AdaptationSourceReport, 0, len(runeCounts))
	for i, runeCount := range runeCounts {
		chapter := i + 1
		title := "Source"
		body := strings.Repeat("a", runeCount)
		source, err := st.Adaptation.SaveSourceChapter(chapter, title, body)
		if err != nil {
			t.Fatalf("SaveSourceChapter %d: %v", chapter, err)
		}
		sources = append(sources, source)
		report := domain.AdaptationSourceReport{
			Chapter:      chapter,
			Title:        title,
			SourceSHA256: source.SHA256,
			Summary:      "source summary",
			KeyEvents:    []string{"source event"},
		}
		if err := st.Adaptation.SaveSourceReport(report); err != nil {
			t.Fatalf("SaveSourceReport %d: %v", chapter, err)
		}
		reports = append(reports, report)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: len(sources),
		Chapters:     sources,
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	if err := st.Adaptation.SaveSourceFoundation(testSourceFoundation()); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	if err := st.Adaptation.SaveSourceReports(reports); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}
	return reports
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
