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

func TestCheckConsistencySchemaPinsActiveChapterSceneCount(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 3); err != nil {
		t.Fatalf("Init progress: %v", err)
	}
	if err := st.Progress.StartChapter(2); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 2,
		Scenes:  []string{"第一场抵达", "第二场试探", "第三场决定", "第四场钩子"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}

	toolSchema := NewCheckConsistencyTool(st).Schema()
	properties, ok := toolSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties=%T", toolSchema["properties"])
	}
	sceneChecks, ok := properties["scene_checks"].(map[string]any)
	if !ok {
		t.Fatalf("scene_checks=%T", properties["scene_checks"])
	}
	if sceneChecks["minItems"] != 4 || sceneChecks["maxItems"] != 4 {
		t.Fatalf("scene count bounds=%v-%v, want 4-4", sceneChecks["minItems"], sceneChecks["maxItems"])
	}
	description, _ := sceneChecks["description"].(string)
	if !strings.Contains(description, "只能提交 4 项") ||
		!strings.Contains(description, "1:第一场抵达") ||
		!strings.Contains(description, "4:第四场钩子") {
		t.Fatalf("dynamic scene contract missing from description: %q", description)
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

func TestCheckConsistencyAcceptsExactEvidenceAcrossParagraphWhitespace(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1,
		Scenes:  []string{"车内决定"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	draft := "# 第一章\n\n滨河路绕完一圈，车没有停。\n\n苏瑾琛靠在后排座椅上，闭着眼。\n"
	if err := st.Drafts.SaveDraft(1, draft); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	_, err := NewCheckConsistencyTool(st).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"scene_checks":[{
			"scene":1,
			"evidence":"滨河路绕完一圈，车没有停。苏瑾琛靠在后排座椅上，闭着眼。",
			"time_and_place_match":true,
			"pov_match":true,
			"characters_match":true,
			"event_order_match":true,
			"knowledge_match":true,
			"irreversible_result_match":true
		}],
		"findings":[]
	}`))
	if err != nil {
		t.Fatalf("paragraph whitespace must not invalidate an otherwise exact quote: %v", err)
	}
}

func TestFindConsistencyEvidenceStillRejectsParaphrase(t *testing.T) {
	content := "滨河路绕完一圈，车没有停。\n\n苏瑾琛靠在后排座椅上，闭着眼。"
	if _, found := findConsistencyEvidence(content, "滨河路绕完后，汽车没有停下。苏瑾琛闭目靠着后座。"); found {
		t.Fatal("semantic paraphrase must not pass exact evidence matching")
	}
}

func TestGroundConsistencyEvidenceCanonicalizesUniqueExactSpan(t *testing.T) {
	content := "林舒然蹲在侧庭院的矮冬青后面，膝盖压在石砖上。"
	offset, grounded, found := groundConsistencyEvidence(
		content,
		"她蹲在侧庭院的矮冬青后面，膝盖压在石砖上",
	)
	if !found {
		t.Fatal("expected the unique verbatim span to ground the evidence")
	}
	if offset <= 0 {
		t.Fatalf("offset = %d, want the grounded span after the character name", offset)
	}
	if grounded != "蹲在侧庭院的矮冬青后面，膝盖压在石砖上" {
		t.Fatalf("grounded evidence = %q", grounded)
	}
	if !strings.Contains(content, grounded) {
		t.Fatalf("grounded evidence is not verbatim draft text: %q", grounded)
	}
}

func TestGroundConsistencyEvidenceRejectsAmbiguousShortAnchor(t *testing.T) {
	content := "她推开房门走进院子。片刻后，她推开房门走进院子。"
	if _, _, found := groundConsistencyEvidence(content, "随后她推开房门走进院子"); found {
		t.Fatal("ambiguous repeated anchor must not be accepted")
	}
}

func TestCheckConsistencyRequiresEvidenceInPlannedSceneOrder(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1,
		Scenes:  []string{"清晨公寓", "午间公司"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	draft := "# 第一章\n\n清晨公寓里，两人准备早餐。\n\n午间公司里，新同事完成报到。"
	if err := st.Drafts.SaveDraft(1, draft); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	_, err := NewCheckConsistencyTool(st).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"scene_checks":[
			{"scene":1,"evidence":"午间公司里，新同事完成报到","time_and_place_match":true,"pov_match":true,"characters_match":true,"event_order_match":true,"knowledge_match":true,"irreversible_result_match":true},
			{"scene":2,"evidence":"清晨公寓里，两人准备早餐","time_and_place_match":true,"pov_match":true,"characters_match":true,"event_order_match":true,"knowledge_match":true,"irreversible_result_match":true}
		],
		"findings":[]
	}`))
	if err == nil || !strings.Contains(err.Error(), "not in planned scene order") {
		t.Fatalf("expected out-of-order evidence to fail, got %v", err)
	}
}

func TestCompactIndexedSceneContractsBoundsErrorContext(t *testing.T) {
	scenes := []string{
		"scene: 1; pov: hero; setting: home; summary: " + strings.Repeat("甲", 300),
		"scene: 2; pov: rival; setting: office; summary: " + strings.Repeat("乙", 300),
	}
	got := compactIndexedSceneContracts(scenes)
	if len([]rune(got)) > 205 {
		t.Fatalf("compact contracts too large: %d runes", len([]rune(got)))
	}
	if !strings.Contains(got, "pov: hero") || !strings.Contains(got, "pov: rival") {
		t.Fatalf("compact contracts lost identity labels: %q", got)
	}
}

func TestCheckConsistencyRecordsMissingPlannedSceneAsBlockingFinding(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Characters.Save([]domain.Character{{
		ID: "lin_shuran", Name: "林舒然",
	}}); err != nil {
		t.Fatalf("Save characters: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1,
		Scenes:  []string{"清晨公寓", "晚间公寓"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Drafts.SaveDraft(1, "# 第一章\n\n清晨公寓里，林舒然准备早餐。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	raw, err := NewCheckConsistencyTool(st).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"scene_checks":[
			{"scene":1,"evidence":"清晨公寓里，林舒然准备早餐","time_and_place_match":true,"pov_match":true,"characters_match":true,"event_order_match":true,"knowledge_match":true,"irreversible_result_match":true},
			{"scene":2,"evidence":"MISSING_FROM_DRAFT","time_and_place_match":false,"pov_match":false,"characters_match":false,"event_order_match":false,"knowledge_match":false,"irreversible_result_match":false}
		],
		"findings":[{
			"type":"arc_beat_miss",
			"severity":"error",
			"character_id":"lin_shuran",
			"scene":"scene 2",
			"evidence":"MISSING_FROM_DRAFT",
			"violated_field":"chapter_contract.scenes[2]",
			"description":"晚间公寓场景缺失",
			"suggestion":"在章末补写晚间公寓复盘与周末采买计划"
		}]
	}`))
	if err != nil {
		t.Fatalf("missing-scene finding should be recorded, got %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result["passed"] != false || result["blocking"] != true {
		t.Fatalf("missing scene must block consistency: %s", raw)
	}
	if st.Checkpoints.LatestByStep(domain.ChapterScope(1), "consistency_check") != nil {
		t.Fatal("blocking missing scene must not create a passing checkpoint")
	}
}

func TestCheckConsistencyMissingMarkerRequiresBlockingFinding(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1,
		Scenes:  []string{"晚间公寓"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Drafts.SaveDraft(1, "# 第一章\n\n当前正文没有晚间场景。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	_, err := NewCheckConsistencyTool(st).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"scene_checks":[
			{"scene":1,"evidence":"MISSING_FROM_DRAFT","time_and_place_match":false,"pov_match":false,"characters_match":false,"event_order_match":false,"knowledge_match":false,"irreversible_result_match":false}
		],
		"findings":[]
	}`))
	if err == nil || !strings.Contains(err.Error(), "add a critical/error finding") {
		t.Fatalf("expected missing marker without finding to fail, got %v", err)
	}
}
