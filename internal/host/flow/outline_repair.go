package flow

import (
	"fmt"

	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func formatOutlineRepairTask(batch *storepkg.OutlineRepairBatch) string {
	duplicate := batch.Duplicate
	return fmt.Sprintf(
		"Repair duplicated outline batch V%d A%d only (global chapters %d-%d). Chapter %d duplicates chapter %d (%s, title %q). Call save_foundation(type=\"repair_arc\", volume=%d, arc=%d) with exactly %d chapter outline entries. Keep the chapter count unchanged, preserve continuity with earlier chapters, and make every title/core_event/hook/scenes detail distinct under the duplicate rule: identical long titles or highly similar detailed outline text are still duplicates. If the tool reports borderline similar pairs, perform the required similarity_review judgment for this same batch; if any pair is duplicate, rewrite the full batch again before saving. Do not repair or include any other batch in this call; after repair_arc succeeds the host will clean stale articles for this batch, rescan, and dispatch the next batch if needed. If this is adaptation mode, keep source anchors and word budgets conceptually unchanged; the tool will preserve those fields in the confirmed plan. Do not dispatch writer until repair_arc succeeds.",
		batch.Volume,
		batch.Arc,
		batch.FromChapter,
		batch.ToChapter,
		duplicate.Chapter,
		duplicate.ExistingChapter,
		duplicate.Reason,
		duplicate.Title,
		batch.Volume,
		batch.Arc,
		batch.ChapterCount,
	)
}
