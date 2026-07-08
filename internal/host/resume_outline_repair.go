package host

import (
	"fmt"
	"slices"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type resumeOutlineRepairResult struct {
	Batch          *storepkg.OutlineRepairBatch
	QueuedChapters []int
}

func prepareResumeOutlineRepair(st *storepkg.Store) (*resumeOutlineRepairResult, error) {
	if st == nil {
		return nil, nil
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		return nil, err
	}
	if progress.Phase != domain.PhaseWriting {
		return nil, nil
	}

	batch, err := st.FindDuplicateOutlineRepairBatch(progress)
	if err != nil || batch == nil {
		return nil, err
	}
	result := &resumeOutlineRepairResult{Batch: batch}
	if !batch.Repairable() || len(batch.CompletedChapters) == 0 {
		return result, nil
	}

	merged, added := mergeResumeRepairRewrites(progress.PendingRewrites, batch.CompletedChapters)
	reason := fmt.Sprintf(
		"outline duplicate repair V%d A%d: chapter %d duplicates chapter %d",
		batch.Volume,
		batch.Arc,
		batch.Duplicate.Chapter,
		batch.Duplicate.ExistingChapter,
	)
	if !slices.Equal(merged, progress.PendingRewrites) || progress.RewriteReason != reason {
		if err := st.Progress.SetPendingRewrites(merged, reason); err != nil {
			return nil, err
		}
	}
	if progress.Flow != domain.FlowRewriting {
		if err := st.Progress.SetFlow(domain.FlowRewriting); err != nil {
			return nil, err
		}
	}
	result.QueuedChapters = added
	return result, nil
}

func mergeResumeRepairRewrites(current []int, repairChapters []int) ([]int, []int) {
	seen := make(map[int]struct{}, len(current)+len(repairChapters))
	currentSet := make(map[int]struct{}, len(current))
	for _, chapter := range current {
		if chapter > 0 {
			currentSet[chapter] = struct{}{}
		}
	}

	merged := make([]int, 0, len(current)+len(repairChapters))
	added := make([]int, 0, len(repairChapters))
	for _, chapter := range repairChapters {
		if chapter <= 0 {
			continue
		}
		if _, ok := seen[chapter]; ok {
			continue
		}
		seen[chapter] = struct{}{}
		merged = append(merged, chapter)
		if _, ok := currentSet[chapter]; !ok {
			added = append(added, chapter)
		}
	}
	for _, chapter := range current {
		if chapter <= 0 {
			continue
		}
		if _, ok := seen[chapter]; ok {
			continue
		}
		seen[chapter] = struct{}{}
		merged = append(merged, chapter)
	}
	slices.Sort(added)
	return merged, added
}

func formatResumeOutlineRepairNotice(result *resumeOutlineRepairResult) string {
	if result == nil || result.Batch == nil {
		return ""
	}
	batch := result.Batch
	duplicate := batch.Duplicate
	if !batch.Repairable() {
		return fmt.Sprintf(
			"恢复前发现重复大纲：第 %d 章重复第 %d 章，但当前大纲无法自动定位到已展开弧，请先人工修复大纲。",
			duplicate.Chapter,
			duplicate.ExistingChapter,
		)
	}
	if len(batch.CompletedChapters) == 0 {
		return fmt.Sprintf(
			"恢复前发现重复大纲：第 %d 章重复第 %d 章；将先修复 V%d A%d 批次大纲，该批次暂无已完成章节，无需重写正文。",
			duplicate.Chapter,
			duplicate.ExistingChapter,
			batch.Volume,
			batch.Arc,
		)
	}
	if len(result.QueuedChapters) == 0 {
		return fmt.Sprintf(
			"恢复前发现重复大纲：第 %d 章重复第 %d 章；V%d A%d 的已完成章节已在重写队列，将先修复大纲再重写。",
			duplicate.Chapter,
			duplicate.ExistingChapter,
			batch.Volume,
			batch.Arc,
		)
	}
	return fmt.Sprintf(
		"恢复前发现重复大纲：第 %d 章重复第 %d 章；已将 V%d A%d 的已完成章节 %v 加入重写队列，将先修复大纲再重写。",
		duplicate.Chapter,
		duplicate.ExistingChapter,
		batch.Volume,
		batch.Arc,
		result.QueuedChapters,
	)
}
