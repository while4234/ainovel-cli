package flow

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestRouteOriginalPlanningAlternatesGenerationAndIndependentAudit(t *testing.T) {
	base := State{
		Progress:       &domain.Progress{Phase: domain.PhaseOutline},
		PlanningReview: &domain.PlanningReview{Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindVolumeSplit},
	}
	base.OriginalPlanningWork = &storepkg.OriginalPlanningWork{Kind: "expand_arc", Volume: 2, Arc: 1, FromChapter: 9, ToChapter: 12}
	generated := Route(base)
	if generated == nil || generated.Agent != "architect_long" || !strings.Contains(generated.Task, "不超过4章") {
		t.Fatalf("generation route = %+v", generated)
	}
	base.OriginalPlanningWork = &storepkg.OriginalPlanningWork{Kind: "audit_arc", Volume: 2, Arc: 1, FromChapter: 9, ToChapter: 12}
	audit := Route(base)
	if audit == nil || audit.Agent != "editor" || !strings.Contains(audit.Task, "save_original_planning_audit") || !strings.Contains(audit.Task, "禁止扩大本批范围") {
		t.Fatalf("audit route = %+v", audit)
	}
}

func TestRouteOriginalPlanningBookAuditUsesDigestOnly(t *testing.T) {
	state := State{
		Progress:             &domain.Progress{Phase: domain.PhaseOutline},
		PlanningReview:       &domain.PlanningReview{Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindVolumeSplit},
		OriginalPlanningWork: &storepkg.OriginalPlanningWork{Kind: "audit_book", Evidence: `[{"scope":"book_batch","summary":"V1-V2 pass"}]`},
	}
	got := Route(state)
	if got == nil || got.Agent != "editor" || !strings.Contains(got.Task, "禁止加载全书原始细纲") {
		t.Fatalf("book audit route = %+v", got)
	}
}

func TestRouteOriginalPlanningLocksFoundationAfterFirstSkeletonVolume(t *testing.T) {
	state := State{
		Progress:       &domain.Progress{Phase: domain.PhaseOutline, Layered: true, TotalChapters: 11},
		PlanningReview: &domain.PlanningReview{Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindBlueprint},
	}
	got := Route(state)
	if got == nil || got.Agent != "architect_long" {
		t.Fatalf("route = %+v", got)
	}
	if !strings.Contains(got.Task, "append_volume") || !strings.Contains(got.Task, "exactly once") {
		t.Fatalf("route must append exactly one volume: %s", got.Task)
	}
	if strings.Contains(got.Task, "先补齐 premise") {
		t.Fatalf("route must not regenerate persisted foundation: %s", got.Task)
	}
}

func TestRouteOriginalPlanningRequestsOnlyMissingFoundation(t *testing.T) {
	state := State{
		Progress:          &domain.Progress{Phase: domain.PhasePremise},
		PlanningReview:    &domain.PlanningReview{Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindBlueprint},
		FoundationMissing: []string{"outline", "characters", "world_rules"},
	}
	got := Route(state)
	if got == nil {
		t.Fatal("expected missing-foundation route")
	}
	if !strings.Contains(got.Task, "characters, world_rules") {
		t.Fatalf("missing list not preserved: %s", got.Task)
	}
	if strings.Contains(got.Task, "premise, characters") {
		t.Fatalf("route must not request an existing premise: %s", got.Task)
	}
}

func TestRouteOriginalPlanningCompletesCompassBeforeAppendingVolume(t *testing.T) {
	state := State{
		Progress:          &domain.Progress{Phase: domain.PhaseOutline, Layered: true, TotalChapters: 33},
		PlanningReview:    &domain.PlanningReview{Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindBlueprint},
		FoundationMissing: []string{"compass"},
	}
	got := Route(state)
	if got == nil || !strings.Contains(got.Task, "update_compass") {
		t.Fatalf("expected compass route, got %+v", got)
	}
	if strings.Contains(got.Task, "append_volume") {
		t.Fatalf("compass route must not append another volume: %s", got.Task)
	}
}

func TestRouteOriginalPlanningMarksBudgetCompletingAppendAsFinal(t *testing.T) {
	state := State{
		Progress:             &domain.Progress{Phase: domain.PhaseOutline, Layered: true, TotalChapters: 22},
		PlanningReview:       &domain.PlanningReview{Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindBlueprint},
		BlueprintVolumeCount: 2,
		BlueprintNextIsFinal: true,
	}
	got := Route(state)
	if got == nil || !strings.Contains(got.Task, "FINAL volume") || !strings.Contains(got.Task, "must close every promised main plot") {
		t.Fatalf("budget-completing append must carry a hard ending contract: %+v", got)
	}
}

func TestRouteOriginalPlanningAuditsSkeletonBeforeUserReview(t *testing.T) {
	state := State{
		Progress:             &domain.Progress{Phase: domain.PhaseOutline, Layered: true, TotalChapters: 33},
		PlanningReview:       &domain.PlanningReview{Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindBlueprint},
		SkeletonPlanningWork: &storepkg.OriginalPlanningWork{Kind: "audit_skeleton_book", Evidence: `[{"scope":"skeleton_book_batch"}]`},
	}
	got := Route(state)
	if got == nil || got.Agent != "editor" || !strings.Contains(got.Task, "skeleton_book") || !strings.Contains(got.Task, "终卷没有真正结束全书") {
		t.Fatalf("skeleton final audit route = %+v", got)
	}
}

func TestRouteOriginalPlanningRepairsRejectedSkeletonVolume(t *testing.T) {
	state := State{
		Progress:       &domain.Progress{Phase: domain.PhaseOutline, Layered: true, TotalChapters: 33},
		PlanningReview: &domain.PlanningReview{Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindBlueprint},
		SkeletonPlanningWork: &storepkg.OriginalPlanningWork{
			Kind: "repair_skeleton_volume", Volume: 3,
			Audit: &domain.OriginalPlanningAudit{Scope: "skeleton_book", Verdict: "revise"},
		},
	}
	got := Route(state)
	if got == nil || got.Agent != "architect_long" || !strings.Contains(got.Task, "repair_volume") || !strings.Contains(got.Task, "第3卷") {
		t.Fatalf("skeleton repair route = %+v", got)
	}
}
