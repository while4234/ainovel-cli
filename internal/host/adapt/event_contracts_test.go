package adapt

import (
	"slices"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestFinalizeArcRejectsHighLevelMainlineMissingFromChapters(t *testing.T) {
	reports := []domain.AdaptationSourceReport{{
		Chapter: 13,
		KeyEvents: []string{
			"百里冰遇劫，林逸飞出手相救并相识",
			"二人共同救助皮二并形成债务关系",
		},
	}}
	proposal := domain.AdaptationPlan{Granularity: domain.AdaptationGranularityArc, Chapters: []domain.AdaptationChapterPlan{{
		Chapter:       1,
		OutlineEntry:  domain.OutlineEntry{CoreEvent: "新增绑架支线"},
		AddedEventIDs: []string{"added-kidnap"},
	}}}
	err := finalizePlannerEventContracts(&proposal, ProposalOptions{Brief: "保留主线", Granularity: domain.AdaptationGranularityArc}, reports)
	if err == nil || !strings.Contains(err.Error(), "added_event_displaces_mainline") {
		t.Fatalf("expected mainline displacement error, got %v", err)
	}
}

func TestFinalizeArcAcceptsEachMainlineEventExactlyOnce(t *testing.T) {
	reports := []domain.AdaptationSourceReport{{Chapter: 1, KeyEvents: []string{"两人初遇", "案件真相揭晓"}}}
	events := sourceEventsFromReports(reports)
	proposal := domain.AdaptationPlan{Granularity: domain.AdaptationGranularityArc, Chapters: []domain.AdaptationChapterPlan{
		{Chapter: 1, EventIDs: []string{events[0].ID}},
		{Chapter: 2, EventIDs: []string{events[1].ID}},
	}}
	if err := finalizePlannerEventContracts(&proposal, ProposalOptions{Brief: "保留主线", Granularity: domain.AdaptationGranularityArc}, reports); err != nil {
		t.Fatalf("finalize: %v", err)
	}
}

func TestValidateArcBatchEventCoverageRejectsForeignMainlineID(t *testing.T) {
	batch := plannerSkeletonBatch{
		TargetFrom:       5,
		TargetTo:         8,
		MainlineEventIDs: []string{"event-current"},
	}
	chapters := []domain.AdaptationChapterPlan{
		{Chapter: 5, EventIDs: []string{"event-current"}},
		{Chapter: 6, EventIDs: []string{"event-from-previous-batch"}},
	}

	err := validateArcBatchEventCoverage(chapters, batch)
	if err == nil || !strings.Contains(err.Error(), "is not assigned to detail batch 5-8") {
		t.Fatalf("expected foreign mainline ownership error, got %v", err)
	}
}

func TestFinalizeFreeBuildsIndependentTargetLedger(t *testing.T) {
	proposal := domain.AdaptationPlan{Granularity: domain.AdaptationGranularityFree, Chapters: []domain.AdaptationChapterPlan{{Chapter: 1, Title: "新开端", OutlineEntry: domain.OutlineEntry{CoreEvent: "陌生人收到一封信"}}}}
	if err := finalizePlannerEventContracts(&proposal, ProposalOptions{Brief: "自由重构", Granularity: domain.AdaptationGranularityFree}, nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if len(proposal.TargetEventLedger) != 1 || !strings.HasPrefix(proposal.TargetEventLedger[0].ID, "tgt-") {
		t.Fatalf("ledger=%+v", proposal.TargetEventLedger)
	}
}

func TestPlannerSkeletonFromVolumeReviewPreservesMainlineContracts(t *testing.T) {
	review := domain.AdaptationVolumeReview{
		Granularity: domain.AdaptationGranularityArc, RewritePolicy: domain.AdaptationRewriteFullRewrite,
		TargetChapterCount: 2,
		Volumes: []domain.AdaptationVolumePlan{{
			Index: 1, TargetFrom: 1, TargetTo: 2, SourceFrom: 1, SourceTo: 3,
			MainlineEventIDs: []string{"meet", "case"},
		}},
	}
	skeleton := plannerSkeletonFromVolumeReview(review)
	if len(skeleton.Batches) != 1 || !slices.Equal(skeleton.Batches[0].MainlineEventIDs, []string{"meet", "case"}) {
		t.Fatalf("mainline event contract was lost: %+v", skeleton.Batches)
	}
}

func TestAttachSkeletonEventsPublishesSupportingWhitelist(t *testing.T) {
	skeleton := plannerSkeleton{Granularity: domain.AdaptationGranularityArc, Batches: []plannerSkeletonBatch{{SourceFrom: 1, SourceTo: 1}}}
	reports := []domain.AdaptationSourceReport{{Chapter: 1, SourceEvents: []domain.AdaptationEvent{
		{ID: "src-main", SourceChapter: 1, Importance: domain.AdaptationEventMainline},
		{ID: "src-support", SourceChapter: 1, Importance: domain.AdaptationEventSupporting},
	}}}
	attachSkeletonMainlineEvents(&skeleton, reports)
	if !detailAuditContainsString(skeleton.Batches[0].MainlineEventIDs, "src-main") {
		t.Fatalf("mainline whitelist=%v", skeleton.Batches[0].MainlineEventIDs)
	}
	if !detailAuditContainsString(skeleton.Batches[0].AllowedEventIDs, "src-support") {
		t.Fatalf("allowed whitelist=%v", skeleton.Batches[0].AllowedEventIDs)
	}
}
