package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestContextToolInjectsStyleStats(t *testing.T) {
	dir := testStoreDir(t)
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	progress := &domain.Progress{TotalChapters: 10}
	body := "# 第N章\n他不是迟疑，而是恐惧。沉默了几息。像一道光。\n夜色落下。\n他走了。"
	for ch := 1; ch <= 6; ch++ {
		if err := st.Drafts.SaveFinalChapter(ch, body); err != nil {
			t.Fatalf("SaveFinalChapter: %v", err)
		}
		progress.CompletedChapters = append(progress.CompletedChapters, ch)
	}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	tool := NewContextTool(st, References{}, "default")
	args, _ := json.Marshal(map[string]any{"chapter": 7})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Episodic map[string]json.RawMessage `json:"episodic_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	statsRaw, ok := payload.Episodic["style_stats"]
	if !ok {
		t.Fatalf("expected episodic_memory.style_stats, got keys %v", keysOf(payload.Episodic))
	}
	var stats struct {
		Chapters int `json:"chapters"`
		Patterns []struct {
			Name  string `json:"name"`
			Total int    `json:"total"`
		} `json:"patterns"`
	}
	if err := json.Unmarshal(statsRaw, &stats); err != nil {
		t.Fatalf("Unmarshal stats: %v", err)
	}
	if stats.Chapters != 6 || len(stats.Patterns) == 0 {
		t.Errorf("stats content: %+v", stats)
	}
	if usage, ok := payload.Episodic["_usage"]; !ok || len(usage) == 0 {
		t.Error("expected episodic_memory._usage annotation")
	}
}

func TestContextToolInjectsWordBudget(t *testing.T) {
	dir := testStoreDir(t)
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 2); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	budget, _ := domain.NewWordBudgetFromTarget(10000, domain.WordBudgetSourcePrompt)
	planned := budget.WithPlannedChapters(2)
	if err := st.RunMeta.SetWordBudget(&planned); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}

	tool := NewContextTool(st, References{}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	working, ok := result["working_memory"].(map[string]any)
	if !ok {
		t.Fatalf("working_memory missing: %+v", result)
	}
	wordBudget, ok := working["word_budget"].(map[string]any)
	if !ok {
		t.Fatalf("word_budget missing: %+v", working)
	}
	target := wordBudget["target"].(map[string]any)
	current := wordBudget["current_chapter"].(map[string]any)
	if got := int(target["target_total_words"].(float64)); got != 10000 {
		t.Fatalf("target_total_words = %d, want 10000", got)
	}
	if got := int(current["recommended_min_words"].(float64)); got != 4500 {
		t.Fatalf("recommended_min_words = %d, want 4500", got)
	}
	if got := int(current["recommended_max_words"].(float64)); got != 5500 {
		t.Fatalf("recommended_max_words = %d, want 5500", got)
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestContextToolReportsWarningsForCorruptedState(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "outline.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("write outline.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta", "progress.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("write progress.json: %v", err)
	}

	tool := NewContextTool(store, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Warnings []string `json:"_warnings"`
		Summary  string   `json:"_loading_summary"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Warnings) == 0 {
		t.Fatal("expected context warnings for corrupted files")
	}
	if !containsWarning(payload.Warnings, "outline") {
		t.Fatalf("expected outline warning, got %v", payload.Warnings)
	}
	if !containsWarning(payload.Warnings, "progress") {
		t.Fatalf("expected progress warning, got %v", payload.Warnings)
	}
	if !strings.Contains(payload.Summary, "告警:") {
		t.Fatalf("expected loading summary to contain warning count, got %q", payload.Summary)
	}
}

func containsWarning(warnings []string, key string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, key) {
			return true
		}
	}
	return false
}

func TestContextToolReadsRevisedFormalChapterOutline(t *testing.T) {
	dir := testStoreDir(t)
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "Harbor Ledger", CoreEvent: "A ferry ledger exposes a tide schedule.", Hook: "A locked bell rings.", Scenes: []string{"Inspect the ferry", "Decode the ledger"}},
		{Chapter: 2, Title: "Old Observatory", CoreEvent: "The cast follows an obsolete signal.", Hook: "The lens goes dark.", Scenes: []string{"Climb the dome", "Test the old lens"}},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, CurrentChapter: 2, TotalChapters: 2}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	revised := domain.OutlineEntry{
		Chapter:   2,
		Title:     "Revised Observatory",
		CoreEvent: "A repaired telescope proves the signal was forged before sunrise.",
		Hook:      "The lens reveals a second moon.",
		Scenes:    []string{"Repair the telescope", "Expose the forged signal"},
	}
	if err := st.ReviseChapterOutline(2, revised); err != nil {
		t.Fatalf("ReviseChapterOutline: %v", err)
	}

	tool := NewContextTool(st, References{}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":2}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	text := string(raw)
	for _, expected := range []string{"Revised Observatory", "forged before sunrise", "Expose the forged signal"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("revised writer context missing %q: %s", expected, text)
		}
	}
	if strings.Contains(text, "Old Observatory") || strings.Contains(text, "obsolete signal") {
		t.Fatalf("writer context still contains stale formal outline: %s", text)
	}
}

func TestContextToolChapterModeIncludesWorkingAndReferenceFields(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SavePremise(`## 题材和基调
少年成长，偏紧张压迫。

## 题材定位
少年升级流

## 核心冲突
主角必须在宗门竞争中活下来。

## 主角目标
进入内门。

## 终局方向
成为真正的执棋者。

## 写作禁区
不提前揭露师尊真相。

## 差异化卖点
弱者逆袭。

## 差异化钩子
每阶段都要用更高代价换成长。

## 核心兑现承诺
持续兑现危机与突破。

## 故事引擎
试炼、资源争夺与身份升级共同推进。

## 中段转折
主角被迫转向另一条修行路线。
`); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "入门", CoreEvent: "主角进入宗门", Scenes: []string{"拜师", "立誓"}},
		{Chapter: 2, Title: "试炼", CoreEvent: "参加外门试炼", Scenes: []string{"集合", "出发"}},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Characters.Save([]domain.Character{
		{Name: "林砚", Role: "主角", Description: "少年修士", Arc: "成长", Traits: []string{"冷静"}},
	}); err != nil {
		t.Fatalf("SaveCharacters: %v", err)
	}
	if err := s.World.SaveWorldRules([]domain.WorldRule{
		{Category: "magic", Rule: "灵气可以炼化", Boundary: "凡人不可直接驾驭"},
	}); err != nil {
		t.Fatalf("SaveWorldRules: %v", err)
	}
	if err := s.Progress.Init("test", 2); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Summaries.SaveSummary(domain.ChapterSummary{
		Chapter:    1,
		Summary:    "主角拜入宗门，确立目标。",
		Characters: []string{"林砚"},
		KeyEvents:  []string{"拜师"},
	}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(1, "第一章正文结尾，留下试炼悬念。"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Drafts.SaveChapterPlan(domain.ChapterPlan{
		Chapter: 2,
		Title:   "试炼",
		Goal:    "通过第一关",
		Contract: domain.ChapterContract{
			RequiredBeats:    []string{"必须让主角通过第一关", "必须埋下内门试炼邀请"},
			ForbiddenMoves:   []string{"不能提前揭露师尊真实身份"},
			ContinuityChecks: []string{"主角左臂旧伤仍未痊愈"},
			EvaluationFocus:  []string{"重点检查试炼节奏是否拖沓"},
		},
	}); err != nil {
		t.Fatalf("SaveChapterPlan: %v", err)
	}
	if err := s.World.SaveStyleRules(domain.WritingStyleRules{
		Volume: 1,
		Arc:    1,
		Prose:  []string{"叙述保持克制"},
	}); err != nil {
		t.Fatalf("SaveStyleRules: %v", err)
	}
	if err := s.RunMeta.SetPlanningTier(domain.PlanningTierLong); err != nil {
		t.Fatalf("SetPlanningTier: %v", err)
	}

	tool := NewContextTool(s, References{
		Consistency:      "一致性检查",
		HookTechniques:   "钩子技巧",
		QualityChecklist: "质量清单",
	}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for _, key := range []string{
		"premise",
		"premise_sections",
		"premise_structure",
		"outline",
		"world_rules",
		"memory_policy",
		"planning_tier",
		"working_memory",
		"episodic_memory",
		"reference_pack",
		"current_chapter_outline",
		"chapter_contract",
		"style_rules",
		"references",
	} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("expected key %q in chapter context", key)
		}
	}
	working, ok := payload["working_memory"].(map[string]any)
	if !ok {
		t.Fatal("expected working_memory object")
	}
	for _, key := range []string{"recent_summaries", "chapter_plan", "previous_tail"} {
		if _, ok := working[key]; !ok {
			t.Fatalf("expected working_memory.%s in chapter context", key)
		}
	}
}

func TestContextToolInjectsWordBudgetForArchitectAndWriter(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("budget", 5); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(1, 800, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	budget := domain.NewWordBudget(5000, "test").WithPlannedChapters(5)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	for name, chapter := range map[string]int{"architect": 0, "writer": 2} {
		args, err := json.Marshal(map[string]any{"chapter": chapter})
		if err != nil {
			t.Fatalf("[%s] Marshal: %v", name, err)
		}
		result, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("[%s] Execute: %v", name, err)
		}
		var payload struct {
			Working map[string]json.RawMessage `json:"working_memory"`
		}
		if err := json.Unmarshal(result, &payload); err != nil {
			t.Fatalf("[%s] Unmarshal: %v", name, err)
		}
		raw, ok := payload.Working["word_budget"]
		if !ok {
			t.Fatalf("[%s] expected working_memory.word_budget", name)
		}
		var got struct {
			Target struct {
				TargetTotalWords int `json:"target_total_words"`
				PlannedChapters  int `json:"planned_chapters"`
			} `json:"target"`
			CurrentChapter *struct {
				Chapter             int `json:"chapter"`
				RecommendedMinWords int `json:"recommended_min_words"`
				RecommendedMaxWords int `json:"recommended_max_words"`
			} `json:"current_chapter"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("[%s] Unmarshal word budget: %v", name, err)
		}
		if got.Target.TargetTotalWords != 5000 || got.Target.PlannedChapters != 5 {
			t.Fatalf("[%s] unexpected word budget: %+v", name, got)
		}
		if chapter > 0 && (got.CurrentChapter == nil || got.CurrentChapter.Chapter != chapter || got.CurrentChapter.RecommendedMinWords <= 0 || got.CurrentChapter.RecommendedMaxWords <= got.CurrentChapter.RecommendedMinWords) {
			t.Fatalf("[%s] unexpected current chapter budget: %+v", name, got.CurrentChapter)
		}
	}
}

func TestContextToolArchitectModeIncludesPlanningAndFoundation(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SavePremise(`## 题材和基调
群像冒险，偏冷峻史诗。

## 题材定位
群像长篇冒险

## 核心冲突
众人必须在不断失控的旧秩序中寻找新秩序。

## 主角目标
抵达真相核心。

## 终局方向
揭开古老真相并重建秩序。

## 写作禁区
不靠天降设定收尾。

## 差异化卖点
群像关系推进。

## 差异化钩子
每卷都改变队伍关系结构。

## 核心兑现承诺
持续提供发现、牺牲与选择。

## 故事引擎
旅途推进、真相调查与队伍关系共同驱动。

## 关系/成长主线
队伍从互不信任走向分裂再重组。

## 升级路径
从地方事件走向世界级危机。

## 中期转向
真相并非敌人，而是秩序本身有问题。

## 终局命题
秩序应由谁定义。
`); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "起点", CoreEvent: "旅途开始"},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Characters.Save([]domain.Character{
		{Name: "沈曜", Role: "主角", Description: "流浪剑客", Arc: "寻找真相", Traits: []string{"敏锐"}},
	}); err != nil {
		t.Fatalf("SaveCharacters: %v", err)
	}
	if err := s.World.SaveWorldRules([]domain.WorldRule{
		{Category: "society", Rule: "城邦林立", Boundary: "皇权不可直辖边地"},
	}); err != nil {
		t.Fatalf("SaveWorldRules: %v", err)
	}
	if err := s.Outline.SaveLayeredOutline([]domain.VolumeOutline{
		{
			Index: 1, Title: "第一卷", Theme: "踏上旅途",
			Arcs: []domain.ArcOutline{
				{Index: 1, Title: "启程", Goal: "建立队伍", Chapters: []domain.OutlineEntry{{Chapter: 1, Title: "起点"}}},
				{Index: 2, Title: "迷雾", Goal: "逼近秘密", EstimatedChapters: 5},
			},
		},
	}); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := s.Outline.SaveCompass(domain.StoryCompass{
		EndingDirection: "揭开古老真相",
		EstimatedScale:  "预计 3 卷",
	}); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}
	if err := s.World.SaveStyleRules(domain.WritingStyleRules{
		Volume: 1,
		Arc:    1,
		Prose:  []string{"保持冷峻节制"},
	}); err != nil {
		t.Fatalf("SaveStyleRules: %v", err)
	}
	if err := s.RunMeta.SetPlanningTier(domain.PlanningTierLong); err != nil {
		t.Fatalf("SetPlanningTier: %v", err)
	}

	tool := NewContextTool(s, References{
		OutlineTemplate:   "大纲模板",
		CharacterTemplate: "角色模板",
		LongformPlanning:  "长篇规划",
	}, "default")
	args, err := json.Marshal(map[string]any{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for _, key := range []string{
		"memory_policy",
		"planning_tier",
		"planning_memory",
		"foundation_memory",
		"reference_pack",
		"premise_sections",
		"premise_structure",
		"characters",
		"layered_outline",
		"skeleton_arcs",
		"compass",
		"style_rules",
		"references",
		"foundation_status",
	} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("expected key %q in architect context", key)
		}
	}
}

func TestContextToolSelectedMemoryRecallsStoryThreadsAndReviewLessons(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "邀约", CoreEvent: "长老暗中给出内门试炼邀请", Scenes: []string{"密谈", "留下试炼令"}},
		{Chapter: 2, Title: "试炼前夜", CoreEvent: "林砚准备回应内门试炼邀请", Hook: "谁在背后推动这场试炼", Scenes: []string{"整理线索", "决定赴约"}},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init("test", 8); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{
		{ID: "trial_invite", Description: "内门试炼邀请的真实目的", PlantedAt: 1, Status: "planted"},
		{ID: "trial_mastermind", Description: "谁在背后推动这场试炼", PlantedAt: 1, Status: "planted"},
		{ID: "trial_rules", Description: "试炼规则碑文残卷", PlantedAt: 1, Status: "planted"},
		{ID: "outer_disciple", Description: "外门弟子的旧债纠纷", PlantedAt: 1, Status: "planted"},
		{ID: "elder_token", Description: "长老手中令牌的来历", PlantedAt: 1, Status: "planted"},
		{ID: "hidden_gate", Description: "山门背后的隐藏通道", PlantedAt: 1, Status: "planted"},
		{ID: "trial_bet", Description: "试炼盘口的幕后操盘人", PlantedAt: 1, Status: "planted"},
	}); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}
	if err := s.Drafts.SaveChapterPlan(domain.ChapterPlan{
		Chapter: 2,
		Title:   "试炼前夜",
		Goal:    "决定是否回应邀请",
		Contract: domain.ChapterContract{
			PayoffPoints: []string{"回应内门试炼邀请"},
			HookGoal:     "抛出谁在背后推动试炼",
		},
	}); err != nil {
		t.Fatalf("SaveChapterPlan: %v", err)
	}
	if err := s.World.SaveReview(domain.ReviewEntry{
		Chapter:        1,
		Scope:          "chapter",
		Verdict:        "polish",
		Summary:        "主线启动完成，但伏笔不够明确。",
		ContractStatus: "partial",
		ContractMisses: []string{"未明确埋下内门试炼邀请"},
		Issues: []domain.ConsistencyIssue{
			{Type: "hook", Severity: "warning", Description: "章末钩子不够具体"},
		},
	}); err != nil {
		t.Fatalf("SaveReview: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Selected struct {
			StoryThreads  []domain.RecallItem `json:"story_threads"`
			ReviewLessons []domain.RecallItem `json:"review_lessons"`
		} `json:"selected_memory"`
		Summary string `json:"_loading_summary"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Selected.StoryThreads) == 0 {
		t.Fatal("expected story thread recall items")
	}
	if len(payload.Selected.ReviewLessons) == 0 {
		t.Fatal("expected review lesson recall items")
	}
	if !containsRecallSummary(payload.Selected.StoryThreads, "内门试炼邀请") {
		t.Fatalf("expected story thread recall to mention invite, got %+v", payload.Selected.StoryThreads)
	}
	if !containsRecallSummary(payload.Selected.StoryThreads, "推动这场试炼") {
		t.Fatalf("expected story thread recall to mention trial mastermind, got %+v", payload.Selected.StoryThreads)
	}
	if containsRecallSummary(payload.Selected.StoryThreads, "试炼规则碑文残卷") {
		t.Fatalf("expected weak-overlap foreshadow to stay out, got %+v", payload.Selected.StoryThreads)
	}
	if containsRecallSummary(payload.Selected.StoryThreads, "建议回看第") {
		t.Fatalf("expected related_chapters not to be duplicated into story_threads, got %+v", payload.Selected.StoryThreads)
	}
	if !containsRecallSummary(payload.Selected.ReviewLessons, "contract 漏项") {
		t.Fatalf("expected review lesson recall to mention contract miss, got %+v", payload.Selected.ReviewLessons)
	}
	if !strings.Contains(payload.Summary, "线索召回:") || !strings.Contains(payload.Summary, "评审召回:") {
		t.Fatalf("expected loading summary to report selected memory, got %q", payload.Summary)
	}
}

// 久挂未回收的伏笔即使与当前章关键词无关，也应被账龄回填进 story_threads——
// 这正是相关性召回的盲区（独自悬挂太久、却没在本章撞上关键词的那根线）。
// 近期埋下的伏笔（账龄 < 阈值）不应被误标为"未回收"。
func TestContextToolSelectedMemorySurfacesAgingForeshadow(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// 当前章主题与所有伏笔都不沾边，确保相关性召回为空，只剩账龄回填生效。
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 50, Title: "瘟疫", CoreEvent: "林砚在城南医馆救治瘟疫病患", Scenes: []string{"熬药", "封锁街巷"}},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init("test", 60); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	// 6 条满足召回阈值；前两条账龄 ≥30（久挂），后四条账龄 <30（近期）。
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{
		{ID: "ancient_seal", Description: "上古封印的裂隙", PlantedAt: 3, Status: "planted"},
		{ID: "lost_bloodline", Description: "主角失落的血脉来历", PlantedAt: 5, Status: "advanced"},
		{ID: "market_feud", Description: "昨夜集市的口角", PlantedAt: 47, Status: "planted"},
		{ID: "rumor_a", Description: "近日传闻甲", PlantedAt: 48, Status: "planted"},
		{ID: "rumor_b", Description: "近日传闻乙", PlantedAt: 48, Status: "planted"},
		{ID: "rumor_c", Description: "近日传闻丙", PlantedAt: 49, Status: "planted"},
	}); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 50})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Selected struct {
			StoryThreads []domain.RecallItem `json:"story_threads"`
		} `json:"selected_memory"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// 两条久挂伏笔应被回填，且带"未回收"账龄标注。
	if !containsRecallSummary(payload.Selected.StoryThreads, "上古封印的裂隙") {
		t.Fatalf("expected aging foreshadow to surface despite no relevance, got %+v", payload.Selected.StoryThreads)
	}
	if !containsRecallSummary(payload.Selected.StoryThreads, "失落的血脉") {
		t.Fatalf("expected second aging foreshadow to surface, got %+v", payload.Selected.StoryThreads)
	}
	if !containsRecallSummary(payload.Selected.StoryThreads, "未回收") {
		t.Fatalf("expected aging item to carry overdue annotation, got %+v", payload.Selected.StoryThreads)
	}
	// 近期伏笔（账龄 <30 且不相关）不应被回填。
	if containsRecallSummary(payload.Selected.StoryThreads, "昨夜集市的口角") {
		t.Fatalf("recent foreshadow must not be labeled overdue, got %+v", payload.Selected.StoryThreads)
	}
}

func TestContextToolSelectedMemoryIncludesGlobalReviewLessons(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "开端", CoreEvent: "故事开始"},
		{Chapter: 2, Title: "推进", CoreEvent: "主线继续推进"},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init("test", 6); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.World.SaveReview(domain.ReviewEntry{
		Chapter: 1,
		Scope:   "global",
		Verdict: "polish",
		Summary: "全局推进合格，但角色目标表达还不够稳定。",
		Issues: []domain.ConsistencyIssue{
			{Type: "character", Severity: "warning", Description: "主角目标表达不够稳定"},
		},
	}); err != nil {
		t.Fatalf("SaveReview(global): %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Selected struct {
			ReviewLessons []domain.RecallItem `json:"review_lessons"`
		} `json:"selected_memory"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !containsRecallSummary(payload.Selected.ReviewLessons, "主角目标表达不够稳定") {
		t.Fatalf("expected global review lesson to be recalled, got %+v", payload.Selected.ReviewLessons)
	}
}

func TestContextToolInjectsOnlyStructuredActiveAdaptationRules(t *testing.T) {
	plainStore := store.NewStore(testStoreDir(t))
	if err := plainStore.Init(); err != nil {
		t.Fatalf("Init plain store: %v", err)
	}
	if err := plainStore.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "开端", CoreEvent: "故事开始"}}); err != nil {
		t.Fatalf("SaveOutline plain: %v", err)
	}
	if err := plainStore.Progress.Init("plain", 1); err != nil {
		t.Fatalf("Init plain progress: %v", err)
	}
	refs := References{
		AdaptationWriter:                "禁止使用（某某内心独白：...）这类补丁标签。",
		AdaptationEditorPreserveDetails: "preserve_details 审阅：禁止内心独白仅为示意。",
		AdaptationEditorFullRewrite:     "full_rewrite 审阅：禁止搬运原文。",
	}
	plainTool := NewContextTool(plainStore, refs, "default")
	plainArgs, _ := json.Marshal(map[string]any{"chapter": 1})
	plainRaw, err := plainTool.Execute(context.Background(), plainArgs)
	if err != nil {
		t.Fatalf("Execute plain: %v", err)
	}
	var plainPayload struct {
		Working map[string]json.RawMessage `json:"working_memory"`
	}
	if err := json.Unmarshal(plainRaw, &plainPayload); err != nil {
		t.Fatalf("Unmarshal plain: %v", err)
	}
	if _, ok := plainPayload.Working["adaptation_writing_guidance"]; ok {
		t.Fatal("plain writing context must not include adaptation writer guidance")
	}
	if _, ok := plainPayload.Working["adaptation_editor_guidance"]; ok {
		t.Fatal("plain writing context must not include adaptation editor guidance")
	}

	plan := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityChapter,
		ModePolicy:    domain.AdaptationPolicyDetailPreservationWithSplit,
		RewritePolicy: domain.AdaptationRewritePreserveDetails,
		Status:        domain.AdaptationPlanStatusConfirmed,
		Brief:         "禁止使用补丁标签",
		Rules: []domain.AdaptationRule{{
			ID:   "brief-label",
			Kind: domain.AdaptationRuleForbidden,
			Text: "禁止使用补丁标签",
			Mode: domain.AdaptationGranularityChapter,
		}},
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "目标章",
			SourceChapters: []int{1},
			SourceRunes:    100,
			TargetRunes:    100,
			TargetMinRunes: 85,
			TargetMaxRunes: 115,
			RuleIDs:        []string{"brief-label"},
		}},
	}
	adaptStore := newAdaptationToolStoreWithPlan(t, plan, []string{"原文主线事件。"})
	if err := adaptStore.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "目标章", CoreEvent: "改编事件"}}); err != nil {
		t.Fatalf("SaveOutline adapt: %v", err)
	}
	if err := adaptStore.Progress.Init("adapt", 1); err != nil {
		t.Fatalf("Init adapt progress: %v", err)
	}
	adaptTool := NewContextTool(adaptStore, refs, "default")
	adaptRaw, err := adaptTool.Execute(context.Background(), plainArgs)
	if err != nil {
		t.Fatalf("Execute adapt: %v", err)
	}
	var adaptPayload struct {
		AdaptationMode bool                       `json:"adaptation_mode"`
		Working        map[string]json.RawMessage `json:"working_memory"`
	}
	if err := json.Unmarshal(adaptRaw, &adaptPayload); err != nil {
		t.Fatalf("Unmarshal adapt: %v", err)
	}
	if !adaptPayload.AdaptationMode {
		t.Fatal("expected adaptation_mode=true")
	}
	if _, ok := adaptPayload.Working["adaptation_writing_guidance"]; ok {
		t.Fatal("adaptation context must not inject the all-mode writer markdown")
	}
	if _, ok := adaptPayload.Working["adaptation_editor_guidance"]; ok {
		t.Fatal("writer context must not inject editor markdown")
	}
	rawRules, ok := adaptPayload.Working["adaptation_active_rules"]
	if !ok {
		t.Fatal("adaptation context should include task-scoped structured rules")
	}
	var activeRules []domain.AdaptationRule
	if err := json.Unmarshal(rawRules, &activeRules); err != nil {
		t.Fatalf("Unmarshal active rules: %v", err)
	}
	if len(activeRules) != 1 || activeRules[0].ID != "brief-label" {
		t.Fatalf("active rules mismatch: %+v", activeRules)
	}
}

func TestContextToolShowsDisabledWordToleranceForFullRewrite(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity:    domain.AdaptationGranularityArc,
		RewritePolicy:  domain.AdaptationRewritePreserveDetails,
		Status:         domain.AdaptationPlanStatusConfirmed,
		WordTolerance:  0.15,
		TargetMinRunes: 85,
		TargetMaxRunes: 115,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "目标章",
			SourceChapters: []int{1},
			SourceRunes:    100,
			TargetRunes:    100,
			TargetMinRunes: 85,
			TargetMaxRunes: 115,
		}},
	}
	adaptStore := newAdaptationToolStoreWithPlan(t, plan, []string{"原文主线事件。"})
	if err := adaptStore.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "目标章", CoreEvent: "改编事件"}}); err != nil {
		t.Fatalf("SaveOutline adapt: %v", err)
	}
	if err := adaptStore.Progress.Init("adapt", 1); err != nil {
		t.Fatalf("Init adapt progress: %v", err)
	}

	tool := NewContextTool(adaptStore, References{}, "default")
	args, _ := json.Marshal(map[string]any{"chapter": 1})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Working map[string]json.RawMessage `json:"working_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal payload: %v", err)
	}
	var adaptation map[string]any
	if err := json.Unmarshal(payload.Working["adaptation"], &adaptation); err != nil {
		t.Fatalf("Unmarshal adaptation: %v", err)
	}
	if adaptation["word_tolerance"] != "disabled" {
		t.Fatalf("word_tolerance=%v, want disabled", adaptation["word_tolerance"])
	}
}

func TestContextToolClarifiesFreeFullRewriteSourceRefsAreAnchors(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityFree,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Status:        domain.AdaptationPlanStatusConfirmed,
		Brief:         "rewrite_policy_rule=chapter=>preserve_details;arc/free=>full_rewrite\n自由重构结局。",
		WordTolerance: 0.15,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        53,
			Title:          "目标章",
			SourceChapters: []int{17},
			SourceRunes:    100,
			TargetRunes:    2000,
			TargetMinRunes: 1800,
			TargetMaxRunes: 2200,
			SourceRange:    domain.SourceRange{From: 17, To: 17},
		}},
	}
	sourceTexts := make([]string, 17)
	for i := range sourceTexts {
		sourceTexts[i] = "原文主线事件。"
	}
	adaptStore := newAdaptationToolStoreWithPlan(t, plan, sourceTexts)
	if err := adaptStore.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 53, Title: "目标章", CoreEvent: "新剧情推进"}}); err != nil {
		t.Fatalf("SaveOutline adapt: %v", err)
	}
	if err := adaptStore.Progress.Init("adapt", 59); err != nil {
		t.Fatalf("Init adapt progress: %v", err)
	}

	tool := NewContextTool(adaptStore, References{}, "default")
	args, _ := json.Marshal(map[string]any{"chapter": 53})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Working map[string]json.RawMessage `json:"working_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal payload: %v", err)
	}
	var mode struct {
		Granularity               string   `json:"granularity"`
		RewritePolicy             string   `json:"rewrite_policy"`
		SourceReferencePolicy     string   `json:"source_reference_policy"`
		SourceMappingMeaning      string   `json:"source_mapping_meaning"`
		SourceReadInstruction     string   `json:"source_read_instruction"`
		LegacyRewritePolicyNotice string   `json:"legacy_rewrite_policy_notice"`
		PreserveDetailsApplicable bool     `json:"preserve_details_applicable"`
		MustNot                   []string `json:"must_not"`
	}
	if err := json.Unmarshal(payload.Working["adaptation_effective_mode"], &mode); err != nil {
		t.Fatalf("Unmarshal adaptation_effective_mode: %v", err)
	}
	if mode.Granularity != domain.AdaptationGranularityFree ||
		mode.RewritePolicy != domain.AdaptationRewriteFullRewrite ||
		mode.SourceReferencePolicy != "optional_background_anchor" ||
		mode.PreserveDetailsApplicable {
		t.Fatalf("free effective mode mismatch: %+v", mode)
	}
	for _, want := range []string{"不表示目标章对应原著章节", "不要因为 source_chapters/source_range 存在就读取原文", "rewrite_policy_rule"} {
		joined := strings.Join([]string{mode.SourceMappingMeaning, mode.SourceReadInstruction, mode.LegacyRewritePolicyNotice, strings.Join(mode.MustNot, "\n")}, "\n")
		if !strings.Contains(joined, want) {
			t.Fatalf("free effective mode missing %q:\n%+v", want, mode)
		}
	}
}

func TestContextToolKeepsFullForeshadowWhenRecallNotTriggered(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "起势", CoreEvent: "故事起势"},
		{Chapter: 2, Title: "推进", CoreEvent: "继续推进"},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init("test", 4); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{
		{ID: "small_1", Description: "第一条小伏笔", PlantedAt: 1, Status: "planted"},
		{ID: "small_2", Description: "第二条小伏笔", PlantedAt: 1, Status: "planted"},
	}); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	episodic, _ := payload["episodic_memory"].(map[string]any)
	if _, ok := episodic["foreshadow_ledger"]; !ok {
		t.Fatal("expected episodic_memory.foreshadow_ledger to remain when selected recall is not triggered")
	}
	if _, ok := payload["selected_memory"]; ok {
		t.Fatalf("expected no selected_memory for small foreshadow sets, got %+v", payload["selected_memory"])
	}
}

func TestContextToolFallsBackToFullForeshadowWhenSelectionIsTooSparse(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "邀约", CoreEvent: "长老暗中给出内门试炼邀请"},
		{Chapter: 2, Title: "试炼前夜", CoreEvent: "林砚准备回应内门试炼邀请", Scenes: []string{"整理线索", "决定赴约"}},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init("test", 8); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{
		{ID: "trial_invite", Description: "内门试炼邀请的真实目的", PlantedAt: 1, Status: "planted"},
		{ID: "trial_rules", Description: "试炼规则碑文残卷", PlantedAt: 1, Status: "planted"},
		{ID: "outer_disciple", Description: "外门弟子的旧债纠纷", PlantedAt: 1, Status: "planted"},
		{ID: "elder_token", Description: "长老手中令牌的来历", PlantedAt: 1, Status: "planted"},
		{ID: "hidden_gate", Description: "山门背后的隐藏通道", PlantedAt: 1, Status: "planted"},
		{ID: "trial_bet", Description: "试炼盘口的幕后操盘人", PlantedAt: 1, Status: "planted"},
	}); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	episodic, _ := payload["episodic_memory"].(map[string]any)
	if _, ok := episodic["foreshadow_ledger"]; !ok {
		t.Fatal("expected episodic_memory.foreshadow_ledger when selection is too sparse")
	}
	if selected, ok := payload["selected_memory"].(map[string]any); ok {
		if _, exists := selected["story_threads"]; exists {
			t.Fatalf("expected sparse story_threads to fall back to full ledger, got %+v", selected["story_threads"])
		}
	}
}

func containsRecallSummary(items []domain.RecallItem, want string) bool {
	for _, item := range items {
		if strings.Contains(item.Summary, want) {
			return true
		}
	}
	return false
}

func TestContextToolInjectsRewriteBriefForPendingRewriteChapter(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(2, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := s.Progress.SetPendingRewrites([]int{2}, "节奏拖沓，需要压缩前半段"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := s.World.SaveReview(domain.ReviewEntry{
		Chapter: 2,
		Scope:   "chapter",
		Verdict: "rewrite",
		Summary: "前半段铺垫过长，冲突迟迟不出现。",
		Issues: []domain.ConsistencyIssue{
			{Type: "pacing", Severity: "error", Description: "前 2000 字无推进"},
		},
		ContractMisses: []string{"未兑现试炼开场"},
	}); err != nil {
		t.Fatalf("SaveReview: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	brief, ok := payload["rewrite_brief"].(map[string]any)
	if !ok {
		t.Fatalf("expected rewrite_brief in chapter context, got %T", payload["rewrite_brief"])
	}
	if got := brief["reason"]; got != "节奏拖沓，需要压缩前半段" {
		t.Fatalf("expected rewrite reason, got %v", got)
	}
	if got, _ := brief["review_summary"].(string); !strings.Contains(got, "铺垫过长") {
		t.Fatalf("expected review summary from chapter review, got %v", brief["review_summary"])
	}
	if issues, _ := brief["issues"].([]any); len(issues) == 0 {
		t.Fatalf("expected review issues in rewrite_brief, got %v", brief["issues"])
	}
	if misses, _ := brief["contract_misses"].([]any); len(misses) == 0 {
		t.Fatalf("expected contract misses in rewrite_brief, got %v", brief["contract_misses"])
	}
}

func TestContextToolOmitsRewriteBriefForNormalChapter(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := payload["rewrite_brief"]; ok {
		t.Fatal("expected no rewrite_brief for chapter outside PendingRewrites")
	}
}

func TestContextToolDoesNotInjectUserDirectives(t *testing.T) {
	// save_directive 已移除：novel_context 不再注入 working_memory.user_directives，
	// 长期写作要求统一走 user_rules。锁死这条，防止回归。
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	for name, chapter := range map[string]int{"writer": 1, "architect": 0} {
		args, _ := json.Marshal(map[string]any{"chapter": chapter})
		result, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("[%s] Execute: %v", name, err)
		}
		var payload map[string]any
		if err := json.Unmarshal(result, &payload); err != nil {
			t.Fatalf("[%s] Unmarshal: %v", name, err)
		}
		working, ok := payload["working_memory"].(map[string]any)
		if !ok {
			t.Fatalf("[%s] missing working_memory", name)
		}
		if _, exists := working["user_directives"]; exists {
			t.Errorf("[%s] working_memory 不应再有 user_directives（已统一到 user_rules）", name)
		}
		// user_rules 仍应稳定注入
		if _, ok := working["user_rules"].(map[string]any); !ok {
			t.Errorf("[%s] working_memory.user_rules 应稳定注入", name)
		}
	}
}

func TestContextToolLongChapterUsesWindowedOutline(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SavePremise(strings.Repeat("large premise ", 8000)); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	if err := s.Outline.SaveOutline(testOutlineEntries(1567)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init("long", 1567); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.World.SaveForeshadowLedger(testForeshadowEntries(160)); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(raw) > writerChapterContextBudgetBytes {
		t.Fatalf("long chapter context = %d bytes, want <= %d", len(raw), writerChapterContextBudgetBytes)
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := payload["outline"]; ok {
		t.Fatal("long chapter context must not include full outline")
	}
	if _, ok := payload["current_chapter_outline"]; !ok {
		t.Fatal("expected current_chapter_outline")
	}
	nearby, ok := payload["nearby_outline"].([]any)
	if !ok || len(nearby) == 0 {
		t.Fatalf("expected nearby_outline, got %T", payload["nearby_outline"])
	}
	scope, ok := payload["outline_scope"].(map[string]any)
	if !ok {
		t.Fatalf("expected outline_scope, got %T", payload["outline_scope"])
	}
	if scope["mode"] != "windowed" || scope["full_outline_omitted"] != true {
		t.Fatalf("unexpected outline_scope: %+v", scope)
	}
	if got := int(scope["total_chapters"].(float64)); got != 1567 {
		t.Fatalf("total_chapters = %d, want 1567", got)
	}
	if _, ok := payload["arc_outline_compact"]; !ok {
		t.Fatal("flat long outline should expose arc_outline_compact fallback")
	}
	if _, ok := payload["_trimmed"]; ok {
		t.Fatalf("long chapter context should be source-bounded without hard trimming, got %v", payload["_trimmed"])
	}
}

func TestContextToolLongChapterDoesNotGrowWithProgressHistory(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SavePremise(strings.Repeat("large premise section ", 5000)); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	outline := testOutlineEntries(117)
	if err := s.Outline.SaveOutline(outline); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1,
		Title: "Long Volume",
		Theme: strings.Repeat("volume theme ", 100),
		Arcs: []domain.ArcOutline{{
			Index:    1,
			Title:    "Long Arc",
			Goal:     strings.Repeat("arc goal ", 100),
			Chapters: outline,
		}},
	}}); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	for ch := 1; ch <= 100; ch++ {
		body := fmt.Sprintf("# Chapter %d\n%s", ch, strings.Repeat("正文段落。", 80))
		if err := s.Drafts.SaveFinalChapter(ch, body); err != nil {
			t.Fatalf("SaveFinalChapter %d: %v", ch, err)
		}
		if err := s.Summaries.SaveSummary(domain.ChapterSummary{
			Chapter:    ch,
			Summary:    strings.Repeat(fmt.Sprintf("summary %d ", ch), 80),
			Characters: []string{"A", "B", "C"},
			KeyEvents:  []string{strings.Repeat("event ", 60)},
		}); err != nil {
			t.Fatalf("SaveSummary %d: %v", ch, err)
		}
	}
	completed := make([]int, 100)
	strands := make([]string, 100)
	hooks := make([]string, 100)
	for i := 0; i < 100; i++ {
		completed[i] = i + 1
		strands[i] = fmt.Sprintf("strand-%03d", i+1)
		hooks[i] = fmt.Sprintf("hook-%03d", i+1)
	}
	if err := s.Progress.Save(&domain.Progress{
		NovelName:         "long-progress",
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		TotalChapters:     117,
		CurrentChapter:    101,
		InProgressChapter: 101,
		CompletedChapters: completed,
		StrandHistory:     strands,
		HookHistory:       hooks,
		Layered:           true,
		CurrentVolume:     1,
		CurrentArc:        1,
		ChapterWordCounts: map[int]int{100: 3200},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	if err := s.World.SaveForeshadowLedger(testForeshadowEntries(240)); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}
	if err := s.World.SaveRelationships(testRelationshipEntries(160)); err != nil {
		t.Fatalf("SaveRelationships: %v", err)
	}
	if err := s.World.AppendStateChanges(testStateChanges(160)); err != nil {
		t.Fatalf("AppendStateChanges: %v", err)
	}
	if err := s.World.SaveWorldRules(testWorldRules(120)); err != nil {
		t.Fatalf("SaveWorldRules: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":101}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(raw) > writerChapterContextBudgetBytes {
		t.Fatalf("chapter context after long progress = %d bytes, want <= %d", len(raw), writerChapterContextBudgetBytes)
	}

	var payload struct {
		Trimmed any `json:"_trimmed"`
		Working struct {
			Checkpoint struct {
				StrandHistory []string `json:"strand_history_recent"`
				HookHistory   []string `json:"hook_history_recent"`
				StrandTotal   int      `json:"strand_history_total"`
				HookTotal     int      `json:"hook_history_total"`
			} `json:"checkpoint"`
		} `json:"working_memory"`
		Episodic struct {
			Foreshadow    []domain.ForeshadowEntry   `json:"foreshadow_ledger"`
			Relationships []domain.RelationshipEntry `json:"relationship_state"`
			StateChanges  []domain.StateChange       `json:"recent_state_changes"`
		} `json:"episodic_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Trimmed != nil {
		t.Fatalf("context should not rely on hard trimming, got %v", payload.Trimmed)
	}
	if len(payload.Working.Checkpoint.StrandHistory) > maxContextHistoryItems ||
		len(payload.Working.Checkpoint.HookHistory) > maxContextHistoryItems ||
		payload.Working.Checkpoint.StrandTotal != 100 ||
		payload.Working.Checkpoint.HookTotal != 100 {
		t.Fatalf("checkpoint history not bounded: %+v", payload.Working.Checkpoint)
	}
	if len(payload.Episodic.Foreshadow) > maxContextForeshadowEntries {
		t.Fatalf("foreshadow ledger length = %d", len(payload.Episodic.Foreshadow))
	}
	if len(payload.Episodic.Relationships) > maxContextRelationships {
		t.Fatalf("relationships length = %d", len(payload.Episodic.Relationships))
	}
	if len(payload.Episodic.StateChanges) > maxContextStateChanges {
		t.Fatalf("state changes length = %d", len(payload.Episodic.StateChanges))
	}
}

func TestContextToolOutlineRangeScopeReturnsRequestedChapters(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline(testOutlineEntries(100)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"scope":"outline_range","from":20,"to":30}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Outline []domain.OutlineEntry `json:"outline"`
		Scope   struct {
			Mode             string `json:"mode"`
			From             int    `json:"from"`
			To               int    `json:"to"`
			ReturnedChapters int    `json:"returned_chapters"`
			TotalChapters    int    `json:"total_chapters"`
		} `json:"outline_scope"`
		Working map[string]any `json:"working_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Outline) != 11 {
		t.Fatalf("outline range length = %d, want 11", len(payload.Outline))
	}
	if payload.Outline[0].Chapter != 20 || payload.Outline[len(payload.Outline)-1].Chapter != 30 {
		t.Fatalf("unexpected chapter range: %+v", payload.Outline)
	}
	if payload.Scope.Mode != "outline_range" || payload.Scope.From != 20 || payload.Scope.To != 30 ||
		payload.Scope.ReturnedChapters != 11 || payload.Scope.TotalChapters != 100 {
		t.Fatalf("unexpected outline_scope: %+v", payload.Scope)
	}
	if payload.Working != nil {
		t.Fatalf("outline_range should not include working_memory, got %+v", payload.Working)
	}
}

func TestContextToolSummaryScopeReturnsCompactEvidenceAndMissingChapters(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, summary := range []domain.ChapterSummary{
		{Chapter: 1, Summary: "发现天才", Characters: []string{"甲"}, KeyEvents: []string{"相遇"}},
		{Chapter: 3, Summary: "确认目标", Characters: []string{"甲", "乙"}, KeyEvents: []string{"结盟"}},
	} {
		if err := s.Summaries.SaveSummary(summary); err != nil {
			t.Fatalf("SaveSummary: %v", err)
		}
	}
	if err := s.World.SaveReview(domain.ReviewEntry{Chapter: 3, Scope: "arc", Verdict: "accept", Summary: "弧评审"}); err != nil {
		t.Fatalf("SaveReview: %v", err)
	}
	if err := s.World.SaveTimeline([]domain.TimelineEvent{{Chapter: 2, Event: "转折"}, {Chapter: 8, Event: "范围外"}}); err != nil {
		t.Fatalf("SaveTimeline: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"scope":"summary","from":1,"to":3}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Summaries []domain.ChapterSummary `json:"chapter_summaries"`
		Review    *domain.ReviewEntry     `json:"arc_review"`
		Timeline  []domain.TimelineEvent  `json:"timeline"`
		Evidence  struct {
			Complete bool  `json:"complete"`
			Missing  []int `json:"missing_summary_chapters"`
		} `json:"summary_evidence"`
		Working map[string]any `json:"working_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Summaries) != 2 || payload.Review == nil || payload.Review.Scope != "arc" {
		t.Fatalf("unexpected summary evidence: summaries=%+v review=%+v", payload.Summaries, payload.Review)
	}
	if payload.Evidence.Complete || len(payload.Evidence.Missing) != 1 || payload.Evidence.Missing[0] != 2 {
		t.Fatalf("unexpected completeness: %+v", payload.Evidence)
	}
	if len(payload.Timeline) != 1 || payload.Timeline[0].Chapter != 2 {
		t.Fatalf("timeline was not range-filtered: %+v", payload.Timeline)
	}
	if payload.Working != nil {
		t.Fatalf("summary scope should not include the full writing context: %+v", payload.Working)
	}
}

func TestContextToolSummaryScopeReturnsVolumeArcEvidence(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	volumes := []domain.VolumeOutline{{Index: 1, Arcs: []domain.ArcOutline{{Index: 1}, {Index: 2}}}}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := s.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Summary: "第一弧"}); err != nil {
		t.Fatalf("SaveArcSummary: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"scope":"summary","volume":1}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		ArcSummaries []domain.ArcSummary `json:"arc_summaries"`
		Evidence     struct {
			Complete    bool  `json:"complete"`
			MissingArcs []int `json:"missing_arcs"`
		} `json:"summary_evidence"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.ArcSummaries) != 1 || payload.Evidence.Complete || len(payload.Evidence.MissingArcs) != 1 || payload.Evidence.MissingArcs[0] != 2 {
		t.Fatalf("unexpected volume evidence: %+v", payload)
	}
}

func TestContextToolAdaptationChapterContextIsSourceBounded(t *testing.T) {
	sourceRefs := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	plan := domain.AdaptationPlan{
		Granularity:       domain.AdaptationGranularityArc,
		RewritePolicy:     domain.AdaptationRewriteFullRewrite,
		Status:            domain.AdaptationPlanStatusConfirmed,
		Brief:             strings.Repeat("adaptation brief ", 4000),
		MainlineRules:     []string{strings.Repeat("mainline rule ", 500)},
		RelationshipGoals: []string{strings.Repeat("relationship goal ", 500)},
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:         7,
			Title:           "目标章",
			SourceChapters:  sourceRefs,
			SourceRunes:     40000,
			TargetRunes:     4200,
			TargetMinRunes:  3600,
			TargetMaxRunes:  5000,
			CoverageNote:    strings.Repeat("coverage ", 500),
			PreserveEvents:  []string{strings.Repeat("preserve ", 500)},
			RequiredChanges: []string{strings.Repeat("required ", 500)},
			ForbiddenMoves:  []string{strings.Repeat("forbidden ", 500)},
		}},
	}
	sourceTexts := make([]string, 12)
	for i := range sourceTexts {
		sourceTexts[i] = strings.Repeat("source prose ", 1000)
	}
	adaptStore := newAdaptationToolStoreWithPlan(t, plan, sourceTexts)
	if err := adaptStore.Outline.SaveOutline(testOutlineEntries(117)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := adaptStore.Progress.Save(&domain.Progress{
		NovelName:         "adapt-long",
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		TotalChapters:     117,
		CurrentChapter:    7,
		InProgressChapter: 7,
		CompletedChapters: []int{1, 2, 3, 4, 5, 6},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	if err := adaptStore.Adaptation.SaveSourceReports(testAdaptationSourceReports(24)); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}

	tool := NewContextTool(adaptStore, References{
		AdaptationWriter:            strings.Repeat("writer guidance ", 500),
		AdaptationEditorFullRewrite: strings.Repeat("editor guidance ", 500),
	}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":7}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(raw) > writerChapterContextBudgetBytes {
		t.Fatalf("adaptation context = %d bytes, want <= %d", len(raw), writerChapterContextBudgetBytes)
	}

	var payload struct {
		Trimmed any `json:"_trimmed"`
		Working struct {
			Adaptation struct {
				Brief string `json:"brief"`
			} `json:"adaptation"`
			Reports  []domain.AdaptationSourceReport `json:"source_ref_reports"`
			Contract domain.AdaptationChapterPlan    `json:"adaptation_contract"`
		} `json:"working_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Trimmed != nil {
		t.Fatalf("adaptation context should not rely on hard trimming, got %v", payload.Trimmed)
	}
	if len([]rune(payload.Working.Adaptation.Brief)) > maxContextAdaptationBriefRunes+3 {
		t.Fatalf("adaptation brief was not compacted")
	}
	if len(payload.Working.Reports) > maxContextSourceReports {
		t.Fatalf("source reports length = %d", len(payload.Working.Reports))
	}
	for _, report := range payload.Working.Reports {
		if len([]rune(report.Summary)) > maxContextSourceReportSummaryRunes+3 {
			t.Fatalf("source report summary not compacted: %d", len([]rune(report.Summary)))
		}
		if len(report.KeyEvents) > 8 || len(report.CharacterFacts) > 8 {
			t.Fatalf("source report lists not compacted: %+v", report)
		}
	}
	if len(payload.Working.Contract.RequiredChanges) > maxContextContractItems ||
		len([]rune(payload.Working.Contract.CoverageNote)) > maxContextChapterPlanTextRunes+3 {
		t.Fatalf("adaptation contract not compacted: %+v", payload.Working.Contract)
	}
}

func testOutlineEntries(count int) []domain.OutlineEntry {
	entries := make([]domain.OutlineEntry, 0, count)
	for chapter := 1; chapter <= count; chapter++ {
		entries = append(entries, domain.OutlineEntry{
			Chapter:   chapter,
			Title:     fmt.Sprintf("Chapter %04d", chapter),
			CoreEvent: fmt.Sprintf("event %04d", chapter),
			Hook:      fmt.Sprintf("hook %04d", chapter),
			Scenes:    []string{fmt.Sprintf("scene %04d", chapter)},
		})
	}
	return entries
}

func testForeshadowEntries(count int) []domain.ForeshadowEntry {
	entries := make([]domain.ForeshadowEntry, 0, count)
	for i := 1; i <= count; i++ {
		entries = append(entries, domain.ForeshadowEntry{
			ID:          fmt.Sprintf("thread_%04d", i),
			Description: strings.Repeat(fmt.Sprintf("unrelated thread %04d ", i), 20),
			PlantedAt:   1,
			Status:      "planted",
		})
	}
	return entries
}

func testRelationshipEntries(count int) []domain.RelationshipEntry {
	entries := make([]domain.RelationshipEntry, 0, count)
	for i := 1; i <= count; i++ {
		entries = append(entries, domain.RelationshipEntry{
			CharacterA: "A",
			CharacterB: fmt.Sprintf("B%03d", i),
			Relation:   strings.Repeat(fmt.Sprintf("relationship %03d ", i), 30),
			Chapter:    i,
		})
	}
	return entries
}

func testStateChanges(count int) []domain.StateChange {
	changes := make([]domain.StateChange, 0, count)
	for i := 1; i <= count; i++ {
		changes = append(changes, domain.StateChange{
			Chapter:  max(1, i-60),
			Entity:   fmt.Sprintf("entity-%03d", i),
			Field:    "status",
			OldValue: strings.Repeat("old ", 40),
			NewValue: strings.Repeat("new ", 40),
			Reason:   strings.Repeat("reason ", 40),
		})
	}
	return changes
}

func testWorldRules(count int) []domain.WorldRule {
	rules := make([]domain.WorldRule, 0, count)
	for i := 1; i <= count; i++ {
		rules = append(rules, domain.WorldRule{
			Category: "rule",
			Rule:     strings.Repeat(fmt.Sprintf("world rule %03d ", i), 30),
			Boundary: strings.Repeat("boundary ", 30),
		})
	}
	return rules
}

func testAdaptationSourceReports(count int) []domain.AdaptationSourceReport {
	reports := make([]domain.AdaptationSourceReport, 0, count)
	for i := 1; i <= count; i++ {
		reports = append(reports, domain.AdaptationSourceReport{
			Chapter:        i,
			Title:          fmt.Sprintf("Source %02d", i),
			Summary:        strings.Repeat(fmt.Sprintf("summary %02d ", i), 200),
			Characters:     []string{"A", "B", "C"},
			CharacterFacts: []string{strings.Repeat("fact ", 200)},
			KeyEvents:      []string{strings.Repeat("event ", 200)},
			WorldRules:     []string{strings.Repeat("world ", 200)},
			Timeline: []domain.TimelineEvent{{
				Chapter:    i,
				Time:       strings.Repeat("time ", 50),
				Event:      strings.Repeat("timeline ", 200),
				Characters: []string{"A", "B"},
			}},
			Foreshadow: []domain.ForeshadowUpdate{{
				ID:          fmt.Sprintf("f%02d", i),
				Action:      "plant",
				Description: strings.Repeat("foreshadow ", 200),
			}},
			Relationships: []domain.RelationshipEntry{{
				CharacterA: "A",
				CharacterB: "B",
				Relation:   strings.Repeat("relation ", 200),
				Chapter:    i,
			}},
			StateChanges: []domain.StateChange{{
				Chapter:  i,
				Entity:   "A",
				Field:    "status",
				NewValue: strings.Repeat("new ", 200),
				Reason:   strings.Repeat("reason ", 200),
			}},
		})
	}
	return reports
}
