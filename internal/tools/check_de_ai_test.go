package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func recordCurrentConsistency(t testing.TB, s *store.Store, chapter int) {
	t.Helper()
	if _, err := s.Checkpoints.AppendArtifact(
		domain.ChapterScope(chapter), "consistency_check",
		fmt.Sprintf("drafts/%02d.draft.md", chapter),
	); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDeAIRecordsDraftDigestAndRejectsUncleanProse(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveDraft(1, "# 第一章\n\n## 一\n然后他没有说话——不是因为不想说，而是不能说——像一盏灯。\n"); err != nil {
		t.Fatal(err)
	}
	recordCurrentConsistency(t, s, 1)
	tool := NewCheckDeAITool(s)
	data, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Passed     bool `json:"passed"`
		RepairPlan struct {
			Mode    string `json:"mode"`
			Batches []struct {
				ID string `json:"id"`
			} `json:"batches"`
		} `json:"repair_plan"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Passed {
		t.Fatalf("expected unclean prose to require repair: %s", data)
	}
	if result.RepairPlan.Mode != "batched" || len(result.RepairPlan.Batches) == 0 {
		t.Fatalf("expected a batched repair plan: %s", data)
	}
	audit, err := s.DeAI.LoadAudit(1)
	if err != nil || audit == nil || audit.DraftSHA256 == "" {
		t.Fatalf("audit = %+v, err=%v", audit, err)
	}
	if cp := s.Checkpoints.LatestByStep(domain.ChapterScope(1), "de_ai_check"); cp == nil {
		t.Fatal("expected de_ai_check checkpoint")
	}
}

func TestCheckDeAIRequiresCurrentConsistencyReceipt(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveDraft(1, "# 第一章\n\n机场初见。\n"); err != nil {
		t.Fatal(err)
	}
	tool := NewCheckDeAITool(s)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err == nil {
		t.Fatal("expected missing consistency receipt to block de-AI")
	}
	if _, err := s.Checkpoints.AppendArtifact(domain.ChapterScope(1), "consistency_check", "drafts/01.draft.md"); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveDraft(1, "# 第一章\n\n商业晚宴初见。\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err == nil {
		t.Fatal("expected stale consistency receipt to block de-AI")
	}
}
