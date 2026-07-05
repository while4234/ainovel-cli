package agents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

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
