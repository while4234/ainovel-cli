package domain

import (
	"errors"
	"testing"
)

func TestPrepareOutlineCharactersUsesStableIDsAndLegacyAliasFallback(t *testing.T) {
	characters := []Character{
		{ID: "hero", Name: "林", Aliases: []string{"导师"}, Tier: "core"},
		{ID: "ally", Name: "岚", Tier: "important"},
	}
	entries := []OutlineEntry{
		{Chapter: 1, ID: "chapter-one", CoreEvent: "导师给出选择"},
		{Chapter: 2, ID: "chapter-two", CharacterIDs: []string{"ally"}, CharacterBeats: []OutlineCharacterBeat{{CharacterID: "hero", Goal: "阻止误判"}}},
	}
	got, err := PrepareOutlineCharacters(entries, characters)
	if err != nil {
		t.Fatalf("PrepareOutlineCharacters: %v", err)
	}
	if len(got[0].CharacterIDs) != 1 || got[0].CharacterIDs[0] != "hero" {
		t.Fatalf("legacy alias fallback = %+v", got[0].CharacterIDs)
	}
	if got[0].ID != "chapter-one" || got[0].Chapter != 1 {
		t.Fatalf("stable outline identity changed: %+v", got[0])
	}
	if len(got[1].CharacterIDs) != 2 {
		t.Fatalf("beat ID was not merged: %+v", got[1].CharacterIDs)
	}
}

func TestPrepareOutlineCharactersRoutesUnknownImportantRole(t *testing.T) {
	_, err := PrepareOutlineCharacters([]OutlineEntry{{
		Chapter:        3,
		CharacterIDs:   []string{"unknown"},
		TemporaryRoles: []TemporaryCharacterNeed{{Role: "new antagonist", Important: true}},
	}}, []Character{{ID: "hero", Name: "林"}})
	var gapErr *OutlineCharacterGapError
	if !errors.As(err, &gapErr) || len(gapErr.Gaps) != 2 {
		t.Fatalf("gap error = %#v", err)
	}
	for _, gap := range gapErr.Gaps {
		if gap.Route != "character" {
			t.Fatalf("gap route = %+v", gap)
		}
	}
}
