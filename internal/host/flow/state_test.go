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
