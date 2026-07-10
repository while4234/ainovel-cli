package adapt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func sourceEventsFromReports(reports []domain.AdaptationSourceReport) []domain.AdaptationEvent {
	var events []domain.AdaptationEvent
	for index := range reports {
		report := reports[index]
		events = append(events, domain.EnsureAdaptationSourceEvents(&report)...)
	}
	return events
}

func mainlineSourceEventsInRange(reports []domain.AdaptationSourceReport, from, to int) []domain.AdaptationEvent {
	var events []domain.AdaptationEvent
	for _, event := range sourceEventsFromReports(reports) {
		if event.SourceChapter < from || event.SourceChapter > to || event.Importance != domain.AdaptationEventMainline {
			continue
		}
		event.Required = true
		events = append(events, event)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].SourceChapter != events[j].SourceChapter {
			return events[i].SourceChapter < events[j].SourceChapter
		}
		return events[i].ID < events[j].ID
	})
	return events
}

func compactPlannerSourceEvents(events []domain.AdaptationEvent, maxItems int) []domain.AdaptationEvent {
	if maxItems <= 0 || len(events) == 0 {
		return nil
	}
	if len(events) > maxItems {
		events = events[:maxItems]
	}
	out := make([]domain.AdaptationEvent, 0, len(events))
	for _, event := range events {
		event.Description = clipText(event.Description, 140)
		event.Evidence = clipText(event.Evidence, 140)
		event.DependsOn = append([]string(nil), event.DependsOn...)
		out = append(out, event)
	}
	return out
}

func attachSkeletonMainlineEvents(skeleton *plannerSkeleton, reports []domain.AdaptationSourceReport) {
	if skeleton == nil || domain.NormalizeAdaptationGranularity(skeleton.Granularity) != domain.AdaptationGranularityArc {
		return
	}
	for index := range skeleton.Batches {
		batch := &skeleton.Batches[index]
		batch.MainlineEventIDs = adaptationEventIDs(mainlineSourceEventsInRange(reports, batch.SourceFrom, batch.SourceTo))
	}
}

func splitEventIDsForBatch(ids []string, partCount, partIndex int) []string {
	if len(ids) == 0 || partCount <= 0 || partIndex < 0 || partIndex >= partCount {
		return nil
	}
	start, end := splitSourceRuneShare(len(ids), partCount, partIndex)
	return append([]string(nil), ids[start:end]...)
}

func validateArcBatchEventCoverage(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch) error {
	if len(batch.MainlineEventIDs) == 0 {
		return nil
	}
	counts := make(map[string]int, len(batch.MainlineEventIDs))
	for _, chapter := range chapters {
		for _, eventID := range chapter.EventIDs {
			counts[strings.TrimSpace(eventID)]++
		}
	}
	for _, eventID := range batch.MainlineEventIDs {
		switch counts[eventID] {
		case 1:
			continue
		case 0:
			return fmt.Errorf("arc mainline event %s is promised by the parent plan but missing from chapter event_ids", eventID)
		default:
			return fmt.Errorf("arc mainline event %s is assigned to %d detail chapters; assign it exactly once", eventID, counts[eventID])
		}
	}
	return nil
}

func finalizePlannerEventContracts(proposal *domain.AdaptationPlan, opts ProposalOptions, reports []domain.AdaptationSourceReport) error {
	if proposal == nil {
		return fmt.Errorf("planner proposal is nil")
	}
	proposal.ModePolicy = domain.AdaptationModePolicyForGranularity(opts.Granularity)
	proposal.Rules = domain.CompileAdaptationRules(opts.Brief, opts.Granularity)
	proposal.SourceEvents = sourceEventsFromReports(reports)
	switch domain.NormalizeAdaptationGranularity(opts.Granularity) {
	case domain.AdaptationGranularityArc:
		return validateArcProposalMainlineCoverage(proposal)
	case domain.AdaptationGranularityFree:
		buildFreeTargetEventLedger(proposal)
	}
	return nil
}

func validateArcProposalMainlineCoverage(proposal *domain.AdaptationPlan) error {
	counts := make(map[string]int)
	addedCount := 0
	for index := range proposal.Chapters {
		chapter := &proposal.Chapters[index]
		chapter.RuleIDs = domain.AdaptationRuleIDs(domain.ApplicableAdaptationRules(proposal.Rules, proposal.Granularity, chapter.Chapter))
		for _, eventID := range chapter.EventIDs {
			counts[strings.TrimSpace(eventID)]++
		}
		addedCount += len(chapter.AddedEventIDs)
	}
	var missing []string
	for _, event := range proposal.SourceEvents {
		if event.Importance != domain.AdaptationEventMainline || !event.Required {
			continue
		}
		switch counts[event.ID] {
		case 1:
			continue
		case 0:
			missing = append(missing, event.ID)
		default:
			return fmt.Errorf("arc mainline event %s is assigned %d times; mainline events must be bound exactly once", event.ID, counts[event.ID])
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		if addedCount > 0 {
			return fmt.Errorf("added_event_displaces_mainline: added events are planned while required mainline events are unassigned: %s", strings.Join(missing, ", "))
		}
		return fmt.Errorf("missing_mainline_plan_binding: volume/source promises are absent from chapter event_ids: %s", strings.Join(missing, ", "))
	}
	return nil
}

func buildFreeTargetEventLedger(proposal *domain.AdaptationPlan) {
	proposal.TargetEventLedger = nil
	seen := make(map[string]bool)
	for index := range proposal.Chapters {
		chapter := &proposal.Chapters[index]
		chapter.RuleIDs = domain.AdaptationRuleIDs(domain.ApplicableAdaptationRules(proposal.Rules, proposal.Granularity, chapter.Chapter))
		if len(chapter.EventIDs) == 0 {
			chapter.EventIDs = []string{stableTargetEventID(chapter.Chapter, chapter.CoreEvent)}
		}
		added := make(map[string]bool, len(chapter.AddedEventIDs))
		for _, eventID := range chapter.AddedEventIDs {
			added[eventID] = true
		}
		for _, eventID := range chapter.EventIDs {
			if eventID == "" || seen[eventID] {
				continue
			}
			seen[eventID] = true
			origin := domain.AdaptationEventOriginTarget
			if added[eventID] {
				origin = domain.AdaptationEventOriginAdded
			}
			targetEvent := domain.AdaptationEvent{
				ID:          eventID,
				Description: firstNonEmptyString(chapter.CoreEvent, chapter.Title),
				Origin:      origin,
				Importance:  domain.AdaptationEventSupporting,
				DependsOn:   append([]string(nil), chapter.DependsOnEventIDs...),
			}
			if len(proposal.TargetEventLedger) == 0 || eventID == chapter.EventIDs[0] {
				targetEvent.Relationship = chapter.Relationship
				targetEvent.SettingClaims = append([]domain.AdaptationSettingClaim(nil), chapter.SettingClaims...)
			}
			proposal.TargetEventLedger = append(proposal.TargetEventLedger, targetEvent)
		}
	}
}

func stableTargetEventID(chapter int, description string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(description)))
	return fmt.Sprintf("tgt-%04d-%s", chapter, hex.EncodeToString(sum[:4]))
}
