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

func TestFoundationRevisionUsesExistingOriginalPlanningRepairRouteWithFence(t *testing.T) {
	state := State{
		RevisionActive: true, RevisionMode: domain.RevisionModeFoundation,
		RevisionRoute:        &domain.RevisionRoute{Agent: "architect_long", Task: "fallback", SessionID: "foundation-revision", Revision: 7, Generation: 11},
		Progress:             &domain.Progress{Phase: domain.PhaseOutline},
		PlanningReview:       &domain.PlanningReview{Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindVolumeSplit},
		OriginalPlanningWork: &storepkg.OriginalPlanningWork{Kind: "repair_arc", Volume: 2, Arc: 1, FromChapter: 9, ToChapter: 12, Audit: &domain.OriginalPlanningAudit{Summary: "Foundation changed"}},
	}
	got := Route(state)
	if got == nil || got.Agent != "architect_long" || !strings.Contains(got.Task, "repair_arc") {
		t.Fatalf("Foundation repair route = %+v", got)
	}
	if got.Fence.SessionID != "foundation-revision" || got.Fence.Revision != 7 || got.Fence.Generation != 11 {
		t.Fatalf("Foundation repair fence = %+v", got.Fence)
	}
}

func TestRouteOriginalPlanningBuildsFoundationBeforeAnyOutline(t *testing.T) {
	state := State{PlanningReview: &domain.PlanningReview{
		Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindFoundation,
		FoundationStatus: domain.FoundationReviewStatusCollecting, FoundationGeneration: 2,
		FoundationSections: []string{"premise"}, FoundationFeedback: "make the hard rules explicit",
	}}
	instruction := routeOriginalPlanning(state)
	if instruction == nil || instruction.Agent != "character" || !strings.Contains(instruction.Task, `"mode":"analyze"`) {
		t.Fatalf("instruction = %+v", instruction)
	}

	signature := strings.Repeat("a", 64)
	state.CharacterBinding = domain.CharacterCardBinding{
		Candidate: domain.CharacterCardCandidateReference{
			FoundationRevision:       1,
			FoundationAuditSignature: signature,
			CharacterContentDigest:   signature,
		},
		InputDigest: signature,
	}
	state.CharacterCandidate = &domain.CharacterCardCandidate{Version: domain.CharacterCardCandidateVersion}
	state.CharacterLifecycle = &domain.CharacterCardLifecycle{
		AnalysisStatus:      domain.CharacterCardAnalysisCandidateReady,
		ReviewStatus:        domain.CharacterCardReviewNotReviewed,
		ConfirmationStatus:  domain.CharacterCardUnconfirmed,
		ReviewedCandidate:   state.CharacterBinding.Candidate,
		ReviewedInputDigest: state.CharacterBinding.InputDigest,
	}
	instruction = routeOriginalPlanning(state)
	if instruction == nil || instruction.Agent != "character" || !strings.Contains(instruction.Task, `"mode":"review"`) {
		t.Fatalf("review instruction = %+v", instruction)
	}
	state.CharacterLifecycle.ReviewStatus = domain.CharacterCardReviewNeedsRevision
	if instruction = routeOriginalPlanning(state); instruction != nil {
		t.Fatalf("needs-revision candidate should wait for user/Character revision, got %+v", instruction)
	}
	state.CharacterLifecycle.ReviewStatus = domain.CharacterCardReviewPassed
	if instruction = routeOriginalPlanning(state); instruction != nil {
		t.Fatalf("passing unconfirmed candidate should wait for user confirmation, got %+v", instruction)
	}
	state.CharacterLifecycle.ConfirmationStatus = domain.CharacterCardConfirmed
	state.PlanningReview.FoundationSections = []string{"premise", "characters", "planned_relationships"}
	instruction = routeOriginalPlanning(state)
	if instruction == nil || instruction.Agent != "architect_long" {
		t.Fatalf("post-confirmation instruction = %+v", instruction)
	}
	for _, want := range []string{"world_rules", "foundation_generation=2", "foundation_base_revision=0", "make the hard rules explicit", "do not generate any outline"} {
		if !strings.Contains(instruction.Task, want) {
			t.Fatalf("task missing %q: %s", want, instruction.Task)
		}
	}
}

func TestRouteOriginalPlanningChapterAuditCarriesStableScopeAndRepairLocation(t *testing.T) {
	state := State{
		Progress:       &domain.Progress{Phase: domain.PhaseOutline},
		PlanningReview: &domain.PlanningReview{Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindVolumeSplit},
		OriginalPlanningWork: &storepkg.OriginalPlanningWork{
			Kind: "audit_chapter", Volume: 2, Arc: 3, FromChapter: 9, ToChapter: 9,
		},
	}

	got := Route(state)
	if got == nil || got.Agent != "editor" {
		t.Fatalf("chapter audit route = %+v", got)
	}
	for _, want := range []string{
		"第2卷第3弧", "scope_id=当前章节稳定ID", "volume=2", "arc=3",
		"from_volume=0", "from_chapter=9", "issues=[]", "issues 首项必须填写 volume=2、arc=3",
	} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("chapter audit task missing %q: %s", want, got.Task)
		}
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

func TestRouteOriginalPlanningUsesScopedDetailAndReviewContexts(t *testing.T) {
	tests := []struct {
		name string
		work *storepkg.OriginalPlanningWork
		want string
	}{
		{
			name: "expand arc",
			work: &storepkg.OriginalPlanningWork{Kind: "expand_arc", Volume: 2, Arc: 3, FromChapter: 17, ToChapter: 20},
			want: "novel_context(scope=planning_detail, volume=2, arc=3)",
		},
		{
			name: "audit arc",
			work: &storepkg.OriginalPlanningWork{Kind: "audit_arc", Volume: 4, Arc: 1, FromChapter: 37, ToChapter: 40},
			want: "novel_context(scope=planning_detail, volume=4, arc=1)",
		},
		{
			name: "audit volume",
			work: &storepkg.OriginalPlanningWork{Kind: "audit_volume", Volume: 5},
			want: "novel_context(scope=planning_review, volume=5)",
		},
		{
			name: "audit book batch",
			work: &storepkg.OriginalPlanningWork{Kind: "audit_book_batch", FromVolume: 7, ToVolume: 8},
			want: "novel_context(scope=planning_review, from_volume=7, to_volume=8)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := State{
				Progress:             &domain.Progress{Phase: domain.PhaseOutline},
				PlanningReview:       &domain.PlanningReview{Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindVolumeSplit},
				OriginalPlanningWork: tt.work,
			}
			got := Route(state)
			if got == nil || !strings.Contains(got.Task, tt.want) {
				t.Fatalf("route = %+v, want scoped context %q", got, tt.want)
			}
			if strings.Contains(got.Task, "novel_context(scope=planning)") {
				t.Fatalf("route retained generic planning scope: %s", got.Task)
			}
		})
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
	largeEvidence := strings.Repeat(`{"scope":"skeleton_book_batch","summary":"durable audit evidence"},`, 2000)
	state := State{
		Progress:             &domain.Progress{Phase: domain.PhaseOutline, Layered: true, TotalChapters: 33},
		PlanningReview:       &domain.PlanningReview{Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindBlueprint},
		SkeletonPlanningWork: &storepkg.OriginalPlanningWork{Kind: "audit_skeleton_book", Evidence: largeEvidence},
	}
	got := Route(state)
	if got == nil || got.Agent != "editor" || !strings.Contains(got.Task, "skeleton_book") || !strings.Contains(got.Task, "终卷没有真正结束全书") {
		t.Fatalf("skeleton final audit route = %+v", got)
	}
	if strings.Contains(got.Task, largeEvidence[:256]) || len(got.Task) > 4096 {
		t.Fatalf("skeleton final audit task repeated durable evidence: %d bytes", len(got.Task))
	}
}

func TestRouteOriginalPlanningBatchAuditReferencesDurableEvidence(t *testing.T) {
	largeEvidence := strings.Repeat(`{"scope":"skeleton_volume","summary":"durable audit evidence"},`, 2000)
	state := State{
		Progress:       &domain.Progress{Phase: domain.PhaseOutline, Layered: true, TotalChapters: 33},
		PlanningReview: &domain.PlanningReview{Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindBlueprint},
		SkeletonPlanningWork: &storepkg.OriginalPlanningWork{
			Kind: "audit_skeleton_book_batch", FromVolume: 7, ToVolume: 8, Evidence: largeEvidence,
		},
	}
	got := Route(state)
	if got == nil || got.Agent != "editor" || !strings.Contains(got.Task, "from_volume=7, to_volume=8") {
		t.Fatalf("skeleton batch audit route = %+v", got)
	}
	if strings.Contains(got.Task, largeEvidence[:256]) || len(got.Task) > 4096 {
		t.Fatalf("skeleton batch audit task repeated durable evidence: %d bytes", len(got.Task))
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
