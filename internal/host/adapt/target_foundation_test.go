package adapt

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestTargetFoundationCheckpointPreservesSourceAndConfirmedCast(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	seedPreparedAdaptationSource(t, st, []int{40, 60})
	sourceBefore, err := st.Adaptation.LoadSourceFoundation()
	if err != nil {
		t.Fatal(err)
	}
	review, err := st.Adaptation.LoadTargetFoundationReview()
	if err != nil || review == nil || review.State != domain.AdaptationFoundationReviewApproved {
		t.Fatalf("review=%+v err=%v", review, err)
	}
	target, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	contract, err := st.CoreCast.Load()
	if err != nil || contract == nil {
		t.Fatalf("contract=%+v err=%v", contract, err)
	}
	if err := domain.ValidateFoundationPreservesCoreCast(target, *contract); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(target.Premise, "目标改编决策") || !strings.Contains(target.Premise, "原著事实依据（只读）") {
		t.Fatalf("target premise does not separate evidence and decisions: %s", target.Premise)
	}
	if sourceAfter, err := st.Adaptation.LoadSourceFoundation(); err != nil || !reflect.DeepEqual(sourceBefore, sourceAfter) {
		t.Fatalf("source foundation changed: before=%+v after=%+v err=%v", sourceBefore, sourceAfter, err)
	}
}

func TestTargetFoundationRevisionBlocksSkeletonUntilReconfirmed(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	seedPreparedAdaptationSource(t, st, []int{50})
	if _, err := st.MarkAdaptationTargetFoundationPending("change the target cost"); err != nil {
		t.Fatal(err)
	}
	_, err := BuildAdaptationProposalContext(context.Background(), Deps{Store: st}, ProposalOptions{
		Brief: "short adaptation", Granularity: domain.AdaptationGranularityChapter,
	})
	if err == nil || !strings.Contains(err.Error(), "target foundation") {
		t.Fatalf("unconfirmed target foundation allowed skeleton: %v", err)
	}
}
