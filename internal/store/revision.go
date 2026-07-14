package store

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const revisionStateFile = "meta/revisions/state.json"

var (
	ErrRevisionNotFound               = errors.New("revision session is not found")
	ErrActiveRevisionExists           = errors.New("an active revision already exists")
	ErrNoActiveRevision               = errors.New("no active revision")
	ErrRevisionIdempotencyConflict    = errors.New("revision idempotency key was reused for a different command")
	ErrActiveRevisionBlocksNormalFlow = errors.New("normal flow is blocked by an active revision")
	revisionLocks                     sync.Map
)

type RevisionConflictError struct {
	Expected int
	Actual   int
}

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("revision conflict: expected %d, actual %d", e.Expected, e.Actual)
}

func IsRevisionConflict(err error) bool {
	var conflict *RevisionConflictError
	return errors.As(err, &conflict)
}

type revisionReceipt struct {
	Operation   string                 `json:"operation"`
	Fingerprint string                 `json:"fingerprint"`
	Result      domain.RevisionSession `json:"result"`
}

type revisionState struct {
	Version          int                               `json:"version"`
	Generation       uint64                            `json:"generation"`
	NormalLease      *NormalFlowLease                  `json:"normal_lease,omitempty"`
	ActiveSessionID  string                            `json:"active_session_id,omitempty"`
	NextSession      int                               `json:"next_session"`
	NextVersion      int                               `json:"next_version"`
	Sessions         map[string]domain.RevisionSession `json:"sessions"`
	Versions         map[string]domain.ArtifactVersion `json:"versions"`
	CurrentArtifacts map[string]string                 `json:"current_artifacts"`
	Receipts         map[string]revisionReceipt        `json:"receipts"`
}

type RevisionStore struct {
	io *IO
	mu *sync.Mutex
}

type NormalFlowLease struct {
	Token      string `json:"token"`
	Generation uint64 `json:"generation"`
	Owner      string `json:"owner"`
	PID        int    `json:"pid"`
	AcquiredAt string `json:"acquired_at"`
}

type RevisionFence struct {
	Generation uint64
	SessionID  string
	Revision   int
	LeaseToken string
}

type revisionFenceContextKey struct{}

// ContextWithRevisionFence binds asynchronous work to the ownership epoch
// that dispatched it. Writable tool boundaries must revalidate this fence
// immediately before applying their side effects.
func ContextWithRevisionFence(ctx context.Context, fence RevisionFence) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, revisionFenceContextKey{}, fence)
}

func RevisionFenceFromContext(ctx context.Context) (RevisionFence, bool) {
	if ctx == nil {
		return RevisionFence{}, false
	}
	fence, ok := ctx.Value(revisionFenceContextKey{}).(RevisionFence)
	return fence, ok
}

func (s *RevisionStore) AcquireNormalFlow(owner string) (*NormalFlowLease, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, fmt.Errorf("normal flow owner is required")
	}
	var lease *NormalFlowLease
	err := s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if state.ActiveSessionID != "" {
			return ErrActiveRevisionBlocksNormalFlow
		}
		if state.NormalLease != nil {
			return fmt.Errorf("normal flow is already leased by %q", state.NormalLease.Owner)
		}
		tokenBytes := make([]byte, 16)
		if _, err := rand.Read(tokenBytes); err != nil {
			return err
		}
		state.Generation++
		lease = &NormalFlowLease{
			Token: fmt.Sprintf("%x", tokenBytes), Generation: state.Generation,
			Owner: owner, PID: os.Getpid(), AcquiredAt: domain.RevisionTimestamp(),
		}
		state.NormalLease = lease
		return s.io.WriteJSON(revisionStateFile, state)
	})
	if err != nil {
		return nil, err
	}
	copy := *lease
	return &copy, nil
}

func (s *RevisionStore) ReleaseNormalFlow(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	return s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if state.NormalLease == nil {
			return nil
		}
		if state.NormalLease.Token != token {
			return fmt.Errorf("normal flow lease token is stale")
		}
		state.NormalLease = nil
		state.Generation++
		return s.io.WriteJSON(revisionStateFile, state)
	})
}

func (s *RevisionStore) FenceForNormalFlow(token string) (RevisionFence, error) {
	var fence RevisionFence
	err := s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if state.ActiveSessionID != "" || state.NormalLease == nil || state.NormalLease.Token != strings.TrimSpace(token) {
			return ErrActiveRevisionBlocksNormalFlow
		}
		fence = RevisionFence{Generation: state.Generation, LeaseToken: state.NormalLease.Token}
		return nil
	})
	return fence, err
}

func (s *RevisionStore) SnapshotFence() (RevisionFence, error) {
	var fence RevisionFence
	err := s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		fence.Generation = state.Generation
		if state.ActiveSessionID != "" {
			session := state.Sessions[state.ActiveSessionID]
			fence.SessionID, fence.Revision = session.ID, session.Revision
		}
		if state.NormalLease != nil {
			fence.LeaseToken = state.NormalLease.Token
		}
		return nil
	})
	return fence, err
}

func (s *RevisionStore) WithFence(fence RevisionFence, fn func() error) error {
	return s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if state.Generation != fence.Generation {
			return fmt.Errorf("revision generation fence is stale")
		}
		if fence.LeaseToken != "" {
			if state.ActiveSessionID != "" || state.NormalLease == nil || state.NormalLease.Token != fence.LeaseToken {
				return ErrActiveRevisionBlocksNormalFlow
			}
		} else {
			session, exists := state.Sessions[fence.SessionID]
			if !exists || state.ActiveSessionID != fence.SessionID || session.Revision != fence.Revision {
				return ErrRevisionNotFound
			}
		}
		return fn()
	})
}

func NewRevisionStore(dir string) *RevisionStore {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	lock, _ := revisionLocks.LoadOrStore(filepath.Clean(abs), &sync.Mutex{})
	return &RevisionStore{io: newIO(dir), mu: lock.(*sync.Mutex)}
}

type StartRevisionInput struct {
	Intent           string
	Impact           domain.RevisionImpact
	PreviewSignature string
	IdempotencyKey   string
}

type CandidateArtifactInput struct {
	ArtifactID   string
	ArtifactKind string
	Payload      json.RawMessage
}

type SubmitRevisionCandidateInput struct {
	SessionID        string
	ExpectedRevision int
	IdempotencyKey   string
	Artifacts        []CandidateArtifactInput
}

type RevisionMutationInput struct {
	SessionID        string
	ExpectedRevision int
	IdempotencyKey   string
}

type RevisionAuditInput struct {
	RevisionMutationInput
	CandidateSignature string
	Passed             bool
	Report             string
	Evidence           []domain.RevisionAuditEvidence
}

type RevisionFeedbackInput struct {
	RevisionMutationInput
	StageID         string
	ImpactSignature string
	Message         string
}

type RevisionApprovalInput struct {
	RevisionMutationInput
	StageID string
}

type RevisionFailureInput struct {
	RevisionMutationInput
	Error string
}

type RestoreArtifactVersionInput struct {
	VersionID      string
	Intent         string
	IdempotencyKey string
}

func (s *RevisionStore) Start(policy domain.RevisionPolicy, input StartRevisionInput) (*domain.RevisionSession, error) {
	intent := strings.TrimSpace(input.Intent)
	if intent == "" {
		return nil, fmt.Errorf("revision intent is required")
	}
	impact, err := normalizeRevisionImpact(input.Impact)
	if err != nil {
		return nil, err
	}
	payload := struct {
		Intent           string
		Impact           domain.RevisionImpact
		PreviewSignature string
	}{intent, impact, strings.TrimSpace(input.PreviewSignature)}
	operation, fingerprint, err := revisionCommandFingerprint(input.IdempotencyKey, "start", payload)
	if err != nil {
		return nil, err
	}
	if receipt, err := s.lookupRevisionReceipt(input.IdempotencyKey, operation, fingerprint); receipt != nil || err != nil {
		return receipt, err
	}
	policyMode, policyID, policyVersion, err := describeRevisionPolicy(policy)
	if err != nil {
		return nil, err
	}
	var working *revisionState
	var generation uint64
	var receipt *domain.RevisionSession
	err = s.withRevisionTransaction(func() error {
		state, loadErr := s.loadUnlocked()
		if loadErr != nil {
			return loadErr
		}
		if found, receiptErr := matchingRevisionReceipt(state, input.IdempotencyKey, operation, fingerprint); found != nil || receiptErr != nil {
			receipt, err = found, receiptErr
			return err
		}
		if state.ActiveSessionID != "" {
			return ErrActiveRevisionExists
		}
		if state.NormalLease != nil {
			return fmt.Errorf("%w: normal flow is still running", ErrActiveRevisionExists)
		}
		generation = state.Generation
		working, err = cloneRevisionState(state)
		return err
	})
	if err != nil || receipt != nil {
		return receipt, err
	}
	// Policy code is deliberately outside both the process and filesystem locks.
	if err := policy.ValidateImpact(cloneRevisionImpact(impact)); err != nil {
		return nil, fmt.Errorf("validate revision impact: %w", err)
	}
	stages, err := validateApprovalStages(policy, cloneRevisionImpact(impact))
	if err != nil {
		return nil, err
	}
	now := domain.RevisionTimestamp()
	working.NextSession++
	working.Generation++
	session := domain.RevisionSession{
		Version: domain.RevisionSchemaVersion, ID: fmt.Sprintf("rev-%06d", working.NextSession), Mode: policyMode,
		Stage: domain.RevisionStageImpactReviewPending, Revision: 1, Generation: working.Generation,
		PolicyID: policyID, PolicyVersion: policyVersion, Intent: intent, Impact: impact,
		PreviewSignature: strings.TrimSpace(input.PreviewSignature),
		ApprovalStages:   stages, Round: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := applyRevisionRoute(policy, &session); err != nil {
		return nil, err
	}
	working.Sessions[session.ID] = session
	working.ActiveSessionID = session.ID
	return s.commitOptimistic(input.IdempotencyKey, operation, fingerprint, generation, working, session)
}

func (s *RevisionStore) ApproveImpact(policy domain.RevisionPolicy, input RevisionMutationInput) (*domain.RevisionSession, error) {
	return s.mutate(policy, input, "approve_impact", input, func(_ *revisionState, session *domain.RevisionSession) error {
		if session.Stage != domain.RevisionStageImpactReviewPending {
			return fmt.Errorf("revision impact can only be approved from %q", domain.RevisionStageImpactReviewPending)
		}
		if len(session.CandidateVersionIDs) > 0 {
			session.Stage = domain.RevisionStageCandidateAudit
		} else {
			session.Stage = domain.RevisionStageCandidateGenerating
		}
		return nil
	})
}

func (s *RevisionStore) SubmitCandidate(policy domain.RevisionPolicy, input SubmitRevisionCandidateInput) (*domain.RevisionSession, error) {
	if len(input.Artifacts) == 0 {
		return nil, fmt.Errorf("revision candidate must contain at least one artifact")
	}
	payload := input
	return s.mutate(policy, input.RevisionMutationInput(), "submit_candidate", payload, func(state *revisionState, session *domain.RevisionSession) error {
		if session.Stage != domain.RevisionStageCandidateGenerating {
			return fmt.Errorf("revision candidate can only be submitted from %q", domain.RevisionStageCandidateGenerating)
		}
		impactKinds := make(map[string]string, len(session.Impact.Items))
		for _, item := range session.Impact.Items {
			impactKinds[item.ArtifactID] = item.ArtifactKind
		}
		seen := make(map[string]struct{}, len(input.Artifacts))
		versions := make([]domain.ArtifactVersion, 0, len(input.Artifacts))
		for _, artifact := range input.Artifacts {
			artifact.ArtifactID = strings.TrimSpace(artifact.ArtifactID)
			artifact.ArtifactKind = strings.TrimSpace(artifact.ArtifactKind)
			kind, inImpact := impactKinds[artifact.ArtifactID]
			if !inImpact || kind != artifact.ArtifactKind {
				return fmt.Errorf("candidate artifact %q is outside the approved impact", artifact.ArtifactID)
			}
			if _, duplicate := seen[artifact.ArtifactID]; duplicate {
				return fmt.Errorf("duplicate candidate artifact %q", artifact.ArtifactID)
			}
			seen[artifact.ArtifactID] = struct{}{}
			if len(artifact.Payload) == 0 || !json.Valid(artifact.Payload) {
				return fmt.Errorf("candidate artifact %q payload must be valid JSON", artifact.ArtifactID)
			}
			state.NextVersion++
			version := domain.ArtifactVersion{
				ID:         fmt.Sprintf("artifact-version-%06d", state.NextVersion),
				ArtifactID: artifact.ArtifactID, ArtifactKind: artifact.ArtifactKind,
				RevisionID: session.ID, ParentVersionID: state.CurrentArtifacts[artifact.ArtifactID],
				Sequence: nextArtifactSequence(state, artifact.ArtifactID), Round: session.Round,
				Payload:          append(json.RawMessage(nil), artifact.Payload...),
				ContentSignature: domain.JSONContentSignature(artifact.Payload), CreatedAt: domain.RevisionTimestamp(),
			}
			versions = append(versions, version)
		}
		policySession, err := cloneRevisionSession(*session)
		if err != nil {
			return err
		}
		canonicalVersions := cloneArtifactVersions(versions)
		policyVersions := cloneArtifactVersions(canonicalVersions)
		if err := policy.ValidateCandidate(*policySession, policyVersions); err != nil {
			return fmt.Errorf("validate revision candidate: %w", err)
		}
		var expectations []domain.RevisionAuditExpectation
		if scoped, ok := policy.(domain.ScopedAuditPolicy); ok {
			expectations, err = scoped.AuditExpectations(*policySession, cloneArtifactVersions(canonicalVersions))
			if err != nil {
				return fmt.Errorf("derive revision audit expectations: %w", err)
			}
			for _, expectation := range expectations {
				if err := expectation.Validate(); err != nil {
					return err
				}
			}
		}
		for _, version := range canonicalVersions {
			state.Versions[version.ID] = version
			session.CandidateVersionIDs = append(session.CandidateVersionIDs, version.ID)
		}
		session.CandidateSignature = domain.CandidateSignature(canonicalVersions)
		session.AuditExpectations = append([]domain.RevisionAuditExpectation(nil), expectations...)
		session.Stage = domain.RevisionStageCandidateAudit
		return nil
	})
}

func (input SubmitRevisionCandidateInput) RevisionMutationInput() RevisionMutationInput {
	return RevisionMutationInput{input.SessionID, input.ExpectedRevision, input.IdempotencyKey}
}

func (s *RevisionStore) RecordAudit(policy domain.RevisionPolicy, input RevisionAuditInput) (*domain.RevisionSession, error) {
	return s.mutate(policy, input.RevisionMutationInput, "record_audit", input, func(_ *revisionState, session *domain.RevisionSession) error {
		if session.Stage != domain.RevisionStageCandidateAudit {
			return fmt.Errorf("revision audit can only be recorded from %q", domain.RevisionStageCandidateAudit)
		}
		if strings.TrimSpace(input.CandidateSignature) == "" || input.CandidateSignature != session.CandidateSignature {
			return fmt.Errorf("revision audit candidate signature mismatch")
		}
		if signed, ok := policy.(domain.SignedAuditSetPolicy); ok {
			if err := signed.ValidateAuditSet(*session, append([]domain.RevisionAuditEvidence(nil), input.Evidence...)); err != nil {
				return fmt.Errorf("validate signed revision audit set: %w", err)
			}
			input.Passed = true
			for _, evidence := range input.Evidence {
				if !evidence.Passed {
					input.Passed = false
					break
				}
			}
		}
		now := domain.RevisionTimestamp()
		if len(input.Evidence) == 0 {
			session.Audits = append(session.Audits, domain.RevisionAudit{
				Round: session.Round, CandidateSignature: session.CandidateSignature,
				Passed: input.Passed, Report: strings.TrimSpace(input.Report), CreatedAt: now,
			})
		} else {
			for _, evidence := range input.Evidence {
				session.Audits = append(session.Audits, domain.RevisionAudit{
					Round: session.Round, CandidateSignature: session.CandidateSignature,
					Scope: evidence.Scope, ScopeID: evidence.ScopeID, FromChapter: evidence.FromChapter,
					ToChapter: evidence.ToChapter, ContentSignature: evidence.ContentSignature,
					Passed: evidence.Passed, Report: strings.TrimSpace(evidence.Report), CreatedAt: now,
				})
			}
		}
		if input.Passed {
			session.Stage = domain.RevisionStageApprovalPending
			return nil
		}
		session.Round++
		session.Stage = domain.RevisionStageCandidateGenerating
		session.CandidateVersionIDs = nil
		session.CandidateSignature = ""
		session.AuditExpectations = nil
		if _, staged := policy.(domain.StagedRevisionPolicy); !staged {
			session.Approvals = nil
		}
		return nil
	})
}

func (s *RevisionStore) SubmitFeedback(policy domain.RevisionPolicy, input RevisionFeedbackInput) (*domain.RevisionSession, error) {
	message := strings.TrimSpace(input.Message)
	if message == "" {
		return nil, fmt.Errorf("revision feedback is required")
	}
	return s.mutate(policy, input.RevisionMutationInput, "submit_feedback", input, func(_ *revisionState, session *domain.RevisionSession) error {
		if _, signed := policy.(domain.SignedAuditSetPolicy); signed &&
			(strings.TrimSpace(input.ImpactSignature) == "" || input.ImpactSignature != session.Impact.Signature) {
			return fmt.Errorf("revision feedback target drift requires a new impact preview")
		}
		if session.Stage != domain.RevisionStageCandidateGenerating && session.Stage != domain.RevisionStageCandidateAudit && session.Stage != domain.RevisionStageApprovalPending {
			return fmt.Errorf("revision feedback is not allowed from stage %q", session.Stage)
		}
		if session.Stage == domain.RevisionStageApprovalPending {
			current := session.CurrentApprovalStage()
			if current == nil || (strings.TrimSpace(input.StageID) != "" && input.StageID != current.ID) {
				return fmt.Errorf("revision feedback stage does not match the pending approval")
			}
		}
		session.Feedback = append(session.Feedback, domain.RevisionFeedback{
			Round: session.Round, StageID: strings.TrimSpace(input.StageID), Message: message, CreatedAt: domain.RevisionTimestamp(),
		})
		session.Round++
		session.Stage = domain.RevisionStageCandidateGenerating
		session.CandidateVersionIDs = nil
		session.CandidateSignature = ""
		session.AuditExpectations = nil
		if _, staged := policy.(domain.StagedRevisionPolicy); !staged {
			session.Approvals = nil
		}
		return nil
	})
}

func (s *RevisionStore) ApproveStage(policy domain.RevisionPolicy, input RevisionApprovalInput) (*domain.RevisionSession, error) {
	return s.mutate(policy, input.RevisionMutationInput, "approve_stage", input, func(_ *revisionState, session *domain.RevisionSession) error {
		if session.Stage != domain.RevisionStageApprovalPending || !session.LatestAuditPassed() {
			return fmt.Errorf("revision candidate must pass audit before approval")
		}
		stage := session.CurrentApprovalStage()
		if stage == nil || input.StageID != stage.ID {
			return fmt.Errorf("revision approval must follow the configured stage order")
		}
		session.Approvals = append(session.Approvals, domain.RevisionApproval{StageID: stage.ID, ApprovedAt: domain.RevisionTimestamp()})
		if len(session.Approvals) == len(session.ApprovalStages) {
			session.Stage = domain.RevisionStageReadyToPublish
			return nil
		}
		if staged, ok := policy.(domain.StagedRevisionPolicy); ok && staged.ContinueAfterApproval(*session, *stage) {
			session.AcceptedVersionIDs = append(session.AcceptedVersionIDs, session.CandidateVersionIDs...)
			session.Round++
			session.Stage = domain.RevisionStageCandidateGenerating
			session.CandidateVersionIDs = nil
			session.CandidateSignature = ""
			session.AuditExpectations = nil
		}
		return nil
	})
}

func (s *RevisionStore) Publish(policy domain.RevisionPolicy, input RevisionMutationInput) (*domain.RevisionSession, error) {
	return s.mutate(policy, input, "publish", input, func(state *revisionState, session *domain.RevisionSession) error {
		versions, err := prepareRevisionPublish(policy, state, session)
		if err != nil {
			return err
		}
		for _, version := range versions {
			state.CurrentArtifacts[version.ArtifactID] = version.ID
		}
		now := domain.RevisionTimestamp()
		session.Stage = domain.RevisionStageCompleted
		session.CompletedAt = now
		state.ActiveSessionID = ""
		return nil
	})
}

// ValidatePublish performs every RevisionStore policy/version/generation check
// without mutating either formal artifacts or revision state. Production
// publication must call this before any formal structure write.
func (s *RevisionStore) ValidatePublish(policy domain.RevisionPolicy, input RevisionMutationInput) ([]domain.ArtifactVersion, error) {
	var versions []domain.ArtifactVersion
	err := s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if state.ActiveSessionID != strings.TrimSpace(input.SessionID) {
			return ErrRevisionNotFound
		}
		session := state.Sessions[state.ActiveSessionID]
		if input.ExpectedRevision != session.Revision {
			return &RevisionConflictError{Expected: input.ExpectedRevision, Actual: session.Revision}
		}
		mode, id, version, err := describeRevisionPolicy(policy)
		if err != nil {
			return err
		}
		if session.Mode != mode || session.PolicyID != id || session.PolicyVersion != version {
			return fmt.Errorf("revision publish policy drift")
		}
		versions, err = prepareRevisionPublish(policy, state, &session)
		return err
	})
	return cloneArtifactVersions(versions), err
}

func prepareRevisionPublish(policy domain.RevisionPolicy, state *revisionState, session *domain.RevisionSession) ([]domain.ArtifactVersion, error) {
	if session.Stage != domain.RevisionStageReadyToPublish || !session.LatestAuditPassed() || len(session.CandidateVersionIDs) == 0 {
		return nil, fmt.Errorf("revision is not ready to publish")
	}
	if err := session.Impact.Validate(); err != nil {
		return nil, fmt.Errorf("revalidate revision impact: %w", err)
	}
	if err := policy.ValidateImpact(cloneRevisionImpact(session.Impact)); err != nil {
		return nil, fmt.Errorf("revalidate revision impact policy: %w", err)
	}
	currentStages, err := validateApprovalStages(policy, cloneRevisionImpact(session.Impact))
	if err != nil {
		return nil, err
	}
	if !approvalStagesEqual(currentStages, session.ApprovalStages) {
		return nil, fmt.Errorf("revision approval stages changed before publish")
	}
	if len(session.Approvals) != len(session.ApprovalStages) {
		return nil, fmt.Errorf("revision is missing ordered approvals")
	}
	currentVersions := make([]domain.ArtifactVersion, 0, len(session.CandidateVersionIDs))
	for _, versionID := range session.CandidateVersionIDs {
		version, exists := state.Versions[versionID]
		if !exists || version.ParentVersionID != state.CurrentArtifacts[version.ArtifactID] || version.Round != session.Round {
			return nil, fmt.Errorf("current candidate version %q is stale or missing", versionID)
		}
		currentVersions = append(currentVersions, version)
	}
	if domain.CandidateSignature(currentVersions) != session.CandidateSignature {
		return nil, fmt.Errorf("revision candidate signature changed before publish")
	}
	versionIDs := append(append([]string(nil), session.AcceptedVersionIDs...), session.CandidateVersionIDs...)
	versionsByArtifact := make(map[string]domain.ArtifactVersion, len(versionIDs))
	artifactOrder := make([]string, 0, len(versionIDs))
	for _, versionID := range versionIDs {
		version, exists := state.Versions[versionID]
		if !exists || version.ParentVersionID != state.CurrentArtifacts[version.ArtifactID] {
			return nil, fmt.Errorf("accepted candidate version %q is stale or missing", versionID)
		}
		if err := version.Validate(); err != nil {
			return nil, err
		}
		if _, exists := versionsByArtifact[version.ArtifactID]; !exists {
			artifactOrder = append(artifactOrder, version.ArtifactID)
		}
		versionsByArtifact[version.ArtifactID] = version
	}
	versions := make([]domain.ArtifactVersion, 0, len(artifactOrder))
	for _, artifactID := range artifactOrder {
		versions = append(versions, versionsByArtifact[artifactID])
	}
	policySession, err := cloneRevisionSession(*session)
	if err != nil {
		return nil, err
	}
	if err := policy.ValidateCandidate(*policySession, cloneArtifactVersions(versions)); err != nil {
		return nil, fmt.Errorf("revalidate revision candidate: %w", err)
	}
	return versions, nil
}

func (s *RevisionStore) Pause(policy domain.RevisionPolicy, input RevisionMutationInput) (*domain.RevisionSession, error) {
	return s.mutate(policy, input, "pause", input, func(_ *revisionState, session *domain.RevisionSession) error {
		if session.Stage == domain.RevisionStagePaused || session.Stage == domain.RevisionStageFailed {
			return fmt.Errorf("revision is already interrupted")
		}
		session.ResumeStage = session.Stage
		session.Stage = domain.RevisionStagePaused
		return nil
	})
}

func (s *RevisionStore) Fail(policy domain.RevisionPolicy, input RevisionFailureInput) (*domain.RevisionSession, error) {
	message := strings.TrimSpace(input.Error)
	if message == "" {
		return nil, fmt.Errorf("revision failure error is required")
	}
	return s.mutate(policy, input.RevisionMutationInput, "fail", input, func(_ *revisionState, session *domain.RevisionSession) error {
		if session.Stage == domain.RevisionStagePaused || session.Stage == domain.RevisionStageFailed {
			return fmt.Errorf("revision is already interrupted")
		}
		session.ResumeStage = session.Stage
		session.Stage = domain.RevisionStageFailed
		session.LastError = message
		return nil
	})
}

func (s *RevisionStore) Resume(policy domain.RevisionPolicy, input RevisionMutationInput) (*domain.RevisionSession, error) {
	return s.mutate(policy, input, "resume", input, func(_ *revisionState, session *domain.RevisionSession) error {
		if session.Stage != domain.RevisionStagePaused && session.Stage != domain.RevisionStageFailed {
			return fmt.Errorf("revision is not paused or failed")
		}
		if !session.ResumeStage.Valid() || session.ResumeStage.Terminal() {
			return fmt.Errorf("revision resume stage is invalid")
		}
		session.Stage = session.ResumeStage
		session.ResumeStage = ""
		session.LastError = ""
		return nil
	})
}

func (s *RevisionStore) Cancel(policy domain.RevisionPolicy, input RevisionMutationInput) (*domain.RevisionSession, error) {
	return s.mutate(policy, input, "cancel", input, func(state *revisionState, session *domain.RevisionSession) error {
		session.Stage = domain.RevisionStageCancelled
		session.ResumeStage = ""
		session.Route = nil
		state.ActiveSessionID = ""
		return nil
	})
}

func (s *RevisionStore) RestoreVersion(policy domain.RevisionPolicy, input RestoreArtifactVersionInput) (*domain.RevisionSession, error) {
	intent := strings.TrimSpace(input.Intent)
	if intent == "" {
		return nil, fmt.Errorf("restore intent is required")
	}
	versionID := strings.TrimSpace(input.VersionID)
	payload := struct {
		VersionID string
		Intent    string
	}{versionID, intent}
	operation, fingerprint, err := revisionCommandFingerprint(input.IdempotencyKey, "restore_version", payload)
	if err != nil {
		return nil, err
	}
	if receipt, err := s.lookupRevisionReceipt(input.IdempotencyKey, operation, fingerprint); receipt != nil || err != nil {
		return receipt, err
	}
	policyMode, policyID, policyVersion, err := describeRevisionPolicy(policy)
	if err != nil {
		return nil, err
	}
	var working *revisionState
	var source domain.ArtifactVersion
	var generation uint64
	var receipt *domain.RevisionSession
	err = s.withRevisionTransaction(func() error {
		state, loadErr := s.loadUnlocked()
		if loadErr != nil {
			return loadErr
		}
		if found, receiptErr := matchingRevisionReceipt(state, input.IdempotencyKey, operation, fingerprint); found != nil || receiptErr != nil {
			receipt, err = found, receiptErr
			return err
		}
		if state.ActiveSessionID != "" {
			return ErrActiveRevisionExists
		}
		if state.NormalLease != nil {
			return fmt.Errorf("%w: normal flow is still running", ErrActiveRevisionExists)
		}
		var exists bool
		source, exists = state.Versions[versionID]
		if !exists {
			return fmt.Errorf("artifact version %q is not found", versionID)
		}
		source.Payload = append(json.RawMessage(nil), source.Payload...)
		generation = state.Generation
		working, err = cloneRevisionState(state)
		return err
	})
	if err != nil || receipt != nil {
		return receipt, err
	}
	impact, err := domain.NewRevisionImpact("Restore a historical artifact version through a new revision", []domain.RevisionImpactItem{{
		ArtifactID: source.ArtifactID, ArtifactKind: source.ArtifactKind,
		Change: "restore historical version " + source.ID, DependencyEvidence: []string{source.ContentSignature},
	}})
	if err != nil {
		return nil, err
	}
	if err := policy.ValidateImpact(cloneRevisionImpact(impact)); err != nil {
		return nil, fmt.Errorf("validate restore impact: %w", err)
	}
	stages, err := validateApprovalStages(policy, impact)
	if err != nil {
		return nil, err
	}
	working.NextSession++
	working.NextVersion++
	working.Generation++
	now := domain.RevisionTimestamp()
	session := domain.RevisionSession{
		Version: domain.RevisionSchemaVersion, ID: fmt.Sprintf("rev-%06d", working.NextSession), Mode: policyMode,
		Stage: domain.RevisionStageImpactReviewPending, Revision: 1, Generation: working.Generation,
		PolicyID: policyID, PolicyVersion: policyVersion, Intent: intent, Impact: impact,
		ApprovalStages: stages, Round: 1, RestoresVersionID: source.ID, CreatedAt: now, UpdatedAt: now,
	}
	candidate := source
	candidate.ID = fmt.Sprintf("artifact-version-%06d", working.NextVersion)
	candidate.RevisionID = session.ID
	candidate.ParentVersionID = working.CurrentArtifacts[source.ArtifactID]
	candidate.Sequence = nextArtifactSequence(working, source.ArtifactID)
	candidate.Round, candidate.CreatedAt = 1, now
	policySession, err := cloneRevisionSession(session)
	if err != nil {
		return nil, err
	}
	if err := policy.ValidateCandidate(*policySession, cloneArtifactVersions([]domain.ArtifactVersion{candidate})); err != nil {
		return nil, fmt.Errorf("validate restore candidate: %w", err)
	}
	working.Versions[candidate.ID] = candidate
	session.CandidateVersionIDs = []string{candidate.ID}
	session.CandidateSignature = domain.CandidateSignature([]domain.ArtifactVersion{candidate})
	if err := applyRevisionRoute(policy, &session); err != nil {
		return nil, err
	}
	working.Sessions[session.ID] = session
	working.ActiveSessionID = session.ID
	return s.commitOptimistic(input.IdempotencyKey, operation, fingerprint, generation, working, session)
}

func (s *RevisionStore) Active() (*domain.RevisionSession, error) {
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	if state.ActiveSessionID == "" {
		return nil, nil
	}
	session, exists := state.Sessions[state.ActiveSessionID]
	if !exists || !session.Active() {
		return nil, fmt.Errorf("revision active-session index is invalid")
	}
	return cloneRevisionSession(session)
}

func (s *RevisionStore) LoadSession(sessionID string) (*domain.RevisionSession, error) {
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	session, exists := state.Sessions[strings.TrimSpace(sessionID)]
	if !exists {
		return nil, ErrRevisionNotFound
	}
	return cloneRevisionSession(session)
}

func (s *RevisionStore) LoadVersion(versionID string) (*domain.ArtifactVersion, error) {
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	version, exists := state.Versions[strings.TrimSpace(versionID)]
	if !exists {
		return nil, fmt.Errorf("artifact version %q is not found", versionID)
	}
	copy := version
	copy.Payload = append(json.RawMessage(nil), version.Payload...)
	return &copy, nil
}

func (s *RevisionStore) CurrentVersion(artifactID string) (*domain.ArtifactVersion, error) {
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	versionID := state.CurrentArtifacts[strings.TrimSpace(artifactID)]
	if versionID == "" {
		return nil, nil
	}
	version := state.Versions[versionID]
	copy := version
	copy.Payload = append(json.RawMessage(nil), version.Payload...)
	return &copy, nil
}

func (s *RevisionStore) ListVersions(artifactID string) ([]domain.ArtifactVersion, error) {
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	artifactID = strings.TrimSpace(artifactID)
	versions := make([]domain.ArtifactVersion, 0)
	for _, version := range state.Versions {
		if version.ArtifactID == artifactID {
			version.Payload = append(json.RawMessage(nil), version.Payload...)
			versions = append(versions, version)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Sequence < versions[j].Sequence })
	return versions, nil
}

func (s *RevisionStore) mutate(
	policy domain.RevisionPolicy,
	input RevisionMutationInput,
	operation string,
	payload any,
	apply func(*revisionState, *domain.RevisionSession) error,
) (*domain.RevisionSession, error) {
	command, fingerprint, err := revisionCommandFingerprint(input.IdempotencyKey, operation, payload)
	if err != nil {
		return nil, err
	}
	if receipt, err := s.lookupRevisionReceipt(input.IdempotencyKey, command, fingerprint); receipt != nil || err != nil {
		return receipt, err
	}
	policyMode, policyID, policyVersion, err := describeRevisionPolicy(policy)
	if err != nil {
		return nil, err
	}
	var working *revisionState
	var generation uint64
	var receipt *domain.RevisionSession
	err = s.withRevisionTransaction(func() error {
		state, loadErr := s.loadUnlocked()
		if loadErr != nil {
			return loadErr
		}
		if found, receiptErr := matchingRevisionReceipt(state, input.IdempotencyKey, command, fingerprint); found != nil || receiptErr != nil {
			receipt, err = found, receiptErr
			return err
		}
		if state.ActiveSessionID == "" {
			return ErrNoActiveRevision
		}
		if strings.TrimSpace(input.SessionID) != state.ActiveSessionID {
			return ErrRevisionNotFound
		}
		session := state.Sessions[state.ActiveSessionID]
		if session.Mode != policyMode || session.PolicyID != policyID || session.PolicyVersion != policyVersion {
			return fmt.Errorf("revision policy %q@%q does not match persisted policy %q@%q", policyID, policyVersion, session.PolicyID, session.PolicyVersion)
		}
		if input.ExpectedRevision != session.Revision {
			return &RevisionConflictError{Expected: input.ExpectedRevision, Actual: session.Revision}
		}
		generation = state.Generation
		working, err = cloneRevisionState(state)
		return err
	})
	if err != nil || receipt != nil {
		return receipt, err
	}
	session := working.Sessions[working.ActiveSessionID]
	if err := apply(working, &session); err != nil {
		return nil, err
	}
	session.Revision++
	working.Generation++
	session.Generation = working.Generation
	session.UpdatedAt = domain.RevisionTimestamp()
	if session.Stage.Terminal() {
		session.Route = nil
	} else if err := applyRevisionRoute(policy, &session); err != nil {
		return nil, err
	}
	if err := session.Validate(); err != nil {
		return nil, err
	}
	working.Sessions[session.ID] = session
	return s.commitOptimistic(input.IdempotencyKey, command, fingerprint, generation, working, session)
}

func revisionCommandFingerprint(idempotencyKey, operation string, payload any) (string, string, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return "", "", fmt.Errorf("revision idempotency key is required")
	}
	fingerprintPayload, err := json.Marshal(payload)
	if err != nil {
		return "", "", err
	}
	return operation, domain.ContentSignature(fingerprintPayload), nil
}

func matchingRevisionReceipt(state *revisionState, key, operation, fingerprint string) (*domain.RevisionSession, error) {
	receipt, exists := state.Receipts[strings.TrimSpace(key)]
	if !exists {
		return nil, nil
	}
	if receipt.Operation != operation || receipt.Fingerprint != fingerprint {
		return nil, ErrRevisionIdempotencyConflict
	}
	return cloneRevisionSession(receipt.Result)
}

func (s *RevisionStore) lookupRevisionReceipt(key, operation, fingerprint string) (*domain.RevisionSession, error) {
	var receipt *domain.RevisionSession
	err := s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		receipt, err = matchingRevisionReceipt(state, key, operation, fingerprint)
		return err
	})
	return receipt, err
}

func cloneRevisionState(state *revisionState) (*revisionState, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	var clone revisionState
	if err := json.Unmarshal(payload, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

func (s *RevisionStore) commitOptimistic(
	idempotencyKey, operation, fingerprint string,
	expectedGeneration uint64,
	working *revisionState,
	result domain.RevisionSession,
) (*domain.RevisionSession, error) {
	var committed *domain.RevisionSession
	err := s.withRevisionTransaction(func() error {
		current, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if found, receiptErr := matchingRevisionReceipt(current, idempotencyKey, operation, fingerprint); found != nil || receiptErr != nil {
			committed = found
			return receiptErr
		}
		if current.Generation != expectedGeneration {
			if operation == "start" && current.ActiveSessionID != "" {
				return ErrActiveRevisionExists
			}
			return &RevisionConflictError{Expected: int(expectedGeneration), Actual: int(current.Generation)}
		}
		if working.Generation != expectedGeneration+1 || result.Generation != working.Generation {
			return fmt.Errorf("revision generation fence did not advance exactly once")
		}
		working.Receipts[idempotencyKey] = revisionReceipt{Operation: operation, Fingerprint: fingerprint, Result: result}
		if err := validateRevisionState(working); err != nil {
			return err
		}
		if err := s.io.WriteJSON(revisionStateFile, working); err != nil {
			return err
		}
		committed, err = cloneRevisionSession(result)
		return err
	})
	return committed, err
}

func (s *RevisionStore) load() (*revisionState, error) {
	var state *revisionState
	err := s.withRevisionTransaction(func() error {
		var loadErr error
		state, loadErr = s.loadUnlocked()
		return loadErr
	})
	return state, err
}

func (s *RevisionStore) loadUnlocked() (*revisionState, error) {
	var state revisionState
	if err := s.io.ReadJSON(revisionStateFile, &state); err != nil {
		if os.IsNotExist(err) {
			return newRevisionState(), nil
		}
		return nil, err
	}
	if state.NormalLease != nil && !revisionProcessAlive(state.NormalLease.PID) {
		state.NormalLease = nil
		state.Generation++
		if err := s.io.WriteJSON(revisionStateFile, &state); err != nil {
			return nil, fmt.Errorf("recover stale normal-flow lease: %w", err)
		}
	}
	if err := validateRevisionState(&state); err != nil {
		return nil, err
	}
	return &state, nil
}

func newRevisionState() *revisionState {
	return &revisionState{
		Version:          domain.RevisionSchemaVersion,
		Generation:       1,
		Sessions:         make(map[string]domain.RevisionSession),
		Versions:         make(map[string]domain.ArtifactVersion),
		CurrentArtifacts: make(map[string]string),
		Receipts:         make(map[string]revisionReceipt),
	}
}

func validateRevisionState(state *revisionState) error {
	if state == nil || state.Version != domain.RevisionSchemaVersion || state.Generation == 0 {
		return fmt.Errorf("unsupported revision store version")
	}
	if state.Sessions == nil || state.Versions == nil || state.CurrentArtifacts == nil || state.Receipts == nil {
		return fmt.Errorf("revision store maps are required")
	}
	if state.NormalLease != nil {
		if strings.TrimSpace(state.NormalLease.Token) == "" || strings.TrimSpace(state.NormalLease.Owner) == "" || state.NormalLease.PID <= 0 || state.NormalLease.Generation != state.Generation {
			return fmt.Errorf("normal flow lease fence is invalid")
		}
		if state.ActiveSessionID != "" {
			return fmt.Errorf("normal flow lease and revision cannot both be active")
		}
	}
	activeCount := 0
	for id, session := range state.Sessions {
		if id != session.ID {
			return fmt.Errorf("revision session index mismatch")
		}
		if err := session.Validate(); err != nil {
			return fmt.Errorf("validate revision session %q: %w", id, err)
		}
		if session.Active() {
			activeCount++
		}
		candidateVersions := make([]domain.ArtifactVersion, 0, len(session.CandidateVersionIDs))
		for _, versionID := range session.AcceptedVersionIDs {
			version, exists := state.Versions[versionID]
			if !exists || version.RevisionID != session.ID || version.Round >= session.Round {
				return fmt.Errorf("revision session %q references invalid accepted version %q", id, versionID)
			}
		}
		for _, versionID := range session.CandidateVersionIDs {
			version, exists := state.Versions[versionID]
			if !exists || version.RevisionID != session.ID {
				return fmt.Errorf("revision session %q references invalid candidate version %q", id, versionID)
			}
			candidateVersions = append(candidateVersions, version)
			if version.Round != session.Round {
				return fmt.Errorf("revision session %q references candidate %q from stale round %d", id, versionID, version.Round)
			}
		}
		if len(candidateVersions) == 0 && session.CandidateSignature != "" {
			return fmt.Errorf("revision session %q has a candidate signature without versions", id)
		}
		if len(candidateVersions) > 0 && session.CandidateSignature != domain.CandidateSignature(candidateVersions) {
			return fmt.Errorf("revision session %q candidate signature mismatch", id)
		}
		if session.Generation > state.Generation {
			return fmt.Errorf("revision session %q generation is ahead of the store", id)
		}
	}
	if activeCount > 1 {
		return fmt.Errorf("revision store contains multiple active sessions")
	}
	if activeCount == 0 && state.ActiveSessionID != "" {
		return fmt.Errorf("revision active-session index is stale")
	}
	if activeCount == 1 {
		active, exists := state.Sessions[state.ActiveSessionID]
		if !exists || !active.Active() {
			return fmt.Errorf("revision active-session index is invalid")
		}
	}
	for id, version := range state.Versions {
		if id != version.ID {
			return fmt.Errorf("artifact version index mismatch")
		}
		if err := version.Validate(); err != nil {
			return fmt.Errorf("validate artifact version %q: %w", id, err)
		}
	}
	for artifactID, versionID := range state.CurrentArtifacts {
		version, exists := state.Versions[versionID]
		if !exists || version.ArtifactID != artifactID {
			return fmt.Errorf("current artifact %q references invalid version %q", artifactID, versionID)
		}
	}
	for key, receipt := range state.Receipts {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(receipt.Operation) == "" || strings.TrimSpace(receipt.Fingerprint) == "" {
			return fmt.Errorf("revision receipt identity is invalid")
		}
		if err := receipt.Result.Validate(); err != nil {
			return fmt.Errorf("validate revision receipt %q: %w", key, err)
		}
		if receipt.Result.Generation > state.Generation {
			return fmt.Errorf("revision receipt %q generation is ahead of the store", key)
		}
	}
	return nil
}

func normalizeRevisionImpact(impact domain.RevisionImpact) (domain.RevisionImpact, error) {
	normalized, err := domain.NewRevisionImpact(impact.Summary, impact.Items)
	if err != nil {
		return domain.RevisionImpact{}, err
	}
	if strings.TrimSpace(impact.Signature) != "" && impact.Signature != normalized.Signature {
		return domain.RevisionImpact{}, fmt.Errorf("revision impact signature mismatch")
	}
	return normalized, nil
}

func describeRevisionPolicy(policy domain.RevisionPolicy) (domain.RevisionMode, string, string, error) {
	if policy == nil {
		return "", "", "", fmt.Errorf("revision policy with a mode is required")
	}
	mode := policy.Mode()
	if strings.TrimSpace(string(mode)) == "" {
		return "", "", "", fmt.Errorf("revision policy with a mode is required")
	}
	id, version := policy.Identity()
	if strings.TrimSpace(id) == "" || strings.TrimSpace(version) == "" {
		return "", "", "", fmt.Errorf("revision policy identity and version are required")
	}
	return mode, id, version, nil
}

func validateApprovalStages(policy domain.RevisionPolicy, impact domain.RevisionImpact) ([]domain.RevisionApprovalStage, error) {
	stages, err := policy.ApprovalStages(cloneRevisionImpact(impact))
	if err != nil {
		return nil, fmt.Errorf("load revision approval stages: %w", err)
	}
	if len(stages) == 0 {
		return nil, fmt.Errorf("revision policy must define at least one approval stage")
	}
	probe := domain.RevisionSession{
		Version: domain.RevisionSchemaVersion, ID: "probe", Mode: policy.Mode(),
		Stage: domain.RevisionStageImpactReviewPending, Revision: 1, Generation: 1, Intent: "probe", Impact: impact,
		ApprovalStages: stages, Round: 1, CreatedAt: domain.RevisionTimestamp(), UpdatedAt: domain.RevisionTimestamp(),
	}
	probe.PolicyID, probe.PolicyVersion = policy.Identity()
	if err := probe.Validate(); err != nil {
		return nil, err
	}
	return append([]domain.RevisionApprovalStage(nil), stages...), nil
}

func approvalStagesEqual(left, right []domain.RevisionApprovalStage) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func applyRevisionRoute(policy domain.RevisionPolicy, session *domain.RevisionSession) error {
	if session.Stage != domain.RevisionStageCandidateGenerating && session.Stage != domain.RevisionStageCandidateAudit {
		session.Route = nil
		return nil
	}
	policySession, err := cloneRevisionSession(*session)
	if err != nil {
		return err
	}
	route, err := policy.Route(*policySession)
	if err != nil {
		return fmt.Errorf("route revision: %w", err)
	}
	if route != nil {
		copy := *route
		copy.SessionID = session.ID
		copy.Revision = session.Revision
		copy.Generation = session.Generation
		if err := copy.Validate(); err != nil {
			return err
		}
		session.Route = &copy
	} else {
		session.Route = nil
	}
	return nil
}

func cloneRevisionImpact(impact domain.RevisionImpact) domain.RevisionImpact {
	clone := impact
	clone.Items = append([]domain.RevisionImpactItem(nil), impact.Items...)
	for index := range clone.Items {
		clone.Items[index].DependencyEvidence = append([]string(nil), impact.Items[index].DependencyEvidence...)
	}
	return clone
}

func cloneArtifactVersions(versions []domain.ArtifactVersion) []domain.ArtifactVersion {
	clone := append([]domain.ArtifactVersion(nil), versions...)
	for index := range clone {
		clone[index].Payload = append(json.RawMessage(nil), versions[index].Payload...)
	}
	return clone
}

func nextArtifactSequence(state *revisionState, artifactID string) int {
	sequence := 1
	for _, version := range state.Versions {
		if version.ArtifactID == artifactID && version.Sequence >= sequence {
			sequence = version.Sequence + 1
		}
	}
	return sequence
}

func cloneRevisionSession(session domain.RevisionSession) (*domain.RevisionSession, error) {
	payload, err := json.Marshal(session)
	if err != nil {
		return nil, err
	}
	var clone domain.RevisionSession
	if err := json.Unmarshal(payload, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}
