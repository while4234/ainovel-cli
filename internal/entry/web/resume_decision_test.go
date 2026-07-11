package web

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

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
