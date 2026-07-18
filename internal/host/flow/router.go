// Package flow 实现垂类路由：Host 根据事实决定下一个调哪个子代理做什么。
//
// 设计原则：
//   - Route 是纯函数：输入 State，输出 *Instruction。无 IO、无 Store 调用，可单测。
//   - State 由 LoadState（非纯）从 Store 构造，一次性把路由需要的事实读齐。
//   - 返回 nil 是合法的：表示"裁定场景，让 Coordinator LLM 自主决策"。
//
// Router 覆盖的是"查表型"决策（每章下一步、弧末后处理、队列驱动），
// 不覆盖"语义理解型"决策（选规划师、处理用户 Steer、输出总结）。
package flow

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// Instruction 指示 Host 下一步要求 Coordinator 调用的子代理与任务。
type Instruction struct {
	Agent          string // architect_long / architect_short / writer / editor
	Task           string // 给子代理的任务描述
	Reason         string // 给 Coordinator 看的理由（可选，方便调试与日志）
	Chapter        int    // writer 任务涉及的章节号（续写/重写/打磨）；0 表示不涉及（editor/architect 任务）
	ResumeRecovery bool   // durable draft recovery constraints survive repeated subagent boundaries
	Fence          storepkg.RevisionFence
}

// State 是 Route 的输入：所有事实必须在此显式声明，禁止 Route 内部读 Store。
type State struct {
	RevisionActive bool
	RevisionRoute  *domain.RevisionRoute

	Progress             *domain.Progress
	PlanningReview       *domain.PlanningReview
	OriginalPlanningWork *storepkg.OriginalPlanningWork
	SkeletonPlanningWork *storepkg.OriginalPlanningWork
	BlueprintVolumeCount int
	BlueprintNextIsFinal bool

	// 上一个已完成章节（Progress.CompletedChapters 末尾）；为 0 表示尚未开始写作。
	LastCompleted int

	// 上一章的弧边界信息；IsArcEnd=false 时其他字段无意义。
	// 当 LastCompleted=0 或非 Layered 模式时应为 nil。
	ArcBoundary *storepkg.ArcBoundary

	// 弧末后处理的三个事实：评审 / 弧摘要 / 卷摘要是否已完成。
	HasArcReview     bool
	ArcReviewBatch   *storepkg.ArcReviewBatch
	HasArcSummary    bool
	HasVolumeSummary bool

	// 基础设定缺项（规划阶段的补齐信号）。
	FoundationMissing []string

	OutlineRepair *storepkg.OutlineRepairBatch

	ContinuationActive      bool
	ContinuationBaseChapter int

	AdaptationActive             bool
	AdaptationPlannedChapters    map[int]struct{}
	AdaptationMaxChapter         int
	AdaptationComplete           bool
	AdaptationOutlineBlocked     bool
	AdaptationOutlineBlockReason string
	CompletionAuditBlocked       bool

	// Writer recovery facts. Normal routing uses only the word-budget subset so
	// a newly persisted oversized draft also enters bounded segment repair; a
	// Resume recovery lease additionally uses the validation facts below.
	InProgressDraftExists      bool
	InProgressCheckpoint       string
	InProgressDeAIState        string
	InProgressConsistencyValid bool
	InProgressWordCount        int
	InProgressWordMin          int
	InProgressWordMax          int
	InProgressWordBudgetValid  bool
	InProgressLineCount        int
}

const (
	writerDeAIStateMissing = "missing"
	writerDeAIStateStale   = "stale"
	writerDeAIStateFailed  = "failed"
	writerDeAIStatePassed  = "passed"
)

// Route 根据事实返回下一步指令；返回 nil 表示让 Coordinator LLM 自主裁定。
//
// 决策优先级（互斥，自上而下匹配第一个）：
//  1. Phase=Complete        → nil（LLM 输出总结）
//  2. Phase!=Writing        → nil（LLM 裁定规划师选型 / 规划补齐）
//  3. 大纲重复批次待修复       → architect_long(repair_arc)
//  4. PendingRewrites 非空  → writer 按队列重写/打磨
//  5. Flow=Reviewing        → nil（editor 刚保存 review，verdict 分叉由工具层处理）
//  6. Flow=Steering         → nil（用户干预处理中）
//  7. 弧末评审缺失           → editor(arc review)
//  8. 弧末评审有但弧摘要缺失  → editor(arc summary)
//  9. 卷末弧摘要有但卷摘要缺失 → editor(volume summary)
//
// 10. 下一弧是骨架           → architect_long(expand_arc)
//
// 11. 卷末需决策下一卷       → architect_long(append_volume / complete_book)
// 12. 其它                  → writer(写 next_chapter)
func Route(s State) *Instruction {
	// An active revision owns the router. Manual approval, pause, and failure
	// stages intentionally return no instruction while still blocking every
	// ordinary planning/writing route below.
	if s.RevisionActive {
		if s.RevisionRoute == nil {
			return nil
		}
		return &Instruction{
			Agent:  s.RevisionRoute.Agent,
			Task:   s.RevisionRoute.Task,
			Reason: s.RevisionRoute.Reason,
			Fence: storepkg.RevisionFence{
				Generation: s.RevisionRoute.Generation,
				SessionID:  s.RevisionRoute.SessionID,
				Revision:   s.RevisionRoute.Revision,
			},
		}
	}
	p := s.Progress
	if p == nil {
		return nil
	}

	// 1. 终态：让 LLM 输出总结
	if p.Phase == domain.PhaseComplete {
		return nil
	}
	if route := routeOriginalPlanning(s); route != nil {
		return route
	}

	// 2. 规划阶段由 Coordinator 裁定（选 architect_long/short + 补齐循环）
	if p.Phase != domain.PhaseWriting {
		return nil
	}

	// An upstream adaptation-contract defect must be fixed before any Writer
	// turn. Returning no route also prevents the coordinator from receiving a
	// fresh automatic chapter instruction while the plan is invalid.
	if s.AdaptationOutlineBlocked {
		return nil
	}
	if s.InProgressDraftExists && s.InProgressWordMin > 0 &&
		s.InProgressWordMax >= s.InProgressWordMin && !s.InProgressWordBudgetValid {
		return routeWriterBudgetSegment(s, p.InProgressChapter)
	}

	if s.OutlineRepair != nil && s.OutlineRepair.Repairable() {
		return &Instruction{
			Agent:  "architect_long",
			Task:   formatOutlineRepairTask(s.OutlineRepair),
			Reason: fmt.Sprintf("outline duplicate: chapter %d repeats chapter %d", s.OutlineRepair.Duplicate.Chapter, s.OutlineRepair.Duplicate.ExistingChapter),
		}
	}

	// 4. 重写/打磨队列优先（事实已在工具层落盘，Router 只照单派发）
	if len(p.PendingRewrites) > 0 {
		ch := p.PendingRewrites[0]
		if !s.adaptationAllowsChapter(ch) {
			return nil
		}
		task := fmt.Sprintf("重写第 %d 章", ch)
		if p.Flow == domain.FlowPolishing {
			task = fmt.Sprintf("打磨第 %d 章：先读取 novel_context.rewrite_brief 与当前完整草稿。若草稿仍与终稿相同，使用 edit_chapter 落实至少一处有审核依据的局部实质改动；若恢复时草稿已包含打磨改动，保留现有改动，不要为了满足次数重复修改。不要在 check_de_ai 已通过时调用 repair_de_ai_batch，不要无结构理由整章重写。回读后在同一版草稿上通过 check_consistency、check_de_ai，再 commit_chapter", ch)
		}
		return &Instruction{
			Agent:   "writer",
			Task:    task,
			Reason:  fmt.Sprintf("PendingRewrites 队列剩余 %d 章", len(p.PendingRewrites)),
			Chapter: ch,
		}
	}

	// 4. 审阅中：save_review 刚落盘，verdict 升级/降级由工具层处理，路由不介入
	if p.Flow == domain.FlowReviewing {
		return nil
	}

	// 5. 用户干预处理中：Coordinator 正在裁定，Host 不抢占
	if p.Flow == domain.FlowSteering {
		return nil
	}

	// Imported chapters are historical source material, not chapters that were
	// just written by this run. Once a reviewed continuation plan starts, route
	// directly to N+1 instead of retrospectively reviewing the source boundary.
	if s.ContinuationActive &&
		s.LastCompleted == s.ContinuationBaseChapter &&
		p.NextChapter() == s.ContinuationBaseChapter+1 {
		return s.nextChapterInstruction(p, "续写规划已审核通过，从导入基线后的第一章开始")
	}

	// 6-10. 分层模式的弧末后处理
	if p.Layered && s.ArcBoundary != nil && s.ArcBoundary.IsArcEnd {
		b := s.ArcBoundary
		chapterNote := ""
		if b.LastChapter > 0 {
			chapterNote = fmt.Sprintf("（弧末第 %d 章）", b.LastChapter)
		}
		switch {
		case !s.HasArcReview:
			return &Instruction{
				Agent:  "editor",
				Task:   formatArcReviewTask(b, s.ArcReviewBatch, chapterNote),
				Reason: "弧末评审未完成",
			}
		case !s.HasArcSummary:
			return &Instruction{
				Agent:  "editor",
				Task:   formatArcSummaryTask(b, chapterNote),
				Reason: "弧摘要未完成",
			}
		case b.IsVolumeEnd && !s.HasVolumeSummary:
			return &Instruction{
				Agent:  "editor",
				Task:   formatVolumeSummaryTask(b.Volume),
				Reason: "卷摘要未完成",
			}
		case b.NeedsExpansion && b.NextArc > 0:
			if s.AdaptationActive {
				return s.nextChapterInstruction(p, "改编计划仍有后续章节，跳过普通扩弧")
			}
			return &Instruction{
				Agent:  "architect_long",
				Task:   fmt.Sprintf("展开第 %d 卷第 %d 弧（save_foundation type=expand_arc）", b.NextVolume, b.NextArc),
				Reason: "下一弧骨架待展开",
			}
		case b.NeedsNewVolume:
			if s.AdaptationActive && s.AdaptationComplete {
				if s.CompletionAuditBlocked {
					return nil
				}
				return &Instruction{
					Agent:  "architect_long",
					Task:   "改编计划章节、弧级评审、弧摘要和卷摘要均已完成；调用 save_foundation type=complete_book 完结全书，不要 append_volume",
					Reason: "改编计划已完成，进入完结收尾",
				}
			}
			if s.AdaptationActive {
				return s.nextChapterInstruction(p, "改编计划仍有后续章节，跳过普通扩卷")
			}
			return &Instruction{
				Agent:  "architect_long",
				Task:   "评估后调用 save_foundation type=append_volume（继续写）或 type=complete_book（全书结束）",
				Reason: "卷末需决定追加新卷或结束全书",
			}
		}
	}

	// 12. 正常续写
	return s.nextChapterInstruction(p, "续写下一章")
}

// RouteResume refines routing while a Resume recovery lease is active. A
// chapter with a durable draft is recovery work, even if an interrupted weak
// Writer moved the latest checkpoint back to "plan".
func RouteResume(s State) *Instruction {
	instruction := Route(s)
	if !writerResumeRouteApplicable(s, instruction) {
		return instruction
	}

	progress := s.Progress
	chapter := progress.InProgressChapter
	if s.InProgressWordMin > 0 && s.InProgressWordMax >= s.InProgressWordMin && !s.InProgressWordBudgetValid {
		return routeWriterBudgetSegment(s, chapter)
	}
	adaptationStep := ""
	if s.AdaptationActive {
		adaptationStep = "；若本书处于改编模式，还必须在同一版草稿上通过 check_adaptation"
	}
	return &Instruction{
		Agent: "writer",
		Task: fmt.Sprintf(
			"恢复第 %d 章现有草稿（checkpoint=%s，de_ai=%s，consistency_current=%t，word_count=%d，word_budget=%d-%d，word_budget_current=%t）。当前草稿是唯一工作版本：禁止调用 plan_chapter 或 draft_chapter，禁止读取其他章节，禁止重复调用 novel_context。先且只先调用一次 read_chapter(chapter=%d, source=\"draft\")，禁止传 from_line/to_line；任何修复落盘后都不要再次回读，直接复检。随后调用 check_de_ai；若有 repair finding，依据刚回读的当前原文用 repair_de_ai_batch 做一小批唯一精确替换并立即复检，过期 old_string 让工具跳过，不要重放旧批次，直到 check_de_ai 通过。随后调用 novel_context(chapter=%d) 一次并在同一版草稿上通过 check_consistency%s；任何后续改稿都要重新完成这些检查。最后直接 commit_chapter，不要重新规划或整章重写。",
			chapter, resumeFact(s.InProgressCheckpoint), resumeFact(s.InProgressDeAIState), s.InProgressConsistencyValid,
			s.InProgressWordCount, s.InProgressWordMin, s.InProgressWordMax, s.InProgressWordBudgetValid,
			chapter, chapter, adaptationStep,
		),
		Reason:         fmt.Sprintf("恢复第 %d 章已有草稿的当前验证阶段", chapter),
		Chapter:        chapter,
		ResumeRecovery: true,
	}
}

const writerBudgetSegmentLines = 48

func routeWriterBudgetSegment(s State, chapter int) *Instruction {
	lineCount := max(s.InProgressLineCount, 1)
	segmentCount := (lineCount + writerBudgetSegmentLines - 1) / writerBudgetSegmentLines
	segment := segmentCount - 1
	if completed, ok := writerBudgetCompletedSegment(s.InProgressCheckpoint); ok {
		if completed > 0 {
			segment = min(completed-1, segmentCount-1)
		}
	}
	fromLine := segment*writerBudgetSegmentLines + 1
	toLine := min(fromLine+writerBudgetSegmentLines-1, lineCount)
	delta := s.InProgressWordCount - s.InProgressWordMax
	direction := "删减"
	if delta < 0 {
		delta = s.InProgressWordMin - s.InProgressWordCount
		direction = "补足"
	}
	remainingSegments := segment + 1
	target := (delta + remainingSegments - 1) / remainingSegments
	return &Instruction{
		Agent: "writer",
		Task: fmt.Sprintf(
			"恢复第 %d 章现有草稿的字数分段修复：当前 %d 字，预算 %d-%d 字；本轮只处理第 %d 段（行 %d-%d，草稿共 %d 行），目标约%s %d 字。先调用 novel_context(chapter=%d) 一次保留章节契约、连续性和人物情感依据，再且只再调用 read_chapter(chapter=%d, source=\"draft\", from_line=%d, to_line=%d) 读取本段。静默选择本段内多处有依据的局部修改，下一次响应必须直接调用一次 edit_chapter(chapter=%d, budget_segment=%d, edits=[...]) 原子落盘；只处理冗余解释、重复动作或缺失承接，保留本段关键事件、人物选择、情感落点和钩子。工具调用后立即结束本轮，由 Host 派下一段。禁止读取整章或其他章节，禁止 plan_chapter、draft_chapter、commit_chapter，禁止输出逐段分析或修改清单。",
			chapter, s.InProgressWordCount, s.InProgressWordMin, s.InProgressWordMax,
			segment, fromLine, toLine, lineCount, direction, max(target, 1), chapter,
			chapter, fromLine, toLine, chapter, segment,
		),
		Reason:         fmt.Sprintf("第 %d 章按行段局部修复字数预算", chapter),
		Chapter:        chapter,
		ResumeRecovery: true,
	}
}

func writerBudgetCompletedSegment(checkpoint string) (int, bool) {
	const prefix = "word_budget_edit_segment_"
	trimmed := strings.TrimSpace(checkpoint)
	if !strings.HasPrefix(trimmed, prefix) {
		return 0, false
	}
	raw := strings.TrimPrefix(trimmed, prefix)
	if raw == "" {
		return 0, false
	}
	segment, err := strconv.Atoi(raw)
	return segment, err == nil && segment >= 0
}

func writerResumeRouteApplicable(s State, instruction *Instruction) bool {
	progress := s.Progress
	return instruction != nil && progress != nil && instruction.Agent == "writer" &&
		progress.Flow != domain.FlowPolishing && len(progress.PendingRewrites) == 0 &&
		progress.InProgressChapter > 0 && instruction.Chapter == progress.InProgressChapter &&
		s.InProgressDraftExists
}

func resumeFact(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func routeOriginalPlanning(s State) *Instruction {
	review := s.PlanningReview
	if review == nil || review.Status != domain.PlanningReviewStatusCollecting {
		return nil
	}
	if review.Kind == domain.PlanningReviewKindBlueprint {
		if s.SkeletonPlanningWork != nil {
			return routeOriginalSkeletonAudit(s.SkeletonPlanningWork)
		}
		if s.Progress != nil && s.Progress.Layered && s.Progress.TotalChapters > 0 {
			for _, item := range s.FoundationMissing {
				if item == "compass" {
					return &Instruction{
						Agent:  "architect_long",
						Task:   "The normal-original volume skeleton already exists. Read novel_context(scope=planning), then call save_foundation(type=update_compass) exactly once with a compact StoryCompass that preserves the approved premise, character arcs, volume escalation and promised ending. Do not append another volume, do not overwrite any existing foundation artifact, do not generate detailed chapters, and do not analyze a source novel.",
						Reason: "分卷骨架已存在，先补齐全书指南针再判断字数覆盖",
					}
				}
			}
			finalContract := "This is not the final volume: end with a paid-off phase climax and a concrete causal handoff, without resolving the whole novel."
			if s.BlueprintNextIsFinal {
				finalContract = "This next volume is the FINAL volume allowed by the persisted word budget. It must close every promised main plot, antagonist outcome, setup, protagonist arc and relationship/ending contract; it must not end with a new-volume handoff or merely open another investigation."
			}
			return &Instruction{
				Agent:  "architect_long",
				Task:   fmt.Sprintf("Continue the normal-original long proposal from the persisted volume skeleton. Read novel_context(scope=planning), then call save_foundation(type=append_volume) exactly once to append only volume %d. The volume must contain 2-3 causal arcs, each reserving exactly 3-4 chapters with chapters omitted. Its theme must state entry state, central conflict, irreversible volume result and exit state; every arc goal must state protagonist goal, opposition, decisive choice/cost, phase payoff and the next causal consequence. %s Preserve all existing premise, characters, world_rules, compass and prior volumes; do not call any replacement foundation type, do not generate detailed chapters, and do not analyze a source novel. Stop after the single append_volume call so the next volume uses a fresh model batch.", s.BlueprintVolumeCount+1, finalContract),
				Reason: "普通原创下一卷骨架待独立批次追加",
			}
		}
		missing := make([]string, 0, len(s.FoundationMissing))
		for _, item := range s.FoundationMissing {
			if item != "outline" {
				missing = append(missing, item)
			}
		}
		if len(missing) > 0 {
			return &Instruction{
				Agent:  "architect_long",
				Task:   fmt.Sprintf("Continue the normal-original long proposal by saving only these currently missing foundation artifacts: %s. Use novel_context(scope=planning), preserve every already persisted artifact verbatim, and do not call save_foundation for any artifact not in this missing list. Do not generate detailed chapters or analyze a source novel. After filling the listed artifacts, create the first skeleton-only volume only if all prerequisites are now present; the first layered_outline call must contain exactly one volume with 2-3 arcs of exactly 3-4 estimated chapters each.", strings.Join(missing, ", ")),
				Reason: "普通原创蓝图仅补齐缺失设定",
			}
		}
		return &Instruction{
			Agent:  "architect_long",
			Task:   "Continue the normal-original long proposal by creating only the first skeleton volume. Call novel_context(scope=planning), then save exactly one volume with save_foundation(type=layered_outline); it must contain 2-3 causal arcs, each reserving exactly 3-4 chapters with chapters omitted. The volume theme must state entry state, central conflict, irreversible phase result and exit state. Every arc goal must state protagonist goal, active opposition, decisive choice/cost, phase payoff and next causal consequence; distribute plot, character and relationship progress instead of repeating evidence discovery. Preserve every persisted foundation artifact verbatim, do not generate detailed chapters, and do not analyze a source novel. Stop after the one-volume save so later volumes use fresh model batches.",
			Reason: "普通原创分卷骨架仍在分批生成",
		}
	}
	if review.Kind != domain.PlanningReviewKindVolumeSplit || s.OriginalPlanningWork == nil {
		return nil
	}
	w := s.OriginalPlanningWork
	switch w.Kind {
	case "expand_arc":
		return &Instruction{Agent: "architect_long", Reason: "下一批3-4章原创细纲待生成", Task: fmt.Sprintf(
			"展开普通原创细纲第%d卷第%d弧：调用 novel_context(scope=planning) 核对人物、规则、前后弧、已通过的分卷契约和字数预算；本批严格生成%d章（第%d-%d章，且永远不超过4章），再调用 save_foundation(type=expand_arc, volume=%d, arc=%d)。每章 core_event 必须明确写出本章目标、主动阻力、关键选择及代价、不可逆结果、人物/关系/信息状态变化；scenes 必须是可写成约3000-5000字的递进场景，不是同一事件的重复表述；hook 必须由本章结果自然产生并给下一章新的行动问题。各章功能不得重复，情感推进必须改变主线决策，不得参考原著。",
			w.Volume, w.Arc, w.ToChapter-w.FromChapter+1, w.FromChapter, w.ToChapter, w.Volume, w.Arc)}
	case "repair_arc":
		payload, _ := json.Marshal(w.Audit)
		return &Instruction{Agent: "architect_long", Reason: "原创细纲自动审核要求定点返修", Task: fmt.Sprintf(
			"原创细纲审核未通过，只修复第%d卷第%d弧内第%d-%d章。审核报告：%s。调用 novel_context(scope=planning) 和 novel_context(scope=outline_range, from=%d, to=%d) 核对证据，只返回这个窗口的%d章并调用 save_foundation(type=repair_arc, volume=%d, arc=%d, from_chapter=%d, to_chapter=%d)。不得改写同弧兄弟章节；逐条落实 repair_instruction，不得分析原著。",
			w.Volume, w.Arc, w.FromChapter, w.ToChapter, payload, w.FromChapter, w.ToChapter, w.ToChapter-w.FromChapter+1, w.Volume, w.Arc, w.FromChapter, w.ToChapter)}
	case "audit_chapter":
		return &Instruction{Agent: "editor", Reason: "修订章节需独立签名审核", Task: fmt.Sprintf(
			"只审核第%d卷第%d弧普通原创第%d章当前细纲。调用 novel_context(scope=outline_range, from=%d, to=%d)，不得读取正文、原著或扩大范围。然后调用 save_original_planning_audit(scope=chapter, scope_id=当前章节稳定ID, volume=%d, arc=%d, from_volume=0, to_volume=0, from_chapter=%d, to_chapter=%d)；必须按 causal_value、character_logic、continuity、scene_progression、hook_and_pacing、originality 六维审核。通过时 verdict=pass 且 issues=[]；不通过时 verdict=revise，issues 首项必须填写 volume=%d、arc=%d、from_chapter=%d、to_chapter=%d、问题描述和定点修复指令。",
			w.Volume, w.Arc, w.FromChapter, w.FromChapter, w.ToChapter, w.Volume, w.Arc, w.FromChapter, w.ToChapter, w.Volume, w.Arc, w.FromChapter, w.ToChapter)}
	case "audit_arc":
		return &Instruction{Agent: "editor", Reason: "本批原创细纲需独立弧级审核", Task: fmt.Sprintf(
			"作为专业原创小说审稿人，分批审核第%d卷第%d弧的第%d-%d章（本批%d章）。只调用 novel_context(scope=planning) 与 novel_context(scope=outline_range, from=%d, to=%d)，禁止读取正文或任何原著，禁止扩大本批范围。检查目标-阻力-选择-结果因果链、人物动机与状态变化、每章独立推进价值、前后连续性、节奏钩子、套路化与批次重复。必须额外核对世界规则与时间线：重生、穿越、回档前的物理实体不能无来源出现在当前时间线；任何决定高潮胜负的证据都必须在本批或已确认前文中有可验证来源与前置动作，不能在回收章临时发明。然后调用 save_original_planning_audit(scope=arc, volume=%d, arc=%d, from_chapter=%d, to_chapter=%d)，dimensions 必须恰含 causal_progression、character_logic、chapter_value、continuity、hook_and_pacing、originality。任一维度低于7或存在重大问题必须 verdict=revise，并把首个问题精确定位到本卷本弧及修复指令。",
			w.Volume, w.Arc, w.FromChapter, w.ToChapter, w.ToChapter-w.FromChapter+1, w.FromChapter, w.ToChapter, w.Volume, w.Arc, w.FromChapter, w.ToChapter)}
	case "audit_volume":
		return &Instruction{Agent: "editor", Reason: "逐弧审核已完成，需归并分卷质量审核", Task: fmt.Sprintf(
			"作为专业原创小说审稿人审核第%d卷。不得一次重读整卷细纲；以下是已经逐弧分批通过、带章节证据索引的报告：%s。调用 novel_context(scope=planning) 只核对分卷骨架、人物和字数预算；仅有明确疑点时才定向调用一次 novel_context(scope=outline_range)，且范围最多4章。检查全卷结构节奏、主题与核心冲突、高潮兑现、人物弧阶段成果、规划内容能否承载目标字数、下一卷驱动力。调用 save_original_planning_audit(scope=volume, volume=%d)，dimensions 必须恰含 structure_pacing、theme_conflict、climax_payoff、character_arc、budget_capacity、next_volume_drive。问题必须定位到具体卷弧并给出可执行修复指令。",
			w.Volume, w.Evidence, w.Volume)}
	case "audit_book_batch":
		return &Instruction{Agent: "editor", Reason: "全书审核按每批最多2卷归并", Task: fmt.Sprintf(
			"分批进行原创全书细纲审核，本次只归并第%d-%d卷（最多2卷），不得读取全书原始细纲。已通过的卷级报告：%s。调用 novel_context(scope=planning) 核对指南针和卷间骨架，检查卷间因果承接、冲突升级、人物成长连续性、伏笔传递、节奏平衡与原创性。调用 save_original_planning_audit(scope=book_batch, from_volume=%d, to_volume=%d)，dimensions 必须恰含 cross_volume_continuity、escalation、character_progression、setup_payoff、pacing_balance、originality。发现问题须定位到具体卷弧。",
			w.FromVolume, w.ToVolume, w.Evidence, w.FromVolume, w.ToVolume)}
	case "audit_book":
		return &Instruction{Agent: "editor", Reason: "分批审核已完成，需以审核摘要进行全书总审", Task: fmt.Sprintf(
			"完成原创小说全书细纲总审。禁止加载全书原始细纲，只使用这些已通过的每2卷分批报告：%s，并调用 novel_context(scope=planning) 核对 premise、人物、世界规则、指南针和分卷骨架。检查主线闭环、人物成长闭环、伏笔回收、高潮梯度与节奏、世界规则一致、题材辨识度、结局兑现。调用 save_original_planning_audit(scope=book)，dimensions 必须恰含 mainline_closure、character_closure、setup_payoff、escalation_pacing、world_consistency、originality、ending_delivery。任何重大问题必须定位卷弧返修；只有全部维度不低于7且无重大问题才能 pass。",
			w.Evidence)}
	}
	return nil
}

func routeOriginalSkeletonAudit(w *storepkg.OriginalPlanningWork) *Instruction {
	if w == nil {
		return nil
	}
	switch w.Kind {
	case "repair_skeleton_volume":
		payload, _ := json.Marshal(w.Audit)
		return &Instruction{Agent: "architect_long", Reason: "分卷骨架自动审核要求定点返修", Task: fmt.Sprintf(
			"原创分卷骨架审核未通过，只返修第%d卷。审核报告：%s。调用 novel_context(scope=planning) 核对全书承诺、相邻卷进出状态、人物弧和字数预算；保持本卷原有预估章节总数及2-3弧/每弧3-4章约束，调用 save_foundation(type=repair_volume, volume=%d) 完整替换本卷骨架。逐条落实 repair_instruction，不改其他卷，不生成详细章节，不参考原著。若本卷是终卷，必须闭合全部主线、人物弧、伏笔、反派结局和结局承诺，禁止留下“下一卷继续”的主线。",
			w.Volume, payload, w.Volume)}
	case "audit_skeleton_volume":
		return &Instruction{Agent: "editor", Reason: "用户审核前先逐卷审核原创分卷骨架", Task: fmt.Sprintf(
			"作为专业原创小说审稿人，只审核第%d卷分卷骨架。调用 novel_context(scope=planning)，不得生成细纲、不得参考原著。检查本卷功能与不可逆推进、弧间因果、人物阶段成长、主动反派与冲突升级、篇幅承载、卷高潮兑现和进出状态；若为终卷还必须检查全部主线/人物/伏笔/结局闭环，非终卷则检查有效交棒。调用 save_original_planning_audit(scope=skeleton_volume, volume=%d)，dimensions 必须恰含 volume_function、arc_causality、character_progression、conflict_escalation、budget_capacity、payoff_and_handoff。任一维度低于7、内容明显写不满预算、重复调查、或终卷只开新线不收束，必须 revise 并定位到具体卷弧。",
			w.Volume, w.Volume)}
	case "audit_skeleton_book_batch":
		return &Instruction{Agent: "editor", Reason: "分卷骨架按最多两卷分批审核", Task: fmt.Sprintf(
			"审核原创分卷骨架第%d-%d卷（最多2卷），只使用已通过的逐卷报告：%s，并调用 novel_context(scope=planning) 核对相邻卷骨架。检查卷间因果、冲突升级、人物成长、伏笔传递与回收、节奏分配和情节类型多样性。调用 save_original_planning_audit(scope=skeleton_book_batch, from_volume=%d, to_volume=%d)，dimensions 必须恰含 cross_volume_continuity、escalation、character_progression、setup_payoff、pacing_balance、plot_diversity；发现重大问题必须 revise 并定位问题卷弧。",
			w.FromVolume, w.ToVolume, w.Evidence, w.FromVolume, w.ToVolume)}
	case "audit_skeleton_book":
		return &Instruction{Agent: "editor", Reason: "用户审核前完成原创分卷全书总审", Task: fmt.Sprintf(
			"完成原创小说分卷骨架全书总审。禁止一次加载未来详细细纲，只使用这些已通过的分批报告：%s，并调用 novel_context(scope=planning) 核对 premise、人物、规则、指南针、总字数与全部分卷骨架。检查所有创作承诺是否都有卷弧承载、主线完整闭环、人物弧完整、伏笔回收、卷级高潮梯度、篇幅合理、题材辨识度和终卷结局兑现。调用 save_original_planning_audit(scope=skeleton_book)，dimensions 必须恰含 mainline_completeness、ending_closure、character_arc_completeness、setup_payoff、volume_balance、budget_capacity、originality。任何维度低于7、任何承诺无承载、或终卷没有真正结束全书都必须 revise；全部通过后系统才允许用户审核分卷。",
			w.Evidence)}
	}
	return nil
}

func (s State) nextChapterInstruction(p *domain.Progress, reason string) *Instruction {
	if p == nil {
		return nil
	}
	next := p.NextChapter()
	if next <= 0 || !s.adaptationAllowsChapter(next) {
		return nil
	}
	return &Instruction{
		Agent:   "writer",
		Task:    fmt.Sprintf("写第 %d 章", next),
		Reason:  reason,
		Chapter: next,
	}
}

// FormatMessage 把 Instruction 格式化为发给 Coordinator 的用户消息。
// 格式固定，便于 Coordinator prompt 识别与 LLM 直接响应。
func (s State) adaptationAllowsChapter(chapter int) bool {
	if !s.AdaptationActive {
		return true
	}
	if chapter <= 0 {
		return false
	}
	_, ok := s.AdaptationPlannedChapters[chapter]
	return ok
}

func FormatMessage(i *Instruction) string {
	return fmt.Sprintf(
		"[Host 下达指令]\n下一步：调用 subagent(%s, %q)\nagent: %s\ntask: %q\n理由：%s\n这是流程层的明确指令，请立即执行；subagent 的 agent/task 参数必须原样使用上面的 agent/task，不要改写 task，不要先调 novel_context，不要先输出推理。",
		i.Agent, i.Task, i.Agent, i.Task, i.Reason,
	)
}

func formatArcReviewTask(b *storepkg.ArcBoundary, batch *storepkg.ArcReviewBatch, chapterNote string) string {
	if b == nil {
		return "做弧级评审（scope=arc）：按完整章节分批读取原文，全部批次审完后合并调用 save_review"
	}
	from, to := b.FirstChapter, b.LastChapter
	if from <= 0 {
		from = to
	}
	if to <= 0 {
		to = from
	}
	rangeNote := ""
	if from > 0 && to >= from {
		chapterNote = strings.Trim(chapterNote, "（）")
		if chapterNote != "" {
			rangeNote = fmt.Sprintf("（第 %d-%d 章，%s）", from, to, chapterNote)
		} else {
			rangeNote = fmt.Sprintf("（第 %d-%d 章）", from, to)
		}
	}
	if batch != nil {
		return fmt.Sprintf(
			"对第 %d 卷第 %d 弧%s做弧级评审的第 %d/%d 批（完整章节第 %d-%d 章）。本轮只审核这一批：第一步且只调用一次 read_chapter(source=\"final\", from=%d, to=%d, max_total_runes=%d) 读取本批完整章节；禁止调用 novel_context，禁止读取其他章节或追加下一批，不要使用 max_runes，不要把章节从中间截断。若本批发现问题，affected_chapters 只写本批内确有问题的章节。读取后立即调用 save_review(chapter=%d, scope=\"arc_batch\", volume=%d, arc=%d, batch_from=%d, batch_to=%d) 保存本批结果并结束；不要在本轮调用 scope=\"arc\"，最后一批保存后系统会合并为弧级 review。",
			b.Volume, b.Arc, rangeNote, batch.Index, batch.Total, batch.From, batch.To,
			batch.From, batch.To, domain.ArcReviewBatchRuneBudget,
			to, b.Volume, b.Arc, batch.From, batch.To,
		)
	}
	return fmt.Sprintf(
		"对第 %d 卷第 %d 弧%s做弧级评审（scope=arc）。必须按完整章节分批审阅：从第 %d 章开始调用 read_chapter(source=\"final\", from=批次起点, to=%d, max_total_runes=%d)，工具返回 next_from 时继续下一批；不要把一个章节从中间切开，若某一章单独超过预算，就把该章作为单章批次完整审阅。全部批次都审完后，合并各批问题与结论，只调用一次 save_review(chapter=%d, scope=\"arc\")。",
		b.Volume, b.Arc, rangeNote, from, to, domain.ArcReviewBatchRuneBudget, to,
	)
}

func formatArcSummaryTask(b *storepkg.ArcBoundary, chapterNote string) string {
	if b == nil {
		return "生成当前弧摘要（save_arc_summary）。先调用 novel_context(scope=\"summary\", from=弧首章, to=弧末章) 取得摘要证据包；证据完整时直接生成，只有证据包明确缺项时才定向 read_chapter 回读缺失章节，不要无差别重读整弧。"
	}
	return fmt.Sprintf(
		"生成第 %d 卷第 %d 弧%s摘要（save_arc_summary）。先调用一次 novel_context(scope=\"summary\", from=%d, to=%d) 加载章节摘要、合并评审、时间线、关系与角色状态证据。证据包 complete=true 时直接生成，不要重新审阅或重读正文；只有 missing_summary_chapters 或评审证据明确指出信息缺口时，才定向 read_chapter 回读对应章节，禁止无差别重读整弧。完成后调用 save_arc_summary。",
		b.Volume, b.Arc, chapterNote, b.FirstChapter, b.LastChapter,
	)
}

func formatVolumeSummaryTask(volume int) string {
	return fmt.Sprintf(
		"生成第 %d 卷卷摘要（save_volume_summary）。先调用一次 novel_context(scope=\"summary\", volume=%d) 加载全部弧摘要与角色状态。证据包 complete=true 时直接生成；只有 missing_arcs 非空时才定向补取缺失弧证据，禁止无差别逐章重读整卷。完成后调用 save_volume_summary。",
		volume, volume,
	)
}
