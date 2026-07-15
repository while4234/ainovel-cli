package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type oneStageNormalPublicationPolicy struct{}

func (oneStageNormalPublicationPolicy) Mode() domain.RevisionMode { return domain.RevisionModeNormal }

func (oneStageNormalPublicationPolicy) Identity() (string, string) {
	return "test.normal-publication", "1"
}

func (oneStageNormalPublicationPolicy) ApprovalStages(domain.RevisionImpact) ([]domain.RevisionApprovalStage, error) {
	return []domain.RevisionApprovalStage{{ID: "structure", Label: "Structure"}}, nil
}

func (oneStageNormalPublicationPolicy) ValidateImpact(domain.RevisionImpact) error { return nil }

func (oneStageNormalPublicationPolicy) ValidateCandidate(_ domain.RevisionSession, versions []domain.ArtifactVersion) error {
	if len(versions) != 1 || versions[0].ArtifactID != domain.NormalStructureSnapshotID ||
		versions[0].ArtifactKind != domain.NormalArtifactStructureSnapshot {
		return fmt.Errorf("one canonical normal structure snapshot is required")
	}
	var candidate []domain.VolumeOutline
	if err := json.Unmarshal(versions[0].Payload, &candidate); err != nil {
		return err
	}
	return domain.ValidateStructureSnapshot(candidate)
}

func (oneStageNormalPublicationPolicy) Route(domain.RevisionSession) (*domain.RevisionRoute, error) {
	return nil, nil
}

type normalPublicationFixture struct {
	dir       string
	store     *Store
	policy    oneStageNormalPublicationPolicy
	baseline  []domain.VolumeOutline
	candidate []domain.VolumeOutline
	progress  *domain.Progress
	session   *domain.RevisionSession
	input     RevisionMutationInput
	owner     *RevisionPublicationOwner
}

func newNormalPublicationFixture(t *testing.T, label string) normalPublicationFixture {
	t.Helper()
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumeID := domain.LegacyStructureID(label, domain.StructureKindVolume, "volume")
	arcID := domain.LegacyStructureID(label, domain.StructureKindArc, "arc")
	chapterID := domain.LegacyStructureID(label, domain.StructureKindChapter, "chapter-1")
	secondID := domain.LegacyStructureID(label, domain.StructureKindChapter, "chapter-2")
	baseline := []domain.VolumeOutline{{
		ID: volumeID, Index: 1, Title: "Volume", Theme: "theme",
		Arcs: []domain.ArcOutline{{
			ID: arcID, Index: 1, Title: "Arc", Goal: "goal",
			Chapters: []domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "One", CoreEvent: "begin", Hook: "cost", Scenes: []string{"begin"}}},
		}},
	}}
	candidate := domain.CloneStructureSnapshot(baseline)
	candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, domain.OutlineEntry{
		ID: secondID, Chapter: 2, Title: "Two", CoreEvent: "cost lands", Hook: "choice", Scenes: []string{"pay"},
	})
	progress := &domain.Progress{
		NovelName: "publication", Phase: domain.PhaseComplete, Flow: domain.FlowWriting,
		Layered: true, TotalChapters: 1, CompletedChapters: []int{1},
		CompletionAuditStatus: "pass", CompletionAuditReportDigest: "prepublish-audit",
	}
	if err := st.Outline.SaveLayeredOutline(baseline); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}
	policy := oneStageNormalPublicationPolicy{}
	impact, err := domain.NewRevisionImpact("publish exact structure", []domain.RevisionImpactItem{{
		ArtifactID: domain.NormalStructureSnapshotID, ArtifactKind: domain.NormalArtifactStructureSnapshot,
		Change: "replace structure", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency,
	}})
	if err != nil {
		t.Fatal(err)
	}
	session, err := st.Revisions.Start(policy, StartRevisionInput{Intent: "publish", Impact: impact, IdempotencyKey: label + "-start"})
	if err != nil {
		t.Fatal(err)
	}
	session, err = st.Revisions.ApproveImpact(policy, RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: label + "-impact"})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(candidate)
	session, err = st.Revisions.SubmitCandidate(policy, SubmitRevisionCandidateInput{
		SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: label + "-candidate",
		Artifacts: []CandidateArtifactInput{{ArtifactID: domain.NormalStructureSnapshotID, ArtifactKind: domain.NormalArtifactStructureSnapshot, Payload: payload}},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err = st.Revisions.RecordAudit(policy, RevisionAuditInput{
		RevisionMutationInput: RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: label + "-audit"},
		CandidateSignature:    session.CandidateSignature, Passed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err = st.Revisions.ApproveStage(policy, RevisionApprovalInput{
		RevisionMutationInput: RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: label + "-approve"},
		StageID:               "structure",
	})
	if err != nil {
		t.Fatal(err)
	}
	input := RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: label + "-publish"}
	_, owner, err := st.Revisions.ValidatePublishWithOwner(policy, input)
	if err != nil {
		t.Fatal(err)
	}
	return normalPublicationFixture{
		dir: dir, store: st, policy: policy, baseline: baseline, candidate: candidate,
		progress: progress, session: session, input: input, owner: owner,
	}
}

func TestChildFormalWritersRejectPreparedOwnerBypassByteIdentically(t *testing.T) {
	fixture := newNormalPublicationFixture(t, "child-writer-fence")
	// End the ready normal revision so the prepared command is the sole owner.
	if _, err := fixture.store.Revisions.Cancel(fixture.policy, RevisionMutationInput{
		SessionID: fixture.session.ID, ExpectedRevision: fixture.session.Revision, IdempotencyKey: "cancel-before-command",
	}); err != nil {
		t.Fatal(err)
	}
	before := snapshotFormalWriterFiles(t, fixture.dir)
	err := fixture.store.WithPreparedAdaptationRevisionCommand("child-writer", "direct-formal-writes", "fingerprint", func(*RevisionStore) error {
		progressWriters := []struct {
			name string
			call func() error
		}{
			{"save", func() error { return fixture.store.Progress.Save(&domain.Progress{NovelName: "changed"}) }},
			{"init", func() error { return fixture.store.Progress.Init("changed", 99) }},
			{"set total", func() error { return fixture.store.Progress.SetTotalChapters(99) }},
			{"set name", func() error { return fixture.store.Progress.SetNovelName("changed") }},
			{"update phase", func() error { return fixture.store.Progress.UpdatePhase(domain.PhaseWriting) }},
			{"start chapter", func() error { return fixture.store.Progress.StartChapter(1) }},
			{"complete chapter", func() error { return fixture.store.Progress.MarkChapterComplete(1, 100, "hook", "strand") }},
			{"complete book", fixture.store.Progress.MarkComplete},
			{"completion audit", func() error { return fixture.store.Progress.SetCompletionAudit("fail", "changed") }},
			{"reopen", func() error { return fixture.store.Progress.Reopen([]int{1}, "changed") }},
			{"reopen flow", func() error { return fixture.store.Progress.ReopenWithFlow([]int{1}, "changed", domain.FlowRewriting) }},
			{"queue rewrite", func() error {
				return fixture.store.Progress.QueuePendingRewrites([]int{1}, "changed", domain.FlowRewriting)
			}},
			{"clear in progress", fixture.store.Progress.ClearInProgress},
			{"volume arc", func() error { return fixture.store.Progress.UpdateVolumeArc(9, 9) }},
			{"layered", func() error { return fixture.store.Progress.SetLayered(false) }},
			{"flow", func() error { return fixture.store.Progress.SetFlow(domain.FlowRewriting) }},
			{"pending rewrites", func() error { return fixture.store.Progress.SetPendingRewrites([]int{1}, "changed") }},
			{"complete rewrite", func() error { return fixture.store.Progress.CompleteRewrite(1) }},
			{"clear rewrites", fixture.store.Progress.ClearPendingRewrites},
		}
		for _, writer := range progressWriters {
			if err := writer.call(); !errors.Is(err, ErrRevisionCommandInProgress) {
				return fmt.Errorf("progress %s bypass error=%v", writer.name, err)
			}
		}
		outlineWriters := []struct {
			name string
			call func() error
		}{
			{"flat", func() error { return fixture.store.Outline.SaveOutline(domain.FlattenOutline(fixture.candidate)) }},
			{"layered", func() error { return fixture.store.Outline.SaveLayeredOutline(fixture.candidate) }},
			{"clear layered", fixture.store.Outline.ClearLayeredOutline},
		}
		for _, writer := range outlineWriters {
			if err := writer.call(); !errors.Is(err, ErrRevisionCommandInProgress) {
				return fmt.Errorf("outline %s bypass error=%v", writer.name, err)
			}
		}
		if _, err := fixture.store.ReconcilePendingRewriteProgress(); !errors.Is(err, ErrRevisionCommandInProgress) {
			return fmt.Errorf("reconcile progress bypass error=%v", err)
		}
		if err := fixture.store.ClearHandledSteer(); !errors.Is(err, ErrRevisionCommandInProgress) {
			return fmt.Errorf("clear steering progress bypass error=%v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after := snapshotFormalWriterFiles(t, fixture.dir)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected child writers changed formal bytes\nbefore=%v\nafter=%v", before, after)
	}
}

func TestNormalPublicationRequiresExactAppliedOwner(t *testing.T) {
	fixture := newNormalPublicationFixture(t, "plain-publish-rejection")
	seedNormalPublicationSentinels(t, fixture.dir)

	before := snapshotNormalPublicationProjectFiles(t, fixture.dir)
	if _, err := fixture.store.Revisions.Publish(fixture.policy, fixture.input); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("plain normal Publish error=%v", err)
	}
	if after := snapshotNormalPublicationProjectFiles(t, fixture.dir); !reflect.DeepEqual(before, after) {
		t.Fatal("plain normal Publish changed revision, formal, runtime, receipt, audit, or journal bytes")
	}
	active, err := fixture.store.Revisions.Active()
	if err != nil || active == nil || active.ID != fixture.session.ID || active.Stage != domain.RevisionStageReadyToPublish {
		t.Fatalf("plain normal Publish consumed the active lifecycle: active=%+v err=%v", active, err)
	}
	if current, err := fixture.store.Revisions.CurrentVersion(domain.NormalStructureSnapshotID); err != nil || current != nil {
		t.Fatalf("plain normal Publish consumed candidate artifacts: current=%+v err=%v", current, err)
	}

	before = snapshotNormalPublicationProjectFiles(t, fixture.dir)
	if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.baseline, fixture.input.IdempotencyKey); err == nil {
		t.Fatal("valid but unaccepted structure substituted for the bound candidate")
	}
	if after := snapshotNormalPublicationProjectFiles(t, fixture.dir); !reflect.DeepEqual(before, after) {
		t.Fatal("candidate substitution changed project bytes")
	}

	if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	before = snapshotNormalPublicationProjectFiles(t, fixture.dir)
	if _, err := fixture.store.Revisions.Publish(fixture.policy, fixture.input); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("plain Publish entered a formal_applied attempt: %v", err)
	}
	if after := snapshotNormalPublicationProjectFiles(t, fixture.dir); !reflect.DeepEqual(before, after) {
		t.Fatal("plain Publish consumed the durable formal_applied attempt")
	}
	wrongInput := fixture.input
	wrongInput.IdempotencyKey += "-substituted"
	if _, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, wrongInput, fixture.owner); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("owner accepted a substituted publish identity: %v", err)
	}
	if after := snapshotNormalPublicationProjectFiles(t, fixture.dir); !reflect.DeepEqual(before, after) {
		t.Fatal("publish identity substitution changed project bytes")
	}

	published, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner)
	if err != nil || published == nil || published.Stage != domain.RevisionStageCompleted {
		t.Fatalf("exact owner publication=%+v err=%v", published, err)
	}
	assertFormalStructure(t, fixture.store, fixture.candidate, 2)
	if active, err := fixture.store.Revisions.Active(); err != nil || active != nil {
		t.Fatalf("exact owner did not complete the lifecycle: active=%+v err=%v", active, err)
	}
	if current, err := fixture.store.Revisions.CurrentVersion(domain.NormalStructureSnapshotID); err != nil || current == nil {
		t.Fatalf("exact owner did not publish bound artifacts: current=%+v err=%v", current, err)
	}
}

func TestNormalPublicationOwnerBindsCandidateAndOneShotAttempt(t *testing.T) {
	t.Run("candidate substitution and rollback mismatch", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "candidate-binding")
		before := snapshotFormalWriterFiles(t, fixture.dir)
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.baseline, fixture.input.IdempotencyKey); err == nil {
			t.Fatal("valid but unaccepted candidate substituted for the canonical candidate")
		}
		if after := snapshotFormalWriterFiles(t, fixture.dir); !reflect.DeepEqual(before, after) {
			t.Fatal("candidate substitution changed formal bytes")
		}
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.RestoreLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.progress); err == nil {
			t.Fatal("rollback accepted a snapshot that was not the bound prepublish state")
		}
		assertFormalStructure(t, fixture.store, fixture.candidate, 2)
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err != nil {
			t.Fatal(err)
		}
		assertFormalStructure(t, fixture.store, fixture.baseline, 1)
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err == nil {
			t.Fatal("rollback owner replay succeeded")
		}
	})

	t.Run("failed formal write rolls back once", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "failure-rollback")
		injected := true
		fixture.store.Outline.migration.failpoint = func(point string) error {
			if injected && point == migrationFailAfterWrite {
				injected = false
				return fmt.Errorf("injected publication failure")
			}
			return nil
		}
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err == nil {
			t.Fatal("injected formal publication failure did not fire")
		}
		fixture.store.Outline.migration.failpoint = nil
		before := snapshotNormalPublicationProjectFiles(t, fixture.dir)
		if _, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner); !errors.Is(err, ErrRevisionCommandInProgress) {
			t.Fatalf("prepared-only publication attempt finalized: %v", err)
		}
		if after := snapshotNormalPublicationProjectFiles(t, fixture.dir); !reflect.DeepEqual(before, after) {
			t.Fatal("rejected prepared-only finalization changed project bytes")
		}
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err != nil {
			t.Fatal(err)
		}
		assertFormalStructure(t, fixture.store, fixture.baseline, 1)
	})

	t.Run("formal structure substitution blocks finalization", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "formal-structure-binding")
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
			t.Fatal(err)
		}
		payload, err := json.MarshalIndent(fixture.baseline, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.dir, "layered_outline.json"), payload, 0o644); err != nil {
			t.Fatal(err)
		}
		before := snapshotNormalPublicationProjectFiles(t, fixture.dir)
		if _, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner); err == nil || !strings.Contains(err.Error(), "formal structure changed") {
			t.Fatalf("substituted formal structure finalization error=%v", err)
		}
		if after := snapshotNormalPublicationProjectFiles(t, fixture.dir); !reflect.DeepEqual(before, after) {
			t.Fatal("formal structure mismatch rejection changed project bytes")
		}
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err != nil {
			t.Fatal(err)
		}
		assertFormalStructure(t, fixture.store, fixture.baseline, 1)
	})

	t.Run("success invalidates owner and blocks successor", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "success-one-shot")
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
			t.Fatal(err)
		}
		published, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner)
		if err != nil || published.Stage != domain.RevisionStageCompleted {
			t.Fatalf("published=%+v err=%v", published, err)
		}
		if _, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner); err == nil {
			t.Fatal("successful owner replay returned its old receipt")
		}
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err == nil {
			t.Fatal("successful publication owner rolled back committed formal state")
		}
		assertFormalStructure(t, fixture.store, fixture.candidate, 2)
	})

	t.Run("restart recovers bound snapshot", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "restart-rollback")
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
			t.Fatal(err)
		}
		reopened := NewStore(fixture.dir)
		if err := reopened.RecoverStructureMigration(); err != nil {
			t.Fatal(err)
		}
		assertFormalStructure(t, reopened, fixture.baseline, 1)
		if err := reopened.RollbackLayeredStructureForRevision(fixture.owner); err == nil {
			t.Fatal("recovered publication owner remained reusable")
		}
	})

	t.Run("active lease successor and cross project reject stale owner", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "stale-owner")
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err != nil {
			t.Fatal(err)
		}
		cancelled, err := fixture.store.Revisions.Cancel(fixture.policy, RevisionMutationInput{
			SessionID: fixture.session.ID, ExpectedRevision: fixture.session.Revision, IdempotencyKey: "cancel-after-rollback",
		})
		if err != nil || cancelled.Stage != domain.RevisionStageCancelled {
			t.Fatalf("cancelled=%+v err=%v", cancelled, err)
		}
		lease, err := fixture.store.Revisions.AcquireNormalFlow("successor-normal-flow")
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err == nil {
			t.Fatal("stale owner overwrote an active normal-flow lease")
		}
		if err := fixture.store.Revisions.ReleaseNormalFlow(lease.Token); err != nil {
			t.Fatal(err)
		}
		successorImpact, _ := domain.NewRevisionImpact("successor", []domain.RevisionImpactItem{{ArtifactID: domain.NormalStructureSnapshotID, ArtifactKind: domain.NormalArtifactStructureSnapshot, Change: "successor"}})
		if _, err := fixture.store.Revisions.Start(fixture.policy, StartRevisionInput{Intent: "successor", Impact: successorImpact, IdempotencyKey: "successor-start"}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err == nil {
			t.Fatal("stale owner overwrote a successor revision")
		}
		other := newNormalPublicationFixture(t, "cross-project")
		if err := other.store.PublishLayeredStructureForRevision(fixture.owner, other.candidate, fixture.input.IdempotencyKey); err == nil {
			t.Fatal("cross-project publication owner was accepted")
		}
	})
}

func snapshotFormalWriterFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
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
		rel = filepath.ToSlash(rel)
		if rel != "meta/progress.json" && rel != "outline.json" && rel != "outline.md" &&
			rel != "layered_outline.json" && rel != "layered_outline.md" && !strings.HasPrefix(rel, structureRootDir+"/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func seedNormalPublicationSentinels(t *testing.T, root string) {
	t.Helper()
	for rel, content := range map[string]string{
		adaptationAuditReportFile:             `{"status":"sentinel-audit"}`,
		adaptationRevisionRuntimeFile:         `{"status":"sentinel-runtime"}`,
		adaptationRevisionServiceReceiptsFile: `{"version":1,"receipts":{"sentinel":{}}}`,
		continuationCommitJournalFile:         `{"stage":"sentinel-journal"}`,
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func snapshotNormalPublicationProjectFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
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
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".lock") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertFormalStructure(t *testing.T, st *Store, want []domain.VolumeOutline, total int) {
	t.Helper()
	got, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("formal structure mismatch\ngot=%+v\nwant=%+v", got, want)
	}
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	if progress == nil || progress.TotalChapters != total {
		t.Fatalf("formal progress=%+v want total=%d", progress, total)
	}
}
