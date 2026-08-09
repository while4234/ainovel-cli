package web

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	artworkpkg "github.com/voocel/ainovel-cli/internal/artwork"
)

func TestCloneProjectForReplanningKeepsSourceAndResetsActiveWork(t *testing.T) {
	projects := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := projects.CreateProject("legacy project")
	if err != nil {
		t.Fatal(err)
	}
	sourceManifest := filepath.Join(source.OutputDir, "meta", "adaptation", "source_manifest.json")
	cloneTestWriteJSON(t, sourceManifest, map[string]any{"source_path": filepath.Join(source.RootDir, "uploads", "adaptation", "source.txt"), "chapter_count": 1})
	cloneTestWriteFile(t, filepath.Join(source.OutputDir, "meta", "adaptation", "source_reports.json"), []byte(`[{"chapter":1,"summary":"source"}]`))
	cloneTestWriteFile(t, filepath.Join(source.OutputDir, "chapters", "chapter-001.md"), []byte("legacy body"))
	cloneTestWriteFile(t, filepath.Join(source.OutputDir, "outline.json"), []byte(`[{"chapter":1,"title":"old"}]`))
	cloneTestWriteFile(t, filepath.Join(source.OutputDir, "progress.json"), []byte(`{"current_chapter":2}`))
	cloneTestWriteFile(t, filepath.Join(source.OutputDir, "meta", "cocreate", "core_cast_gate.json"), []byte(`{"version":1}`))
	artworkStore, err := artworkpkg.NewWorkspaceStore(source.OutputDir)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := artworkStore.CreateDraft(artworkAPITestDraftInput(), "replan-artwork-draft")
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := artworkStore.SubmitGeneration(draft.ID, draft.Version, "replan-artwork-job", "https://fixture.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := artworkStore.BeginJob(job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := artworkStore.FinalizeJob(job.ID, fixedArtworkPNG(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := artworkStore.ApplyAsset(job.AssetID); err != nil {
		t.Fatal(err)
	}
	before := cloneTestReadFile(t, filepath.Join(source.OutputDir, "chapters", "chapter-001.md"))

	cloned, err := projects.CloneProjectForReplanning(source.ID, "fresh plan")
	if err != nil {
		t.Fatal(err)
	}
	if cloned.ReplannedFromID != source.ID || cloned.ReplannedAt == nil {
		t.Fatalf("replan lineage = %+v", cloned)
	}
	if after := cloneTestReadFile(t, filepath.Join(source.OutputDir, "chapters", "chapter-001.md")); !bytes.Equal(before, after) {
		t.Fatal("source body changed")
	}
	for _, relative := range []string{"chapters/chapter-001.md", "outline.json", "progress.json", "meta/cocreate/core_cast_gate.json"} {
		if _, err := os.Stat(filepath.Join(cloned.OutputDir, filepath.FromSlash(relative))); !os.IsNotExist(err) {
			t.Fatalf("active artifact %s was not reset: %v", relative, err)
		}
		if _, err := os.Stat(filepath.Join(cloned.RootDir, "reference", "legacy", "source-output", filepath.FromSlash(relative))); err != nil {
			t.Fatalf("reference artifact %s missing: %v", relative, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cloned.OutputDir, "artwork")); !os.IsNotExist(err) {
		t.Fatalf("replanned project inherited active artwork: %v", err)
	}
	archivedArtwork := filepath.Join(cloned.RootDir, "reference", "legacy", "source-output", "artwork")
	archivedStore, err := artworkpkg.NewWorkspaceStore(filepath.Dir(archivedArtwork))
	if err != nil {
		t.Fatalf("open archived artwork reference: %v", err)
	}
	if state, content, err := archivedStore.ReadAppliedDerivative(artworkpkg.WorkTypeCover, "project", ""); err != nil || state.AssetID != job.AssetID || len(content) == 0 {
		t.Fatalf("archived applied artwork state=%+v bytes=%d err=%v", state, len(content), err)
	}
	if _, err := os.Stat(filepath.Join(cloned.OutputDir, "meta", "adaptation", "source_manifest.json")); err != nil {
		t.Fatalf("source identity was not retained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cloned.OutputDir, "meta", "adaptation", "source_reports.json")); err != nil {
		t.Fatalf("source analysis was not retained: %v", err)
	}
}
