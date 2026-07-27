package domain

import (
	"fmt"
	"sort"
	"strings"
)

const AdaptationSourceCharacterIndexVersion = 1

type AdaptationCharacterEvidenceReference struct {
	Chapter      int    `json:"chapter,omitempty"`
	ReportSHA256 string `json:"report_sha256,omitempty"`
	Kind         string `json:"kind"`
	Summary      string `json:"summary,omitempty"`
}

type AdaptationSourceCharacterRelationship struct {
	OtherCharacterID string `json:"other_character_id,omitempty"`
	OtherName        string `json:"other_name"`
	Relation         string `json:"relation"`
	Chapter          int    `json:"chapter,omitempty"`
}

type AdaptationSourceCharacterStateChange struct {
	Chapter  int    `json:"chapter,omitempty"`
	Field    string `json:"field"`
	OldValue string `json:"old_value,omitempty"`
	NewValue string `json:"new_value"`
	Reason   string `json:"reason,omitempty"`
}

type AdaptationSourceCharacterIndexEntry struct {
	ID                 string                                  `json:"id"`
	CanonicalName      string                                  `json:"canonical_name"`
	Aliases            []string                                `json:"aliases"`
	Profile            Character                               `json:"profile"`
	Chapters           []int                                   `json:"chapters"`
	Evidence           []AdaptationCharacterEvidenceReference  `json:"evidence"`
	Facts              []string                                `json:"facts"`
	Relationships      []AdaptationSourceCharacterRelationship `json:"relationships"`
	StateChanges       []AdaptationSourceCharacterStateChange  `json:"state_changes"`
	AppearanceCount    int                                     `json:"appearance_count"`
	CausalEventCount   int                                     `json:"causal_event_count"`
	DossierMajor       bool                                    `json:"dossier_major"`
	CoreCast           bool                                    `json:"core_cast"`
	Named              bool                                    `json:"named"`
	ImportanceEvidence []string                                `json:"importance_evidence"`
	Conflicts          []string                                `json:"conflicts"`
	Uncertainties      []string                                `json:"uncertainties"`
}

type AdaptationSourceCharacterIndex struct {
	Version        int                                   `json:"version"`
	InputSignature string                                `json:"input_signature"`
	Characters     []AdaptationSourceCharacterIndexEntry `json:"characters"`
}

type AdaptationCharacterCoverageDecision struct {
	SourceCharacterID string        `json:"source_character_id"`
	CanonicalName     string        `json:"canonical_name"`
	SuggestedTier     CharacterTier `json:"suggested_tier"`
	DecisionRequired  bool          `json:"decision_required"`
	Reasons           []string      `json:"reasons"`
	MappingID         string        `json:"mapping_id,omitempty"`
	Action            string        `json:"action,omitempty"`
	Pending           bool          `json:"pending"`
	Blocking          bool          `json:"blocking"`
}

type AdaptationCharacterCoverage struct {
	SourceTotal        int                                   `json:"source_total"`
	DecisionRequired   int                                   `json:"decision_required"`
	Mapped             int                                   `json:"mapped"`
	ExplicitlyExcluded int                                   `json:"explicitly_excluded"`
	Pending            int                                   `json:"pending"`
	BlockingGaps       int                                   `json:"blocking_gaps"`
	Decisions          []AdaptationCharacterCoverageDecision `json:"decisions"`
}

type pendingAdaptationRelationship struct {
	ownerID   string
	otherName string
	relation  string
	chapter   int
}

// BuildAdaptationSourceCharacterIndex deterministically derives a bounded,
// auditable character index. SourceFoundation and chapter reports remain the
// source of truth; the returned signature makes any cached copy disposable.
func BuildAdaptationSourceCharacterIndex(
	source *AdaptationSourceFoundation,
	reports []AdaptationSourceReport,
	dossier *AdaptationCoCreateDossier,
	coreCast *CoreCastContract,
) (AdaptationSourceCharacterIndex, error) {
	signature, err := jsonSignature(struct {
		Source  *AdaptationSourceFoundation `json:"source_foundation"`
		Reports []AdaptationSourceReport    `json:"reports"`
		Dossier *AdaptationCoCreateDossier  `json:"dossier"`
		Core    *CoreCastContract           `json:"core_cast"`
	}{source, reports, dossier, coreCast})
	if err != nil {
		return AdaptationSourceCharacterIndex{}, fmt.Errorf("sign adaptation source character inputs: %w", err)
	}

	entries := make(map[string]*AdaptationSourceCharacterIndexEntry)
	identityOwners := make(map[string][]string)
	registerIdentity := func(id, value string) {
		key := normalizedIdentity(value)
		if key == "" {
			return
		}
		for _, owner := range identityOwners[key] {
			if owner == id {
				return
			}
		}
		identityOwners[key] = append(identityOwners[key], id)
		sort.Strings(identityOwners[key])
	}
	addProfile := func(profile Character) *AdaptationSourceCharacterIndexEntry {
		profile = CloneCharacter(profile)
		profile.Name = strings.TrimSpace(profile.Name)
		if profile.Name == "" {
			return nil
		}
		profile.ID = strings.TrimSpace(profile.ID)
		if profile.ID == "" {
			// Chapter analyzers may omit an ID while SourceFoundation already
			// owns the stable identity. Merge only when the name/alias resolves
			// uniquely; explicit distinct IDs with the same display name remain
			// separate and are surfaced as ambiguous evidence below.
			owners := identityOwners[normalizedIdentity(profile.Name)]
			if len(owners) == 1 {
				profile.ID = owners[0]
			} else if len(owners) > 1 {
				for _, owner := range owners {
					if entry := entries[owner]; entry != nil {
						entry.Conflicts = appendUniqueString(
							entry.Conflicts,
							fmt.Sprintf("ID-less profile %q matches multiple explicit source characters", profile.Name),
						)
					}
				}
				return nil
			} else {
				profile.ID = stableFoundationID("source-char", normalizedIdentity(profile.Name))
			}
		}
		entry := entries[profile.ID]
		if entry == nil {
			entry = &AdaptationSourceCharacterIndexEntry{
				ID:            profile.ID,
				CanonicalName: profile.Name,
				Aliases:       normalizedStrings(profile.Aliases),
				Profile:       profile,
			}
			entries[profile.ID] = entry
		} else {
			mergeAdaptationSourceProfile(entry, profile)
		}
		registerIdentity(entry.ID, entry.CanonicalName)
		for _, alias := range entry.Aliases {
			registerIdentity(entry.ID, alias)
		}
		return entry
	}

	if source != nil {
		for _, character := range source.Characters {
			entry := addProfile(character)
			if entry != nil {
				entry.Evidence = appendUniqueAdaptationEvidence(entry.Evidence, AdaptationCharacterEvidenceReference{
					Kind: "source_foundation", Summary: "source character profile",
				})
			}
		}
	}

	resolveName := func(name string) []*AdaptationSourceCharacterIndexEntry {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil
		}
		owners := identityOwners[normalizedIdentity(name)]
		if len(owners) == 0 {
			entry := addProfile(Character{Name: name, Traits: []string{}})
			if entry == nil {
				return nil
			}
			return []*AdaptationSourceCharacterIndexEntry{entry}
		}
		out := make([]*AdaptationSourceCharacterIndexEntry, 0, len(owners))
		for _, id := range owners {
			if entry := entries[id]; entry != nil {
				out = append(out, entry)
			}
		}
		return out
	}

	var pendingRelationships []pendingAdaptationRelationship
	for _, report := range reports {
		for _, profile := range report.CharacterProfiles {
			entry := addProfile(profile)
			if entry == nil {
				continue
			}
			addAdaptationChapterEvidence(entry, report, "character_profile", "structured chapter character profile")
		}
		for _, name := range report.Characters {
			resolved := resolveName(name)
			for _, entry := range resolved {
				addAdaptationChapterEvidence(entry, report, "appearance", "")
				if len(resolved) > 1 {
					entry.Conflicts = appendUniqueString(entry.Conflicts,
						fmt.Sprintf("chapter %d uses ambiguous identity %q", report.Chapter, strings.TrimSpace(name)))
				}
			}
		}
		for _, fact := range report.CharacterFacts {
			for _, entry := range entriesMentionedByFact(entries, fact) {
				entry.Facts = appendUniqueString(entry.Facts, fact)
				entry.Evidence = appendUniqueAdaptationEvidence(entry.Evidence, AdaptationCharacterEvidenceReference{
					Chapter: report.Chapter, ReportSHA256: report.SourceSHA256, Kind: "character_fact",
					Summary: compactAdaptationEvidence(fact),
				})
			}
		}
		for _, relation := range report.Relationships {
			left := resolveName(relation.CharacterA)
			right := resolveName(relation.CharacterB)
			for _, owner := range left {
				pendingRelationships = append(pendingRelationships, pendingAdaptationRelationship{
					ownerID: owner.ID, otherName: relation.CharacterB, relation: relation.Relation, chapter: report.Chapter,
				})
			}
			for _, owner := range right {
				pendingRelationships = append(pendingRelationships, pendingAdaptationRelationship{
					ownerID: owner.ID, otherName: relation.CharacterA, relation: relation.Relation, chapter: report.Chapter,
				})
			}
		}
		for _, change := range report.StateChanges {
			for _, entry := range resolveName(change.Entity) {
				entry.StateChanges = appendUniqueAdaptationStateChange(entry.StateChanges, AdaptationSourceCharacterStateChange{
					Chapter: report.Chapter, Field: change.Field, OldValue: change.OldValue,
					NewValue: change.NewValue, Reason: change.Reason,
				})
			}
		}
		for _, event := range append(append([]string(nil), report.KeyEvents...), report.Summary) {
			for _, entry := range entriesMentionedByFact(entries, event) {
				entry.CausalEventCount++
			}
		}
	}

	for _, relation := range pendingRelationships {
		entry := entries[relation.ownerID]
		if entry == nil {
			continue
		}
		otherID := ""
		if owners := identityOwners[normalizedIdentity(relation.otherName)]; len(owners) == 1 {
			otherID = owners[0]
		}
		entry.Relationships = appendUniqueAdaptationRelationship(entry.Relationships, AdaptationSourceCharacterRelationship{
			OtherCharacterID: otherID, OtherName: strings.TrimSpace(relation.otherName),
			Relation: strings.TrimSpace(relation.relation), Chapter: relation.chapter,
		})
	}

	if dossier != nil {
		for _, batch := range dossier.Batches {
			for _, name := range batch.MajorCharacters {
				for _, entry := range resolveName(name) {
					entry.DossierMajor = true
				}
			}
		}
	}
	if coreCast != nil {
		normalized, normalizeErr := NormalizeCoreCastContract(*coreCast)
		if normalizeErr != nil {
			return AdaptationSourceCharacterIndex{}, fmt.Errorf("normalize adaptation core cast for source index: %w", normalizeErr)
		}
		for _, member := range normalized.Members {
			for _, sourceID := range member.SourceCharacterIDs {
				if entry := entries[sourceID]; entry != nil {
					entry.CoreCast = true
				}
			}
		}
		for _, disposition := range normalized.SourceDispositions {
			if entry := entries[disposition.SourceCharacterID]; entry != nil {
				entry.CoreCast = true
			}
		}
	}

	out := AdaptationSourceCharacterIndex{
		Version: AdaptationSourceCharacterIndexVersion, InputSignature: signature,
		Characters: make([]AdaptationSourceCharacterIndexEntry, 0, len(entries)),
	}
	for _, entry := range entries {
		finalizeAdaptationSourceCharacterEntry(entry)
		out.Characters = append(out.Characters, *entry)
	}
	sort.Slice(out.Characters, func(i, j int) bool { return out.Characters[i].ID < out.Characters[j].ID })
	return out, nil
}

func EvaluateAdaptationCharacterCoverage(
	index AdaptationSourceCharacterIndex,
	mappings []CharacterSourceMapping,
) (AdaptationCharacterCoverage, error) {
	if index.Version != AdaptationSourceCharacterIndexVersion || len(index.InputSignature) != 64 {
		return AdaptationCharacterCoverage{}, fmt.Errorf("adaptation source character index is invalid")
	}
	mappedBySource := make(map[string]CharacterSourceMapping)
	for _, mapping := range mappings {
		normalized := mapping
		normalizeCharacterSourceMapping(&normalized)
		if err := validateCharacterSourceMapping(normalized); err != nil {
			return AdaptationCharacterCoverage{}, err
		}
		for _, sourceID := range normalized.SourceCharacterIDs {
			if owner, exists := mappedBySource[sourceID]; exists && owner.ID != normalized.ID {
				return AdaptationCharacterCoverage{}, fmt.Errorf(
					"source character %q has conflicting mappings %q and %q", sourceID, owner.ID, normalized.ID,
				)
			}
			mappedBySource[sourceID] = normalized
		}
	}

	coverage := AdaptationCharacterCoverage{SourceTotal: len(index.Characters)}
	for _, entry := range index.Characters {
		tier, required, reasons := adaptationCoverageRule(entry)
		decision := AdaptationCharacterCoverageDecision{
			SourceCharacterID: entry.ID,
			CanonicalName:     entry.CanonicalName,
			SuggestedTier:     tier,
			DecisionRequired:  required,
			Reasons:           reasons,
		}
		if required {
			coverage.DecisionRequired++
		}
		if mapping, exists := mappedBySource[entry.ID]; exists {
			decision.MappingID = mapping.ID
			decision.Action = string(mapping.Action)
			coverage.Mapped++
			if mapping.Action == CharacterSourceExclude {
				coverage.ExplicitlyExcluded++
			}
		} else {
			decision.Pending = true
			decision.Blocking = required
			coverage.Pending++
			if required {
				coverage.BlockingGaps++
			}
		}
		coverage.Decisions = append(coverage.Decisions, decision)
	}
	return coverage, nil
}

func adaptationCoverageRule(entry AdaptationSourceCharacterIndexEntry) (CharacterTier, bool, []string) {
	var reasons []string
	if entry.CoreCast {
		reasons = append(reasons, "CoreCast source disposition")
	}
	if entry.DossierMajor {
		reasons = append(reasons, "dossier major character")
	}
	if entry.AppearanceCount > 1 {
		reasons = append(reasons, fmt.Sprintf("appears in %d chapters", entry.AppearanceCount))
	}
	if entry.Profile.Goal != "" || entry.Profile.Motivation != "" {
		reasons = append(reasons, "has an independent goal or motivation")
	}
	if len(entry.Relationships) > 0 {
		reasons = append(reasons, "has a persistent relationship signal")
	}
	if entry.CausalEventCount > 0 {
		reasons = append(reasons, "causally affects reported events")
	}
	if len(entry.StateChanges) > 0 {
		reasons = append(reasons, "has reported state changes")
	}

	required := entry.CoreCast || entry.DossierMajor ||
		(entry.Named && (entry.AppearanceCount > 1 || entry.Profile.Goal != "" ||
			entry.Profile.Motivation != "" || len(entry.Relationships) > 0 ||
			entry.CausalEventCount > 0 || len(entry.StateChanges) > 0))
	if entry.CoreCast || entry.DossierMajor || entry.AppearanceCount >= 3 || entry.CausalEventCount >= 2 {
		return CharacterTierImportant, true, reasons
	}
	if required {
		return CharacterTierSecondary, true, reasons
	}
	if len(reasons) == 0 {
		reasons = []string{"single appearance without independent motive or continuing causal effect"}
	}
	return CharacterTierDecorative, false, reasons
}

func mergeAdaptationSourceProfile(entry *AdaptationSourceCharacterIndexEntry, incoming Character) {
	if entry == nil {
		return
	}
	incoming = CloneCharacter(incoming)
	entry.Aliases = normalizedStrings(append(entry.Aliases, incoming.Aliases...))
	for _, pair := range [][2]*string{
		{&entry.Profile.Role, &incoming.Role},
		{&entry.Profile.Description, &incoming.Description},
		{&entry.Profile.Arc, &incoming.Arc},
		{&entry.Profile.Tier, &incoming.Tier},
		{&entry.Profile.Faction, &incoming.Faction},
		{&entry.Profile.Goal, &incoming.Goal},
		{&entry.Profile.Motivation, &incoming.Motivation},
		{&entry.Profile.Conflict, &incoming.Conflict},
		{&entry.Profile.Voice, &incoming.Voice},
		{&entry.Profile.Notes, &incoming.Notes},
	} {
		current := strings.TrimSpace(*pair[0])
		next := strings.TrimSpace(*pair[1])
		if current == "" {
			*pair[0] = next
		} else if next != "" && current != next {
			entry.Conflicts = appendUniqueString(entry.Conflicts, current+" <> "+next)
		}
	}
	entry.Profile.Aliases = append([]string(nil), entry.Aliases...)
	entry.Profile.Traits = normalizedStrings(append(entry.Profile.Traits, incoming.Traits...))
	entry.Profile.Constraints = normalizedStrings(append(entry.Profile.Constraints, incoming.Constraints...))
	entry.Profile.ContrastDetails = appendUniqueContrast(entry.Profile.ContrastDetails, incoming.ContrastDetails...)
	entry.Profile.KeyBackstory = appendUniqueBackstory(entry.Profile.KeyBackstory, incoming.KeyBackstory...)
	if entry.Profile.InitialState == nil && incoming.InitialState != nil {
		initial := *incoming.InitialState
		initial.Resources = append([]string(nil), incoming.InitialState.Resources...)
		entry.Profile.InitialState = &initial
	}
	if entry.Profile.KnowledgeBoundary == nil && incoming.KnowledgeBoundary != nil {
		boundary := *incoming.KnowledgeBoundary
		boundary.Known = append([]string(nil), incoming.KnowledgeBoundary.Known...)
		boundary.Unknown = append([]string(nil), incoming.KnowledgeBoundary.Unknown...)
		boundary.Misconceptions = append([]string(nil), incoming.KnowledgeBoundary.Misconceptions...)
		boundary.Forbidden = append([]string(nil), incoming.KnowledgeBoundary.Forbidden...)
		entry.Profile.KnowledgeBoundary = &boundary
	}
}

func addAdaptationChapterEvidence(
	entry *AdaptationSourceCharacterIndexEntry,
	report AdaptationSourceReport,
	kind, summary string,
) {
	if entry == nil {
		return
	}
	entry.Chapters = appendUniqueInt(entry.Chapters, report.Chapter)
	entry.Evidence = appendUniqueAdaptationEvidence(entry.Evidence, AdaptationCharacterEvidenceReference{
		Chapter: report.Chapter, ReportSHA256: report.SourceSHA256, Kind: kind,
		Summary: compactAdaptationEvidence(summary),
	})
}

func entriesMentionedByFact(
	entries map[string]*AdaptationSourceCharacterIndexEntry,
	fact string,
) []*AdaptationSourceCharacterIndexEntry {
	factKey := normalizedIdentity(fact)
	if factKey == "" {
		return nil
	}
	var out []*AdaptationSourceCharacterIndexEntry
	for _, entry := range entries {
		for _, name := range append([]string{entry.CanonicalName}, entry.Aliases...) {
			key := normalizedIdentity(name)
			if key != "" && strings.Contains(factKey, key) {
				out = append(out, entry)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func finalizeAdaptationSourceCharacterEntry(entry *AdaptationSourceCharacterIndexEntry) {
	entry.Aliases = normalizedStrings(entry.Aliases)
	entry.Profile.ID = entry.ID
	entry.Profile.Name = entry.CanonicalName
	entry.Profile.Aliases = append([]string(nil), entry.Aliases...)
	entry.Chapters = sortedUniqueInts(entry.Chapters)
	entry.AppearanceCount = len(entry.Chapters)
	entry.Facts = normalizedStrings(entry.Facts)
	entry.Conflicts = normalizedStrings(entry.Conflicts)
	entry.Named = likelyNamedAdaptationCharacter(entry.CanonicalName, entry.Profile.Role)
	if entry.Profile.Goal == "" && entry.Profile.Motivation == "" {
		entry.Uncertainties = append(entry.Uncertainties, "evidence insufficient for an independent goal or motivation")
	}
	if entry.Profile.KnowledgeBoundary == nil {
		entry.Uncertainties = append(entry.Uncertainties, "evidence insufficient for a knowledge boundary")
	}
	entry.Uncertainties = normalizedStrings(entry.Uncertainties)
	entry.ImportanceEvidence = nil
	_, _, entry.ImportanceEvidence = adaptationCoverageRule(*entry)
	sort.Slice(entry.Evidence, func(i, j int) bool {
		left := fmt.Sprintf("%09d\x00%s\x00%s", entry.Evidence[i].Chapter, entry.Evidence[i].Kind, entry.Evidence[i].Summary)
		right := fmt.Sprintf("%09d\x00%s\x00%s", entry.Evidence[j].Chapter, entry.Evidence[j].Kind, entry.Evidence[j].Summary)
		return left < right
	})
	sort.Slice(entry.Relationships, func(i, j int) bool {
		left := fmt.Sprintf("%09d\x00%s\x00%s", entry.Relationships[i].Chapter, entry.Relationships[i].OtherName, entry.Relationships[i].Relation)
		right := fmt.Sprintf("%09d\x00%s\x00%s", entry.Relationships[j].Chapter, entry.Relationships[j].OtherName, entry.Relationships[j].Relation)
		return left < right
	})
	sort.Slice(entry.StateChanges, func(i, j int) bool {
		left := fmt.Sprintf("%09d\x00%s\x00%s", entry.StateChanges[i].Chapter, entry.StateChanges[i].Field, entry.StateChanges[i].NewValue)
		right := fmt.Sprintf("%09d\x00%s\x00%s", entry.StateChanges[j].Chapter, entry.StateChanges[j].Field, entry.StateChanges[j].NewValue)
		return left < right
	})
}

func likelyNamedAdaptationCharacter(name, role string) bool {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return false
	}
	for _, marker := range []string{
		"未命名", "无名", "路人", "群众", "店员", "侍卫", "士兵", "管理员",
		"服务员", "司机", "医生", "护士", "老师", "同学", "unknown", "unnamed",
	} {
		if strings.Contains(key, marker) &&
			(strings.TrimSpace(role) == "" || normalizedIdentity(name) == normalizedIdentity(role)) {
			return false
		}
	}
	return true
}

func appendUniqueAdaptationEvidence(
	values []AdaptationCharacterEvidenceReference,
	value AdaptationCharacterEvidenceReference,
) []AdaptationCharacterEvidenceReference {
	value.Kind = strings.TrimSpace(value.Kind)
	value.ReportSHA256 = strings.TrimSpace(value.ReportSHA256)
	value.Summary = compactAdaptationEvidence(value.Summary)
	if value.Kind == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueAdaptationRelationship(
	values []AdaptationSourceCharacterRelationship,
	value AdaptationSourceCharacterRelationship,
) []AdaptationSourceCharacterRelationship {
	if value.OtherName == "" || value.Relation == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueAdaptationStateChange(
	values []AdaptationSourceCharacterStateChange,
	value AdaptationSourceCharacterStateChange,
) []AdaptationSourceCharacterStateChange {
	if strings.TrimSpace(value.Field) == "" || strings.TrimSpace(value.NewValue) == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueContrast(values []CharacterContrastDetail, incoming ...CharacterContrastDetail) []CharacterContrastDetail {
	for _, value := range incoming {
		found := false
		for _, existing := range values {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			values = append(values, value)
		}
	}
	return values
}

func appendUniqueBackstory(values []CharacterBackstory, incoming ...CharacterBackstory) []CharacterBackstory {
	for _, value := range incoming {
		found := false
		for _, existing := range values {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			values = append(values, value)
		}
	}
	return values
}

func appendUniqueString(values []string, value string) []string {
	return normalizedStrings(append(values, value))
}

func appendUniqueInt(values []int, value int) []int {
	if value <= 0 {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func sortedUniqueInts(values []int) []int {
	out := append([]int(nil), values...)
	sort.Ints(out)
	if len(out) < 2 {
		return out
	}
	compacted := out[:1]
	for _, value := range out[1:] {
		if value != compacted[len(compacted)-1] {
			compacted = append(compacted, value)
		}
	}
	return compacted
}

func compactAdaptationEvidence(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	const maxRunes = 240
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return value
}
