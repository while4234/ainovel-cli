package flow

import (
	"fmt"

	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func formatOutlineRepairTask(batch *storepkg.OutlineRepairBatch) string {
	duplicate := batch.Duplicate
	return fmt.Sprintf(
		"Repair duplicated outline batch V%d A%d (global chapters %d-%d). Chapter %d duplicates chapter %d in title/core_event/hook (%q). Call save_foundation(type=\"repair_arc\", volume=%d, arc=%d) with exactly %d chapter outline entries. Keep the chapter count unchanged, preserve continuity with earlier chapters, and make every title/core_event/hook distinct. If this is adaptation mode, keep source anchors and word budgets conceptually unchanged; the tool will preserve those fields in the confirmed plan. Do not dispatch writer until repair_arc succeeds.",
		batch.Volume,
		batch.Arc,
		batch.FromChapter,
		batch.ToChapter,
		duplicate.Chapter,
		duplicate.ExistingChapter,
		duplicate.Title,
		batch.Volume,
		batch.Arc,
		batch.ChapterCount,
	)
}
