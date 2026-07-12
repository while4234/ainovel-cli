package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	AdaptationOutlineQualityAuditVersion = 1
	AdaptationOutlineQualityStatusPassed = "passed"
)

// AdaptationOutlineQualityAudit is a durable marker for the deterministic
// plan-only gate. Its signature covers only contract fields, so normal store
// normalization (budgets/status/rules) does not invalidate a valid audit.
type AdaptationOutlineQualityAudit struct {
	Version   int    `json:"version"`
	Status    string `json:"status"`
	Signature string `json:"signature"`
	CheckedAt string `json:"checked_at"`
}

func AdaptationPlanOutlineQualitySignature(plan AdaptationPlan) string {
	type sourceEvent struct {
		ID            string                    `json:"id"`
		Description   string                    `json:"description"`
		Importance    AdaptationEventImportance `json:"importance"`
		SourceChapter int                       `json:"source_chapter"`
	}
	type volume struct {
		Index            int      `json:"index"`
		TargetFrom       int      `json:"target_from"`
		TargetTo         int      `json:"target_to"`
		SourceFrom       int      `json:"source_from"`
		SourceTo         int      `json:"source_to"`
		MainlineEventIDs []string `json:"mainline_event_ids,omitempty"`
	}
	type chapter struct {
		Chapter         int      `json:"chapter"`
		Title           string   `json:"title"`
		CoreEvent       string   `json:"core_event"`
		Hook            string   `json:"hook"`
		Scenes          []string `json:"scenes,omitempty"`
		EventIDs        []string `json:"event_ids,omitempty"`
		AddedEventIDs   []string `json:"added_event_ids,omitempty"`
		PreserveEvents  []string `json:"preserve_events,omitempty"`
		RequiredChanges []string `json:"required_changes,omitempty"`
		ForbiddenMoves  []string `json:"forbidden_moves,omitempty"`
	}
	contract := struct {
		Granularity  string        `json:"granularity"`
		SourceEvents []sourceEvent `json:"source_events,omitempty"`
		Volumes      []volume      `json:"volumes,omitempty"`
		Chapters     []chapter     `json:"chapters"`
	}{Granularity: NormalizeAdaptationGranularity(plan.Granularity)}
	for _, event := range plan.SourceEvents {
		contract.SourceEvents = append(contract.SourceEvents, sourceEvent{
			ID: strings.TrimSpace(event.ID), Description: strings.TrimSpace(event.Description),
			Importance: event.Importance, SourceChapter: event.SourceChapter,
		})
	}
	for _, item := range plan.Volumes {
		contract.Volumes = append(contract.Volumes, volume{
			Index: item.Index, TargetFrom: item.TargetFrom, TargetTo: item.TargetTo,
			SourceFrom: item.SourceFrom, SourceTo: item.SourceTo,
			MainlineEventIDs: append([]string(nil), item.MainlineEventIDs...),
		})
	}
	for _, item := range plan.Chapters {
		contract.Chapters = append(contract.Chapters, chapter{
			Chapter: item.Chapter, Title: strings.TrimSpace(item.Title),
			CoreEvent: strings.TrimSpace(item.CoreEvent), Hook: strings.TrimSpace(item.Hook),
			Scenes: append([]string(nil), item.Scenes...), EventIDs: append([]string(nil), item.EventIDs...),
			AddedEventIDs: append([]string(nil), item.AddedEventIDs...), PreserveEvents: append([]string(nil), item.PreserveEvents...),
			RequiredChanges: append([]string(nil), item.RequiredChanges...), ForbiddenMoves: append([]string(nil), item.ForbiddenMoves...),
		})
	}
	data, err := json.Marshal(contract)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func AdaptationOutlineQualityPassed(plan AdaptationPlan) bool {
	audit := plan.OutlineQualityAudit
	return audit != nil && audit.Version == AdaptationOutlineQualityAuditVersion &&
		audit.Status == AdaptationOutlineQualityStatusPassed && audit.Signature != "" &&
		audit.Signature == AdaptationPlanOutlineQualitySignature(plan)
}

func MarkAdaptationOutlineQualityPassed(plan *AdaptationPlan) {
	if plan == nil {
		return
	}
	plan.OutlineQualityAudit = &AdaptationOutlineQualityAudit{
		Version:   AdaptationOutlineQualityAuditVersion,
		Status:    AdaptationOutlineQualityStatusPassed,
		Signature: AdaptationPlanOutlineQualitySignature(*plan),
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

func ClearAdaptationOutlineQualityAudit(plan *AdaptationPlan) {
	if plan != nil {
		plan.OutlineQualityAudit = nil
	}
}

// AdaptationEventBindingIssue is the small, dependency-free subset of the
// adaptation contract that runtime code can validate without importing the
// planner. The planner performs the richer semantic outline audit; this
// helper protects writing and commit paths from unknown or multiply-owned
// source event IDs as well.
type AdaptationEventBindingIssue struct {
	Code     string
	EventID  string
	Chapters []int
	Detail   string
}

// AdaptationEventOutlineMismatchIssue reports a source event whose assigned
// chapter has no matching plot theme while another target chapter does. It is
// intentionally defined in domain so runtime tools can use the same final
// fallback as the planner without importing internal/host/adapt.
type AdaptationEventOutlineMismatchIssue struct {
	EventID             string
	Description         string
	SourceChapter       int
	TargetChapter       int
	MissingThemes       []string
	AlternativeChapters []int
	Detail              string
}

type adaptationEventTheme struct {
	name         string
	eventTerms   []string
	outlineTerms []string
}

var arcAdaptationEventThemes = []adaptationEventTheme{
	{
		name:         "encounter_conflict",
		eventTerms:   []string{"抢劫", "劫匪", "黑衣人", "交出手机", "手机", "拦截", "夺刀", "放走", "抢钱", "救母", "晨练"},
		outlineTerms: []string{"抢劫", "劫匪", "黑衣人", "被迫", "交出", "拦截", "夺刀", "钢管", "围堵", "讨债", "出手", "救下", "救人", "冲突"},
	},
	{
		name:         "first_meeting",
		eventTerms:   []string{"结识", "相识", "初遇", "请客吃饭", "带其进入", "第一次见面"},
		outlineTerms: []string{"首次登场", "直视", "叫什么名字", "四目相对", "相识", "结识", "请客吃饭", "邀请", "姓名", "初次对话", "早餐", "早餐对话"},
	},
}

// ValidateArcSourceEventBindings verifies that every event_id used by an arc
// chapter is a known source event and that one source event has one owner.
// added_event_ids are intentionally excluded: they are target-story events,
// not source-event bindings.
func ValidateArcSourceEventBindings(plan AdaptationPlan) []AdaptationEventBindingIssue {
	if NormalizeAdaptationGranularity(plan.Granularity) != AdaptationGranularityArc {
		return nil
	}
	sourceEvents := make(map[string]AdaptationEvent, len(plan.SourceEvents))
	for _, event := range plan.SourceEvents {
		if eventID := strings.TrimSpace(event.ID); eventID != "" {
			sourceEvents[eventID] = event
		}
	}
	issues := make([]AdaptationEventBindingIssue, 0)
	bindings := make(map[string][]int)
	for index, chapter := range plan.Chapters {
		number := chapter.Chapter
		if number <= 0 {
			number = index + 1
		}
		for _, rawEventID := range chapter.EventIDs {
			eventID := strings.TrimSpace(rawEventID)
			if eventID == "" {
				continue
			}
			if _, known := sourceEvents[eventID]; !known && strings.HasPrefix(eventID, "src-") {
				issues = append(issues, AdaptationEventBindingIssue{
					Code:     "arc_event_unknown",
					EventID:  eventID,
					Chapters: []int{number},
					Detail:   fmt.Sprintf("target chapter %d references unknown source event %s", number, eventID),
				})
				continue
			}
			// Keep repeated occurrences in the slice so the validator also rejects
			// the same event listed twice inside one chapter.
			bindings[eventID] = append(bindings[eventID], number)
		}
	}
	for eventID, chapters := range bindings {
		if len(chapters) <= 1 {
			continue
		}
		issues = append(issues, AdaptationEventBindingIssue{
			Code:     "arc_event_duplicate_binding",
			EventID:  eventID,
			Chapters: append([]int(nil), chapters...),
			Detail:   fmt.Sprintf("source event %s is bound to target chapters %v; it must have exactly one owning chapter", eventID, chapters),
		})
	}
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].Code != issues[right].Code {
			return issues[left].Code < issues[right].Code
		}
		return issues[left].EventID < issues[right].EventID
	})
	return issues
}

// ValidateArcEventOutlineThemes is the shared semantic safety net for arc
// plans. It is deliberately conservative: it only reports a mismatch when a
// recognized event theme is absent from its owner but is present in another
// target chapter. Supporting/texture events remain optional when the planner
// does not bind them at all.
func ValidateArcEventOutlineThemes(plan AdaptationPlan) []AdaptationEventOutlineMismatchIssue {
	if NormalizeAdaptationGranularity(plan.Granularity) != AdaptationGranularityArc {
		return nil
	}
	sourceEvents := make(map[string]AdaptationEvent, len(plan.SourceEvents))
	for _, event := range plan.SourceEvents {
		if eventID := strings.TrimSpace(event.ID); eventID != "" {
			sourceEvents[eventID] = event
		}
	}
	bindings := make(map[string][]int)
	for index, chapter := range plan.Chapters {
		number := chapter.Chapter
		if number <= 0 {
			number = index + 1
		}
		for _, rawEventID := range chapter.EventIDs {
			eventID := strings.TrimSpace(rawEventID)
			if eventID != "" {
				if !containsInt(bindings[eventID], number) {
					bindings[eventID] = append(bindings[eventID], number)
				}
			}
		}
	}
	issues := make([]AdaptationEventOutlineMismatchIssue, 0)
	for eventID, chapters := range bindings {
		event, known := sourceEvents[eventID]
		if !known {
			continue
		}
		themes := adaptationThemeNames(event.Description, true)
		if len(themes) == 0 {
			continue
		}
		for _, owner := range chapters {
			ownerThemes := adaptationChapterThemeSet(plan.Chapters, owner)
			missing := differenceThemeNames(themes, ownerThemes)
			if len(missing) == 0 {
				continue
			}
			alternatives := make([]int, 0)
			for _, candidate := range plan.Chapters {
				candidateNumber := candidate.Chapter
				if candidateNumber == owner {
					continue
				}
				if len(intersectThemeNames(missing, adaptationChapterThemeSet([]AdaptationChapterPlan{candidate}, candidateNumber))) > 0 {
					alternatives = append(alternatives, candidateNumber)
				}
			}
			if len(alternatives) == 0 {
				continue
			}
			issues = append(issues, AdaptationEventOutlineMismatchIssue{
				EventID:             eventID,
				Description:         event.Description,
				SourceChapter:       event.SourceChapter,
				TargetChapter:       owner,
				MissingThemes:       missing,
				AlternativeChapters: alternatives,
				Detail: fmt.Sprintf(
					"source event %s (%s) is bound to target chapter %d, but its plot theme(s) %s are absent from that chapter and appear in target chapter(s) %v; move event_ids, preserve_events, required_changes, and the matching story beat together before writing",
					eventID, clipAdaptationText(event.Description, 120), owner, strings.Join(missing, ", "), alternatives,
				),
			})
		}
	}
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].TargetChapter != issues[right].TargetChapter {
			return issues[left].TargetChapter < issues[right].TargetChapter
		}
		return issues[left].EventID < issues[right].EventID
	})
	return issues
}

func adaptationThemeNames(text string, event bool) []string {
	set := make(map[string]bool)
	for _, theme := range arcAdaptationEventThemes {
		terms := theme.outlineTerms
		if event {
			terms = theme.eventTerms
		}
		for _, term := range terms {
			if strings.Contains(text, term) {
				set[theme.name] = true
				break
			}
		}
	}
	out := make([]string, 0, len(set))
	for _, theme := range arcAdaptationEventThemes {
		if set[theme.name] {
			out = append(out, theme.name)
		}
	}
	return out
}

func adaptationChapterThemeSet(chapters []AdaptationChapterPlan, number int) map[string]bool {
	set := make(map[string]bool)
	for _, chapter := range chapters {
		if chapter.Chapter != number {
			continue
		}
		text := strings.Join([]string{chapter.Title, chapter.CoreEvent, chapter.Hook, strings.Join(chapter.Scenes, " ")}, "\n")
		for _, theme := range adaptationThemeNames(text, false) {
			set[theme] = true
		}
		return set
	}
	return set
}

func differenceThemeNames(themes []string, present map[string]bool) []string {
	out := make([]string, 0, len(themes))
	for _, theme := range themes {
		if !present[theme] {
			out = append(out, theme)
		}
	}
	return out
}

func intersectThemeNames(themes []string, present map[string]bool) []string {
	out := make([]string, 0, len(themes))
	for _, theme := range themes {
		if present[theme] {
			out = append(out, theme)
		}
	}
	return out
}

func containsInt(values []int, wanted int) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func clipAdaptationText(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "..."
}
