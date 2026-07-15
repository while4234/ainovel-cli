package store

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestAdaptationRevisionPolicySessionResumesAfterStoreRestart(t *testing.T) {
	dir := t.TempDir()
	policy := domain.AdaptationRevisionPolicy{Stage: domain.ManuscriptStageWriting}
	impact, err := domain.NewRevisionImpact("adaptation writing insertion", []domain.RevisionImpactItem{
		{
			ArtifactID: "target-2", ArtifactKind: domain.StructureKindChapter, Change: "insert target chapter",
			Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency,
			DependencyEvidence: []string{"stable target insertion"},
		},
		{
			ArtifactID: domain.AdaptationRevisionBatchPlanID, ArtifactKind: domain.AdaptationRevisionArtifactBatchPlan,
			Change: "bounded source-local work", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency,
			DependencyEvidence: []string{"BatchPlan boundary"},
		},
		{
			ArtifactID: domain.AdaptationRevisionPlanSnapshotID, ArtifactKind: domain.AdaptationRevisionArtifactPlanSnapshot,
			Change: "bind source contract", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency,
			DependencyEvidence: []string{"immutable source signature"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := NewRevisionStore(dir).Start(policy, StartRevisionInput{
		Intent: "insert chapter during writing", Impact: impact, IdempotencyKey: "adaptation-start",
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := NewRevisionStore(dir).ApproveImpact(policy, RevisionMutationInput{
		SessionID: started.ID, ExpectedRevision: started.Revision, IdempotencyKey: "adaptation-impact",
	})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := NewRevisionStore(dir).Pause(policy, RevisionMutationInput{
		SessionID: approved.ID, ExpectedRevision: approved.Revision, IdempotencyKey: "adaptation-pause",
	})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := NewRevisionStore(dir).Resume(policy, RevisionMutationInput{
		SessionID: paused.ID, ExpectedRevision: paused.Revision, IdempotencyKey: "adaptation-resume",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Stage != domain.RevisionStageCandidateGenerating || resumed.ResumeStage != "" || resumed.Generation <= started.Generation {
		t.Fatalf("resumed adaptation revision lost durable position: %+v", resumed)
	}
}

func TestAdaptationRevisionStoreDestructivePathsFenceBeforeFirstWrite(t *testing.T) {
	st := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "volume", Theme: "theme",
		Arcs: []domain.ArcOutline{{Index: 1, Title: "arc", Chapters: []domain.OutlineEntry{{Chapter: 1, Title: "one", CoreEvent: "event", Hook: "hook"}}}},
	}})
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc, Status: domain.AdaptationPlanStatusConfirmed,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Volumes:       []domain.AdaptationVolumePlan{{Index: 1, Title: "volume", TargetFrom: 1, TargetTo: 1, SourceFrom: 1, SourceTo: 1}},
		Chapters: []domain.AdaptationChapterPlan{{
			OutlineEntry: domain.OutlineEntry{Chapter: 1, Title: "one", CoreEvent: "event", Hook: "hook"},
			Chapter:      1, Title: "one", SourceChapters: []int{1}, SourceRange: domain.SourceRange{From: 1, To: 1},
			TargetRunes: 1000, TargetMinRunes: 800, TargetMaxRunes: 1200,
		}},
	}
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	report := adaptaudit.Audit(adaptaudit.Input{Mode: adaptaudit.ModeFree, Scope: adaptaudit.Scope{TargetFrom: 1, TargetTo: 1}})
	if err := st.Adaptation.SaveAuditReport(report); err != nil {
		t.Fatal(err)
	}
	before, err := st.CaptureAdaptationFormalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	session := startAdaptationStoreRevision(t, st)

	for name, mutate := range map[string]func() error{
		"reset":           st.Adaptation.Reset,
		"reset generated": st.Adaptation.ResetGenerated,
		"rollback migration": func() error {
			_, err := st.Rollback(domain.RollbackRequest{Confirm: true})
			return err
		},
		"repair outline": func() error {
			return st.RepairArcOutline(1, 1, []domain.OutlineEntry{{Chapter: 1, Title: "changed", CoreEvent: "changed", Hook: "changed"}})
		},
		"repair recovery": func() error {
			_, err := st.FindDuplicateOutlineRepairBatch(&domain.Progress{Layered: true, TotalChapters: 1})
			return err
		},
		"audit migration": func() error {
			_, err := st.Adaptation.ListAuditRuns()
			return err
		},
		"expand arc": func() error {
			return st.ExpandArc(1, 1, []domain.OutlineEntry{{Chapter: 1, Title: "changed", CoreEvent: "changed", Hook: "changed", Scenes: []string{"changed"}}})
		},
		"append volume": func() error {
			return st.AppendVolume(domain.VolumeOutline{
				Index: 2, Title: "two", Theme: "two",
				Arcs: []domain.ArcOutline{{
					Index: 1, Title: "arc",
					Chapters: []domain.OutlineEntry{{Title: "two", CoreEvent: "two", Hook: "two", Scenes: []string{"two"}}},
				}},
			})
		},
		"append skeleton volume": func() error {
			return st.AppendSkeletonVolume(domain.VolumeOutline{Index: 2, Title: "two", Theme: "two", Arcs: []domain.ArcOutline{{Index: 1, Title: "arc"}}})
		},
		"revise chapter outline": func() error {
			return st.ReviseChapterOutline(1, domain.OutlineEntry{Title: "changed", CoreEvent: "changed", Hook: "changed", Scenes: []string{"changed"}})
		},
		"audit clear without owner":    func() error { return st.ClearAdaptationRevisionAudits(st.Revisions, "wrong-session") },
		"formal restore without owner": func() error { return st.RestoreAdaptationFormalSnapshot(st.Revisions, before, "wrong-session") },
	} {
		t.Run(name, func(t *testing.T) {
			if err := mutate(); err == nil || !errors.Is(err, ErrRevisionCommandInProgress) &&
				!strings.Contains(err.Error(), "active revision") && !strings.Contains(err.Error(), "does not own") {
				t.Fatalf("destructive path was not owner-fenced: %v", err)
			}
			after, captureErr := st.CaptureAdaptationFormalSnapshot()
			if captureErr != nil {
				t.Fatal(captureErr)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected destructive path changed the formal snapshot")
			}
		})
	}

	if err := st.WithPreparedAdaptationRevisionCommand("clear-audits", "publish", "clear-audits-fingerprint", func(owner *RevisionStore) error {
		return st.ClearAdaptationRevisionAudits(owner, session.ID)
	}); err != nil {
		t.Fatalf("active owner could not clear revision audits: %v", err)
	}
}

func TestLegacyMutationSerializesWithRevisionStart(t *testing.T) {
	st := NewStore(t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- st.Revisions.withLegacyMutation("race probe", func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	startDone := make(chan error, 1)
	go func() {
		_, err := st.Revisions.Start(domain.AdaptationRevisionPolicy{Stage: domain.ManuscriptStageWriting}, StartRevisionInput{
			Intent: "race", Impact: adaptationStoreFenceImpact(t), IdempotencyKey: "race-start",
		})
		startDone <- err
	}()
	select {
	case err := <-startDone:
		t.Fatalf("revision start bypassed the legacy write transaction: %v", err)
	default:
	}
	close(release)
	if err := <-mutationDone; err != nil {
		t.Fatal(err)
	}
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
}

func TestPreparedRevisionCommandRequiresFullOwnerCapability(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	revisions := st.Revisions
	owner, err := revisions.claimCommandFence("shared-key", "preview", "preview-fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	policy := domain.AdaptationRevisionPolicy{Stage: domain.ManuscriptStageWriting}
	start := StartRevisionInput{Intent: "owned preview", Impact: adaptationStoreFenceImpact(t), IdempotencyKey: "owned-start"}

	if _, err := NewRevisionStore(dir).Start(policy, StartRevisionInput{
		Intent: "forged same-key preview", Impact: start.Impact, IdempotencyKey: "shared-key",
	}); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("ordinary same-key start entered prepared command: %v", err)
	}
	started, err := owner.Start(policy, start)
	if err != nil {
		t.Fatalf("owned nested start was blocked: %v", err)
	}
	if replay, err := owner.Start(policy, start); err != nil || !reflect.DeepEqual(replay, started) {
		t.Fatalf("owned exact replay failed: replay=%+v err=%v", replay, err)
	}
	if _, err := NewRevisionStore(dir).Start(policy, start); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("ordinary exact receipt replay bypassed prepared ownership: %v", err)
	}
	if _, err := NewRevisionStore(dir).Pause(policy, RevisionMutationInput{
		SessionID: started.ID, ExpectedRevision: started.Revision, IdempotencyKey: "shared-key",
	}); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("ordinary different mutation forged prepared key: %v", err)
	}
	paused, err := owner.Pause(policy, RevisionMutationInput{
		SessionID: started.ID, ExpectedRevision: started.Revision, IdempotencyKey: "owned-pause",
	})
	if err != nil {
		t.Fatalf("owned nested mutation was blocked: %v", err)
	}
	if _, err := revisions.claimCommandFence("shared-key", "publish", "preview-fingerprint"); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("operation metadata did not participate in ownership: %v", err)
	}
	if _, err := revisions.claimCommandFence("shared-key", "preview", "different-fingerprint"); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("fingerprint metadata did not participate in ownership: %v", err)
	}
	if err := st.PrepareAdaptationRevisionCommand(owner, "shared-key", "publish", "preview-fingerprint"); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("scoped owner API accepted different operation metadata: %v", err)
	}
	if err := NewStore(t.TempDir()).PrepareAdaptationRevisionCommand(owner, "shared-key", "preview", "preview-fingerprint"); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("scoped owner API accepted a capability from another project: %v", err)
	}
	if err := owner.releaseCommandFence(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRevisionStore(dir).Resume(policy, RevisionMutationInput{
		SessionID: paused.ID, ExpectedRevision: paused.Revision, IdempotencyKey: "successor-resume",
	}); err != nil {
		t.Fatalf("successor mutation remained fenced after owner cleanup: %v", err)
	}
}

func TestPreparedRevisionCommandOwnsServiceReceiptAndRuntimeWrites(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	session := startAdaptationStoreRevision(t, st)
	runtime := adaptationStoreRuntime(session.ID, false)
	if err := st.SaveAdaptationRevisionRuntime(st.Revisions, runtime); err != nil {
		t.Fatal(err)
	}
	competitor := NewStore(dir)
	otherProject := NewStore(t.TempDir())
	var staleOwner *RevisionStore

	err := st.WithPreparedAdaptationRevisionCommand("owned-durable-write", "cancel", "cancel-fingerprint", func(owner *RevisionStore) error {
		if token, decodeErr := hex.DecodeString(owner.commandOwner.Token); decodeErr != nil || len(token) != 32 {
			t.Fatalf("prepared owner capability is not 256 bits: token_bytes=%d err=%v", len(token), decodeErr)
		}
		if err := st.PrepareAdaptationRevisionCommand(owner, "owned-durable-write", "cancel", "cancel-fingerprint"); err != nil {
			return err
		}
		for name, forged := range map[string]error{
			"matching receipt": competitor.SaveAdaptationRevisionServiceReceipt(competitor.Revisions, "owned-durable-write", "cancel", "cancel-fingerprint", session),
			"different key":    st.SaveAdaptationRevisionServiceReceipt(owner, "different-key", "cancel", "cancel-fingerprint", session),
			"different operation": st.SaveAdaptationRevisionServiceReceipt(
				owner, "owned-durable-write", "publish", "cancel-fingerprint", session,
			),
			"different fingerprint": st.SaveAdaptationRevisionServiceReceipt(
				owner, "owned-durable-write", "cancel", "different-fingerprint", session,
			),
			"runtime save":  competitor.SaveAdaptationRevisionRuntime(competitor.Revisions, adaptationStoreRuntime(session.ID, true)),
			"runtime clear": competitor.ClearAdaptationRevisionRuntime(competitor.Revisions, session.ID),
			"cross project": st.SaveAdaptationRevisionServiceReceipt(otherProject.Revisions, "owned-durable-write", "cancel", "cancel-fingerprint", session),
		} {
			if !errors.Is(forged, ErrRevisionCommandInProgress) {
				t.Fatalf("%s bypassed prepared ownership: %v", name, forged)
			}
		}
		staleOwner = owner.withCommandOwner(&revisionCommandFence{
			Key: owner.commandOwner.Key, Operation: owner.commandOwner.Operation,
			Fingerprint: owner.commandOwner.Fingerprint, OwnerToken: strings.Repeat("0", 64),
		})
		if err := st.SaveAdaptationRevisionRuntime(staleOwner, adaptationStoreRuntime(session.ID, true)); !errors.Is(err, ErrRevisionCommandInProgress) {
			t.Fatalf("stale owner token changed runtime: %v", err)
		}
		ownedRuntime := adaptationStoreRuntime(session.ID, true)
		if err := st.SaveAdaptationRevisionRuntime(owner, ownedRuntime); err != nil {
			return err
		}
		if err := st.ClearAdaptationRevisionRuntime(owner, session.ID); err != nil {
			return err
		}
		if err := st.SaveAdaptationRevisionRuntime(owner, ownedRuntime); err != nil {
			return err
		}
		if err := st.SaveAdaptationRevisionServiceReceipt(owner, "owned-durable-write", "cancel", "cancel-fingerprint", session); err != nil {
			return err
		}
		return st.CompleteAdaptationRevisionCommand(owner, "owned-durable-write", "cancel", "cancel-fingerprint")
	})
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := st.Adaptation.LoadRevisionRuntime()
	if err != nil || persisted == nil || !persisted.Paused {
		t.Fatalf("owner runtime was not durable: runtime=%+v err=%v", persisted, err)
	}
	if err := st.SaveAdaptationRevisionRuntime(st.Revisions, adaptationStoreRuntime(session.ID, false)); err != nil {
		t.Fatalf("successor remained fenced after terminal receipt cleanup: %v", err)
	}
	if err := st.SaveAdaptationRevisionServiceReceipt(staleOwner, "owned-durable-write", "cancel", "cancel-fingerprint", session); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("released owner token remained usable: %v", err)
	}
	if err := st.SaveAdaptationRevisionRuntime(staleOwner, adaptationStoreRuntime(session.ID, false)); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("released owner token changed runtime: %v", err)
	}
}

func TestPreparedRevisionCommandOwnsEveryFormalWriteAndRollback(t *testing.T) {
	st := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "volume", Theme: "theme",
		Arcs: []domain.ArcOutline{{Index: 1, Title: "arc", Chapters: []domain.OutlineEntry{{Chapter: 1, Title: "one", CoreEvent: "event", Hook: "hook"}}}},
	}})
	base := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc, Status: domain.AdaptationPlanStatusConfirmed,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Volumes:       []domain.AdaptationVolumePlan{{Index: 1, Title: "volume", TargetFrom: 1, TargetTo: 1, SourceFrom: 1, SourceTo: 1}},
		Chapters: []domain.AdaptationChapterPlan{{
			OutlineEntry: domain.OutlineEntry{Chapter: 1, Title: "one", CoreEvent: "event", Hook: "hook"},
			Chapter:      1, Title: "one", SourceChapters: []int{1}, SourceRange: domain.SourceRange{From: 1, To: 1},
			TargetRunes: 1000, TargetMinRunes: 800, TargetMaxRunes: 1200,
		}},
	}
	if err := st.Adaptation.SavePlan(base); err != nil {
		t.Fatal(err)
	}
	report := adaptaudit.Audit(adaptaudit.Input{Mode: adaptaudit.ModeArc, Scope: adaptaudit.Scope{TargetFrom: 1, TargetTo: 1}})
	if err := st.Adaptation.SaveAuditReport(report); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, Layered: true, TotalChapters: 1}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := st.CaptureAdaptationFormalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	session := startAdaptationStoreRevision(t, st)
	competitor := NewStore(st.Dir())
	otherProject := NewStore(t.TempDir())
	var staleOwner *RevisionStore
	if err := st.WithPreparedAdaptationRevisionCommand("stale-formal", "publish", "stale-formal-fingerprint", func(owner *RevisionStore) error {
		staleOwner = owner
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	changed := base
	changed.Chapters = append([]domain.AdaptationChapterPlan(nil), base.Chapters...)
	changed.Chapters[0].Title = "owner change"
	changed.Chapters[0].OutlineEntry.Title = "owner change"
	progress := &domain.Progress{Phase: domain.PhaseWriting, Layered: true, TotalChapters: 2}
	layered, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatal(err)
	}
	before := formalWriteProjectBytes(t, st.Dir())
	var currentOwner *RevisionStore
	err = st.WithPreparedAdaptationRevisionCommand("owned-formal", "publish", "owned-formal-fingerprint", func(owner *RevisionStore) error {
		currentOwner = owner
		if err := st.PrepareAdaptationRevisionCommand(owner, "owned-formal", "publish", "owned-formal-fingerprint"); err != nil {
			return err
		}
		forgedPublicationOwner := &RevisionPublicationOwner{
			revisions: competitor.Revisions, policy: domain.NormalRevisionPolicy{}, sessionID: session.ID,
			expectedRevision: session.Revision, mode: domain.RevisionModeNormal, policyID: domain.NormalRevisionPolicyID,
			policyVersion: domain.NormalRevisionPolicyVersion,
		}
		attempts := []struct {
			name string
			call func() error
		}{
			{"same-session plan save", func() error {
				return competitor.SaveAdaptationPlanForRevision(competitor.Revisions, changed, session.ID)
			}},
			{"same-session plan restore", func() error {
				return competitor.RestoreAdaptationPlanForRevision(competitor.Revisions, base, session.ID)
			}},
			{"same-session snapshot restore", func() error {
				return competitor.RestoreAdaptationFormalSnapshot(competitor.Revisions, snapshot, session.ID)
			}},
			{"same-session audit cleanup", func() error { return competitor.ClearAdaptationRevisionAudits(competitor.Revisions, session.ID) }},
			{"same-session progress write", func() error {
				return competitor.SaveAdaptationRevisionProgress(competitor.Revisions, progress, session.ID)
			}},
			{"cross-project plan save", func() error { return st.SaveAdaptationPlanForRevision(otherProject.Revisions, changed, session.ID) }},
			{"stale plan save", func() error { return st.SaveAdaptationPlanForRevision(staleOwner, changed, session.ID) }},
			{"layered publish", func() error {
				return st.PublishLayeredStructureForRevision(forgedPublicationOwner, layered, "forged-layered-publish")
			}},
			{"layered restore", func() error { return st.RestoreLayeredStructureForRevision(forgedPublicationOwner, layered, progress) }},
		}
		errs := make([]error, len(attempts))
		var wg sync.WaitGroup
		for index := range attempts {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				errs[index] = attempts[index].call()
			}(index)
		}
		wg.Wait()
		for index, attempt := range attempts {
			if !errors.Is(errs[index], ErrRevisionCommandInProgress) {
				t.Fatalf("%s bypassed exact prepared ownership: %v", attempt.name, errs[index])
			}
		}
		if after := formalWriteProjectBytes(t, st.Dir()); !reflect.DeepEqual(before, after) {
			t.Fatal("rejected prepared-command races changed formal bytes")
		}
		if err := st.SaveAdaptationPlanForRevision(owner, changed, session.ID); err != nil {
			return err
		}
		if err := st.SaveAdaptationRevisionProgress(owner, progress, session.ID); err != nil {
			return err
		}
		if err := st.ClearAdaptationRevisionAudits(owner, session.ID); err != nil {
			return err
		}
		if err := st.RestoreAdaptationPlanForRevision(owner, base, session.ID); err != nil {
			return err
		}
		if err := st.RestoreAdaptationFormalSnapshot(owner, snapshot, session.ID); err != nil {
			return err
		}
		if err := st.SaveAdaptationRevisionServiceReceipt(owner, "owned-formal", "publish", "owned-formal-fingerprint", session); err != nil {
			return err
		}
		return st.CompleteAdaptationRevisionCommand(owner, "owned-formal", "publish", "owned-formal-fingerprint")
	})
	if err != nil {
		t.Fatal(err)
	}
	if after := formalWriteProjectBytes(t, st.Dir()); !reflect.DeepEqual(before, after) {
		t.Fatal("genuine owner rollback did not restore byte-identical formal state")
	}
	if err := st.SaveAdaptationPlanForRevision(currentOwner, changed, session.ID); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("released formal owner remained usable: %v", err)
	}
	if after := formalWriteProjectBytes(t, st.Dir()); !reflect.DeepEqual(before, after) {
		t.Fatal("stale owner changed formal bytes after release")
	}
}

func TestPreparedRevisionRuntimeCrashRecoveryRestoresExactCheckpoint(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	session := startAdaptationStoreRevision(t, st)
	before := adaptationStoreRuntime(session.ID, false)
	if err := st.SaveAdaptationRevisionRuntime(st.Revisions, before); err != nil {
		t.Fatal(err)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("prepared command did not simulate a process crash")
			}
		}()
		_ = st.WithPreparedAdaptationRevisionCommand("runtime-crash", "pause", "pause-fingerprint", func(owner *RevisionStore) error {
			if err := st.PrepareAdaptationRevisionCommand(owner, "runtime-crash", "pause", "pause-fingerprint"); err != nil {
				return err
			}
			if err := st.SaveAdaptationRevisionRuntime(owner, adaptationStoreRuntime(session.ID, true)); err != nil {
				return err
			}
			panic("simulated crash after runtime write")
		})
	}()

	restarted := NewStore(dir)
	if restarted.commandRecoveryErr != nil {
		t.Fatalf("restart recovery failed: %v", restarted.commandRecoveryErr)
	}
	after, err := restarted.Adaptation.LoadRevisionRuntime()
	if err != nil || !reflect.DeepEqual(after, &before) {
		t.Fatalf("restart did not restore exact runtime: got=%+v want=%+v err=%v", after, before, err)
	}
	if found, err := restarted.Adaptation.HasRevisionServiceReceipt("runtime-crash", "pause", "pause-fingerprint"); err != nil || found {
		t.Fatalf("crash recovery retained a forged terminal marker: found=%v err=%v", found, err)
	}
	if err := restarted.SaveAdaptationRevisionRuntime(restarted.Revisions, adaptationStoreRuntime(session.ID, true)); err != nil {
		t.Fatalf("restart recovery did not release successor writes: %v", err)
	}
}

func adaptationStoreRuntime(sessionID string, paused bool) domain.AdaptationRevisionRuntime {
	return domain.AdaptationRevisionRuntime{
		Version: domain.AdaptationRevisionRuntimeVersion, SessionID: sessionID,
		Stage: domain.ManuscriptStageWriting, BasePlanSignature: "base",
		SourceManifestSignature: "source", PreviewSignature: "preview", Paused: paused,
		BatchPlan: domain.BatchPlan{Batches: []domain.BatchWork{{ID: "batch-1"}}},
	}
}

func formalWriteProjectBytes(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte)
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".lock") || strings.HasPrefix(rel, "meta/revisions/") ||
			strings.Contains(rel, "adaptation-command-journal") || strings.Contains(rel, "adaptation-command-snapshot") ||
			rel == adaptationRevisionServiceReceiptsFile || rel == adaptationRevisionRuntimeFile {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[rel] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPendingMigrationRecoveryIsFencedOnReopenAndCommand(t *testing.T) {
	st := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "one", Theme: "one",
		Arcs: []domain.ArcOutline{{Index: 1, Title: "arc", Chapters: []domain.OutlineEntry{{Chapter: 1, Title: "one", CoreEvent: "one", Hook: "one"}}}},
	}})
	st.Outline.migration.failpoint = func(stage string) error {
		if stage == migrationFailAfterWrite {
			return errors.New("leave pending migration")
		}
		return nil
	}
	err := st.AppendSkeletonVolume(domain.VolumeOutline{Index: 2, Title: "two", Theme: "two", Arcs: []domain.ArcOutline{{Index: 1, Title: "arc"}}})
	if err == nil {
		t.Fatal("expected pending migration failpoint")
	}
	st.Outline.migration.failpoint = nil
	before, err := st.CaptureAdaptationFormalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	startAdaptationStoreRevision(t, st)
	reopened := NewStore(st.Dir())
	if reopened.recoveryErr == nil || !strings.Contains(reopened.recoveryErr.Error(), "active revision") {
		t.Fatalf("startup recovered a pending migration through the active fence: %v", reopened.recoveryErr)
	}
	if err := reopened.RecoverStructureMigration(); err == nil || !strings.Contains(err.Error(), "active revision") {
		t.Fatalf("explicit recovery bypassed the active fence: %v", err)
	}
	if _, err := reopened.Adaptation.LoadPlan(); err == nil || !strings.Contains(err.Error(), "active revision") {
		t.Fatalf("LoadPlan recovered a pending migration through the active fence: %v", err)
	}
	if _, err := reopened.Outline.LoadLayeredOutline(); err == nil || !strings.Contains(err.Error(), "active revision") {
		t.Fatalf("index-backed outline read recovered a pending migration through the active fence: %v", err)
	}
	after, err := st.CaptureAdaptationFormalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected recovery changed the formal snapshot")
	}
}

func startAdaptationStoreRevision(t *testing.T, st *Store) *domain.RevisionSession {
	t.Helper()
	policy := domain.AdaptationRevisionPolicy{Stage: domain.ManuscriptStageWriting}
	impact := adaptationStoreFenceImpact(t)
	session, err := st.Revisions.Start(policy, StartRevisionInput{Intent: "fence direct paths", Impact: impact, PreviewSignature: "preview", IdempotencyKey: "store-fence"})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func adaptationStoreFenceImpact(t *testing.T) domain.RevisionImpact {
	t.Helper()
	impact, err := domain.NewRevisionImpact("adaptation store fence", []domain.RevisionImpactItem{
		{ArtifactID: "target-1", ArtifactKind: domain.StructureKindChapter, Change: "revise target", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"stable target"}},
		{ArtifactID: domain.AdaptationRevisionBatchPlanID, ArtifactKind: domain.AdaptationRevisionArtifactBatchPlan, Change: "bounded work", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"batch"}},
		{ArtifactID: domain.AdaptationRevisionPlanSnapshotID, ArtifactKind: domain.AdaptationRevisionArtifactPlanSnapshot, Change: "bind source", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"source"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return impact
}
