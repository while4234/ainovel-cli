package host

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestFoundationRevisionServiceExplicitlyIsolatesAdaptationPreview(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SaveSourceFoundation(domain.AdaptationSourceFoundation{Premise: "immutable source"}); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(st.Dir(), "meta", "adaptation", "plan.json")
	if err := os.WriteFile(planPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := NewFoundationRevisionService(st)
	_, err := service.Preview(FoundationPreviewRequest{})
	var classified *FoundationRevisionError
	if !errors.As(err, &classified) || classified.Code != FoundationErrorModeNotEnabled {
		t.Fatalf("preview error = %T %v", err, err)
	}
}

func TestFoundationRevisionServiceTreatsWritingAsReadonly(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, CurrentChapter: 1}); err != nil {
		t.Fatal(err)
	}
	state, err := NewFoundationRevisionService(st).State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Editable || state.ReadonlyReason != "body_started" {
		t.Fatalf("state = %+v", state)
	}
}

func TestFoundationRevisionServiceTreatsPersistedDraftAsBodyStarted(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseOutline}); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveDraft(1, "persisted prose"); err != nil {
		t.Fatal(err)
	}
	state, err := NewFoundationRevisionService(st).State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Editable || state.ReadonlyReason != "body_started" {
		t.Fatalf("state = %+v", state)
	}
}

func TestFoundationRevisionRequiresCoreCastReconfirmationBeforeApply(t *testing.T) {
	st, base := newConfirmedFoundationRevisionStore(t)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Characters[0].Goal = "a different core goal"
	baseAudit, _ := domain.FoundationAuditSignature(base)
	preview, err := NewFoundationRevisionService(st).Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: baseAudit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if preview.CanApply || !preview.Impact.RequiresCoreCastConfirmation || len(preview.Validation.Warnings) == 0 {
		t.Fatalf("core-cast preview=%+v", preview)
	}
}

func TestFoundationRevisionApplyRetryDoesNotPublishTwice(t *testing.T) {
	st, base := newConfirmedFoundationRevisionStore(t)
	service := NewFoundationRevisionService(st)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Premise = "A revised central premise"
	baseAudit, err := domain.FoundationAuditSignature(base)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: baseAudit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanApply || !preview.Impact.FullBook {
		t.Fatalf("preview = %+v", preview)
	}
	service.SetApplyHookForTesting(func(stage string) error {
		if stage == "after_publication" {
			return errors.New("injected regeneration failure")
		}
		return nil
	})
	if _, err := service.Apply(FoundationApplyRequest{PreviewID: preview.ID, IdempotencyKey: "apply-once"}); err == nil {
		t.Fatal("injected failure was ignored")
	}
	published, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if published.Revision != base.Revision+1 {
		t.Fatalf("published revision=%d want=%d", published.Revision, base.Revision+1)
	}
	service.SetApplyHookForTesting(nil)
	retried, err := service.Retry("retry-once")
	if err != nil {
		t.Fatal(err)
	}
	if retried.Stage != "regenerating" {
		t.Fatalf("retry stage=%s", retried.Stage)
	}
	replayed, err := service.Retry("retry-once")
	if err != nil || replayed.RevisionID != retried.RevisionID {
		t.Fatalf("retry replay=%+v err=%v", replayed, err)
	}
	afterRetry, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if afterRetry.Revision != published.Revision {
		t.Fatalf("retry republished Foundation: %d -> %d", published.Revision, afterRetry.Revision)
	}
	active, err := st.Revisions.Active()
	if err != nil || active == nil || active.ID != retried.SessionID {
		t.Fatalf("active revision=%+v err=%v", active, err)
	}
	if active.Stage != domain.RevisionStageCandidateGenerating || len(active.Approvals) != 1 || active.Approvals[0].StageID != "foundation_apply" || active.Route == nil {
		t.Fatalf("Foundation planning session did not remain active: %+v", active)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil || review == nil || review.Status != domain.PlanningReviewStatusCollecting {
		t.Fatalf("planning repair review=%+v err=%v", review, err)
	}
}

func TestFoundationRevisionApplyIdempotencyReturnsSameRuntime(t *testing.T) {
	st, base := newConfirmedFoundationRevisionStore(t)
	service := NewFoundationRevisionService(st)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Premise = "A revised central premise"
	baseAudit, _ := domain.FoundationAuditSignature(base)
	preview, err := service.Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: baseAudit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Apply(FoundationApplyRequest{PreviewID: preview.ID, IdempotencyKey: "same-key"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Apply(FoundationApplyRequest{PreviewID: preview.ID, IdempotencyKey: "same-key"})
	if err != nil {
		t.Fatal(err)
	}
	if first.RevisionID != second.RevisionID || first.Publication.FoundationRevision != second.Publication.FoundationRevision {
		t.Fatalf("idempotent results differ: %+v %+v", first, second)
	}
}

func TestFoundationRevisionRouteLaunchFailureRetriesSameSession(t *testing.T) {
	st, base := newConfirmedFoundationRevisionStore(t)
	service := NewFoundationRevisionService(st)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Premise = "route launch retry premise"
	baseAudit, _ := domain.FoundationAuditSignature(base)
	preview, err := service.Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: baseAudit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Apply(FoundationApplyRequest{PreviewID: preview.ID, IdempotencyKey: "route-launch"})
	if err != nil {
		t.Fatal(err)
	}
	publishedRevision := runtime.Publication.FoundationRevision
	if err := service.MarkRegenerationFailure(errors.New("router launch failed")); err != nil {
		t.Fatal(err)
	}
	failed, err := st.Revisions.Active()
	if err != nil || failed == nil || failed.ID != runtime.SessionID || failed.Stage != domain.RevisionStageFailed || failed.ResumeStage != domain.RevisionStageCandidateGenerating {
		t.Fatalf("failed session=%+v err=%v", failed, err)
	}
	retried, err := service.Retry("route-launch-retry")
	if err != nil || retried.SessionID != runtime.SessionID || retried.Stage != "regenerating" {
		t.Fatalf("retried=%+v err=%v", retried, err)
	}
	foundation, err := st.Foundation.Load()
	if err != nil || foundation.Revision != publishedRevision {
		t.Fatalf("route retry republished Foundation: revision=%d want=%d err=%v", foundation.Revision, publishedRevision, err)
	}
}

func TestFoundationRevisionAwaitsExistingOutlineApprovalBeforeCompletion(t *testing.T) {
	st, base := newConfirmedFoundationRevisionStore(t)
	volumes := []domain.VolumeOutline{{ID: domain.LegacyStructureID("foundation-route", domain.StructureKindVolume, "v1"), Index: 1, Title: "Volume 1", Theme: "repair", Arcs: []domain.ArcOutline{{ID: domain.LegacyStructureID("foundation-route", domain.StructureKindArc, "v1-a1"), Index: 1, Title: "Arc 1", Goal: "repair", EstimatedChapters: 1, Chapters: []domain.OutlineEntry{{ID: domain.LegacyStructureID("foundation-route", domain.StructureKindChapter, "c1"), Chapter: 1, Title: "Chapter 1", CoreEvent: "event", Scenes: []string{"scene"}, Hook: "hook"}}}}}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	service := NewFoundationRevisionService(st)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Premise = "A revised premise requiring planning repair"
	baseAudit, _ := domain.FoundationAuditSignature(base)
	preview, err := service.Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: baseAudit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Apply(FoundationApplyRequest{PreviewID: preview.ID, IdempotencyKey: "approval-boundary"})
	if err != nil || runtime.Stage != "regenerating" {
		t.Fatalf("apply runtime=%+v err=%v", runtime, err)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil || review == nil {
		t.Fatalf("planning review=%+v err=%v", review, err)
	}
	review.Status = domain.PlanningReviewStatusPending
	review.Kind = domain.PlanningReviewKindVolumeSplit
	if err := st.RunMeta.SetPlanningReview(review); err != nil {
		t.Fatal(err)
	}
	state, err := service.State()
	if err != nil || state.ActiveRevision.Stage != "awaiting_outline_approval" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	active, err := st.Revisions.Active()
	if err != nil || active == nil || active.Stage != domain.RevisionStageApprovalPending || len(active.AuditExpectations) != 1 || active.AuditExpectations[0].Scope != "planning" {
		t.Fatalf("active planning approval=%+v err=%v", active, err)
	}
	if err := service.ApproveOutline(); err != nil {
		t.Fatal(err)
	}
	if active, err := st.Revisions.Active(); err != nil || active != nil {
		t.Fatalf("completed revision remained active: %+v err=%v", active, err)
	}
}

func TestFoundationRevisionConcurrentApplyCreatesOneActiveRevision(t *testing.T) {
	st, base := newConfirmedFoundationRevisionStore(t)
	service := NewFoundationRevisionService(st)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Premise = "A revised central premise"
	baseAudit, _ := domain.FoundationAuditSignature(base)
	preview, err := service.Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: baseAudit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, key := range []string{"concurrent-a", "concurrent-b"} {
		wait.Add(1)
		go func(key string) {
			defer wait.Done()
			_, applyErr := NewFoundationRevisionService(storepkg.NewStore(st.Dir())).Apply(FoundationApplyRequest{PreviewID: preview.ID, IdempotencyKey: key})
			results <- applyErr
		}(key)
	}
	wait.Wait()
	close(results)
	successes := 0
	for applyErr := range results {
		if applyErr == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent applies=%d want=1", successes)
	}
	active, err := st.Revisions.Active()
	if err != nil || active == nil {
		t.Fatalf("active=%+v err=%v", active, err)
	}
}

func newConfirmedFoundationRevisionStore(t *testing.T) (*storepkg.Store, domain.StoryFoundation) {
	t.Helper()
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoreCast.SaveGateBinding(storepkg.CoreCastGateBinding{Mode: domain.CoreCastModeNormal, DraftRevision: 1, DraftHash: "draft-hash"}); err != nil {
		t.Fatal(err)
	}
	contract := domain.CoreCastContract{Version: domain.CoreCastContractVersion, Mode: domain.CoreCastModeNormal, DraftRevision: 1, DraftHash: "draft-hash", Members: []domain.CoreCastMember{{Character: domain.Character{ID: "hero", Name: "Hero", Role: "lead", Description: "hero", Goal: "save home", Motivation: "duty", Conflict: "fear", Arc: "lead", Traits: []string{"brave"}, Constraints: []string{"will not betray friends"}}, Importance: domain.CoreCastImportanceProtagonist, Origin: domain.CoreCastOriginOriginal, MainlineFunction: "drives the mainline", NoCoreRelationships: true}}}
	saved, err := st.CoreCast.SaveCAS(contract, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CoreCast.ConfirmCAS(saved.Revision, saved.ContentSignature, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoreCast.PublishConfirmed(st.Foundation, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	review := &domain.PlanningReview{Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindBlueprint, Brief: "plan"}
	if _, err := st.BeginFoundationReview(review); err != nil {
		t.Fatal(err)
	}
	fence := &storepkg.FoundationGenerationFence{Generation: review.FoundationGeneration, BaseRevision: review.FoundationBaseRevision}
	if _, err := st.SaveFoundationPremise(fence, "An original premise"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFoundationCharacters(fence, contractCharactersForFoundationTest(contract)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFoundationRelationships(fence, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFoundationWorldRules(fence, []domain.WorldRule{{ID: "rule-1", Category: "society", Rule: "Promises bind", Boundary: "No free escape", Strength: domain.WorldRuleStrengthHard}}); err != nil {
		t.Fatal(err)
	}
	formal, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	audit, _ := domain.FoundationAuditSignature(formal)
	if _, err := st.ConfirmFoundation(formal.Revision, audit); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseOutline, Layered: true}); err != nil {
		t.Fatal(err)
	}
	formal, err = st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	return st, formal
}

func contractCharactersForFoundationTest(contract domain.CoreCastContract) []domain.Character {
	return domain.ContractCharacters(contract)
}
