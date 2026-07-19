package store

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	FoundationReviewErrorStale      = "foundation_stale"
	FoundationReviewErrorBusy       = "foundation_busy"
	FoundationReviewErrorStage      = "foundation_invalid_stage"
	FoundationReviewErrorValidation = "foundation_validation_failed"
)

type FoundationReviewError struct {
	Code   string
	Err    error
	Review *domain.PlanningReview
}

func (e *FoundationReviewError) Error() string {
	if e == nil || e.Err == nil {
		return "foundation review failed"
	}
	return e.Err.Error()
}

func (e *FoundationReviewError) Unwrap() error { return e.Err }

// FoundationGenerationFence identifies one immutable Foundation generation
// round. BaseRevision never advances while sections in that round are saved.
type FoundationGenerationFence struct {
	Generation   int64
	BaseRevision int64
}

// FoundationConfirmationTransition is an opaque receipt for one exact
// pending -> blueprint/collecting transition. Only Store can populate it;
// rollback succeeds only while that exact transition is still authoritative.
type FoundationConfirmationTransition struct {
	before domain.PlanningReview
	after  domain.PlanningReview
}

// FoundationReviewTransition is an opaque receipt for one exact initial
// planning-review -> Foundation-collecting transition. It permits only Store
// to conditionally undo that exact attempt when planner startup fails.
type FoundationReviewTransition struct {
	before *domain.PlanningReview
	after  domain.PlanningReview
}

func (s *Store) BeginFoundationReview(review *domain.PlanningReview) (*FoundationReviewTransition, error) {
	if s == nil || review == nil {
		return nil, fmt.Errorf("foundation review is required")
	}
	var transition *FoundationReviewTransition
	err := s.Revisions.withRevisionTransaction(func() error {
		s.Foundation.lifecycle.reviewMu.Lock()
		defer s.Foundation.lifecycle.reviewMu.Unlock()
		s.crossMu.Lock()
		defer s.crossMu.Unlock()
		current, err := s.RunMeta.PlanningReview()
		if err != nil {
			return err
		}
		if planningReviewHasFoundationAuthority(current) {
			return &FoundationReviewError{Code: FoundationReviewErrorStage, Err: fmt.Errorf("foundation review has already started"), Review: current}
		}
		var before *domain.PlanningReview
		if current != nil {
			copy := clonePlanningReview(*current)
			before = &copy
		}
		if err := s.beginFoundationReviewLocked(review); err != nil {
			return err
		}
		transition = &FoundationReviewTransition{before: before, after: clonePlanningReview(*review)}
		return nil
	})
	return transition, err
}

// RollbackFoundationReview restores only the state replaced by one exact
// BeginFoundationReview attempt. A later generation or other transition wins.
func (s *Store) RollbackFoundationReview(transition *FoundationReviewTransition) error {
	if transition == nil {
		return &FoundationReviewError{Code: FoundationReviewErrorStale, Err: fmt.Errorf("foundation review transition is unavailable")}
	}
	return s.Revisions.withRevisionTransaction(func() error {
		s.Foundation.lifecycle.reviewMu.Lock()
		defer s.Foundation.lifecycle.reviewMu.Unlock()
		s.crossMu.Lock()
		defer s.crossMu.Unlock()
		current, err := s.RunMeta.PlanningReview()
		if err != nil {
			return err
		}
		if current == nil || !reflect.DeepEqual(*current, transition.after) {
			return &FoundationReviewError{Code: FoundationReviewErrorStale, Err: fmt.Errorf("foundation review transition is no longer current"), Review: current}
		}
		if transition.before == nil {
			return s.RunMeta.setPlanningReviewAuthoritative(nil)
		}
		before := clonePlanningReview(*transition.before)
		return s.RunMeta.setPlanningReviewAuthoritative(&before)
	})
}

func (s *Store) beginFoundationReviewLocked(review *domain.PlanningReview) error {
	if _, err := s.requireConfirmedNormalCoreCast(); err != nil {
		return &FoundationReviewError{Code: FoundationReviewErrorStage, Err: err, Review: review}
	}
	foundation, err := s.Foundation.Load()
	if err != nil {
		return fmt.Errorf("load foundation generation fence: %w", err)
	}
	if foundation.RelationshipsReviewed {
		if err := s.Foundation.updateRelationships(foundation.Relationships, false); err != nil {
			return fmt.Errorf("clear foundation relationship review marker: %w", err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	review.Status = domain.PlanningReviewStatusCollecting
	review.Kind = domain.PlanningReviewKindFoundation
	review.FoundationStatus = domain.FoundationReviewStatusCollecting
	review.FoundationRevision = 0
	review.FoundationAuditSignature = ""
	review.CoreCastSignature = ""
	review.FoundationConfirmedAt = ""
	review.FoundationSections = nil
	review.FoundationGeneration++
	if review.FoundationGeneration <= 0 {
		review.FoundationGeneration = 1
	}
	review.FoundationBaseRevision = foundation.Revision
	review.UpdatedAt = now
	return s.RunMeta.setPlanningReviewAuthoritative(review)
}

func (s *Store) SaveFoundationPremise(fence *FoundationGenerationFence, content string) (*domain.PlanningReview, error) {
	return s.saveFoundationGenerationSection(fence, "premise", func() error {
		return s.Foundation.updatePremise(content)
	})
}

func (s *Store) SaveFoundationCharacters(fence *FoundationGenerationFence, characters []domain.Character) (*domain.PlanningReview, error) {
	return s.saveFoundationGenerationSection(fence, "characters", func() error {
		return s.Foundation.updateCharacters(characters)
	})
}

func (s *Store) SaveFoundationRelationships(fence *FoundationGenerationFence, relationships []domain.CharacterRelationship) (*domain.PlanningReview, error) {
	return s.saveFoundationGenerationSection(fence, "planned_relationships", func() error {
		return s.Foundation.updateRelationships(relationships, false)
	})
}

func (s *Store) SaveFoundationWorldRules(fence *FoundationGenerationFence, rules []domain.WorldRule) (*domain.PlanningReview, error) {
	return s.saveFoundationGenerationSection(fence, "world_rules", func() error {
		return s.Foundation.updateWorldRules(rules)
	})
}

// saveFoundationGenerationSection validates the durable round token and
// serializes the canonical mutation with its section checkpoint. A token that
// outlives revise, confirm, restart, or another response never reaches mutate.
func (s *Store) saveFoundationGenerationSection(fence *FoundationGenerationFence, section string, mutate func() error) (*domain.PlanningReview, error) {
	if s == nil || mutate == nil {
		return nil, fmt.Errorf("foundation section mutation is required")
	}
	if !s.Foundation.lifecycle.reviewMu.TryLock() {
		review, _ := s.RunMeta.PlanningReview()
		return review, &FoundationReviewError{Code: FoundationReviewErrorBusy, Err: fmt.Errorf("foundation generation mutation is busy"), Review: review}
	}
	defer s.Foundation.lifecycle.reviewMu.Unlock()
	if !s.crossMu.TryLock() {
		review, _ := s.RunMeta.PlanningReview()
		return review, &FoundationReviewError{Code: FoundationReviewErrorBusy, Err: fmt.Errorf("foundation generation mutation is busy"), Review: review}
	}
	defer s.crossMu.Unlock()

	review, err := s.RunMeta.PlanningReview()
	if err != nil {
		return nil, err
	}
	collecting := review != nil && review.Kind == domain.PlanningReviewKindFoundation &&
		review.Status == domain.PlanningReviewStatusCollecting && review.FoundationStatus == domain.FoundationReviewStatusCollecting
	if !collecting {
		if fence != nil {
			return review, &FoundationReviewError{Code: FoundationReviewErrorStale, Err: fmt.Errorf("foundation generation token is stale"), Review: review}
		}
		return nil, mutate()
	}
	if fence == nil || fence.Generation != review.FoundationGeneration || fence.BaseRevision != review.FoundationBaseRevision {
		return review, &FoundationReviewError{Code: FoundationReviewErrorStale, Err: fmt.Errorf("foundation generation or base revision is stale"), Review: review}
	}
	if !foundationGenerationSectionKnown(section) {
		return review, &FoundationReviewError{Code: FoundationReviewErrorValidation, Err: fmt.Errorf("unknown foundation generation section %q", section), Review: review}
	}
	if foundationSectionRecorded(review.FoundationSections, section) {
		return review, &FoundationReviewError{Code: FoundationReviewErrorStale, Err: fmt.Errorf("foundation section %s was already recorded for generation %d", section, review.FoundationGeneration), Review: review}
	}
	foundation, err := s.Foundation.Load()
	if err != nil {
		return review, err
	}
	if foundation.Revision < review.FoundationBaseRevision {
		return review, &FoundationReviewError{Code: FoundationReviewErrorStale, Err: fmt.Errorf("foundation revision predates generation base revision"), Review: review}
	}
	if err := mutate(); err != nil {
		return review, err
	}
	return s.recordFoundationSectionLocked(review, section)
}

// recordFoundationSectionLocked advances only the already validated current
// round. The caller owns crossMu across both the canonical write and checkpoint.
func (s *Store) recordFoundationSectionLocked(review *domain.PlanningReview, section string) (*domain.PlanningReview, error) {
	review.FoundationSections = domain.AddFoundationSection(review.FoundationSections, section)
	review.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if !domain.FoundationSectionsComplete(review.FoundationSections) {
		return review, s.RunMeta.setPlanningReviewAuthoritative(review)
	}
	foundation, contract, auditSignature, err := s.currentFoundationBinding()
	if err != nil {
		return review, &FoundationReviewError{Code: FoundationReviewErrorValidation, Err: err, Review: review}
	}
	if foundation.Revision <= review.FoundationBaseRevision {
		return review, &FoundationReviewError{Code: FoundationReviewErrorValidation, Err: fmt.Errorf("foundation generation did not produce a new semantic revision"), Review: review}
	}
	review.Status = domain.PlanningReviewStatusPending
	review.FoundationStatus = domain.FoundationReviewStatusPending
	review.FoundationRevision = foundation.Revision
	review.FoundationAuditSignature = auditSignature
	review.CoreCastSignature = contract.ContentSignature
	if err := s.RunMeta.setPlanningReviewAuthoritative(review); err != nil {
		return review, err
	}
	return review, nil
}

func foundationSectionRecorded(sections []string, section string) bool {
	section = strings.TrimSpace(section)
	for _, existing := range sections {
		if strings.TrimSpace(existing) == section {
			return true
		}
	}
	return false
}

func foundationGenerationSectionKnown(section string) bool {
	for _, expected := range domain.FoundationGenerationSections {
		if expected == strings.TrimSpace(section) {
			return true
		}
	}
	return false
}

func (s *Store) ConfirmFoundation(expectedRevision int64, expectedAuditSignature string) (*domain.PlanningReview, error) {
	review, _, err := s.ConfirmFoundationForPlanning(expectedRevision, expectedAuditSignature)
	return review, err
}

func (s *Store) ConfirmFoundationForPlanning(
	expectedRevision int64,
	expectedAuditSignature string,
) (*domain.PlanningReview, *FoundationConfirmationTransition, error) {
	var review *domain.PlanningReview
	var transition *FoundationConfirmationTransition
	err := s.Revisions.withRevisionTransaction(func() error {
		s.Foundation.lifecycle.reviewMu.Lock()
		defer s.Foundation.lifecycle.reviewMu.Unlock()
		s.crossMu.Lock()
		defer s.crossMu.Unlock()
		var err error
		review, transition, err = s.confirmFoundationForPlanningLocked(expectedRevision, expectedAuditSignature)
		return err
	})
	return review, transition, err
}

func (s *Store) confirmFoundationForPlanningLocked(
	expectedRevision int64,
	expectedAuditSignature string,
) (*domain.PlanningReview, *FoundationConfirmationTransition, error) {
	review, err := s.RunMeta.PlanningReview()
	if err != nil {
		return nil, nil, err
	}
	if review == nil || review.Kind != domain.PlanningReviewKindFoundation || review.Status != domain.PlanningReviewStatusPending ||
		review.FoundationStatus != domain.FoundationReviewStatusPending || review.FoundationGeneration <= 0 ||
		!domain.FoundationSectionsComplete(review.FoundationSections) {
		return review, nil, &FoundationReviewError{Code: FoundationReviewErrorStage, Err: fmt.Errorf("foundation review is not a complete pending generation"), Review: review}
	}
	foundation, contract, auditSignature, err := s.currentFoundationBinding()
	if err != nil {
		return review, nil, &FoundationReviewError{Code: FoundationReviewErrorValidation, Err: err, Review: review}
	}
	if expectedRevision != foundation.Revision || expectedRevision != review.FoundationRevision ||
		strings.TrimSpace(expectedAuditSignature) != auditSignature || review.FoundationAuditSignature != auditSignature ||
		review.CoreCastSignature != contract.ContentSignature {
		if err := s.resetFoundationPending(review, foundation, contract, auditSignature); err != nil {
			return review, nil, err
		}
		return review, nil, &FoundationReviewError{Code: FoundationReviewErrorStale, Err: fmt.Errorf("foundation revision or signature is stale"), Review: review}
	}
	before := clonePlanningReview(*review)
	if err := s.Foundation.updateRelationships(foundation.Relationships, true); err != nil {
		return review, nil, &FoundationReviewError{Code: FoundationReviewErrorValidation, Err: fmt.Errorf("mark foundation relationships reviewed: %w", err), Review: review}
	}
	review.FoundationStatus = domain.FoundationReviewStatusApproved
	review.Kind = domain.PlanningReviewKindBlueprint
	review.Status = domain.PlanningReviewStatusCollecting
	review.FoundationConfirmedAt = time.Now().UTC().Format(time.RFC3339Nano)
	review.UpdatedAt = review.FoundationConfirmedAt
	if err := s.RunMeta.setPlanningReviewAuthoritative(review); err != nil {
		return review, nil, err
	}
	after := clonePlanningReview(*review)
	return review, &FoundationConfirmationTransition{before: before, after: after}, nil
}

func (s *Store) RollbackFoundationConfirmation(transition *FoundationConfirmationTransition) error {
	if transition == nil {
		return &FoundationReviewError{Code: FoundationReviewErrorStale, Err: fmt.Errorf("foundation confirmation transition is unavailable")}
	}
	return s.Revisions.withRevisionTransaction(func() error {
		s.Foundation.lifecycle.reviewMu.Lock()
		defer s.Foundation.lifecycle.reviewMu.Unlock()
		s.crossMu.Lock()
		defer s.crossMu.Unlock()
		current, err := s.RunMeta.PlanningReview()
		if err != nil {
			return err
		}
		if current == nil || !reflect.DeepEqual(*current, transition.after) {
			return &FoundationReviewError{Code: FoundationReviewErrorStale, Err: fmt.Errorf("foundation confirmation transition is no longer current"), Review: current}
		}
		before := clonePlanningReview(transition.before)
		return s.RunMeta.setPlanningReviewAuthoritative(&before)
	})
}

func (s *Store) ReviseFoundation(feedback string) (*domain.PlanningReview, error) {
	feedback = strings.TrimSpace(feedback)
	if feedback == "" {
		return nil, &FoundationReviewError{Code: FoundationReviewErrorValidation, Err: fmt.Errorf("foundation revision feedback is required")}
	}
	var review *domain.PlanningReview
	err := s.Revisions.withRevisionTransaction(func() error {
		s.Foundation.lifecycle.reviewMu.Lock()
		defer s.Foundation.lifecycle.reviewMu.Unlock()
		s.crossMu.Lock()
		defer s.crossMu.Unlock()
		var err error
		review, err = s.RunMeta.PlanningReview()
		if err != nil {
			return err
		}
		if review == nil || review.FoundationStatus == "" || review.FoundationStatus == domain.FoundationReviewStatusCollecting {
			return &FoundationReviewError{Code: FoundationReviewErrorStage, Err: fmt.Errorf("foundation review cannot be revised in the current stage"), Review: review}
		}
		review.FoundationFeedback = feedback
		return s.beginFoundationReviewLocked(review)
	})
	return review, err
}

func (s *Store) RequireConfirmedFoundation() error {
	review, err := s.RunMeta.PlanningReview()
	if err != nil {
		return err
	}
	if review == nil || review.FoundationStatus != domain.FoundationReviewStatusApproved || strings.TrimSpace(review.FoundationConfirmedAt) == "" ||
		review.Kind == domain.PlanningReviewKindFoundation || review.FoundationGeneration <= 0 ||
		!domain.FoundationSectionsComplete(review.FoundationSections) || !validApprovedFoundationPlanningState(review.Kind, review.Status) {
		return &FoundationReviewError{Code: FoundationReviewErrorStage, Err: fmt.Errorf("foundation confirmation gate is not satisfied"), Review: review}
	}
	foundation, contract, auditSignature, err := s.currentFoundationBinding()
	if err != nil {
		return &FoundationReviewError{Code: FoundationReviewErrorValidation, Err: err, Review: review}
	}
	if review.FoundationRevision != foundation.Revision || review.FoundationAuditSignature != auditSignature ||
		review.CoreCastSignature != contract.ContentSignature {
		if err := s.resetFoundationPending(review, foundation, contract, auditSignature); err != nil {
			return err
		}
		return &FoundationReviewError{Code: FoundationReviewErrorStale, Err: fmt.Errorf("confirmed foundation binding is stale"), Review: review}
	}
	return nil
}

func validApprovedFoundationPlanningState(kind, status string) bool {
	switch kind {
	case domain.PlanningReviewKindBlueprint, domain.PlanningReviewKindVolumeSplit, domain.PlanningReviewKindChapterOutline:
	default:
		return false
	}
	switch status {
	case domain.PlanningReviewStatusCollecting, domain.PlanningReviewStatusPending, domain.PlanningReviewStatusApproved:
		return true
	default:
		return false
	}
}

func clonePlanningReview(review domain.PlanningReview) domain.PlanningReview {
	review.FoundationSections = append([]string(nil), review.FoundationSections...)
	return review
}

func (s *Store) CurrentFoundationAuditSignature() (string, error) {
	foundation, err := s.Foundation.Load()
	if err != nil {
		return "", err
	}
	return domain.FoundationAuditSignature(foundation)
}

func (s *Store) CurrentApprovedFoundationBinding() (int64, string, error) {
	s.Foundation.lifecycle.reviewMu.Lock()
	defer s.Foundation.lifecycle.reviewMu.Unlock()
	s.crossMu.Lock()
	defer s.crossMu.Unlock()
	return s.currentApprovedFoundationBindingLocked()
}

func (s *Store) currentApprovedFoundationBindingLocked() (int64, string, error) {
	if err := s.RequireConfirmedFoundation(); err != nil {
		return 0, "", err
	}
	review, err := s.RunMeta.PlanningReview()
	if err != nil || review == nil {
		return 0, "", err
	}
	if review.FoundationRevision <= 0 || strings.TrimSpace(review.FoundationAuditSignature) == "" {
		return 0, "", &FoundationReviewError{Code: FoundationReviewErrorStage, Err: fmt.Errorf("approved foundation binding is incomplete"), Review: review}
	}
	return review.FoundationRevision, review.FoundationAuditSignature, nil
}

func (s *Store) resetFoundationPending(review *domain.PlanningReview, foundation domain.StoryFoundation, contract domain.CoreCastContract, auditSignature string) error {
	if review == nil {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	review.Kind = domain.PlanningReviewKindFoundation
	review.Status = domain.PlanningReviewStatusPending
	review.FoundationStatus = domain.FoundationReviewStatusPending
	review.FoundationRevision = foundation.Revision
	review.FoundationAuditSignature = auditSignature
	review.CoreCastSignature = contract.ContentSignature
	review.FoundationConfirmedAt = ""
	review.UpdatedAt = now
	return s.RunMeta.setPlanningReviewAuthoritative(review)
}

func (s *Store) currentFoundationBinding() (domain.StoryFoundation, domain.CoreCastContract, string, error) {
	contract, err := s.requireConfirmedNormalCoreCast()
	if err != nil {
		return domain.StoryFoundation{}, domain.CoreCastContract{}, "", err
	}
	foundation, err := s.Foundation.Load()
	if err != nil {
		return domain.StoryFoundation{}, domain.CoreCastContract{}, "", err
	}
	if err := domain.ValidateFoundationComplete(foundation, contract); err != nil {
		return domain.StoryFoundation{}, domain.CoreCastContract{}, "", err
	}
	signature, err := domain.FoundationAuditSignature(foundation)
	if err != nil {
		return domain.StoryFoundation{}, domain.CoreCastContract{}, "", err
	}
	return foundation, contract, signature, nil
}

func (s *Store) requireConfirmedNormalCoreCast() (domain.CoreCastContract, error) {
	binding, err := s.CoreCast.LoadGateBinding()
	if err != nil {
		return domain.CoreCastContract{}, fmt.Errorf("load core cast gate binding: %w", err)
	}
	if binding == nil {
		return domain.CoreCastContract{}, fmt.Errorf("core cast gate binding does not exist")
	}
	if binding.Mode != domain.CoreCastModeNormal {
		return domain.CoreCastContract{}, fmt.Errorf("foundation review requires a normal-original core cast")
	}
	contract, err := s.CoreCast.RequireConfirmedGate(*binding, nil, nil, nil)
	if err != nil {
		return domain.CoreCastContract{}, fmt.Errorf("core cast confirmation gate is stale: %w", err)
	}
	return contract, nil
}
