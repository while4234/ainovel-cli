package store

import (
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
