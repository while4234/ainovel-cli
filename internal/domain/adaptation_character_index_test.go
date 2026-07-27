package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestAdaptationSourceCharacterIndexAndCoverageIncludeNonCoreCast(t *testing.T) {
	source := &AdaptationSourceFoundation{
		SourceSignature: strings.Repeat("a", 64),
		Characters: []Character{
			{ID: "src-hero", Name: "林舟", Role: "主角", Traits: []string{"克制"}},
			{ID: "src-villain", Name: "顾沉", Role: "反派", Traits: []string{"强硬"}},
			{ID: "src-friend", Name: "周宁", Aliases: []string{"阿宁"}, Role: "朋友", Goal: "保护林舟", Traits: []string{"热心"}},
			{ID: "src-mentor", Name: "沈老师", Role: "导师", Motivation: "弥补旧错", Traits: []string{"审慎"}},
			{ID: "src-passerby", Name: "路人", Role: "路人", Traits: []string{}},
		},
	}
	reports := []AdaptationSourceReport{
		{
			Chapter: 1, SourceSHA256: strings.Repeat("1", 64),
			Characters: []string{"林舟", "顾沉", "阿宁", "沈老师", "路人"},
			KeyEvents:  []string{"林舟拒绝顾沉的交易"},
			Relationships: []RelationshipEntry{{
				CharacterA: "林舟", CharacterB: "沈老师", Relation: "师生关系受考验",
			}},
		},
		{
			Chapter: 2, SourceSHA256: strings.Repeat("2", 64),
			Characters: []string{"林舟", "阿宁"},
			KeyEvents:  []string{"周宁独自追回关键证据并交给林舟"},
		},
		{
			Chapter: 3, SourceSHA256: strings.Repeat("3", 64),
			Characters: []string{"林舟", "顾沉", "周宁", "沈老师"},
			KeyEvents:  []string{"沈老师承担代价阻止顾沉"},
			StateChanges: []StateChange{{
				Entity: "沈老师", Field: "立场", OldValue: "旁观", NewValue: "公开帮助林舟", Reason: "弥补旧错",
			}},
		},
	}
	dossier := &AdaptationCoCreateDossier{
		Batches: []AdaptationCoCreateDossierBatch{{
			MajorCharacters: []string{"林舟", "顾沉"},
		}},
	}

	index, err := BuildAdaptationSourceCharacterIndex(source, reports, dossier, nil)
	if err != nil {
		t.Fatal(err)
	}
	if index.Version != AdaptationSourceCharacterIndexVersion || len(index.InputSignature) != 64 {
		t.Fatalf("index identity = %+v", index)
	}
	if len(index.Characters) != 5 {
		t.Fatalf("characters = %+v", index.Characters)
	}
	friend := indexedSourceCharacter(t, index, "src-friend")
	if !reflect.DeepEqual(friend.Aliases, []string{"阿宁"}) || friend.AppearanceCount != 3 {
		t.Fatalf("friend index = %+v", friend)
	}
	mentor := indexedSourceCharacter(t, index, "src-mentor")
	if len(mentor.Relationships) == 0 || len(mentor.StateChanges) == 0 {
		t.Fatalf("mentor evidence = %+v", mentor)
	}
	passerby := indexedSourceCharacter(t, index, "src-passerby")
	if passerby.Named {
		t.Fatalf("generic passerby treated as named: %+v", passerby)
	}

	mappings := []CharacterSourceMapping{
		sourceMappingForTest("map-hero", CharacterSourceKeep, []string{"src-hero"}, []string{"target-hero"}),
		sourceMappingForTest("map-villain", CharacterSourceKeep, []string{"src-villain"}, []string{"target-villain"}),
		sourceMappingForTest("map-friend", CharacterSourceKeep, []string{"src-friend"}, []string{"target-friend"}),
		sourceMappingForTest("map-mentor", CharacterSourceKeep, []string{"src-mentor"}, []string{"target-mentor"}),
		sourceMappingForTest("map-passerby", CharacterSourceExclude, []string{"src-passerby"}, nil),
	}
	coverage, err := EvaluateAdaptationCharacterCoverage(index, mappings)
	if err != nil {
		t.Fatal(err)
	}
	if coverage.SourceTotal != 5 || coverage.DecisionRequired != 4 ||
		coverage.Mapped != 5 || coverage.ExplicitlyExcluded != 1 ||
		coverage.Pending != 0 || coverage.BlockingGaps != 0 {
		t.Fatalf("coverage = %+v", coverage)
	}
	for _, decision := range coverage.Decisions {
		if decision.SourceCharacterID == "src-passerby" &&
			(decision.SuggestedTier != CharacterTierDecorative || decision.DecisionRequired) {
			t.Fatalf("passerby coverage = %+v", decision)
		}
	}

	changed := append([]AdaptationSourceReport(nil), reports...)
	changed[1].Summary = "changed report evidence"
	changedIndex, err := BuildAdaptationSourceCharacterIndex(source, changed, dossier, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changedIndex.InputSignature == index.InputSignature {
		t.Fatal("source report change did not invalidate index signature")
	}
}

func TestAdaptationSourceCharacterIndexKeepsLegacyEvidenceAsUncertain(t *testing.T) {
	index, err := BuildAdaptationSourceCharacterIndex(&AdaptationSourceFoundation{
		Characters: []Character{{Name: "旧角色", Role: "配角", Traits: []string{}}},
	}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Characters) != 1 || len(index.Characters[0].Uncertainties) == 0 {
		t.Fatalf("legacy evidence = %+v", index)
	}
}

func TestAdaptationSourceCharacterIndexMergesIDLessProfilesButKeepsExplicitHomonymsDistinct(t *testing.T) {
	index, err := BuildAdaptationSourceCharacterIndex(
		&AdaptationSourceFoundation{Characters: []Character{
			{ID: "source-lin", Name: "Lin", Role: "investigator", Traits: []string{"careful"}},
		}},
		[]AdaptationSourceReport{{
			Chapter: 1, SourceSHA256: strings.Repeat("1", 64),
			CharacterProfiles: []Character{{
				Name: "Lin", Goal: "Recover the archive.", Traits: []string{"persistent"},
			}},
			Characters: []string{"Lin"},
			KeyEvents:  []string{"Lin recovers the archive."},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Characters) != 1 {
		t.Fatalf("ID-less chapter profile duplicated SourceFoundation identity: %+v", index.Characters)
	}
	lin := indexedSourceCharacter(t, index, "source-lin")
	if lin.Profile.Goal != "Recover the archive." || lin.AppearanceCount != 1 ||
		!reflect.DeepEqual(lin.Profile.Traits, []string{"careful", "persistent"}) {
		t.Fatalf("merged profile = %+v", lin)
	}

	homonyms, err := BuildAdaptationSourceCharacterIndex(
		&AdaptationSourceFoundation{Characters: []Character{
			{ID: "source-alex-one", Name: "Alex", Role: "pilot"},
			{ID: "source-alex-two", Name: "Alex", Role: "doctor"},
		}},
		[]AdaptationSourceReport{{
			Chapter: 2, Characters: []string{"Alex"}, KeyEvents: []string{"Alex arrives."},
			CharacterProfiles: []Character{{Name: "Alex", Goal: "Ambiguous evidence must not invent a third identity."}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(homonyms.Characters) != 2 {
		t.Fatalf("explicit homonyms collapsed: %+v", homonyms.Characters)
	}
	for _, entry := range homonyms.Characters {
		if len(entry.Conflicts) == 0 {
			t.Fatalf("ambiguous homonym was not surfaced: %+v", entry)
		}
	}
}

func TestValidateCharacterSourceCoverageSupportsMergeSplitExcludeAndTargetOriginal(t *testing.T) {
	mappings := []CharacterSourceMapping{
		sourceMappingForTest("merge", CharacterSourceMerge, []string{"source-a", "source-b"}, []string{"target-ab"}),
		sourceMappingForTest("split", CharacterSourceSplit, []string{"source-c"}, []string{"target-c1", "target-c2"}),
		sourceMappingForTest("exclude", CharacterSourceExclude, []string{"source-d"}, nil),
		{
			ID: "original", Action: CharacterSourceTargetOriginal, TargetCharacterIDs: []string{"target-new"},
			Rationale: "target-only archive function",
			Evidence: []CharacterSourceEvidence{{
				Kind: CharacterSourceOriginalAddition, Reference: "fixture.intent", Summary: "target-only decision",
			}},
		},
	}
	if err := ValidateCharacterSourceCoverage(
		mappings,
		[]string{"source-a", "source-b", "source-c", "source-d"},
		[]string{"target-ab", "target-c1", "target-c2", "target-new"},
	); err != nil {
		t.Fatalf("valid merge/split/exclude/target-original coverage: %v", err)
	}
}

func TestValidateCharacterSourceCoverageRejectsUnknownEndpoints(t *testing.T) {
	mapping := sourceMappingForTest("map", CharacterSourceKeep, []string{"missing-source"}, []string{"target"})
	if err := ValidateCharacterSourceCoverage([]CharacterSourceMapping{mapping}, []string{"source"}, []string{"target"}); err == nil ||
		!strings.Contains(err.Error(), "unknown source") {
		t.Fatalf("unknown source err = %v", err)
	}
	mapping = sourceMappingForTest("map", CharacterSourceKeep, []string{"source"}, []string{"missing-target"})
	if err := ValidateCharacterSourceCoverage([]CharacterSourceMapping{mapping}, []string{"source"}, []string{"target"}); err == nil ||
		!strings.Contains(err.Error(), "unknown target") {
		t.Fatalf("unknown target err = %v", err)
	}
}

func indexedSourceCharacter(
	t *testing.T,
	index AdaptationSourceCharacterIndex,
	id string,
) AdaptationSourceCharacterIndexEntry {
	t.Helper()
	for _, character := range index.Characters {
		if character.ID == id {
			return character
		}
	}
	t.Fatalf("source character %q missing", id)
	return AdaptationSourceCharacterIndexEntry{}
}

func sourceMappingForTest(
	id string,
	action CharacterSourceMappingAction,
	sourceIDs, targetIDs []string,
) CharacterSourceMapping {
	return CharacterSourceMapping{
		ID: id, Action: action, SourceCharacterIDs: sourceIDs, TargetCharacterIDs: targetIDs,
		Rationale: "fixture decision",
		Evidence: []CharacterSourceEvidence{{
			Kind: CharacterSourceAdaptationDecision, Reference: "fixture.intent", Summary: "fixture decision",
		}},
	}
}
