package adapt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/litellm"
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

func TestBuildAdaptationProposalFreeDefaultsLongSourceToChunkedPlanner(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	runeCounts := make([]int, 17)
	for i := range runeCounts {
		runeCounts[i] = 10 + i
	}
	seedPreparedAdaptationSource(t, st, runeCounts)
	brief := "free long-form expansion without an explicit chapter count"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free long-form expansion without an explicit chapter count",
			"target_chapter_count": 18,
			"batches": [
				{"index": 1, "title": "Opening volume", "theme": "orientation", "target_from": 1, "target_to": 8, "source_from": 1, "source_to": 8, "summary": "establish the new premise"},
				{"index": 2, "title": "Pressure volume", "theme": "choice", "target_from": 9, "target_to": 16, "source_from": 9, "source_to": 16, "summary": "expand the middle"},
				{"index": 3, "title": "Resolution volume", "theme": "payoff", "target_from": 17, "target_to": 18, "source_from": 17, "source_to": 17, "summary": "resolve the ending"}
			]
		}`},
		{text: plannerBatchProposalJSON(1, 8, 1, 8)},
		{text: plannerBatchProposalJSON(9, 16, 9, 16)},
		{text: plannerBatchProposalJSON(17, 18, 17, 17)},
	}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       brief,
		Granularity: domain.AdaptationGranularityFree,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 4 {
		t.Fatalf("planner calls=%d, want default skeleton + 3 batch calls", llm.calls)
	}
	if len(proposal.Chapters) != 18 {
		t.Fatalf("chapters=%d, want 18", len(proposal.Chapters))
	}
	if proposal.Planner == nil || proposal.Planner.PromptVersion != "v1-chunked" {
		t.Fatalf("planner metadata should mark chunked run: %+v", proposal.Planner)
	}
	firstPrompt := llm.got[0][1].TextContent()
	if !strings.Contains(firstPrompt, `"target_chapter_hint": 18`) {
		t.Fatalf("skeleton prompt should carry default target chapter hint: %s", firstPrompt)
	}
}

func TestBuildAdaptationProposalCoversSparseSourceAnchorsByExplicitRange(t *testing.T) {
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
				"core_event": "Ari merges the first two source turns.",
				"hook": "The merged pressure points forward.",
				"scenes": ["station"],
				"source_chapters": [1],
				"source_range": {"from": 1, "to": 2},
				"preserve_events": ["source event"],
				"required_changes": ["merge the first span"],
				"forbidden_moves": ["drop the second source chapter"]
			},
			{
				"chapter": 2,
				"title": "Closing turn",
				"core_event": "Ari resolves the final source turn.",
				"hook": "The ending opens a new question.",
				"scenes": ["archive"],
				"source_chapters": [3],
				"source_range": {"from": 3, "to": 3},
				"preserve_events": ["source event"],
				"required_changes": ["adapt the final span"],
				"forbidden_moves": ["drop the final source chapter"]
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
	if llm.calls != 1 {
		t.Fatalf("planner calls=%d, want single planner call", llm.calls)
	}
	if proposal.Chapters[0].WordBudget == nil || proposal.Chapters[0].WordBudget.SourceRunes != 30 {
		t.Fatalf("explicit source_range should drive covered source runes: %+v", proposal.Chapters[0])
	}
	if got := proposal.Chapters[0].SourceChapters; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("explicit source_range should expand saved source_chapters for later tools: %+v", proposal.Chapters[0])
	}
}

func TestBuildAdaptationProposalClearsChunkedRuntimeAfterFinalValidationFailure(t *testing.T) {
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
		{text: plannerBatchProposalJSON(1, 8, 1, 1)},
		{text: plannerBatchProposalJSON(9, 16, 2, 2)},
		{text: plannerBatchProposalJSON(17, 20, 3, 3)},
	}}

	_, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err == nil {
		t.Fatal("BuildAdaptationProposal should fail when final coverage omits a source chapter")
	}
	if !strings.Contains(err.Error(), "planner proposal does not cover source chapter 4") {
		t.Fatalf("error=%v, want missing source chapter 4", err)
	}
	if runtime, runtimeErr := st.Adaptation.LoadProposalRuntime(); runtimeErr != nil || runtime != nil {
		t.Fatalf("runtime should be cleared after final validation failure: runtime=%+v err=%v", runtime, runtimeErr)
	}
	if saved, savedErr := st.Adaptation.LoadProposal(); savedErr != nil || saved != nil {
		t.Fatalf("invalid proposal should not be saved: proposal=%+v err=%v", saved, savedErr)
	}
}

func TestBuildAdaptationProposalResumesChunkedPlannerRuntimeAfterBatchFailure(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	brief := "free restructure into 20 chapters"
	first := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free restructure into 20 chapters",
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
		{err: context.Canceled},
	}}

	_, err := BuildAdaptationProposal(Deps{Store: st, LLM: first}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err == nil {
		t.Fatal("first interrupted proposal build should fail")
	}
	if first.calls != 3 {
		t.Fatalf("first planner calls=%d, want skeleton + first batch + failed second batch", first.calls)
	}
	runtime, err := st.Adaptation.LoadProposalRuntime()
	if err != nil {
		t.Fatalf("LoadProposalRuntime: %v", err)
	}
	if runtime == nil || runtime.Skeleton == nil || len(runtime.CompletedBatches) != 1 {
		t.Fatalf("runtime should keep skeleton and first completed batch: %+v", runtime)
	}
	if runtime.CompletedBatches[0].TargetFrom != 1 || runtime.CompletedBatches[0].TargetTo != 8 {
		t.Fatalf("completed runtime batch = %+v", runtime.CompletedBatches[0])
	}
	if saved, err := st.Adaptation.LoadProposal(); err != nil || saved != nil {
		t.Fatalf("proposal should not be saved after interrupted run: proposal=%+v err=%v", saved, err)
	}

	second := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: plannerBatchProposalJSON(9, 16, 2, 3)},
		{text: plannerBatchProposalJSON(17, 20, 3, 4)},
	}}
	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: second}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal resume: %v", err)
	}
	if second.calls != 2 {
		t.Fatalf("resume planner calls=%d, want only remaining two detail batches", second.calls)
	}
	if len(proposal.Chapters) != 20 || proposal.Chapters[0].Chapter != 1 || proposal.Chapters[19].Chapter != 20 {
		t.Fatalf("resumed proposal chapters = %+v", proposal.Chapters)
	}
	firstResumePrompt := second.got[0][1].TextContent()
	if !strings.Contains(firstResumePrompt, `"target_from": 9`) || strings.Contains(firstResumePrompt, "Do not return chapter details") {
		t.Fatalf("resume should skip skeleton and first batch, prompt=%s", firstResumePrompt)
	}
	if runtime, err := st.Adaptation.LoadProposalRuntime(); err != nil || runtime != nil {
		t.Fatalf("runtime should be cleared after successful proposal save: runtime=%+v err=%v", runtime, err)
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

func TestBuildAdaptationProposalSplitsOversizedSkeletonBatchForDetails(t *testing.T) {
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
				{"index": 1, "title": "One broad volume", "theme": "pressure", "target_from": 1, "target_to": 20, "source_from": 1, "source_to": 4, "summary": "model chose a broad long-form segment"}
			]
		}`},
		{text: plannerBatchProposalJSON(1, 8, 1, 4)},
		{text: plannerBatchProposalJSON(9, 16, 1, 4)},
		{text: plannerBatchProposalJSON(17, 20, 1, 4)},
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
		t.Fatalf("planner calls=%d, want skeleton + 3 detail calls", llm.calls)
	}
	if len(proposal.Chapters) != 20 {
		t.Fatalf("chapters=%d, want 20", len(proposal.Chapters))
	}
	if len(proposal.Volumes) != 1 || proposal.Volumes[0].TargetFrom != 1 || proposal.Volumes[0].TargetTo != 20 {
		t.Fatalf("model-planned volume should remain intact: %+v", proposal.Volumes)
	}
	firstDetailPrompt := llm.got[1][1].TextContent()
	if !strings.Contains(firstDetailPrompt, "Return exactly 8 chapter objects") ||
		!strings.Contains(firstDetailPrompt, `"target_from": 1`) ||
		!strings.Contains(firstDetailPrompt, `"target_to": 8`) {
		t.Fatalf("first detail prompt should request only the first 8 chapters: %s", firstDetailPrompt)
	}
	secondDetailPrompt := llm.got[2][1].TextContent()
	if !strings.Contains(secondDetailPrompt, "Return exactly 8 chapter objects") ||
		!strings.Contains(secondDetailPrompt, `"target_from": 9`) ||
		!strings.Contains(secondDetailPrompt, `"target_to": 16`) {
		t.Fatalf("second detail prompt should request the second 8 chapters: %s", secondDetailPrompt)
	}
	thirdDetailPrompt := llm.got[3][1].TextContent()
	if !strings.Contains(thirdDetailPrompt, "Return exactly 4 chapter objects") ||
		!strings.Contains(thirdDetailPrompt, `"target_from": 17`) ||
		!strings.Contains(thirdDetailPrompt, `"target_to": 20`) {
		t.Fatalf("third detail prompt should request remaining 4 chapters: %s", thirdDetailPrompt)
	}
}

func TestBuildAdaptationProposalFillsMissingChunkedBatchChapter(t *testing.T) {
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
		{text: plannerBatchProposalJSON(1, 7, 1, 2)},
		{text: plannerBatchProposalJSON(8, 8, 2, 2)},
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
		t.Fatalf("planner calls=%d, want skeleton + partial batch + missing fill + 2 remaining batches", llm.calls)
	}
	if len(proposal.Chapters) != 20 || proposal.Chapters[7].Chapter != 8 {
		t.Fatalf("missing chapter should be merged into proposal: %+v", proposal.Chapters)
	}
	missingPrompt := llm.got[2][1].TextContent()
	if !strings.Contains(missingPrompt, `"missing_chapters"`) ||
		!strings.Contains(missingPrompt, "8") ||
		!strings.Contains(missingPrompt, "existing_chapters") ||
		!strings.Contains(missingPrompt, "Target 7") ||
		!strings.Contains(missingPrompt, "Return only the chapters listed in missing_chapters") {
		t.Fatalf("missing repair prompt should carry accepted chapters and only request chapter 8: %s", missingPrompt)
	}
}

func TestBuildAdaptationProposalFillsMissingChapterWordBudget(t *testing.T) {
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
		{text: plannerBatchProposalJSON(1, 8, 1, 2)},
		{text: plannerBatchProposalJSONWithoutWordBudget(9, 16, 2, 3, 15)},
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
		t.Fatalf("planner calls=%d, want skeleton + 3 detail calls without repair", llm.calls)
	}
	chapter := proposal.Chapters[14]
	if chapter.Chapter != 15 || chapter.WordBudget == nil || chapter.WordBudget.TargetRunes <= 0 {
		t.Fatalf("chapter 15 word budget should be filled locally: %+v", chapter)
	}
	if chapter.TargetRunes != chapter.WordBudget.TargetRunes ||
		chapter.TargetMinRunes != chapter.WordBudget.MinRunes ||
		chapter.TargetMaxRunes != chapter.WordBudget.MaxRunes {
		t.Fatalf("legacy budget fields should mirror filled word_budget: %+v", chapter)
	}
}

func TestBuildAdaptationProposalRetriesTransientPlannerGenerateError(t *testing.T) {
	restore := stubPlannerRetrySleep(t)
	defer restore()
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{err: litellm.NewHTTPError("deepseek", 502, "<html><body>502 Bad Gateway</body></html>")},
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
	var progress []Event

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              "free restructure into 20 chapters",
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
		EmitProgress:       captureAdaptProgress(&progress),
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 5 {
		t.Fatalf("planner calls=%d, want failed skeleton attempt + retry + 3 detail calls", llm.calls)
	}
	if len(proposal.Chapters) != 20 {
		t.Fatalf("chapters=%d, want 20", len(proposal.Chapters))
	}
	if !hasAdaptProgress(progress, "重试 2/7") || !hasAdaptProgress(progress, "provider gateway error: 502 Bad Gateway") {
		t.Fatalf("progress should expose retry count and model error: %+v", progress)
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
	missingRepairPrompt := llm.got[3][1].TextContent()
	if !strings.Contains(missingRepairPrompt, "missing_chapters") ||
		!strings.Contains(missingRepairPrompt, "existing_chapters") ||
		!strings.Contains(missingRepairPrompt, "Only one chapter") {
		t.Fatalf("missing-chapter repair prompt should include existing partial context: %s", missingRepairPrompt)
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

func TestReviseAdaptationProposalSupportsChapterRangeAndVolumeTargets(t *testing.T) {
	cases := []struct {
		name          string
		options       ProposalRevisionOptions
		responses     []adaptLLMResponse
		wantCalls     int
		wantRevised   []int
		wantUnchanged []int
	}{
		{
			name: "single chapter",
			options: ProposalRevisionOptions{
				FromChapter: 3,
				Instruction: "raise the chapter three hook",
			},
			responses:     []adaptLLMResponse{{text: plannerRevisionProposalJSON(3, 3, 1, 1)}},
			wantCalls:     1,
			wantRevised:   []int{3},
			wantUnchanged: []int{2, 4},
		},
		{
			name: "reversed continuous range",
			options: ProposalRevisionOptions{
				FromChapter: 8,
				ToChapter:   5,
				Instruction: "smooth the middle four chapters",
			},
			responses:     []adaptLLMResponse{{text: plannerRevisionProposalJSON(5, 8, 2, 3)}},
			wantCalls:     1,
			wantRevised:   []int{5, 6, 7, 8},
			wantUnchanged: []int{4, 9},
		},
		{
			name: "specific volume",
			options: ProposalRevisionOptions{
				VolumeIndex: 2,
				Instruction: "make the second volume flow better",
			},
			responses: []adaptLLMResponse{
				{text: plannerVolumeRevisionSkeletonJSON(2, 5, 8, 2, 3)},
				{text: plannerRevisionProposalJSON(5, 8, 2, 3)},
			},
			wantCalls:     2,
			wantRevised:   []int{5, 6, 7, 8},
			wantUnchanged: []int{4, 9},
		},
		{
			name: "all volumes batches revision requests",
			options: ProposalRevisionOptions{
				VolumeIndex: -1,
				Instruction: "rebalance every volume",
			},
			responses: []adaptLLMResponse{
				{text: plannerRevisionProposalJSON(1, 8, 1, 3)},
				{text: plannerRevisionProposalJSON(9, 12, 3, 4)},
			},
			wantCalls:   2,
			wantRevised: []int{1, 8, 9, 12},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := store.NewStore(t.TempDir())
			if err := st.Init(); err != nil {
				t.Fatalf("Init: %v", err)
			}
			seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
			saveRevisionTestProposal(t, st)
			llm := &scriptedAdaptLLM{responses: tc.responses}

			updated, err := ReviseAdaptationProposal(context.Background(), Deps{Store: st, LLM: llm}, tc.options)
			if err != nil {
				t.Fatalf("ReviseAdaptationProposal: %v", err)
			}
			if llm.calls != tc.wantCalls {
				t.Fatalf("revision planner calls=%d, want %d", llm.calls, tc.wantCalls)
			}
			if len(updated.Chapters) != 12 {
				t.Fatalf("chapters=%d, want 12", len(updated.Chapters))
			}
			for _, chapter := range tc.wantRevised {
				got := updated.Chapters[chapter-1]
				if !strings.HasPrefix(got.Title, "Revised ") {
					t.Fatalf("chapter %d was not revised: %+v", chapter, got)
				}
			}
			for _, chapter := range tc.wantUnchanged {
				got := updated.Chapters[chapter-1]
				if !strings.HasPrefix(got.Title, "Original ") {
					t.Fatalf("chapter %d should be unchanged: %+v", chapter, got)
				}
			}
			if len(updated.Planner.Notes) == 0 || !strings.Contains(updated.Planner.Notes[len(updated.Planner.Notes)-1], tc.options.Instruction) {
				t.Fatalf("revision note missing instruction: %+v", updated.Planner)
			}
			saved, err := st.Adaptation.LoadProposal()
			if err != nil {
				t.Fatalf("LoadProposal: %v", err)
			}
			if saved == nil || saved.Chapters[tc.wantRevised[0]-1].Title != updated.Chapters[tc.wantRevised[0]-1].Title {
				t.Fatalf("revised proposal was not saved: saved=%+v updated=%+v", saved, updated)
			}
		})
	}
}

func TestReviseAdaptationProposalAllowsFinalVolumeEndingExpansion(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	saveRevisionTestProposal(t, st)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: plannerVolumeRevisionSkeletonJSON(3, 9, 14, 3, 4)},
		{text: plannerRevisionProposalJSON(9, 14, 3, 4)},
	}}

	updated, err := ReviseAdaptationProposal(context.Background(), Deps{Store: st, LLM: llm}, ProposalRevisionOptions{
		VolumeIndex: 3,
		Instruction: "补充最后一卷结尾，新增两个章节",
	})
	if err != nil {
		t.Fatalf("ReviseAdaptationProposal: %v", err)
	}
	if len(updated.Chapters) != 14 {
		t.Fatalf("chapters=%d, want 14", len(updated.Chapters))
	}
	if updated.Chapters[7].Title != "Original 8" {
		t.Fatalf("chapter 8 should stay unchanged: %+v", updated.Chapters[7])
	}
	for _, chapter := range []int{9, 12, 13, 14} {
		got := updated.Chapters[chapter-1]
		if !strings.HasPrefix(got.Title, "Revised ") {
			t.Fatalf("chapter %d was not revised/appended: %+v", chapter, got)
		}
	}
	if len(updated.Volumes) != 3 || updated.Volumes[2].TargetTo != 14 {
		t.Fatalf("final volume should extend to chapter 14: %+v", updated.Volumes)
	}
	if updated.Volumes[2].Title != "Revised volume 3" || updated.Volumes[2].Summary != "Replanned volume beats." {
		t.Fatalf("final volume metadata was not updated: %+v", updated.Volumes[2])
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved == nil || len(saved.Chapters) != 14 || len(saved.Volumes) != 3 || saved.Volumes[2].TargetTo != 14 || saved.Volumes[2].Title != "Revised volume 3" {
		t.Fatalf("expanded proposal was not saved: %+v", saved)
	}
}

func TestReviseAdaptationProposalLetsModelChooseVolumeExpansionForNaturalInstruction(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	saveRevisionTestProposal(t, st)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: plannerVolumeRevisionSkeletonJSON(3, 9, 14, 3, 4)},
		{text: plannerRevisionProposalJSON(9, 14, 3, 4)},
	}}

	updated, err := ReviseAdaptationProposal(context.Background(), Deps{Store: st, LLM: llm}, ProposalRevisionOptions{
		VolumeIndex: 3,
		Instruction: "加上更多日常纯爱的言情章节，一直写到男女主结婚、怀孕、生了个女儿",
	})
	if err != nil {
		t.Fatalf("ReviseAdaptationProposal: %v", err)
	}
	if len(updated.Chapters) != 14 || updated.Volumes[2].TargetTo != 14 {
		t.Fatalf("model-selected expansion was not applied: chapters=%d volumes=%+v", len(updated.Chapters), updated.Volumes)
	}
	if !strings.Contains(llm.got[0][len(llm.got[0])-1].TextContent(), "expansion_decision") {
		t.Fatalf("volume skeleton prompt should ask the model for an expansion decision")
	}
}

func TestReviseAdaptationProposalRejectsModelExpansionDecisionWithoutNewChapters(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	saveRevisionTestProposal(t, st)
	unchangedSkeleton := plannerVolumeRevisionSkeletonJSONWithDecision(3, 9, 12, 3, 4, "expand")
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: unchangedSkeleton},
		{text: unchangedSkeleton},
		{text: unchangedSkeleton},
	}}

	updated, err := ReviseAdaptationProposal(context.Background(), Deps{Store: st, LLM: llm}, ProposalRevisionOptions{
		VolumeIndex: 3,
		Instruction: "add two new chapters to supplement the ending",
	})
	if err == nil {
		t.Fatalf("ReviseAdaptationProposal succeeded without required expansion: %+v", updated)
	}
	if !strings.Contains(err.Error(), "model chose expansion") {
		t.Fatalf("error should explain missing expansion, got: %v", err)
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved == nil || len(saved.Chapters) != 12 || saved.Volumes[2].TargetTo != 12 || saved.Volumes[2].Title != "Final volume" {
		t.Fatalf("proposal should remain unchanged after failed expansion: %+v", saved)
	}
}

func TestReviseAdaptationProposalRepairsVolumeDetailMissingWordBudget(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	saveRevisionTestProposal(t, st)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: plannerVolumeRevisionSkeletonJSON(3, 9, 14, 3, 4)},
		{text: plannerRevisionProposalJSONMissingWordBudget(9, 14)},
		{text: plannerRevisionProposalJSON(9, 14, 3, 4)},
	}}

	updated, err := ReviseAdaptationProposal(context.Background(), Deps{Store: st, LLM: llm}, ProposalRevisionOptions{
		VolumeIndex: 3,
		Instruction: "加上更多恋爱日常章节",
	})
	if err != nil {
		t.Fatalf("ReviseAdaptationProposal: %v", err)
	}
	if llm.calls != 3 {
		t.Fatalf("planner calls=%d, want skeleton + detail + detail repair", llm.calls)
	}
	if len(updated.Chapters) != 14 || updated.Chapters[8].WordBudget == nil {
		t.Fatalf("repaired proposal should include expanded chapters with word budgets: %+v", updated.Chapters[8])
	}
}

func TestReviseAdaptationProposalAllowsMiddleVolumeExpansionAndShiftsLaterVolumes(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	saveRevisionTestProposal(t, st)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: plannerVolumeRevisionSkeletonJSON(2, 5, 10, 2, 3)},
		{text: plannerRevisionProposalJSON(5, 10, 2, 3)},
	}}

	updated, err := ReviseAdaptationProposal(context.Background(), Deps{Store: st, LLM: llm}, ProposalRevisionOptions{
		VolumeIndex: 2,
		Instruction: "给第二卷新增两章剧情",
	})
	if err != nil {
		t.Fatalf("ReviseAdaptationProposal: %v", err)
	}
	if len(updated.Chapters) != 14 {
		t.Fatalf("chapters=%d, want 14", len(updated.Chapters))
	}
	for _, chapter := range []int{5, 8, 9, 10} {
		got := updated.Chapters[chapter-1]
		if !strings.HasPrefix(got.Title, "Revised ") {
			t.Fatalf("chapter %d was not revised/appended: %+v", chapter, got)
		}
	}
	if updated.Chapters[10].Chapter != 11 || updated.Chapters[10].Title != "Original 9" {
		t.Fatalf("old chapter 9 should shift to target chapter 11: %+v", updated.Chapters[10])
	}
	if len(updated.Volumes) != 3 {
		t.Fatalf("volumes=%d, want 3: %+v", len(updated.Volumes), updated.Volumes)
	}
	if updated.Volumes[1].TargetFrom != 5 || updated.Volumes[1].TargetTo != 10 {
		t.Fatalf("volume 2 should extend to 5-10: %+v", updated.Volumes[1])
	}
	if updated.Volumes[1].Title != "Revised volume 2" || updated.Volumes[1].Summary != "Replanned volume beats." {
		t.Fatalf("volume 2 metadata was not updated: %+v", updated.Volumes[1])
	}
	if updated.Volumes[2].TargetFrom != 11 || updated.Volumes[2].TargetTo != 14 {
		t.Fatalf("volume 3 should shift to 11-14: %+v", updated.Volumes[2])
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved == nil || len(saved.Chapters) != 14 || saved.Volumes[2].TargetFrom != 11 || saved.Volumes[2].TargetTo != 14 {
		t.Fatalf("expanded middle-volume proposal was not saved: %+v", saved)
	}
}

func TestReviseAdaptationProposalRejectsFixedRangeCountChange(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	saveRevisionTestProposal(t, st)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: plannerRevisionProposalJSON(5, 9, 2, 3)}}}

	if _, err := ReviseAdaptationProposal(context.Background(), Deps{Store: st, LLM: llm}, ProposalRevisionOptions{
		FromChapter: 5,
		ToChapter:   8,
		Instruction: "add an extra middle chapter",
	}); err == nil {
		t.Fatal("ReviseAdaptationProposal should reject fixed-range chapter expansion")
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved == nil || len(saved.Chapters) != 12 || saved.Chapters[4].Title != "Original 5" {
		t.Fatalf("failed fixed-range revision should not save changes: %+v", saved)
	}
}

func TestReviseAdaptationProposalRejectsNoChangeRevision(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	saveRevisionTestProposal(t, st)
	original, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: plannerVolumeRevisionSkeletonJSON(3, 9, 12, 3, 4)},
		{text: plannerRevisionNoChangeProposalJSON(t, original.Chapters, 9, 12)},
	}}

	if _, err := ReviseAdaptationProposal(context.Background(), Deps{Store: st, LLM: llm}, ProposalRevisionOptions{
		VolumeIndex: 3,
		Instruction: "make the final volume more emotional",
	}); err == nil {
		t.Fatal("ReviseAdaptationProposal should reject a no-change revision")
	} else if !strings.Contains(err.Error(), "no proposal changes") {
		t.Fatalf("error=%q, want no-change message", err.Error())
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved == nil || len(saved.Chapters) != len(original.Chapters) || saved.Chapters[8].Title != original.Chapters[8].Title {
		t.Fatalf("saved proposal should remain unchanged: saved=%+v original=%+v", saved, original)
	}
	if len(saved.Planner.Notes) != len(original.Planner.Notes) {
		t.Fatalf("failed no-change revision should not append planner notes: saved=%+v original=%+v", saved.Planner, original.Planner)
	}
}

func TestBuildAdaptationProposalLongFreeReturnsVolumeReviewOnly(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "free long arc",
		"target_chapter_count": 20,
		"mainline_rules": ["keep every source turn anchored"],
		"batches": [
			{"index": 1, "title": "Opening volume", "theme": "orientation", "target_from": 1, "target_to": 8, "source_from": 1, "source_to": 2, "summary": "establish the new premise"},
			{"index": 2, "title": "Pressure volume", "theme": "choice", "target_from": 9, "target_to": 16, "source_from": 2, "source_to": 3, "summary": "expand the central conflict"},
			{"index": 3, "title": "Resolution volume", "theme": "payoff", "target_from": 17, "target_to": 20, "source_from": 3, "source_to": 4, "summary": "resolve the adapted ending"}
		]
	}`}}}

	result, err := BuildAdaptationProposalVolumesContext(context.Background(), Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              "free long arc",
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 1 {
		t.Fatalf("planner calls=%d, want skeleton-only volume review", llm.calls)
	}
	if result == nil || result.VolumeReview == nil || result.Proposal != nil {
		t.Fatalf("long free proposal should return volume review only: %+v", result)
	}
	review := result.VolumeReview
	if review.Status != domain.AdaptationPlanStatusVolumeReview {
		t.Fatalf("status=%q, want volume_review", review.Status)
	}
	if len(review.Volumes) != 3 || review.Volumes[2].TargetTo != 20 {
		t.Fatalf("volume review should expose model-planned volumes only: %+v", review.Volumes)
	}
	saved, err := st.Adaptation.LoadVolumeReview()
	if err != nil {
		t.Fatalf("LoadVolumeReview: %v", err)
	}
	if saved == nil || saved.Status != domain.AdaptationPlanStatusVolumeReview || len(saved.Volumes) != 3 {
		t.Fatalf("saved volume review mismatch: %+v", saved)
	}
	if savedProposal, err := st.Adaptation.LoadProposal(); err != nil || savedProposal != nil {
		t.Fatalf("volume review should not save full proposal yet: proposal=%+v err=%v", savedProposal, err)
	}
}

func TestBuildAdaptationProposalChapterAndShortArcStayFullProposal(t *testing.T) {
	t.Run("chapter", func(t *testing.T) {
		st := store.NewStore(t.TempDir())
		if err := st.Init(); err != nil {
			t.Fatalf("Init: %v", err)
		}
		seedPreparedAdaptationSource(t, st, []int{10, 20})

		proposal, err := BuildAdaptationProposal(Deps{Store: st}, ProposalOptions{
			Brief:       "chapter rewrite",
			Granularity: domain.AdaptationGranularityChapter,
		})
		if err != nil {
			t.Fatalf("BuildAdaptationProposal: %v", err)
		}
		if proposal.Status != domain.AdaptationPlanStatusProposal || len(proposal.Chapters) != 2 {
			t.Fatalf("chapter proposal should stay full proposal: %+v", proposal)
		}
	})

	t.Run("short arc", func(t *testing.T) {
		st := store.NewStore(t.TempDir())
		if err := st.Init(); err != nil {
			t.Fatalf("Init: %v", err)
		}
		seedPreparedAdaptationSource(t, st, []int{10, 20, 30})
		llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
			"granularity": "arc",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "short arc restructure",
			"chapters": [
				{
					"chapter": 1,
					"title": "Short arc 1",
					"core_event": "Ari adapts the first source turn.",
					"hook": "The first hook points onward.",
					"scenes": ["station"],
					"source_chapters": [1],
					"source_range": {"from": 1, "to": 1},
					"word_budget": {"source_runes": 10, "target_runes": 12, "min_runes": 11, "max_runes": 13, "tolerance": 0.15}
				},
				{
					"chapter": 2,
					"title": "Short arc 2",
					"core_event": "Ari adapts the second source turn.",
					"hook": "The second hook escalates.",
					"scenes": ["archive"],
					"source_chapters": [2],
					"source_range": {"from": 2, "to": 2},
					"word_budget": {"source_runes": 20, "target_runes": 22, "min_runes": 21, "max_runes": 23, "tolerance": 0.15}
				},
				{
					"chapter": 3,
					"title": "Short arc 3",
					"core_event": "Ari adapts the third source turn.",
					"hook": "The third hook resolves.",
					"scenes": ["roof"],
					"source_chapters": [3],
					"source_range": {"from": 3, "to": 3},
					"word_budget": {"source_runes": 30, "target_runes": 32, "min_runes": 31, "max_runes": 33, "tolerance": 0.15}
				}
			]
		}`}}}

		proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
			Brief:       "short arc restructure",
			Granularity: domain.AdaptationGranularityArc,
		})
		if err != nil {
			t.Fatalf("BuildAdaptationProposal: %v", err)
		}
		if proposal.Status != domain.AdaptationPlanStatusProposal || len(proposal.Chapters) != 3 {
			t.Fatalf("short arc proposal should include full chapter proposal: %+v", proposal)
		}
	})
}

func TestReviseAdaptationVolumeReviewUpdatesOnlySelectedVolume(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	if err := st.Adaptation.SaveVolumeReview(domain.AdaptationVolumeReview{
		Granularity:        domain.AdaptationGranularityFree,
		Status:             domain.AdaptationPlanStatusVolumeReview,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		Brief:              "free long arc",
		TargetChapterCount: 20,
		Volumes: []domain.AdaptationVolumePlan{
			{Index: 1, Title: "Opening volume", TargetFrom: 1, TargetTo: 8, SourceFrom: 1, SourceTo: 2},
			{Index: 2, Title: "Pressure volume", TargetFrom: 9, TargetTo: 16, SourceFrom: 2, SourceTo: 3},
			{Index: 3, Title: "Resolution volume", TargetFrom: 17, TargetTo: 20, SourceFrom: 3, SourceTo: 4},
		},
	}); err != nil {
		t.Fatalf("SaveProposal: %v", err)
	}
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"granularity": "free",
		"status": "volume_review",
		"rewrite_policy": "full_rewrite",
		"brief": "free long arc",
		"target_chapter_count": 22,
		"batches": [
			{"index": 2, "title": "Rebalanced pressure", "theme": "choice under cost", "expansion_decision": "expand", "target_from": 9, "target_to": 18, "source_from": 2, "source_to": 3, "summary": "expand only the middle volume"}
		]
	}`}}}

	updated, err := ReviseAdaptationVolumeReviewContext(context.Background(), Deps{Store: st, LLM: llm}, ProposalRevisionOptions{
		VolumeIndex: 2,
		Instruction: "give the middle volume more room",
	})
	if err != nil {
		t.Fatalf("ReviseAdaptationVolumeReviewContext: %v", err)
	}
	if updated.Status != domain.AdaptationPlanStatusVolumeReview {
		t.Fatalf("volume review revision should stay review-only: %+v", updated)
	}
	if len(updated.Volumes) != 3 {
		t.Fatalf("volumes=%d, want 3: %+v", len(updated.Volumes), updated.Volumes)
	}
	if updated.Volumes[0].TargetFrom != 1 || updated.Volumes[0].TargetTo != 8 || updated.Volumes[0].Title != "Opening volume" {
		t.Fatalf("volume 1 should not be regenerated: %+v", updated.Volumes[0])
	}
	if updated.Volumes[1].Title != "Rebalanced pressure" || updated.Volumes[1].TargetFrom != 9 || updated.Volumes[1].TargetTo != 18 {
		t.Fatalf("volume 2 should be continuously updated: %+v", updated.Volumes[1])
	}
	if updated.Volumes[2].TargetFrom != 19 || updated.Volumes[2].TargetTo != 22 || updated.Volumes[2].Title != "Resolution volume" {
		t.Fatalf("later volume range should shift without regenerating metadata: %+v", updated.Volumes[2])
	}
}

func TestBuildAdaptationProposalDetailsFromVolumeReviewGeneratesFullProposalAndClearsReview(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	review := domain.AdaptationVolumeReview{
		Granularity:        domain.AdaptationGranularityFree,
		Status:             domain.AdaptationPlanStatusVolumeReview,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		Brief:              "free long arc",
		TargetChapterCount: 20,
		Volumes: []domain.AdaptationVolumePlan{
			{Index: 1, Title: "Opening volume", TargetFrom: 1, TargetTo: 8, SourceFrom: 1, SourceTo: 2},
			{Index: 2, Title: "Pressure volume", TargetFrom: 9, TargetTo: 16, SourceFrom: 2, SourceTo: 3},
			{Index: 3, Title: "Resolution volume", TargetFrom: 17, TargetTo: 20, SourceFrom: 3, SourceTo: 4},
		},
	}
	if err := st.Adaptation.SaveVolumeReview(review); err != nil {
		t.Fatalf("SaveVolumeReview: %v", err)
	}
	if err := st.Adaptation.SaveProposalRuntime(domain.AdaptationProposalRuntime{
		Version:            1,
		Brief:              "free long arc",
		Granularity:        domain.AdaptationGranularityFree,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		TargetChapterCount: 20,
		Skeleton: &domain.AdaptationProposalRuntimeOutline{
			TargetChapterCount: 20,
			Batches: []domain.AdaptationProposalRuntimeSkeletonBatch{
				{Index: 1, Title: "Opening volume", TargetFrom: 1, TargetTo: 8, SourceFrom: 1, SourceTo: 2},
				{Index: 2, Title: "Pressure volume", TargetFrom: 9, TargetTo: 16, SourceFrom: 2, SourceTo: 3},
				{Index: 3, Title: "Resolution volume", TargetFrom: 17, TargetTo: 20, SourceFrom: 3, SourceTo: 4},
			},
		},
	}); err != nil {
		t.Fatalf("SaveProposalRuntime: %v", err)
	}
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: plannerBatchProposalJSON(1, 8, 1, 2)},
		{text: plannerBatchProposalJSON(9, 16, 2, 3)},
		{text: plannerBatchProposalJSON(17, 20, 3, 4)},
	}}

	proposal, err := BuildAdaptationProposalDetailsContext(context.Background(), Deps{Store: st, LLM: llm}, ProposalDetailsOptions{})
	if err != nil {
		t.Fatalf("BuildAdaptationProposalDetailsContext: %v", err)
	}
	if proposal.Status != domain.AdaptationPlanStatusProposal || len(proposal.Chapters) != 20 {
		t.Fatalf("confirming volume review should generate full chapter proposal: %+v", proposal)
	}
	if runtime, err := st.Adaptation.LoadProposalRuntime(); err != nil || runtime != nil {
		t.Fatalf("volume review runtime should be cleared after details generation: runtime=%+v err=%v", runtime, err)
	}
	if savedReview, err := st.Adaptation.LoadVolumeReview(); err != nil || savedReview != nil {
		t.Fatalf("volume review should be cleared after details generation: review=%+v err=%v", savedReview, err)
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

func captureAdaptProgress(events *[]Event) ProgressEmitter {
	return func(stage Stage, current, total int, msg string, err error) {
		*events = append(*events, Event{
			Stage:   stage,
			Current: current,
			Total:   total,
			Message: msg,
			Err:     err,
		})
	}
}

func hasAdaptProgress(events []Event, fragment string) bool {
	for _, event := range events {
		if strings.Contains(event.Message, fragment) {
			return true
		}
		if event.Err != nil && strings.Contains(event.Err.Error(), fragment) {
			return true
		}
	}
	return false
}

func stubPlannerRetrySleep(t *testing.T) func() {
	t.Helper()
	original := plannerRetrySleep
	plannerRetrySleep = func(context.Context, time.Duration) error { return nil }
	return func() { plannerRetrySleep = original }
}

func plannerBatchProposalJSON(from, to, sourceFrom, sourceTo int) string {
	return plannerBatchProposalJSONWithOmittedBudget(from, to, sourceFrom, sourceTo, 0)
}

func plannerBatchProposalJSONWithoutWordBudget(from, to, sourceFrom, sourceTo int, omittedChapter int) string {
	return plannerBatchProposalJSONWithOmittedBudget(from, to, sourceFrom, sourceTo, omittedChapter)
}

func plannerBatchProposalJSONWithOmittedBudget(from, to, sourceFrom, sourceTo int, omittedChapter int) string {
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
		wordBudget := fmt.Sprintf(`,
			"word_budget": {"source_runes": %d, "target_runes": %d, "min_runes": %d, "max_runes": %d, "tolerance": 0.15}`, sourceRunes, targetRunes, targetRunes-1, targetRunes+1)
		if chapter == omittedChapter {
			wordBudget = ""
		}
		chapters = append(chapters, fmt.Sprintf(`{
			"chapter": %d,
			"title": "Target %d",
			"core_event": "Ari adapts source turn %d.",
			"hook": "A clear hook for target %d.",
			"scenes": ["station"],
			"source_chapters": [%d],
			"source_range": {"from": %d, "to": %d}%s,
			"preserve_events": ["source event"],
			"required_changes": ["adapt the beat"],
			"forbidden_moves": ["drop the source anchor"]
		}`, chapter, chapter, sourceChapter, chapter, sourceChapter, sourceChapter, sourceChapter, wordBudget))
	}
	return fmt.Sprintf(`{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "chunk",
		"chapters": [%s]
	}`, strings.Join(chapters, ","))
}

func plannerVolumeRevisionSkeletonJSON(index, from, to, sourceFrom, sourceTo int) string {
	originalTo := map[int]int{1: 4, 2: 8, 3: 12}[index]
	decision := "keep"
	if originalTo > 0 && to > originalTo {
		decision = "expand"
	}
	return plannerVolumeRevisionSkeletonJSONWithDecision(index, from, to, sourceFrom, sourceTo, decision)
}

func plannerVolumeRevisionSkeletonJSONWithDecision(index, from, to, sourceFrom, sourceTo int, decision string) string {
	return fmt.Sprintf(`{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "chunk",
		"target_chapter_count": %d,
		"batches": [{
			"index": %d,
			"title": "Revised volume %d",
			"theme": "rebalanced pressure",
			"expansion_decision": %q,
			"expansion_reason": "model judged the revised volume scope.",
			"summary": "Replanned volume beats.",
			"target_from": %d,
			"target_to": %d,
			"source_from": %d,
			"source_to": %d
		}]
	}`, to-from+1, index, index, decision, from, to, sourceFrom, sourceTo)
}

func plannerRevisionProposalJSON(from, to, sourceFrom, sourceTo int) string {
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
			"title": "Revised %d",
			"core_event": "Revised event for target %d.",
			"hook": "Revised hook for target %d.",
			"scenes": ["archive", "station"],
			"source_chapters": [%d],
			"source_range": {"from": %d, "to": %d},
			"word_budget": {"source_runes": %d, "target_runes": %d, "min_runes": %d, "max_runes": %d, "tolerance": 0.15},
			"preserve_events": ["source event"],
			"required_changes": ["apply the revision"],
			"forbidden_moves": ["drop the source anchor"]
		}`, chapter, chapter, chapter, chapter, sourceChapter, sourceChapter, sourceChapter, sourceRunes, targetRunes, targetRunes-1, targetRunes+1))
	}
	return fmt.Sprintf(`{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "chunk",
		"chapters": [%s]
	}`, strings.Join(chapters, ","))
}

func plannerRevisionProposalJSONMissingWordBudget(from, to int) string {
	var chapters []string
	for chapter := from; chapter <= to; chapter++ {
		chapters = append(chapters, fmt.Sprintf(`{
			"chapter": %d,
			"title": "Incomplete revised %d",
			"core_event": "Incomplete revised event %d.",
			"hook": "Incomplete revised hook %d.",
			"scenes": ["repair-needed"]
		}`, chapter, chapter, chapter, chapter))
	}
	return fmt.Sprintf(`{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "chunk",
		"chapters": [%s]
	}`, strings.Join(chapters, ","))
}

func plannerRevisionNoChangeProposalJSON(t *testing.T, chapters []domain.AdaptationChapterPlan, from, to int) string {
	t.Helper()
	payload := map[string]any{
		"chapters": proposalChaptersInRange(chapters, from, to),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal no-change revision: %v", err)
	}
	return string(raw)
}

func saveRevisionTestProposal(t *testing.T, st *store.Store) {
	t.Helper()
	chapters := make([]domain.AdaptationChapterPlan, 0, 12)
	sourceTotal := 0
	targetTotal := 0
	targetMinTotal := 0
	targetMaxTotal := 0
	for chapter := 1; chapter <= 12; chapter++ {
		sourceChapter := 1 + (chapter-1)*4/12
		sourceRunes := sourceChapter * 10
		targetRunes := sourceRunes + 2
		sourceTotal += sourceRunes
		targetTotal += targetRunes
		targetMinTotal += targetRunes - 1
		targetMaxTotal += targetRunes + 1
		chapters = append(chapters, domain.AdaptationChapterPlan{
			Chapter:        chapter,
			Title:          fmt.Sprintf("Original %d", chapter),
			SourceChapters: []int{sourceChapter},
			SourceRunes:    sourceRunes,
			TargetRunes:    targetRunes,
			TargetMinRunes: targetRunes - 1,
			TargetMaxRunes: targetRunes + 1,
			SourceRange:    domain.SourceRange{From: sourceChapter, To: sourceChapter},
			WordBudget: &domain.AdaptationChapterWordBudget{
				SourceRunes: sourceRunes,
				TargetRunes: targetRunes,
				MinRunes:    targetRunes - 1,
				MaxRunes:    targetRunes + 1,
				Tolerance:   0.15,
			},
			PreserveEvents:  []string{"source event"},
			RequiredChanges: []string{"keep the original shape"},
			ForbiddenMoves:  []string{"drop the source anchor"},
			OutlineEntry: domain.OutlineEntry{
				Chapter:   chapter,
				Title:     fmt.Sprintf("Original %d", chapter),
				CoreEvent: fmt.Sprintf("Original event for target %d.", chapter),
				Hook:      fmt.Sprintf("Original hook for target %d.", chapter),
				Scenes:    []string{"station"},
			},
		})
	}
	plan := domain.AdaptationPlan{
		Granularity:      domain.AdaptationGranularityFree,
		Status:           domain.AdaptationPlanStatusProposal,
		RewritePolicy:    domain.AdaptationRewriteFullRewrite,
		Brief:            "chunk",
		SourceTotalRunes: sourceTotal,
		TargetTotalRunes: targetTotal,
		TargetMinRunes:   targetMinTotal,
		TargetMaxRunes:   targetMaxTotal,
		Volumes: []domain.AdaptationVolumePlan{
			{Index: 1, Title: "Opening volume", Theme: "orientation", TargetFrom: 1, TargetTo: 4, SourceFrom: 1, SourceTo: 2},
			{Index: 2, Title: "Middle volume", Theme: "pressure", TargetFrom: 5, TargetTo: 8, SourceFrom: 2, SourceTo: 3},
			{Index: 3, Title: "Final volume", Theme: "payoff", TargetFrom: 9, TargetTo: 12, SourceFrom: 3, SourceTo: 4},
		},
		Chapters: chapters,
		Planner:  &domain.AdaptationPlannerMeta{Prompt: "adaptation-planner", Model: "fake"},
	}
	if err := st.Adaptation.SaveProposal(plan); err != nil {
		t.Fatalf("SaveProposal: %v", err)
	}
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
