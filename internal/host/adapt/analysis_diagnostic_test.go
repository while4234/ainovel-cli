package adapt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestDiagnoseSourceAnalysisRejectsHollowLegacyDoneArtifacts(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("chapter source"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := store.NewStore(root)
	source, err := st.Adaptation.SaveSourceChapter(1, "one", "chapter source")
	if err != nil {
		t.Fatal(err)
	}
	manifest := domain.AdaptationSourceManifest{SourcePath: sourcePath, ChapterCount: 1, Chapters: []domain.AdaptationSource{source}}
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SaveSourceReports([]domain.AdaptationSourceReport{{Chapter: 1, Title: "one", SourceSHA256: source.SHA256, Summary: "summary"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SaveSourceFoundation(domain.AdaptationSourceFoundation{Premise: "only premise"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SaveCoCreateDossier(domain.AdaptationCoCreateDossier{RelationshipMap: nil}); err != nil {
		t.Fatal(err)
	}
	diagnostic, err := DiagnoseSourceAnalysis(st, Prompts{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if diagnostic.Complete || diagnostic.Status == "complete" {
		t.Fatalf("hollow artifacts declared complete: %+v", diagnostic)
	}
	for _, required := range []string{"characters", "world_rules", "relationships"} {
		if !containsAny(diagnostic.MissingProducts, required) {
			t.Fatalf("missing product %q not diagnosed: %+v", required, diagnostic)
		}
	}
	if diagnostic.Estimate.EstimatedCalls == 0 {
		t.Fatal("repair estimate was not calculated")
	}
}
