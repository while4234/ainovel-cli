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
