package host

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestFoundationStateRecognizesLegacyAdaptationWithoutTargetPlan(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{ChapterCount: 1, Chapters: []domain.AdaptationSource{{Chapter: 1, SHA256: "sha", Path: "meta/adaptation/source_chapters/chapter-0001.md"}}}); err != nil {
		t.Fatal(err)
	}
	state, err := NewFoundationRevisionService(st).State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != "adaptation" {
		t.Fatalf("mode = %q", state.Mode)
	}
	if state.SourceAnalysis == nil || state.SourceAnalysis.Complete {
		t.Fatalf("source analysis diagnostic = %+v", state.SourceAnalysis)
	}
	if state.TargetAvailable {
		t.Fatal("empty legacy target was declared available")
	}
	if len(state.NextActions) == 0 || state.NextActions[0] != "complete_source_analysis" {
		t.Fatalf("next actions = %v", state.NextActions)
	}
}
