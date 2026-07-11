package store

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestRollbackConfirmedAdaptationRestoresProposalAndDeletesWritingArtifacts(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	source, err := st.Adaptation.SaveSourceChapter(1, "source", "source text")
	if err != nil {
		t.Fatalf("SaveSourceChapter: %v", err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 1,
		Chapters:     []domain.AdaptationSource{source},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	plan := rollbackTestAdaptationPlan()
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if err := st.Outline.SavePremise("# Adapted\n\nconfirmed foundation"); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "Old outline"}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "final chapter"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseComplete,
		CurrentChapter:    2,
		TotalChapters:     1,
		CompletedChapters: []int{1},
		TotalWordCount:    12,
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	preview, err := st.RollbackPreview()
	if err != nil {
		t.Fatalf("RollbackPreview: %v", err)
	}
	if !preview.CanRollback || preview.TargetStage != domain.RollbackStageProposal {
		t.Fatalf("preview = %+v, want proposal target", preview)
	}
	result, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: preview.PreviewHash})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if !slices.Contains(result.DeletedPaths, "chapters") {
		t.Fatalf("deleted paths = %v, want chapters removed", result.DeletedPaths)
	}
	if plan, err := st.Adaptation.LoadPlan(); err != nil || plan != nil {
		t.Fatalf("confirmed plan should be removed, plan=%+v err=%v", plan, err)
	}
	proposal, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if proposal == nil || proposal.Status != domain.AdaptationPlanStatusProposal || len(proposal.Chapters) != 1 {
		t.Fatalf("proposal not restored: %+v", proposal)
	}
	if text, err := st.Drafts.LoadChapterText(1); err != nil || text != "" {
		t.Fatalf("chapter text should be deleted, text=%q err=%v", text, err)
	}
	if premise, err := st.Outline.LoadPremise(); err != nil || premise != "" {
		t.Fatalf("adaptation foundation should be deleted, premise=%q err=%v", premise, err)
	}
	if manifest, err := st.Adaptation.LoadSourceManifest(); err != nil || manifest == nil {
		t.Fatalf("source manifest should be preserved, manifest=%+v err=%v", manifest, err)
	}
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	if progress == nil || progress.Phase != domain.PhaseOutline || progress.CompletedChapters != nil {
		t.Fatalf("progress after rollback = %+v", progress)
	}
	next, err := st.RollbackPreview()
	if err != nil {
		t.Fatalf("second RollbackPreview: %v", err)
	}
	if next.TargetStage != domain.RollbackStageDraft {
		t.Fatalf("unvolumed proposal target = %q, want draft", next.TargetStage)
	}
}

func TestRollbackAdaptationWithVolumesStopsAtVolumeReviewBeforeDraft(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	source, err := st.Adaptation.SaveSourceChapter(1, "source", "source text")
	if err != nil {
		t.Fatalf("SaveSourceChapter: %v", err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath: "source.txt", ChapterCount: 1, Chapters: []domain.AdaptationSource{source},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	plan := rollbackTestAdaptationPlan()
	plan.Volumes = []domain.AdaptationVolumePlan{{
		Index: 1, Title: "Volume 1", TargetFrom: 1, TargetTo: 1, SourceFrom: 1, SourceTo: 1,
	}}
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}

	first := mustRollbackPreview(t, st, domain.RollbackStageProposal)
	if _, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: first.PreviewHash}); err != nil {
		t.Fatalf("rollback to proposal: %v", err)
	}
	second := mustRollbackPreview(t, st, domain.RollbackStageVolumeOutline)
	if _, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: second.PreviewHash}); err != nil {
		t.Fatalf("rollback to volume outline: %v", err)
	}
	review, err := st.Adaptation.LoadVolumeReview()
	if err != nil || review == nil {
		t.Fatalf("LoadVolumeReview: review=%+v err=%v", review, err)
	}
	if len(review.Volumes) != 1 || review.TargetChapterCount != 1 || review.SourcePath != "source.txt" {
		t.Fatalf("restored volume review = %+v", review)
	}
	third := mustRollbackPreview(t, st, domain.RollbackStageDraft)
	if _, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: third.PreviewHash}); err != nil {
		t.Fatalf("rollback to co-create draft: %v", err)
	}
	if review, err := st.Adaptation.LoadVolumeReview(); err != nil || review != nil {
		t.Fatalf("volume review after draft rollback = %+v err=%v", review, err)
	}
	if manifest, err := st.Adaptation.LoadSourceManifest(); err != nil || manifest == nil {
		t.Fatalf("source manifest must survive draft rollback: manifest=%+v err=%v", manifest, err)
	}
	fourth := mustRollbackPreview(t, st, domain.RollbackStageBlank)
	if _, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: fourth.PreviewHash}); err != nil {
		t.Fatalf("rollback to blank: %v", err)
	}
	final, err := st.RollbackPreview()
	if err != nil {
		t.Fatalf("final RollbackPreview: %v", err)
	}
	if final.CanRollback {
		t.Fatalf("blank project should not roll back: %+v", final)
	}
}

func mustRollbackPreview(t *testing.T, st *Store, target domain.RollbackStage) domain.RollbackPreview {
	t.Helper()
	preview, err := st.RollbackPreview()
	if err != nil {
		t.Fatalf("RollbackPreview: %v", err)
	}
	if !preview.CanRollback || preview.TargetStage != target {
		t.Fatalf("preview = %+v, want target %q", preview, target)
	}
	return preview
}

func TestRollbackNormalChapterOutlineCollapsesLayeredOutlineToVolumeSkeleton(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	layered := []domain.VolumeOutline{{
		Index: 1,
		Title: "Volume 1",
		Theme: "test",
		Arcs: []domain.ArcOutline{{
			Index: 1,
			Title: "Arc 1",
			Goal:  "open",
			Chapters: []domain.OutlineEntry{
				{Chapter: 1, Title: "A"},
				{Chapter: 2, Title: "B"},
			},
		}},
	}}
	if err := st.Outline.SavePremise("# Story"); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	if err := st.Outline.SaveLayeredOutline(layered); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := st.Outline.SaveOutline(domain.FlattenOutline(layered)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.RunMeta.SetPlanningReview(&domain.PlanningReview{
		Status: domain.PlanningReviewStatusPending,
		Kind:   domain.PlanningReviewKindChapterOutline,
		Brief:  "draft",
	}); err != nil {
		t.Fatalf("SetPlanningReview: %v", err)
	}

	preview, err := st.RollbackPreview()
	if err != nil {
		t.Fatalf("RollbackPreview: %v", err)
	}
	if !preview.CanRollback || preview.TargetStage != domain.RollbackStageVolumeOutline {
		t.Fatalf("preview = %+v, want volume outline target", preview)
	}
	if _, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: preview.PreviewHash}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if flat, err := st.Outline.LoadOutline(); err != nil || len(flat) != 0 {
		t.Fatalf("flat outline should be deleted, flat=%+v err=%v", flat, err)
	}
	collapsed, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("LoadLayeredOutline: %v", err)
	}
	if len(collapsed) != 1 || len(collapsed[0].Arcs) != 1 {
		t.Fatalf("collapsed outline shape = %+v", collapsed)
	}
	arc := collapsed[0].Arcs[0]
	if len(arc.Chapters) != 0 || arc.EstimatedChapters != 2 {
		t.Fatalf("arc should be skeleton with estimated count, got %+v", arc)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		t.Fatalf("PlanningReview: %v", err)
	}
	if review == nil || review.Kind != domain.PlanningReviewKindVolumeSplit {
		t.Fatalf("review = %+v, want volume split", review)
	}
}

func TestRollbackSafeRemoveRejectsEscapingPaths(t *testing.T) {
	ioStore := newIO(t.TempDir())
	for _, rel := range []string{"", ".", "..", "../outside", filepath.Join("..", "outside")} {
		if _, err := ioStore.RemoveAllRel(rel); err == nil {
			t.Fatalf("RemoveAllRel(%q) succeeded, want error", rel)
		}
	}
}

func TestRollbackCompletedProjectFixtureDoesNotDeleteOutsideWhitelist(t *testing.T) {
	fixture := strings.TrimSpace(os.Getenv("AINOVEL_ROLLBACK_FIXTURE_OUTPUT"))
	if fixture == "" {
		t.Skip("set AINOVEL_ROLLBACK_FIXTURE_OUTPUT to a completed project output directory to run this safety test")
	}
	info, err := os.Stat(fixture)
	if err != nil || !info.IsDir() {
		t.Fatalf("fixture output directory is invalid: %s err=%v", fixture, err)
	}

	sandbox := filepath.Join(t.TempDir(), "output")
	if err := copyDir(fixture, sandbox); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	before, err := fileSet(sandbox)
	if err != nil {
		t.Fatalf("scan before: %v", err)
	}
	st := NewStore(sandbox)
	preview, err := st.RollbackPreview()
	if err != nil {
		t.Fatalf("RollbackPreview: %v", err)
	}
	if !preview.CanRollback {
		t.Fatalf("fixture cannot roll back: %+v", preview)
	}
	if _, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: preview.PreviewHash}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	after, err := fileSet(sandbox)
	if err != nil {
		t.Fatalf("scan after: %v", err)
	}
	for rel := range before {
		if after[rel] {
			continue
		}
		if !rollbackFixtureDeletionAllowed(rel) {
			t.Fatalf("fixture rollback deleted unexpected file: %s", rel)
		}
	}
}

func rollbackTestAdaptationPlan() domain.AdaptationPlan {
	return domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityChapter,
		RewritePolicy: domain.AdaptationRewritePreserveDetails,
		Brief:         "adapt the source",
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter: 1,
			Title:   "Target",
			OutlineEntry: domain.OutlineEntry{
				Chapter:   1,
				Title:     "Target",
				CoreEvent: "target event",
			},
		}},
	}
}

func rollbackFixtureDeletionAllowed(rel string) bool {
	rel = filepath.ToSlash(rel)
	allowedPrefixes := []string{
		"chapters/",
		"drafts/",
		"summaries/",
		"reviews/",
		"meta/runtime/",
		"meta/adaptation/checks/",
		"meta/snapshots/",
	}
	allowedExact := map[string]bool{
		"premise.md":                                  true,
		"outline.json":                                true,
		"outline.md":                                  true,
		"layered_outline.json":                        true,
		"layered_outline.md":                          true,
		"characters.json":                             true,
		"characters.md":                               true,
		"world_rules.json":                            true,
		"world_rules.md":                              true,
		"timeline.json":                               true,
		"timeline.md":                                 true,
		"foreshadow_ledger.json":                      true,
		"foreshadow_ledger.md":                        true,
		"relationship_state.json":                     true,
		"relationship_state.md":                       true,
		"meta/progress.json":                          true,
		"meta/checkpoints.jsonl":                      true,
		"meta/state_changes.json":                     true,
		"meta/cast_ledger.json":                       true,
		"meta/last_commit.json":                       true,
		"meta/pending_commit.json":                    true,
		"meta/last_review.json":                       true,
		"meta/compass.json":                           true,
		"meta/outline_duplicate_scan.json":            true,
		"meta/outline_repair_finalization.json":       true,
		"meta/adaptation/plan.json":                   true,
		"meta/adaptation/proposal.json":               true,
		"meta/adaptation/proposal_runtime.json":       true,
		"meta/adaptation/proposal_volume_review.json": true,
	}
	if allowedExact[rel] {
		return true
	}
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

func copyDir(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFile(path, dst, info.Mode())
	})
}

func copyFile(source, target string, mode os.FileMode) error {
	srcFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = srcFile.Close() }()

	dstFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(dstFile, srcFile)
	closeErr := dstFile.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func fileSet(root string) (map[string]bool, error) {
	files := make(map[string]bool)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = true
		return nil
	})
	return files, err
}
