package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const rollbackLogFile = "meta/rollback_log.jsonl"

type rollbackState struct {
	progress       *domain.Progress
	review         *domain.PlanningReview
	plan           *domain.AdaptationPlan
	proposal       *domain.AdaptationPlan
	volumeReview   *domain.AdaptationVolumeReview
	runtime        *domain.AdaptationProposalRuntime
	sourceManifest *domain.AdaptationSourceManifest
	flatOutline    []domain.OutlineEntry
	layeredOutline []domain.VolumeOutline
	premise        string
	meta           *domain.RunMeta
}

func (s *Store) RollbackPreview() (domain.RollbackPreview, error) {
	state, err := s.inspectRollbackState()
	if err != nil {
		return domain.RollbackPreview{}, err
	}
	return domain.RollbackPreviewWithHash(state.preview()), nil
}

func (s *Store) Rollback(req domain.RollbackRequest) (domain.RollbackResult, error) {
	if !req.Confirm {
		return domain.RollbackResult{}, fmt.Errorf("rollback confirmation is required")
	}
	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	state, err := s.inspectRollbackState()
	if err != nil {
		return domain.RollbackResult{}, err
	}
	preview := domain.RollbackPreviewWithHash(state.preview())
	if !preview.CanRollback {
		return domain.RollbackResult{Preview: preview}, fmt.Errorf("project cannot roll back: %s", preview.Reason)
	}
	if strings.TrimSpace(req.PreviewHash) != "" && req.PreviewHash != preview.PreviewHash {
		return domain.RollbackResult{Preview: preview}, fmt.Errorf("rollback preview expired; refresh and confirm again")
	}

	deleted, err := s.executeRollback(preview.TargetStage, state)
	if err != nil {
		return domain.RollbackResult{Preview: preview, DeletedPaths: deleted}, err
	}
	if err := s.Init(); err != nil {
		return domain.RollbackResult{Preview: preview, DeletedPaths: deleted}, fmt.Errorf("recreate project directories: %w", err)
	}
	if err := s.appendRollbackLog(preview, deleted); err != nil {
		return domain.RollbackResult{Preview: preview, DeletedPaths: deleted}, fmt.Errorf("append rollback log: %w", err)
	}
	return domain.RollbackResult{Preview: preview, DeletedPaths: deleted}, nil
}

func (s *Store) inspectRollbackState() (rollbackState, error) {
	var state rollbackState
	var err error
	if state.progress, err = s.Progress.Load(); err != nil {
		return state, fmt.Errorf("load progress: %w", err)
	}
	if state.review, err = s.RunMeta.PlanningReview(); err != nil {
		return state, fmt.Errorf("load planning review: %w", err)
	}
	if state.meta, err = s.RunMeta.Load(); err != nil {
		return state, fmt.Errorf("load run meta: %w", err)
	}
	if state.plan, err = s.Adaptation.LoadPlan(); err != nil {
		return state, fmt.Errorf("load adaptation plan: %w", err)
	}
	if state.proposal, err = s.Adaptation.LoadProposal(); err != nil {
		return state, fmt.Errorf("load adaptation proposal: %w", err)
	}
	if state.volumeReview, err = s.Adaptation.LoadVolumeReview(); err != nil {
		return state, fmt.Errorf("load adaptation volume review: %w", err)
	}
	if state.runtime, err = s.Adaptation.LoadProposalRuntime(); err != nil {
		return state, fmt.Errorf("load adaptation proposal runtime: %w", err)
	}
	if state.sourceManifest, err = s.Adaptation.LoadSourceManifest(); err != nil {
		return state, fmt.Errorf("load adaptation source manifest: %w", err)
	}
	if state.flatOutline, err = s.Outline.LoadOutline(); err != nil {
		return state, fmt.Errorf("load outline: %w", err)
	}
	if state.layeredOutline, err = s.Outline.LoadLayeredOutline(); err != nil {
		return state, fmt.Errorf("load layered outline: %w", err)
	}
	if state.premise, err = s.Outline.LoadPremise(); err != nil {
		return state, fmt.Errorf("load premise: %w", err)
	}
	return state, nil
}

func (state rollbackState) preview() domain.RollbackPreview {
	if state.hasAdaptationState() {
		return state.adaptationPreview()
	}
	return state.normalPreview()
}

func (state rollbackState) hasAdaptationState() bool {
	return state.plan != nil ||
		state.proposal != nil ||
		state.volumeReview != nil ||
		state.runtime != nil ||
		state.sourceManifest != nil
}

func (state rollbackState) adaptationPreview() domain.RollbackPreview {
	switch {
	case state.plan != nil:
		return state.readyPreview("adaptation", "writing", domain.RollbackStageProposal,
			"改编提案完成待审核",
			adaptationWritingDeletePaths(),
			[]string{"meta/adaptation/proposal.json", "meta/adaptation/source_*", "uploads/"})
	case state.proposal != nil || state.runtime != nil:
		if state.volumeReview != nil {
			return state.readyPreview("adaptation", "chapter_outline", domain.RollbackStageVolumeOutline,
				"分卷提纲完成待审核",
				[]string{adaptationProposalFile, adaptationProposalRuntimeFile},
				[]string{adaptationVolumeReviewFile, "meta/adaptation/source_*", "uploads/"})
		}
		return state.readyPreview("adaptation", "proposal", domain.RollbackStageDraft,
			"共创 draft",
			adaptationGeneratedDeletePaths(),
			[]string{"meta/adaptation/source_*", "meta/adaptation/cocreate_*", "uploads/"})
	case state.volumeReview != nil:
		return state.readyPreview("adaptation", "volume_outline", domain.RollbackStageDraft,
			"共创 draft",
			adaptationGeneratedDeletePaths(),
			[]string{"meta/adaptation/source_*", "meta/adaptation/cocreate_*", "uploads/"})
	case state.sourceManifest != nil:
		return state.readyPreview("adaptation", "draft", domain.RollbackStageBlank,
			"空白项目",
			blankDeletePaths(),
			[]string{"project manifest", "style/config", "uploads/"})
	default:
		return state.blockedPreview("adaptation", "blank", "当前项目没有可回退的改编阶段")
	}
}

func (state rollbackState) normalPreview() domain.RollbackPreview {
	if state.hasWritingProgress() {
		if state.hasDetailedOutline() {
			return state.readyPreview("normal", "writing", domain.RollbackStageChapterOutline,
				"详细章节提纲完成待审核",
				writingDeletePaths(),
				[]string{"premise.md", "outline.json", "layered_outline.json", "characters.json", "world_rules.json"})
		}
		return state.blockedPreview("normal", "writing", "缺少可回退到的章节提纲")
	}
	if state.hasDetailedOutline() {
		if len(state.layeredOutline) > 0 {
			return state.readyPreview("normal", "chapter_outline", domain.RollbackStageVolumeOutline,
				"分卷提纲完成待审核",
				[]string{"outline.json", "outline.md", "layered_outline.json 中的章节细纲"},
				[]string{"layered_outline.json", "premise.md", "characters.json", "world_rules.json"})
		}
		return state.readyPreview("normal", "chapter_outline", domain.RollbackStageDraft,
			"共创 draft",
			foundationDeletePaths(),
			[]string{"meta/run.json 中的 planning_review"})
	}
	if len(state.layeredOutline) > 0 || state.hasFoundation() {
		return state.readyPreview("normal", "volume_outline", domain.RollbackStageDraft,
			"共创 draft",
			foundationDeletePaths(),
			[]string{"meta/run.json 中的 planning_review"})
	}
	if state.review != nil && strings.TrimSpace(state.review.Brief) != "" {
		return state.readyPreview("normal", "draft", domain.RollbackStageBlank,
			"空白项目",
			blankDeletePaths(),
			[]string{"project manifest", "style/config", "uploads/"})
	}
	return state.blockedPreview("normal", "blank", "当前项目已经是空白状态")
}

func (state rollbackState) readyPreview(mode, current string, target domain.RollbackStage, label string, deletePaths, preservePaths []string) domain.RollbackPreview {
	return domain.RollbackPreview{
		CanRollback:    true,
		Mode:           mode,
		CurrentStage:   current,
		TargetStage:    target,
		TargetLabel:    label,
		Warning:        "回退会删除目标阶段之后产生的项目文件，此操作不可撤销。",
		DeletePaths:    append([]string(nil), deletePaths...),
		PreservePaths:  append([]string(nil), preservePaths...),
		StateSignature: state.signature(),
	}
}

func (state rollbackState) blockedPreview(mode, current, reason string) domain.RollbackPreview {
	return domain.RollbackPreview{
		CanRollback:    false,
		Mode:           mode,
		CurrentStage:   current,
		Reason:         reason,
		StateSignature: state.signature(),
	}
}

func (state rollbackState) hasWritingProgress() bool {
	if state.progress == nil {
		return false
	}
	return state.progress.Phase == domain.PhaseWriting ||
		state.progress.Phase == domain.PhaseComplete ||
		state.progress.InProgressChapter > 0 ||
		len(state.progress.CompletedChapters) > 0
}

func (state rollbackState) hasDetailedOutline() bool {
	return len(state.flatOutline) > 0 || layeredOutlineHasExpandedArcs(state.layeredOutline)
}

func (state rollbackState) hasFoundation() bool {
	return strings.TrimSpace(state.premise) != "" ||
		len(state.flatOutline) > 0 ||
		len(state.layeredOutline) > 0
}

func (state rollbackState) signature() string {
	progress := "nil"
	if state.progress != nil {
		progress = fmt.Sprintf("%s:%s:%d:%d:%d:%d",
			state.progress.Phase,
			state.progress.Flow,
			state.progress.CurrentChapter,
			state.progress.InProgressChapter,
			state.progress.TotalChapters,
			len(state.progress.CompletedChapters),
		)
	}
	review := "nil"
	if state.review != nil {
		review = fmt.Sprintf("%s:%s:%s:%d", state.review.Status, state.review.Kind, state.review.UpdatedAt, len(state.review.Brief))
	}
	return strings.Join([]string{
		"progress=" + progress,
		"review=" + review,
		fmt.Sprintf("plan=%t:%d", state.plan != nil, adaptationPlanChapterCount(state.plan)),
		fmt.Sprintf("proposal=%t:%d", state.proposal != nil, adaptationPlanChapterCount(state.proposal)),
		fmt.Sprintf("volume=%t:%d", state.volumeReview != nil, adaptationVolumeTargetCount(state.volumeReview)),
		fmt.Sprintf("runtime=%t:%d:%d", state.runtime != nil, adaptationRuntimeTargetCount(state.runtime), adaptationRuntimeCompletedCount(state.runtime)),
		fmt.Sprintf("source=%t", state.sourceManifest != nil),
		fmt.Sprintf("outline=%d:%d:%t", len(state.flatOutline), len(state.layeredOutline), layeredOutlineHasExpandedArcs(state.layeredOutline)),
	}, ";")
}

func (s *Store) executeRollback(target domain.RollbackStage, state rollbackState) ([]string, error) {
	switch target {
	case domain.RollbackStageProposal:
		return s.rollbackToAdaptationProposal(state)
	case domain.RollbackStageChapterOutline:
		return s.rollbackToChapterOutline(state)
	case domain.RollbackStageVolumeOutline:
		return s.rollbackToVolumeOutline(state)
	case domain.RollbackStageDraft:
		return s.rollbackToDraft(state)
	case domain.RollbackStageBlank:
		return s.rollbackToBlank()
	default:
		return nil, fmt.Errorf("unsupported rollback target %q", target)
	}
}

func (s *Store) rollbackToAdaptationProposal(state rollbackState) ([]string, error) {
	source := state.proposal
	if source == nil {
		source = state.plan
	}
	if source == nil || len(source.Chapters) == 0 {
		return nil, fmt.Errorf("adaptation proposal cannot be restored")
	}
	proposal := *source
	proposal.Status = domain.AdaptationPlanStatusProposal
	if err := s.Adaptation.SaveProposal(proposal); err != nil {
		return nil, fmt.Errorf("restore adaptation proposal: %w", err)
	}
	deleted, err := s.removeWritingArtifacts()
	if err != nil {
		return deleted, err
	}
	if removed, err := s.removePaths(adaptationConfirmedDeletePaths()); err != nil {
		return deleted, err
	} else {
		deleted = append(deleted, removed...)
	}
	if removed, err := s.removePaths(foundationDeletePaths()); err != nil {
		return deleted, err
	} else {
		deleted = append(deleted, removed...)
	}
	if err := s.savePlanningProgress(domain.PhaseOutline, len(proposal.Chapters), false, state); err != nil {
		return deleted, err
	}
	return deleted, nil
}

func (s *Store) rollbackToChapterOutline(state rollbackState) ([]string, error) {
	if !state.hasDetailedOutline() {
		return nil, fmt.Errorf("chapter outline is missing")
	}
	deleted, err := s.removeWritingArtifacts()
	if err != nil {
		return deleted, err
	}
	if err := s.ensureNormalPlanningReview(domain.PlanningReviewKindChapterOutline, state); err != nil {
		return deleted, err
	}
	total := len(state.flatOutline)
	layered := len(state.layeredOutline) > 0
	if total == 0 && layered {
		total = domain.TotalChapters(state.layeredOutline)
	}
	if err := s.savePlanningProgress(domain.PhaseOutline, total, layered, state); err != nil {
		return deleted, err
	}
	return deleted, nil
}

func (s *Store) rollbackToVolumeOutline(state rollbackState) ([]string, error) {
	if state.volumeReview != nil || state.proposal != nil || state.runtime != nil || state.plan != nil {
		deleted, err := s.removePaths([]string{adaptationProposalFile, adaptationProposalRuntimeFile, adaptationPlanFile, adaptationPlanningWorkflowFile})
		if err != nil {
			return deleted, err
		}
		if removed, err := s.removePaths([]string{adaptationCheckDir}); err != nil {
			return deleted, err
		} else {
			deleted = append(deleted, removed...)
		}
		if err := s.savePlanningProgress(domain.PhaseOutline, adaptationVolumeTargetCount(state.volumeReview), true, state); err != nil {
			return deleted, err
		}
		if state.volumeReview != nil {
			if _, err := s.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageVolumeReviewPending, 0); err != nil {
				return deleted, err
			}
		}
		return deleted, nil
	}
	if len(state.layeredOutline) == 0 {
		return nil, fmt.Errorf("volume outline is missing")
	}
	collapsed := collapseLayeredOutline(state.layeredOutline)
	if err := s.Outline.SaveLayeredOutline(collapsed); err != nil {
		return nil, fmt.Errorf("collapse layered outline: %w", err)
	}
	deleted, err := s.removeWritingArtifacts()
	if err != nil {
		return deleted, err
	}
	if removed, err := s.removePaths([]string{"outline.json", "outline.md"}); err != nil {
		return deleted, err
	} else {
		deleted = append(deleted, removed...)
	}
	if err := s.ensureNormalPlanningReview(domain.PlanningReviewKindVolumeSplit, state); err != nil {
		return deleted, err
	}
	if err := s.savePlanningProgress(domain.PhaseOutline, domain.TotalChapters(collapsed), true, state); err != nil {
		return deleted, err
	}
	return deleted, nil
}

func (s *Store) rollbackToDraft(state rollbackState) ([]string, error) {
	deleted, err := s.removeWritingArtifacts()
	if err != nil {
		return deleted, err
	}
	if removed, err := s.removePaths(adaptationGeneratedDeletePaths()); err != nil {
		return deleted, err
	} else {
		deleted = append(deleted, removed...)
	}
	if removed, err := s.removePaths(foundationDeletePaths()); err != nil {
		return deleted, err
	} else {
		deleted = append(deleted, removed...)
	}
	if removed, err := s.removePaths([]string{"meta/progress.json"}); err != nil {
		return deleted, err
	} else {
		deleted = append(deleted, removed...)
	}
	if state.hasAdaptationState() {
		if err := s.RunMeta.ClearPlanningReview(); err != nil {
			return deleted, fmt.Errorf("clear planning review: %w", err)
		}
	} else {
		if err := s.ensureNormalPlanningReview(domain.PlanningReviewKindBlueprint, state); err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

func (s *Store) rollbackToBlank() ([]string, error) {
	deleted, err := s.removeWritingArtifacts()
	if err != nil {
		return deleted, err
	}
	if removed, err := s.removePaths(blankDeletePaths()); err != nil {
		return deleted, err
	} else {
		deleted = append(deleted, removed...)
	}
	if err := s.RunMeta.ClearPlanningReview(); err != nil {
		return deleted, fmt.Errorf("clear planning review: %w", err)
	}
	if err := s.RunMeta.SetWordBudget(nil); err != nil {
		return deleted, fmt.Errorf("clear word budget: %w", err)
	}
	if err := s.RunMeta.ClearPendingSteer(); err != nil {
		return deleted, fmt.Errorf("clear pending steer: %w", err)
	}
	return deleted, nil
}

func (s *Store) removeWritingArtifacts() ([]string, error) {
	var deleted []string
	if s.pathExists(checkpointsFile) {
		deleted = append(deleted, checkpointsFile)
	}
	if err := s.Checkpoints.Reset(); err != nil {
		return deleted, fmt.Errorf("reset checkpoints: %w", err)
	}
	if s.pathExists("meta/runtime") {
		deleted = append(deleted, "meta/runtime/")
	}
	if err := s.Runtime.Reset(); err != nil {
		return deleted, fmt.Errorf("reset runtime: %w", err)
	}
	if removed, err := s.removePaths(writingDeletePathsWithoutRuntime()); err != nil {
		return deleted, err
	} else {
		deleted = append(deleted, removed...)
	}
	return deleted, nil
}

func (s *Store) removePaths(paths []string) ([]string, error) {
	var deleted []string
	io := newIO(s.dir)
	for _, rel := range paths {
		cleanRel := strings.TrimSpace(rel)
		if cleanRel == "" || strings.Contains(cleanRel, "*") || strings.Contains(cleanRel, " 中的") {
			continue
		}
		existed, err := io.RemoveAllRel(cleanRel)
		if err != nil {
			return deleted, fmt.Errorf("remove %s: %w", cleanRel, err)
		}
		if existed {
			deleted = append(deleted, filepath.ToSlash(cleanRel))
		}
	}
	return deleted, nil
}

func (s *Store) savePlanningProgress(phase domain.Phase, total int, layered bool, state rollbackState) error {
	if total < 0 {
		total = 0
	}
	currentChapter := 0
	if total > 0 {
		currentChapter = 1
	}
	return s.Progress.Save(&domain.Progress{
		NovelName:      rollbackNovelName(state),
		Phase:          phase,
		CurrentChapter: currentChapter,
		TotalChapters:  total,
		Layered:        layered,
	})
}

func (s *Store) ensureNormalPlanningReview(kind string, state rollbackState) error {
	now := time.Now().UTC().Format(time.RFC3339)
	review := &domain.PlanningReview{
		Status:      domain.PlanningReviewStatusPending,
		Kind:        kind,
		Brief:       fallbackPlanningBrief(state),
		StartPrompt: fallbackPlanningStartPrompt(state),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if state.review != nil {
		cp := *state.review
		review = &cp
		review.Status = domain.PlanningReviewStatusPending
		review.Kind = kind
		if strings.TrimSpace(review.Brief) == "" {
			review.Brief = fallbackPlanningBrief(state)
		}
		if review.CreatedAt == "" {
			review.CreatedAt = now
		}
		review.UpdatedAt = now
	} else if state.meta != nil && state.meta.WordBudget != nil {
		review.TargetTotalWords = state.meta.WordBudget.TargetTotalWords
	}
	if strings.TrimSpace(review.Brief) == "" && kind == domain.PlanningReviewKindBlueprint {
		return fmt.Errorf("cannot restore draft without a planning brief")
	}
	return s.RunMeta.SetPlanningReview(review)
}

func (s *Store) appendRollbackLog(preview domain.RollbackPreview, deleted []string) error {
	entry := struct {
		Time         string               `json:"time"`
		Mode         string               `json:"mode,omitempty"`
		TargetStage  domain.RollbackStage `json:"target_stage"`
		TargetLabel  string               `json:"target_label,omitempty"`
		DeletedPaths []string             `json:"deleted_paths,omitempty"`
	}{
		Time:         time.Now().UTC().Format(time.RFC3339),
		Mode:         preview.Mode,
		TargetStage:  preview.TargetStage,
		TargetLabel:  preview.TargetLabel,
		DeletedPaths: append([]string(nil), deleted...),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return newIO(s.dir).AppendLine(rollbackLogFile, data)
}

func (s *Store) pathExists(rel string) bool {
	_, err := os.Lstat(filepath.Join(s.dir, filepath.FromSlash(rel)))
	return err == nil
}

func writingDeletePaths() []string {
	return append([]string{checkpointsFile, "meta/runtime/"}, writingDeletePathsWithoutRuntime()...)
}

func writingDeletePathsWithoutRuntime() []string {
	return []string{
		"chapters",
		"drafts",
		"summaries",
		"reviews",
		adaptationCheckDir,
		"timeline.json",
		"timeline.md",
		"foreshadow_ledger.json",
		"foreshadow_ledger.md",
		"relationship_state.json",
		"relationship_state.md",
		"meta/state_changes.json",
		"meta/cast_ledger.json",
		"meta/last_commit.json",
		"meta/pending_commit.json",
		"meta/last_review.json",
		"meta/outline_duplicate_scan.json",
		"meta/outline_repair_finalization.json",
	}
}

func adaptationWritingDeletePaths() []string {
	paths := writingDeletePaths()
	paths = append(paths, adaptationPlanFile)
	paths = append(paths, foundationDeletePaths()...)
	return paths
}

func adaptationConfirmedDeletePaths() []string {
	return []string{adaptationPlanFile}
}

func adaptationGeneratedDeletePaths() []string {
	return []string{
		adaptationProposalFile,
		adaptationVolumeReviewFile,
		adaptationProposalRuntimeFile,
		adaptationPlanFile,
		adaptationPlanningWorkflowFile,
		adaptationCheckDir,
	}
}

func foundationDeletePaths() []string {
	return []string{
		"premise.md",
		"outline.json",
		"outline.md",
		"layered_outline.json",
		"layered_outline.md",
		"characters.json",
		"characters.md",
		"world_rules.json",
		"world_rules.md",
		"meta/compass.json",
		"meta/snapshots",
	}
}

func blankDeletePaths() []string {
	paths := append([]string{}, foundationDeletePaths()...)
	paths = append(paths,
		"meta/progress.json",
		"meta/user_rules.json",
		"meta/style_rules.json",
		adaptationRootDir,
	)
	paths = append(paths, adaptationGeneratedDeletePaths()...)
	return paths
}

func adaptationRuntimeHasSkeleton(runtime *domain.AdaptationProposalRuntime) bool {
	return runtime != nil && (runtime.Skeleton != nil || len(runtime.SkeletonBatches) > 0)
}

func adaptationRuntimeTargetCount(runtime *domain.AdaptationProposalRuntime) int {
	if runtime == nil {
		return 0
	}
	if runtime.TargetChapterCount > 0 {
		return runtime.TargetChapterCount
	}
	if runtime.Skeleton != nil {
		return runtime.Skeleton.TargetChapterCount
	}
	return 0
}

func adaptationRuntimeCompletedCount(runtime *domain.AdaptationProposalRuntime) int {
	if runtime == nil {
		return 0
	}
	return len(runtime.CompletedBatches)
}

func adaptationPlanChapterCount(plan *domain.AdaptationPlan) int {
	if plan == nil {
		return 0
	}
	return len(plan.Chapters)
}

func adaptationVolumeTargetCount(review *domain.AdaptationVolumeReview) int {
	if review == nil {
		return 0
	}
	return review.TargetChapterCount
}

func layeredOutlineHasExpandedArcs(volumes []domain.VolumeOutline) bool {
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			if len(arc.Chapters) > 0 {
				return true
			}
		}
	}
	return false
}

func collapseLayeredOutline(volumes []domain.VolumeOutline) []domain.VolumeOutline {
	out := append([]domain.VolumeOutline(nil), volumes...)
	for vIdx := range out {
		out[vIdx].Arcs = append([]domain.ArcOutline(nil), out[vIdx].Arcs...)
		for aIdx := range out[vIdx].Arcs {
			arc := &out[vIdx].Arcs[aIdx]
			if len(arc.Chapters) > 0 && arc.EstimatedChapters <= 0 {
				arc.EstimatedChapters = len(arc.Chapters)
			}
			arc.Chapters = nil
		}
	}
	return out
}

func rollbackNovelName(state rollbackState) string {
	if state.progress != nil && strings.TrimSpace(state.progress.NovelName) != "" {
		return strings.TrimSpace(state.progress.NovelName)
	}
	return domain.ExtractNovelNameFromPremise(state.premise)
}

func fallbackPlanningBrief(state rollbackState) string {
	if state.review != nil && strings.TrimSpace(state.review.Brief) != "" {
		return strings.TrimSpace(state.review.Brief)
	}
	if strings.TrimSpace(state.premise) != "" {
		return strings.TrimSpace(state.premise)
	}
	return ""
}

func fallbackPlanningStartPrompt(state rollbackState) string {
	if state.review == nil {
		return ""
	}
	return strings.TrimSpace(state.review.StartPrompt)
}
