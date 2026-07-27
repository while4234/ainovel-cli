package domain

import (
	"fmt"
	"sort"
	"strings"
)

// OutlineCharacterGap is returned to Host instead of letting Architect invent
// a missing important character.
type OutlineCharacterGap struct {
	Chapter     int    `json:"chapter"`
	CharacterID string `json:"character_id,omitempty"`
	Role        string `json:"role,omitempty"`
	Reason      string `json:"reason"`
	Route       string `json:"route"`
}

// OutlineCharacterGapError preserves machine-readable routing evidence.
type OutlineCharacterGapError struct {
	Gaps []OutlineCharacterGap `json:"gaps"`
}

func (e *OutlineCharacterGapError) Error() string {
	return fmt.Sprintf("outline contains %d character gap(s); route to Character Agent", len(e.Gaps))
}

// PrepareOutlineCharacters trims, deduplicates, validates, and opportunistically
// hydrates legacy name-only entries without changing chapter numbers or IDs.
func PrepareOutlineCharacters(entries []OutlineEntry, characters []Character) ([]OutlineEntry, error) {
	out := append([]OutlineEntry(nil), entries...)
	byID := make(map[string]Character, len(characters))
	for _, character := range characters {
		id := strings.TrimSpace(character.ID)
		if id != "" {
			byID[id] = character
		}
	}
	var gaps []OutlineCharacterGap
	for i := range out {
		out[i] = hydrateOutlineCharacterIDs(out[i], characters)
		out[i].CharacterIDs = normalizedOutlineIDs(out[i].CharacterIDs)
		for beatIndex := range out[i].CharacterBeats {
			beat := &out[i].CharacterBeats[beatIndex]
			beat.CharacterID = strings.TrimSpace(beat.CharacterID)
			if beat.CharacterID == "" {
				gaps = append(gaps, OutlineCharacterGap{
					Chapter: out[i].Chapter,
					Reason:  "character beat requires a stable character_id",
					Route:   "character",
				})
				continue
			}
			out[i].CharacterIDs = normalizedOutlineIDs(append(out[i].CharacterIDs, beat.CharacterID))
		}
		for _, id := range out[i].CharacterIDs {
			if _, ok := byID[id]; !ok {
				gaps = append(gaps, OutlineCharacterGap{
					Chapter: out[i].Chapter, CharacterID: id,
					Reason: "stable character ID is absent from confirmed StoryFoundation",
					Route:  "character",
				})
			}
		}
		for _, relation := range out[i].RelationshipBeats {
			endpoints := []string{strings.TrimSpace(relation.SourceCharacterID), strings.TrimSpace(relation.TargetCharacterID)}
			for _, id := range endpoints {
				if id == "" {
					gaps = append(gaps, OutlineCharacterGap{
						Chapter: out[i].Chapter,
						Reason:  "relationship beat requires stable source and target character IDs",
						Route:   "character",
					})
					continue
				}
				if _, ok := byID[id]; !ok {
					gaps = append(gaps, OutlineCharacterGap{
						Chapter: out[i].Chapter, CharacterID: id,
						Reason: "relationship beat endpoint is absent from confirmed StoryFoundation",
						Route:  "character",
					})
				}
			}
		}
		for _, need := range out[i].TemporaryRoles {
			if need.Important {
				gaps = append(gaps, OutlineCharacterGap{
					Chapter: out[i].Chapter, Role: strings.TrimSpace(need.Role),
					Reason: "important temporary role requires a reviewed canonical character",
					Route:  "character",
				})
			}
		}
	}
	if len(gaps) > 0 {
		return nil, &OutlineCharacterGapError{Gaps: uniqueOutlineCharacterGaps(gaps)}
	}
	return out, nil
}

func hydrateOutlineCharacterIDs(entry OutlineEntry, characters []Character) OutlineEntry {
	if len(entry.CharacterIDs) > 0 {
		return entry
	}
	text := strings.Join(append([]string{entry.Title, entry.CoreEvent, entry.Hook}, entry.Scenes...), " ")
	for _, character := range characters {
		if strings.TrimSpace(character.ID) == "" {
			continue
		}
		if outlineMentionsCharacter(text, character) {
			entry.CharacterIDs = append(entry.CharacterIDs, character.ID)
		}
	}
	return entry
}

func outlineMentionsCharacter(text string, character Character) bool {
	for _, name := range append([]string{character.Name}, character.Aliases...) {
		name = strings.TrimSpace(name)
		if name != "" && strings.Contains(text, name) {
			return true
		}
	}
	return false
}

func normalizedOutlineIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func uniqueOutlineCharacterGaps(gaps []OutlineCharacterGap) []OutlineCharacterGap {
	seen := make(map[string]struct{}, len(gaps))
	out := make([]OutlineCharacterGap, 0, len(gaps))
	for _, gap := range gaps {
		key := fmt.Sprintf("%d|%s|%s|%s", gap.Chapter, gap.CharacterID, gap.Role, gap.Reason)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, gap)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Chapter != out[j].Chapter {
			return out[i].Chapter < out[j].Chapter
		}
		return out[i].CharacterID+out[i].Role < out[j].CharacterID+out[j].Role
	})
	return out
}
