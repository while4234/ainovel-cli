package artwork

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

func (s *WorkspaceStore) CreatePromptJob(draftID string, expectedVersion int64, idempotencyKey string, source SourceSnapshot, model TextModelSnapshot) (PromptJob, bool, error) {
	if expectedVersion <= 0 {
		return PromptJob{}, false, errors.New("expected_version must be positive")
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 256 {
		return PromptJob{}, false, errors.New("idempotency_key must contain 1-256 characters")
	}
	model.Provider = strings.TrimSpace(model.Provider)
	model.Model = strings.TrimSpace(model.Model)
	model.ReasoningEffort = strings.TrimSpace(model.ReasoningEffort)
	if model.Provider == "" || model.Model == "" {
		return PromptJob{}, false, errors.New("artwork prompt model snapshot is incomplete")
	}
	if err := validateSourceSnapshot(source); err != nil {
		return PromptJob{}, false, err
	}
	keyHash := hashString(idempotencyKey)
	jobID := deterministicID("prompt-job", "generate", keyHash)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, err := s.readPromptJobUnlocked(jobID); err == nil {
		if existing.IdempotencyKeyHash != keyHash || existing.DraftID != draftID || existing.DraftVersion != expectedVersion || existing.Source.Digest != source.Digest {
			return PromptJob{}, false, ErrIdempotencyConflict
		}
		return existing, true, nil
	} else if !errors.Is(err, ErrNotFound) {
		return PromptJob{}, false, err
	}
	draft, err := s.readDraftUnlocked(draftID)
	if err != nil {
		return PromptJob{}, false, err
	}
	if draft.Version != expectedVersion {
		return PromptJob{}, false, &VersionConflictError{Expected: expectedVersion, Actual: draft.Version}
	}
	if err := validateSnapshotForDraft(source, draft); err != nil {
		return PromptJob{}, false, err
	}
	requestFingerprint, err := fingerprint(struct {
		DraftID      string
		DraftVersion int64
		Source       SourceSnapshot
		Model        TextModelSnapshot
	}{draft.ID, draft.Version, source, model})
	if err != nil {
		return PromptJob{}, false, err
	}
	now := s.now()
	job := PromptJob{
		SchemaVersion: WorkspaceSchemaVersion, ID: jobID, DraftID: draft.ID,
		DraftVersion: draft.Version, Status: PromptJobQueued, Source: source, Model: model,
		PreviousPromptJobID: draft.CurrentPromptJobID,
		IdempotencyKeyHash:  keyHash, RequestFingerprint: requestFingerprint,
		CreatedAt: now, UpdatedAt: now,
	}
	path, _ := s.path("prompt-jobs", job.ID)
	if err := writeJSONAtomic(path, job, true); err != nil {
		return PromptJob{}, false, fmt.Errorf("persist artwork prompt job: %w", err)
	}
	return job, false, nil
}

func (s *WorkspaceStore) BeginPromptJob(id string) (PromptJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.readPromptJobUnlocked(id)
	if err != nil {
		return PromptJob{}, err
	}
	if job.Status != PromptJobQueued {
		return PromptJob{}, fmt.Errorf("%w: artwork prompt job is %s", ErrConflict, job.Status)
	}
	now := s.now()
	job.Status = PromptJobRunning
	job.AttemptCount++
	job.StartedAt = &now
	job.UpdatedAt = now
	path, _ := s.path("prompt-jobs", job.ID)
	if err := writeJSONAtomic(path, job, false); err != nil {
		return PromptJob{}, err
	}
	return job, nil
}

func (s *WorkspaceStore) CompletePromptJob(id, prompt string, usage PromptUsageSnapshot) (PromptJob, Draft, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return PromptJob{}, Draft{}, ErrPromptEmpty
	}
	if utf8.RuneCountInString(prompt) > MaxAIPromptRunes {
		return PromptJob{}, Draft{}, ErrPromptTooLong
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.readPromptJobUnlocked(id)
	if err != nil {
		return PromptJob{}, Draft{}, err
	}
	if job.Status == PromptJobSucceeded {
		draft, draftErr := s.readDraftUnlocked(job.DraftID)
		return job, draft, draftErr
	}
	if job.Status != PromptJobRunning {
		return PromptJob{}, Draft{}, fmt.Errorf("%w: artwork prompt job is %s", ErrConflict, job.Status)
	}
	draft, err := s.readDraftUnlocked(job.DraftID)
	if err != nil {
		return PromptJob{}, Draft{}, err
	}
	if draft.Version != job.DraftVersion {
		return PromptJob{}, Draft{}, &VersionConflictError{Expected: job.DraftVersion, Actual: draft.Version}
	}
	if err := validateSnapshotForDraft(job.Source, draft); err != nil {
		return PromptJob{}, Draft{}, err
	}
	nextDraftVersion := draft.Version + 1
	promptFingerprint, err := fingerprint(struct {
		JobID        string
		DraftID      string
		DraftVersion int64
		Prompt       string
		SourceDigest string
		Model        TextModelSnapshot
	}{job.ID, draft.ID, nextDraftVersion, prompt, job.Source.Digest, job.Model})
	if err != nil {
		return PromptJob{}, Draft{}, err
	}
	now := s.now()
	promptVersion := ArtworkPromptVersion{
		SchemaVersion: WorkspaceSchemaVersion, ID: deterministicID("prompt", promptFingerprint),
		DraftID: draft.ID, DraftVersion: nextDraftVersion, Prompt: prompt, Source: PromptSourceAI,
		PromptJobID: job.ID, PreviousPromptVersionID: draft.CurrentPromptVersionID,
		SourceSnapshot: cloneSourceSnapshot(&job.Source), Model: cloneTextModelSnapshot(&job.Model),
		CreatedAt: now,
	}
	promptPath, _ := s.path("prompts", promptVersion.ID)
	if err := writeJSONAtomic(promptPath, promptVersion, true); err != nil && !os.IsExist(err) {
		return PromptJob{}, Draft{}, fmt.Errorf("persist immutable AI artwork prompt version: %w", err)
	}
	draft.Version = nextDraftVersion
	draft.Prompt = prompt
	draft.PromptSource = PromptSourceAI
	draft.SourceAssetID = ""
	draft.SourceSignature = job.Source.Digest
	draft.CurrentSourceSignature = job.Source.Digest
	draft.ConfirmedSignature = ""
	draft.ConfirmedAt = nil
	draft.SourceStatus = "current"
	draft.IsStale = false
	draft.PreviousPromptJobID = draft.CurrentPromptJobID
	draft.CurrentPromptJobID = job.ID
	draft.CurrentPromptVersionID = promptVersion.ID
	draft.UpdatedAt = now
	if err := validateDraft(draft); err != nil {
		return PromptJob{}, Draft{}, err
	}
	draftPath, _ := s.path("drafts", draft.ID)
	if err := writeJSONAtomic(draftPath, draft, false); err != nil {
		return PromptJob{}, Draft{}, err
	}
	job.Status = PromptJobSucceeded
	job.PromptVersionID = promptVersion.ID
	job.Usage = usage
	job.ErrorCode = ""
	job.UpdatedAt = now
	job.FinishedAt = &now
	jobPath, _ := s.path("prompt-jobs", job.ID)
	if err := writeJSONAtomic(jobPath, job, false); err != nil {
		return PromptJob{}, Draft{}, err
	}
	return job, draft, nil
}

func (s *WorkspaceStore) CompletePromptJobFailure(id, code string, usage PromptUsageSnapshot) error {
	code = safePromptFailureCode(code)
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.readPromptJobUnlocked(id)
	if err != nil {
		return err
	}
	if job.Status == PromptJobSucceeded || job.Status == PromptJobFailed {
		return nil
	}
	now := s.now()
	job.Status = PromptJobFailed
	job.ErrorCode = code
	job.Usage = usage
	job.UpdatedAt = now
	job.FinishedAt = &now
	path, _ := s.path("prompt-jobs", job.ID)
	return writeJSONAtomic(path, job, false)
}

func (s *WorkspaceStore) GetPromptJob(id string) (PromptJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readPromptJobUnlocked(id)
}

func (s *WorkspaceStore) readPromptJobUnlocked(id string) (PromptJob, error) {
	path, err := s.path("prompt-jobs", id)
	if err != nil {
		return PromptJob{}, err
	}
	var job PromptJob
	if err := readJSONFile(path, &job); err != nil {
		if os.IsNotExist(err) {
			return PromptJob{}, ErrNotFound
		}
		return PromptJob{}, err
	}
	if err := validatePromptJob(job); err != nil {
		return PromptJob{}, fmt.Errorf("invalid artwork prompt job %q: %w", id, err)
	}
	return job, nil
}

func validatePromptJob(job PromptJob) error {
	if job.SchemaVersion != WorkspaceSchemaVersion || validateRecordID(job.ID) != nil || validateRecordID(job.DraftID) != nil {
		return errors.New("artwork prompt job schema is invalid")
	}
	if err := validateSourceSnapshot(job.Source); err != nil {
		return errors.New("artwork prompt job source snapshot is invalid")
	}
	if job.Model.Provider == "" || job.Model.Model == "" || job.CreatedAt.IsZero() || job.UpdatedAt.IsZero() {
		return errors.New("artwork prompt job audit snapshot is invalid")
	}
	switch job.Status {
	case PromptJobQueued, PromptJobRunning, PromptJobSucceeded, PromptJobFailed:
	default:
		return errors.New("artwork prompt job status is invalid")
	}
	if job.Status == PromptJobSucceeded && validateRecordID(job.PromptVersionID) != nil {
		return errors.New("succeeded artwork prompt job has no prompt version")
	}
	return nil
}

func (s *WorkspaceStore) ListPromptJobs(cursor string, limit int, draftID string) (Page[PromptJobView], error) {
	decoded, err := decodeCursor(cursor)
	if err != nil {
		return Page[PromptJobView]{}, err
	}
	draftID = strings.TrimSpace(draftID)
	if draftID != "" && validateRecordID(draftID) != nil {
		return Page[PromptJobView]{}, errors.New("draft_id is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.readAllPromptJobsUnlocked()
	if err != nil {
		return Page[PromptJobView]{}, err
	}
	filtered := jobs[:0]
	for _, job := range jobs {
		if (draftID == "" || job.DraftID == draftID) && recordBeforeCursor(job.CreatedAt, job.ID, decoded) {
			filtered = append(filtered, job)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if !filtered[i].CreatedAt.Equal(filtered[j].CreatedAt) {
			return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
		}
		return filtered[i].ID < filtered[j].ID
	})
	limit = normalizeLimit(limit)
	page := Page[PromptJobView]{Items: make([]PromptJobView, 0, min(limit, len(filtered)))}
	for _, job := range filtered[:min(limit, len(filtered))] {
		page.Items = append(page.Items, job.Public())
	}
	if len(filtered) > limit {
		last := filtered[limit-1]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func (s *WorkspaceStore) readAllPromptJobsUnlocked() ([]PromptJob, error) {
	paths, err := jsonRecordPaths(filepath.Join(s.root, "prompt-jobs"))
	if err != nil {
		return nil, err
	}
	jobs := make([]PromptJob, 0, len(paths))
	for _, path := range paths {
		var job PromptJob
		if err := readJSONFile(path, &job); err != nil {
			return nil, err
		}
		if err := validatePromptJob(job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func safePromptFailureCode(code string) string {
	code = safeFailureCode(code)
	if code == "image_generation_failed" {
		return "prompt_generation_failed"
	}
	return code
}

// PromptFailureCode maps an internal generation error to a stable, non-secret
// audit code suitable for prompt-job records and HTTP responses.
func PromptFailureCode(err error) string {
	switch {
	case errors.Is(err, ErrPromptEmpty):
		return "prompt_empty"
	case errors.Is(err, ErrPromptTooLong):
		return "prompt_too_long"
	case errors.Is(err, ErrPromptModel):
		return "prompt_model_failed"
	case errors.As(err, new(*VersionConflictError)):
		return "draft_changed"
	default:
		return "prompt_generation_failed"
	}
}

func validateSnapshotForDraft(snapshot SourceSnapshot, draft Draft) error {
	if err := validateSourceSnapshot(snapshot); err != nil {
		return err
	}
	if snapshot.WorkType != draft.WorkType || snapshot.Scope != draft.Scope || snapshot.ScopeID != draft.ScopeID {
		return fmt.Errorf("%w: source snapshot no longer matches the draft scope", ErrConflict)
	}
	return nil
}

func cloneSourceSnapshot(source *SourceSnapshot) *SourceSnapshot {
	if source == nil {
		return nil
	}
	copy := *source
	copy.Fragments = append([]SourceFragment(nil), source.Fragments...)
	return &copy
}

func cloneTextModelSnapshot(model *TextModelSnapshot) *TextModelSnapshot {
	if model == nil {
		return nil
	}
	copy := *model
	return &copy
}
