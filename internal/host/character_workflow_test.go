package host

import (
	"slices"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

func TestConfirmOriginalCharacterCandidatePublishesReviewedCandidateAndIsIdempotent(t *testing.T) {
	st, candidate, binding := stagedOriginalCharacterWorkflow(t)
	request := CharacterConfirmationRequest{
		ExpectedCandidateRevision: candidate.Revision,
		CandidateDigest:           binding.Candidate.CharacterContentDigest,
		IdempotencyKey:            "confirm-character-1",
	}
	first, err := ConfirmOriginalCharacterCandidate(st, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Idempotent || first.FoundationRevision <= binding.Candidate.FoundationRevision {
		t.Fatalf("first confirmation = %+v", first)
	}
	published, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(published.Characters) != 1 || published.Characters[0].ID != "char-investigator" ||
		!published.RelationshipsReviewed {
		t.Fatalf("published Foundation = %+v", published)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(review.FoundationSections, "characters") ||
		!slices.Contains(review.FoundationSections, "planned_relationships") {
		t.Fatalf("Foundation sections = %+v", review.FoundationSections)
	}

	restarted := storepkg.NewStore(st.Dir())
	retry, err := ConfirmOriginalCharacterCandidate(restarted, request)
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Idempotent || retry.FoundationRevision != first.FoundationRevision ||
		retry.CandidateRevision != first.CandidateRevision {
		t.Fatalf("retry = %+v, first = %+v", retry, first)
	}
}

func TestConfirmOriginalCharacterCandidateRejectsStaleFoundation(t *testing.T) {
	st, candidate, binding := stagedOriginalCharacterWorkflow(t)
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFoundationPremise(&storepkg.FoundationGenerationFence{
		Generation: review.FoundationGeneration, BaseRevision: review.FoundationBaseRevision,
	}, "A concurrent author revision wins."); err != nil {
		t.Fatal(err)
	}
	_, err = ConfirmOriginalCharacterCandidate(st, CharacterConfirmationRequest{
		ExpectedCandidateRevision: candidate.Revision,
		CandidateDigest:           binding.Candidate.CharacterContentDigest,
		IdempotencyKey:            "confirm-stale",
	})
	if err == nil {
		t.Fatal("expected stale Foundation confirmation failure")
	}
}

func TestEditOriginalCharacterCandidateInvalidatesReview(t *testing.T) {
	st, candidate, _ := stagedOriginalCharacterWorkflow(t)
	editedCharacters := append([]domain.Character(nil), candidate.Foundation.Characters...)
	editedCharacters[0].Goal = "Expose the conspiracy and rescue the missing witness."
	saved, lifecycle, err := EditOriginalCharacterCandidate(st, CharacterCandidateEditRequest{
		ExpectedCandidateRevision: candidate.Revision,
		Characters:                editedCharacters,
		Relationships:             candidate.Foundation.Relationships,
		RelationshipsReviewed:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision <= candidate.Revision ||
		lifecycle.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady ||
		lifecycle.ReviewStatus != domain.CharacterCardReviewStale ||
		lifecycle.ConfirmationStatus != domain.CharacterCardUnconfirmed {
		t.Fatalf("edited workflow candidate=%+v lifecycle=%+v", saved, lifecycle)
	}
	editedBinding, err := domain.CharacterCardBindingFromFoundation(saved.Foundation, lifecycle.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ConfirmOriginalCharacterCandidate(st, CharacterConfirmationRequest{
		ExpectedCandidateRevision: saved.Revision,
		CandidateDigest:           editedBinding.Candidate.CharacterContentDigest,
		IdempotencyKey:            "confirm-with-stale-review",
	}); err == nil {
		t.Fatal("edited candidate confirmation accepted without a fresh review")
	}
}

func stagedOriginalCharacterWorkflow(
	t *testing.T,
) (*storepkg.Store, domain.CharacterCardCandidate, domain.CharacterCardBinding) {
	t.Helper()
	st := storepkg.NewStore(t.TempDir())
	base, err := st.Foundation.SaveRevisionCAS(domain.StoryFoundation{
		SchemaVersion: domain.StoryFoundationSchemaVersion,
		Premise:       "An investigator must expose a conspiracy without losing her family.",
		WorldRules: []domain.WorldRule{{
			ID: "rule-evidence", Category: "mystery", Rule: "Every accusation requires two independent clues.",
			Strength: domain.WorldRuleStrengthHard,
		}},
		Characters:    []domain.Character{},
		Relationships: []domain.CharacterRelationship{},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginOriginalCharacterReview(&domain.PlanningReview{}); err != nil {
		t.Fatal(err)
	}
	_, canonicalBinding, inputs, _, err := tools.CurrentCharacterCanonicalBinding(st)
	if err != nil {
		t.Fatal(err)
	}
	candidateFoundation := domain.CloneStoryFoundation(base)
	candidateFoundation.Characters = []domain.Character{completeHostCharacter()}
	candidateFoundation.Relationships = []domain.CharacterRelationship{}
	candidateFoundation.RelationshipsReviewed = true
	projected, findings, err := domain.ProjectCharacterCandidateCoreCast(candidateFoundation, nil)
	if err != nil || len(findings) != 0 {
		t.Fatalf("project CoreCast findings=%+v err=%v", findings, err)
	}
	completeness, err := domain.EvaluateCharacterCardCompleteness(candidateFoundation, &projected)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range completeness {
		if item.Status != domain.CharacterCardComplete {
			t.Fatalf("candidate completeness = %+v", completeness)
		}
	}
	candidateBinding, err := domain.CharacterCardBindingFromFoundation(candidateFoundation, inputs)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := st.CharacterCards.SaveCandidateCAS(domain.CharacterCardCandidate{
		Version:       domain.CharacterCardCandidateVersion,
		Base:          canonicalBinding,
		Foundation:    candidateFoundation,
		ProjectedCast: projected,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CharacterCards.SaveCAS(domain.CharacterCardLifecycle{
		Version:             domain.CharacterCardLifecycleVersion,
		Mode:                domain.CharacterCardProjectOriginal,
		Candidate:           candidateBinding.Candidate,
		Inputs:              candidateBinding.Inputs,
		InputDigest:         candidateBinding.InputDigest,
		AnalysisSummary:     "deterministic candidate",
		Completeness:        completeness,
		AnalysisStatus:      domain.CharacterCardAnalysisCandidateReady,
		ReviewStatus:        domain.CharacterCardReviewPassed,
		ReviewedCandidate:   candidateBinding.Candidate,
		ReviewedInputDigest: candidateBinding.InputDigest,
		ReviewSummary:       "independent review passed",
		Findings:            []domain.CharacterCardReviewFinding{},
		ConfirmationStatus:  domain.CharacterCardUnconfirmed,
		SourceMappings:      []domain.CharacterSourceMapping{},
	}, 0, candidateBinding)
	if err != nil {
		t.Fatal(err)
	}
	return st, candidate, candidateBinding
}

func completeHostCharacter() domain.Character {
	return domain.Character{
		ID:          "char-investigator",
		Name:        "Lin Che",
		Aliases:     []string{},
		Role:        "protagonist investigator",
		Description: "A disciplined investigator torn between public truth and family safety.",
		Arc:         "She moves from controlling information to accepting the cost of public truth.",
		Traits:      []string{"disciplined", "observant"},
		Tier:        string(domain.CharacterTierCore),
		Goal:        "Expose the conspiracy.",
		Motivation:  "Protect her sibling from the same institutional harm.",
		Conflict:    "Publishing the truth may destroy her family.",
		Voice:       "Short evidence-first sentences.",
		Constraints: []string{"Never accuses without corroboration."},
		ContrastDetails: []domain.CharacterContrastDetail{{
			Surface: "calm", Depth: "loses judgment when her sibling is threatened",
		}},
		KeyBackstory: []domain.CharacterBackstory{{
			Event: "She once misidentified a witness.", Impact: "She now cross-checks every clue.",
		}},
		InitialState: &domain.CharacterInitialState{
			Identity: "investigator", Situation: "receives a new lead in a cold case", Emotion: "guarded",
			Resources: []string{"sealed archive"}, Relationships: "estranged from her sibling",
		},
		KnowledgeBoundary: &domain.CharacterKnowledgeBoundary{
			Known: []string{"official case record"}, Unknown: []string{"conspiracy leader"},
			Misconceptions: []string{"her father left the city"}, Forbidden: []string{"leader identity"},
		},
	}
}
