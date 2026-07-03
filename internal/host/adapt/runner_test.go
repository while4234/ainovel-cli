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

func TestBuildAdaptationProposalSinglePlannerPromptHasExplicitJSONContract(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20})
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "arc restructure",
		"chapters": [
			{
				"chapter": 1,
				"title": "Opening",
				"core_event": "Ari adapts the source.",
				"hook": "The clue points onward.",
				"scenes": ["archive"],
				"source_chapters": [1, 2],
				"source_range": {"from": 1, "to": 2},
				"word_budget": {"source_runes": 30, "target_runes": 32, "min_runes": 30, "max_runes": 34, "tolerance": 0.15},
				"preserve_events": ["source event"],
				"required_changes": ["adapt the beat"],
				"forbidden_moves": ["drop the source anchor"]
			}
		]
	}`}}}

	if _, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       "arc restructure",
		Granularity: domain.AdaptationGranularityArc,
	}); err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	prompt := llm.got[0][1].TextContent()
	if !strings.Contains(prompt, "top-level object must contain a chapters array") ||
		!strings.Contains(prompt, "Required shape") ||
		!strings.Contains(prompt, `Invalid shapes: {"chapter":1`) ||
		!strings.Contains(prompt, "Every chapter field must be an integer") {
		t.Fatalf("single planner prompt should contain explicit JSON contract: %s", prompt)
	}
}

func TestBuildAdaptationProposalFreeUsesChunkedPlannerForLongTargetChapters(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	brief := "free restructure into 20章"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free restructure into 20章",
			"target_chapter_count": 20,
			"mainline_rules": ["keep every source turn anchored"],
			"relationship_goals": ["slow emotional escalation"],
			"batches": [
				{"index": 1, "title": "Opening volume", "theme": "orientation", "target_from": 1, "target_to": 8, "source_from": 1, "source_to": 2, "summary": "establish the new premise"},
				{"index": 2, "title": "Pressure volume", "theme": "choice", "target_from": 9, "target_to": 16, "source_from": 2, "source_to": 3, "summary": "expand the central conflict"},
				{"index": 3, "title": "Resolution volume", "theme": "payoff", "target_from": 17, "target_to": 20, "source_from": 3, "source_to": 4, "summary": "resolve the adapted ending"}
			]
		}`},
		{text: plannerBatchProposalJSON(1, 8, 1, 2)},
		{text: plannerBatchProposalJSON(9, 16, 2, 3)},
		{text: plannerBatchProposalJSON(17, 20, 3, 4)},
	}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 4 {
		t.Fatalf("planner calls=%d, want skeleton + 3 batch calls", llm.calls)
	}
	if len(proposal.Chapters) != 20 {
		t.Fatalf("chapters=%d, want 20", len(proposal.Chapters))
	}
	if proposal.Chapters[8].Chapter != 9 || proposal.Chapters[19].Chapter != 20 {
		t.Fatalf("batch chapters should keep absolute numbering: %+v", proposal.Chapters)
	}
	if proposal.Planner == nil || proposal.Planner.PromptVersion != "v1-chunked" {
		t.Fatalf("planner metadata should mark chunked run: %+v", proposal.Planner)
	}
	firstPrompt := llm.got[0][1].TextContent()
	if !strings.Contains(firstPrompt, `"target_chapter_hint": 20`) ||
		!strings.Contains(firstPrompt, "do not mechanically split chapters") ||
		!strings.Contains(firstPrompt, "top-level object must contain a batches array") ||
		!strings.Contains(firstPrompt, "Do not return chapter details") {
		t.Fatalf("skeleton prompt should carry long-form target and model-planned split instruction: %s", firstPrompt)
	}
	secondBatchPrompt := llm.got[2][1].TextContent()
	if !strings.Contains(secondBatchPrompt, `"target_from": 9`) ||
		!strings.Contains(secondBatchPrompt, `"target_to": 16`) ||
		!strings.Contains(secondBatchPrompt, `top-level object must be {"chapters":[...]}`) ||
		!strings.Contains(secondBatchPrompt, "Return exactly 8 chapter objects") ||
		!strings.Contains(secondBatchPrompt, `Invalid shapes: {"chapter":9`) {
		t.Fatalf("batch prompt should use skeleton-provided range and explicit JSON contract: %s", secondBatchPrompt)
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved == nil || len(saved.Chapters) != 20 {
		t.Fatalf("chunked proposal should be saved as proposal only: %+v", saved)
	}
	if st.Adaptation.Active() {
		t.Fatal("proposal should not activate adaptation project")
	}
}

func TestBuildAdaptationProposalRepairsChunkedSkeletonWithoutBatches(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	brief := "free restructure into 20 chapters"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"overall_arc": "model returned a high level arc but no machine usable batches",
			"key_turns": ["call", "choice", "return"],
			"pair": {"lead": "Ari", "partner": "Bea"}
		}`},
		{text: `{
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free restructure into 20 chapters",
			"target_chapter_count": 20,
			"batches": [
				{"index": 1, "title": "Opening volume", "theme": "orientation", "target_from": 1, "target_to": 8, "source_from": 1, "source_to": 2, "summary": "establish the new premise"},
				{"index": 2, "title": "Pressure volume", "theme": "choice", "target_from": 9, "target_to": 16, "source_from": 2, "source_to": 3, "summary": "expand the central conflict"},
				{"index": 3, "title": "Resolution volume", "theme": "payoff", "target_from": 17, "target_to": 20, "source_from": 3, "source_to": 4, "summary": "resolve the adapted ending"}
			]
		}`},
		{text: plannerBatchProposalJSON(1, 8, 1, 2)},
		{text: plannerBatchProposalJSON(9, 16, 2, 3)},
		{text: plannerBatchProposalJSON(17, 20, 3, 4)},
	}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 5 {
		t.Fatalf("planner calls=%d, want skeleton + repair + 3 batch calls", llm.calls)
	}
	repairPrompt := llm.got[1][1].TextContent()
	if !strings.Contains(repairPrompt, "previous planner response could not be used") ||
		!strings.Contains(repairPrompt, "top-level batches array") ||
		!strings.Contains(repairPrompt, "overall_arc") {
		t.Fatalf("repair prompt should explain missing-batches schema failure: %s", repairPrompt)
	}
	if len(proposal.Chapters) != 20 {
		t.Fatalf("chapters=%d, want 20", len(proposal.Chapters))
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved == nil || len(saved.Chapters) != 20 {
		t.Fatalf("repaired chunked proposal should be saved: %+v", saved)
	}
}

func TestBuildAdaptationProposalRepairsChunkedBatchWithoutChapters(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	brief := "free restructure into 20 chapters"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free restructure into 20 chapters",
			"target_chapter_count": 20,
			"batches": [
				{"index": 1, "title": "Opening volume", "theme": "orientation", "target_from": 1, "target_to": 8, "source_from": 1, "source_to": 2, "summary": "establish the new premise"},
				{"index": 2, "title": "Pressure volume", "theme": "choice", "target_from": 9, "target_to": 16, "source_from": 2, "source_to": 3, "summary": "expand the central conflict"},
				{"index": 3, "title": "Resolution volume", "theme": "payoff", "target_from": 17, "target_to": 20, "source_from": 3, "source_to": 4, "summary": "resolve the adapted ending"}
			]
		}`},
		{text: `{"summary":"batch outline only","key_turns":["setup","decision"]}`},
		{text: `{
			"chapter": "第1章",
			"title": "Only one chapter",
			"core_event": "The model still returned one chapter object instead of a chapters array.",
			"hook": "The schema is still wrong.",
			"scenes": ["station"],
			"source_chapters": [1],
			"source_range": {"from": 1, "to": 1},
			"word_budget": {"source_runes": 10, "target_runes": 12, "min_runes": 10, "max_runes": 14, "tolerance": 0.15},
			"preserve_events": ["source event"],
			"required_changes": ["repair the shape"],
			"forbidden_moves": ["single chapter object"]
		}`},
		{text: plannerBatchProposalJSON(1, 8, 1, 2)},
		{text: plannerBatchProposalJSON(9, 16, 2, 3)},
		{text: plannerBatchProposalJSON(17, 20, 3, 4)},
	}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 6 {
		t.Fatalf("planner calls=%d, want skeleton + failed batch + two repairs + remaining batches", llm.calls)
	}
	repairPrompt := llm.got[2][1].TextContent()
	if !strings.Contains(repairPrompt, "shaped exactly like") ||
		!strings.Contains(repairPrompt, "chapters 1 through 8") {
		t.Fatalf("batch repair prompt should explain chapter schema failure: %s", repairPrompt)
	}
	secondRepairPrompt := llm.got[3][1].TextContent()
	if !strings.Contains(secondRepairPrompt, "chapter count=1, want 8") ||
		!strings.Contains(secondRepairPrompt, "Only one chapter") {
		t.Fatalf("second batch repair prompt should include single-object failure feedback: %s", secondRepairPrompt)
	}
	if len(proposal.Chapters) != 20 {
		t.Fatalf("chapters=%d, want 20", len(proposal.Chapters))
	}
}

func TestParsePlannerProposalCollectsLooseChapterObjects(t *testing.T) {
	proposal, err := parsePlannerProposal(`
		{"chapter":1,"title":"Loose One","coreEvent":"Ari finds the first clue.","hook":"The clue points onward.","scenes":["archive"],"sourceChapters":[1],"sourceRange":{"from":1,"to":1},"wordBudget":{"sourceRunes":10,"targetRunes":12,"minRunes":11,"maxRunes":13,"tolerance":0.15},"preserveEvents":["first source beat"],"requiredChanges":["adapt first beat"],"forbiddenMoves":["drop first anchor"]}
		{"chapter":2,"title":"Loose Two","coreEvent":"Ari chooses the second path.","hook":"The new route opens.","scenes":["station"],"sourceChapters":[2],"sourceRange":{"from":2,"to":2},"wordBudget":{"sourceRunes":20,"targetRunes":22,"minRunes":21,"maxRunes":23,"tolerance":0.15},"preserveEvents":["second source beat"],"requiredChanges":["adapt second beat"],"forbiddenMoves":["drop second anchor"]}
	`)
	if err != nil {
		t.Fatalf("parsePlannerProposal: %v", err)
	}
	if len(proposal.Chapters) != 2 || proposal.Chapters[0].Title != "Loose One" || proposal.Chapters[1].Title != "Loose Two" {
		t.Fatalf("loose chapter objects were not collected: %+v", proposal.Chapters)
	}
	if proposal.Chapters[0].CoreEvent == "" || proposal.Chapters[0].WordBudget == nil || len(proposal.Chapters[0].SourceChapters) != 1 {
		t.Fatalf("loose chapter aliases were not normalized: %+v", proposal.Chapters[0])
	}
}

func TestParsePlannerProposalCollectsSingleChapterObjectWithTextChapter(t *testing.T) {
	proposal, err := parsePlannerProposal(`{
		"chapter": "第1章",
		"title": "Text Chapter Number",
		"core_event": "Ari finds the first clue.",
		"hook": "The clue points onward.",
		"scenes": ["archive"],
		"source_chapters": [1],
		"source_range": {"from": 1, "to": 1},
		"word_budget": {"source_runes": 10, "target_runes": 12, "min_runes": 11, "max_runes": 13, "tolerance": 0.15},
		"preserve_events": ["first source beat"],
		"required_changes": ["adapt first beat"],
		"forbidden_moves": ["drop first anchor"]
	}`)
	if err != nil {
		t.Fatalf("parsePlannerProposal: %v", err)
	}
	if len(proposal.Chapters) != 1 || proposal.Chapters[0].Chapter != 1 || proposal.Chapters[0].Title != "Text Chapter Number" {
		t.Fatalf("text chapter object was not normalized: %+v", proposal.Chapters)
	}
}

func TestBuildAdaptationProposalRejectsChunkedSkeletonThatShrinksLongTarget(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	brief := "free restructure into 50-60章"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "free restructure into 50-60章",
		"target_chapter_count": 17,
		"batches": [
			{"index": 1, "target_from": 1, "target_to": 17, "source_from": 1, "source_to": 4, "summary": "too short"}
		]
	}`}}}

	_, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 60,
	})
	if err == nil {
		t.Fatal("BuildAdaptationProposal should reject a skeleton that ignores the long target")
	}
	if !strings.Contains(err.Error(), "ignores requested long-form target") {
		t.Fatalf("error=%v, want long-form shrink rejection", err)
	}
	if llm.calls != 1 {
		t.Fatalf("planner calls=%d, want stop after skeleton", llm.calls)
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved != nil {
		t.Fatalf("rejected skeleton should not save proposal: %+v", saved)
	}
}

func TestInferTargetChapterCountFromBrief(t *testing.T) {
	cases := []struct {
		brief string
		want  int
	}{
		{brief: "plan 50-60章", want: 60},
		{brief: "plan 50 60章", want: 60},
		{brief: "规划20多章节", want: 25},
		{brief: "规划二十多章", want: 25},
		{brief: "规划五六十章", want: 60},
		{brief: "第15章补一个误会", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.brief, func(t *testing.T) {
			if got := inferTargetChapterCount(tc.brief); got != tc.want {
				t.Fatalf("inferTargetChapterCount(%q)=%d, want %d", tc.brief, got, tc.want)
			}
		})
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

func TestBuildAdaptationProposalRejectsPlannerWithNoChapters(t *testing.T) {
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

	_, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       brief,
		Granularity: domain.AdaptationGranularityFree,
	})
	if err == nil {
		t.Fatal("BuildAdaptationProposal should reject planner output with no chapters")
	}
	if !strings.Contains(err.Error(), "planner proposal has no chapters") {
		t.Fatalf("error = %v, want no-chapters planner error", err)
	}
	if llm.calls != 1 {
		t.Fatalf("planner calls=%d, want 1", llm.calls)
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved != nil {
		t.Fatalf("unusable planner output should not save proposal: %+v", saved)
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

func plannerBatchProposalJSON(from, to, sourceFrom, sourceTo int) string {
	count := to - from + 1
	sourceSpan := sourceTo - sourceFrom + 1
	chapters := make([]string, 0, count)
	for chapter := from; chapter <= to; chapter++ {
		sourceChapter := sourceFrom
		if count > 0 && sourceSpan > 0 {
			sourceChapter = sourceFrom + (chapter-from)*sourceSpan/count
		}
		sourceRunes := sourceChapter * 10
		targetRunes := sourceRunes + 2
		chapters = append(chapters, fmt.Sprintf(`{
			"chapter": %d,
			"title": "Target %d",
			"core_event": "Ari adapts source turn %d.",
			"hook": "A clear hook for target %d.",
			"scenes": ["station"],
			"source_chapters": [%d],
			"source_range": {"from": %d, "to": %d},
			"word_budget": {"source_runes": %d, "target_runes": %d, "min_runes": %d, "max_runes": %d, "tolerance": 0.15},
			"preserve_events": ["source event"],
			"required_changes": ["adapt the beat"],
			"forbidden_moves": ["drop the source anchor"]
		}`, chapter, chapter, sourceChapter, chapter, sourceChapter, sourceChapter, sourceChapter, sourceRunes, targetRunes, targetRunes-1, targetRunes+1))
	}
	return fmt.Sprintf(`{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "chunk",
		"chapters": [%s]
	}`, strings.Join(chapters, ","))
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
