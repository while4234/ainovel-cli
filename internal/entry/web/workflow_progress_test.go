package web

import (
	"encoding/json"
	"strings"
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
			Kind:   domain.PlanningReviewKindVolumeSplit,
		},
	}
	progress := normalWorkflowProgress("project-normal", snapshot, nil)

	if progress.Workflow != workflowNormal || progress.Status != WorkflowStatusWaitingConfirmation {
		t.Fatalf("progress = %+v", progress)
	}
	if progress.CurrentStep != "volume_plan" {
		t.Fatalf("current step = %q", progress.CurrentStep)
	}
	if progress.NextAction == nil || progress.NextAction.ID != "confirm_planning" {
		t.Fatalf("next action = %+v", progress.NextAction)
	}
	if progress.NextAction.IdempotencyKey == "" {
		t.Fatal("idempotency key is empty")
	}
}

func TestNormalWorkflowProgressPlanningReviewSupersedesStaleCoCreateFailure(t *testing.T) {
	snapshot := host.UISnapshot{PlanningReview: &host.PlanningReviewSummary{
		Status: domain.PlanningReviewStatusPending,
		Kind:   domain.PlanningReviewKindChapterOutline,
	}}
	coCreate := &webCoCreateState{
		Kind:   webCoCreateKindNormal,
		Failed: true,
	}

	progress := normalWorkflowProgress("project-normal", snapshot, coCreate)

	if progress.Status != WorkflowStatusWaitingConfirmation || progress.CurrentStep != "chapter_outline" {
		t.Fatalf("status/step = %q/%q", progress.Status, progress.CurrentStep)
	}
	if progress.NextAction == nil || progress.NextAction.ID != "confirm_planning" {
		t.Fatalf("next action = %+v", progress.NextAction)
	}
	if progress.Error != "" || progress.Recoverable {
		t.Fatalf("stale co-create failure leaked into planning progress: %+v", progress)
	}
}

func TestWorkflowProgressReportsTheRunningStageModel(t *testing.T) {
	session, err := NewProjectSession(ProjectManifest{ID: "project-model-progress"}, newFakeProjectHost())
	if err != nil {
		t.Fatalf("NewProjectSession: %v", err)
	}
	defer session.Close()

	progress := session.workflowProgress(host.UISnapshot{
		IsRunning:      true,
		RuntimeState:   "running",
		TotalChapters:  10,
		CompletedCount: 2,
	})
	if progress.CurrentStep != "writing" || progress.CurrentModel != "model-a" {
		t.Fatalf("running progress = step %q model %q, want writing/model-a", progress.CurrentStep, progress.CurrentModel)
	}
}

func TestNormalWorkflowProgressCompletesPrerequisitesAfterWritingStarts(t *testing.T) {
	tests := []struct {
		name           string
		snapshot       host.UISnapshot
		expectedStatus WorkflowStatus
	}{
		{
			name: "running",
			snapshot: host.UISnapshot{
				IsRunning:     true,
				RuntimeState:  "running",
				Phase:         string(domain.PhaseWriting),
				TotalChapters: 12,
			},
			expectedStatus: WorkflowStatusRunning,
		},
		{
			name: "paused",
			snapshot: host.UISnapshot{
				RuntimeState:  "paused",
				Phase:         string(domain.PhaseWriting),
				TotalChapters: 12,
			},
			expectedStatus: WorkflowStatusPaused,
		},
		{
			name: "completed",
			snapshot: host.UISnapshot{
				RuntimeState:   "completed",
				Phase:          string(domain.PhaseComplete),
				CompletedCount: 12,
				TotalChapters:  12,
			},
			expectedStatus: WorkflowStatusCompleted,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			progress := normalWorkflowProgress("project-writing", test.snapshot, nil)
			if progress.CurrentStep != "writing" || progress.Status != test.expectedStatus {
				t.Fatalf("progress step/status = %q/%q, want writing/%q", progress.CurrentStep, progress.Status, test.expectedStatus)
			}
			for _, stepID := range []string{"creative_intent", "structure", "clarification", "volume_plan", "chapter_outline"} {
				step := workflowStepByID(progress.Steps, stepID)
				if step == nil || step.Status != WorkflowStatusCompleted {
					t.Fatalf("prerequisite step %q = %+v, want completed", stepID, step)
				}
			}
			writing := workflowStepByID(progress.Steps, "writing")
			if writing == nil || writing.Status != test.expectedStatus {
				t.Fatalf("writing step = %+v, want status %q", writing, test.expectedStatus)
			}
		})
	}
}

func TestNormalWorkflowProgressKeepsNewProjectStepsIdle(t *testing.T) {
	progress := normalWorkflowProgress("project-new", host.UISnapshot{}, nil)
	if progress.CurrentStep != "creative_intent" || progress.Status != WorkflowStatusIdle {
		t.Fatalf("new project progress = %+v", progress)
	}
	for _, step := range progress.Steps {
		if step.Status != WorkflowStatusIdle {
			t.Fatalf("new project step %q status = %q, want idle", step.ID, step.Status)
		}
	}
}

func TestNormalWorkflowProgressDoesNotTreatBackgroundActionAsWriting(t *testing.T) {
	snapshot := host.UISnapshot{
		IsRunning:    true,
		RuntimeState: "running",
		Agents: []host.AgentSnapshot{{
			TaskKind: projectActionKindSimulationAnalysis,
			State:    "running",
		}},
	}

	progress := normalWorkflowProgress("project-simulation-analysis", snapshot, nil)

	if progress.CurrentStep != "creative_intent" || progress.Status != WorkflowStatusIdle {
		t.Fatalf("background analysis progress = %+v, want idle creative intent", progress)
	}
	for _, step := range progress.Steps {
		if step.Status != WorkflowStatusIdle {
			t.Fatalf("background analysis step %q status = %q, want idle", step.ID, step.Status)
		}
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
	if step == nil || step.Status != WorkflowStatusRunning || step.Message != "正在生成并逐批审核章节细纲" {
		t.Fatalf("proposal step = %+v", step)
	}
}

func TestAdaptationWorkflowProgressKeepsDetailAuditFailureInProposalStage(t *testing.T) {
	snapshot := host.UISnapshot{
		AdaptationVolumeReview: &domain.AdaptationVolumeReview{
			Status: domain.AdaptationPlanStatusVolumeReview,
		},
	}
	latest := &APIHostEvent{
		Category: "ADAPT",
		Kind:     string(adapt.StageAudit),
		Level:    "error",
		Current:  123,
		Total:    370,
		Summary:  "章节详情审核失败",
		Detail:   "duplicate source event owner",
	}
	progress := adaptationWorkflowProgress("project-adaptation", snapshot, nil, latest, nil)

	if progress.Status != WorkflowStatusFailed || progress.CurrentStep != "proposal_review" {
		t.Fatalf("status/step=%q/%q, want failed/proposal_review", progress.Status, progress.CurrentStep)
	}
	step := workflowStepByID(progress.Steps, "proposal_review")
	if step == nil || step.Status != WorkflowStatusFailed || step.Current != 123 || step.Total != 370 {
		t.Fatalf("proposal step=%+v", step)
	}
	if progress.NextAction == nil || progress.NextAction.ID != "resume_adaptation_proposal_details" || progress.NextAction.Label != "继续章节详细提案" {
		t.Fatalf("next action=%+v", progress.NextAction)
	}
	if progress.Error != latest.Detail {
		t.Fatalf("error=%q, want %q", progress.Error, latest.Detail)
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
	fake.snapshot = host.UISnapshot{
		NovelName:        "sse novel",
		PremiseFull:      strings.Repeat("正文", projectSnapshotSummaryRunes+1),
		CharacterDetails: []domain.Character{{Name: "角色"}},
		Outline: []host.OutlineSnapshot{{
			Chapter:   1,
			CoreEvent: strings.Repeat("事件", projectSnapshotSummaryRunes+1),
			Scenes:    []string{"场景一", "场景二", "场景三"},
		}},
	}
	session := &ProjectSession{
		manifest:    ProjectManifest{ID: "project-sse-progress"},
		host:        fake,
		actionKinds: make(map[string]int),
		hostEventAt: make(map[string]int),
		subscribers: make(map[chan WebEvent]struct{}),
	}

	event := session.AppendSnapshot()
	snapshot, ok := event.Snapshot.(host.UISnapshot)
	if !ok {
		t.Fatalf("legacy snapshot type = %T", event.Snapshot)
	}
	if snapshot.PremiseFull != "" || len(snapshot.CharacterDetails) != 0 {
		t.Fatalf("SSE snapshot retained heavyweight fields: %+v", snapshot)
	}
	if len(snapshot.Outline) != 1 || len(snapshot.Outline[0].Scenes) != 0 ||
		snapshot.Outline[0].CoreEvent != "" {
		t.Fatalf("SSE outline was not compacted: %+v", snapshot.Outline)
	}
	if event.WorkflowProgress == nil || event.WorkflowProgress.Workflow != workflowNormal {
		t.Fatalf("workflow progress = %+v", event.WorkflowProgress)
	}
}
