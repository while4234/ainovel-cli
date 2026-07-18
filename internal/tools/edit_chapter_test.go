package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// TestEditChapterAppliesEdit 正常路径：drafts 已有内容，唯一匹配替换成功。
func TestEditChapterAppliesEdit(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Drafts.SaveDraft(2, "他握紧了拳头，指节发白。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewEditChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"old_string": "指节发白",
		"new_string": "指节泛起青白",
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got, err := s.Drafts.LoadDraft(2)
	if err != nil {
		t.Fatalf("LoadDraft: %v", err)
	}
	if !strings.Contains(got, "指节泛起青白") {
		t.Fatalf("expected draft to contain new text, got %q", got)
	}
	if strings.Contains(got, "指节发白") {
		t.Fatalf("old text should be replaced, got %q", got)
	}
}

func TestEditChapterUpdatesCanonicalDraftAfterStructureMigration(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1,
		Title: "第一卷",
		Arcs: []domain.ArcOutline{{
			Index:    1,
			Title:    "第一弧",
			Chapters: []domain.OutlineEntry{{Chapter: 1, Title: "第一章"}},
		}},
	}}); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, "她记得一句不该出现的旧台词。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	args := json.RawMessage(`{"chapter":1,"old_string":"不该出现的旧台词","new_string":"露台上真实发生的问答"}`)
	if _, err := NewEditChapterTool(s).Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	draft, err := s.Drafts.LoadDraft(1)
	if err != nil {
		t.Fatalf("LoadDraft: %v", err)
	}
	if draft != "她记得一句露台上真实发生的问答。" {
		t.Fatalf("canonical draft was not updated: %q", draft)
	}
	legacy, err := os.ReadFile(filepath.Join(dir, "drafts", "01.draft.md"))
	if err != nil {
		t.Fatalf("ReadFile legacy draft: %v", err)
	}
	if string(legacy) != draft {
		t.Fatalf("legacy/canonical draft diverged: legacy=%q canonical=%q", legacy, draft)
	}
	legacyPath := filepath.Join(dir, "drafts", "01.draft.md")
	if err := os.WriteFile(legacyPath, []byte("stale legacy draft"), 0o644); err != nil {
		t.Fatalf("WriteFile stale legacy draft: %v", err)
	}
	reopened := store.NewStore(dir)
	canonical, err := reopened.Drafts.LoadDraft(1)
	if err != nil {
		t.Fatalf("LoadDraft after reopen: %v", err)
	}
	if canonical != draft {
		t.Fatalf("canonical draft did not retain edit: got=%q want=%q", canonical, draft)
	}
}

func TestEditChapterAdaptationInvalidatesBothChecks(t *testing.T) {
	s := newAdaptationToolStore(t)
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, "他握紧了拳头。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	raw, err := NewEditChapterTool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1,"old_string":"握紧","new_string":"松开"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	next, _ := payload["next_step"].(string)
	for _, want := range []string{"旧 check_consistency/check_adaptation 已失效", "重新调用", "同一版草稿", "commit_chapter"} {
		if !strings.Contains(next, want) {
			t.Fatalf("next_step missing %q: %q", want, next)
		}
	}
}

// TestEditChapterSeedsFromFinalChapter drafts 不存在但 chapters 有 → 自动从 chapters 播种。
func TestEditChapterSeedsFromFinalChapter(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	// 模拟第 3 章已提交且进入打磨队列
	original := "风从窗缝里钻进来，带着潮湿的泥土气味。"
	if err := s.Drafts.SaveFinalChapter(3, original); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(3, len([]rune(original)), "mystery", "quest"); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := s.Progress.SetPendingRewrites([]int{3}, "测试打磨"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := s.Progress.SetFlow(domain.FlowPolishing); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}

	tool := NewEditChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    3,
		"old_string": "潮湿的泥土气味",
		"new_string": "泥土和铁锈混杂的气味",
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// drafts 应被播种且包含新文本
	draft, err := s.Drafts.LoadDraft(3)
	if err != nil {
		t.Fatalf("LoadDraft: %v", err)
	}
	if !strings.Contains(draft, "泥土和铁锈混杂的气味") {
		t.Fatalf("expected draft seeded + edited, got %q", draft)
	}

	// chapters 保持原样（edit_chapter 不碰终稿）
	final, err := s.Drafts.LoadChapterText(3)
	if err != nil {
		t.Fatalf("LoadChapterText: %v", err)
	}
	if final != original {
		t.Fatalf("final chapter must stay untouched, got %q", final)
	}
}

// TestEditChapterRejectsCompletedWithoutQueue 已完成且不在重写队列中 → 拒绝。
func TestEditChapterRejectsCompletedWithoutQueue(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	original := "第二章原始正文。"
	if err := s.Drafts.SaveDraft(2, original); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(2, original); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(2, len([]rune(original)), "mystery", "quest"); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}

	tool := NewEditChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"old_string": "原始正文",
		"new_string": "篡改内容",
	})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected rejection for completed chapter not in PendingRewrites")
	}
	if !errors.Is(err, errs.ErrToolPrecondition) {
		t.Fatalf("expected ErrToolPrecondition, got %v", err)
	}
}

// TestEditChapterRejectsAmbiguousMatch 多处匹配且未开 replace_all → 报错。
func TestEditChapterRejectsAmbiguousMatch(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Drafts.SaveDraft(2, "他笑了。她也笑了。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewEditChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"old_string": "笑了",
		"new_string": "沉默了",
	})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected rejection for ambiguous match")
	}
}

// TestEditChapterReplaceAll replace_all=true 时所有匹配均被替换。
func TestEditChapterReplaceAll(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Drafts.SaveDraft(2, "他笑了。她也笑了。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewEditChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":     2,
		"old_string":  "笑了",
		"new_string":  "沉默了",
		"replace_all": true,
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got, _ := s.Drafts.LoadDraft(2)
	if strings.Contains(got, "笑了") {
		t.Fatalf("all occurrences should be replaced, got %q", got)
	}
	if strings.Count(got, "沉默了") != 2 {
		t.Fatalf("expected 2 replacements, got %q", got)
	}
}

// TestEditChapterRejectsEmptyOldString 空 old_string → 参数非法。
func TestEditChapterRejectsEmptyOldString(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewEditChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"old_string": "",
		"new_string": "xxx",
	})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected rejection for empty old_string")
	}
	if !errors.Is(err, errs.ErrToolArgs) {
		t.Fatalf("expected ErrToolArgs, got %v", err)
	}
}

// TestEditChapterRejectsNoDraftNoFinal drafts 与 chapters 都不存在 → 报错提示先 draft_chapter。
func TestEditChapterRejectsNoDraftNoFinal(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewEditChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    5,
		"old_string": "任何",
		"new_string": "替换",
	})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected rejection when neither draft nor chapter exists")
	}
	if !errors.Is(err, errs.ErrToolPrecondition) {
		t.Fatalf("expected ErrToolPrecondition, got %v", err)
	}
}

// TestEditChapterWorksWithCommitValidation 整条链路：edit_chapter → commit_chapter 成功 drain 队列。
// 验证新工具与 commit_chapter 的 drafts≠chapters 硬校验配合良好。
func TestEditChapterWorksWithCommitValidation(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	original := "风从窗缝里钻进来，带着潮湿的泥土气味。"
	if err := s.Drafts.SaveDraft(2, original); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(2, original); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(2, len([]rune(original)), "mystery", "quest"); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := s.Progress.SetPendingRewrites([]int{2}, "打磨"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := s.Progress.SetFlow(domain.FlowPolishing); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}

	editTool := NewEditChapterTool(s)
	editArgs, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"old_string": "潮湿的泥土气味",
		"new_string": "泥土和铁锈混杂的气味",
	})
	if _, err := editTool.Execute(context.Background(), editArgs); err != nil {
		t.Fatalf("edit_chapter: %v", err)
	}

	commitTool := NewCommitChapterTool(s)
	commitArgs, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"summary":    "打磨后摘要",
		"characters": []string{"主角"},
		"key_events": []string{"完成打磨"},
	})
	if _, err := commitTool.Execute(context.Background(), commitArgs); err != nil {
		t.Fatalf("commit_chapter after edit: %v", err)
	}

	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if len(progress.PendingRewrites) != 0 {
		t.Fatalf("expected queue drained, got %v", progress.PendingRewrites)
	}
}

func TestEditChapterAllowsExactRepairWhenDeAIReportHasMultipleKinds(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 2); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	content := "# 第一章\n\n林逸飞没有回答——门外有人敲了一下。\n\n林逸飞没有回答——桌上的杯子晃了晃。\n\n林逸飞没有回答——吴宇申把文件推过来。\n\n林逸飞没有回答——窗帘动了一下。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if _, err := NewCheckDeAITool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatalf("check_de_ai: %v", err)
	}
	_, err := NewEditChapterTool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1,"old_string":"林逸飞没有回答——门外有人敲了一下。","new_string":"门外传来两下轻敲，林逸飞把视线移向门缝。"}`))
	if err != nil {
		t.Fatalf("expected exact repair to remain available, got %v", err)
	}
}
