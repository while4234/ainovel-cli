package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestReadChapterSourceRequiresAdaptationProject(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tool := NewReadChapterTool(s)
	args, _ := json.Marshal(map[string]any{"chapter": 1, "source": "source"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "不是改编模式") {
		t.Fatalf("expected non-adaptation source read error, got %v", err)
	}
}

func TestReadChapterSourceLoadsAdaptationSnapshot(t *testing.T) {
	s := newAdaptationToolStore(t)

	tool := NewReadChapterTool(s)
	args, _ := json.Marshal(map[string]any{"chapter": 1, "source": "source"})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Chapter int    `json:"chapter"`
		Title   string `json:"title"`
		Source  string `json:"source"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Source != "source" || payload.Title != "源章" || payload.Content != "原文主线事件。" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestCheckAdaptationStoresDraftDigest(t *testing.T) {
	s := newAdaptationToolStore(t)
	if err := s.Drafts.SaveDraft(1, "改编草稿正文。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewCheckAdaptationTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter": 1,
		"passed":  true,
		"summary": "保留主线，落实女主互动",
	})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Passed      bool   `json:"passed"`
		DraftSHA256 string `json:"draft_sha256"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !payload.Passed {
		t.Fatalf("expected passed check, got %+v", payload)
	}
	if payload.DraftSHA256 != store.TextSHA256("改编草稿正文。") {
		t.Fatalf("digest mismatch: %s", payload.DraftSHA256)
	}
	check, err := s.Adaptation.LoadCheck(1)
	if err != nil {
		t.Fatalf("LoadCheck: %v", err)
	}
	if check == nil || !check.Passed || check.DraftSHA256 != payload.DraftSHA256 {
		t.Fatalf("saved check mismatch: %+v", check)
	}
}

func TestCommitChapterRequiresPassingAdaptationCheck(t *testing.T) {
	s := newAdaptationToolStore(t)
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, "改编草稿正文。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	commit := NewCommitChapterTool(s)
	commitArgs, _ := json.Marshal(map[string]any{
		"chapter":    1,
		"summary":    "摘要",
		"characters": []string{"主角"},
		"key_events": []string{"主线事件"},
	})
	if _, err := commit.Execute(context.Background(), commitArgs); err == nil || !strings.Contains(err.Error(), "check_adaptation") {
		t.Fatalf("expected commit gate rejection, got %v", err)
	}

	check := NewCheckAdaptationTool(s)
	failArgs, _ := json.Marshal(map[string]any{
		"chapter": 1,
		"passed":  true,
		"issues":  []string{"遗漏原章关键事件"},
		"summary": "仍需返工",
	})
	if _, err := check.Execute(context.Background(), failArgs); err != nil {
		t.Fatalf("failed check Execute: %v", err)
	}
	if _, err := commit.Execute(context.Background(), commitArgs); err == nil || !strings.Contains(err.Error(), "未通过") {
		t.Fatalf("expected failed check rejection, got %v", err)
	}

	passArgs, _ := json.Marshal(map[string]any{
		"chapter": 1,
		"passed":  true,
		"summary": "主线和改编目标均满足",
	})
	if _, err := check.Execute(context.Background(), passArgs); err != nil {
		t.Fatalf("passing check Execute: %v", err)
	}
	if _, err := commit.Execute(context.Background(), commitArgs); err != nil {
		t.Fatalf("commit after passing check: %v", err)
	}
}

func newAdaptationToolStore(t *testing.T) *store.Store {
	t.Helper()
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	source, err := s.Adaptation.SaveSourceChapter(1, "源章", "原文主线事件。")
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
	if err := s.Adaptation.SaveSourceReports([]domain.AdaptationSourceReport{
		{Chapter: 1, Title: "源章", Summary: "原文摘要", KeyEvents: []string{"主线事件"}},
	}); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}
	if err := s.Adaptation.SavePlan(domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityChapter,
		Brief:         "增加女主互动",
		MainlineRules: []string{"主线不要走偏"},
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:         1,
			Title:           "目标章",
			SourceChapters:  []int{1},
			PreserveEvents:  []string{"主线事件"},
			RequiredChanges: []string{"增加女主互动"},
			ForbiddenMoves:  []string{"不要跳过主线事件"},
		}},
	}); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	return s
}
