package store

import (
	"os"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestAdaptationStoreSavesSourceSnapshotAndChecks(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	source, err := s.Adaptation.SaveSourceChapter(1, "初遇", "原文第一章内容。")
	if err != nil {
		t.Fatalf("SaveSourceChapter: %v", err)
	}
	if source.SHA256 == "" || source.Path == "" || source.Runes == 0 {
		t.Fatalf("source metadata incomplete: %+v", source)
	}
	if err := s.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 1,
		Chapters:     []domain.AdaptationSource{source},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}

	text, loaded, err := s.Adaptation.LoadSourceChapter(1)
	if err != nil {
		t.Fatalf("LoadSourceChapter: %v", err)
	}
	if text != "原文第一章内容。" {
		t.Fatalf("text=%q", text)
	}
	if loaded == nil || loaded.Title != "初遇" {
		t.Fatalf("source metadata not loaded: %+v", loaded)
	}

	check := domain.AdaptationCheck{
		Chapter:     1,
		DraftSHA256: TextSHA256("草稿"),
		Passed:      true,
		CheckedAt:   "2026-06-29T00:00:00Z",
	}
	if err := s.Adaptation.SaveCheck(check); err != nil {
		t.Fatalf("SaveCheck: %v", err)
	}
	passed, saved, err := s.Adaptation.HasPassingCheck(1, check.DraftSHA256)
	if err != nil {
		t.Fatalf("HasPassingCheck: %v", err)
	}
	if !passed || saved == nil || saved.DraftSHA256 != check.DraftSHA256 {
		t.Fatalf("passing check mismatch: passed=%v saved=%+v", passed, saved)
	}
}

func TestAdaptationProposalDoesNotActivateProject(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	proposal := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityChapter,
		RewritePolicy: domain.AdaptationRewritePreserveDetails,
		Brief:         "按原著细节逐章改编",
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "第一章",
			SourceChapters: []int{1},
		}},
	}
	if err := s.Adaptation.SaveProposal(proposal); err != nil {
		t.Fatalf("SaveProposal: %v", err)
	}
	if s.Adaptation.Active() {
		t.Fatal("proposal-only adaptation should not be active")
	}
	if plan, err := s.Adaptation.LoadPlan(); err != nil || plan != nil {
		t.Fatalf("LoadPlan should ignore proposal: plan=%+v err=%v", plan, err)
	}
	loaded, err := s.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if loaded == nil || loaded.Status != domain.AdaptationPlanStatusProposal {
		t.Fatalf("proposal status mismatch: %+v", loaded)
	}

	if err := s.Adaptation.SavePlan(proposal); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if !s.Adaptation.Active() {
		t.Fatal("confirmed adaptation plan should be active")
	}
	confirmed, err := s.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan confirmed: %v", err)
	}
	if confirmed == nil || confirmed.Status != domain.AdaptationPlanStatusConfirmed {
		t.Fatalf("confirmed status mismatch: %+v", confirmed)
	}
}

func TestAdaptationPlanLoadNormalizesLegacyAndNestedWordBudget(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	legacy := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityChapter,
		RewritePolicy: domain.AdaptationRewritePreserveDetails,
		Brief:         "legacy",
		WordTolerance: 0.15,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "Legacy",
			SourceChapters: []int{1},
			SourceRunes:    1000,
			TargetRunes:    1100,
			TargetMinRunes: 900,
			TargetMaxRunes: 1200,
		}},
	}
	if err := s.Adaptation.io.WriteJSON(adaptationPlanFile, legacy); err != nil {
		t.Fatalf("WriteJSON legacy: %v", err)
	}
	loadedLegacy, err := s.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan legacy: %v", err)
	}
	chapter := loadedLegacy.Chapters[0]
	if loadedLegacy.Status != domain.AdaptationPlanStatusConfirmed {
		t.Fatalf("legacy status = %q, want confirmed", loadedLegacy.Status)
	}
	if chapter.WordBudget == nil || chapter.WordBudget.TargetRunes != 1100 || chapter.WordBudget.MinRunes != 900 || chapter.WordBudget.Tolerance != 0.15 {
		t.Fatalf("legacy word budget not mirrored: %+v", chapter.WordBudget)
	}
	if chapter.TargetRunes != 1100 || chapter.TargetMinRunes != 900 || chapter.TargetMaxRunes != 1200 {
		t.Fatalf("legacy target fields changed: %+v", chapter)
	}

	nested := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		Status:      domain.AdaptationPlanStatusProposal,
		Brief:       "nested",
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "Nested",
			SourceChapters: []int{1, 2},
			OutlineEntry: domain.OutlineEntry{
				CoreEvent: "combined event",
				Hook:      "new hook",
				Scenes:    []string{"first", "second"},
			},
			WordBudget: &domain.AdaptationChapterWordBudget{
				SourceRunes: 2000,
				TargetRunes: 2300,
				MinRunes:    2100,
				MaxRunes:    2500,
			},
		}},
	}
	if err := s.Adaptation.io.WriteJSON(adaptationProposalFile, nested); err != nil {
		t.Fatalf("WriteJSON nested: %v", err)
	}
	loadedNested, err := s.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal nested: %v", err)
	}
	nestedChapter := loadedNested.Chapters[0]
	if nestedChapter.TargetRunes != 2300 || nestedChapter.TargetMinRunes != 2100 || nestedChapter.TargetMaxRunes != 2500 {
		t.Fatalf("nested word budget did not backfill legacy fields: %+v", nestedChapter)
	}
	if nestedChapter.CoreEvent != "combined event" || nestedChapter.Hook != "new hook" || len(nestedChapter.Scenes) != 2 {
		t.Fatalf("outline fields not preserved: %+v", nestedChapter.OutlineEntry)
	}
}

func TestAdaptationPlanPersistsSourceDerivedSoftBudgets(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	source1, err := s.Adaptation.SaveSourceChapter(1, "One", strings.Repeat("a", 100))
	if err != nil {
		t.Fatalf("SaveSourceChapter 1: %v", err)
	}
	source2, err := s.Adaptation.SaveSourceChapter(2, "Two", strings.Repeat("b", 50))
	if err != nil {
		t.Fatalf("SaveSourceChapter 2: %v", err)
	}
	if err := s.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 2,
		Chapters:     []domain.AdaptationSource{source1, source2},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}

	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		Brief:       "soft default budgets",
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "One", SourceChapters: []int{1}},
			{Chapter: 2, Title: "Two", SourceChapters: []int{2}},
		},
	}
	if err := s.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	first, err := s.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan first: %v", err)
	}
	reloaded := NewStore(s.Dir())
	second, err := reloaded.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan second: %v", err)
	}

	if first.TargetTotalRunes != 150 || first.TargetMinRunes != 128 || first.TargetMaxRunes != 172 {
		t.Fatalf("plan totals = %+v", first)
	}
	if first.Chapters[0].WordBudget == nil || first.Chapters[0].TargetRunes != 100 ||
		first.Chapters[0].TargetMinRunes != 85 || first.Chapters[0].TargetMaxRunes != 115 {
		t.Fatalf("chapter 1 budget = %+v", first.Chapters[0])
	}
	if first.Chapters[1].WordBudget == nil || first.Chapters[1].TargetRunes != 50 ||
		first.Chapters[1].TargetMinRunes != 43 || first.Chapters[1].TargetMaxRunes != 57 {
		t.Fatalf("chapter 2 budget = %+v", first.Chapters[1])
	}
	if second.TargetTotalRunes != first.TargetTotalRunes ||
		second.TargetMinRunes != first.TargetMinRunes ||
		second.TargetMaxRunes != first.TargetMaxRunes ||
		second.Chapters[0].TargetMinRunes != first.Chapters[0].TargetMinRunes ||
		second.Chapters[1].TargetMaxRunes != first.Chapters[1].TargetMaxRunes {
		t.Fatalf("budgets changed across reload: first=%+v second=%+v", first, second)
	}
}

func TestAdaptationStoreResetGeneratedPreservesSourceSnapshot(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	source, err := s.Adaptation.SaveSourceChapter(1, "Opening", "source chapter body")
	if err != nil {
		t.Fatalf("SaveSourceChapter: %v", err)
	}
	if err := s.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 1,
		Chapters:     []domain.AdaptationSource{source},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	if err := s.Adaptation.SaveSourceFoundation(domain.AdaptationSourceFoundation{
		Premise: "# Source Book\n",
		Characters: []domain.Character{
			{Name: "Ari", Role: "lead", Description: "keeps the plot moving"},
		},
	}); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	if err := s.Adaptation.SaveSourceReports([]domain.AdaptationSourceReport{
		{Chapter: 1, Title: "Opening", KeyEvents: []string{"Ari accepts the call"}},
	}); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}
	if err := s.Adaptation.SavePlan(domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityFree,
		Brief:       "old generated plan",
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "Old", SourceChapters: []int{1}},
		},
	}); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if err := s.Adaptation.SaveCheck(domain.AdaptationCheck{
		Chapter:     1,
		DraftSHA256: TextSHA256("old draft"),
		Passed:      true,
		CheckedAt:   "2026-06-29T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveCheck: %v", err)
	}

	if err := s.Adaptation.ResetGenerated(); err != nil {
		t.Fatalf("ResetGenerated: %v", err)
	}

	if _, err := os.Stat(s.Adaptation.io.path(adaptationPlanFile)); !os.IsNotExist(err) {
		t.Fatalf("plan file should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(s.Adaptation.io.path(adaptationCheckDir)); !os.IsNotExist(err) {
		t.Fatalf("checks directory should be removed, stat err=%v", err)
	}
	if plan, err := s.Adaptation.LoadPlan(); err != nil || plan != nil {
		t.Fatalf("LoadPlan after reset: plan=%+v err=%v", plan, err)
	}
	if check, err := s.Adaptation.LoadCheck(1); err != nil || check != nil {
		t.Fatalf("LoadCheck after reset: check=%+v err=%v", check, err)
	}

	manifest, err := s.Adaptation.LoadSourceManifest()
	if err != nil {
		t.Fatalf("LoadSourceManifest: %v", err)
	}
	if manifest == nil || manifest.SourcePath != "source.txt" || manifest.ChapterCount != 1 {
		t.Fatalf("source manifest not preserved: %+v", manifest)
	}
	text, loadedSource, err := s.Adaptation.LoadSourceChapter(1)
	if err != nil {
		t.Fatalf("LoadSourceChapter: %v", err)
	}
	if text != "source chapter body" || loadedSource == nil || loadedSource.Title != "Opening" {
		t.Fatalf("source chapter not preserved: text=%q source=%+v", text, loadedSource)
	}
	foundation, err := s.Adaptation.LoadSourceFoundation()
	if err != nil {
		t.Fatalf("LoadSourceFoundation: %v", err)
	}
	if foundation == nil || foundation.Premise != "# Source Book\n" {
		t.Fatalf("source foundation not preserved: %+v", foundation)
	}
	reports, err := s.Adaptation.LoadSourceReports()
	if err != nil {
		t.Fatalf("LoadSourceReports: %v", err)
	}
	if len(reports) != 1 || reports[0].Title != "Opening" {
		t.Fatalf("source reports not preserved: %+v", reports)
	}
}

func TestAdaptationStoreLoadsSingleChapterReportsBeforeLegacyAggregate(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if err := s.Adaptation.SaveSourceReports([]domain.AdaptationSourceReport{
		{Chapter: 1, Title: "legacy", Summary: "legacy summary", KeyEvents: []string{"legacy event"}},
	}); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}
	if err := s.Adaptation.SaveSourceReport(domain.AdaptationSourceReport{
		Chapter:      1,
		Title:        "single",
		SourceSHA256: "sha-1",
		Summary:      "single summary",
		KeyEvents:    []string{"single event"},
	}); err != nil {
		t.Fatalf("SaveSourceReport: %v", err)
	}

	reports, err := s.Adaptation.LoadSourceReports()
	if err != nil {
		t.Fatalf("LoadSourceReports: %v", err)
	}
	if len(reports) != 1 || reports[0].Title != "single" {
		t.Fatalf("LoadSourceReports should prefer single files, got %+v", reports)
	}
}

func TestAdaptationStoreLoadCompleteSourceReportsRequiresMatchingSHA(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	source1, err := s.Adaptation.SaveSourceChapter(1, "One", "chapter one")
	if err != nil {
		t.Fatalf("SaveSourceChapter 1: %v", err)
	}
	source2, err := s.Adaptation.SaveSourceChapter(2, "Two", "chapter two")
	if err != nil {
		t.Fatalf("SaveSourceChapter 2: %v", err)
	}
	if err := s.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 2,
		Chapters:     []domain.AdaptationSource{source1, source2},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}

	if err := s.Adaptation.SaveSourceReport(domain.AdaptationSourceReport{
		Chapter:      1,
		Title:        "One",
		SourceSHA256: source1.SHA256,
		Summary:      "summary one",
		KeyEvents:    []string{"event one"},
	}); err != nil {
		t.Fatalf("SaveSourceReport 1: %v", err)
	}
	if reports, err := s.Adaptation.LoadCompleteSourceReports(); err != nil || reports != nil {
		t.Fatalf("incomplete reports should not load: reports=%+v err=%v", reports, err)
	}

	if err := s.Adaptation.SaveSourceReport(domain.AdaptationSourceReport{
		Chapter:      2,
		Title:        "Two",
		SourceSHA256: "wrong-sha",
		Summary:      "summary two",
		KeyEvents:    []string{"event two"},
	}); err != nil {
		t.Fatalf("SaveSourceReport wrong SHA: %v", err)
	}
	if reports, err := s.Adaptation.LoadCompleteSourceReports(); err != nil || reports != nil {
		t.Fatalf("SHA mismatch should not load: reports=%+v err=%v", reports, err)
	}

	if err := s.Adaptation.SaveSourceReport(domain.AdaptationSourceReport{
		Chapter:      2,
		Title:        "Two",
		SourceSHA256: source2.SHA256,
		Summary:      "summary two",
		KeyEvents:    []string{"event two"},
	}); err != nil {
		t.Fatalf("SaveSourceReport matching SHA: %v", err)
	}
	reports, err := s.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		t.Fatalf("LoadCompleteSourceReports: %v", err)
	}
	if len(reports) != 2 || reports[0].Chapter != 1 || reports[1].Chapter != 2 {
		t.Fatalf("complete reports mismatch: %+v", reports)
	}
}
