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

func TestAdaptationWriterToolsRejectChapterOutsideConfirmedPlan(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityChapter,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Brief:         "三章改编计划",
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "一", SourceChapters: []int{1}},
			{Chapter: 2, Title: "二", SourceChapters: []int{2}},
			{Chapter: 3, Title: "三", SourceChapters: []int{3}},
		},
	}
	s := newAdaptationToolStoreWithPlan(t, plan, []string{"源一", "源二", "源三"})
	if err := s.Progress.Init("test", 4); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}

	if _, err := NewPlanChapterTool(s).Execute(context.Background(), planArgs(4)); err == nil || !strings.Contains(err.Error(), "第 4 章") {
		t.Fatalf("plan_chapter should reject chapter 4, got %v", err)
	}

	draftArgs, _ := json.Marshal(map[string]any{
		"chapter": 4,
		"content": "越界草稿。",
		"mode":    "write",
	})
	if _, err := NewDraftChapterTool(s).Execute(context.Background(), draftArgs); err == nil || !strings.Contains(err.Error(), "第 4 章") {
		t.Fatalf("draft_chapter should reject chapter 4, got %v", err)
	}

	checkArgs, _ := json.Marshal(map[string]any{
		"chapter": 4,
		"passed":  true,
		"summary": "越界校验",
	})
	if _, err := NewCheckAdaptationTool(s).Execute(context.Background(), checkArgs); err == nil || !strings.Contains(err.Error(), "第 4 章") {
		t.Fatalf("check_adaptation should reject chapter 4, got %v", err)
	}

	commitArgs, _ := json.Marshal(map[string]any{
		"chapter":    4,
		"summary":    "越界提交",
		"characters": []string{"主角"},
		"key_events": []string{"越界事件"},
	})
	if _, err := NewCommitChapterTool(s).Execute(context.Background(), commitArgs); err == nil || !strings.Contains(err.Error(), "第 4 章") {
		t.Fatalf("commit_chapter should reject chapter 4, got %v", err)
	}
}

func TestPreserveDetailsWordContractRejectsCheckAndCommit(t *testing.T) {
	source := strings.Repeat("源", 10)
	draft := strings.Repeat("改", 20)
	plan := domain.AdaptationPlan{
		Granularity:      domain.AdaptationGranularityChapter,
		RewritePolicy:    domain.AdaptationRewritePreserveDetails,
		WordTolerance:    0.15,
		Brief:            "原著细节优先",
		SourceTotalRunes: 10,
		TargetTotalRunes: 10,
		TargetMinRunes:   9,
		TargetMaxRunes:   12,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:         1,
			Title:           "目标章",
			SourceChapters:  []int{1},
			SourceRunes:     10,
			TargetRunes:     10,
			TargetMinRunes:  9,
			TargetMaxRunes:  12,
			SourceRange:     domain.SourceRange{From: 1, To: 1},
			PreserveEvents:  []string{"主线事件"},
			RequiredChanges: []string{"增加女主互动"},
			ForbiddenMoves:  []string{"不要跳过主线事件"},
		}},
	}
	s := newAdaptationToolStoreWithPlan(t, plan, []string{source})
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, draft); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	checkArgs, _ := json.Marshal(map[string]any{
		"chapter": 1,
		"passed":  true,
		"summary": "主线和改编目标均满足",
	})
	raw, err := NewCheckAdaptationTool(s).Execute(context.Background(), checkArgs)
	if err != nil {
		t.Fatalf("check_adaptation Execute: %v", err)
	}
	var checkPayload struct {
		Passed bool     `json:"passed"`
		Issues []string `json:"issues"`
		Next   string   `json:"next_step"`
	}
	if err := json.Unmarshal(raw, &checkPayload); err != nil {
		t.Fatalf("Unmarshal check payload: %v", err)
	}
	if checkPayload.Passed || !issueContains(checkPayload.Issues, "adaptation_word_contract") {
		t.Fatalf("check_adaptation should fail word contract, got %+v", checkPayload)
	}
	if !strings.Contains(checkPayload.Next, "不要再次调用 commit_chapter") {
		t.Fatalf("check_adaptation should return repair next_step, got %q", checkPayload.Next)
	}

	if err := s.Adaptation.SaveCheck(domain.AdaptationCheck{
		Chapter:     1,
		DraftSHA256: store.TextSHA256(draft),
		Passed:      true,
		CheckedAt:   "2026-06-30T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveCheck override: %v", err)
	}
	commitArgs, _ := json.Marshal(map[string]any{
		"chapter":    1,
		"summary":    "摘要",
		"characters": []string{"主角"},
		"key_events": []string{"主线事件"},
	})
	if _, err := NewCommitChapterTool(s).Execute(context.Background(), commitArgs); err == nil ||
		!strings.Contains(err.Error(), "adaptation_word_contract") ||
		!strings.Contains(err.Error(), "不要再次调用 commit_chapter") {
		t.Fatalf("commit_chapter should independently reject word contract, got %v", err)
	}
}

func TestPreserveDetailsPlanAndDraftSteerTowardFullSourceBackedChapter(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity:      domain.AdaptationGranularityChapter,
		RewritePolicy:    domain.AdaptationRewritePreserveDetails,
		WordTolerance:    0.15,
		Brief:            "原著细节优先",
		SourceTotalRunes: 100,
		TargetTotalRunes: 100,
		TargetMinRunes:   85,
		TargetMaxRunes:   115,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "目标章",
			SourceChapters: []int{1},
			SourceRunes:    100,
			TargetRunes:    100,
			TargetMinRunes: 85,
			TargetMaxRunes: 115,
			SourceRange:    domain.SourceRange{From: 1, To: 1},
		}},
	}
	s := newAdaptationToolStoreWithPlan(t, plan, []string{strings.Repeat("源", 100)})
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}

	planRaw, err := NewPlanChapterTool(s).Execute(context.Background(), planArgs(1))
	if err != nil {
		t.Fatalf("plan_chapter Execute: %v", err)
	}
	var planPayload struct {
		Next string `json:"next_step"`
	}
	if err := json.Unmarshal(planRaw, &planPayload); err != nil {
		t.Fatalf("Unmarshal plan payload: %v", err)
	}
	for _, want := range []string{`read_chapter(source="source"`, "完整章节正文", "不能只写改动片段", "85-115"} {
		if !strings.Contains(planPayload.Next, want) {
			t.Fatalf("plan next_step missing %q: %q", want, planPayload.Next)
		}
	}

	draftArgs, _ := json.Marshal(map[string]any{
		"chapter": 1,
		"content": strings.Repeat("改", 20),
		"mode":    "write",
	})
	draftRaw, err := NewDraftChapterTool(s).Execute(context.Background(), draftArgs)
	if err != nil {
		t.Fatalf("draft_chapter Execute: %v", err)
	}
	var draftPayload struct {
		WordContractPassed bool     `json:"word_contract_passed"`
		WordContractIssues []string `json:"word_contract_issues"`
		Next               string   `json:"next_step"`
	}
	if err := json.Unmarshal(draftRaw, &draftPayload); err != nil {
		t.Fatalf("Unmarshal draft payload: %v", err)
	}
	if draftPayload.WordContractPassed || !issueContains(draftPayload.WordContractIssues, "低于硬区间") {
		t.Fatalf("draft should report failed low word contract, got %+v", draftPayload)
	}
	for _, want := range []string{`不要再次调用 commit_chapter`, `read_chapter(source="source"`, "完整章节正文", "不是只写改动片段", "85-115"} {
		if !strings.Contains(draftPayload.Next, want) {
			t.Fatalf("draft next_step missing %q: %q", want, draftPayload.Next)
		}
	}
}

func TestAdaptationSaveFoundationRejectsAppendVolume(t *testing.T) {
	s := newAdaptationToolStore(t)
	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type": "append_volume",
		"content": map[string]any{
			"index": 1,
			"title": "新卷",
			"theme": "继续",
			"arcs":  []any{},
		},
	})
	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "confirmed plan") {
		t.Fatalf("append_volume should be rejected in adaptation mode, got %v", err)
	}
}

func issueContains(issues []string, sub string) bool {
	for _, issue := range issues {
		if strings.Contains(issue, sub) {
			return true
		}
	}
	return false
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

func newAdaptationToolStoreWithPlan(t *testing.T, plan domain.AdaptationPlan, sourceTexts []string) *store.Store {
	t.Helper()
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	sources := make([]domain.AdaptationSource, 0, len(sourceTexts))
	reports := make([]domain.AdaptationSourceReport, 0, len(sourceTexts))
	for i, text := range sourceTexts {
		chapter := i + 1
		source, err := s.Adaptation.SaveSourceChapter(chapter, "源章", text)
		if err != nil {
			t.Fatalf("SaveSourceChapter(%d): %v", chapter, err)
		}
		sources = append(sources, source)
		reports = append(reports, domain.AdaptationSourceReport{
			Chapter: chapter,
			Title:   "源章",
			Summary: "原文摘要",
			KeyEvents: []string{
				"主线事件",
			},
		})
	}
	if err := s.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: len(sources),
		Chapters:     sources,
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	if err := s.Adaptation.SaveSourceReports(reports); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}
	if err := s.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	return s
}
