package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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

type consistencySceneCheck struct {
	Scene             int    `json:"scene"`
	Evidence          string `json:"evidence"`
	TimeAndPlaceMatch bool   `json:"time_and_place_match"`
	POVMatch          bool   `json:"pov_match"`
	CharactersMatch   bool   `json:"characters_match"`
	EventOrderMatch   bool   `json:"event_order_match"`
	KnowledgeMatch    bool   `json:"knowledge_match"`
	IrreversibleMatch bool   `json:"irreversible_result_match"`
}

func NewCheckConsistencyTool(store *store.Store) *CheckConsistencyTool {
	return &CheckConsistencyTool{store: store}
}

func (t *CheckConsistencyTool) Name() string { return "check_consistency" }
func (t *CheckConsistencyTool) Description() string {
	return "记录当前草稿已按 novel_context 的章节契约与连续性证据完成一致性审核。必须先 novel_context(chapter=N) 并 read_chapter；每个计划场景都必须提交一条可在当前草稿中精确找到的原文 evidence，并逐项核对时间地点、POV、人物、事件顺序、信息边界和不可逆结果。只有全部核对无矛盾时 findings 才能为空"
}
func (t *CheckConsistencyTool) Label() string { return "一致性检查" }

// 只读工具（仅追加 checkpoint 事件，不改状态），可被并发调度。
func (t *CheckConsistencyTool) ReadOnly(_ json.RawMessage) bool        { return true }
func (t *CheckConsistencyTool) ConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *CheckConsistencyTool) Schema() map[string]any {
	sceneCheckSchema := schema.Object(
		schema.Property("scene", schema.Int("章节契约中的场景序号，从 1 开始")).Required(),
		schema.Property("evidence", schema.String("当前草稿中可精确检索的原文短句；不是概述")).Required(),
		schema.Property("time_and_place_match", schema.Bool("时间与命名地点是否符合该场景契约")).Required(),
		schema.Property("pov_match", schema.Bool("POV 是否符合该场景契约")).Required(),
		schema.Property("characters_match", schema.Bool("参与人物及其身份是否符合该场景契约")).Required(),
		schema.Property("event_order_match", schema.Bool("关键事件与先后顺序是否符合该场景契约")).Required(),
		schema.Property("knowledge_match", schema.Bool("人物知情边界是否符合该场景契约")).Required(),
		schema.Property("irreversible_result_match", schema.Bool("该场景承担的不可逆结果或交接是否落地")).Required(),
	)
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
		schema.Property("scene_checks", schema.Array("逐场景、以当前草稿原文为证据的契约核对；数量必须等于章节细纲场景数", sceneCheckSchema)).Required(),
		schema.Property("findings", schema.Array("structured character and continuity findings; [] means no finding", findingSchema)).Required(),
	)
}

func (t *CheckConsistencyTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter     int                       `json:"chapter"`
		SceneChecks []consistencySceneCheck   `json:"scene_checks"`
		Findings    []domain.ConsistencyIssue `json:"findings"`
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
	if err := t.validateSceneChecks(a.Chapter, content, a.SceneChecks); err != nil {
		return nil, err
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
		"scene_checks": a.SceneChecks,
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

func (t *CheckConsistencyTool) validateSceneChecks(
	chapter int,
	content string,
	checks []consistencySceneCheck,
) error {
	outline, err := t.store.Outline.GetChapterOutline(chapter)
	if err != nil {
		// Legacy/isolated tests and repair projects can have a draft before a
		// formal outline exists. Preserve the historical receipt behavior for
		// that narrow case; production layered writing always has an outline.
		return nil
	}
	if outline == nil || len(outline.Scenes) == 0 {
		return nil
	}
	if len(checks) != len(outline.Scenes) {
		return fmt.Errorf(
			"scene_checks count %d does not match chapter %d contract scene count %d; submit exactly one grounded check for each indexed planned scene, not each prose subsection. expected_scene_contracts=%s: %w",
			len(checks), chapter, len(outline.Scenes), compactIndexedSceneContracts(outline.Scenes), errs.ErrToolPrecondition,
		)
	}
	seen := make(map[int]struct{}, len(checks))
	evidenceOffsets := make(map[int]int, len(checks))
	for index, check := range checks {
		if check.Scene < 1 || check.Scene > len(outline.Scenes) {
			return fmt.Errorf("scene_checks[%d].scene %d is outside 1-%d: %w", index, check.Scene, len(outline.Scenes), errs.ErrToolArgs)
		}
		if _, duplicate := seen[check.Scene]; duplicate {
			return fmt.Errorf("scene_checks contains duplicate scene %d: %w", check.Scene, errs.ErrToolArgs)
		}
		seen[check.Scene] = struct{}{}
		evidence := strings.TrimSpace(check.Evidence)
		if len([]rune(evidence)) < 8 || !strings.Contains(content, evidence) {
			return fmt.Errorf(
				"scene_checks[%d].evidence is not an exact current-draft quote of at least 8 characters; call read_chapter and quote the draft, never invent or summarize evidence: %w",
				index, errs.ErrToolPrecondition,
			)
		}
		evidenceOffsets[check.Scene] = strings.Index(content, evidence)
		if !check.TimeAndPlaceMatch || !check.POVMatch || !check.CharactersMatch ||
			!check.EventOrderMatch || !check.KnowledgeMatch || !check.IrreversibleMatch {
			return fmt.Errorf(
				"scene_checks[%d] marks a chapter-contract dimension as failed; add a blocking finding and repair the draft before recording a passing consistency receipt: %w",
				index, errs.ErrToolPrecondition,
			)
		}
	}
	orderedScenes := make([]int, 0, len(evidenceOffsets))
	for scene := range evidenceOffsets {
		orderedScenes = append(orderedScenes, scene)
	}
	sort.Ints(orderedScenes)
	previousOffset := -1
	for _, scene := range orderedScenes {
		offset := evidenceOffsets[scene]
		if offset <= previousOffset {
			return fmt.Errorf(
				"scene_checks evidence is not in planned scene order at scene %d; quote one representative passage from each planned scene in narrative order instead of mapping prose subsections to new scene numbers: %w",
				scene, errs.ErrToolPrecondition,
			)
		}
		previousOffset = offset
	}
	return nil
}

func compactIndexedSceneContracts(scenes []string) string {
	var result strings.Builder
	for index, scene := range scenes {
		if index > 0 {
			result.WriteString(" | ")
		}
		fmt.Fprintf(&result, "%d:%s", index+1, truncateConsistencyContract(strings.TrimSpace(scene), 96))
	}
	return result.String()
}

func truncateConsistencyContract(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
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
