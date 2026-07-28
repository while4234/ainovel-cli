package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
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

func TestCheckConsistencyRequiresGroundedEvidenceForEveryPlannedScene(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1,
		Scenes:  []string{"机场初见", "公司入职"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	draft := "# 第一章\n\n机场到达厅里，她拖着白色行李箱从他面前经过。\n\n第二天清晨，他进入维纳斯集团报到。"
	if err := st.Drafts.SaveDraft(1, draft); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	tool := NewCheckConsistencyTool(st)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"scene_checks":[],
		"findings":[]
	}`)); err == nil || !strings.Contains(err.Error(), "scene_checks count") {
		t.Fatalf("expected missing scene evidence to fail, got %v", err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"scene_checks":[
			{"scene":1,"evidence":"机场到达厅里，她拖着白色行李箱","time_and_place_match":true,"pov_match":true,"characters_match":true,"event_order_match":true,"knowledge_match":true,"irreversible_result_match":true},
			{"scene":2,"evidence":"第二天清晨，他进入维纳斯集团报到","time_and_place_match":true,"pov_match":true,"characters_match":true,"event_order_match":true,"knowledge_match":true,"irreversible_result_match":true}
		],
		"findings":[]
	}`)); err != nil {
		t.Fatalf("grounded scene checks should pass: %v", err)
	}
}

func TestCheckConsistencyRejectsInventedSceneEvidence(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1,
		Scenes:  []string{"机场初见"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Drafts.SaveDraft(1, "# 第一章\n\n机场到达厅里，她从他面前经过。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	_, err := NewCheckConsistencyTool(st).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"scene_checks":[
			{"scene":1,"evidence":"苏家商业晚宴上两人第一次相见","time_and_place_match":true,"pov_match":true,"characters_match":true,"event_order_match":true,"knowledge_match":true,"irreversible_result_match":true}
		],
		"findings":[]
	}`))
	if err == nil || !strings.Contains(err.Error(), "exact current-draft quote") {
		t.Fatalf("expected invented evidence to fail, got %v", err)
	}
}
