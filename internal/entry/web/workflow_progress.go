package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
)

type WorkflowStatus string

const (
	WorkflowStatusIdle                WorkflowStatus = "idle"
	WorkflowStatusRunning             WorkflowStatus = "running"
	WorkflowStatusWaitingConfirmation WorkflowStatus = "waiting_confirmation"
	WorkflowStatusPaused              WorkflowStatus = "paused"
	WorkflowStatusFailed              WorkflowStatus = "failed"
	WorkflowStatusCompleted           WorkflowStatus = "completed"
)

const (
	workflowNormal       = "normal"
	workflowAdaptation   = "adaptation"
	workflowContinuation = "continuation"
)

// WorkflowStep is one user-visible stage in a workflow. Status uses the same
// lifecycle vocabulary as WorkflowProgress so clients need only one state
// machine for project and step rendering.
type WorkflowStep struct {
	ID      string         `json:"id"`
	Label   string         `json:"label"`
	Status  WorkflowStatus `json:"status"`
	Current int            `json:"current,omitempty"`
	Total   int            `json:"total,omitempty"`
	Message string         `json:"message,omitempty"`
}

// WorkflowNextAction carries the concurrency token a client should send with
// its next mutation. The key is deterministic for a workflow revision, making
// retries safe to identify while the persistent job layer is being introduced.
type WorkflowNextAction struct {
	ID                   string `json:"id"`
	Label                string `json:"label"`
	ExpectedRevision     int    `json:"expected_revision"`
	IdempotencyKey       string `json:"idempotency_key"`
	RequiresConfirmation bool   `json:"requires_confirmation,omitempty"`
}

type WorkflowProgress struct {
	Workflow     string              `json:"workflow"`
	RunID        string              `json:"run_id"`
	Revision     int                 `json:"revision"`
	Status       WorkflowStatus      `json:"status"`
	CurrentStep  string              `json:"current_step"`
	Steps        []WorkflowStep      `json:"steps"`
	NextAction   *WorkflowNextAction `json:"next_action,omitempty"`
	Recoverable  bool                `json:"recoverable"`
	Error        string              `json:"error,omitempty"`
	CurrentModel string              `json:"current_model,omitempty"`
}

// WebSnapshot preserves the legacy flat host snapshot while adding the
// unified workflow contract used by Web and SSE clients.
type WebSnapshot struct {
	host.UISnapshot
	WorkflowProgress WorkflowProgress `json:"workflow_progress"`
	CurrentAction    *ActionRecord    `json:"current_action,omitempty"`
}

func (s *ProjectSession) WebSnapshot() WebSnapshot {
	snapshot := s.Snapshot()
	return WebSnapshot{
		UISnapshot:       snapshot,
		WorkflowProgress: s.workflowProgress(snapshot),
		CurrentAction:    s.LatestBackgroundAction(),
	}
}

func (s *ProjectSession) WorkflowProgress() WorkflowProgress {
	return s.workflowProgress(s.Snapshot())
}

func (s *ProjectSession) workflowProgress(snapshot host.UISnapshot) WorkflowProgress {
	coCreate := s.CoCreateState()
	continuation, _ := s.ContinuationSnapshot()
	adaptationEvent := s.latestAdaptationProgressEvent()

	workflow := selectWorkflow(snapshot, coCreate, continuation, adaptationEvent, s.currentActionKinds())
	var progress WorkflowProgress
	switch workflow {
	case workflowContinuation:
		progress = continuationWorkflowProgress(s.manifest.ID, snapshot, coCreate, continuation)
	case workflowAdaptation:
		progress = adaptationWorkflowProgress(s.manifest.ID, snapshot, coCreate, adaptationEvent, s.currentActionKinds())
	default:
		progress = normalWorkflowProgress(s.manifest.ID, snapshot, coCreate)
	}
	s.attachCurrentWorkflowModel(&progress)
	return progress
}

func (s *ProjectSession) attachCurrentWorkflowModel(progress *WorkflowProgress) {
	if progress == nil || progress.Status != WorkflowStatusRunning || s.host == nil {
		return
	}
	stage := currentWorkflowModelStage(progress.CurrentStep)
	if stage == "" {
		return
	}
	_, model, _ := s.host.CurrentModelSelection(bootstrap.StageRouteKey(stage))
	progress.CurrentModel = strings.TrimSpace(model)
}

func currentWorkflowModelStage(step string) string {
	switch step {
	case "creative_intent", "clarification", "contract", "draft":
		return bootstrap.StageCoCreate
	case "source", "source_baseline", "analysis":
		return bootstrap.StageSourceAnalysis
	case "structure", "proposal", "volumes":
		return bootstrap.StageSkeleton
	case "planning_review", "proposal_review", "outlines":
		return bootstrap.StageDetailOutline
	case "writing":
		return bootstrap.StageWriting
	case "quality_audit":
		return bootstrap.StageReview
	default:
		return ""
	}
}

func selectWorkflow(
	snapshot host.UISnapshot,
	coCreate *webCoCreateState,
	continuation *domain.ContinuationSnapshot,
	adaptationEvent *APIHostEvent,
	actionKinds []string,
) string {
	if coCreate != nil {
		switch coCreate.Kind {
		case webCoCreateKindContinuation:
			return workflowContinuation
		case webCoCreateKindAdapt:
			return workflowAdaptation
		case webCoCreateKindNormal, webCoCreateKindStage:
			return workflowNormal
		}
	}
	if continuation != nil {
		return workflowContinuation
	}
	if snapshot.AdaptationPlan != nil || snapshot.AdaptationProposal != nil ||
		snapshot.AdaptationVolumeReview != nil || adaptationEvent != nil ||
		containsAdaptationAction(actionKinds) {
		return workflowAdaptation
	}
	return workflowNormal
}

func containsAdaptationAction(kinds []string) bool {
	for _, kind := range kinds {
		switch kind {
		case projectActionKindAdaptationAnalysis, projectActionKindAdaptationProposal, projectActionKindAdaptationRevision:
			return true
		}
	}
	return false
}

func normalWorkflowProgress(projectID string, snapshot host.UISnapshot, coCreate *webCoCreateState) WorkflowProgress {
	steps := []WorkflowStep{
		{ID: "creative_intent", Label: "创意输入", Status: WorkflowStatusIdle},
		{ID: "structure", Label: "篇幅与结构", Status: WorkflowStatusIdle},
		{ID: "clarification", Label: "澄清决策", Status: WorkflowStatusIdle},
		{ID: "planning_review", Label: "设定与规划审核", Status: WorkflowStatusIdle},
		{ID: "writing", Label: "正文创作", Status: WorkflowStatusIdle, Current: snapshot.CompletedCount, Total: snapshot.TotalChapters},
	}
	revision := normalWorkflowRevision(snapshot, coCreate)
	progress := WorkflowProgress{
		Workflow: workflowNormal,
		RunID:    workflowRunID(projectID, workflowNormal),
		Revision: revision,
		Status:   WorkflowStatusIdle,
		Steps:    steps,
	}

	if coCreate != nil && (coCreate.Kind == webCoCreateKindNormal || coCreate.Kind == webCoCreateKindStage) {
		progress.CurrentStep = "clarification"
		progress.Status = WorkflowStatusWaitingConfirmation
		progress.Steps = completeStepsBefore(steps, "clarification")
		progress.Steps = setStep(progress.Steps, "clarification", progress.Status, 0, 0, coCreate.BlockedReason)
		switch {
		case coCreate.Failed:
			progress.Status = WorkflowStatusFailed
			progress.Recoverable = true
			progress.Error = "普通共创生成失败，可重试后继续"
			progress.Steps = setStep(progress.Steps, "clarification", WorkflowStatusFailed, 0, 0, progress.Error)
			progress.NextAction = nextWorkflowAction(progress, "retry_cocreate", "重试共创", false)
		case coCreate.CanStart:
			progress.CurrentStep = "planning_review"
			progress.Steps = completeStepsBefore(progress.Steps, "planning_review")
			progress.Steps = setStep(progress.Steps, "planning_review", WorkflowStatusWaitingConfirmation, 0, 0, "创作方向已就绪，等待确认")
			progress.NextAction = nextWorkflowAction(progress, "commit_cocreate", "生成规划", true)
		default:
			progress.NextAction = nextWorkflowAction(progress, "continue_cocreate", "继续共创", true)
		}
		return progress
	}

	if snapshot.PlanningReview != nil && snapshot.PlanningReview.Status == domain.PlanningReviewStatusPending {
		progress.Status = WorkflowStatusWaitingConfirmation
		progress.CurrentStep = "planning_review"
		progress.Steps = completeStepsBefore(steps, progress.CurrentStep)
		progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, "规划已生成，等待确认")
		progress.NextAction = nextWorkflowAction(progress, "confirm_planning", "确认规划并开始创作", true)
		return progress
	}

	applyWritingState(&progress, snapshot)
	if progress.Status == WorkflowStatusIdle {
		progress.CurrentStep = "creative_intent"
		progress.Steps = setStep(progress.Steps, progress.CurrentStep, WorkflowStatusIdle, 0, 0, "输入创意后开始共创")
		progress.NextAction = nextWorkflowAction(progress, "begin_cocreate", "开始普通共创", false)
	}
	return progress
}

func adaptationWorkflowProgress(
	projectID string,
	snapshot host.UISnapshot,
	coCreate *webCoCreateState,
	latest *APIHostEvent,
	actionKinds []string,
) WorkflowProgress {
	steps := []WorkflowStep{
		{ID: "source", Label: "原文导入", Status: WorkflowStatusIdle},
		{ID: "analysis", Label: "原文分析", Status: WorkflowStatusIdle},
		{ID: "contract", Label: "改编契约", Status: WorkflowStatusIdle},
		{ID: "proposal_review", Label: "章节细纲与审核", Status: WorkflowStatusIdle},
		{ID: "writing", Label: "正文创作", Status: WorkflowStatusIdle, Current: snapshot.CompletedCount, Total: snapshot.TotalChapters},
		{ID: "quality_audit", Label: "质量审计", Status: WorkflowStatusIdle},
	}
	revision := adaptationWorkflowRevision(snapshot, coCreate, latest)
	progress := WorkflowProgress{
		Workflow: workflowAdaptation,
		RunID:    workflowRunID(projectID, workflowAdaptation),
		Revision: revision,
		Status:   WorkflowStatusIdle,
		Steps:    steps,
	}

	if latest != nil {
		progress.CurrentStep = "analysis"
		progress.Steps = completeStepsBefore(steps, "analysis")
		stepStatus := workflowStatusFromAdaptationEvent(*latest)
		progress.Status = stepStatus
		progress.Steps = setStep(progress.Steps, "analysis", stepStatus, latest.Current, latest.Total, latest.Summary)
		if stepStatus == WorkflowStatusFailed || stepStatus == WorkflowStatusPaused {
			progress.Recoverable = true
			progress.Error = strings.TrimSpace(latest.Detail)
			progress.NextAction = nextWorkflowAction(progress, "resume_analysis", "继续原文分析", false)
			return progress
		}
	}

	if currentStep, message, running := adaptationRunningPresentation(actionKinds); running {
		progress.Status = WorkflowStatusRunning
		progress.CurrentStep = currentStep
		progress.Steps = completeStepsBefore(progress.Steps, currentStep)
		current, total := 0, 0
		if latest != nil && latest.Total > 0 {
			current, total = latest.Current, latest.Total
			if strings.TrimSpace(latest.Summary) != "" {
				message = latest.Summary
			}
		}
		progress.Steps = setStep(progress.Steps, currentStep, progress.Status, current, total, message)
		return progress
	}

	if coCreate != nil && coCreate.Kind == webCoCreateKindAdapt {
		progress.CurrentStep = "contract"
		progress.Status = WorkflowStatusWaitingConfirmation
		progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
		progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, coCreate.BlockedReason)
		switch {
		case coCreate.Failed:
			progress.Status = WorkflowStatusFailed
			progress.Recoverable = true
			progress.Error = "改编共创生成失败，可重试后继续"
			progress.Steps = setStep(progress.Steps, progress.CurrentStep, WorkflowStatusFailed, 0, 0, progress.Error)
			progress.NextAction = nextWorkflowAction(progress, "retry_cocreate", "重试改编共创", false)
		case coCreate.CanStart:
			progress.NextAction = nextWorkflowAction(progress, "commit_adaptation_contract", "确认契约并生成提案", true)
		default:
			progress.NextAction = nextWorkflowAction(progress, "continue_adaptation_cocreate", "继续改编共创", true)
		}
		return progress
	}

	proposal := snapshot.AdaptationProposal
	if proposal == nil {
		proposal = snapshot.AdaptationPlan
	}
	if snapshot.AdaptationVolumeReview != nil || (proposal != nil && proposal.Status != domain.AdaptationPlanStatusConfirmed) {
		progress.CurrentStep = "proposal_review"
		progress.Status = WorkflowStatusWaitingConfirmation
		progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
		progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, "改编提案已生成，等待确认")
		progress.NextAction = nextWorkflowAction(progress, "confirm_adaptation_proposal", "确认改编提案", true)
		return progress
	}

	if proposal != nil && proposal.Status == domain.AdaptationPlanStatusConfirmed {
		progress.Steps = completeStepsBefore(progress.Steps, "writing")
		applyWritingState(&progress, snapshot)
		if progress.Status == WorkflowStatusCompleted {
			progress.CurrentStep = "quality_audit"
			progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
			progress.Steps = setStep(progress.Steps, progress.CurrentStep, WorkflowStatusCompleted, 1, 1, "改编创作与质量检查已完成")
		}
		return progress
	}

	if latest != nil && latest.Kind == "done" {
		progress.Status = WorkflowStatusWaitingConfirmation
		progress.CurrentStep = "contract"
		progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
		progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, "原文分析完成，等待确定改编契约")
		progress.NextAction = nextWorkflowAction(progress, "begin_adaptation_cocreate", "开始改编共创", false)
		return progress
	}

	progress.CurrentStep = "source"
	progress.NextAction = nextWorkflowAction(progress, "upload_adaptation_source", "上传原文", false)
	return progress
}

func adaptationRunningPresentation(actionKinds []string) (string, string, bool) {
	switch {
	case workflowContainsString(actionKinds, projectActionKindAdaptationRevision):
		return "proposal_review", "正在修订改编提案", true
	case workflowContainsString(actionKinds, projectActionKindAdaptationProposal):
		return "proposal_review", "正在生成并逐批审核章节细纲", true
	case workflowContainsString(actionKinds, projectActionKindAdaptationAnalysis):
		return "analysis", "正在分析原文", true
	default:
		return "", "", false
	}
}

func continuationWorkflowProgress(
	projectID string,
	snapshot host.UISnapshot,
	coCreate *webCoCreateState,
	continuation *domain.ContinuationSnapshot,
) WorkflowProgress {
	steps := []WorkflowStep{
		{ID: "source_baseline", Label: "原作基线", Status: WorkflowStatusIdle},
		{ID: "draft", Label: "续写方向", Status: WorkflowStatusIdle},
		{ID: "proposal", Label: "续写提案", Status: WorkflowStatusIdle},
		{ID: "volumes", Label: "分卷规划", Status: WorkflowStatusIdle},
		{ID: "outlines", Label: "章节细纲", Status: WorkflowStatusIdle},
		{ID: "writing", Label: "续写正文", Status: WorkflowStatusIdle, Current: snapshot.CompletedCount, Total: snapshot.TotalChapters},
	}
	if continuation == nil {
		progress := WorkflowProgress{
			Workflow:    workflowContinuation,
			RunID:       workflowRunID(projectID, workflowContinuation, "uninitialized"),
			Status:      WorkflowStatusIdle,
			CurrentStep: "source_baseline",
			Steps:       steps,
		}
		progress.NextAction = nextWorkflowAction(progress, "upload_continuation_source", "导入原作", false)
		return progress
	}

	workflow := continuation.Workflow
	progress := WorkflowProgress{
		Workflow: workflowContinuation,
		RunID:    workflowRunID(projectID, workflowContinuation, workflow.SourceSignature),
		Revision: workflow.Revision,
		Status:   WorkflowStatusIdle,
		Steps:    steps,
	}
	if coCreate != nil && coCreate.Kind == webCoCreateKindContinuation {
		progress.CurrentStep = "draft"
		progress.Status = WorkflowStatusWaitingConfirmation
		progress.Steps = completeStepsBefore(steps, progress.CurrentStep)
		progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, coCreate.BlockedReason)
		switch {
		case coCreate.Failed:
			progress.Status = WorkflowStatusFailed
			progress.Recoverable = true
			progress.Error = "续写方向生成失败，可重试后继续"
			progress.Steps = setStep(progress.Steps, progress.CurrentStep, WorkflowStatusFailed, 0, 0, progress.Error)
			progress.NextAction = nextWorkflowAction(progress, "retry_cocreate", "重试续写共创", false)
		case coCreate.CanStart:
			progress.NextAction = nextWorkflowAction(progress, "commit_continuation_draft", "确认续写方向", true)
		default:
			progress.NextAction = nextWorkflowAction(progress, "continue_continuation_cocreate", "继续续写共创", true)
		}
		return progress
	}

	currentStep, status, message, nextID, nextLabel, confirmation := continuationStagePresentation(workflow)
	progress.CurrentStep = currentStep
	progress.Status = status
	progress.Steps = completeStepsBefore(steps, currentStep)
	progress.Steps = setStep(progress.Steps, currentStep, status, continuationWritingCurrent(snapshot, workflow), continuationWritingTotal(snapshot, workflow), message)
	progress.Error = strings.TrimSpace(workflow.LastError)
	progress.Recoverable = status == WorkflowStatusPaused || status == WorkflowStatusFailed
	if nextID != "" {
		progress.NextAction = nextWorkflowAction(progress, nextID, nextLabel, confirmation)
	}
	return progress
}

func continuationStagePresentation(workflow domain.ContinuationWorkflow) (string, WorkflowStatus, string, string, string, bool) {
	switch workflow.Stage {
	case domain.ContinuationStageSourceReady:
		return "source_baseline", WorkflowStatusWaitingConfirmation, "原作已导入，等待确定续写方向", "begin_continuation_draft", "开始续写共创", false
	case domain.ContinuationStageDraftCollecting:
		return "draft", WorkflowStatusWaitingConfirmation, "正在确定续写方向", "continue_continuation_cocreate", "继续续写共创", true
	case domain.ContinuationStageProposalGenerating:
		return "proposal", WorkflowStatusRunning, "正在生成续写提案", "", "", false
	case domain.ContinuationStageProposalReviewPending:
		return "proposal", WorkflowStatusWaitingConfirmation, "续写提案等待确认", "approve_continuation_proposal", "确认续写提案", true
	case domain.ContinuationStageVolumeReviewPending:
		return "volumes", WorkflowStatusWaitingConfirmation, "分卷规划等待确认", "approve_continuation_volumes", "确认分卷规划", true
	case domain.ContinuationStageOutlineGenerating:
		return "outlines", WorkflowStatusRunning, "正在生成章节细纲", "", "", false
	case domain.ContinuationStageOutlineReviewPending:
		return "outlines", WorkflowStatusWaitingConfirmation, "章节细纲等待确认", "approve_continuation_outlines", "确认章节细纲", true
	case domain.ContinuationStageReadyToWrite:
		return "writing", WorkflowStatusWaitingConfirmation, "续写规划已就绪，等待开始创作", "start_continuation", "开始续写", true
	case domain.ContinuationStageWriting:
		return "writing", WorkflowStatusRunning, "正在创作续写正文", "", "", false
	case domain.ContinuationStagePaused:
		return continuationResumeStep(workflow.ResumeStage), WorkflowStatusPaused, "续写流程已暂停", "retry_continuation", "继续续写流程", false
	case domain.ContinuationStageFailed:
		return continuationResumeStep(workflow.ResumeStage), WorkflowStatusFailed, "续写流程失败，可从检查点重试", "retry_continuation", "重试续写流程", false
	default:
		return "source_baseline", WorkflowStatusIdle, "", "upload_continuation_source", "导入原作", false
	}
}

func continuationResumeStep(stage domain.ContinuationStage) string {
	switch stage {
	case domain.ContinuationStageDraftCollecting:
		return "draft"
	case domain.ContinuationStageVolumeReviewPending:
		return "volumes"
	case domain.ContinuationStageOutlineGenerating, domain.ContinuationStageOutlineReviewPending:
		return "outlines"
	case domain.ContinuationStageReadyToWrite, domain.ContinuationStageWriting:
		return "writing"
	default:
		return "proposal"
	}
}

func applyWritingState(progress *WorkflowProgress, snapshot host.UISnapshot) {
	if progress == nil {
		return
	}
	progress.CurrentStep = "writing"
	status := WorkflowStatusIdle
	message := ""
	switch {
	case snapshot.RuntimeState == "completed" || snapshot.Phase == string(domain.PhaseComplete):
		status = WorkflowStatusCompleted
		message = "创作已完成"
	case snapshot.RuntimeState == "paused" || snapshot.RuntimeState == "pausing":
		status = WorkflowStatusPaused
		message = "创作已暂停，可从检查点继续"
		progress.Recoverable = true
	case snapshot.IsRunning || snapshot.RuntimeState == "running":
		status = WorkflowStatusRunning
		message = "正在创作正文"
	case snapshot.Phase == string(domain.PhaseWriting):
		status = WorkflowStatusPaused
		message = "创作可继续"
		progress.Recoverable = true
	}
	progress.Status = status
	progress.Steps = setStep(progress.Steps, "writing", status, snapshot.CompletedCount, snapshot.TotalChapters, message)
	if status == WorkflowStatusPaused {
		progress.NextAction = nextWorkflowAction(*progress, "resume_writing", "继续创作", false)
	}
}

func nextWorkflowAction(progress WorkflowProgress, id, label string, confirmation bool) *WorkflowNextAction {
	action := &WorkflowNextAction{
		ID:                   id,
		Label:                label,
		ExpectedRevision:     progress.Revision,
		RequiresConfirmation: confirmation,
	}
	action.IdempotencyKey = workflowIdempotencyKey(progress.RunID, progress.Revision, id)
	return action
}

func completeStepsBefore(steps []WorkflowStep, current string) []WorkflowStep {
	out := append([]WorkflowStep(nil), steps...)
	for i := range out {
		if out[i].ID == current {
			break
		}
		out[i].Status = WorkflowStatusCompleted
	}
	return out
}

func setStep(steps []WorkflowStep, id string, status WorkflowStatus, current, total int, message string) []WorkflowStep {
	out := append([]WorkflowStep(nil), steps...)
	for i := range out {
		if out[i].ID != id {
			continue
		}
		out[i].Status = status
		out[i].Current = current
		out[i].Total = total
		out[i].Message = strings.TrimSpace(message)
		break
	}
	return out
}

func normalWorkflowRevision(snapshot host.UISnapshot, coCreate *webCoCreateState) int {
	revision := snapshot.CompletedCount
	if snapshot.PlanningReview != nil {
		revision++
	}
	if coCreate != nil {
		revision += len(coCreate.Messages) + 1
	}
	return revision
}

func adaptationWorkflowRevision(snapshot host.UISnapshot, coCreate *webCoCreateState, event *APIHostEvent) int {
	revision := snapshot.CompletedCount
	if coCreate != nil {
		revision += len(coCreate.Messages) + 1
	}
	if event != nil {
		revision += event.Current
		if event.Total > 0 {
			revision++
		}
	}
	if snapshot.AdaptationProposal != nil || snapshot.AdaptationPlan != nil {
		revision++
	}
	return revision
}

func workflowRunID(projectID, workflow string, identity ...string) string {
	parts := append([]string{projectID, workflow}, identity...)
	return "wf_" + shortWorkflowHash(parts...)
}

func workflowIdempotencyKey(runID string, revision int, action string) string {
	return "idem_" + shortWorkflowHash(runID, fmt.Sprintf("%d", revision), action)
}

func shortWorkflowHash(parts ...string) string {
	canonical := make([]string, 0, len(parts))
	for _, part := range parts {
		canonical = append(canonical, strings.TrimSpace(part))
	}
	sum := sha256.Sum256([]byte(strings.Join(canonical, "\x00")))
	return hex.EncodeToString(sum[:8])
}

func workflowStatusFromAdaptationEvent(event APIHostEvent) WorkflowStatus {
	if event.Failed || event.Level == "error" || event.Kind == "error" {
		return WorkflowStatusFailed
	}
	switch event.Kind {
	case "paused":
		return WorkflowStatusPaused
	case "done":
		return WorkflowStatusCompleted
	default:
		return WorkflowStatusRunning
	}
}

func continuationWritingCurrent(snapshot host.UISnapshot, workflow domain.ContinuationWorkflow) int {
	if workflow.Stage != domain.ContinuationStageWriting {
		return 0
	}
	completed := snapshot.CompletedCount - workflow.BaseChapterCount
	if completed < 0 {
		return 0
	}
	return completed
}

func continuationWritingTotal(snapshot host.UISnapshot, workflow domain.ContinuationWorkflow) int {
	if workflow.Stage != domain.ContinuationStageWriting || snapshot.TotalChapters <= workflow.BaseChapterCount {
		return 0
	}
	return snapshot.TotalChapters - workflow.BaseChapterCount
}

func workflowContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (s *ProjectSession) latestAdaptationProgressEvent() *APIHostEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.history) - 1; i >= 0; i-- {
		event := s.history[i].Event
		if event == nil || event.Category != "ADAPT" {
			continue
		}
		copy := *event
		return &copy
	}
	return nil
}
