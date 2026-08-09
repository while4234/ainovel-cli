package artwork

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type applyJournal struct {
	SchemaVersion int        `json:"schema_version"`
	Operation     string     `json:"operation"`
	State         ApplyState `json:"state"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type deleteJournal struct {
	SchemaVersion int       `json:"schema_version"`
	Operation     string    `json:"operation"`
	Asset         Asset     `json:"asset"`
	TrashName     string    `json:"trash_name"`
	Phase         string    `json:"phase"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (s *WorkspaceStore) ApplyAsset(id string) (ApplyState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, err := s.readCommittedAssetUnlocked(id)
	if err != nil {
		return ApplyState{}, err
	}
	if err := verifyImageFile(filepath.Join(s.root, "images", asset.FileName), asset); err != nil {
		return ApplyState{}, errors.New("managed artwork image is unavailable")
	}
	states, err := s.readAppliedUnlocked()
	if err != nil {
		return ApplyState{}, err
	}
	target := applyTarget(asset)
	for _, existing := range states {
		if existing.Target == target && existing.AssetID == asset.ID {
			return existing, nil
		}
	}
	state := ApplyState{
		SchemaVersion: WorkspaceSchemaVersion, Target: target,
		AssetID: asset.ID, AppliedAt: s.now(),
	}
	journal := applyJournal{SchemaVersion: WorkspaceSchemaVersion, Operation: "apply", State: state, UpdatedAt: s.now()}
	journalID := "apply-" + deterministicID("op", state.Target)
	journalPath, _ := s.path("journals", journalID)
	if err := writeJSONAtomic(journalPath, journal, false); err != nil {
		return ApplyState{}, err
	}
	if err := s.injectFault("apply_after_journal"); err != nil {
		return ApplyState{}, err
	}
	statePath := filepath.Join(s.root, "apply", deterministicID("target", state.Target)+".json")
	if err := writeJSONAtomic(statePath, state, false); err != nil {
		return ApplyState{}, err
	}
	if err := s.injectFault("apply_after_state"); err != nil {
		return ApplyState{}, err
	}
	if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
		return ApplyState{}, err
	}
	if err := syncDirectory(filepath.Dir(journalPath)); err != nil {
		return ApplyState{}, err
	}
	return state, nil
}

func applyTarget(asset Asset) string {
	return string(asset.WorkType) + ":" + asset.Scope + ":" + asset.ScopeID
}

func (s *WorkspaceStore) ListApplied() ([]ApplyState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readAppliedUnlocked()
}

func (s *WorkspaceStore) readAppliedUnlocked() ([]ApplyState, error) {
	paths, err := jsonRecordPaths(filepath.Join(s.root, "apply"))
	if err != nil {
		return nil, err
	}
	states := make([]ApplyState, 0, len(paths))
	for _, path := range paths {
		var state ApplyState
		if err := readJSONFile(path, &state); err != nil {
			return nil, err
		}
		if state.SchemaVersion != WorkspaceSchemaVersion || state.Target == "" || validateRecordID(state.AssetID) != nil || state.AppliedAt.IsZero() {
			return nil, errors.New("artwork apply state is invalid")
		}
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Target < states[j].Target })
	return states, nil
}

func (s *WorkspaceStore) isAssetAppliedUnlocked(id string) (bool, error) {
	states, err := s.readAppliedUnlocked()
	if err != nil {
		return false, err
	}
	for _, state := range states {
		if state.AssetID == id {
			return true, nil
		}
	}
	return false, nil
}

func (s *WorkspaceStore) DeleteAsset(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, err := s.readCommittedAssetUnlocked(id)
	if err != nil {
		return err
	}
	applied, err := s.isAssetAppliedUnlocked(id)
	if err != nil {
		return err
	}
	if applied {
		return ErrAppliedAsset
	}
	trashName := asset.FileName + ".delete"
	journal := deleteJournal{
		SchemaVersion: WorkspaceSchemaVersion, Operation: "delete", Asset: asset,
		TrashName: trashName, Phase: "prepared", UpdatedAt: s.now(),
	}
	journalPath, _ := s.path("journals", "delete-"+asset.ID)
	if err := writeJSONAtomic(journalPath, journal, false); err != nil {
		return err
	}
	if err := s.injectFault("delete_after_journal"); err != nil {
		return err
	}
	imagePath := filepath.Join(s.root, "images", asset.FileName)
	trashPath := filepath.Join(s.root, "staging", trashName)
	if err := ensureContained(s.root, imagePath); err != nil {
		return err
	}
	if err := os.Chmod(imagePath, 0o600); err != nil {
		return err
	}
	if err := os.Rename(imagePath, trashPath); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(imagePath)); err != nil {
		return err
	}
	journal.Phase = "image_staged"
	journal.UpdatedAt = s.now()
	if err := writeJSONAtomic(journalPath, journal, false); err != nil {
		return err
	}
	if err := s.injectFault("delete_after_image_rename"); err != nil {
		return err
	}
	assetPath, _ := s.path("assets", asset.ID)
	if err := os.Remove(assetPath); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(assetPath)); err != nil {
		return err
	}
	journal.Phase = "metadata_deleted"
	journal.UpdatedAt = s.now()
	if err := writeJSONAtomic(journalPath, journal, false); err != nil {
		return err
	}
	if err := s.injectFault("delete_after_metadata"); err != nil {
		return err
	}
	if err := os.Remove(trashPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDirectory(filepath.Dir(journalPath))
}

func (s *WorkspaceStore) Workspace(limit int) (WorkspaceSnapshot, error) {
	drafts, err := s.ListDrafts("", limit)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	jobs, err := s.ListJobs("", limit)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	promptJobs, err := s.ListPromptJobs("", limit, "")
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	assets, err := s.ListAssets("", limit)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	applied, err := s.ListApplied()
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	return WorkspaceSnapshot{SchemaVersion: WorkspaceSchemaVersion, Drafts: drafts, PromptJobs: promptJobs, Jobs: jobs, Assets: assets, Applied: applied}, nil
}

type ReconcileResult struct {
	ResumedQueued       []string
	InterruptedRunning  int
	FinalizedAssets     int
	RemovedOrphans      int
	RemovedMissingAsset int
}

func (s *WorkspaceStore) Reconcile() (ReconcileResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.initializeUnlocked(); err != nil {
		return ReconcileResult{}, err
	}
	result := ReconcileResult{}
	paths, err := jsonRecordPaths(filepath.Join(s.root, "journals"))
	if err != nil {
		return result, err
	}
	for _, path := range paths {
		name := filepath.Base(path)
		switch {
		case strings.HasPrefix(name, "finalize-"):
			finalized, err := s.reconcileFinalizeJournalUnlocked(path)
			if err != nil {
				return result, err
			}
			if finalized {
				result.FinalizedAssets++
			}
		case strings.HasPrefix(name, "apply-"):
			if err := s.reconcileApplyJournalUnlocked(path); err != nil {
				return result, err
			}
		case strings.HasPrefix(name, "delete-"):
			if err := s.reconcileDeleteJournalUnlocked(path); err != nil {
				return result, err
			}
		default:
			return result, fmt.Errorf("unknown artwork operation journal %q", name)
		}
	}
	if err := s.reconcileCommittedAssetsUnlocked(&result); err != nil {
		return result, err
	}
	jobs, err := s.readAllJobsUnlocked()
	if err != nil {
		return result, err
	}
	promptJobs, err := s.readAllPromptJobsUnlocked()
	if err != nil {
		return result, err
	}
	for _, job := range promptJobs {
		if job.Status != PromptJobQueued && job.Status != PromptJobRunning {
			continue
		}
		draft, draftErr := s.readDraftUnlocked(job.DraftID)
		if draftErr == nil && draft.CurrentPromptJobID == job.ID && validateRecordID(draft.CurrentPromptVersionID) == nil {
			if _, promptErr := s.readPromptVersionUnlocked(draft.CurrentPromptVersionID); promptErr == nil {
				now := s.now()
				job.Status = PromptJobSucceeded
				job.PromptVersionID = draft.CurrentPromptVersionID
				job.ErrorCode = ""
				job.UpdatedAt = now
				job.FinishedAt = &now
				path, _ := s.path("prompt-jobs", job.ID)
				if err := writeJSONAtomic(path, job, false); err != nil {
					return result, err
				}
				continue
			}
		}
		now := s.now()
		job.Status = PromptJobFailed
		job.ErrorCode = "prompt_generation_interrupted"
		job.UpdatedAt = now
		job.FinishedAt = &now
		path, _ := s.path("prompt-jobs", job.ID)
		if err := writeJSONAtomic(path, job, false); err != nil {
			return result, err
		}
	}
	for _, job := range jobs {
		switch job.Status {
		case JobQueued:
			result.ResumedQueued = append(result.ResumedQueued, job.ID)
		case JobRunning:
			if err := s.completeJobFailureUnlocked(job, JobInterruptedUnknown, "restart_delivery_unknown", DeliveryUncertain); err != nil {
				return result, err
			}
			result.InterruptedRunning++
		case JobSucceeded:
			asset, assetErr := s.readAssetUnlocked(job.AssetID)
			if assetErr != nil || verifyImageFile(filepath.Join(s.root, "images", asset.FileName), asset) != nil {
				if !errors.Is(assetErr, ErrNotFound) && assetErr != nil {
					return result, assetErr
				}
				if assetErr == nil {
					assetPath, _ := s.path("assets", asset.ID)
					_ = os.Remove(assetPath)
				}
				if err := s.rewriteTerminalJobFailureUnlocked(job, "asset_missing"); err != nil {
					return result, err
				}
				result.RemovedMissingAsset++
			}
		}
	}
	orphans, err := s.removeOrphanImagesUnlocked()
	if err != nil {
		return result, err
	}
	result.RemovedOrphans += orphans
	if err := s.removeDanglingApplyStateUnlocked(); err != nil {
		return result, err
	}
	return result, nil
}

func (s *WorkspaceStore) reconcileCommittedAssetsUnlocked(result *ReconcileResult) error {
	assets, err := s.readAllAssetsUnlocked()
	if err != nil {
		return err
	}
	for _, asset := range assets {
		job, jobErr := s.readJobUnlocked(asset.JobID)
		validBinding := jobErr == nil && job.AssetID == asset.ID && job.PromptVersionID == asset.PromptVersionID
		imageErr := verifyImageFile(filepath.Join(s.root, "images", asset.FileName), asset)
		if !validBinding || imageErr != nil {
			assetPath, _ := s.path("assets", asset.ID)
			if err := os.Remove(assetPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			if jobErr == nil {
				if err := s.rewriteTerminalJobFailureUnlocked(job, "asset_missing"); err != nil {
					return err
				}
			}
			result.RemovedMissingAsset++
			continue
		}
		if job.Status != JobSucceeded {
			if err := s.completeJobSuccessUnlocked(job, asset); err != nil {
				return err
			}
			result.FinalizedAssets++
		}
	}
	return nil
}

func (s *WorkspaceStore) removeDanglingApplyStateUnlocked() error {
	paths, err := jsonRecordPaths(filepath.Join(s.root, "apply"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		var state ApplyState
		if err := readJSONFile(path, &state); err != nil {
			return err
		}
		if _, err := s.readAssetUnlocked(state.AssetID); errors.Is(err, ErrNotFound) {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

// PrepareClonedWorkspace prevents a project clone from replaying a copied
// queued request. Running requests are already made interrupted_unknown by
// Reconcile; copied queued requests are known not to have been submitted by
// the clone and become failed without any gateway call.
func PrepareClonedWorkspace(outputDir string) error {
	if _, err := os.Stat(filepath.Join(outputDir, "artwork", "schema.json")); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	store, err := NewWorkspaceStore(outputDir)
	if err != nil {
		return err
	}
	result, err := store.Reconcile()
	if err != nil {
		return err
	}
	for _, jobID := range result.ResumedQueued {
		if err := store.CompleteJobFailure(jobID, JobFailed, "cloned_job_not_resumed", DeliveryNotSent); err != nil {
			return err
		}
	}
	return nil
}

// WithCloneSnapshot serializes a project-tree clone with artwork mutations so
// the copied workspace is one coherent filesystem generation.
func WithCloneSnapshot(outputDir string, clone func() error) error {
	if clone == nil {
		return errors.New("artwork clone callback is required")
	}
	if _, err := os.Stat(filepath.Join(outputDir, "artwork", "schema.json")); os.IsNotExist(err) {
		return clone()
	} else if err != nil {
		return err
	}
	store, err := NewWorkspaceStore(outputDir)
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return clone()
}

func (s *WorkspaceStore) reconcileFinalizeJournalUnlocked(path string) (bool, error) {
	var journal finalizeJournal
	if err := readJSONFile(path, &journal); err != nil {
		return false, err
	}
	if journal.SchemaVersion != WorkspaceSchemaVersion || journal.Operation != "finalize" || journal.Asset.JobID != journal.JobID {
		return false, errors.New("artwork finalization journal is invalid")
	}
	job, err := s.readJobUnlocked(journal.JobID)
	if err != nil {
		return false, err
	}
	finalPath := filepath.Join(s.root, "images", journal.Asset.FileName)
	stagePath := filepath.Join(s.root, "staging", journal.StageName)
	if _, err := os.Stat(finalPath); os.IsNotExist(err) {
		if _, stageErr := os.Stat(stagePath); stageErr == nil {
			content, readErr := os.ReadFile(stagePath)
			if readErr != nil {
				return false, readErr
			}
			image, validateErr := ValidateImage(content)
			if validateErr != nil || image.SHA256 != journal.Asset.SHA256 {
				return false, errors.New("staged artwork image does not match finalization journal")
			}
			if err := os.Rename(stagePath, finalPath); err != nil {
				return false, err
			}
			_ = os.Chmod(finalPath, 0o444)
		} else if !os.IsNotExist(stageErr) {
			return false, stageErr
		} else {
			_ = os.Remove(path)
			if job.Status == JobRunning {
				return false, s.completeJobFailureUnlocked(job, JobInterruptedUnknown, "restart_delivery_unknown", DeliveryUncertain)
			}
			return false, nil
		}
	}
	if err := verifyImageFile(finalPath, journal.Asset); err != nil {
		return false, err
	}
	assetPath, _ := s.path("assets", journal.Asset.ID)
	if err := writeJSONAtomic(assetPath, journal.Asset, true); err != nil && !os.IsExist(err) {
		return false, err
	}
	if err := s.completeJobSuccessUnlocked(job, journal.Asset); err != nil {
		return false, err
	}
	_ = os.Remove(stagePath)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return true, nil
}

func (s *WorkspaceStore) reconcileApplyJournalUnlocked(path string) error {
	var journal applyJournal
	if err := readJSONFile(path, &journal); err != nil {
		return err
	}
	if journal.SchemaVersion != WorkspaceSchemaVersion || journal.Operation != "apply" {
		return errors.New("artwork apply journal is invalid")
	}
	if _, err := s.readAssetUnlocked(journal.State.AssetID); err != nil {
		return err
	}
	statePath := filepath.Join(s.root, "apply", deterministicID("target", journal.State.Target)+".json")
	if err := writeJSONAtomic(statePath, journal.State, false); err != nil {
		return err
	}
	return os.Remove(path)
}

func (s *WorkspaceStore) reconcileDeleteJournalUnlocked(path string) error {
	var journal deleteJournal
	if err := readJSONFile(path, &journal); err != nil {
		return err
	}
	if journal.SchemaVersion != WorkspaceSchemaVersion || journal.Operation != "delete" {
		return errors.New("artwork delete journal is invalid")
	}
	assetPath, _ := s.path("assets", journal.Asset.ID)
	imagePath := filepath.Join(s.root, "images", journal.Asset.FileName)
	trashPath := filepath.Join(s.root, "staging", journal.TrashName)
	if _, err := os.Stat(assetPath); err == nil {
		if _, imageErr := os.Stat(imagePath); os.IsNotExist(imageErr) {
			if _, trashErr := os.Stat(trashPath); trashErr == nil {
				if err := os.Rename(trashPath, imagePath); err != nil {
					return err
				}
				_ = os.Chmod(imagePath, 0o444)
			} else if os.IsNotExist(trashErr) {
				return errors.New("artwork delete recovery lost both image copies")
			} else {
				return trashErr
			}
		}
		return os.Remove(path)
	} else if !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(trashPath)
	_ = os.Remove(imagePath)
	return os.Remove(path)
}

func (s *WorkspaceStore) completeJobFailureUnlocked(job ImageJob, status JobStatus, code string, delivery DeliveryState) error {
	now := s.now()
	job.Status = status
	job.ErrorCode = safeFailureCode(code)
	job.Internal.Delivery = delivery
	job.Internal.ProviderCode = job.ErrorCode
	job.UpdatedAt = now
	job.FinishedAt = &now
	path, _ := s.path("jobs", job.ID)
	return writeJSONAtomic(path, job, false)
}

func (s *WorkspaceStore) rewriteTerminalJobFailureUnlocked(job ImageJob, code string) error {
	return s.completeJobFailureUnlocked(job, JobFailed, code, DeliveryResponded)
}

func (s *WorkspaceStore) removeOrphanImagesUnlocked() (int, error) {
	assets, err := s.readAllAssetsUnlocked()
	if err != nil {
		return 0, err
	}
	referenced := make(map[string]struct{}, len(assets))
	for _, asset := range assets {
		referenced[asset.FileName] = struct{}{}
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "images"))
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		if _, exists := referenced[entry.Name()]; exists {
			continue
		}
		path := filepath.Join(s.root, "images", entry.Name())
		_ = os.Chmod(path, 0o600)
		if err := os.Remove(path); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
