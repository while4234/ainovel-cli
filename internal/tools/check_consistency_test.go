package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/store"
)

func TestCheckConsistencyReturnsCompactSameDraftReceipt(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	draft := strings.Repeat("正文段落。", 1000)
	if err := st.Drafts.SaveDraft(39, draft); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	raw, err := NewCheckConsistencyTool(st).Execute(context.Background(), json.RawMessage(`{"chapter":39}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(raw) > 2048 {
		t.Fatalf("consistency receipt = %d bytes, want <= 2048", len(raw))
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, exists := payload["content"]; exists {
		t.Fatal("consistency receipt must not echo the already-read draft")
	}
	if payload["draft_sha256"] != store.TextSHA256(draft) || payload["reviewed"] != true {
		t.Fatalf("unexpected receipt: %+v", payload)
	}
}
