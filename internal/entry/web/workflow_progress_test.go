package web

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
)

func TestWorkflowStatusesUseUnifiedWireValues(t *testing.T) {
	got := []WorkflowStatus{
		WorkflowStatusIdle,
		WorkflowStatusRunning,
		WorkflowStatusWaitingConfirmation,
		WorkflowStatusPaused,
		WorkflowStatusFailed,
		WorkflowStatusCompleted,
	}
	want := []WorkflowStatus{
		"idle",
		"running",
		"waiting_confirmation",
		"paused",
		"failed",
		"completed",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("status[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNormalWorkflowProgressWaitsForPlanningConfirmation(t *testing.T) {
	snapshot := host.UISnapshot{
		NovelName: "test novel",
		PlanningReview: &host.PlanningReviewSummary{
			Status: domain.PlanningReviewStatusPending,
		},
	}
	progress := normalWorkflowProgress("project-normal", snapshot, nil)

	if progress.Workflow != workflowNormal || progress.Status != WorkflowStatusWaitingConfirmation {
		t.Fatalf("progress = %+v", progress)
	}
	if progress.CurrentStep != "planning_review" {
		t.Fatalf("current step = %q", progress.CurrentStep)
	}
	if progress.NextAction == nil || progress.NextAction.ID != "confirm_planning" {
		t.Fatalf("next action = %+v", progress.NextAction)
	}
	if progress.NextAction.IdempotencyKey == "" {
		t.Fatal("idempotency key is empty")
	}
}

func TestContinuationWorkflowProgressUsesDurableRevision(t *testing.T) {
	continuation := &domain.ContinuationSnapshot{Workflow: domain.ContinuationWorkflow{
		Stage:           domain.ContinuationStageProposalReviewPending,
		SourceSignature: "source-signature",
		Revision:        7,
	}}
	progress := continuationWorkflowProgress("project-continuation", host.UISnapshot{}, nil, continuation)

	if progress.Workflow != workflowContinuation || progress.Revision != 7 {
		t.Fatalf("progress = %+v", progress)
	}
	if progress.Status != WorkflowStatusWaitingConfirmation || progress.CurrentStep != "proposal" {
		t.Fatalf("status/step = %q/%q", progress.Status, progress.CurrentStep)
	}
	if progress.NextAction == nil || progress.NextAction.ExpectedRevision != 7 {
		t.Fatalf("next action = %+v", progress.NextAction)
	}
	wantKey := progress.NextAction.IdempotencyKey
	again := continuationWorkflowProgress("project-continuation", host.UISnapshot{}, nil, continuation)
	if again.NextAction == nil || again.NextAction.IdempotencyKey != wantKey {
		t.Fatalf("idempotency key changed: %q -> %+v", wantKey, again.NextAction)
	}
}

func TestAdaptationEventPreservesCurrentAndTotalInAPIEvent(t *testing.T) {
	session := &ProjectSession{
		manifest:    ProjectManifest{ID: "project-adaptation"},
		hostEventAt: make(map[string]int),
		subscribers: make(map[chan WebEvent]struct{}),
	}
	event := session.appendAdaptationEvent(apiAdaptationEvent{
		Time:    time.Now().UTC(),
		Stage:   string(adapt.StageChapter),
		Current: 3,
		Total:   12,
		Message: "analyzing source chapter",
	})

	if event.Event == nil {
		t.Fatal("API host event is nil")
	}
	if event.Event.Current != 3 || event.Event.Total != 12 {
		t.Fatalf("event progress = %d/%d, want 3/12", event.Event.Current, event.Event.Total)
	}
}

func TestAdaptationWorkflowProgressPrioritizesRunningProposalOverReviewArtifacts(t *testing.T) {
	snapshot := host.UISnapshot{
		AdaptationProposal: &domain.AdaptationPlan{Status: domain.AdaptationPlanStatusProposal},
	}
	progress := adaptationWorkflowProgress(
		"project-adaptation",
		snapshot,
		nil,
		nil,
		[]string{projectActionKindAdaptationProposal},
	)

	if progress.Status != WorkflowStatusRunning || progress.CurrentStep != "proposal_review" {
		t.Fatalf("status/step = %q/%q, want running/proposal_review", progress.Status, progress.CurrentStep)
	}
	if progress.NextAction != nil {
		t.Fatalf("running proposal unexpectedly requires confirmation: %+v", progress.NextAction)
	}
	step := workflowStepByID(progress.Steps, "proposal_review")
	if step == nil || step.Status != WorkflowStatusRunning || step.Message != "正在生成改编提案" {
		t.Fatalf("proposal step = %+v", step)
	}
}

func workflowStepByID(steps []WorkflowStep, id string) *WorkflowStep {
	for i := range steps {
		if steps[i].ID == id {
			return &steps[i]
		}
	}
	return nil
}

func TestWebSnapshotSerializesWorkflowProgressWithoutDroppingLegacyFields(t *testing.T) {
	snapshot := WebSnapshot{
		UISnapshot: host.UISnapshot{NovelName: "legacy novel"},
		WorkflowProgress: WorkflowProgress{
			Workflow: workflowNormal,
			RunID:    "wf_test",
			Status:   WorkflowStatusIdle,
			Steps:    []WorkflowStep{},
		},
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := body["NovelName"]; !ok {
		t.Fatalf("legacy NovelName field missing: %s", data)
	}
	if _, ok := body["workflow_progress"]; !ok {
		t.Fatalf("workflow_progress missing: %s", data)
	}
}

func TestSnapshotSSECarriesWorkflowProgressAlongsideLegacySnapshot(t *testing.T) {
	fake := newFakeProjectHost()
	fake.snapshot = host.UISnapshot{NovelName: "sse novel"}
	session := &ProjectSession{
		manifest:    ProjectManifest{ID: "project-sse-progress"},
		host:        fake,
		actionKinds: make(map[string]int),
		hostEventAt: make(map[string]int),
		subscribers: make(map[chan WebEvent]struct{}),
	}

	event := session.AppendSnapshot()
	if _, ok := event.Snapshot.(host.UISnapshot); !ok {
		t.Fatalf("legacy snapshot type = %T", event.Snapshot)
	}
	if event.WorkflowProgress == nil || event.WorkflowProgress.Workflow != workflowNormal {
		t.Fatalf("workflow progress = %+v", event.WorkflowProgress)
	}
}
