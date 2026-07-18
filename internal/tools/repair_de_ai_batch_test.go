package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestRepairDeAIBatchAppliesBoundedExactRevisions(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	content := "# 第一章\n\n林逸飞没有回答——门外有人敲了一下。\n\n林逸飞没有回答——桌上的杯子晃了晃。\n\n林逸飞没有回答——吴宇申把文件推过来。\n\n林逸飞没有回答——窗帘动了一下。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCheckDeAITool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}

	result, err := NewRepairDeAIBatchTool(s).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"repairs":[
			{"old_string":"林逸飞没有回答——门外有人敲了一下。","new_string":"门外传来两下轻敲，林逸飞把视线移向门缝。"},
			{"old_string":"林逸飞没有回答——桌上的杯子晃了晃。","new_string":"桌上的杯子被他无意碰得轻响一声，他仍盯着那份文件。"}
		]
	}`))
	if err != nil {
		t.Fatalf("repair batch: %v", err)
	}
	if !strings.Contains(string(result), `"repaired_count":2`) {
		t.Fatalf("result = %s", result)
	}
	if !strings.Contains(string(result), "同一版草稿") {
		t.Fatalf("result must require rechecking one stable draft: %s", result)
	}
	draft, err := s.Drafts.LoadDraft(1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(draft, "林逸飞没有回答——门外有人敲了一下。") || !strings.Contains(draft, "门外传来两下轻敲") {
		t.Fatalf("unexpected repaired draft: %s", draft)
	}
	if checkpoint := s.Checkpoints.LatestByStep(domain.ChapterScope(1), "de_ai_batch_repair"); checkpoint == nil {
		t.Fatal("expected de_ai_batch_repair checkpoint")
	}
}

func TestRepairDeAIBatchDefersCurrentOutOfBudgetDraft(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Save(&domain.Progress{NovelName: "test", Phase: domain.PhaseWriting, TotalChapters: 1, InProgressChapter: 1}); err != nil {
		t.Fatal(err)
	}
	budget := domain.NewWordBudget(100, "test").WithPlannedChapters(1)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatal(err)
	}
	problem := "林逸飞没有回答——门外有人敲了一下。"
	content := strings.Repeat("前文。", 50) + problem +
		"林逸飞没有回答——桌上的杯子晃了晃。" +
		"林逸飞没有回答——吴宇申把文件推过来。" +
		"林逸飞没有回答——窗帘动了一下。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCheckDeAITool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}

	raw, err := NewRepairDeAIBatchTool(s).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"repairs":[{"old_string":"林逸飞没有回答——门外有人敲了一下。","new_string":"门外传来两下轻敲，林逸飞看向门缝。"}]
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Changed        bool `json:"changed"`
		DeferredToHost bool `json:"deferred_to_host"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Changed || !payload.DeferredToHost {
		t.Fatalf("out-of-budget de-AI repair was not deferred: %+v err=%v raw=%s", payload, err, raw)
	}
	if got, loadErr := s.Drafts.LoadDraft(1); loadErr != nil || got != content {
		t.Fatalf("deferred repair changed draft: err=%v", loadErr)
	}
}

func TestRepairDeAIBatchSkipsStaleEntryAndAppliesCurrentMatches(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	content := "# 第一章\n\n门外传来两下轻敲，林逸飞把视线移向门缝。\n\n林逸飞没有回答——桌上的杯子晃了晃。\n\n林逸飞没有回答——吴宇申把文件推过来。\n\n林逸飞没有回答——窗帘动了一下。\n\n林逸飞没有回答——走廊的灯灭了。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCheckDeAITool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}

	result, err := NewRepairDeAIBatchTool(s).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"repairs":[
			{"old_string":"林逸飞没有回答——门外有人敲了一下。","new_string":"门外传来两下轻敲，林逸飞把视线移向门缝。"},
			{"old_string":"林逸飞没有回答——桌上的杯子晃了晃。","new_string":"杯子被他碰得轻响一声，他仍盯着那份文件。"}
		]
	}`))
	if err != nil {
		t.Fatalf("repair batch with one stale entry: %v", err)
	}
	var payload struct {
		Changed             bool   `json:"changed"`
		RepairedCount       int    `json:"repaired_count"`
		SkippedStaleCount   int    `json:"skipped_stale_count"`
		SkippedStaleIndices []int  `json:"skipped_stale_indices"`
		NextStep            string `json:"next_step"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Changed || payload.RepairedCount != 1 || payload.SkippedStaleCount != 1 ||
		len(payload.SkippedStaleIndices) != 1 || payload.SkippedStaleIndices[0] != 0 {
		t.Fatalf("unexpected stale-entry result: %+v", payload)
	}
	if !strings.Contains(payload.NextStep, "不要重放旧批次") || !strings.Contains(payload.NextStep, "立即调用 check_de_ai") {
		t.Fatalf("missing safe recovery guidance: %q", payload.NextStep)
	}
	draft, err := s.Drafts.LoadDraft(1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(draft, "门外传来两下轻敲") || !strings.Contains(draft, "杯子被他碰得轻响一声") {
		t.Fatalf("stale entry prevented valid repair: %s", draft)
	}
}

func TestRepairDeAIBatchTreatsFullyStaleBatchAsNoOp(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	content := "# 第一章\n\n门外传来两下轻敲，林逸飞把视线移向门缝。\n\n林逸飞没有回答——桌上的杯子晃了晃。\n\n林逸飞没有回答——吴宇申把文件推过来。\n\n林逸飞没有回答——窗帘动了一下。\n\n林逸飞没有回答——走廊的灯灭了。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCheckDeAITool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}

	result, err := NewRepairDeAIBatchTool(s).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"repairs":[
			{"old_string":"林逸飞没有回答——门外有人敲了一下。","new_string":"门外传来两下轻敲，林逸飞把视线移向门缝。"}
		]
	}`))
	if err != nil {
		t.Fatalf("fully stale batch must be a recoverable no-op: %v", err)
	}
	var payload struct {
		Changed           bool `json:"changed"`
		RepairedCount     int  `json:"repaired_count"`
		SkippedStaleCount int  `json:"skipped_stale_count"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Changed || payload.RepairedCount != 0 || payload.SkippedStaleCount != 1 {
		t.Fatalf("unexpected fully stale result: %+v", payload)
	}
	draft, err := s.Drafts.LoadDraft(1)
	if err != nil {
		t.Fatal(err)
	}
	if draft != content {
		t.Fatalf("fully stale batch changed draft: %s", draft)
	}
	if checkpoint := s.Checkpoints.LatestByStep(domain.ChapterScope(1), "de_ai_batch_repair"); checkpoint != nil {
		t.Fatal("fully stale batch must not create a repair checkpoint")
	}
}

func TestRepairDeAIBatchRejectsAmbiguousText(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	content := "# 第一章\n\n林逸飞没有回答。\n\n林逸飞没有回答。\n\n林逸飞没有回答。\n\n林逸飞没有回答。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCheckDeAITool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}
	_, err := NewRepairDeAIBatchTool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1,"repairs":[{"old_string":"没有回答","new_string":"抬起头"}]}`))
	if err == nil || !errors.Is(err, errs.ErrToolPrecondition) {
		t.Fatalf("expected exact-match precondition, got %v", err)
	}
}

func TestRepairDeAIBatchRejectsOverlappingPatchesWithoutSaving(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	content := "# 第一章\n\n林逸飞没有回答——门外有人敲了一下。\n\n林逸飞没有回答——桌上的杯子晃了晃。\n\n林逸飞没有回答——吴宇申把文件推过来。\n\n林逸飞没有回答——窗帘动了一下。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCheckDeAITool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}

	_, err := NewRepairDeAIBatchTool(s).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"repairs":[
			{"old_string":"林逸飞没有回答——门外有人敲了一下。","new_string":"门外传来两下轻敲。"},
			{"old_string":"没有回答——门外有人敲了一下","new_string":"抬眼看向门缝"}
		]
	}`))
	if err == nil || !errors.Is(err, errs.ErrToolArgs) {
		t.Fatalf("expected overlapping patch error, got %v", err)
	}
	draft, err := s.Drafts.LoadDraft(1)
	if err != nil {
		t.Fatal(err)
	}
	if draft != content {
		t.Fatalf("overlapping repairs must not persist a partial draft: %s", draft)
	}
}
