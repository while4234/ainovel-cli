package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// DraftChapterTool 写入整章草稿，替代旧的 write_scene + polish_chapter 流水线。
// Agent 自主决定一次写完还是分批续写。
type DraftChapterTool struct {
	store *store.Store
}

func NewDraftChapterTool(store *store.Store) *DraftChapterTool {
	return &DraftChapterTool{store: store}
}

func (t *DraftChapterTool) Name() string { return "draft_chapter" }
func (t *DraftChapterTool) Description() string {
	return "写入章节正文。mode=write 覆盖写入整章，mode=append 追加到现有草稿（续写/修改）"
}
func (t *DraftChapterTool) Label() string { return "写入章节" }

// 写工具，禁止并发（读-改-写竞态）。
func (t *DraftChapterTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *DraftChapterTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *DraftChapterTool) Schema() map[string]any {
	// mode 标 required 是为了兼容 OpenAI strict tool calling——strict 模式
	// 要求所有 properties 都在 required 列表中。原来的"省略 mode 走 write
	// 默认"行为现在需要模型显式传 mode="write"，Execute 的 default 分支不变。
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号")).Required(),
		schema.Property("content", schema.String("章节正文")).Required(),
		schema.Property("mode", schema.Enum("写入模式", "write", "append")).Required(),
	)
}

// StrictSchema 启用 OpenAI 的 strict tool calling，让模型必须严格遵守
// schema：所有 required 字段必填，arguments 不能"提前 EOT"出现空对象。
// litellm 透传 strict 字段；OpenAI / xAI 等支持的后端会强制执行，其他后端
// 按 HTTP/JSON 惯例忽略未知字段。Anthropic/Gemini/Bedrock 走各自的转换链路
// 自然不会看到这个字段。
func (t *DraftChapterTool) StrictSchema() bool { return true }

func (t *DraftChapterTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter int    `json:"chapter"`
		Content string `json:"content"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	if a.Content == "" {
		return nil, fmt.Errorf("content must not be empty: %w", errs.ErrToolArgs)
	}
	if issue := repeatedDraftContentIssue(a.Content); issue != "" {
		return json.Marshal(repeatedDraftRejection(a.Chapter, a.Mode, issue))
	}
	if err := t.store.Progress.ValidateChapterWork(a.Chapter); err != nil {
		return nil, err
	}
	if err := EnsureAdaptationChapterPlanned(t.store, a.Chapter); err != nil {
		return nil, err
	}
	if err := EnsureChapterExpanded(t.store, a.Chapter); err != nil {
		return nil, err
	}
	if t.store.Progress.IsChapterCompleted(a.Chapter) {
		// 打磨/重写路径：章节虽已完成，但仍在 pending_rewrites 中，允许覆盖草稿
		progress, _ := t.store.Progress.Load()
		inRewriteQueue := progress != nil && slices.Contains(progress.PendingRewrites, a.Chapter)
		if !inRewriteQueue {
			return json.Marshal(map[string]any{
				"chapter":   a.Chapter,
				"skipped":   true,
				"completed": true,
				"reason":    fmt.Sprintf("第 %d 章已提交完成，不能覆盖", a.Chapter),
			})
		}
	}
	if err := t.store.Progress.StartChapter(a.Chapter); err != nil {
		return nil, fmt.Errorf("mark chapter in progress: %w", err)
	}

	switch a.Mode {
	case "append":
		if err := t.store.Drafts.AppendDraft(a.Chapter, a.Content); err != nil {
			return nil, fmt.Errorf("append draft: %w", err)
		}
		full, err := t.store.Drafts.LoadDraft(a.Chapter)
		if err != nil {
			return nil, fmt.Errorf("load draft after append: %w", err)
		}
		if _, err := t.store.Checkpoints.AppendArtifact(
			domain.ChapterScope(a.Chapter), "draft",
			fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
		); err != nil {
			return nil, fmt.Errorf("checkpoint draft: %w", err)
		}
		return json.Marshal(t.buildDraftResult(a.Chapter, "append", utf8.RuneCountInString(full)))
	default: // write
		if err := t.store.Drafts.SaveDraft(a.Chapter, a.Content); err != nil {
			return nil, fmt.Errorf("save draft: %w", err)
		}
		if _, err := t.store.Checkpoints.AppendArtifact(
			domain.ChapterScope(a.Chapter), "draft",
			fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
		); err != nil {
			return nil, fmt.Errorf("checkpoint draft: %w", err)
		}
		return json.Marshal(t.buildDraftResult(a.Chapter, "write", utf8.RuneCountInString(a.Content)))
	}
}

func (t *DraftChapterTool) buildDraftResult(chapter int, mode string, wordCount int) map[string]any {
	result := map[string]any{
		"written":    true,
		"chapter":    chapter,
		"mode":       mode,
		"word_count": wordCount,
		"next_step":  "先 read_chapter(source=draft) 回读草稿，再调用 check_consistency，最后 commit_chapter",
	}
	t.addNormalWordBudgetStatus(result, chapter, wordCount)
	contract, issues, ok := adaptationWordContractStatus(t.store, chapter, wordCount)
	if !ok {
		return result
	}
	result["adaptation_word_contract"] = contract
	result["word_contract_passed"] = len(issues) == 0
	if len(contract.Warnings) > 0 {
		result["word_contract_warnings"] = contract.Warnings
	}
	if len(issues) == 0 && normalWordBudgetAllowsDraftNextStep(result) {
		if contract.Hard {
			result["next_step"] = "字数硬契约已满足：按 read_chapter(source=\"draft\") → check_consistency → check_adaptation → commit_chapter 继续。"
		}
	}
	if len(issues) > 0 {
		result["word_contract_issues"] = issues
		if repair := adaptationWordContractRepairStep(contract, issues, chapter); repair != "" {
			result["next_step"] = repair
		}
	}
	if qualityIssues, ok := adaptationDraftQualityStatus(t.store, chapter, loadDraftTextForQuality(t.store, chapter)); ok && len(qualityIssues) > 0 {
		result["adaptation_quality_passed"] = false
		result["adaptation_quality_issues"] = qualityIssues
		if repair := adaptationQualityRepairStep(qualityIssues, chapter); repair != "" {
			result["next_step"] = repair
		}
	}
	return result
}

func normalWordBudgetAllowsDraftNextStep(result map[string]any) bool {
	passed, ok := result["word_budget_passed"].(bool)
	return !ok || passed
}

func repeatedDraftRejection(chapter int, mode string, issue string) map[string]any {
	if mode == "" {
		mode = "write"
	}
	return map[string]any{
		"written":                 false,
		"chapter":                 chapter,
		"mode":                    mode,
		"repeated_draft_rejected": true,
		"reason":                  fmt.Sprintf("draft_chapter content appears to repeat existing prose (%s)", issue),
		"next_step": fmt.Sprintf(
			"不要追加或重复提交同一段正文。请调用 draft_chapter(mode=\"write\", chapter=%d) 进行干净的整章重写，删除重复句，并满足本章字数预算后再 read_chapter/check_consistency/commit_chapter。",
			chapter,
		),
	}
}

func (t *DraftChapterTool) addNormalWordBudgetStatus(result map[string]any, chapter int, wordCount int) {
	if t.store == nil {
		return
	}
	meta, err := t.store.RunMeta.Load()
	if err != nil || meta == nil || meta.WordBudget == nil || meta.WordBudget.TargetTotalWords <= 0 {
		return
	}
	progress, err := t.store.Progress.Load()
	if err != nil {
		return
	}
	runtime, ok := meta.WordBudget.Runtime(progress, chapter)
	if !ok || runtime.CurrentChapter.Chapter <= 0 {
		return
	}
	minWords := runtime.CurrentChapter.RecommendedMinWords
	maxWords := runtime.CurrentChapter.RecommendedMaxWords
	result["word_budget"] = map[string]any{
		"min_words":              minWords,
		"max_words":              maxWords,
		"target_total_words":     runtime.Target.TargetTotalWords,
		"completed_words":        runtime.Progress.CompletedWords,
		"remaining_target_words": runtime.Remaining.TargetWords,
		"remaining_chapters":     runtime.Remaining.Chapters,
	}
	if wordCount >= minWords && wordCount <= maxWords {
		result["word_budget_passed"] = true
		return
	}
	result["word_budget_passed"] = false
	direction := "低于"
	if wordCount > maxWords {
		direction = "高于"
	}
	result["word_budget_issues"] = []string{
		fmt.Sprintf("第 %d 章草稿%s预算区间：当前 %d 字，要求 %d-%d 字。", chapter, direction, wordCount, minWords, maxWords),
	}
	result["next_step"] = fmt.Sprintf(
		"不要调用 commit_chapter。请调用 draft_chapter(mode=\"write\", chapter=%d) 干净地整章重写到 %d-%d 字，再 read_chapter(source=\"draft\")、check_consistency、commit_chapter。",
		chapter, minWords, maxWords,
	)
}

func loadDraftTextForQuality(st *store.Store, chapter int) string {
	if st == nil || chapter <= 0 {
		return ""
	}
	text, err := st.Drafts.LoadDraft(chapter)
	if err != nil {
		return ""
	}
	return text
}

func repeatedDraftContentIssue(content string) string {
	sentences := splitDraftSentences(content)
	seen := map[string]int{}
	repeatedSentences := 0
	repeatedRunes := 0
	for _, sentence := range sentences {
		normalized := normalizeDraftSentence(sentence)
		runes := utf8.RuneCountInString(normalized)
		if runes < 24 {
			continue
		}
		seen[normalized]++
		if seen[normalized] > 1 {
			repeatedSentences++
			repeatedRunes += runes
		}
	}
	if repeatedSentences >= 3 || repeatedRunes >= 180 {
		return fmt.Sprintf("%d repeated long sentence(s), about %d repeated characters", repeatedSentences, repeatedRunes)
	}
	return ""
}

func splitDraftSentences(content string) []string {
	var sentences []string
	var current strings.Builder
	for _, r := range content {
		current.WriteRune(r)
		switch r {
		case '。', '！', '？', '；', '.', '!', '?', ';', '\n':
			if sentence := strings.TrimSpace(current.String()); sentence != "" {
				sentences = append(sentences, sentence)
			}
			current.Reset()
		}
	}
	if sentence := strings.TrimSpace(current.String()); sentence != "" {
		sentences = append(sentences, sentence)
	}
	return sentences
}

func normalizeDraftSentence(sentence string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(sentence)), "")
}
