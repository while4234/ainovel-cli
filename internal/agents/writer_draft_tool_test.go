package agents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

func TestWriterContextToolInfersPendingPolishChapter(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 40); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	progress, _ := st.Progress.Load()
	progress.Flow = domain.FlowPolishing
	progress.PendingRewrites = []int{39}
	progress.InProgressChapter = 39
	if err := st.Progress.Save(progress); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	raw, err := newWriterContextTool(tools.NewContextTool(st, tools.References{}, "default"), st).Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload["context_profile"] != "polishing" {
		t.Fatalf("context_profile = %v, want polishing", payload["context_profile"])
	}
	if _, ok := payload["planning_memory"]; ok {
		t.Fatal("writer empty context call must not fall through to planning context")
	}
}

func TestWriterContextToolRejectsFullCrossChapterWorkPackage(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 50); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.StartChapter(41); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	tool := newWriterContextTool(tools.NewContextTool(st, tools.References{}, "default"), st)
	raw, err := tool.Execute(t.Context(), json.RawMessage(`{"chapter":40}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["context_profile"] != "cross_chapter_redirect" || payload["full_context_loaded"] != false {
		t.Fatalf("unexpected cross-chapter payload: %+v", payload)
	}
	if len(raw) >= 2*1024 {
		t.Fatalf("cross-chapter redirect=%d bytes, want compact tool guidance", len(raw))
	}
}

func TestWriterReadChapterToolBoundsPriorContinuityTail(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 50); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.StartChapter(41); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(40, strings.Repeat("前章连续性正文。", 300)); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	tool := newWriterReadChapterTool(tools.NewReadChapterTool(st), st)
	raw, err := tool.Execute(t.Context(), json.RawMessage(`{"chapter":40,"source":"final"}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		ReturnedRunes              int    `json:"returned_runes"`
		Truncated                  bool   `json:"truncated"`
		ContextProfile             string `json:"context_profile"`
		ContinuityEvidenceComplete bool   `json:"continuity_evidence_complete"`
		DoNotRetryForMore          bool   `json:"do_not_retry_for_more"`
		Hint                       string `json:"hint"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Truncated || payload.ReturnedRunes > writerPriorChapterMaxRunes+3 {
		t.Fatalf("prior chapter was not bounded: %+v", payload)
	}
	if payload.ContextProfile != "bounded_prior_continuity_tail" || !payload.ContinuityEvidenceComplete || !payload.DoNotRetryForMore {
		t.Fatalf("prior continuity guidance is incomplete: %+v", payload)
	}
	if !strings.Contains(payload.Hint, "Proceed directly") || strings.Contains(payload.Hint, "increase the limit") {
		t.Fatalf("prior continuity hint can trigger a retry loop: %q", payload.Hint)
	}
}

func TestWriterReadChapterToolInfersCurrentDraftAndSource(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 50); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.StartChapter(41); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	if err := st.Drafts.SaveDraft(41, "current stored draft"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	tool := newWriterReadChapterTool(tools.NewReadChapterTool(st), st)
	raw, err := tool.Execute(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Chapter int    `json:"chapter"`
		Source  string `json:"source"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Chapter != 41 || payload.Source != "draft" || payload.Content != "current stored draft" {
		t.Fatalf("inferred current read=%+v", payload)
	}
}

func TestWriterChapterInferenceToolAddsActiveChapter(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 50); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.StartChapter(41); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	if err := st.Drafts.SaveDraft(41, strings.Repeat("自然叙事句子。", 500)); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	tool := newWriterChapterInferenceTool(tools.NewCheckDeAITool(st), st)
	raw, err := tool.Execute(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Chapter int `json:"chapter"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Chapter != 41 {
		t.Fatalf("inferred chapter=%d, want 41", payload.Chapter)
	}
}

func TestCoordinatorContextToolDefaultsToProgressStatus(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 40); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	raw, err := newCoordinatorContextTool(tools.NewContextTool(st, tools.References{}, "default")).Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := payload["progress_status"]; !ok {
		t.Fatalf("progress_status missing: %+v", payload)
	}
	if _, ok := payload["planning_memory"]; ok {
		t.Fatal("coordinator default context call must not load Architect planning context")
	}
}

func TestWriterDraftChapterToolInfersInProgressChapter(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 4); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.StartChapter(3); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}

	tool := newWriterDraftChapterTool(st)
	raw, err := tool.Execute(context.Background(), writerDraftArgs(t, map[string]any{
		"content": "chapter three prose",
		"mode":    "write",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if result["chapter"].(float64) != 3 {
		t.Fatalf("chapter = %v, want 3", result["chapter"])
	}
	if draft, _ := st.Drafts.LoadDraft(3); draft != "chapter three prose" {
		t.Fatalf("draft chapter 3 = %q", draft)
	}
}

func TestWriterDraftChapterToolDoesNotInferAmbiguousRewriteQueue(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 2); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	for chapter := 1; chapter <= 2; chapter++ {
		if err := st.Progress.MarkChapterComplete(chapter, 1000, "", ""); err != nil {
			t.Fatalf("MarkChapterComplete(%d): %v", chapter, err)
		}
	}
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	progress.Flow = domain.FlowRewriting
	progress.PendingRewrites = []int{1, 2}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	tool := newWriterDraftChapterTool(st)
	_, err = tool.Execute(context.Background(), writerDraftArgs(t, map[string]any{
		"content": "ambiguous rewrite prose",
		"mode":    "write",
	}))
	if err == nil || !strings.Contains(err.Error(), "cannot be inferred") {
		t.Fatalf("expected inference rejection, got %v", err)
	}
}

func TestWriterDraftChapterToolSchemaKeepsContentAndModeRequired(t *testing.T) {
	st := store.NewStore(t.TempDir())
	schema := newWriterDraftChapterTool(st).Schema()
	required := schema["required"].([]string)
	if stringSliceContains(required, "chapter") {
		t.Fatalf("chapter should not be schema-required for writer wrapper: %v", required)
	}
	for _, field := range []string{"content", "mode"} {
		if !stringSliceContains(required, field) {
			t.Fatalf("%s should remain required: %v", field, required)
		}
	}
}

func writerDraftArgs(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return raw
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
