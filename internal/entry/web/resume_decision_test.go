package web

import (
	"errors"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type fakeAutoResumeRevisionPolicy struct{}

func (fakeAutoResumeRevisionPolicy) Identity() (string, string) {
	return "test.auto-resume-revision", "1"
}

func (fakeAutoResumeRevisionPolicy) Mode() domain.RevisionMode { return "fake-auto-resume" }

func (fakeAutoResumeRevisionPolicy) ApprovalStages(domain.RevisionImpact) ([]domain.RevisionApprovalStage, error) {
	return []domain.RevisionApprovalStage{{ID: "prose", Label: "Prose"}}, nil
}

func (fakeAutoResumeRevisionPolicy) ValidateImpact(domain.RevisionImpact) error { return nil }

func (fakeAutoResumeRevisionPolicy) ValidateCandidate(domain.RevisionSession, []domain.ArtifactVersion) error {
	return nil
}

func (fakeAutoResumeRevisionPolicy) Route(domain.RevisionSession) (*domain.RevisionRoute, error) {
	return nil, nil
}

func TestAutoResumeDecisionPreservesAdaptationReviewGates(t *testing.T) {
	tests := []struct {
		name       string
		stage      domain.AdaptationPlanningStage
		want       AutoResumeDisposition
		wantAction string
	}{
		{"skeleton generation", domain.AdaptationPlanningStageSkeletonGenerating, AutoResumeActionable, AutoResumeActionAdaptationSkeleton},
		{"volume review", domain.AdaptationPlanningStageVolumeReviewPending, AutoResumeWaitUser, ""},
		{"detail generation", domain.AdaptationPlanningStageDetailsGenerating, AutoResumeActionable, AutoResumeActionAdaptationDetails},
		{"proposal review", domain.AdaptationPlanningStageProposalReviewPending, AutoResumeWaitUser, ""},
		{"confirmed", domain.AdaptationPlanningStageConfirmed, AutoResumeNoWork, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outputDir := t.TempDir()
			st := storepkg.NewStore(outputDir)
			if err := st.Init(); err != nil {
				t.Fatalf("Init: %v", err)
			}
			switch tt.stage {
			case domain.AdaptationPlanningStageSkeletonGenerating:
				if err := st.Adaptation.SaveProposalRuntime(domain.AdaptationProposalRuntime{
					Version: 1, Brief: "resume", Granularity: domain.AdaptationGranularityFree,
					RewritePolicy: domain.AdaptationRewriteFullRewrite, TargetChapterCount: 3,
				}); err != nil {
					t.Fatalf("SaveProposalRuntime: %v", err)
				}
			case domain.AdaptationPlanningStageDetailsGenerating:
				if err := st.Adaptation.SaveVolumeReview(domain.AdaptationVolumeReview{
					Brief: "approved", Granularity: domain.AdaptationGranularityArc,
					Volumes: []domain.AdaptationVolumePlan{{Index: 1, Title: "One", TargetFrom: 1, TargetTo: 3}},
				}); err != nil {
					t.Fatalf("SaveVolumeReview: %v", err)
				}
			}
			current, err := st.Adaptation.LoadPlanningWorkflow()
			if err != nil {
				t.Fatalf("LoadPlanningWorkflow: %v", err)
			}
			expected := 0
			if current != nil && current.UpdatedAt != "" {
				expected = current.Revision
			}
			if _, err := st.Adaptation.SetPlanningWorkflowStage(tt.stage, expected); err != nil {
				t.Fatalf("SetPlanningWorkflowStage: %v", err)
			}
			fake := newFakeProjectHost()
			session, err := NewProjectSession(ProjectManifest{ID: "decision", OutputDir: outputDir}, fake)
			if err != nil {
				t.Fatalf("NewProjectSession: %v", err)
			}
			defer session.Close()

			decision, err := session.AutoResumeDecision()
			if err != nil {
				t.Fatalf("AutoResumeDecision: %v", err)
			}
			if decision.Disposition != tt.want || decision.Action != tt.wantAction {
				t.Fatalf("decision = %+v, want disposition=%q action=%q", decision, tt.want, tt.wantAction)
			}
			if decision.StateFingerprint == "" {
				t.Fatal("state fingerprint is empty")
			}
		})
	}
}

func TestAutoResumeDecisionContinuationReviewDoesNotAdvance(t *testing.T) {
	outputDir := t.TempDir()
	if err := storepkg.NewStore(outputDir).Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	fake := newFakeProjectHost()
	fake.continuationSnapshot = testContinuationSnapshot(domain.ContinuationStageReadyToWrite, 7)
	session, err := NewProjectSession(ProjectManifest{ID: "continuation", OutputDir: outputDir}, fake)
	if err != nil {
		t.Fatalf("NewProjectSession: %v", err)
	}
	defer session.Close()

	decision, err := session.AutoResumeDecision()
	if err != nil {
		t.Fatalf("AutoResumeDecision: %v", err)
	}
	if decision.Disposition != AutoResumeWaitUser || decision.Action != "" {
		t.Fatalf("decision = %+v, want wait_user", decision)
	}
}

func TestAutoAndManualResumeDoNotCrossActiveRevision(t *testing.T) {
	outputDir := t.TempDir()
	st := storepkg.NewStore(outputDir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	impact, err := domain.NewRevisionImpact("active revision", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.Start(fakeAutoResumeRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "revise", Impact: impact, IdempotencyKey: "start-revision",
	}); err != nil {
		t.Fatal(err)
	}
	fake := newFakeProjectHost()
	session, err := NewProjectSession(ProjectManifest{ID: "revision-block", OutputDir: outputDir}, fake)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	decision, err := session.AutoResumeDecision()
	if err != nil {
		t.Fatal(err)
	}
	if decision.Disposition != AutoResumeBlocked || decision.ReasonCode != "active_revision" || decision.Action != "" {
		t.Fatalf("auto resume decision = %+v", decision)
	}
	if _, err := session.Resume(); !errors.Is(err, storepkg.ErrActiveRevisionBlocksNormalFlow) {
		t.Fatalf("manual resume error = %v", err)
	}
}

func TestWebActionLeaseBlocksRevisionUntilActionFinishes(t *testing.T) {
	for _, kind := range []string{"", projectActionKindAdaptationProposal} {
		t.Run(map[bool]string{true: "empty kind", false: "named kind"}[kind == ""], func(t *testing.T) {
			dir := t.TempDir()
			session := &ProjectSession{
				manifest:    ProjectManifest{OutputDir: dir},
				actionKinds: make(map[string]int),
			}
			finish, err := session.beginActionKind(kind)
			if err != nil {
				t.Fatal(err)
			}
			impact, err := domain.NewRevisionImpact("web action", []domain.RevisionImpactItem{{
				ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
			}})
			if err != nil {
				t.Fatal(err)
			}
			revisions := storepkg.NewRevisionStore(dir)
			if _, err := revisions.Start(fakeAutoResumeRevisionPolicy{}, storepkg.StartRevisionInput{
				Intent: "must wait", Impact: impact, IdempotencyKey: "during-web-action",
			}); !errors.Is(err, storepkg.ErrActiveRevisionExists) {
				t.Fatalf("revision crossed running web action: %v", err)
			}
			finish()
			if _, err := revisions.Start(fakeAutoResumeRevisionPolicy{}, storepkg.StartRevisionInput{
				Intent: "after action", Impact: impact, IdempotencyKey: "after-web-action",
			}); err != nil {
				t.Fatalf("revision did not start after web action finished: %v", err)
			}
		})
	}
}

func TestActiveRevisionBlocksRepresentativeEmptyKindWebActionsBeforeHostCalls(t *testing.T) {
	outputDir := t.TempDir()
	st := storepkg.NewStore(outputDir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	impact, err := domain.NewRevisionImpact("active revision", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.Start(fakeAutoResumeRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "revise", Impact: impact, IdempotencyKey: "block-empty-web-actions",
	}); err != nil {
		t.Fatal(err)
	}
	fake := newFakeProjectHost()
	session, err := NewProjectSession(ProjectManifest{ID: "blocked-actions", OutputDir: outputDir}, fake)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tests := []struct {
		name string
		run  func() error
	}{
		{"quick start", func() error { return session.StartQuick("write a mystery", 5000) }},
		{"continue", func() error { return session.Continue("continue") }},
		{"idle steer", func() error { return session.Steer("later") }},
		{"rollback", func() error { _, err := session.Rollback(domain.RollbackRequest{Confirm: true}); return err }},
		{"co-create", func() error {
			_, err := session.BeginCoCreate(nil, webCoCreateBeginRequest{Kind: webCoCreateKindNormal, Initial: "write"})
			return err
		}},
		{"planning revision", func() error {
			return session.ReviseCoCreatePlanning(nil, webCoCreatePlanningRevisionRequest{Feedback: "change the ending"})
		}},
		{"planning confirmation", func() error { _, err := session.ConfirmCoCreatePlanning(); return err }},
		{"reset", session.ResetCoCreateProgress},
		{"cancel", func() error { _, err := session.CancelCoCreate(); return err }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, storepkg.ErrActiveRevisionBlocksNormalFlow) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if fake.prepareRulesCalls != 0 || fake.startPreparedCalls != 0 || fake.continueCalls != 0 || fake.steerCalls != 0 || fake.rollbackCalls != 0 || fake.cocreateCalls != 0 {
		t.Fatalf("blocked Web actions reached Host: %+v", fake)
	}
	if session.cocreate != nil {
		t.Fatal("blocked Web action changed co-create state")
	}
}
