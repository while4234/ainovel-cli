package tools

import (
	"sort"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/rules"
)

const (
	maxContextOutlineTextRunes         = 120
	maxContextOutlineScenes            = 3
	maxContextChapterPlanTextRunes     = 300
	maxContextContractItems            = 10
	maxContextContractItemRunes        = 180
	maxContextCharacters               = 24
	maxContextCharacterTextRunes       = 220
	maxContextCharacterSnapshots       = 24
	maxContextRelationships            = 30
	maxContextStateChanges             = 30
	maxContextForeshadowEntries        = 30
	maxContextTimelineEvents           = 12
	maxContextSummaryItems             = 6
	maxContextSummaryRunes             = 180
	maxContextSummaryEventItems        = 6
	maxContextHistoryItems             = 12
	maxContextUserPreferencesRunes     = 4000
	maxContextUserRuleListItems        = 80
	maxContextAdaptationBriefRunes     = 1000
	maxContextAdaptationRuleItems      = 6
	maxContextSourceReports            = 6
	maxContextSourceReportItemRunes    = 90
	maxContextSourceReportSummaryRunes = 140
	maxContextPlanningVolumes          = 12
	maxContextPlanningVolumeSummaries  = 6
)

func compactOutlineEntry(entry domain.OutlineEntry) domain.OutlineEntry {
	entry.Title = truncateRunes(entry.Title, 80)
	entry.CoreEvent = truncateRunes(entry.CoreEvent, maxContextOutlineTextRunes)
	entry.Hook = truncateRunes(entry.Hook, maxContextOutlineTextRunes)
	entry.Scenes = compactStringList(entry.Scenes, maxContextOutlineScenes, maxContextOutlineTextRunes)
	return entry
}

func compactOutlineEntries(entries []domain.OutlineEntry) []domain.OutlineEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]domain.OutlineEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, compactOutlineEntry(entry))
	}
	return out
}

func compactChapterPlan(plan domain.ChapterPlan) domain.ChapterPlan {
	plan.Title = truncateRunes(plan.Title, 80)
	plan.Goal = truncateRunes(plan.Goal, maxContextChapterPlanTextRunes)
	plan.Conflict = truncateRunes(plan.Conflict, maxContextChapterPlanTextRunes)
	plan.Hook = truncateRunes(plan.Hook, maxContextChapterPlanTextRunes)
	plan.EmotionArc = truncateRunes(plan.EmotionArc, maxContextChapterPlanTextRunes)
	plan.Notes = truncateRunes(plan.Notes, maxContextChapterPlanTextRunes)
	plan.Contract = compactChapterContract(plan.Contract)
	return plan
}

func compactChapterContract(contract domain.ChapterContract) domain.ChapterContract {
	contract.RequiredBeats = compactStringList(contract.RequiredBeats, maxContextContractItems, maxContextContractItemRunes)
	contract.ForbiddenMoves = compactStringList(contract.ForbiddenMoves, maxContextContractItems, maxContextContractItemRunes)
	contract.ContinuityChecks = compactStringList(contract.ContinuityChecks, maxContextContractItems, maxContextContractItemRunes)
	contract.EvaluationFocus = compactStringList(contract.EvaluationFocus, maxContextContractItems, maxContextContractItemRunes)
	contract.PayoffPoints = compactStringList(contract.PayoffPoints, maxContextContractItems, maxContextContractItemRunes)
	contract.EmotionTarget = truncateRunes(contract.EmotionTarget, maxContextContractItemRunes)
	contract.HookGoal = truncateRunes(contract.HookGoal, maxContextContractItemRunes)
	return contract
}

func compactAdaptationChapterPlan(plan domain.AdaptationChapterPlan) domain.AdaptationChapterPlan {
	plan.OutlineEntry = compactOutlineEntry(plan.OutlineEntry)
	plan.Title = truncateRunes(plan.Title, 80)
	plan.CoverageNote = truncateRunes(plan.CoverageNote, maxContextChapterPlanTextRunes)
	plan.PreserveEvents = compactStringList(plan.PreserveEvents, maxContextContractItems, maxContextContractItemRunes)
	plan.RequiredChanges = compactStringList(plan.RequiredChanges, maxContextContractItems, maxContextContractItemRunes)
	plan.ForbiddenMoves = compactStringList(plan.ForbiddenMoves, maxContextContractItems, maxContextContractItemRunes)
	return plan
}

func compactCharacters(chars []domain.Character, maxItems int) []domain.Character {
	if len(chars) == 0 || maxItems <= 0 {
		return nil
	}
	limit := min(len(chars), maxItems)
	out := make([]domain.Character, 0, limit)
	for _, c := range chars[:limit] {
		c.Description = truncateRunes(c.Description, maxContextCharacterTextRunes)
		c.Arc = truncateRunes(c.Arc, maxContextCharacterTextRunes)
		c.Traits = compactStringList(c.Traits, 8, 60)
		c.Aliases = compactStringList(c.Aliases, 8, 40)
		out = append(out, c)
	}
	return out
}

func compactCharacterSnapshots(snapshots []domain.CharacterSnapshot, maxItems int) []domain.CharacterSnapshot {
	if len(snapshots) == 0 || maxItems <= 0 {
		return nil
	}
	start := max(len(snapshots)-maxItems, 0)
	out := make([]domain.CharacterSnapshot, 0, len(snapshots)-start)
	for _, snap := range snapshots[start:] {
		snap.Status = truncateRunes(snap.Status, maxContextCharacterTextRunes)
		snap.Power = truncateRunes(snap.Power, 120)
		snap.Motivation = truncateRunes(snap.Motivation, maxContextCharacterTextRunes)
		snap.Relations = truncateRunes(snap.Relations, maxContextCharacterTextRunes)
		out = append(out, snap)
	}
	return out
}

func compactRelationshipEntries(entries []domain.RelationshipEntry, currentChapter, maxItems int) []domain.RelationshipEntry {
	if len(entries) == 0 || maxItems <= 0 {
		return nil
	}
	var picked []domain.RelationshipEntry
	for i := len(entries) - 1; i >= 0 && len(picked) < maxItems; i-- {
		entry := entries[i]
		if currentChapter > 0 && entry.Chapter > currentChapter {
			continue
		}
		entry.Relation = truncateRunes(entry.Relation, maxContextContractItemRunes)
		picked = append(picked, entry)
	}
	reverseRelationshipEntries(picked)
	return picked
}

func compactStateChanges(changes []domain.StateChange, maxItems int) []domain.StateChange {
	if len(changes) == 0 || maxItems <= 0 {
		return nil
	}
	limit := min(len(changes), maxItems)
	out := make([]domain.StateChange, 0, limit)
	for _, change := range changes[:limit] {
		change.OldValue = truncateRunes(change.OldValue, 100)
		change.NewValue = truncateRunes(change.NewValue, maxContextContractItemRunes)
		change.Reason = truncateRunes(change.Reason, maxContextContractItemRunes)
		out = append(out, change)
	}
	return out
}

func compactForeshadowEntries(entries []domain.ForeshadowEntry, maxItems int) []domain.ForeshadowEntry {
	if len(entries) == 0 || maxItems <= 0 {
		return nil
	}
	limit := min(len(entries), maxItems)
	out := make([]domain.ForeshadowEntry, 0, limit)
	for _, entry := range entries[:limit] {
		entry.Description = truncateRunes(entry.Description, maxContextContractItemRunes)
		out = append(out, entry)
	}
	return out
}

func compactTimelineEvents(events []domain.TimelineEvent, maxItems int) []domain.TimelineEvent {
	if len(events) == 0 || maxItems <= 0 {
		return nil
	}
	start := max(len(events)-maxItems, 0)
	out := make([]domain.TimelineEvent, 0, len(events)-start)
	for _, event := range events[start:] {
		event.Time = truncateRunes(event.Time, 80)
		event.Event = truncateRunes(event.Event, maxContextContractItemRunes)
		event.Characters = compactStringList(event.Characters, 8, 40)
		out = append(out, event)
	}
	return out
}

func compactChapterSummaries(summaries []domain.ChapterSummary) []domain.ChapterSummary {
	if len(summaries) == 0 {
		return nil
	}
	start := max(len(summaries)-maxContextSummaryItems, 0)
	out := make([]domain.ChapterSummary, 0, len(summaries)-start)
	for _, summary := range summaries[start:] {
		summary.Summary = truncateRunes(summary.Summary, maxContextSummaryRunes)
		summary.Characters = compactStringList(summary.Characters, 12, 40)
		summary.KeyEvents = compactStringList(summary.KeyEvents, maxContextSummaryEventItems, maxContextContractItemRunes)
		out = append(out, summary)
	}
	return out
}

func compactArcSummaries(summaries []domain.ArcSummary, maxItems int) []domain.ArcSummary {
	if len(summaries) == 0 || maxItems <= 0 {
		return nil
	}
	start := max(len(summaries)-maxItems, 0)
	out := make([]domain.ArcSummary, 0, len(summaries)-start)
	for _, summary := range summaries[start:] {
		summary.Summary = truncateRunes(summary.Summary, maxContextSummaryRunes)
		summary.KeyEvents = compactStringList(summary.KeyEvents, maxContextSummaryEventItems, maxContextContractItemRunes)
		out = append(out, summary)
	}
	return out
}

func compactVolumeSummaries(summaries []domain.VolumeSummary, maxItems int) []domain.VolumeSummary {
	if len(summaries) == 0 || maxItems <= 0 {
		return nil
	}
	start := max(len(summaries)-maxItems, 0)
	out := make([]domain.VolumeSummary, 0, len(summaries)-start)
	for _, summary := range summaries[start:] {
		summary.Summary = truncateRunes(summary.Summary, maxContextSummaryRunes)
		summary.KeyEvents = compactStringList(summary.KeyEvents, maxContextSummaryEventItems, maxContextContractItemRunes)
		out = append(out, summary)
	}
	return out
}

func compactWritingStyleRules(style *domain.WritingStyleRules) *domain.WritingStyleRules {
	if style == nil {
		return nil
	}
	out := *style
	out.Prose = compactStringList(out.Prose, 6, 90)
	out.Taboos = compactStringList(out.Taboos, 8, 90)
	out.Dialogue = compactCharacterVoices(out.Dialogue, 8)
	return &out
}

func compactCharacterVoices(voices []domain.CharacterVoice, maxItems int) []domain.CharacterVoice {
	if len(voices) == 0 || maxItems <= 0 {
		return nil
	}
	limit := min(len(voices), maxItems)
	out := make([]domain.CharacterVoice, 0, limit)
	for _, voice := range voices[:limit] {
		voice.Rules = compactStringList(voice.Rules, 4, 80)
		out = append(out, voice)
	}
	return out
}

func compactAdaptationPlanSummary(plan *domain.AdaptationPlan) map[string]any {
	if plan == nil {
		return nil
	}
	return map[string]any{
		"granularity":        plan.Granularity,
		"mode_policy":        plan.ModePolicy,
		"status":             plan.Status,
		"rewrite_policy":     plan.RewritePolicy,
		"word_tolerance":     adaptationWordToleranceForContext(plan),
		"mainline_rules":     compactStringList(plan.MainlineRules, maxContextAdaptationRuleItems, 120),
		"relationship_goals": compactStringList(plan.RelationshipGoals, maxContextAdaptationRuleItems, 120),
		"source_total_runes": plan.SourceTotalRunes,
		"target_total_runes": plan.TargetTotalRunes,
		"target_min_runes":   plan.TargetMinRunes,
		"target_max_runes":   plan.TargetMaxRunes,
	}
}

func compactLayeredOutlineForPlanning(volumes []domain.VolumeOutline, progress *domain.Progress) []map[string]any {
	if len(volumes) == 0 {
		return nil
	}
	limit := min(len(volumes), maxContextPlanningVolumes)
	out := make([]map[string]any, 0, limit)
	globalChapter := 1
	currentChapter := 0
	currentVolume := 0
	currentArc := 0
	if progress != nil {
		currentChapter = progress.CurrentChapter
		if progress.InProgressChapter > 0 {
			currentChapter = progress.InProgressChapter
		}
		currentVolume = progress.CurrentVolume
		currentArc = progress.CurrentArc
	}

	for _, volume := range volumes[:limit] {
		volumePayload := map[string]any{
			"index": volume.Index,
			"title": truncateRunes(volume.Title, 80),
			"theme": truncateRunes(volume.Theme, maxContextSummaryRunes),
		}
		arcs := make([]map[string]any, 0, len(volume.Arcs))
		for _, arc := range volume.Arcs {
			chapterCount := len(arc.Chapters)
			if chapterCount == 0 {
				chapterCount = arc.EstimatedChapters
			}
			arcStart := globalChapter
			arcEnd := globalChapter + max(chapterCount-1, 0)
			goalLimit := 30
			if volume.Index == currentVolume && arc.Index == currentArc {
				goalLimit = maxContextSummaryRunes
			}
			arcPayload := map[string]any{
				"index":         arc.Index,
				"title":         truncateRunes(arc.Title, 40),
				"goal":          truncateRunes(arc.Goal, goalLimit),
				"from":          arcStart,
				"to":            arcEnd,
				"chapter_count": chapterCount,
				"expanded":      arc.IsExpanded(),
			}
			if arc.EstimatedChapters > 0 && !arc.IsExpanded() {
				arcPayload["estimated_chapters"] = arc.EstimatedChapters
			}
			if arc.IsExpanded() && volume.Index == currentVolume && arc.Index == currentArc {
				from := max(currentChapter-1, arcStart)
				to := min(currentChapter+1, arcEnd)
				chapters := make([]domain.OutlineEntry, 0, len(arc.Chapters))
				chapterNo := arcStart
				for _, entry := range arc.Chapters {
					entry.Chapter = chapterNo
					chapters = append(chapters, entry)
					chapterNo++
				}
				arcPayload["nearby_chapters"] = compactOutlineEntries(outlineEntriesInRange(chapters, from, to))
			}
			arcs = append(arcs, arcPayload)
			globalChapter += chapterCount
		}
		volumePayload["arcs"] = arcs
		out = append(out, volumePayload)
	}
	return out
}

func compactSourceReportsForContext(reports []domain.AdaptationSourceReport, refs []int) []domain.AdaptationSourceReport {
	if len(reports) == 0 || len(refs) == 0 {
		return nil
	}
	want := make(map[int]struct{}, len(refs))
	for _, ref := range refs {
		want[ref] = struct{}{}
	}
	out := make([]domain.AdaptationSourceReport, 0, min(len(refs), maxContextSourceReports))
	for _, report := range reports {
		if _, ok := want[report.Chapter]; !ok {
			continue
		}
		out = append(out, compactSourceReport(report))
		if len(out) >= maxContextSourceReports {
			break
		}
	}
	return out
}

func compactSourceReport(report domain.AdaptationSourceReport) domain.AdaptationSourceReport {
	report.Summary = truncateRunes(report.Summary, maxContextSourceReportSummaryRunes)
	report.Characters = compactStringList(report.Characters, 8, 40)
	report.CharacterFacts = compactStringList(report.CharacterFacts, 3, maxContextSourceReportItemRunes)
	report.KeyEvents = compactStringList(report.KeyEvents, 3, maxContextSourceReportItemRunes)
	report.WorldRules = compactStringList(report.WorldRules, 2, maxContextSourceReportItemRunes)
	report.Timeline = nil
	report.Foreshadow = nil
	report.Relationships = nil
	report.StateChanges = nil
	return report
}

func compactReviewIssues(issues []domain.ConsistencyIssue, maxItems int) []domain.ConsistencyIssue {
	if len(issues) == 0 || maxItems <= 0 {
		return nil
	}
	limit := min(len(issues), maxItems)
	out := make([]domain.ConsistencyIssue, 0, limit)
	for _, issue := range issues[:limit] {
		issue.Description = truncateRunes(issue.Description, maxContextContractItemRunes)
		issue.Evidence = truncateRunes(issue.Evidence, maxContextContractItemRunes)
		issue.Suggestion = truncateRunes(issue.Suggestion, maxContextContractItemRunes)
		out = append(out, issue)
	}
	return out
}

func compactForeshadowUpdates(entries []domain.ForeshadowUpdate, maxItems int) []domain.ForeshadowUpdate {
	if len(entries) == 0 || maxItems <= 0 {
		return nil
	}
	limit := min(len(entries), maxItems)
	out := make([]domain.ForeshadowUpdate, 0, limit)
	for _, entry := range entries[:limit] {
		entry.Description = truncateRunes(entry.Description, maxContextSourceReportItemRunes)
		out = append(out, entry)
	}
	return out
}

func compactUserRulesPayload(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		out[key] = value
	}
	if prefs, ok := out["preferences"].(string); ok {
		out["preferences"] = truncateRunes(prefs, maxContextUserPreferencesRunes)
	}
	if structured, ok := out["structured"].(rules.Structured); ok {
		out["structured"] = compactStructuredRules(structured)
	}
	return out
}

func compactStructuredRules(structured rules.Structured) rules.Structured {
	structured.ForbiddenChars = compactStringList(structured.ForbiddenChars, maxContextUserRuleListItems, 20)
	structured.ForbiddenPhrases = compactStringList(structured.ForbiddenPhrases, maxContextUserRuleListItems, 80)
	structured.FatigueWords = compactFatigueWords(structured.FatigueWords, maxContextUserRuleListItems)
	return structured
}

func compactFatigueWords(words map[string]int, maxItems int) map[string]int {
	if len(words) == 0 || maxItems <= 0 {
		return nil
	}
	keys := make([]string, 0, len(words))
	for key := range words {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > maxItems {
		keys = keys[:maxItems]
	}
	out := make(map[string]int, len(keys))
	for _, key := range keys {
		out[truncateRunes(key, 30)] = words[key]
	}
	return out
}

func compactStringList(items []string, maxItems, maxRunes int) []string {
	if len(items) == 0 || maxItems <= 0 {
		return nil
	}
	limit := min(len(items), maxItems)
	out := make([]string, 0, limit)
	for _, item := range items[:limit] {
		out = append(out, truncateRunes(item, maxRunes))
	}
	return out
}

func compactRecentStrings(items []string, maxItems int) []string {
	if len(items) == 0 || maxItems <= 0 {
		return nil
	}
	start := max(len(items)-maxItems, 0)
	out := make([]string, len(items)-start)
	copy(out, items[start:])
	return out
}

func reverseRelationshipEntries(entries []domain.RelationshipEntry) {
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
}
