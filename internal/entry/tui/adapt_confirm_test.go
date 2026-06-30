package tui

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
)

func TestAdaptModeConfirmDefaultsToPreserveDetailsChapter(t *testing.T) {
	cocreate := newAdaptCoCreateState("source.txt")
	cocreate.apply(host.CoCreateReply{
		Prompt: "## 改编模式\n\n用户希望细节优先。",
		Ready:  true,
	})
	state := newAdaptModeConfirmState(cocreate)

	if state.selectedGranularity() != domain.AdaptationGranularityChapter {
		t.Fatalf("granularity=%s", state.selectedGranularity())
	}
	if state.selectedRewritePolicy() != domain.AdaptationRewritePreserveDetails {
		t.Fatalf("rewrite policy=%s", state.selectedRewritePolicy())
	}
	if state.selectedTolerance() != 0.15 {
		t.Fatalf("tolerance=%v", state.selectedTolerance())
	}

	plan, err := state.buildPlan()
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	if plan.AdaptGranularity != domain.AdaptationGranularityChapter || plan.AdaptRewritePolicy != domain.AdaptationRewritePreserveDetails || plan.AdaptWordTolerance != 0.15 {
		t.Fatalf("plan options mismatch: %+v", plan)
	}

	rendered := renderAdaptModeConfirmModal(100, 30, state)
	for _, want := range []string{"chapter", "preserve_details", "±15%"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("confirm modal missing %q:\n%s", want, rendered)
		}
	}
}
