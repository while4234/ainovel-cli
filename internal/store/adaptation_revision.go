package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const adaptationRevisionRuntimeFile = adaptationRootDir + "/revision_runtime.json"
const adaptationRevisionServiceReceiptsFile = adaptationRootDir + "/revision_service_receipts.json"
const adaptationRevisionCommandLockFile = "meta/revisions/adaptation-command.lock"
const adaptationRevisionCommandJournalFile = "meta/revisions/adaptation-command-journal.json"
const adaptationRevisionCommandSnapshotDir = "meta/revisions/adaptation-command-snapshot"

var adaptationRevisionCommandLocks sync.Map

type adaptationRevisionFileSnapshot struct {
	exists bool
	data   []byte
}

type adaptationRevisionServiceReceipt struct {
	Operation   string          `json:"operation"`
	Fingerprint string          `json:"fingerprint"`
	Result      json.RawMessage `json:"result"`
}

type adaptationRevisionServiceReceiptState struct {
	Version  int                                         `json:"version"`
	Receipts map[string]adaptationRevisionServiceReceipt `json:"receipts"`
}

type adaptationRevisionCommandJournal struct {
	Version     int      `json:"version"`
	Key         string   `json:"key"`
	Operation   string   `json:"operation"`
	Fingerprint string   `json:"fingerprint"`
	Files       []string `json:"files"`
}

type AdaptationFormalSnapshot struct {
	files          map[string]adaptationRevisionFileSnapshot
	structureFiles map[string][]byte
}

var adaptationRevisionFormalFiles = []string{
	adaptationPlanFile,
	adaptationProposalFile,
	adaptationVolumeReviewFile,
	adaptationProposalRuntimeFile,
	adaptationPlanningWorkflowFile,
	adaptationAuditReportFile,
	adaptationRepairApplicationFile,
	"meta/progress.json",
}

func (s *AdaptationStore) withLegacyFormalMutation(operation string, mutation func() error) error {
	if s == nil {
		return fmt.Errorf("adaptation store is required before %s", operation)
	}
	if s.withLegacyMutation == nil {
		return mutation()
	}
	return s.withLegacyMutation(operation, s.migration, mutation)
}

func (s *AdaptationStore) saveRevisionRuntimeRaw(runtime domain.AdaptationRevisionRuntime) error {
	if err := runtime.Validate(); err != nil {
		return err
	}
	return s.io.WriteJSON(adaptationRevisionRuntimeFile, runtime)
}

func (s *AdaptationStore) LoadRevisionServiceReceipt(key, operation, fingerprint string, result any) (bool, error) {
	key = strings.TrimSpace(key)
	var state adaptationRevisionServiceReceiptState
	if err := s.io.ReadJSON(adaptationRevisionServiceReceiptsFile, &state); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	receipt, found := state.Receipts[key]
	if !found {
		return false, nil
	}
	if receipt.Operation != operation || receipt.Fingerprint != fingerprint {
		return false, ErrRevisionIdempotencyConflict
	}
	if err := json.Unmarshal(receipt.Result, result); err != nil {
		return false, fmt.Errorf("decode adaptation revision service receipt %q: %w", key, err)
	}
	return true, nil
}

func (s *AdaptationStore) HasRevisionServiceReceipt(key, operation, fingerprint string) (bool, error) {
	key = strings.TrimSpace(key)
	var state adaptationRevisionServiceReceiptState
	if err := s.io.ReadJSON(adaptationRevisionServiceReceiptsFile, &state); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	receipt, found := state.Receipts[key]
	if !found {
		return false, nil
	}
	if receipt.Operation != operation || receipt.Fingerprint != fingerprint {
		return false, ErrRevisionIdempotencyConflict
	}
	return true, nil
}

func (s *AdaptationStore) saveRevisionServiceReceiptRaw(key, operation, fingerprint string, result any) error {
	key = strings.TrimSpace(key)
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.io.WithWriteLock(func() error {
		state := adaptationRevisionServiceReceiptState{Version: 1, Receipts: make(map[string]adaptationRevisionServiceReceipt)}
		if err := s.io.ReadJSONUnlocked(adaptationRevisionServiceReceiptsFile, &state); err != nil && !os.IsNotExist(err) {
			return err
		}
		if state.Receipts == nil {
			state.Receipts = make(map[string]adaptationRevisionServiceReceipt)
		}
		if current, found := state.Receipts[key]; found {
			if current.Operation != operation || current.Fingerprint != fingerprint {
				return ErrRevisionIdempotencyConflict
			}
			return nil
		}
		state.Receipts[key] = adaptationRevisionServiceReceipt{Operation: operation, Fingerprint: fingerprint, Result: encoded}
		return s.io.WriteJSONUnlocked(adaptationRevisionServiceReceiptsFile, state)
	})
}

// SaveAdaptationRevisionServiceReceipt durably records a prepared service
// command only for the exact owner capability that currently fences it.
func (s *Store) SaveAdaptationRevisionServiceReceipt(owner *RevisionStore, key, operation, fingerprint string, result any) error {
	key, operation, fingerprint = strings.TrimSpace(key), strings.TrimSpace(operation), strings.TrimSpace(fingerprint)
	if key == "" || operation == "" || fingerprint == "" {
		return fmt.Errorf("adaptation revision service receipt identity is required")
	}
	return s.withAdaptationRevisionStateWrite(owner, func(state *revisionState) error {
		if !revisionCommandMatches(state.CommandFence, key, operation, fingerprint) ||
			!revisionCommandOwnerMatches(state.CommandFence, owner.commandOwner) {
			return ErrRevisionCommandInProgress
		}
		return s.Adaptation.saveRevisionServiceReceiptRaw(key, operation, fingerprint, result)
	})
}

// SaveAdaptationRevisionRuntime checkpoints the active adaptation revision.
// An unfenced command may use the project's ordinary RevisionStore; a prepared
// command must present its exact owner capability.
func (s *Store) SaveAdaptationRevisionRuntime(owner *RevisionStore, runtime domain.AdaptationRevisionRuntime) error {
	if err := runtime.Validate(); err != nil {
		return err
	}
	return s.withAdaptationRevisionStateWrite(owner, func(state *revisionState) error {
		if err := allowRevisionCommandMutation(state, owner.commandOwner); err != nil {
			return err
		}
		active, exists := state.Sessions[state.ActiveSessionID]
		if !exists || active.ID != runtime.SessionID || active.Mode != domain.RevisionModeAdaptation {
			return fmt.Errorf("adaptation revision runtime %q has no matching active owner", runtime.SessionID)
		}
		return s.Adaptation.saveRevisionRuntimeRaw(runtime)
	})
}

// ClearAdaptationRevisionRuntime removes the active adaptation checkpoint under
// the same prepared-command ownership rules as SaveAdaptationRevisionRuntime.
func (s *Store) ClearAdaptationRevisionRuntime(owner *RevisionStore, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	return s.withAdaptationRevisionStateWrite(owner, func(state *revisionState) error {
		if err := allowRevisionCommandMutation(state, owner.commandOwner); err != nil {
			return err
		}
		active, exists := state.Sessions[state.ActiveSessionID]
		if !exists || active.ID != sessionID || active.Mode != domain.RevisionModeAdaptation {
			return fmt.Errorf("adaptation revision %q does not own runtime cleanup", sessionID)
		}
		return s.Adaptation.clearRevisionRuntimeRaw(sessionID)
	})
}

func (s *Store) withAdaptationRevisionStateWrite(owner *RevisionStore, write func(*revisionState) error) error {
	if s == nil || s.Revisions == nil || s.Adaptation == nil || owner == nil || owner.io == nil {
		return ErrRevisionCommandInProgress
	}
	if !revisionStoresShareProject(s.Revisions, owner) {
		return ErrRevisionCommandInProgress
	}
	return s.Revisions.withRevisionTransaction(func() error {
		state, err := s.Revisions.loadUnlocked()
		if err != nil {
			return err
		}
		return write(state)
	})
}

func revisionStoresShareProject(expected, owner *RevisionStore) bool {
	if expected == nil || expected.io == nil || owner == nil || owner.io == nil {
		return false
	}
	expectedDir, expectedErr := filepath.Abs(expected.io.dir)
	ownerDir, ownerErr := filepath.Abs(owner.io.dir)
	return expectedErr == nil && ownerErr == nil && strings.EqualFold(filepath.Clean(expectedDir), filepath.Clean(ownerDir))
}

func requirePreparedAdaptationRevisionOwner(state *revisionState, owner *RevisionStore, sessionID, operation string) error {
	if state == nil || owner == nil || owner.commandOwner == nil ||
		!revisionCommandOwnerMatches(state.CommandFence, owner.commandOwner) {
		return ErrRevisionCommandInProgress
	}
	sessionID = strings.TrimSpace(sessionID)
	active, exists := state.Sessions[state.ActiveSessionID]
	if !exists || state.ActiveSessionID != sessionID || active.ID != sessionID ||
		active.Mode != domain.RevisionModeAdaptation || !active.Active() {
		return fmt.Errorf("adaptation revision %q does not own %s", sessionID, operation)
	}
	return nil
}

// WithAdaptationRevisionCommand serializes the complete adaptation revision
// service transition across Store and service instances, including processes.
// RevisionStore uses a distinct nested transaction lock for its own state.
func (s *Store) WithAdaptationRevisionCommand(fn func() error) error {
	if s == nil || s.Revisions == nil {
		return fmt.Errorf("adaptation revision store is required")
	}
	return s.withAdaptationRevisionCommandLock(func() error {
		if err := s.recoverAdaptationRevisionCommand(); err != nil {
			return err
		}
		return fn()
	})
}

// WithPreparedAdaptationRevisionCommand reserves shared revision/normal-flow
// ownership before the service snapshot is captured. The durable fence remains
// until rollback or receipt-backed cleanup has removed every recovery artifact.
func (s *Store) WithPreparedAdaptationRevisionCommand(key, operation, fingerprint string, fn func(*RevisionStore) error) error {
	if s == nil || s.Revisions == nil {
		return fmt.Errorf("adaptation revision store is required")
	}
	return s.withAdaptationRevisionCommandLock(func() error {
		if err := s.recoverAdaptationRevisionCommand(); err != nil {
			return err
		}
		owner, err := s.Revisions.claimCommandFence(key, operation, fingerprint)
		if err != nil {
			return err
		}
		commandErr := fn(owner)
		pending, pendingErr := s.adaptationRevisionCommandFilesPending()
		if pendingErr != nil || pending {
			return errors.Join(commandErr, pendingErr)
		}
		releaseErr := owner.releaseCommandFence()
		return errors.Join(commandErr, releaseErr)
	})
}

func (s *Store) withAdaptationRevisionCommandLock(fn func() error) error {
	abs, err := filepath.Abs(s.dir)
	if err != nil {
		abs = s.dir
	}
	lock, _ := adaptationRevisionCommandLocks.LoadOrStore(filepath.Clean(abs), new(sync.Mutex))
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()
	return withRevisionFileTransaction(newIO(s.dir), adaptationRevisionCommandLockFile, fn)
}

func (s *Store) PrepareAdaptationRevisionCommand(owner *RevisionStore, key, operation, fingerprint string) error {
	journal := adaptationRevisionCommandJournal{
		Version: 1, Key: strings.TrimSpace(key), Operation: strings.TrimSpace(operation), Fingerprint: strings.TrimSpace(fingerprint),
	}
	if journal.Key == "" || journal.Operation == "" || journal.Fingerprint == "" {
		return fmt.Errorf("adaptation revision command journal identity is required")
	}
	if err := s.requireAdaptationRevisionCommandOwner(owner, journal.Key, journal.Operation, journal.Fingerprint); err != nil {
		return err
	}
	io := newIO(s.dir)
	if _, err := io.RemoveAllRel(adaptationRevisionCommandSnapshotDir); err != nil {
		return err
	}
	files, err := s.captureAdaptationRevisionCommandSnapshot(io)
	if err != nil {
		_, _ = io.RemoveAllRel(adaptationRevisionCommandSnapshotDir)
		return err
	}
	journal.Files = files
	if err := io.WriteJSON(adaptationRevisionCommandJournalFile, journal); err != nil {
		_, _ = io.RemoveAllRel(adaptationRevisionCommandSnapshotDir)
		return err
	}
	return nil
}

func (s *Store) RollbackAdaptationRevisionCommand(owner *RevisionStore) error {
	io := newIO(s.dir)
	journal, err := loadAdaptationRevisionCommandJournal(io)
	if err != nil {
		return err
	}
	if journal == nil {
		return nil
	}
	if err := s.requireAdaptationRevisionCommandOwner(owner, journal.Key, journal.Operation, journal.Fingerprint); err != nil {
		return err
	}
	if err := s.restoreAdaptationRevisionCommandSnapshot(io, *journal, owner); err != nil {
		return err
	}
	return cleanupAdaptationRevisionCommand(io)
}

func (s *Store) CompleteAdaptationRevisionCommand(owner *RevisionStore, key, operation, fingerprint string) error {
	if err := s.requireAdaptationRevisionCommandOwner(owner, key, operation, fingerprint); err != nil {
		return err
	}
	found, err := s.Adaptation.HasRevisionServiceReceipt(key, operation, fingerprint)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("adaptation revision command receipt is not durable")
	}
	return cleanupAdaptationRevisionCommand(newIO(s.dir))
}

func (s *Store) requireAdaptationRevisionCommandOwner(owner *RevisionStore, key, operation, fingerprint string) error {
	if s == nil || s.Revisions == nil || s.Revisions.io == nil || owner == nil || owner.io == nil {
		return ErrRevisionCommandInProgress
	}
	storeDir, storeErr := filepath.Abs(s.Revisions.io.dir)
	ownerDir, ownerErr := filepath.Abs(owner.io.dir)
	if storeErr != nil || ownerErr != nil || !strings.EqualFold(filepath.Clean(storeDir), filepath.Clean(ownerDir)) {
		return ErrRevisionCommandInProgress
	}
	return owner.requireCommandFenceFor(key, operation, fingerprint)
}

func (s *Store) recoverAdaptationRevisionCommand() error {
	io := newIO(s.dir)
	journal, err := loadAdaptationRevisionCommandJournal(io)
	if err != nil {
		return fmt.Errorf("recover adaptation revision command journal: %w", err)
	}
	if journal == nil {
		if _, err := io.RemoveAllRel(adaptationRevisionCommandSnapshotDir); err != nil {
			return err
		}
		owner, err := s.Revisions.currentCommandOwner()
		if err != nil || owner == nil {
			return err
		}
		return owner.releaseCommandFence()
	}
	owner, err := s.Revisions.claimCommandFence(journal.Key, journal.Operation, journal.Fingerprint)
	if err != nil {
		return fmt.Errorf("recover adaptation revision command ownership: %w", err)
	}
	committed, receiptErr := s.Adaptation.HasRevisionServiceReceipt(journal.Key, journal.Operation, journal.Fingerprint)
	if receiptErr == nil && committed {
		if err := cleanupAdaptationRevisionCommand(io); err != nil {
			return err
		}
		return owner.releaseCommandFence()
	}
	if err := s.restoreAdaptationRevisionCommandSnapshot(io, *journal, owner); err != nil {
		return fmt.Errorf("roll back interrupted adaptation revision command: %w", err)
	}
	if err := cleanupAdaptationRevisionCommand(io); err != nil {
		return err
	}
	if receiptErr != nil && !errors.Is(receiptErr, ErrRevisionIdempotencyConflict) {
		return fmt.Errorf("read interrupted adaptation revision command receipt: %w", receiptErr)
	}
	return owner.releaseCommandFence()
}

func (s *Store) adaptationRevisionCommandPending() (bool, error) {
	pending, err := s.adaptationRevisionCommandFilesPending()
	if err != nil || pending {
		return pending, err
	}
	if _, err := os.Stat(newIO(s.dir).path(revisionStateFile)); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	fence, err := s.Revisions.currentCommandFence()
	return fence != nil, err
}

func (s *Store) adaptationRevisionCommandFilesPending() (bool, error) {
	io := newIO(s.dir)
	for _, rel := range []string{adaptationRevisionCommandJournalFile, adaptationRevisionCommandSnapshotDir} {
		if _, err := os.Stat(io.path(rel)); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
}

func (s *Store) captureAdaptationRevisionCommandSnapshot(io *IO) ([]string, error) {
	files := make([]string, 0)
	for _, rel := range adaptationRevisionCommandTrackedFiles() {
		data, err := io.ReadFile(rel)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if err := writeAdaptationRevisionCommandFile(io, filepath.ToSlash(filepath.Join(adaptationRevisionCommandSnapshotDir, filepath.FromSlash(rel))), data); err != nil {
			return nil, err
		}
		files = append(files, rel)
	}
	root := io.path(structureRootDir)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := writeAdaptationRevisionCommandFile(io, filepath.ToSlash(filepath.Join(adaptationRevisionCommandSnapshotDir, filepath.FromSlash(rel))), data); err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}

func (s *Store) restoreAdaptationRevisionCommandSnapshot(io *IO, journal adaptationRevisionCommandJournal, owner *RevisionStore) error {
	if journal.Version != 1 || journal.Key == "" || journal.Operation == "" || journal.Fingerprint == "" {
		return fmt.Errorf("invalid adaptation revision command journal")
	}
	if owner == nil {
		return ErrRevisionCommandInProgress
	}
	if err := owner.requireCommandFence(); err != nil {
		return err
	}
	fence, err := owner.currentCommandFence()
	if err != nil {
		return err
	}
	if !revisionCommandMatches(fence, journal.Key, journal.Operation, journal.Fingerprint) || !revisionCommandOwnerMatches(fence, owner.commandOwner) {
		return ErrRevisionCommandInProgress
	}
	if _, err := io.RemoveAllRel(structureRootDir); err != nil {
		return err
	}
	existing := make(map[string]bool, len(journal.Files))
	for _, rel := range journal.Files {
		existing[rel] = true
	}
	for _, rel := range adaptationRevisionCommandTrackedFiles() {
		if existing[rel] {
			continue
		}
		if rel == revisionStateFile {
			state := newRevisionState()
			state.CommandFence = fence
			if err := io.WriteJSON(revisionStateFile, state); err != nil {
				return err
			}
			continue
		}
		if err := io.RemoveFile(rel); err != nil {
			return err
		}
	}
	for _, rel := range journal.Files {
		data, err := io.ReadFile(filepath.ToSlash(filepath.Join(adaptationRevisionCommandSnapshotDir, filepath.FromSlash(rel))))
		if err != nil {
			return fmt.Errorf("read adaptation revision command snapshot %s: %w", rel, err)
		}
		if rel == revisionStateFile {
			var state revisionState
			if err := json.Unmarshal(data, &state); err != nil {
				return fmt.Errorf("decode adaptation revision command snapshot %s: %w", rel, err)
			}
			state.CommandFence = fence
			data, err = json.MarshalIndent(state, "", "  ")
			if err != nil {
				return err
			}
			data = append(data, '\n')
		}
		if err := writeAdaptationRevisionCommandFile(io, rel, data); err != nil {
			return fmt.Errorf("restore adaptation revision command snapshot %s: %w", rel, err)
		}
	}
	return nil
}

func adaptationRevisionCommandTrackedFiles() []string {
	files := append([]string(nil), adaptationRevisionFormalFiles...)
	files = append(files, adaptationRevisionRuntimeFile, adaptationRevisionServiceReceiptsFile, revisionStateFile)
	slices.Sort(files)
	return slices.Compact(files)
}

func loadAdaptationRevisionCommandJournal(io *IO) (*adaptationRevisionCommandJournal, error) {
	var journal adaptationRevisionCommandJournal
	if err := io.ReadJSON(adaptationRevisionCommandJournalFile, &journal); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &journal, nil
}

func cleanupAdaptationRevisionCommand(io *IO) error {
	if err := io.RemoveFile(adaptationRevisionCommandJournalFile); err != nil {
		return err
	}
	_, err := io.RemoveAllRel(adaptationRevisionCommandSnapshotDir)
	return err
}

func writeAdaptationRevisionCommandFile(io *IO, rel string, data []byte) error {
	return io.WithWriteLock(func() error { return io.WriteFileUnlocked(rel, data) })
}

func (s *AdaptationStore) LoadRevisionRuntime() (*domain.AdaptationRevisionRuntime, error) {
	var runtime domain.AdaptationRevisionRuntime
	if err := s.io.ReadJSON(adaptationRevisionRuntimeFile, &runtime); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := runtime.Validate(); err != nil {
		return nil, err
	}
	return &runtime, nil
}

func (s *Store) SaveAdaptationPlanForRevision(owner *RevisionStore, plan domain.AdaptationPlan, sessionID string) error {
	return s.withAdaptationRevisionStateWrite(owner, func(state *revisionState) error {
		if err := requirePreparedAdaptationRevisionOwner(state, owner, sessionID, "the formal plan write"); err != nil {
			return err
		}
		return s.Adaptation.savePlan(plan)
	})
}

func (s *Store) RestoreAdaptationPlanForRevision(owner *RevisionStore, plan domain.AdaptationPlan, sessionID string) error {
	return s.withAdaptationRevisionStateWrite(owner, func(state *revisionState) error {
		if err := requirePreparedAdaptationRevisionOwner(state, owner, sessionID, "the rollback plan"); err != nil {
			return err
		}
		return s.Adaptation.savePlan(plan)
	})
}

func (s *AdaptationStore) clearRevisionRuntimeRaw(sessionID string) error {
	runtime, err := s.LoadRevisionRuntime()
	if err != nil {
		return err
	}
	if runtime != nil && runtime.SessionID != strings.TrimSpace(sessionID) {
		return fmt.Errorf("adaptation revision runtime belongs to %s", runtime.SessionID)
	}
	return s.io.RemoveFile(adaptationRevisionRuntimeFile)
}

func (s *Store) CaptureAdaptationFormalSnapshot() (*AdaptationFormalSnapshot, error) {
	if s == nil {
		return nil, fmt.Errorf("store is required")
	}
	io := newIO(s.dir)
	snapshot := &AdaptationFormalSnapshot{
		files:          make(map[string]adaptationRevisionFileSnapshot, len(adaptationRevisionFormalFiles)),
		structureFiles: make(map[string][]byte),
	}
	for _, path := range adaptationRevisionFormalFiles {
		data, err := io.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				snapshot.files[path] = adaptationRevisionFileSnapshot{}
				continue
			}
			return nil, err
		}
		snapshot.files[path] = adaptationRevisionFileSnapshot{exists: true, data: append([]byte(nil), data...)}
	}
	structureRoot := io.path(structureRootDir)
	if err := filepath.WalkDir(structureRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot.structureFiles[filepath.ToSlash(rel)] = append([]byte(nil), data...)
		return nil
	}); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return snapshot, nil
}

func (s *Store) RestoreAdaptationFormalSnapshot(owner *RevisionStore, snapshot *AdaptationFormalSnapshot, sessionID string) error {
	if s == nil || snapshot == nil {
		return fmt.Errorf("adaptation formal snapshot is required")
	}
	return s.withAdaptationRevisionStateWrite(owner, func(state *revisionState) error {
		if err := requirePreparedAdaptationRevisionOwner(state, owner, sessionID, "formal snapshot restore"); err != nil {
			return err
		}
		io := newIO(s.dir)
		return io.WithWriteLock(func() error {
			if _, err := io.RemoveAllRelUnlocked(structureRootDir); err != nil {
				return err
			}
			for path, data := range snapshot.structureFiles {
				if err := io.WriteFileUnlocked(path, data); err != nil {
					return err
				}
			}
			for _, path := range adaptationRevisionFormalFiles {
				file := snapshot.files[path]
				if file.exists {
					if err := io.WriteFileUnlocked(path, file.data); err != nil {
						return err
					}
					continue
				}
				if err := io.RemoveFileUnlocked(path); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func (s *Store) ClearAdaptationRevisionAudits(owner *RevisionStore, sessionID string) error {
	if s == nil {
		return fmt.Errorf("store is required")
	}
	return s.withAdaptationRevisionStateWrite(owner, func(state *revisionState) error {
		if err := requirePreparedAdaptationRevisionOwner(state, owner, sessionID, "revision audit cleanup"); err != nil {
			return err
		}
		io := newIO(s.dir)
		return io.WithWriteLock(func() error {
			if err := io.RemoveFileUnlocked(adaptationAuditReportFile); err != nil {
				return err
			}
			return io.RemoveFileUnlocked(adaptationRepairApplicationFile)
		})
	})
}

func (s *Store) SaveAdaptationRevisionProgress(owner *RevisionStore, progress *domain.Progress, sessionID string) error {
	if progress == nil {
		return fmt.Errorf("adaptation revision progress is required")
	}
	return s.withAdaptationRevisionStateWrite(owner, func(state *revisionState) error {
		if err := requirePreparedAdaptationRevisionOwner(state, owner, sessionID, "formal progress write"); err != nil {
			return err
		}
		return s.Progress.saveOwned(cloneProgress(progress))
	})
}
