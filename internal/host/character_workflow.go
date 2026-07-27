package host

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

type CharacterConfirmationRequest struct {
	ExpectedCandidateRevision int64
	CandidateDigest           string
	IdempotencyKey            string
}

type CharacterConfirmationResult struct {
	CandidateRevision  int64
	FoundationRevision int64
	CoreCastRevision   int64
	CandidateDigest    string
	Idempotent         bool
}

type CharacterCandidateEditRequest struct {
	ExpectedCandidateRevision int64
	Characters                []domain.Character
	Relationships             []domain.CharacterRelationship
	RelationshipsReviewed     bool
}

// EditOriginalCharacterCandidate changes only staged Character-owned fields
// and deterministically invalidates the previous independent review.
func EditOriginalCharacterCandidate(
	st *storepkg.Store,
	request CharacterCandidateEditRequest,
) (domain.CharacterCardCandidate, domain.CharacterCardLifecycle, error) {
	if st == nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, fmt.Errorf("character edit store is nil")
	}
	candidate, lifecycle, _, err := tools.CurrentCharacterWorkflow(st)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, err
	}
	if candidate == nil || lifecycle == nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, fmt.Errorf("character candidate or lifecycle is missing")
	}
	if candidate.Revision != request.ExpectedCandidateRevision {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, fmt.Errorf(
			"character candidate revision conflict: expected %d, actual %d",
			request.ExpectedCandidateRevision,
			candidate.Revision,
		)
	}
	editedFoundation := domain.CloneStoryFoundation(candidate.Foundation)
	editedFoundation.Characters = append([]domain.Character(nil), request.Characters...)
	editedFoundation.Relationships = append([]domain.CharacterRelationship(nil), request.Relationships...)
	editedFoundation.RelationshipsReviewed = request.RelationshipsReviewed
	projected, findings, err := domain.ProjectCharacterCandidateCoreCast(editedFoundation, currentCoreCast(st))
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, err
	}
	completeness, err := domain.EvaluateCharacterCardCompleteness(editedFoundation, &projected)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, err
	}
	candidate.Foundation = editedFoundation
	candidate.ProjectedCast = projected
	savedCandidate, err := st.CharacterCards.SaveCandidateCAS(*candidate, candidate.Revision)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, fmt.Errorf("save edited character candidate: %w", err)
	}
	editedBinding, err := domain.CharacterCardBindingFromFoundation(savedCandidate.Foundation, lifecycle.Inputs)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, err
	}
	editedLifecycle := *lifecycle
	editedLifecycle.Candidate = editedBinding.Candidate
	editedLifecycle.Inputs = editedBinding.Inputs
	editedLifecycle.InputDigest = editedBinding.InputDigest
	editedLifecycle.Completeness = completeness
	editedLifecycle.AnalysisStatus = domain.CharacterCardAnalysisCandidateReady
	editedLifecycle.ReviewStatus = domain.CharacterCardReviewStale
	editedLifecycle.ConfirmationStatus = domain.CharacterCardUnconfirmed
	editedLifecycle.Findings = findings
	editedLifecycle.Error = nil
	savedLifecycle, err := st.CharacterCards.SaveCAS(editedLifecycle, lifecycle.Revision, editedBinding)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, fmt.Errorf("invalidate edited character review: %w", err)
	}
	return savedCandidate, savedLifecycle, nil
}

// ConfirmOriginalCharacterCandidate is the explicit user boundary that
// confirms the reviewed full candidate, its deterministic CoreCast projection,
// and the canonical StoryFoundation publication as one recoverable operation.
func ConfirmOriginalCharacterCandidate(
	st *storepkg.Store,
	request CharacterConfirmationRequest,
) (CharacterConfirmationResult, error) {
	if st == nil {
		return CharacterConfirmationResult{}, fmt.Errorf("character confirmation store is nil")
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		return CharacterConfirmationResult{}, fmt.Errorf("character confirmation idempotency_key is required")
	}
	candidate, lifecycle, binding, err := tools.CurrentCharacterWorkflow(st)
	if err != nil {
		return CharacterConfirmationResult{}, err
	}
	if candidate == nil || lifecycle == nil {
		return CharacterConfirmationResult{}, fmt.Errorf("character candidate or lifecycle is missing")
	}
	if lifecycle.ConfirmationStatus == domain.CharacterCardConfirmed &&
		lifecycle.Candidate == binding.Candidate &&
		lifecycle.IdempotencyKey == strings.TrimSpace(request.IdempotencyKey) &&
		strings.TrimSpace(request.CandidateDigest) == binding.Candidate.CharacterContentDigest {
		coreCast, loadErr := st.CoreCast.Load()
		if loadErr != nil {
			return CharacterConfirmationResult{}, loadErr
		}
		coreCastRevision := int64(0)
		if coreCast != nil {
			coreCastRevision = coreCast.Revision
		}
		return CharacterConfirmationResult{
			CandidateRevision:  candidate.Revision,
			FoundationRevision: binding.Candidate.FoundationRevision,
			CoreCastRevision:   coreCastRevision,
			CandidateDigest:    binding.Candidate.CharacterContentDigest,
			Idempotent:         true,
		}, nil
	}
	if candidate.Revision != request.ExpectedCandidateRevision {
		return CharacterConfirmationResult{}, fmt.Errorf(
			"character candidate revision conflict: expected %d, actual %d",
			request.ExpectedCandidateRevision,
			candidate.Revision,
		)
	}
	if strings.TrimSpace(request.CandidateDigest) != binding.Candidate.CharacterContentDigest {
		return CharacterConfirmationResult{}, fmt.Errorf("character candidate digest is stale")
	}
	if lifecycle.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady ||
		lifecycle.ReviewStatus != domain.CharacterCardReviewPassed ||
		lifecycle.ReviewedCandidate != binding.Candidate ||
		lifecycle.ReviewedInputDigest != binding.InputDigest {
		return CharacterConfirmationResult{}, fmt.Errorf("character candidate requires a current passing independent review")
	}
	for _, completeness := range lifecycle.Completeness {
		if completeness.Status != domain.CharacterCardComplete {
			return CharacterConfirmationResult{}, fmt.Errorf("character candidate deterministic completeness is not passing")
		}
	}
	projected, conflicts, err := domain.ProjectCharacterCandidateCoreCast(candidate.Foundation, currentCoreCast(st))
	if err != nil {
		return CharacterConfirmationResult{}, err
	}
	for _, finding := range conflicts {
		if finding.Blocking || finding.Severity == domain.CharacterCardSeverityBlocking {
			return CharacterConfirmationResult{}, fmt.Errorf("CoreCast conflict blocks character confirmation: %s", finding.Description)
		}
	}
	completion := domain.CoreCastCompletion(projected, nil, nil)
	if !completion.Complete {
		return CharacterConfirmationResult{}, fmt.Errorf("projected CoreCast is incomplete: %s", strings.Join(completion.BlockingReasons, "; "))
	}
	canonical, canonicalBinding, _, _, err := tools.CurrentCharacterCanonicalBinding(st)
	if err != nil {
		return CharacterConfirmationResult{}, err
	}
	canonicalDigest, digestErr := domain.CharacterCardContentDigest(canonical)
	if digestErr != nil {
		return CharacterConfirmationResult{}, digestErr
	}
	candidateDigest, digestErr := domain.CharacterCardContentDigest(candidate.Foundation)
	if digestErr != nil {
		return CharacterConfirmationResult{}, digestErr
	}
	if candidate.Base.Candidate != canonicalBinding.Candidate && canonicalDigest != candidateDigest {
		return CharacterConfirmationResult{}, fmt.Errorf("canonical Foundation changed before character publication")
	}
	currentCast, err := st.CoreCast.Load()
	if err != nil {
		return CharacterConfirmationResult{}, err
	}
	expectedCoreRevision := int64(0)
	if currentCast != nil {
		expectedCoreRevision = currentCast.Revision
	}
	savedCast, err := st.CoreCast.SaveCAS(projected, expectedCoreRevision)
	if err != nil {
		return CharacterConfirmationResult{}, fmt.Errorf("save projected CoreCast: %w", err)
	}
	confirmedCast, _, err := st.CoreCast.ConfirmCAS(savedCast.Revision, savedCast.ContentSignature, nil, nil, nil)
	if err != nil {
		return CharacterConfirmationResult{}, fmt.Errorf("confirm projected CoreCast: %w", err)
	}

	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		return CharacterConfirmationResult{}, err
	}
	if review == nil {
		return CharacterConfirmationResult{}, fmt.Errorf("Foundation generation is missing")
	}
	published, _, err := st.PublishOriginalCharacterCandidate(
		storepkg.FoundationGenerationFence{
			Generation:   review.FoundationGeneration,
			BaseRevision: review.FoundationBaseRevision,
		},
		candidate.Foundation,
		candidate.Base.Candidate.FoundationRevision,
	)
	if err != nil {
		return CharacterConfirmationResult{}, fmt.Errorf("publish character candidate: %w", err)
	}
	published, err = st.Foundation.Load()
	if err != nil {
		return CharacterConfirmationResult{}, err
	}
	_, rebound, inputs, _, err := tools.CurrentCharacterCanonicalBinding(st)
	if err != nil {
		return CharacterConfirmationResult{}, err
	}
	rebound, err = domain.CharacterCardBindingFromFoundation(published, inputs)
	if err != nil {
		return CharacterConfirmationResult{}, err
	}
	candidate.Base = rebound
	candidate.Foundation = published
	candidate.ProjectedCast = confirmedCast
	savedCandidate, err := st.CharacterCards.SaveCandidateCAS(*candidate, candidate.Revision)
	if err != nil {
		return CharacterConfirmationResult{}, fmt.Errorf("rebind published character candidate: %w", err)
	}
	confirmedLifecycle := *lifecycle
	confirmedLifecycle.Candidate = rebound.Candidate
	confirmedLifecycle.Inputs = rebound.Inputs
	confirmedLifecycle.InputDigest = rebound.InputDigest
	confirmedLifecycle.ReviewedCandidate = rebound.Candidate
	confirmedLifecycle.ReviewedInputDigest = rebound.InputDigest
	confirmedLifecycle.ConfirmationStatus = domain.CharacterCardConfirmed
	confirmedLifecycle.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	savedLifecycle, err := st.CharacterCards.SaveCAS(confirmedLifecycle, lifecycle.Revision, rebound)
	if err != nil {
		return CharacterConfirmationResult{}, fmt.Errorf("confirm character lifecycle: %w", err)
	}
	return CharacterConfirmationResult{
		CandidateRevision:  savedCandidate.Revision,
		FoundationRevision: rebound.Candidate.FoundationRevision,
		CoreCastRevision:   confirmedCast.Revision,
		CandidateDigest:    savedLifecycle.Candidate.CharacterContentDigest,
	}, nil
}

func currentCoreCast(st *storepkg.Store) *domain.CoreCastContract {
	value, err := st.CoreCast.Load()
	if err != nil {
		return nil
	}
	return value
}
