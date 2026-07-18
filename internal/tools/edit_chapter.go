package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/voocel/agentcore/schema"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// EditChapterTool 对章节草稿做定点字符串替换，适用于打磨场景。
// 相比 draft_chapter 整章重写，token 节省 10x+。
//
// 落盘契约：只改 drafts/{ch:02d}.draft.md，禁止直接改 chapters/（终稿由 commit_chapter 独占）。
// Seed 语义：drafts 不存在但 chapters 有 → 自动把 chapters 复制到 drafts 作为起点。
// 归属检查：章节已完成时必须在 PendingRewrites 队列中，否则拒绝。
//
// 本工具是 agentcore.EditTool 的薄封装，找-换逻辑（多级容错匹配、diff 输出、行尾/BOM 保留）
// 全部复用上游实现。
type EditChapterTool struct {
	store *store.Store
	edit  *agentcoretools.EditTool
}

const maxChapterBatchEdits = 24

type chapterTextEdit struct {
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

type editChapterRequest struct {
	Chapter    int               `json:"chapter"`
	OldString  string            `json:"old_string"`
	NewString  string            `json:"new_string"`
	ReplaceAll bool              `json:"replace_all"`
	Edits      []chapterTextEdit `json:"edits"`
}

func NewEditChapterTool(s *store.Store) *EditChapterTool {
	return &EditChapterTool{
		store: s,
		edit:  agentcoretools.NewEdit(s.Dir(), nil),
	}
}

func (t *EditChapterTool) Name() string  { return "edit_chapter" }
func (t *EditChapterTool) Label() string { return "编辑章节" }

// ReadOnly 明确声明写工具（配合 ConcurrencySafeTool 防止被并发调度）。
func (t *EditChapterTool) ReadOnly(_ json.RawMessage) bool { return false }

// ConcurrencySafe 显式禁止并发：同章节多次 edit_chapter 并行会读-改-写竞态，
// 即使不同章节并行也会穿插 checkpoint 顺序。统一串行最稳。
func (t *EditChapterTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

// ActivityDescription 供 UI/日志展示当前工具的活动描述。
func (t *EditChapterTool) ActivityDescription(_ json.RawMessage) string { return "编辑章节草稿" }

func (t *EditChapterTool) Description() string {
	return "对章节草稿做定点字符串替换（适合单处、唯一的精确打磨；同类去AI化问题优先用 repair_de_ai_batch 分批处理）。" +
		"找到 old_string 并替换为 new_string，要求精确匹配且唯一（多处匹配需 replace_all=true）。" +
		"写入 drafts/{ch}.draft.md；drafts 不存在时自动从 chapters 播种。" +
		"章节已完成且不在 PendingRewrites 队列中时拒绝执行。单处修改传 old_string/new_string；" +
		"一次回读已确定多处局部修改时，用 edits 一次原子落盘 1-24 处，不要为每处修改重读整章。"
}

func (t *EditChapterTool) Schema() map[string]any {
	edit := schema.Object(
		schema.Property("old_string", schema.String("当前草稿中唯一出现的原文精确片段")).Required(),
		schema.Property("new_string", schema.String("替换后的新文本，可为空以删除冗余段落")).Required(),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号")).Required(),
		schema.Property("old_string", schema.String("单处替换的原文精确片段；与 edits 二选一")),
		schema.Property("new_string", schema.String("单处替换的新文本，可为空")),
		schema.Property("replace_all", schema.Bool("替换所有匹配（默认 false）")),
		schema.Property("edits", schema.Array("一次回读后确定的 1-24 处不重叠局部替换；与 old_string/new_string 二选一", edit)),
	)
}

func (t *EditChapterTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a editChapterRequest
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	batchMode := len(a.Edits) > 0
	singleMode := a.OldString != "" || a.NewString != "" || a.ReplaceAll
	if batchMode && singleMode {
		return nil, fmt.Errorf("use either edits or old_string/new_string, not both: %w", errs.ErrToolArgs)
	}
	if batchMode && len(a.Edits) > maxChapterBatchEdits {
		return nil, fmt.Errorf("edits must contain 1-%d items: %w", maxChapterBatchEdits, errs.ErrToolArgs)
	}
	if !batchMode && a.OldString == "" {
		return nil, fmt.Errorf("old_string 不能为空: %w", errs.ErrToolArgs)
	}
	if !batchMode && a.OldString == a.NewString {
		return nil, fmt.Errorf("old_string 与 new_string 相同，无需修改: %w", errs.ErrToolArgs)
	}

	// 归属检查：已完成章节必须在重写队列中，避免污染终稿
	if t.store.Progress.IsChapterCompleted(a.Chapter) {
		progress, _ := t.store.Progress.Load()
		if progress == nil || !slices.Contains(progress.PendingRewrites, a.Chapter) {
			return nil, fmt.Errorf("第 %d 章已完成且不在 PendingRewrites 队列中，不能编辑；需修改请先由 editor 评审触发重写/打磨: %w", a.Chapter, errs.ErrToolPrecondition)
		}
	}

	// Seed：drafts 不存在时从 chapters 复制一份作为起点
	if err := t.ensureDraft(a.Chapter); err != nil {
		return nil, err
	}
	current, err := t.store.Drafts.LoadDraft(a.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load current draft before edit: %w: %w", errs.ErrStoreRead, err)
	}
	if batchMode {
		return t.executeBatch(a.Chapter, current, a.Edits)
	}
	// Recovery and context compaction can make a weak model repeat the exact
	// same patch. Treat only a provably completed replacement as an idempotent
	// no-op: the old text is gone and the complete non-empty new text is already
	// present. All other mismatches still reach EditTool and remain hard errors.
	if a.NewString != "" && !strings.Contains(current, a.OldString) && strings.Contains(current, a.NewString) {
		payload := map[string]any{
			"chapter":         a.Chapter,
			"already_applied": true,
			"changed":         false,
			"message":         "相同的局部修改已存在于当前草稿，无需重复写入。",
			"next_step":       t.nextStepAfterEdit(),
		}
		t.addDraftStatus(payload, a.Chapter)
		return json.Marshal(payload)
	}
	// 委托 agentcore.EditTool 完成找-换
	subArgs, _ := json.Marshal(map[string]any{
		"path":        fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
		"file_path":   fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
		"old_text":    a.OldString,
		"old_string":  a.OldString,
		"new_text":    a.NewString,
		"new_string":  a.NewString,
		"replace_all": a.ReplaceAll,
	})
	result, err := t.edit.Execute(ctx, subArgs)
	if err != nil {
		return nil, fmt.Errorf("apply edit: %w: %w", errs.ErrToolPrecondition, err)
	}
	if err := t.syncEditedDraft(a.Chapter); err != nil {
		return nil, err
	}

	if err := t.checkpointEdit(a.Chapter); err != nil {
		return nil, err
	}

	// 附加指引：让 writer 知道后续步骤，避免遗漏 check_consistency / commit_chapter
	var passthrough map[string]any
	if err := json.Unmarshal(result, &passthrough); err != nil {
		return result, nil
	}
	passthrough["chapter"] = a.Chapter
	passthrough["next_step"] = t.nextStepAfterEdit()
	t.addDraftStatus(passthrough, a.Chapter)
	return json.Marshal(passthrough)
}

func (t *EditChapterTool) executeBatch(chapter int, current string, edits []chapterTextEdit) (json.RawMessage, error) {
	updated, changed, alreadyApplied, err := applyChapterEditBatch(current, edits)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"chapter":               chapter,
		"changed":               changed > 0,
		"edit_count":            changed,
		"already_applied_count": alreadyApplied,
		"next_step":             t.nextStepAfterEdit(),
	}
	if changed == 0 {
		payload["already_applied"] = true
		payload["message"] = "这一批局部修改已全部存在于当前草稿，无需重复写入。"
		t.addDraftStatus(payload, chapter)
		return json.Marshal(payload)
	}
	if err := t.store.Drafts.SaveDraft(chapter, updated); err != nil {
		return nil, fmt.Errorf("save batch-edited draft: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := t.checkpointEdit(chapter); err != nil {
		return nil, err
	}
	t.addDraftStatus(payload, chapter)
	return json.Marshal(payload)
}

func applyChapterEditBatch(content string, edits []chapterTextEdit) (string, int, int, error) {
	type patch struct {
		item        int
		start, end  int
		replacement string
	}
	patches := make([]patch, 0, len(edits))
	alreadyApplied := 0
	seen := make(map[string]struct{}, len(edits))
	for index, edit := range edits {
		if edit.OldString == "" {
			return "", 0, 0, fmt.Errorf("edits[%d].old_string cannot be empty: %w", index, errs.ErrToolArgs)
		}
		if edit.OldString == edit.NewString {
			return "", 0, 0, fmt.Errorf("edits[%d] does not change the text: %w", index, errs.ErrToolArgs)
		}
		if _, duplicate := seen[edit.OldString]; duplicate {
			return "", 0, 0, fmt.Errorf("edits[%d] duplicates an earlier old_string: %w", index, errs.ErrToolArgs)
		}
		seen[edit.OldString] = struct{}{}
		matches := strings.Count(content, edit.OldString)
		if matches == 0 && edit.NewString != "" && strings.Contains(content, edit.NewString) {
			alreadyApplied++
			continue
		}
		if matches != 1 {
			return "", 0, 0, fmt.Errorf("edits[%d].old_string must match exactly once in the current draft, got %d: %w", index, matches, errs.ErrToolPrecondition)
		}
		start := strings.Index(content, edit.OldString)
		patches = append(patches, patch{item: index, start: start, end: start + len(edit.OldString), replacement: edit.NewString})
	}
	sort.Slice(patches, func(left, right int) bool { return patches[left].start < patches[right].start })
	for index := 1; index < len(patches); index++ {
		previous, current := patches[index-1], patches[index]
		if current.start < previous.end {
			return "", 0, 0, fmt.Errorf("edits[%d] overlaps edits[%d]: %w", current.item, previous.item, errs.ErrToolArgs)
		}
	}
	sort.Slice(patches, func(left, right int) bool { return patches[left].start > patches[right].start })
	updated := content
	for _, patch := range patches {
		updated = updated[:patch.start] + patch.replacement + updated[patch.end:]
	}
	return updated, len(patches), alreadyApplied, nil
}

func (t *EditChapterTool) checkpointEdit(chapter int) error {
	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(chapter), "edit", fmt.Sprintf("drafts/%02d.draft.md", chapter),
	); err != nil {
		return fmt.Errorf("checkpoint edit: %w: %w", errs.ErrStoreWrite, err)
	}
	return nil
}

func (t *EditChapterTool) addDraftStatus(payload map[string]any, chapter int) {
	content, wordCount, err := t.store.Drafts.LoadChapterContent(chapter)
	if err != nil || content == "" {
		return
	}
	payload["word_count"] = wordCount
	meta, err := t.store.RunMeta.Load()
	if err != nil || meta == nil || meta.WordBudget == nil {
		return
	}
	progress, err := t.store.Progress.Load()
	if err != nil {
		return
	}
	runtime, ok := meta.WordBudget.Runtime(progress, chapter)
	if !ok || runtime.CurrentChapter.Chapter == 0 {
		return
	}
	minWords := runtime.CurrentChapter.RecommendedMinWords
	maxWords := runtime.CurrentChapter.RecommendedMaxWords
	payload["word_budget"] = map[string]any{"min_words": minWords, "max_words": maxWords}
	withinBudget := wordCount >= minWords && wordCount <= maxWords
	payload["word_budget_passed"] = withinBudget
	if withinBudget {
		return
	}
	payload["next_step"] = fmt.Sprintf(
		"当前草稿 %d 字，仍不在 %d-%d 字区间。不要重新 read_chapter；依据本轮最初回读的原文，用 edit_chapter(edits=[...]) 再做一批不重叠的局部删减或补足，保留关键情节、人物选择、情感落点和章末钩子。进入区间后再执行各项检查。",
		wordCount, minWords, maxWords,
	)
}

func (t *EditChapterTool) nextStepAfterEdit() string {
	if t.store != nil && t.store.Adaptation.Active() {
		return "edit 已落盘，旧 check_consistency/check_adaptation 已失效；旧 check_de_ai 也已失效。先重新调用 check_consistency 和 check_adaptation；若任一检查还要求改稿，重复本轮直到它们在同一版草稿上通过。随后必须调用 check_de_ai；仍有 finding 时同类问题用 repair_de_ai_batch 做一小批精确修订并立即复检。去AI化通过后再次重跑 check_consistency 和 check_adaptation；任何后续改稿都会使去AI报告失效，必须重新 check_de_ai。只有同一版草稿全部通过才能 commit_chapter。"
	}
	return "edit 已落盘，旧 check_consistency/check_de_ai 均已失效。先重新调用 check_consistency；若它要求改稿，重复本轮直到通过。随后必须调用 check_de_ai，仍有 finding 时用 repair_de_ai_batch 做一小批精确修订并立即复检；去AI化通过后再次 check_consistency。任何后续改稿都会使去AI报告失效，必须重新 check_de_ai。只有同一版草稿全部通过才能 commit_chapter。"
}

// ensureDraft 保证 drafts/{ch}.draft.md 存在：
//   - 已有草稿 → 直接返回
//   - 无草稿但有终稿 → 把终稿复制到 drafts 作为修改起点（常见于打磨场景）
//   - 都没有 → 报错，提示先用 draft_chapter 创建初稿
func (t *EditChapterTool) ensureDraft(chapter int) error {
	draft, err := t.store.Drafts.LoadDraft(chapter)
	if err != nil {
		return fmt.Errorf("load draft: %w: %w", errs.ErrStoreRead, err)
	}
	if draft != "" {
		return t.saveEditableDraft(chapter, draft)
	}
	text, err := t.store.Drafts.LoadChapterText(chapter)
	if err != nil {
		return fmt.Errorf("load chapter: %w: %w", errs.ErrStoreRead, err)
	}
	if text == "" {
		return fmt.Errorf("第 %d 章无草稿也无终稿，请先调 draft_chapter(mode=write, chapter=%d) 创建初稿: %w", chapter, chapter, errs.ErrToolPrecondition)
	}
	if err := t.store.Drafts.SaveDraft(chapter, text); err != nil {
		return fmt.Errorf("seed draft from chapter: %w: %w", errs.ErrStoreWrite, err)
	}
	return nil
}

func (t *EditChapterTool) saveEditableDraft(chapter int, content string) error {
	if err := t.store.Drafts.SaveDraft(chapter, content); err != nil {
		return fmt.Errorf("prepare editable draft: %w: %w", errs.ErrStoreWrite, err)
	}
	return nil
}

func (t *EditChapterTool) syncEditedDraft(chapter int) error {
	path := filepath.Join(t.store.Dir(), "drafts", fmt.Sprintf("%02d.draft.md", chapter))
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read edited draft: %w: %w", errs.ErrStoreRead, err)
	}
	if err := t.store.Drafts.SaveDraft(chapter, string(content)); err != nil {
		return fmt.Errorf("sync edited draft: %w: %w", errs.ErrStoreWrite, err)
	}
	return nil
}
