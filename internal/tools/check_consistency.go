package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// CheckConsistencyTool records that Writer reviewed the current draft against
// its already-loaded chapter contract and continuity evidence. It deliberately
// returns a receipt instead of echoing the draft and global state back into the
// same model turn: novel_context + read_chapter are the authoritative inputs.
type CheckConsistencyTool struct {
	store *store.Store
}

func NewCheckConsistencyTool(store *store.Store) *CheckConsistencyTool {
	return &CheckConsistencyTool{store: store}
}

func (t *CheckConsistencyTool) Name() string { return "check_consistency" }
func (t *CheckConsistencyTool) Description() string {
	return "记录当前草稿已按 novel_context 的章节契约与连续性证据完成一致性审核。必须先 novel_context(chapter=N) 并 read_chapter；逐场景核对契约中的时间、地点、POV、人物、事件顺序、信息边界和不可逆结果。只有全部核对无矛盾时 findings 才能为空"
}
func (t *CheckConsistencyTool) Label() string { return "一致性检查" }

// 只读工具（仅追加 checkpoint 事件，不改状态），可被并发调度。
func (t *CheckConsistencyTool) ReadOnly(_ json.RawMessage) bool        { return true }
func (t *CheckConsistencyTool) ConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *CheckConsistencyTool) Schema() map[string]any {
	findingSchema := schema.Object(
		schema.Property("type", schema.Enum("character finding type", "ooc", "voice_drift", "motivation_break", "knowledge_leak", "relationship_jump", "arc_beat_miss", "supporting_character_flat", "static_dynamic_conflict", "adaptation_source_confusion")).Required(),
		schema.Property("severity", schema.Enum("severity", "critical", "error", "warning")).Required(),
		schema.Property("character_id", schema.String("stable character ID")).Required(),
		schema.Property("scene", schema.String("chapter or scene locator")).Required(),
		schema.Property("evidence", schema.String("concise draft evidence")).Required(),
		schema.Property("violated_field", schema.String("character card, knowledge boundary, outline beat, or chapter contract field")).Required(),
		schema.Property("description", schema.String("what is inconsistent")).Required(),
		schema.Property("suggestion", schema.String("executable repair instruction")).Required(),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("要检查的章节号")).Required(),
		schema.Property("findings", schema.Array("structured character and continuity findings; [] means no finding", findingSchema)).Required(),
	)
}

func (t *CheckConsistencyTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter  int                       `json:"chapter"`
		Findings []domain.ConsistencyIssue `json:"findings"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}

	content, wordCount, err := t.store.Drafts.LoadChapterContent(a.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load chapter content: %w: %w", errs.ErrStoreRead, err)
	}
	if content == "" {
		return nil, fmt.Errorf("no content found for chapter %d: %w", a.Chapter, errs.ErrToolPrecondition)
	}
	if err := t.validateCharacterFindings(a.Chapter, a.Findings); err != nil {
		return nil, err
	}
	blocking := false
	for _, finding := range a.Findings {
		if finding.Severity == "critical" || finding.Severity == "error" {
			blocking = true
			break
		}
	}
	result := map[string]any{
		"chapter":      a.Chapter,
		"word_count":   wordCount,
		"draft_sha256": store.TextSHA256(content),
		"reviewed":     !blocking,
		"passed":       !blocking,
		"findings":     a.Findings,
		"review_against": []string{
			"novel_context.working_memory.chapter_contract",
			"novel_context.episodic_memory",
			"the current read_chapter draft",
		},
		"next_step": "If the comparison found a contradiction, edit the draft and rerun all checks; otherwise continue the same-draft validation sequence.",
	}
	if blocking {
		result["blocking"] = true
		result["next_step"] = "Apply every critical/error repair instruction to the current draft, then rerun check_consistency and all same-draft gates. Do not commit."
		return json.Marshal(result)
	}

	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(a.Chapter), "consistency_check",
		fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
	); err != nil {
		return nil, fmt.Errorf("checkpoint consistency check: %w", err)
	}

	return json.Marshal(result)
}

func (t *CheckConsistencyTool) validateCharacterFindings(chapter int, findings []domain.ConsistencyIssue) error {
	foundation, err := t.store.Foundation.Load()
	if err != nil {
		return fmt.Errorf("load StoryFoundation for consistency findings: %w", err)
	}
	ids := make(map[string]struct{}, len(foundation.Characters))
	for _, character := range foundation.Characters {
		ids[character.ID] = struct{}{}
	}
	for index, finding := range findings {
		if _, ok := ids[strings.TrimSpace(finding.CharacterID)]; !ok {
			return fmt.Errorf("findings[%d].character_id %q is not in StoryFoundation: %w", index, finding.CharacterID, errs.ErrToolArgs)
		}
		if strings.TrimSpace(finding.Scene) == "" ||
			strings.TrimSpace(finding.Evidence) == "" ||
			strings.TrimSpace(finding.ViolatedField) == "" ||
			strings.TrimSpace(finding.Description) == "" ||
			strings.TrimSpace(finding.Suggestion) == "" {
			return fmt.Errorf("findings[%d] requires scene, evidence, violated_field, description, and suggestion: %w", index, errs.ErrToolArgs)
		}
	}
	return nil
}
