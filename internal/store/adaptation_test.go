package store

import (
	"os"
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
