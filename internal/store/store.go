package store

import (
	"fmt"
	"os"
	"slices"
	"sync"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// Store 是状态管理的组合根，持有所有子存储。
type Store struct {
	dir string

	recoveryMu  sync.Mutex
	recoveryErr error

	Progress               *ProgressStore
	Outline                *OutlineStore
	Drafts                 *DraftStore
	Summaries              *SummaryStore
	RunMeta                *RunMetaStore
	UserRules              *UserRulesStore
	Signals                *SignalStore
	Runtime                *RuntimeStore
	Characters             *CharacterStore
	Cast                   *CastStore
	World                  *WorldStore
	Checkpoints            *CheckpointStore
	Sessions               *SessionStore
	Usage                  *UsageStore
	Simulation             *SimulationStore
	DeAI                   *DeAIStore
	Adaptation             *AdaptationStore
	Continuation           *ContinuationStore
	Revisions              *RevisionStore
	OriginalPlanningAudits *OriginalPlanningAuditStore

	crossMu sync.Mutex // 保护跨域原子操作
}

// NewStore 创建状态管理器，dir 为小说输出根目录。
func NewStore(dir string) *Store {
	identity := newStructureIdentity(dir)
	migration := newStructureMigration(dir)
	// Recover before constructing cache-bearing sub-stores such as checkpoints;
	// otherwise they could retain the pre-transaction file generation in memory.
	recoveryErr := migration.recover()
	io := newIO(dir)
	outline := NewOutlineStore(io, identity, migration)
	return &Store{
		dir:                    dir,
		recoveryErr:            recoveryErr,
		Progress:               NewProgressStore(newIO(dir), migration),
		Outline:                outline,
		Drafts:                 NewDraftStore(newIO(dir), migration),
		Summaries:              NewSummaryStore(newIO(dir), outline, migration),
		RunMeta:                NewRunMetaStore(newIO(dir)),
		UserRules:              NewUserRulesStore(newIO(dir)),
		Signals:                NewSignalStore(newIO(dir)),
		Runtime:                NewRuntimeStore(newIO(dir)),
		Characters:             NewCharacterStore(newIO(dir), outline),
		Cast:                   NewCastStore(newIO(dir)),
		World:                  NewWorldStore(newIO(dir), migration),
		Checkpoints:            newCheckpointStore(io, recoveryErr == nil),
		Sessions:               NewSessionStore(newIO(dir)),
		Usage:                  NewUsageStore(newIO(dir)),
		Simulation:             NewSimulationStore(newIO(dir)),
		DeAI:                   NewDeAIStore(newIO(dir)),
		Adaptation:             NewAdaptationStore(newIO(dir), identity, migration),
		Continuation:           NewContinuationStore(newIO(dir), migration),
		Revisions:              NewRevisionStore(dir),
		OriginalPlanningAudits: NewOriginalPlanningAuditStore(newIO(dir)),
	}
}

// Dir 返回输出根目录。
func (s *Store) Dir() string { return s.dir }

// RecoverStructureMigration completes an interrupted structure transaction
// before a caller assembles a compound diagnostic snapshot.
func (s *Store) RecoverStructureMigration() error {
	if s == nil || s.Outline == nil || s.Outline.migration == nil {
		return nil
	}
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	if err := s.Outline.migration.recover(); err != nil {
		s.recoveryErr = err
		if s.Checkpoints != nil {
			s.Checkpoints.invalidateCache()
		}
		return err
	}
	s.recoveryErr = nil
	if s.Checkpoints != nil {
		s.Checkpoints.loadFromDisk()
	}
	return nil
}

// CheckConsistency 对事实层做一次浅层校验，用于启动/恢复时生成 warning。
// 纯只读：不修正数据，仅返回可读的问题描述。调用方决定如何展示（log / UI）。
// 为避免扫全目录带来的 IO 开销，只校验 Progress 的关键点：
//   - 最后一个完成章节必须在 chapters/ 下存在终稿
//   - Layered 模式下，当前 Volume/Arc 必须能在 layered_outline 中找到
func (s *Store) CheckConsistency() []string {
	var warnings []string
	progress, err := s.Progress.Load()
	if err != nil || progress == nil {
		return warnings
	}
	if n := len(progress.CompletedChapters); n > 0 {
		lastCh := progress.CompletedChapters[n-1]
		if text, err := s.Drafts.LoadChapterText(lastCh); err == nil && text == "" && !slices.Contains(progress.PendingRewrites, lastCh) {
			warnings = append(warnings, fmt.Sprintf("progress 标记第 %d 章已完成，但 chapters/%02d.md 不存在或为空", lastCh, lastCh))
		}
	}
	if progress.Layered && progress.CurrentVolume > 0 && progress.CurrentArc > 0 {
		volumes, err := s.Outline.LoadLayeredOutline()
		if err == nil && len(volumes) > 0 {
			found := false
			for _, v := range volumes {
				if v.Index != progress.CurrentVolume {
					continue
				}
				for _, a := range v.Arcs {
					if a.Index == progress.CurrentArc {
						found = true
						break
					}
				}
				break
			}
			if !found {
				warnings = append(warnings, fmt.Sprintf("progress 当前 V%d A%d 在分层大纲中找不到对应条目", progress.CurrentVolume, progress.CurrentArc))
			}
		}
	}
	return warnings
}

// FoundationMissing 返回基础设定中尚缺的项，按用于 Prompt/Reminder 的稳定顺序排列。
// 长篇模式（已有 layered_outline）额外要求 compass。
func (s *Store) FoundationMissing() []string {
	var missing []string
	if p, _ := s.Outline.LoadPremise(); p == "" {
		missing = append(missing, "premise")
	}
	if o, _ := s.Outline.LoadOutline(); len(o) == 0 {
		missing = append(missing, "outline")
	}
	if c, _ := s.Characters.Load(); len(c) == 0 {
		missing = append(missing, "characters")
	}
	if r, _ := s.World.LoadWorldRules(); len(r) == 0 {
		missing = append(missing, "world_rules")
	}
	if layered, _ := s.Outline.LoadLayeredOutline(); len(layered) > 0 {
		if c, _ := s.Outline.LoadCompass(); c == nil {
			missing = append(missing, "compass")
		}
	}
	return missing
}

// Init 创建所需的子目录结构。
func (s *Store) Init() error {
	return s.Progress.io.EnsureDirs([]string{
		"chapters", "summaries", "drafts", "reviews", "meta", "meta/runtime", "meta/runtime/tasks", "meta/sessions", "meta/sessions/agents",
		"meta/adaptation", "meta/adaptation/source_chapters", "meta/adaptation/source_reports", "meta/adaptation/source_foundation_batches", "meta/adaptation/cocreate_dossier_batches", "meta/adaptation/cocreate_briefing_batches", "meta/adaptation/checks", "meta/continuation", "meta/revisions", "meta/deai", "meta/deai/checks", "meta/original_planning",
	})
}

// ── 跨域协调方法 ──

// ExpandArc 将骨架弧展开为详细章节（Outline + Progress 联动）。
func (s *Store) ExpandArc(volumeIdx, arcIdx int, chapters []domain.OutlineEntry) error {
	requestID, err := migrationRequestIdentity("expand_arc", struct {
		Volume   int
		Arc      int
		Chapters []domain.OutlineEntry
	}{Volume: volumeIdx, Arc: arcIdx, Chapters: chapters})
	if err != nil {
		return err
	}
	s.crossMu.Lock()
	defer s.crossMu.Unlock()
	return s.saveLayeredStructureMutation("expand_arc", requestID, false, func(existing []domain.VolumeOutline) ([]domain.VolumeOutline, error) {
		return s.Outline.expandArc(existing, volumeIdx, arcIdx, chapters)
	})
}

// AppendVolume 追加新卷到分层大纲末尾（Outline + Progress 联动）。
func (s *Store) AppendVolume(vol domain.VolumeOutline) error {
	requestID, err := migrationRequestIdentity("append_volume", vol)
	if err != nil {
		return err
	}
	s.crossMu.Lock()
	defer s.crossMu.Unlock()
	return s.saveLayeredStructureMutation("append_volume", requestID, false, func(existing []domain.VolumeOutline) ([]domain.VolumeOutline, error) {
		return s.Outline.appendVolume(existing, vol)
	})
}

// AppendSkeletonVolume appends one skeleton-only volume during the reviewed
// normal-original proposal stage. Writing-time AppendVolume keeps requiring an
// already expanded first arc.
func (s *Store) AppendSkeletonVolume(vol domain.VolumeOutline) error {
	requestID, err := migrationRequestIdentity("append_skeleton_volume", vol)
	if err != nil {
		return err
	}
	s.crossMu.Lock()
	defer s.crossMu.Unlock()
	return s.saveLayeredStructureMutation("append_skeleton_volume", requestID, true, func(existing []domain.VolumeOutline) ([]domain.VolumeOutline, error) {
		return s.Outline.appendSkeletonVolume(existing, vol)
	})
}

func (s *Store) saveLayeredStructureMutation(
	kind string,
	requestID string,
	markLayered bool,
	mutate func(existing []domain.VolumeOutline) ([]domain.VolumeOutline, error),
) error {
	if s.Outline.migration == nil {
		return fmt.Errorf("structure migration is required for %s", kind)
	}
	return s.Outline.migration.saveRequested(kind, requestID, func(_ structureIndex, _ bool) (structureMigrationBuild, error) {
		s.Outline.io.mu.Lock()
		defer s.Outline.io.mu.Unlock()
		s.Progress.io.mu.Lock()
		defer s.Progress.io.mu.Unlock()

		var existing []domain.VolumeOutline
		if err := s.Outline.io.ReadJSONUnlocked("layered_outline.json", &existing); err != nil {
			return structureMigrationBuild{}, fmt.Errorf("load layered_outline: %w", err)
		}
		var flat []domain.OutlineEntry
		if err := s.Outline.io.ReadJSONUnlocked("outline.json", &flat); err != nil && !os.IsNotExist(err) {
			return structureMigrationBuild{}, err
		}
		legacySource := s.Outline.identity.sourceIndex(flat, existing)
		volumes, err := mutate(existing)
		if err != nil {
			return structureMigrationBuild{}, err
		}
		progress, err := s.Progress.loadUnlocked()
		if err != nil {
			return structureMigrationBuild{}, err
		}
		if progress == nil {
			progress = &domain.Progress{}
		} else {
			progress = cloneProgress(progress)
		}
		progress.TotalChapters = domain.TotalChapters(volumes)
		if markLayered {
			progress.Layered = true
		}
		payloads, err := layeredOutlineMigrationPayloads(volumes)
		if err != nil {
			return structureMigrationBuild{}, err
		}
		progressPayload, err := jsonMigrationPayload("meta/progress.json", progress)
		if err != nil {
			return structureMigrationBuild{}, err
		}
		payloads = append(payloads, progressPayload)
		return structureMigrationBuild{
			LegacySource: legacySource,
			Target:       structureIndexFromLayered(volumes),
			Payloads:     payloads,
		}, nil
	})
}

// ClearHandledSteer 原子性清除 PendingSteer 并重置 FlowSteering 状态
// （RunMeta + Progress 联动）。
func (s *Store) ClearHandledSteer() error {
	return s.clearHandledSteerIf("")
}

// ClearHandledSteerIf 仅在 PendingSteer 仍等于 expected 时清除它。
// Resume 将干预注入 Coordinator 后调用此方法，避免清除注入期间新写入的
// 用户干预。expected 为空时保留 ClearHandledSteer 的无条件清除语义。
func (s *Store) ClearHandledSteerIf(expected string) error {
	return s.clearHandledSteerIf(expected)
}

func (s *Store) clearHandledSteerIf(expected string) error {
	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	s.RunMeta.io.mu.Lock()
	defer s.RunMeta.io.mu.Unlock()

	meta, err := s.RunMeta.loadUnlocked()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if meta != nil && meta.PendingSteer != "" && (expected == "" || meta.PendingSteer == expected) {
		meta.PendingSteer = ""
		if err := s.RunMeta.saveUnlocked(*meta); err != nil {
			return err
		}
	}

	s.Progress.io.mu.Lock()
	defer s.Progress.io.mu.Unlock()

	p, err := s.Progress.loadUnlocked()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if p != nil && p.Flow == domain.FlowSteering {
		if err := domain.ValidateFlowTransition(p.Flow, domain.FlowWriting); err != nil {
			return err
		}
		p.Flow = domain.FlowWriting
		if err := s.Progress.saveUnlocked(p); err != nil {
			return err
		}
	}
	return nil
}
