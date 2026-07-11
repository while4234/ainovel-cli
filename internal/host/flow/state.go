package flow

import (
	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// LoadState 从 Store 读取 Route 所需的全部事实。
// 这是路由的"IO 边界"：所有读取集中在这里，Route 保持纯。
// 读取失败按保守默认填充（has*=false, boundary=nil），让 Router 倾向重派而非跳过。
func LoadState(store *storepkg.Store) State {
	s := State{
		FoundationMissing: store.FoundationMissing(),
	}
	progress, err := store.Progress.Load()
	if err != nil || progress == nil {
		return s
	}
	s.Progress = progress
	loadAdaptationState(&s, store, progress)
	loadContinuationState(&s, store)

	if repair, rerr := store.FindDuplicateOutlineRepairBatch(progress); rerr == nil && repair != nil {
		s.OutlineRepair = repair
	}

	if n := len(progress.CompletedChapters); n > 0 {
		s.LastCompleted = progress.CompletedChapters[n-1]
	}

	if progress.Layered && len(progress.PendingRewrites) == 0 {
		if boundary, berr := store.FindPendingArcPostprocess(progress); berr == nil && boundary != nil {
			s.ArcBoundary = boundary
			s.HasArcReview = store.World.HasArcReview(boundary.LastChapter) ||
				store.Checkpoints.LatestByStep(domain.ArcScope(boundary.Volume, boundary.Arc), "review") != nil
			if !s.HasArcReview {
				s.ArcReviewBatch, _ = store.NextArcReviewBatch(boundary, domain.ArcReviewBatchRuneBudget)
			}
			s.HasArcSummary = store.Summaries.HasArcSummary(boundary.Volume, boundary.Arc)
			if boundary.IsVolumeEnd {
				s.HasVolumeSummary = store.Summaries.HasVolumeSummary(boundary.Volume)
			}
			return s
		}
	}

	// 弧边界仅在分层模式且有已完成章节时才计算
	if progress.Layered && s.LastCompleted > 0 {
		if boundary, berr := store.Outline.CheckArcBoundary(s.LastCompleted); berr == nil && boundary != nil {
			s.ArcBoundary = boundary
			if boundary.IsArcEnd {
				s.HasArcReview = store.World.HasArcReview(s.LastCompleted) ||
					store.Checkpoints.LatestByStep(domain.ArcScope(boundary.Volume, boundary.Arc), "review") != nil
				if !s.HasArcReview {
					s.ArcReviewBatch, _ = store.NextArcReviewBatch(boundary, domain.ArcReviewBatchRuneBudget)
				}
				s.HasArcSummary = store.Summaries.HasArcSummary(boundary.Volume, boundary.Arc)
				if boundary.IsVolumeEnd {
					s.HasVolumeSummary = store.Summaries.HasVolumeSummary(boundary.Volume)
				}
			}
		}
	}

	return s
}

func loadContinuationState(s *State, store *storepkg.Store) {
	if s == nil || store == nil {
		return
	}
	snapshot, err := store.Continuation.LoadSnapshot()
	if err != nil || snapshot == nil || snapshot.Plan == nil || snapshot.Workflow.Stage != domain.ContinuationStageWriting {
		return
	}
	s.ContinuationActive = true
	s.ContinuationBaseChapter = snapshot.Workflow.BaseChapterCount
}

func loadAdaptationState(s *State, store *storepkg.Store, progress *domain.Progress) {
	if s == nil || store == nil || progress == nil {
		return
	}
	plan, err := store.Adaptation.LoadPlan()
	if err != nil || plan == nil || plan.Status != domain.AdaptationPlanStatusConfirmed {
		return
	}

	s.AdaptationActive = true
	s.AdaptationPlannedChapters = make(map[int]struct{}, len(plan.Chapters))
	completed := make(map[int]struct{}, len(progress.CompletedChapters))
	for _, chapter := range progress.CompletedChapters {
		completed[chapter] = struct{}{}
	}
	s.AdaptationComplete = len(plan.Chapters) > 0
	for _, chapterPlan := range plan.Chapters {
		s.AdaptationPlannedChapters[chapterPlan.Chapter] = struct{}{}
		if chapterPlan.Chapter > s.AdaptationMaxChapter {
			s.AdaptationMaxChapter = chapterPlan.Chapter
		}
		if _, ok := completed[chapterPlan.Chapter]; !ok {
			s.AdaptationComplete = false
		}
	}
	if progress.CompletionAuditStatus != "" && progress.CompletionAuditStatus != "pass" && progress.CompletionAuditStatus != "inconclusive" {
		s.CompletionAuditBlocked = true
	}
	if s.AdaptationComplete {
		report, reportErr := store.Adaptation.LoadAuditReport()
		if reportErr == nil && report != nil && !completionReportAllows(report) && report.Scope.TargetTo >= s.AdaptationMaxChapter {
			s.CompletionAuditBlocked = true
		}
	}
}

func completionReportAllows(report *adaptaudit.Report) bool {
	if report == nil {
		return false
	}
	if report.Status == "pass" {
		return true
	}
	if report.Status != "inconclusive" {
		return false
	}
	for _, finding := range report.Findings {
		if finding.Code == "audit_contract_unavailable" {
			return true
		}
	}
	return false
}
