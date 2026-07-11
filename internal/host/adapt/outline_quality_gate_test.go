package adapt

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestValidateAdaptationOutlineQualityChapterRequiresCompleteLongSourceSegments(t *testing.T) {
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 1,
		Chapters:     []domain.AdaptationSource{{Chapter: 1, Runes: 10_000}},
	}
	valid := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, SourceSegments: []domain.AdaptationSourceSegment{{
				SourceChapter: 1, Sequence: 1, EventIDs: []string{"src-1-a"},
				RuneShare:  domain.AdaptationSourceRuneShare{Start: 0, End: 5_000},
				EntryState: domain.AdaptationSegmentState{}, ExitState: domain.AdaptationSegmentState{"place": "inn"},
			}}},
			{Chapter: 2, SourceSegments: []domain.AdaptationSourceSegment{{
				SourceChapter: 1, Sequence: 2, EventIDs: []string{"src-1-b"},
				RuneShare:  domain.AdaptationSourceRuneShare{Start: 5_000, End: 10_000},
				EntryState: domain.AdaptationSegmentState{"place": "inn"}, ExitState: domain.AdaptationSegmentState{},
			}}},
		},
	}
	if err := ValidateAdaptationOutlineQuality(&valid, manifest); err != nil {
		t.Fatalf("valid Chapter source segments: %v", err)
	}

	invalid := valid
	invalid.Chapters = invalid.Chapters[:1]
	if err := ValidateAdaptationOutlineQuality(&invalid, manifest); !outlineQualityHasCode(err, outlineQualityIssueChapterInvalidSegment) {
		t.Fatalf("long source chapter without all segments error=%v, want %s", err, outlineQualityIssueChapterInvalidSegment)
	}
}

func TestValidateAdaptationOutlineQualityArcBindsVolumeMainlineExactlyOnce(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		SourceEvents: []domain.AdaptationEvent{{
			ID: "src-0001-e01-introduction", SourceChapter: 1, Importance: domain.AdaptationEventMainline, Required: true,
		}},
		Volumes: []domain.AdaptationVolumePlan{{
			Index: 1, SourceFrom: 1, SourceTo: 1, TargetFrom: 1, TargetTo: 1,
			MainlineEventIDs: []string{"src-0001-e01-introduction"},
		}},
		Chapters: []domain.AdaptationChapterPlan{{Chapter: 1}},
	}
	if err := ValidateAdaptationOutlineQuality(&plan, nil); !outlineQualityHasCode(err, outlineQualityIssueArcMissingMainline) {
		t.Fatalf("unmapped volume mainline error=%v, want %s", err, outlineQualityIssueArcMissingMainline)
	}

	plan.Chapters = []domain.AdaptationChapterPlan{{Chapter: 1, EventIDs: []string{"src-0001-e01-introduction"}}}
	if err := ValidateAdaptationOutlineQuality(&plan, nil); err != nil {
		t.Fatalf("mainline bound once in its volume: %v", err)
	}

	plan.Chapters = append(plan.Chapters, domain.AdaptationChapterPlan{Chapter: 2, EventIDs: []string{"src-0001-e01-introduction"}})
	if err := ValidateAdaptationOutlineQuality(&plan, nil); !outlineQualityHasCode(err, outlineQualityIssueArcDuplicateMainline) {
		t.Fatalf("duplicated mainline error=%v, want %s", err, outlineQualityIssueArcDuplicateMainline)
	}
}

func TestValidateAdaptationOutlineQualityFreeChecksTargetCausalityRelationshipAndSettings(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity:              domain.AdaptationGranularityFree,
		TargetSettingLocks:       []domain.AdaptationSettingLock{{Key: "city", Value: "洛阳"}},
		TargetRelationshipStates: map[string]string{"林逸飞|百里冰": "信任"},
		TargetEventLedger: []domain.AdaptationEvent{
			{
				ID: "trust", DependsOn: []string{"rescue"},
				Relationship:  &domain.AdaptationRelationshipTransition{Pair: "林逸飞|百里冰", From: "陌生", To: "信任", RequiresEventIDs: []string{"rescue"}},
				SettingClaims: []domain.AdaptationSettingClaim{{Key: "city", Value: "长安"}},
			},
			{ID: "rescue"},
		},
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, EventIDs: []string{"trust"}},
			{Chapter: 2, EventIDs: []string{"rescue"}},
		},
	}
	err := ValidateAdaptationOutlineQuality(&plan, nil)
	if !outlineQualityHasCode(err, outlineQualityIssueFreeDependency) {
		t.Fatalf("future dependency error=%v, want %s", err, outlineQualityIssueFreeDependency)
	}
	if !outlineQualityHasCode(err, outlineQualityIssueFreeSetting) {
		t.Fatalf("setting lock conflict error=%v, want %s", err, outlineQualityIssueFreeSetting)
	}

	plan.TargetEventLedger[0].DependsOn = nil
	plan.TargetEventLedger[0].Relationship.RequiresEventIDs = nil
	plan.TargetEventLedger[0].SettingClaims[0].Value = "洛阳"
	plan.Chapters = []domain.AdaptationChapterPlan{
		{Chapter: 1, EventIDs: []string{"rescue"}},
		{Chapter: 2, EventIDs: []string{"trust"}},
	}
	if err := ValidateAdaptationOutlineQuality(&plan, nil); err != nil {
		t.Fatalf("coherent target ledger: %v", err)
	}
}

func TestConfirmAdaptationProposalRunsPlanOnlyChapterQualityGate(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Adaptation.SaveSourceFoundation(testSourceFoundation()); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		ChapterCount: 1,
		Chapters:     []domain.AdaptationSource{{Chapter: 1, Runes: 10_000}},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	proposal := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Status:      domain.AdaptationPlanStatusProposal,
		Brief:       "preserve source scenes",
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter: 1, Title: "Opening", SourceChapters: []int{1},
			OutlineEntry: domain.OutlineEntry{CoreEvent: "The first conflict starts.", Hook: "A witness arrives.", Scenes: []string{"scene"}},
		}},
	}
	_, err := ConfirmAdaptationProposal(context.Background(), Deps{Store: st}, proposal)
	if !outlineQualityHasCode(err, outlineQualityIssueChapterMissingSegment) {
		t.Fatalf("ConfirmAdaptationProposal error=%v, want plan-only %s", err, outlineQualityIssueChapterMissingSegment)
	}
}

func TestRetryAdaptationOutlineQualityUsesIndependentAttemptBudget(t *testing.T) {
	qualityErr := &AdaptationOutlineQualityError{Issues: []AdaptationOutlineQualityIssue{{Code: outlineQualityIssueArcMissingMainline}}}
	var calls, preparations int
	proposal, err := retryAdaptationOutlineQuality(
		2,
		func() (domain.AdaptationPlan, error) {
			calls++
			if calls <= 2 {
				return domain.AdaptationPlan{}, qualityErr
			}
			return domain.AdaptationPlan{Chapters: []domain.AdaptationChapterPlan{{Chapter: 1}}}, nil
		},
		func(attempt int, got *AdaptationOutlineQualityError) error {
			preparations++
			if attempt != preparations || got != qualityErr {
				t.Fatalf("retry preparation attempt=%d error=%#v", attempt, got)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("retryAdaptationOutlineQuality: %v", err)
	}
	if len(proposal.Chapters) != 1 || calls != 3 || preparations != 2 {
		t.Fatalf("proposal=%+v calls=%d preparations=%d, want two quality retries after the initial generation", proposal, calls, preparations)
	}
}

func TestRetryAdaptationOutlineQualityDoesNotRetryOtherErrors(t *testing.T) {
	var calls, preparations int
	_, err := retryAdaptationOutlineQuality(
		5,
		func() (domain.AdaptationPlan, error) {
			calls++
			return domain.AdaptationPlan{}, fmt.Errorf("ordinary planner error")
		},
		func(int, *AdaptationOutlineQualityError) error {
			preparations++
			return nil
		},
	)
	if err == nil {
		t.Fatal("non-quality error should be returned")
	}
	if calls != 1 || preparations != 0 {
		t.Fatalf("calls=%d preparations=%d, want no retry for non-quality error", calls, preparations)
	}
}

func TestRetryAdaptationOutlineQualityReadsExpandedAttemptBudgetDuringRun(t *testing.T) {
	qualityErr := &AdaptationOutlineQualityError{Issues: []AdaptationOutlineQualityIssue{{Code: outlineQualityIssueArcMissingMainline}}}
	maxRetries := 1
	var calls int
	proposal, err := retryAdaptationOutlineQualityDynamic(
		func() int { return maxRetries },
		func() (domain.AdaptationPlan, error) {
			calls++
			if calls <= 3 {
				return domain.AdaptationPlan{}, qualityErr
			}
			return domain.AdaptationPlan{Chapters: []domain.AdaptationChapterPlan{{Chapter: 1}}}, nil
		},
		func(attempt int, _ *AdaptationOutlineQualityError) error {
			if attempt == 1 {
				maxRetries = 3
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("retryAdaptationOutlineQualityDynamic: %v", err)
	}
	if len(proposal.Chapters) != 1 || calls != 4 {
		t.Fatalf("proposal=%+v calls=%d, want expanded live budget to allow four total calls", proposal, calls)
	}
}

func outlineQualityHasCode(err error, code string) bool {
	var qualityErr *AdaptationOutlineQualityError
	if !errors.As(err, &qualityErr) {
		return false
	}
	for _, issue := range qualityErr.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
