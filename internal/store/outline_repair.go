package store

import (
	"fmt"
	"slices"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// OutlineRepairBatch describes the smallest durable outline batch that can be
// repaired before writing resumes.
type OutlineRepairBatch struct {
	Volume            int
	Arc               int
	FromChapter       int
	ToChapter         int
	ChapterCount      int
	Duplicate         domain.OutlineDuplicate
	CompletedChapters []int
}

func (b *OutlineRepairBatch) Repairable() bool {
	return b != nil && b.Volume > 0 && b.Arc > 0 && b.FromChapter > 0 && b.ToChapter >= b.FromChapter
}

// FindDuplicateOutlineRepairBatch locates the first duplicated outline promise
// and maps the later duplicate chapter to its expanded layered arc.
func (s *Store) FindDuplicateOutlineRepairBatch(progress *domain.Progress) (*OutlineRepairBatch, error) {
	entries, err := s.Outline.LoadOutline()
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	duplicate, ok := domain.FindDuplicateOutlineEntries(entries)
	if !ok {
		return nil, nil
	}
	if progress == nil || !progress.Layered {
		return &OutlineRepairBatch{Duplicate: duplicate}, nil
	}
	return s.layeredRepairBatchForDuplicate(progress, duplicate)
}

func (s *Store) layeredRepairBatchForDuplicate(progress *domain.Progress, duplicate domain.OutlineDuplicate) (*OutlineRepairBatch, error) {
	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		return nil, err
	}

	globalChapter := 1
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			arcLen := len(arc.Chapters)
			if arcLen == 0 {
				continue
			}
			from := globalChapter
			to := globalChapter + arcLen - 1
			if duplicate.Chapter >= from && duplicate.Chapter <= to {
				return &OutlineRepairBatch{
					Volume:            volume.Index,
					Arc:               arc.Index,
					FromChapter:       from,
					ToChapter:         to,
					ChapterCount:      arcLen,
					Duplicate:         duplicate,
					CompletedChapters: completedChaptersInRange(progress.CompletedChapters, from, to),
				}, nil
			}
			globalChapter += arcLen
		}
	}

	return &OutlineRepairBatch{Duplicate: duplicate}, nil
}

func completedChaptersInRange(completed []int, from, to int) []int {
	out := make([]int, 0, to-from+1)
	for _, chapter := range completed {
		if chapter >= from && chapter <= to && !slices.Contains(out, chapter) {
			out = append(out, chapter)
		}
	}
	slices.Sort(out)
	return out
}

// RepairArcOutline replaces an already-expanded arc without changing its
// chapter count. In adaptation projects it also updates the confirmed plan's
// target outline fields while preserving source anchors and word budgets.
func (s *Store) RepairArcOutline(volumeIdx, arcIdx int, chapters []domain.OutlineEntry) error {
	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	var planToSave *domain.AdaptationPlan
	if s.Adaptation.Active() {
		plan, err := s.Adaptation.LoadPlan()
		if err != nil {
			return fmt.Errorf("load adaptation plan: %w", err)
		}
		if plan != nil {
			planCopy := *plan
			planCopy.Chapters = cloneAdaptationPlans(plan.Chapters)
			planToSave = &planCopy
		}
	}

	s.Outline.io.mu.Lock()
	volumes, repaired, err := s.Outline.replaceArcChaptersUnlocked(volumeIdx, arcIdx, chapters)
	s.Outline.io.mu.Unlock()
	if err != nil {
		return err
	}

	if planToSave != nil {
		if err := updateAdaptationPlanOutlineEntries(planToSave, repaired); err != nil {
			return err
		}
		if err := s.Adaptation.SavePlan(*planToSave); err != nil {
			return fmt.Errorf("save repaired adaptation plan: %w", err)
		}
	}

	s.Progress.io.mu.Lock()
	defer s.Progress.io.mu.Unlock()

	progress, err := s.Progress.loadUnlocked()
	if err != nil {
		return err
	}
	if progress == nil {
		progress = &domain.Progress{}
	}
	progress.TotalChapters = domain.TotalChapters(volumes)
	return s.Progress.saveUnlocked(progress)
}

func cloneAdaptationPlans(chapters []domain.AdaptationChapterPlan) []domain.AdaptationChapterPlan {
	out := make([]domain.AdaptationChapterPlan, len(chapters))
	for i := range chapters {
		out[i] = chapters[i]
		out[i].SourceChapters = append([]int(nil), chapters[i].SourceChapters...)
		out[i].PreserveEvents = append([]string(nil), chapters[i].PreserveEvents...)
		out[i].RequiredChanges = append([]string(nil), chapters[i].RequiredChanges...)
		out[i].ForbiddenMoves = append([]string(nil), chapters[i].ForbiddenMoves...)
		out[i].OutlineEntry.Scenes = append([]string(nil), chapters[i].OutlineEntry.Scenes...)
	}
	return out
}

func updateAdaptationPlanOutlineEntries(plan *domain.AdaptationPlan, entries []domain.OutlineEntry) error {
	if plan == nil {
		return nil
	}
	byChapter := make(map[int]domain.OutlineEntry, len(entries))
	for _, entry := range entries {
		byChapter[entry.Chapter] = entry
	}

	updated := 0
	for i := range plan.Chapters {
		entry, ok := byChapter[plan.Chapters[i].Chapter]
		if !ok {
			continue
		}
		plan.Chapters[i].Title = entry.Title
		plan.Chapters[i].OutlineEntry.Title = entry.Title
		plan.Chapters[i].OutlineEntry.CoreEvent = entry.CoreEvent
		plan.Chapters[i].OutlineEntry.Hook = entry.Hook
		plan.Chapters[i].OutlineEntry.Scenes = append([]string(nil), entry.Scenes...)
		updated++
	}
	if updated != len(entries) {
		return fmt.Errorf("adaptation plan missing %d repaired outline chapters", len(entries)-updated)
	}
	return nil
}
