package host

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestLegacyRecoveryRequiresPreviewAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	st := storepkg.NewStore(root)
	legacy := domain.StoryFoundation{
		Premise:       "A courier must save a sealed city.",
		Characters:    []domain.Character{{ID: "hero", Name: "Lin", Role: "主角", Description: "courier", Arc: "accepts leadership", Traits: []string{"brave"}, Tier: "core", Goal: "save the city", Motivation: "protect family", Conflict: "fears command", Voice: "direct", Constraints: []string{"never abandons civilians"}}},
		WorldRules:    []domain.WorldRule{{ID: "rule", Title: "seal", Rule: "The city seal opens once.", Strength: "hard"}},
		Relationships: []domain.CharacterRelationship{}, RelationshipsReviewed: true,
	}
	if _, err := st.Foundation.SaveCAS(legacy, 0); err != nil {
		t.Fatal(err)
	}
	chapter := filepath.Join(root, "chapters", "chapter-001.md")
	if err := os.MkdirAll(filepath.Dir(chapter), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chapter, []byte("existing prose must stay unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewLegacyRecovery(st)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Completion.Complete || len(preview.Conflicts) != 0 {
		t.Fatalf("preview blocked: %+v", preview)
	}
	if _, err := ApplyLegacyRecovery(st, "stale", preview.FoundationRevision, preview.FoundationAuditSignature); err == nil {
		t.Fatal("stale preview was accepted")
	}
	applied, err := ApplyLegacyRecovery(st, preview.ID, preview.FoundationRevision, preview.FoundationAuditSignature)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyLegacyRecovery(st, preview.ID, preview.FoundationRevision, preview.FoundationAuditSignature); err != nil {
		t.Fatalf("idempotent retry failed: %v", err)
	}
	if applied.Candidate.ConfirmedSignature == "" {
		t.Fatal("core cast was not explicitly confirmed")
	}
	if err := RequireManagedCoreCastGate(st, false); err != nil {
		t.Fatalf("recovered gate invalid: %v", err)
	}
	if body, err := os.ReadFile(chapter); err != nil || string(body) != "existing prose must stay unchanged" {
		t.Fatalf("existing prose changed: %q %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(root, "meta", "recovery", "snapshots", preview.ID, "snapshot.json")); err != nil {
		t.Fatalf("recovery snapshot missing: %v", err)
	}
}

func TestLegacyProtagonistRecognizesChineseLeadRolesWithoutMatchingRelatives(t *testing.T) {
	tests := []struct {
		role string
		want bool
	}{
		{role: "女主", want: true},
		{role: "男主", want: true},
		{role: "女主/成长线", want: true},
		{role: "核心主角", want: true},
		{role: "female lead", want: true},
		{role: "男主父亲/背景压力", want: false},
		{role: "女主助理", want: false},
		{role: "配角声部", want: false},
	}
	for _, test := range tests {
		t.Run(test.role, func(t *testing.T) {
			if got := legacyProtagonist(domain.Character{Role: test.role}); got != test.want {
				t.Fatalf("legacyProtagonist(%q) = %v, want %v", test.role, got, test.want)
			}
		})
	}
}

func TestLegacyRecoveryCompletesSparseChineseLeadsFromCompatibilityEvidence(t *testing.T) {
	root := t.TempDir()
	st := storepkg.NewStore(root)
	foundation := domain.StoryFoundation{
		Premise: "林舒然与墨子曜共同改写旧结局。",
		Characters: []domain.Character{
			{ID: "heroine", Name: "林舒然", Role: "女主", Arc: "从讨好走向独立"},
			{ID: "hero", Name: "墨子曜", Role: "男主"},
			{ID: "father", Name: "墨阳", Role: "男主父亲/背景压力"},
		},
		WorldRules: []domain.WorldRule{{ID: "rule", Rule: "保持既有故事连续性", Strength: domain.WorldRuleStrengthHard}},
	}
	if _, err := st.Foundation.SaveCAS(foundation, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.World.SaveRelationships([]domain.RelationshipEntry{{
		CharacterA: "林舒然", CharacterB: "墨子曜", Relation: "彼此信任并共同面对主线", Chapter: 48,
	}}); err != nil {
		t.Fatal(err)
	}
	chapter := filepath.Join(root, "chapters", "chapter-001.md")
	if err := os.MkdirAll(filepath.Dir(chapter), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chapter, []byte("existing prose"), 0o644); err != nil {
		t.Fatal(err)
	}

	preview, err := PreviewLegacyRecovery(st)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Completion.Complete || len(preview.Conflicts) > 0 {
		t.Fatalf("sparse legacy leads were not recoverable: %+v", preview)
	}
	if len(preview.Candidate.Members) != 2 {
		t.Fatalf("members = %d, want only the two leads", len(preview.Candidate.Members))
	}
	if len(preview.Candidate.PlannedRelationships) != 1 {
		t.Fatalf("relationships = %d, want recovered runtime relationship", len(preview.Candidate.PlannedRelationships))
	}
	if len(preview.Recovered) < 10 {
		t.Fatalf("recovery provenance is incomplete: %+v", preview.Recovered)
	}
}
