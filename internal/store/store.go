package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// Store 是状态管理的组合根，持有所有子存储。
type Store struct {
	dir string

	recoveryMu             sync.Mutex
	recoveryErr            error
	commandRecoveryErr     error
	publicationRecoveryErr error

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
	revisions := NewRevisionStore(dir)
	migration.recoverWithRevisionFence = revisions.withLegacyMutation
	io := newIO(dir)
	outline := NewOutlineStore(io, identity, migration)
	outline.withLegacyMutation = revisions.withLegacyMigrationMutation
	adaptation := NewAdaptationStore(newIO(dir), identity, migration)
	adaptation.withLegacyMutation = revisions.withLegacyMigrationMutation
	store := &Store{
		dir:                    dir,
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
		Sessions:               NewSessionStore(newIO(dir)),
		Usage:                  NewUsageStore(newIO(dir)),
		Simulation:             NewSimulationStore(newIO(dir)),
		DeAI:                   NewDeAIStore(newIO(dir)),
		Adaptation:             adaptation,
		Continuation:           NewContinuationStore(newIO(dir), migration),
		Revisions:              revisions,
		OriginalPlanningAudits: NewOriginalPlanningAuditStore(newIO(dir), outline),
	}
	store.Progress.withLegacyMutation = revisions.withLegacyMigrationMutation
	// Recover the outer service journal before the inner structure journal. The
	// outer snapshot may intentionally restore a pending structure generation.
	commandRecoveryPending, commandRecoveryErr := store.adaptationRevisionCommandPending()
	if commandRecoveryErr == nil && commandRecoveryPending {
		commandRecoveryErr = store.WithAdaptationRevisionCommand(func() error { return nil })
	}
	publicationRecoveryErr := store.recoverNormalRevisionPublication()
	structureRecoveryErr := recoverStructureMigrationIfPending(revisions, migration, "recover pending structure migration during startup")
	store.commandRecoveryErr = commandRecoveryErr
	store.publicationRecoveryErr = publicationRecoveryErr
	store.recoveryErr = errors.Join(commandRecoveryErr, publicationRecoveryErr, structureRecoveryErr)
	// Recover before constructing cache-bearing sub-stores such as checkpoints;
	// otherwise they could retain the pre-transaction file generation in memory.
	store.Checkpoints = newCheckpointStore(io, store.recoveryErr == nil)
	return store
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
	if s.commandRecoveryErr != nil {
		if err := s.WithAdaptationRevisionCommand(func() error { return nil }); err != nil {
			s.commandRecoveryErr = err
			s.recoveryErr = err
			return err
		}
		s.commandRecoveryErr = nil
	}
	if s.publicationRecoveryErr != nil {
		if err := s.recoverNormalRevisionPublication(); err != nil {
			s.publicationRecoveryErr = err
			s.recoveryErr = err
			return err
		}
		s.publicationRecoveryErr = nil
	}
	if err := recoverStructureMigrationIfPending(s.Revisions, s.Outline.migration, "recover pending structure migration"); err != nil {
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

func recoverStructureMigrationIfPending(revisions *RevisionStore, migration *structureMigration, operation string) error {
	pending, err := migration.pending()
	if err != nil || !pending {
		return err
	}
	return revisions.withLegacyMigrationMutation(operation, migration, func() error { return nil })
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
	return s.Revisions.withLegacyMigrationMutation("expand adaptation arc", s.Outline.migration, func() error {
		s.crossMu.Lock()
		defer s.crossMu.Unlock()
		return s.saveLayeredStructureMutation("expand_arc", requestID, false, false, nil, func(existing []domain.VolumeOutline) ([]domain.VolumeOutline, error) {
			return s.Outline.expandArc(existing, volumeIdx, arcIdx, chapters)
		})
	})
}

// AppendVolume 追加新卷到分层大纲末尾（Outline + Progress 联动）。
func (s *Store) AppendVolume(vol domain.VolumeOutline) error {
	requestID, err := migrationRequestIdentity("append_volume", vol)
	if err != nil {
		return err
	}
	return s.Revisions.withLegacyMigrationMutation("append adaptation volume", s.Outline.migration, func() error {
		s.crossMu.Lock()
		defer s.crossMu.Unlock()
		return s.saveLayeredStructureMutation("append_volume", requestID, false, true, nil, func(existing []domain.VolumeOutline) ([]domain.VolumeOutline, error) {
			return s.Outline.appendVolume(existing, vol)
		})
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
	return s.Revisions.withLegacyMigrationMutation("append adaptation skeleton volume", s.Outline.migration, func() error {
		s.crossMu.Lock()
		defer s.crossMu.Unlock()
		return s.saveLayeredStructureMutation("append_skeleton_volume", requestID, true, false, nil, func(existing []domain.VolumeOutline) ([]domain.VolumeOutline, error) {
			return s.Outline.appendSkeletonVolume(existing, vol)
		})
	})
}

// ReplaceLayeredStructureForRevision applies a kernel-confirmed normal
// revision candidate through the same crash-recoverable structure migration as
// incremental edits. The opaque publication owner proves that the exact normal
// revision passed every human and audit gate.
func (s *Store) ReplaceLayeredStructureForRevision(owner *RevisionPublicationOwner, candidate []domain.VolumeOutline) error {
	if owner == nil {
		return ErrRevisionCommandInProgress
	}
	return s.PublishLayeredStructureForRevision(owner, candidate, owner.publishKey)
}

func (s *Store) PublishLayeredStructureForRevision(owner *RevisionPublicationOwner, candidate []domain.VolumeOutline, commandKey string) error {
	candidateDigest, err := normalStructureDigest(candidate)
	if err != nil {
		return err
	}
	if owner == nil || strings.TrimSpace(commandKey) != owner.publishKey {
		return ErrRevisionCommandInProgress
	}
	snapshot, err := s.captureNormalRevisionFormalSnapshot()
	if err != nil {
		return err
	}
	requestID, err := migrationRequestIdentity("normal_revision_publish", struct {
		Candidate  []domain.VolumeOutline
		CommandKey string
		OwnerToken string
	}{candidate, strings.TrimSpace(commandKey), owner.token})
	if err != nil {
		return err
	}
	cloned := domain.CloneStructureSnapshot(candidate)
	return s.beginNormalRevisionPublication(owner, candidateDigest, snapshot, func() error {
		s.crossMu.Lock()
		defer s.crossMu.Unlock()
		return s.saveLayeredStructureMutation("normal_revision_publish", requestID, true, true, nil, func([]domain.VolumeOutline) ([]domain.VolumeOutline, error) {
			return domain.CloneStructureSnapshot(cloned), nil
		})
	})
}

// RestoreLayeredStructureForRevision restores only the durable prepublication
// snapshot bound to owner. Non-nil values are compatibility assertions: they
// must identify that exact snapshot and can never substitute rollback data.
func (s *Store) RestoreLayeredStructureForRevision(owner *RevisionPublicationOwner, candidate []domain.VolumeOutline, progress *domain.Progress) error {
	return s.rollbackNormalRevisionPublication(owner, candidate, progress)
}

func (s *Store) RollbackLayeredStructureForRevision(owner *RevisionPublicationOwner) error {
	return s.rollbackNormalRevisionPublication(owner, nil, nil)
}

func (s *Store) captureNormalRevisionFormalSnapshot() (normalRevisionFormalSnapshot, error) {
	structure, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		return normalRevisionFormalSnapshot{}, err
	}
	progress, err := s.Progress.Load()
	if err != nil {
		return normalRevisionFormalSnapshot{}, err
	}
	snapshot := normalRevisionFormalSnapshot{
		Structure: domain.CloneStructureSnapshot(structure),
		Progress:  cloneProgress(progress),
	}
	snapshot.Digest, err = normalRevisionFormalSnapshotDigest(snapshot.Structure, snapshot.Progress)
	return snapshot, err
}

func normalRevisionFormalSnapshotDigest(structure []domain.VolumeOutline, progress *domain.Progress) (string, error) {
	payload, err := json.Marshal(struct {
		Structure []domain.VolumeOutline `json:"structure"`
		Progress  *domain.Progress       `json:"progress,omitempty"`
	}{domain.CloneStructureSnapshot(structure), cloneProgress(progress)})
	if err != nil {
		return "", err
	}
	return domain.ContentSignature(payload), nil
}

func cloneNormalRevisionFormalSnapshot(snapshot normalRevisionFormalSnapshot) normalRevisionFormalSnapshot {
	return normalRevisionFormalSnapshot{
		Structure: domain.CloneStructureSnapshot(snapshot.Structure),
		Progress:  cloneProgress(snapshot.Progress),
		Digest:    snapshot.Digest,
	}
}

func (s *Store) requireNormalRevisionPublicationOwner(state *revisionState, owner *RevisionPublicationOwner, candidateDigest string) (domain.RevisionSession, error) {
	if s == nil || s.Revisions == nil || owner == nil || owner.revisions == nil ||
		!revisionStoresShareProject(s.Revisions, owner.revisions) {
		return domain.RevisionSession{}, ErrRevisionCommandInProgress
	}
	if state == nil || state.NormalLease != nil || state.CommandFence != nil || state.ActiveSessionID != owner.sessionID {
		return domain.RevisionSession{}, ErrRevisionCommandInProgress
	}
	session, exists := state.Sessions[owner.sessionID]
	if !exists || !session.Active() || session.Revision != owner.expectedRevision ||
		session.Mode != domain.RevisionModeNormal || session.Mode != owner.mode ||
		session.PolicyID != owner.policyID || session.PolicyVersion != owner.policyVersion {
		return domain.RevisionSession{}, ErrRevisionNotFound
	}
	if state.Generation != owner.generation || session.Generation != owner.generation {
		return domain.RevisionSession{}, &RevisionConflictError{Expected: int(owner.generation), Actual: int(state.Generation)}
	}
	versions, err := prepareRevisionPublish(owner.policy, state, &session)
	if err != nil {
		return domain.RevisionSession{}, err
	}
	ids, acceptedDigest, canonicalDigest, err := publicationBinding(state, &session, versions)
	if err != nil {
		return domain.RevisionSession{}, err
	}
	if !slices.Equal(ids, owner.acceptedVersionIDs) || acceptedDigest != owner.acceptedDigest ||
		canonicalDigest != owner.candidateDigest || candidateDigest != owner.candidateDigest {
		return domain.RevisionSession{}, fmt.Errorf("normal publication candidate or accepted artifacts changed")
	}
	return session, nil
}

func (s *Store) beginNormalRevisionPublication(owner *RevisionPublicationOwner, candidateDigest string, snapshot normalRevisionFormalSnapshot, write func() error) error {
	return s.Revisions.withRevisionTransaction(func() error {
		state, err := s.Revisions.loadUnlocked()
		if err != nil {
			return err
		}
		if state.Publication != nil {
			return ErrRevisionCommandInProgress
		}
		session, err := s.requireNormalRevisionPublicationOwner(state, owner, candidateDigest)
		if err != nil {
			return err
		}
		digest, err := normalRevisionFormalSnapshotDigest(snapshot.Structure, snapshot.Progress)
		if err != nil || digest != snapshot.Digest {
			return errors.Join(fmt.Errorf("normal prepublication snapshot changed"), err)
		}
		state.Generation++
		session.Generation = state.Generation
		state.Sessions[session.ID] = session
		owner.generation = state.Generation
		state.Publication = &revisionPublicationAttempt{
			Token: owner.token, SessionID: owner.sessionID, ExpectedRevision: owner.expectedRevision,
			Generation: state.Generation, Mode: owner.mode, PolicyID: owner.policyID, PolicyVersion: owner.policyVersion,
			PublishKey: owner.publishKey, PublishFingerprint: owner.publishFingerprint,
			AcceptedVersionIDs: append([]string(nil), owner.acceptedVersionIDs...),
			AcceptedDigest:     owner.acceptedDigest, CandidateDigest: owner.candidateDigest,
			Status: revisionPublicationPrepared, PrepublishSnapshot: cloneNormalRevisionFormalSnapshot(snapshot),
		}
		if err := validateRevisionState(state); err != nil {
			return err
		}
		if err := s.Revisions.io.WriteJSON(revisionStateFile, state); err != nil {
			return err
		}
		if err := write(); err != nil {
			return err
		}
		state.Publication.Status = revisionPublicationApplied
		return s.Revisions.io.WriteJSON(revisionStateFile, state)
	})
}

func (s *Store) rollbackNormalRevisionPublication(owner *RevisionPublicationOwner, assertedStructure []domain.VolumeOutline, assertedProgress *domain.Progress) error {
	if s == nil || s.Revisions == nil || owner == nil || owner.revisions == nil ||
		!revisionStoresShareProject(s.Revisions, owner.revisions) {
		return ErrRevisionCommandInProgress
	}
	return s.Revisions.withRevisionTransaction(func() error {
		state, err := s.Revisions.loadUnlocked()
		if err != nil {
			return err
		}
		attempt := state.Publication
		if !revisionPublicationOwnerMatches(attempt, owner) ||
			(attempt.Status != revisionPublicationPrepared && attempt.Status != revisionPublicationApplied) ||
			state.NormalLease != nil || state.CommandFence != nil || state.ActiveSessionID != attempt.SessionID ||
			state.Generation != attempt.Generation {
			return ErrRevisionCommandInProgress
		}
		session, exists := state.Sessions[attempt.SessionID]
		if !exists || !session.Active() || session.Revision != attempt.ExpectedRevision ||
			session.Generation != attempt.Generation || session.Mode != domain.RevisionModeNormal {
			return ErrRevisionCommandInProgress
		}
		digest, err := normalRevisionFormalSnapshotDigest(attempt.PrepublishSnapshot.Structure, attempt.PrepublishSnapshot.Progress)
		if err != nil || digest != attempt.PrepublishSnapshot.Digest {
			return errors.Join(fmt.Errorf("normal prepublication snapshot is invalid"), err)
		}
		if assertedStructure != nil || assertedProgress != nil {
			assertedDigest, err := normalRevisionFormalSnapshotDigest(assertedStructure, assertedProgress)
			if err != nil || assertedDigest != attempt.PrepublishSnapshot.Digest {
				return errors.Join(fmt.Errorf("normal rollback snapshot substitution is not allowed"), err)
			}
		}
		if err := s.restoreNormalRevisionFormalSnapshot(attempt); err != nil {
			return err
		}
		return s.clearNormalRevisionPublicationAttempt(state)
	})
}

func (s *Store) restoreNormalRevisionFormalSnapshot(attempt *revisionPublicationAttempt) error {
	snapshot := cloneNormalRevisionFormalSnapshot(attempt.PrepublishSnapshot)
	requestID, err := migrationRequestIdentity("normal_revision_rollback", struct {
		Token  string
		Digest string
	}{attempt.Token, snapshot.Digest})
	if err != nil {
		return err
	}
	s.crossMu.Lock()
	defer s.crossMu.Unlock()
	return s.saveLayeredStructureMutation("normal_revision_rollback", requestID, snapshot.Progress != nil && snapshot.Progress.Layered, false, snapshot.Progress, func([]domain.VolumeOutline) ([]domain.VolumeOutline, error) {
		return domain.CloneStructureSnapshot(snapshot.Structure), nil
	})
}

func (s *Store) clearNormalRevisionPublicationAttempt(state *revisionState) error {
	attempt := state.Publication
	if attempt == nil {
		return ErrRevisionCommandInProgress
	}
	state.Publication = nil
	state.Generation++
	if session, exists := state.Sessions[attempt.SessionID]; exists && session.Active() {
		session.Generation = state.Generation
		state.Sessions[session.ID] = session
	}
	if err := validateRevisionState(state); err != nil {
		return err
	}
	return s.Revisions.io.WriteJSON(revisionStateFile, state)
}

func (s *Store) recoverNormalRevisionPublication() error {
	if s == nil || s.Revisions == nil {
		return nil
	}
	if _, err := os.Stat(s.Revisions.io.path(revisionStateFile)); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return s.Revisions.withRevisionTransaction(func() error {
		state, err := s.Revisions.loadUnlocked()
		if err != nil || state.Publication == nil {
			return err
		}
		attempt := state.Publication
		if state.NormalLease != nil || state.CommandFence != nil || state.ActiveSessionID != attempt.SessionID ||
			state.Generation != attempt.Generation {
			return ErrRevisionCommandInProgress
		}
		if err := s.restoreNormalRevisionFormalSnapshot(attempt); err != nil {
			return fmt.Errorf("recover interrupted normal publication: %w", err)
		}
		return s.clearNormalRevisionPublicationAttempt(state)
	})
}

func (s *Store) saveLayeredStructureMutation(
	kind string,
	requestID string,
	markLayered bool,
	reopenCompletedExpansion bool,
	progressOverride *domain.Progress,
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
		progress := cloneProgress(progressOverride)
		if progress == nil {
			progress, err = s.Progress.loadUnlocked()
			if err != nil {
				return structureMigrationBuild{}, err
			}
			if progress == nil {
				progress = &domain.Progress{}
			} else {
				progress = cloneProgress(progress)
			}
		}
		progress.TotalChapters = domain.TotalChapters(volumes)
		if markLayered {
			progress.Layered = true
		}
		if reopenCompletedExpansion && progress.Phase == domain.PhaseComplete {
			progress.Phase = domain.PhaseWriting
			progress.Flow = domain.FlowWriting
			progress.ReopenedFromComplete = false
			progress.CompletionAuditStatus = ""
			progress.CompletionAuditReportDigest = ""
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
	return s.Revisions.withLegacyMutation("clear handled steering progress", func() error {
		return s.clearHandledSteerIfOwned(expected)
	})
}

func (s *Store) clearHandledSteerIfOwned(expected string) error {
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
