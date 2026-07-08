package flow

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestLoadStateUsesArcReviewCheckpointWhenReviewChapterDiffers(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{
		{
			Index: 1,
			Arcs: []domain.ArcOutline{
				{
					Index: 1,
					Chapters: []domain.OutlineEntry{
						{Title: "one"},
						{Title: "two"},
						{Title: "three"},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("save layered outline: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		Layered:           true,
		CompletedChapters: []int{1, 2, 3},
	}); err != nil {
		t.Fatalf("save progress: %v", err)
	}
	if _, err := st.Checkpoints.Append(domain.ArcScope(1, 1), "review", "reviews/01.json", "sha256:review"); err != nil {
		t.Fatalf("append review checkpoint: %v", err)
	}

	state := LoadState(st)

	if state.ArcBoundary == nil || !state.ArcBoundary.IsArcEnd {
		t.Fatalf("expected arc-end boundary, got %+v", state.ArcBoundary)
	}
	if !state.HasArcReview {
		t.Fatal("expected arc review to be recognized from arc-scope checkpoint")
	}
}

func TestLoadStateIncludesOutlineRepairBatch(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Arcs: []domain.ArcOutline{
			{
				Index: 1,
				Chapters: []domain.OutlineEntry{
					{Title: "Shared Promise", CoreEvent: "The team enters the archive and finds the sealed ledger before dawn.", Hook: "The ledger names the missing witness."},
					{Title: "Shared Promise", CoreEvent: "The team enters the archive and finds the sealed ledger before dawn.", Hook: "The ledger names the missing witness."},
					{Title: "同题", CoreEvent: "同事件", Hook: "同钩子"},
				},
			},
			{
				Index: 2,
				Chapters: []domain.OutlineEntry{
					{Title: "同题", CoreEvent: "同事件", Hook: "同钩子"},
				},
			},
		},
	}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("save layered outline: %v", err)
	}
	if err := st.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("save outline: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		Layered:           true,
		CompletedChapters: []int{1, 2},
	}); err != nil {
		t.Fatalf("save progress: %v", err)
	}

	state := LoadState(st)

	if state.OutlineRepair == nil || !state.OutlineRepair.Repairable() {
		t.Fatalf("expected outline repair batch, got %+v", state.OutlineRepair)
	}
	if state.OutlineRepair.Volume != 1 || state.OutlineRepair.Arc != 1 {
		t.Fatalf("expected V1 A1, got %+v", state.OutlineRepair)
	}
}
