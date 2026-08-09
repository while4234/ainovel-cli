package artwork

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

var (
	artworkSourceVolumeID   = domain.LegacyStructureID("artwork-source-fixture", domain.StructureKindVolume, "volume-1")
	artworkSourceArcID      = domain.LegacyStructureID("artwork-source-fixture", domain.StructureKindArc, "volume-1/arc-1")
	artworkSourceChapterIDs = []string{
		domain.LegacyStructureID("artwork-source-fixture", domain.StructureKindChapter, "volume-1/arc-1/chapter-1"),
		domain.LegacyStructureID("artwork-source-fixture", domain.StructureKindChapter, "volume-1/arc-1/chapter-2"),
		domain.LegacyStructureID("artwork-source-fixture", domain.StructureKindChapter, "volume-1/arc-1/chapter-3"),
	}
)

func TestArtworkSourceContextsAreDeterministicBoundedAndReadOnly(t *testing.T) {
	root, st := newArtworkSourceFixture(t)
	if err := st.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 1, Summary: "The hero opens the sealed gate.", CharacterIDs: []string{"hero"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveVolumeSummary(domain.VolumeSummary{Volume: 1, Title: "The Gate", Summary: "A city prepares for the eclipse."}); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Title: "Opening", Summary: "The old alliance reforms."}); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(2, strings.Repeat("bounded final prose ", 900)); err != nil {
		t.Fatal(err)
	}

	before := snapshotFiles(t, root)
	tests := []struct {
		name     string
		workType WorkType
		scope    string
		scopeID  string
		wantID   string
	}{
		{name: "book", workType: WorkTypeCover, scope: "project", wantID: "formal-outline:layered"},
		{name: "volume", workType: WorkTypeIllustration, scope: "volume", scopeID: artworkSourceVolumeID, wantID: "volume-outline:" + artworkSourceVolumeID},
		{name: "chapter", workType: WorkTypeIllustration, scope: "chapter", scopeID: artworkSourceChapterIDs[0], wantID: "chapter-summary:000001"},
		{name: "character", workType: WorkTypeCharacterPortrait, scope: "character", scopeID: "hero", wantID: "character:hero"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first, err := BuildSourceSnapshot(st, test.workType, test.scope, test.scopeID, ArtworkPromptTemplateVersion)
			if err != nil {
				t.Fatal(err)
			}
			second, err := BuildSourceSnapshot(st, test.workType, test.scope, test.scopeID, ArtworkPromptTemplateVersion)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(first, second) {
				t.Fatalf("unchanged source was not deterministic:\nfirst=%+v\nsecond=%+v", first, second)
			}
			if len(first.Fragments) > maxSourceFragments || sourceContentBytes(first) > maxSourceContentBytes {
				t.Fatalf("source bounds exceeded: fragments=%d bytes=%d", len(first.Fragments), sourceContentBytes(first))
			}
			if !hasSourceFragment(first, test.wantID, "") {
				t.Fatalf("source missing %q: %+v", test.wantID, first.Fragments)
			}
		})
	}
	if after := snapshotFiles(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("source reads wrote project artifacts:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestArtworkChapterEvidenceFallbackOrder(t *testing.T) {
	_, st := newArtworkSourceFixture(t)
	if err := st.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 1, Summary: "summary evidence wins"}); err != nil {
		t.Fatal(err)
	}
	prose := strings.Repeat("final prose evidence ", 900)
	if err := st.Drafts.SaveFinalChapter(2, prose); err != nil {
		t.Fatal(err)
	}

	summary, err := BuildSourceSnapshot(st, WorkTypeIllustration, "chapter", artworkSourceChapterIDs[0], ArtworkPromptTemplateVersion)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSourceFragment(summary, "chapter-summary:000001", "summary") || hasSourceFragment(summary, "chapter-prose:000001", "") {
		t.Fatalf("summary must win over later fallbacks: %+v", summary.Fragments)
	}

	finalProse, err := BuildSourceSnapshot(st, WorkTypeIllustration, "chapter", artworkSourceChapterIDs[1], ArtworkPromptTemplateVersion)
	if err != nil {
		t.Fatal(err)
	}
	fragment, ok := findSourceFragment(finalProse, "chapter-prose:000002")
	if !ok || fragment.Kind != "prose" || !fragment.Truncated || len([]byte(fragment.Content)) > maxSourceFragmentBytes {
		t.Fatalf("final prose fallback was not bounded: %+v", fragment)
	}
	if hasSourceFragment(finalProse, "chapter-outline:000002", "") {
		t.Fatalf("outline must not replace available final prose: %+v", finalProse.Fragments)
	}

	outline, err := BuildSourceSnapshot(st, WorkTypeIllustration, "chapter", artworkSourceChapterIDs[2], ArtworkPromptTemplateVersion)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSourceFragment(outline, "chapter-outline:000003", "outline") {
		t.Fatalf("missing outline fallback: %+v", outline.Fragments)
	}
}

func TestArtworkCharacterContextNeverReadsManuscriptProse(t *testing.T) {
	_, st := newArtworkSourceFixture(t)
	const manuscriptSecret = "WHOLE_MANUSCRIPT_PROSE_MUST_NOT_APPEAR"
	if err := st.Drafts.SaveFinalChapter(1, manuscriptSecret); err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildSourceSnapshot(st, WorkTypeCharacterPortrait, "character", "hero", ArtworkPromptTemplateVersion)
	if err != nil {
		t.Fatal(err)
	}
	joined := sourceText(snapshot)
	for _, want := range []string{"Hero of Glass", "Trusted Cartographer", "Magic mirrors require moonlight", "allies at the sealed gate"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("character context missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, manuscriptSecret) || strings.Contains(joined, "Unrelated Monarch") {
		t.Fatalf("character context included unrelated manuscript/cast evidence: %s", joined)
	}
	for _, fragment := range snapshot.Fragments {
		if fragment.Kind == "prose" || fragment.Kind == "summary" || fragment.Kind == "outline" {
			t.Fatalf("character context included %s evidence: %+v", fragment.Kind, fragment)
		}
	}
}

func TestArtworkSourceDigestCoversScopeWorkTemplateAndExactBoundedText(t *testing.T) {
	baseFragments := []SourceFragment{{Kind: "summary", ID: "chapter-summary:1", Label: "Summary", Content: strings.Repeat("x", maxSourceFragmentBytes+200)}}
	base, err := NewSourceSnapshot(WorkTypeIllustration, "chapter", "one", "template/v1", baseFragments)
	if err != nil {
		t.Fatal(err)
	}
	repeated, _ := NewSourceSnapshot(WorkTypeIllustration, "chapter", "one", "template/v1", baseFragments)
	if !reflect.DeepEqual(base, repeated) || !base.Fragments[0].Truncated || len([]byte(base.Fragments[0].Content)) > maxSourceFragmentBytes {
		t.Fatalf("bounded digest input is not deterministic: %+v / %+v", base, repeated)
	}
	variants := []SourceSnapshot{}
	variants = append(variants, mustSourceSnapshot(t, WorkTypeCover, "chapter", "one", "template/v1", baseFragments))
	variants = append(variants, mustSourceSnapshot(t, WorkTypeIllustration, "volume", "one", "template/v1", baseFragments))
	variants = append(variants, mustSourceSnapshot(t, WorkTypeIllustration, "chapter", "two", "template/v1", baseFragments))
	variants = append(variants, mustSourceSnapshot(t, WorkTypeIllustration, "chapter", "one", "template/v2", baseFragments))
	variants = append(variants, mustSourceSnapshot(t, WorkTypeIllustration, "chapter", "one", "template/v1", []SourceFragment{{Kind: "summary", ID: "chapter-summary:1", Content: "changed"}}))
	for index, variant := range variants {
		if variant.Digest == base.Digest {
			t.Fatalf("digest variant %d did not change", index)
		}
	}
}

func newArtworkSourceFixture(t *testing.T) (string, *storepkg.Store) {
	t.Helper()
	root := t.TempDir()
	st := storepkg.NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	_, err := st.Foundation.SaveCAS(domain.StoryFoundation{
		Premise: "A glass city faces a moonless eclipse.",
		Characters: []domain.Character{
			{ID: "hero", Name: "Hero of Glass", Role: "lead", Description: "A patient keeper with a silver coat.", Arc: "accepts leadership", Traits: []string{"observant"}},
			{ID: "ally", Name: "Trusted Cartographer", Role: "ally", Description: "Carries ink-stained maps.", Arc: "earns trust", Traits: []string{"precise"}},
			{ID: "unrelated", Name: "Unrelated Monarch", Role: "background", Description: "Should not enter the portrait.", Arc: "none", Traits: []string{"distant"}},
		},
		Relationships: []domain.CharacterRelationship{{
			ID: "hero-ally", SourceCharacterID: "hero", TargetCharacterID: "ally",
			Type: domain.RelationshipTypeAlly, Direction: domain.RelationshipDirectionBidirectional,
			Status: domain.RelationshipStatusActive, Description: "allies at the sealed gate",
		}},
		RelationshipsReviewed: true,
		WorldRules:            []domain.WorldRule{{ID: "mirror-rule", Category: "magic", Rule: "Magic mirrors require moonlight", Boundary: "No stored light", Strength: domain.WorldRuleStrengthHard}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		ID: artworkSourceVolumeID, Index: 1, Title: "The Gate", Theme: "trust under pressure",
		Arcs: []domain.ArcOutline{{
			ID: artworkSourceArcID, Index: 1, Title: "Opening", Goal: "open the gate",
			Chapters: []domain.OutlineEntry{
				{ID: artworkSourceChapterIDs[0], Chapter: 1, Title: "A Silver Key", CoreEvent: "the hero finds a key", Hook: "the gate answers"},
				{ID: artworkSourceChapterIDs[1], Chapter: 2, Title: "The Eclipse", CoreEvent: "the light goes out", Hook: "a map glows"},
				{ID: artworkSourceChapterIDs[2], Chapter: 3, Title: "The Choice", CoreEvent: "the allies choose a road", Hook: "the mirror cracks"},
			},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	return root, st
}

func snapshotFiles(t *testing.T, root string) map[string][sha256.Size]byte {
	t.Helper()
	files := make(map[string][sha256.Size]byte)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = sha256.Sum256(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func hasSourceFragment(snapshot SourceSnapshot, id, kind string) bool {
	fragment, ok := findSourceFragment(snapshot, id)
	return ok && (kind == "" || fragment.Kind == kind)
}

func findSourceFragment(snapshot SourceSnapshot, id string) (SourceFragment, bool) {
	for _, fragment := range snapshot.Fragments {
		if fragment.ID == id {
			return fragment, true
		}
	}
	return SourceFragment{}, false
}

func sourceContentBytes(snapshot SourceSnapshot) int {
	total := 0
	for _, fragment := range snapshot.Fragments {
		total += len([]byte(fragment.Content))
	}
	return total
}

func sourceText(snapshot SourceSnapshot) string {
	var builder strings.Builder
	for _, fragment := range snapshot.Fragments {
		builder.WriteString(fragment.Content)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func mustSourceSnapshot(t *testing.T, workType WorkType, scope, scopeID, template string, fragments []SourceFragment) SourceSnapshot {
	t.Helper()
	snapshot, err := NewSourceSnapshot(workType, scope, scopeID, template, fragments)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
