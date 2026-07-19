package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

const (
	FoundationErrorStale          = "foundation_stale"
	FoundationErrorReadonly       = "foundation_readonly"
	FoundationErrorBusy           = "foundation_busy"
	FoundationErrorInvalid        = "foundation_invalid_candidate"
	FoundationErrorEvidence       = "foundation_dependency_evidence_missing"
	FoundationErrorRecovery       = "foundation_recovery_failed"
	FoundationErrorModeNotEnabled = "foundation_mode_not_enabled"
)

type FoundationRevisionError struct {
	Code string
	Err  error
}

func (e *FoundationRevisionError) Error() string {
	if e == nil || e.Err == nil {
		return "foundation revision failed"
	}
	return e.Err.Error()
}
func (e *FoundationRevisionError) Unwrap() error { return e.Err }

type FoundationState struct {
	Mode               string                            `json:"mode"`
	SourceFoundation   any                               `json:"source_foundation,omitempty"`
	TargetFoundation   domain.StoryFoundation            `json:"target_foundation"`
	Editable           bool                              `json:"editable"`
	ReadonlyReason     string                            `json:"readonly_reason,omitempty"`
	BaseRevision       int64                             `json:"base_revision"`
	BaseAuditSignature string                            `json:"base_audit_signature"`
	CoreCastSignature  string                            `json:"core_cast_signature"`
	ActiveRevision     *domain.FoundationRevisionRuntime `json:"active_revision,omitempty"`
	PlanningReview     *domain.PlanningReview            `json:"planning_review,omitempty"`
	AllowedOperations  []string                          `json:"allowed_operations"`
}

type FoundationPreviewRequest struct {
	ExpectedBaseRevision       int64                  `json:"expected_base_revision"`
	ExpectedBaseAuditSignature string                 `json:"expected_base_audit_signature"`
	Candidate                  domain.StoryFoundation `json:"candidate"`
}

type FoundationApplyRequest struct {
	PreviewID      string `json:"preview_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type FoundationRevisionService struct {
	store *storepkg.Store
	hook  func(string) error
}

func NewFoundationRevisionService(st *storepkg.Store) *FoundationRevisionService {
	return &FoundationRevisionService{store: st}
}
func (s *FoundationRevisionService) SetApplyHookForTesting(hook func(string) error) { s.hook = hook }

func (s *FoundationRevisionService) State() (*FoundationState, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("foundation revision store is required")
	}
	target, err := s.store.Foundation.Load()
	if err != nil {
		return nil, err
	}
	auditSignature, err := domain.FoundationAuditSignature(target)
	if err != nil {
		return nil, err
	}
	contract, err := s.store.CoreCast.Load()
	if err != nil {
		return nil, err
	}
	mode := "normal"
	var source any
	if s.store.Adaptation.Exists() {
		mode = "adaptation"
		source, err = s.store.Adaptation.LoadSourceFoundation()
		if err != nil {
			return nil, err
		}
	}
	state := &FoundationState{Mode: mode, SourceFoundation: source, TargetFoundation: target, BaseRevision: target.Revision, BaseAuditSignature: auditSignature, AllowedOperations: []string{"get"}}
	if contract != nil {
		state.CoreCastSignature = contract.ContentSignature
	}
	state.PlanningReview, err = s.store.RunMeta.PlanningReview()
	if err != nil {
		return nil, err
	}
	state.ActiveRevision, err = s.store.FoundationRevisions.LoadRuntime()
	if err != nil {
		return nil, err
	}
	if state.ActiveRevision != nil {
		if err := s.reconcilePlanningRuntime(state.ActiveRevision, state.PlanningReview); err != nil {
			return nil, err
		}
	}
	state.Editable, state.ReadonlyReason = s.editability(mode, state.PlanningReview, state.ActiveRevision)
	if state.Editable {
		state.AllowedOperations = []string{"get", "preview", "apply"}
	}
	if state.ActiveRevision != nil && state.ActiveRevision.Stage == "failed" {
		state.AllowedOperations = append(state.AllowedOperations, "retry")
	}
	return state, nil
}

func (s *FoundationRevisionService) Preview(request FoundationPreviewRequest) (*domain.FoundationRevisionPreview, error) {
	state, err := s.State()
	if err != nil {
		return nil, err
	}
	if state.Mode == "adaptation" {
		return nil, foundationError(FoundationErrorModeNotEnabled, "adaptation Foundation revision will be enabled in PR-06")
	}
	if request.ExpectedBaseRevision != state.BaseRevision || strings.TrimSpace(request.ExpectedBaseAuditSignature) != state.BaseAuditSignature {
		return nil, foundationError(FoundationErrorStale, "Foundation base revision or audit signature changed")
	}
	normalized, normalizeErr := domain.NormalizeStoryFoundation(request.Candidate)
	validation := domain.FoundationPreviewValidation{Valid: normalizeErr == nil}
	if normalizeErr != nil {
		validation.Errors = []string{normalizeErr.Error()}
		normalized = request.Candidate
	}
	contract, err := s.store.CoreCast.Load()
	if err != nil {
		return nil, err
	}
	diff, err := domain.ComputeFoundationDiff(state.TargetFoundation, normalized, contract)
	if err != nil {
		return nil, foundationError(FoundationErrorInvalid, err.Error())
	}
	if normalizeErr == nil && !diff.CoreCastReconfirmation && contract != nil {
		if err := domain.ValidateFoundationComplete(normalized, *contract); err != nil {
			validation.Valid = false
			validation.Errors = append(validation.Errors, err.Error())
		}
	} else if normalizeErr == nil && (strings.TrimSpace(normalized.Premise) == "" || len(normalized.Characters) == 0 || len(normalized.WorldRules) == 0) {
		validation.Valid = false
		validation.Errors = append(validation.Errors, "premise, characters, and world rules are required")
	}
	dependencies, dependencyErr := s.store.FoundationRevisions.LoadDependencies()
	if dependencyErr != nil {
		validation.Warnings = append(validation.Warnings, dependencyErr.Error())
		dependencies = nil
	}
	if dependencies != nil && dependencies.FoundationSignature != state.BaseAuditSignature {
		validation.Warnings = append(validation.Warnings, "dependency manifest is stale for the current Foundation")
		dependencies = nil
	}
	if dependencies != nil {
		if err := s.store.ValidateFoundationDependencies(*dependencies); err != nil {
			validation.Warnings = append(validation.Warnings, err.Error())
			dependencies = nil
		}
	}
	if diff.CoreCastReconfirmation {
		validation.Warnings = append(validation.Warnings, "confirm the revised CoreCastContract before applying this Foundation candidate")
	}
	impact, err := domain.AnalyzeFoundationImpact(diff, dependencies)
	if err != nil {
		return nil, err
	}
	fence, err := s.store.Revisions.SnapshotFence()
	if err != nil {
		return nil, err
	}
	planningSignature := foundationPlanningSignature(state.PlanningReview)
	candidateSignature, err := domain.FoundationContentSignature(normalized)
	if err != nil {
		return nil, foundationError(FoundationErrorInvalid, err.Error())
	}
	now := time.Now().UTC()
	preview := domain.FoundationRevisionPreview{
		Version: domain.FoundationRevisionSchemaVersion, ProjectMode: state.Mode, BaseRevision: state.BaseRevision,
		BaseAuditSignature: state.BaseAuditSignature, BaseCoreCastSignature: state.CoreCastSignature, BasePlanningSignature: planningSignature,
		Generation: fence.Generation, Base: state.TargetFoundation, Candidate: normalized, CandidateSignature: candidateSignature, Diff: diff, Impact: impact,
		Validation: validation, CanApply: state.Editable && validation.Valid && !diff.CoreCastReconfirmation && len(diff.Changes) > 0,
		ReadonlyReason: state.ReadonlyReason, CreatedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339Nano),
	}
	if dependencies != nil {
		preview.DependencySnapshotSignature = dependencies.Signature
	}
	preview.ID = "foundation-preview-" + storepkg.FoundationRevisionFingerprint(struct {
		Base                    int64
		Candidate, Diff, Impact string
	}{preview.BaseRevision, preview.CandidateSignature, preview.Diff.Signature, preview.Impact.Signature})[:20]
	preview = domain.SignFoundationRevisionPreview(preview)
	if err := s.store.FoundationRevisions.SavePreview(preview); err != nil {
		return nil, err
	}
	return &preview, nil
}

func (s *FoundationRevisionService) Apply(request FoundationApplyRequest) (*domain.FoundationRevisionRuntime, error) {
	if strings.TrimSpace(request.PreviewID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		return nil, foundationError(FoundationErrorInvalid, "preview_id and idempotency_key are required")
	}
	fingerprint := storepkg.FoundationRevisionFingerprint(struct{ PreviewID string }{strings.TrimSpace(request.PreviewID)})
	if receipt, found, err := s.store.FoundationRevisions.LoadReceipt(request.IdempotencyKey, fingerprint); found || err != nil {
		return receipt, err
	}
	preview, err := s.store.FoundationRevisions.LoadPreview(request.PreviewID)
	if err != nil {
		return nil, foundationError(FoundationErrorStale, err.Error())
	}
	if preview.ProjectMode != "normal" {
		return nil, foundationError(FoundationErrorModeNotEnabled, "adaptation Foundation revision will be enabled in PR-06")
	}
	if !preview.CanApply {
		return nil, foundationError(FoundationErrorReadonly, "persisted Foundation preview is not applicable")
	}
	if err := s.validatePreviewCurrent(*preview); err != nil {
		return nil, err
	}
	revisionImpact, err := domain.FoundationRevisionImpact(preview.Impact)
	if err != nil {
		return nil, err
	}
	session, err := s.store.Revisions.Start(domain.FoundationRevisionPolicy{}, storepkg.StartRevisionInput{Intent: "apply canonical Foundation preview " + preview.ID, Impact: revisionImpact, PreviewSignature: preview.Signature, IdempotencyKey: "foundation/start/" + request.IdempotencyKey})
	if err != nil {
		return nil, foundationError(FoundationErrorBusy, err.Error())
	}
	session, err = s.store.Revisions.ApproveImpact(domain.FoundationRevisionPolicy{}, storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "foundation/impact/" + request.IdempotencyKey})
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(preview.Candidate)
	session, err = s.store.Revisions.SubmitCandidate(domain.FoundationRevisionPolicy{}, storepkg.SubmitRevisionCandidateInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "foundation/candidate/" + request.IdempotencyKey, Artifacts: []storepkg.CandidateArtifactInput{{ArtifactID: domain.FoundationArtifactID, ArtifactKind: domain.FoundationArtifactKind, Payload: payload}}})
	if err != nil {
		return nil, err
	}
	now := domain.RevisionTimestamp()
	runtime := domain.FoundationRevisionRuntime{Version: domain.FoundationRevisionSchemaVersion, RevisionID: session.ID, SessionID: session.ID, PreviewID: preview.ID, ProjectMode: "normal", Stage: "applying", Attempt: 1, Generation: session.Generation, Impact: preview.Impact, CreatedAt: now, UpdatedAt: now}
	if err := s.store.FoundationRevisions.SaveRuntime(runtime); err != nil {
		return nil, err
	}
	result, err := s.continueApply(&runtime, preview)
	if err != nil {
		s.failRuntime(&runtime, err)
		return nil, err
	}
	if err := s.store.FoundationRevisions.SaveReceipt(request.IdempotencyKey, fingerprint, *result); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *FoundationRevisionService) Retry(idempotencyKey string) (*domain.FoundationRevisionRuntime, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return nil, foundationError(FoundationErrorInvalid, "idempotency_key is required")
	}
	runtime, err := s.store.FoundationRevisions.LoadRuntime()
	if err != nil {
		return nil, err
	}
	runtimeID := ""
	if runtime != nil {
		runtimeID = runtime.RevisionID
	}
	fingerprint := storepkg.FoundationRevisionFingerprint(struct{ RevisionID string }{runtimeID})
	if receipt, found, receiptErr := s.store.FoundationRevisions.LoadReceipt(idempotencyKey, fingerprint); found || receiptErr != nil {
		return receipt, receiptErr
	}
	if runtime == nil || runtime.Stage != "failed" {
		return nil, foundationError(FoundationErrorRecovery, "no failed active Foundation revision can be retried")
	}
	preview, err := s.store.FoundationRevisions.LoadPreview(runtime.PreviewID)
	if err != nil {
		return nil, foundationError(FoundationErrorRecovery, err.Error())
	}
	session, err := s.store.Revisions.LoadSession(runtime.SessionID)
	if err != nil {
		return nil, foundationError(FoundationErrorRecovery, err.Error())
	}
	if session.Stage == domain.RevisionStageFailed {
		if _, err := s.store.Revisions.Resume(domain.FoundationRevisionPolicy{}, storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "foundation/retry/resume/" + idempotencyKey}); err != nil {
			return nil, err
		}
	}
	runtime.Attempt++
	runtime.Stage, runtime.LastError, runtime.LastErrorClass = runtime.ResumeStage, "", ""
	runtime.UpdatedAt = domain.RevisionTimestamp()
	if err := s.store.FoundationRevisions.SaveRuntime(*runtime); err != nil {
		return nil, err
	}
	result, err := s.continueApply(runtime, preview)
	if err != nil {
		s.failRuntime(runtime, err)
		return nil, err
	}
	if err := s.store.FoundationRevisions.SaveReceipt(idempotencyKey, fingerprint, *result); err != nil {
		return nil, err
	}
	return result, nil
}

// MarkRegenerationFailure records a failure to launch or continue the existing
// normal planning router after canonical publication. Retry resumes the same
// durable boundary and never republishes the Foundation candidate.
func (s *FoundationRevisionService) MarkRegenerationFailure(cause error) error {
	if cause == nil {
		return nil
	}
	runtime, err := s.store.FoundationRevisions.LoadRuntime()
	if err != nil {
		return err
	}
	if runtime == nil || runtime.Stage != "regenerating" {
		return foundationError(FoundationErrorRecovery, "no regenerating Foundation revision can be failed")
	}
	runtime.ResumeStage = "regenerating"
	s.failRuntime(runtime, cause)
	return nil
}

func (s *FoundationRevisionService) ApproveOutline() error {
	runtime, err := s.store.FoundationRevisions.LoadRuntime()
	if err != nil || runtime == nil {
		return errors.Join(fmt.Errorf("Foundation revision runtime is missing"), err)
	}
	review, err := s.store.RunMeta.PlanningReview()
	if err != nil {
		return err
	}
	if err := s.reconcilePlanningRuntime(runtime, review); err != nil {
		return err
	}
	session, err := s.store.Revisions.LoadSession(runtime.SessionID)
	if err != nil {
		return err
	}
	if session.Stage != domain.RevisionStageApprovalPending || len(session.Approvals) != 1 {
		return fmt.Errorf("Foundation outline revision is not awaiting approval")
	}
	stage := session.CurrentApprovalStage()
	if stage == nil || stage.ID != "outline_approval" {
		return fmt.Errorf("Foundation outline approval stage is missing")
	}
	policy := domain.FoundationRevisionPolicy{}
	session, err = s.store.Revisions.ApproveStage(policy, storepkg.RevisionApprovalInput{
		RevisionMutationInput: storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "foundation/outline-approval/" + runtime.PreviewID},
		StageID:               stage.ID,
	})
	if err != nil {
		return err
	}
	if _, err := s.store.Revisions.Publish(policy, storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "foundation/complete/" + runtime.PreviewID}); err != nil {
		return err
	}
	runtime.Stage, runtime.ResumeStage, runtime.UpdatedAt = "completed", "", domain.RevisionTimestamp()
	return s.store.FoundationRevisions.SaveRuntime(*runtime)
}

func (s *FoundationRevisionService) reconcilePlanningRuntime(runtime *domain.FoundationRevisionRuntime, review *domain.PlanningReview) error {
	if runtime == nil || runtime.Stage != "regenerating" || review == nil || review.Status != domain.PlanningReviewStatusPending {
		return nil
	}
	if review.Kind != domain.PlanningReviewKindVolumeSplit && review.Kind != domain.PlanningReviewKindChapterOutline {
		return nil
	}
	session, err := s.store.Revisions.LoadSession(runtime.SessionID)
	if err != nil {
		return err
	}
	policy := domain.FoundationRevisionPolicy{}
	if session.Stage == domain.RevisionStageCandidateGenerating && len(session.Approvals) == 1 {
		volumes, err := s.store.Outline.LoadLayeredOutline()
		if err != nil {
			return err
		}
		payload, err := json.Marshal(volumes)
		if err != nil {
			return err
		}
		session, err = s.store.Revisions.SubmitCandidate(policy, storepkg.SubmitRevisionCandidateInput{
			SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "foundation/planning-candidate/" + runtime.PreviewID,
			Artifacts: []storepkg.CandidateArtifactInput{{ArtifactID: domain.FoundationPlanningArtifactID, ArtifactKind: domain.FoundationPlanningArtifactKind, Payload: payload}},
		})
		if err != nil {
			return err
		}
	}
	if session.Stage == domain.RevisionStageCandidateAudit && len(session.Approvals) == 1 {
		if len(session.AuditExpectations) != 1 {
			return fmt.Errorf("Foundation planning audit expectation is missing")
		}
		expected := session.AuditExpectations[0]
		session, err = s.store.Revisions.RecordAudit(policy, storepkg.RevisionAuditInput{
			RevisionMutationInput: storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "foundation/planning-audit/" + runtime.PreviewID},
			CandidateSignature:    session.CandidateSignature,
			Evidence:              []domain.RevisionAuditEvidence{{Scope: expected.Scope, ScopeID: expected.ScopeID, ContentSignature: expected.ContentSignature, Passed: true, Report: "required original-planning audits passed"}},
		})
		if err != nil {
			return err
		}
	}
	if session.Stage != domain.RevisionStageApprovalPending || len(session.Approvals) != 1 {
		return fmt.Errorf("Foundation planning revision stopped at %q", session.Stage)
	}
	runtime.Stage, runtime.ResumeStage, runtime.UpdatedAt = "awaiting_outline_approval", "", domain.RevisionTimestamp()
	return s.store.FoundationRevisions.SaveRuntime(*runtime)
}

func (s *FoundationRevisionService) continueApply(runtime *domain.FoundationRevisionRuntime, preview *domain.FoundationRevisionPreview) (*domain.FoundationRevisionRuntime, error) {
	if runtime.Publication == nil {
		if err := s.runHook("before_publication"); err != nil {
			return nil, err
		}
		current, err := s.store.Foundation.Load()
		if err != nil {
			return nil, err
		}
		currentSignature, err := domain.FoundationContentSignature(current)
		if err != nil {
			return nil, err
		}
		var saved domain.StoryFoundation
		if currentSignature == preview.CandidateSignature {
			saved = current
		} else {
			if current.Revision != preview.BaseRevision {
				return nil, foundationError(FoundationErrorStale, "Foundation changed before publication")
			}
			saved, err = s.store.Foundation.SaveRevisionCAS(preview.Candidate, preview.BaseRevision)
			if err != nil {
				return nil, err
			}
		}
		runtime.Publication = &domain.FoundationPublicationReceipt{Status: "published", CandidateSignature: preview.CandidateSignature, FoundationRevision: saved.Revision, PublishedAt: domain.RevisionTimestamp()}
		runtime.Stage, runtime.UpdatedAt = "auditing", domain.RevisionTimestamp()
		if err := s.store.FoundationRevisions.SaveRuntime(*runtime); err != nil {
			return nil, err
		}
	}
	if err := s.runHook("after_publication"); err != nil {
		return nil, err
	}
	dependencies, _ := s.store.FoundationRevisions.LoadDependencies()
	if dependencies != nil && dependencies.Signature != preview.DependencySnapshotSignature {
		dependencies = nil
	}
	if err := s.store.OriginalPlanningAudits.InvalidateFoundationRevision(preview.Base, preview.Candidate, preview.Impact, dependencies); err != nil {
		return nil, err
	}
	if preview.Diff.CoreCastReconfirmation {
		if contract, err := s.store.CoreCast.Load(); err != nil {
			return nil, err
		} else if contract != nil && contract.ConfirmedSignature != "" {
			if _, err := s.store.CoreCast.UnconfirmCAS(contract.Revision); err != nil {
				return nil, err
			}
		}
	}
	if err := s.store.MarkFoundationRevisionPending(); err != nil {
		return nil, err
	}
	if err := s.runHook("after_invalidation"); err != nil {
		return nil, err
	}
	if err := s.approveFoundationApply(runtime.SessionID, preview); err != nil {
		return nil, err
	}
	if preview.Impact.RequiresCoreCastConfirmation {
		session, err := s.store.Revisions.LoadSession(runtime.SessionID)
		if err != nil {
			return nil, err
		}
		if session.Stage == domain.RevisionStageCandidateGenerating {
			if _, err := s.store.Revisions.Pause(domain.FoundationRevisionPolicy{}, storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "foundation/core-cast-pause/" + preview.ID}); err != nil {
				return nil, err
			}
		}
		runtime.Stage, runtime.ResumeStage, runtime.UpdatedAt = "awaiting_core_cast_confirmation", "regenerating", domain.RevisionTimestamp()
		if err := s.store.FoundationRevisions.SaveRuntime(*runtime); err != nil {
			return nil, err
		}
		return runtime, nil
	}
	auditSignature, err := domain.FoundationAuditSignature(preview.Candidate)
	if err != nil {
		return nil, err
	}
	review, _, err := s.store.ConfirmFoundationForPlanning(runtime.Publication.FoundationRevision, auditSignature)
	if err != nil {
		return nil, err
	}
	kind, err := s.store.OriginalPlanningAudits.QueueFoundationRepair(preview.Impact)
	if err != nil {
		return nil, err
	}
	if review == nil {
		return nil, fmt.Errorf("planning review disappeared after Foundation confirmation")
	}
	review.Kind = kind
	review.Status = domain.PlanningReviewStatusCollecting
	review.UpdatedAt = domain.RevisionTimestamp()
	if err := s.store.RunMeta.SetPlanningReview(review); err != nil {
		return nil, err
	}
	runtime.Stage, runtime.ResumeStage, runtime.UpdatedAt = "regenerating", "", domain.RevisionTimestamp()
	if err := s.store.FoundationRevisions.SaveRuntime(*runtime); err != nil {
		return nil, err
	}
	return runtime, nil
}

func (s *FoundationRevisionService) approveFoundationApply(sessionID string, preview *domain.FoundationRevisionPreview) error {
	session, err := s.store.Revisions.LoadSession(sessionID)
	if err != nil {
		return err
	}
	policy := domain.FoundationRevisionPolicy{}
	if session.Stage == domain.RevisionStageCandidateAudit && len(session.Approvals) == 0 {
		if len(session.AuditExpectations) != 1 {
			return fmt.Errorf("Foundation candidate audit expectation is missing")
		}
		expected := session.AuditExpectations[0]
		session, err = s.store.Revisions.RecordAudit(policy, storepkg.RevisionAuditInput{
			RevisionMutationInput: storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "foundation/apply-audit/" + preview.ID},
			CandidateSignature:    session.CandidateSignature,
			Evidence:              []domain.RevisionAuditEvidence{{Scope: expected.Scope, ScopeID: expected.ScopeID, ContentSignature: expected.ContentSignature, Passed: true, Report: "canonical Foundation validation passed"}},
		})
		if err != nil {
			return err
		}
	}
	if session.Stage == domain.RevisionStageApprovalPending && len(session.Approvals) == 0 {
		stage := session.CurrentApprovalStage()
		if stage == nil || stage.ID != "foundation_apply" {
			return fmt.Errorf("Foundation apply approval stage is missing")
		}
		_, err = s.store.Revisions.ApproveStage(policy, storepkg.RevisionApprovalInput{
			RevisionMutationInput: storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "foundation/apply-approval/" + preview.ID},
			StageID:               stage.ID,
		})
		return err
	}
	if len(session.Approvals) != 1 || session.Stage != domain.RevisionStageCandidateGenerating {
		return fmt.Errorf("Foundation revision did not enter planning repair")
	}
	return nil
}

func (s *FoundationRevisionService) validatePreviewCurrent(preview domain.FoundationRevisionPreview) error {
	state, err := s.State()
	if err != nil {
		return err
	}
	if !state.Editable {
		return foundationError(FoundationErrorReadonly, state.ReadonlyReason)
	}
	if state.BaseRevision != preview.BaseRevision || state.BaseAuditSignature != preview.BaseAuditSignature || state.CoreCastSignature != preview.BaseCoreCastSignature || foundationPlanningSignature(state.PlanningReview) != preview.BasePlanningSignature {
		return foundationError(FoundationErrorStale, "persisted Foundation preview is stale")
	}
	fence, err := s.store.Revisions.SnapshotFence()
	if err != nil {
		return err
	}
	if fence.Generation != preview.Generation || fence.SessionID != "" || fence.LeaseToken != "" {
		return foundationError(FoundationErrorBusy, "project revision/action ownership changed")
	}
	if expires, err := time.Parse(time.RFC3339Nano, preview.ExpiresAt); err != nil || !time.Now().UTC().Before(expires) {
		return foundationError(FoundationErrorStale, "Foundation preview expired")
	}
	return nil
}

func (s *FoundationRevisionService) editability(mode string, review *domain.PlanningReview, runtime *domain.FoundationRevisionRuntime) (bool, string) {
	if mode == "adaptation" {
		return false, "adaptation_foundation_revision_not_enabled"
	}
	progress, err := s.store.Progress.Load()
	if err != nil {
		return false, "progress_unavailable"
	}
	if progress != nil {
		if progress.Phase == domain.PhaseComplete {
			return false, "project_complete"
		}
		if progress.Phase == domain.PhaseWriting || progress.CurrentChapter > 0 || progress.InProgressChapter > 0 || len(progress.CompletedChapters) > 0 {
			return false, "body_started"
		}
		if progress.Phase != domain.PhaseOutline {
			return false, "planning_stage_not_editable"
		}
	}
	if s.bodyFilesStarted() {
		return false, "body_started"
	}
	if review == nil || (review.Kind != domain.PlanningReviewKindBlueprint && review.Kind != domain.PlanningReviewKindVolumeSplit && review.Kind != domain.PlanningReviewKindChapterOutline) {
		return false, "planning_stage_not_editable"
	}
	if runtime != nil && runtime.Active() {
		return false, "active_foundation_revision"
	}
	active, err := s.store.Revisions.Active()
	if err != nil {
		return false, "revision_state_unavailable"
	}
	if active != nil {
		return false, "active_revision"
	}
	if err := s.store.RequireConfirmedFoundation(); err != nil {
		return false, "foundation_confirmation_invalid"
	}
	return true, ""
}

func (s *FoundationRevisionService) bodyFilesStarted() bool {
	for _, directory := range []string{"chapters", "drafts"} {
		root := filepath.Join(s.store.Dir(), directory)
		found := false
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(data)) != "" {
				found = true
				return filepath.SkipAll
			}
			return nil
		})
		if found || (err != nil && !os.IsNotExist(err)) {
			return true
		}
	}
	volumes, err := s.store.Outline.LoadLayeredOutline()
	if err != nil {
		return true
	}
	chapters := domain.FlattenOutline(volumes)
	if len(chapters) == 0 {
		chapters, err = s.store.Outline.LoadOutline()
		if err != nil {
			return true
		}
	}
	for _, chapter := range chapters {
		if text, err := s.store.Drafts.LoadChapterText(chapter.Chapter); err != nil || strings.TrimSpace(text) != "" {
			return true
		}
		if text, err := s.store.Drafts.LoadDraft(chapter.Chapter); err != nil || strings.TrimSpace(text) != "" {
			return true
		}
	}
	return false
}

func (s *FoundationRevisionService) failRuntime(runtime *domain.FoundationRevisionRuntime, cause error) {
	runtime.ResumeStage = runtime.Stage
	runtime.Stage = "failed"
	runtime.LastErrorClass = FoundationErrorRecovery
	runtime.LastError = cause.Error()
	runtime.UpdatedAt = domain.RevisionTimestamp()
	_ = s.store.FoundationRevisions.SaveRuntime(*runtime)
	if session, err := s.store.Revisions.LoadSession(runtime.SessionID); err == nil && session.Active() && session.Stage != domain.RevisionStageFailed {
		_, _ = s.store.Revisions.Fail(domain.FoundationRevisionPolicy{}, storepkg.RevisionFailureInput{RevisionMutationInput: storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "foundation/fail/" + fmt.Sprint(runtime.Attempt)}, Error: cause.Error()})
	}
}

func (s *FoundationRevisionService) runHook(stage string) error {
	if s.hook != nil {
		return s.hook(stage)
	}
	return nil
}
func foundationPlanningSignature(review *domain.PlanningReview) string {
	payload, _ := json.Marshal(review)
	return domain.ContentSignature(payload)
}
func foundationError(code, message string) error {
	return &FoundationRevisionError{Code: code, Err: errors.New(message)}
}
