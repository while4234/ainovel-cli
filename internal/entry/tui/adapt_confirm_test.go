package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
)

func TestAdaptModeConfirmDefaultsToPreserveDetailsChapter(t *testing.T) {
	state := newAdaptModeConfirmState("source.txt")

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
	for _, want := range []string{"granularity=chapter", "rewrite_policy=preserve_details", "word_tolerance=0.15"} {
		if !strings.Contains(plan.RawPrompt, want) {
			t.Fatalf("plan brief missing %q:\n%s", want, plan.RawPrompt)
		}
	}

	rendered := renderAdaptModeConfirmModal(100, 30, state)
	for _, want := range []string{"选择章节结构", "chapter", "arc", "free", "chapter => preserve_details"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("confirm modal missing %q:\n%s", want, rendered)
		}
	}
}

func TestAdaptModeConfirmSelectsGranularityInOneStep(t *testing.T) {
	state := newAdaptModeConfirmState("source.txt")
	if final := state.selectNumber('2'); !final {
		t.Fatal("granularity selection should finish")
	}
	if state.selectedGranularity() != domain.AdaptationGranularityArc {
		t.Fatalf("granularity=%s", state.selectedGranularity())
	}
	if state.selectedRewritePolicy() != domain.AdaptationRewriteFullRewrite {
		t.Fatalf("rewrite policy=%s", state.selectedRewritePolicy())
	}
}

func TestAdaptModeConfirmStartsCoCreateWithSelectedMode(t *testing.T) {
	state := newAdaptModeConfirmState("source.txt")
	state.granularity = 1

	m := NewModel(&host.Host{}, nil, "")
	m.adaptConfirm = state

	next, cmd := m.handleAdaptModeConfirmKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(Model)
	if got.adaptConfirm != nil {
		t.Fatalf("mode selection should close: %+v", got.adaptConfirm)
	}
	if got.cocreate == nil || !got.cocreate.adapt {
		t.Fatalf("adapt co-create should start after mode selection: %+v", got.cocreate)
	}
	if got.cocreate.adaptGranularity != domain.AdaptationGranularityArc {
		t.Fatalf("granularity=%s", got.cocreate.adaptGranularity)
	}
	if got.cocreate.adaptRewritePolicy != domain.AdaptationRewriteFullRewrite {
		t.Fatalf("rewrite policy=%s", got.cocreate.adaptRewritePolicy)
	}
	if !strings.Contains(got.cocreate.initialInput(), "granularity=arc") ||
		!strings.Contains(got.cocreate.initialInput(), "rewrite_policy=full_rewrite") {
		t.Fatalf("initial co-create input missing selected mode:\n%s", got.cocreate.initialInput())
	}
	if cmd == nil {
		t.Fatal("mode selection should kick off co-create")
	}
}

func TestAdaptCoCreateBuildPlanUsesPreselectedMode(t *testing.T) {
	state := newAdaptCoCreateStateWithOptions(
		"source.txt",
		domain.AdaptationGranularityArc,
		domain.AdaptationRewritePreserveDetails,
		0.2,
	)
	state.apply(host.CoCreateReply{
		Prompt: "## 用户目标\n\n- 强化女主互动，主线不要走偏。",
		Ready:  true,
	})

	plan, err := state.buildPlan()
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	if plan.AdaptGranularity != domain.AdaptationGranularityArc {
		t.Fatalf("granularity=%s", plan.AdaptGranularity)
	}
	if plan.AdaptRewritePolicy != domain.AdaptationRewriteFullRewrite {
		t.Fatalf("rewrite policy=%s", plan.AdaptRewritePolicy)
	}
	if plan.AdaptWordTolerance != 0.2 {
		t.Fatalf("word tolerance=%v", plan.AdaptWordTolerance)
	}
	if !strings.Contains(plan.RawPrompt, "强化女主互动") {
		t.Fatalf("plan should use co-create brief, got:\n%s", plan.RawPrompt)
	}
}

func TestAdaptModeConfirmFreeFixesFullRewrite(t *testing.T) {
	state := newAdaptModeConfirmState("source.txt")
	state.granularity = 2

	rendered := renderAdaptModeConfirmModal(100, 30, state)
	for _, want := range []string{"选择章节结构", "free => full_rewrite"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("confirm modal missing %q:\n%s", want, rendered)
		}
	}
}
