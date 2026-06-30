package flow

import (
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

	if n := len(progress.CompletedChapters); n > 0 {
		s.LastCompleted = progress.CompletedChapters[n-1]
	}

	// 弧边界仅在分层模式且有已完成章节时才计算
	if progress.Layered && s.LastCompleted > 0 {
		if boundary, berr := store.Outline.CheckArcBoundary(s.LastCompleted); berr == nil && boundary != nil {
			s.ArcBoundary = boundary
			if boundary.IsArcEnd {
				s.HasArcReview = store.World.HasArcReview(s.LastCompleted)
				s.HasArcSummary = store.Summaries.HasArcSummary(boundary.Volume, boundary.Arc)
				if boundary.IsVolumeEnd {
					s.HasVolumeSummary = store.Summaries.HasVolumeSummary(boundary.Volume)
				}
			}
		}
	}

	if store.Adaptation.Active() {
		if plan, perr := store.Adaptation.LoadPlan(); perr == nil && plan != nil {
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
		}
	}

	return s
}
