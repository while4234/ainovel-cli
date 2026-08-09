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

func (s *WorkspaceStore) CreateDraft(input DraftInput, idempotencyKey string) (Draft, error) {
	normalized, err := normalizeDraftInput(input)
	if err != nil {
		return Draft{}, err
	}
	fingerprintValue, err := fingerprint(normalized)
	if err != nil {
		return Draft{}, err
	}
	keyHash := ""
	draftID := ""
	if idempotencyKey = strings.TrimSpace(idempotencyKey); idempotencyKey != "" {
		keyHash = hashString(idempotencyKey)
		draftID = deterministicID("draft", "create", keyHash)
	} else if draftID, err = randomID("draft"); err != nil {
		return Draft{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path, _ := s.path("drafts", draftID)
	var existing Draft
	if err := readJSONFile(path, &existing); err == nil {
		if keyHash == "" || existing.CreateKeyHash != keyHash || existing.CreateFingerprint != fingerprintValue {
			return Draft{}, ErrIdempotencyConflict
		}
		return existing, validateDraft(existing)
	} else if !os.IsNotExist(err) {
		return Draft{}, err
	}
	now := s.now()
	draft := Draft{
		SchemaVersion: WorkspaceSchemaVersion, ID: draftID, Version: 1,
		WorkType: normalized.WorkType, Scope: normalized.Scope, ScopeID: normalized.ScopeID,
		Prompt: normalized.Prompt, PromptSource: normalized.PromptSource,
		SourceAssetID: normalized.SourceAssetID, ModelID: normalized.ModelID, Size: normalized.Size,
		SourceStatus: "current", CreatedAt: now, UpdatedAt: now,
		CreateKeyHash: keyHash, CreateFingerprint: fingerprintValue,
	}
	if err := writeJSONAtomic(path, draft, true); err != nil {
		return Draft{}, fmt.Errorf("persist artwork draft: %w", err)
	}
	return draft, nil
}

func (s *WorkspaceStore) GetDraft(id string) (Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readDraftUnlocked(id)
}

func (s *WorkspaceStore) readDraftUnlocked(id string) (Draft, error) {
	path, err := s.path("drafts", id)
	if err != nil {
		return Draft{}, err
	}
	var draft Draft
	if err := readJSONFile(path, &draft); err != nil {
		if os.IsNotExist(err) {
			return Draft{}, ErrNotFound
		}
		return Draft{}, err
	}
	if err := validateDraft(draft); err != nil {
		return Draft{}, fmt.Errorf("invalid artwork draft %q: %w", id, err)
	}
	return draft, nil
}

func (s *WorkspaceStore) UpdateDraft(id string, patch DraftPatch) (Draft, error) {
	if patch.ExpectedVersion <= 0 {
		return Draft{}, errors.New("expected_version must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	draft, err := s.readDraftUnlocked(id)
	if err != nil {
		return Draft{}, err
	}
	if draft.Version != patch.ExpectedVersion {
		return Draft{}, &VersionConflictError{Expected: patch.ExpectedVersion, Actual: draft.Version}
	}
	if patch.WorkType != nil {
		draft.WorkType = *patch.WorkType
	}
	if patch.Scope != nil {
		draft.Scope = *patch.Scope
	}
	if patch.ScopeID != nil {
		draft.ScopeID = *patch.ScopeID
	}
	if patch.Prompt != nil {
		draft.Prompt = *patch.Prompt
		draft.PromptSource = PromptSourceManual
		draft.SourceAssetID = ""
		draft.SourceSignature = ""
		draft.CurrentSourceSignature = ""
		draft.ConfirmedSignature = ""
		draft.SourceStatus = "current"
		draft.IsStale = false
	}
	if patch.ModelID != nil {
		draft.ModelID = *patch.ModelID
	}
	if patch.Size != nil {
		draft.Size = *patch.Size
	}
	if err := validateDraft(draft); err != nil {
		return Draft{}, err
	}
	draft.Version++
	draft.UpdatedAt = s.now()
	path, _ := s.path("drafts", id)
	if err := writeJSONAtomic(path, draft, false); err != nil {
		return Draft{}, fmt.Errorf("update artwork draft: %w", err)
	}
	return draft, nil
}

func (s *WorkspaceStore) DeleteDraft(id string, expectedVersion int64) error {
	if expectedVersion <= 0 {
		return errors.New("expected_version must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	draft, err := s.readDraftUnlocked(id)
	if err != nil {
		return err
	}
	if draft.Version != expectedVersion {
		return &VersionConflictError{Expected: expectedVersion, Actual: draft.Version}
	}
	jobs, err := s.readAllJobsUnlocked()
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.DraftID == id && !job.Status.Terminal() {
			return fmt.Errorf("%w: draft has an active image job", ErrConflict)
		}
	}
	path, _ := s.path("drafts", id)
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func (s *WorkspaceStore) ConfirmStalePrompt(id string, expectedVersion int64, signature string) (Draft, error) {
	if expectedVersion <= 0 {
		return Draft{}, errors.New("expected_version must be positive")
	}
	signature = strings.TrimSpace(signature)
	s.mu.Lock()
	defer s.mu.Unlock()
	draft, err := s.readDraftUnlocked(id)
	if err != nil {
		return Draft{}, err
	}
	if draft.Version != expectedVersion {
		return Draft{}, &VersionConflictError{Expected: expectedVersion, Actual: draft.Version}
	}
	if !draft.IsStale || draft.CurrentSourceSignature == "" {
		return Draft{}, fmt.Errorf("%w: artwork prompt is not stale", ErrConflict)
	}
	if signature == "" || signature != draft.CurrentSourceSignature {
		return Draft{}, fmt.Errorf("%w: artwork prompt source changed", ErrConflict)
	}
	draft.ConfirmedSignature = signature
	draft.SourceStatus = "confirmed"
	draft.IsStale = false
	draft.Version++
	draft.UpdatedAt = s.now()
	path, _ := s.path("drafts", id)
	if err := writeJSONAtomic(path, draft, false); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func (s *WorkspaceStore) ListDrafts(cursor string, limit int) (Page[DraftView], error) {
	decoded, err := decodeCursor(cursor)
	if err != nil {
		return Page[DraftView]{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	paths, err := jsonRecordPaths(filepath.Join(s.root, "drafts"))
	if err != nil {
		return Page[DraftView]{}, err
	}
	drafts := make([]Draft, 0, len(paths))
	for _, path := range paths {
		var draft Draft
		if err := readJSONFile(path, &draft); err != nil {
			return Page[DraftView]{}, err
		}
		if err := validateDraft(draft); err != nil {
			return Page[DraftView]{}, err
		}
		if recordBeforeCursor(draft.CreatedAt, draft.ID, decoded) {
			drafts = append(drafts, draft)
		}
	}
	sort.Slice(drafts, func(i, j int) bool {
		if !drafts[i].CreatedAt.Equal(drafts[j].CreatedAt) {
			return drafts[i].CreatedAt.After(drafts[j].CreatedAt)
		}
		return drafts[i].ID < drafts[j].ID
	})
	limit = normalizeLimit(limit)
	page := Page[DraftView]{Items: make([]DraftView, 0, min(limit, len(drafts)))}
	for _, draft := range drafts[:min(limit, len(drafts))] {
		page.Items = append(page.Items, draft.Public())
	}
	if len(drafts) > limit {
		last := drafts[limit-1]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func normalizeDraftInput(input DraftInput) (DraftInput, error) {
	input.Scope = strings.ToLower(strings.TrimSpace(input.Scope))
	input.ScopeID = strings.TrimSpace(input.ScopeID)
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.ModelID = strings.ToLower(strings.TrimSpace(input.ModelID))
	input.Size = strings.ToLower(strings.TrimSpace(input.Size))
	if input.PromptSource == "" {
		input.PromptSource = PromptSourceManual
	}
	draft := Draft{
		SchemaVersion: WorkspaceSchemaVersion, ID: "draft-validation", Version: 1,
		WorkType: input.WorkType, Scope: input.Scope, ScopeID: input.ScopeID,
		Prompt: input.Prompt, PromptSource: input.PromptSource, SourceAssetID: input.SourceAssetID,
		ModelID: input.ModelID, Size: input.Size, SourceStatus: "current",
		CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
	}
	if err := validateDraft(draft); err != nil {
		return DraftInput{}, err
	}
	return input, nil
}

func validateDraft(draft Draft) error {
	if draft.SchemaVersion != WorkspaceSchemaVersion || draft.Version <= 0 || validateRecordID(draft.ID) != nil {
		return errors.New("artwork draft schema is invalid")
	}
	if draft.CreatedAt.IsZero() || draft.UpdatedAt.IsZero() {
		return errors.New("artwork draft timestamps are required")
	}
	if draft.Prompt == "" || len([]byte(draft.Prompt)) > MaxPromptBytes {
		return fmt.Errorf("artwork prompt must contain 1-%d UTF-8 bytes", MaxPromptBytes)
	}
	if draft.PromptSource != PromptSourceManual && draft.PromptSource != PromptSourceReuse {
		return errors.New("artwork prompt source is invalid")
	}
	if draft.PromptSource == PromptSourceReuse && validateRecordID(draft.SourceAssetID) != nil {
		return errors.New("reused artwork source asset is invalid")
	}
	switch draft.WorkType {
	case WorkTypeCover:
		if draft.Scope != "project" || draft.ScopeID != "" {
			return errors.New("cover artwork must use project scope")
		}
	case WorkTypeCharacterPortrait:
		if draft.Scope != "character" || draft.ScopeID == "" {
			return errors.New("character portrait artwork requires character scope_id")
		}
	case WorkTypeIllustration:
		if draft.Scope != "project" && draft.Scope != "chapter" && draft.Scope != "scene" {
			return errors.New("illustration artwork scope must be project, chapter, or scene")
		}
		if draft.Scope == "project" && draft.ScopeID != "" {
			return errors.New("project illustration scope_id must be empty")
		}
		if draft.Scope != "project" && draft.ScopeID == "" {
			return errors.New("scoped illustration requires scope_id")
		}
	default:
		return errors.New("artwork work_type is invalid")
	}
	model, ok := LookupModel(draft.ModelID)
	if !ok || !model.Enabled || !model.Verified {
		return errors.New("artwork image model is not enabled")
	}
	if _, err := ResolveImageRequest(draft.ModelID, draft.Size); err != nil {
		return err
	}
	return nil
}

func (s *WorkspaceStore) SubmitGeneration(draftID string, expectedVersion int64, idempotencyKey, gatewayEndpoint string) (ImageJob, bool, error) {
	if expectedVersion <= 0 {
		return ImageJob{}, false, errors.New("expected_version must be positive")
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 256 {
		return ImageJob{}, false, errors.New("idempotency_key must contain 1-256 characters")
	}
	keyHash := hashString(idempotencyKey)
	jobID := deterministicID("job", "generate", keyHash)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, err := s.readJobUnlocked(jobID); err == nil {
		if existing.IdempotencyKeyHash != keyHash || existing.DraftID != draftID || existing.DraftVersion != expectedVersion {
			return ImageJob{}, false, ErrIdempotencyConflict
		}
		return existing, true, nil
	} else if !errors.Is(err, ErrNotFound) {
		return ImageJob{}, false, err
	}
	draft, err := s.readDraftUnlocked(draftID)
	if err != nil {
		return ImageJob{}, false, err
	}
	if draft.Version != expectedVersion {
		return ImageJob{}, false, &VersionConflictError{Expected: expectedVersion, Actual: draft.Version}
	}
	jobs, err := s.readAllJobsUnlocked()
	if err != nil {
		return ImageJob{}, false, err
	}
	for _, job := range jobs {
		if !job.Status.Terminal() {
			return ImageJob{}, false, fmt.Errorf("%w: project already has an active image job", ErrConflict)
		}
	}
	requestOptions, err := ResolveImageRequest(draft.ModelID, draft.Size)
	if err != nil {
		return ImageJob{}, false, err
	}
	request := ProviderRequestSnapshot{
		CatalogVersion: CapabilityRegistryVersion, ModelID: draft.ModelID,
		ResolvedModel: requestOptions.Model, Size: requestOptions.Size,
		AspectRatio: requestOptions.AspectRatio, Resolution: requestOptions.Resolution,
		ResponseFormat: "b64_json",
	}
	promptFingerprint, err := fingerprint(struct {
		DraftID      string
		DraftVersion int64
		Prompt       string
		Source       PromptSource
		SourceAsset  string
	}{draft.ID, draft.Version, draft.Prompt, draft.PromptSource, draft.SourceAssetID})
	if err != nil {
		return ImageJob{}, false, err
	}
	promptVersion := ArtworkPromptVersion{
		SchemaVersion: WorkspaceSchemaVersion,
		ID:            deterministicID("prompt", promptFingerprint), DraftID: draft.ID,
		DraftVersion: draft.Version, Prompt: draft.Prompt, Source: draft.PromptSource,
		SourceAssetID: draft.SourceAssetID, CreatedAt: s.now(),
	}
	promptPath, _ := s.path("prompts", promptVersion.ID)
	if err := writeJSONAtomic(promptPath, promptVersion, true); err != nil && !os.IsExist(err) {
		return ImageJob{}, false, fmt.Errorf("persist immutable artwork prompt version: %w", err)
	}
	requestFingerprint, err := fingerprint(struct {
		DraftID         string
		DraftVersion    int64
		PromptVersionID string
		Request         ProviderRequestSnapshot
		GatewayEndpoint string
	}{draft.ID, draft.Version, promptVersion.ID, request, gatewayEndpoint})
	if err != nil {
		return ImageJob{}, false, err
	}
	now := s.now()
	job := ImageJob{
		SchemaVersion: WorkspaceSchemaVersion, ID: jobID, DraftID: draft.ID,
		DraftVersion: draft.Version, WorkType: draft.WorkType, Scope: draft.Scope,
		ScopeID: draft.ScopeID, PromptSource: draft.PromptSource, SourceAssetID: draft.SourceAssetID,
		PromptVersionID: promptVersion.ID,
		AssetID:         deterministicID("asset", jobID), Status: JobQueued, Request: request,
		Internal:           JobInternalSnapshot{GatewayEndpoint: gatewayEndpoint, Delivery: DeliveryNotSent},
		IdempotencyKeyHash: keyHash, RequestFingerprint: requestFingerprint,
		CreatedAt: now, UpdatedAt: now,
	}
	jobPath, _ := s.path("jobs", job.ID)
	if err := writeJSONAtomic(jobPath, job, true); err != nil {
		return ImageJob{}, false, fmt.Errorf("persist queued artwork job: %w", err)
	}
	return job, false, nil
}

func (s *WorkspaceStore) GetJob(id string) (ImageJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readJobUnlocked(id)
}

func (s *WorkspaceStore) readJobUnlocked(id string) (ImageJob, error) {
	path, err := s.path("jobs", id)
	if err != nil {
		return ImageJob{}, err
	}
	var job ImageJob
	if err := readJSONFile(path, &job); err != nil {
		if os.IsNotExist(err) {
			return ImageJob{}, ErrNotFound
		}
		return ImageJob{}, err
	}
	if err := validateJob(job); err != nil {
		return ImageJob{}, err
	}
	return job, nil
}

func validateJob(job ImageJob) error {
	if job.SchemaVersion != WorkspaceSchemaVersion || validateRecordID(job.ID) != nil || validateRecordID(job.DraftID) != nil || validateRecordID(job.PromptVersionID) != nil || validateRecordID(job.AssetID) != nil {
		return errors.New("artwork job schema is invalid")
	}
	if job.WorkType != WorkTypeCover && job.WorkType != WorkTypeIllustration && job.WorkType != WorkTypeCharacterPortrait {
		return errors.New("artwork job work_type is invalid")
	}
	if job.CreatedAt.IsZero() || job.UpdatedAt.IsZero() || job.Request.CatalogVersion == "" || job.Request.ResponseFormat != "b64_json" {
		return errors.New("artwork job snapshot is invalid")
	}
	switch job.Status {
	case JobQueued, JobRunning, JobSucceeded, JobFailed, JobInterruptedUnknown:
	default:
		return errors.New("artwork job status is invalid")
	}
	return nil
}

func (s *WorkspaceStore) readAllJobsUnlocked() ([]ImageJob, error) {
	paths, err := jsonRecordPaths(filepath.Join(s.root, "jobs"))
	if err != nil {
		return nil, err
	}
	jobs := make([]ImageJob, 0, len(paths))
	for _, path := range paths {
		var job ImageJob
		if err := readJSONFile(path, &job); err != nil {
			return nil, err
		}
		if err := validateJob(job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *WorkspaceStore) ListJobs(cursor string, limit int) (Page[ImageJobView], error) {
	decoded, err := decodeCursor(cursor)
	if err != nil {
		return Page[ImageJobView]{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.readAllJobsUnlocked()
	if err != nil {
		return Page[ImageJobView]{}, err
	}
	filtered := jobs[:0]
	for _, job := range jobs {
		if recordBeforeCursor(job.CreatedAt, job.ID, decoded) {
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
	page := Page[ImageJobView]{Items: make([]ImageJobView, 0, min(limit, len(filtered)))}
	for _, job := range filtered[:min(limit, len(filtered))] {
		page.Items = append(page.Items, job.Public())
	}
	if len(filtered) > limit {
		last := filtered[limit-1]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func (s *WorkspaceStore) BeginJob(id string) (ImageJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.readJobUnlocked(id)
	if err != nil {
		return ImageJob{}, err
	}
	if job.Status != JobQueued {
		return ImageJob{}, fmt.Errorf("%w: artwork job is %s", ErrConflict, job.Status)
	}
	now := s.now()
	job.Status = JobRunning
	job.AttemptCount++
	job.StartedAt = &now
	job.UpdatedAt = now
	job.Internal.Delivery = DeliveryUncertain
	path, _ := s.path("jobs", id)
	if err := writeJSONAtomic(path, job, false); err != nil {
		return ImageJob{}, err
	}
	return job, nil
}

func (s *WorkspaceStore) CompleteJobFailure(id string, status JobStatus, code string, delivery DeliveryState) error {
	if status != JobFailed && status != JobInterruptedUnknown {
		return errors.New("artwork failure status is invalid")
	}
	code = safeFailureCode(code)
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.readJobUnlocked(id)
	if err != nil {
		return err
	}
	if job.Status == JobSucceeded {
		return nil
	}
	if job.Status.Terminal() {
		return nil
	}
	now := s.now()
	job.Status = status
	job.ErrorCode = code
	job.Internal.Delivery = delivery
	job.Internal.ProviderCode = code
	job.UpdatedAt = now
	job.FinishedAt = &now
	path, _ := s.path("jobs", id)
	return writeJSONAtomic(path, job, false)
}

func safeFailureCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" || len(code) > 64 {
		return "image_generation_failed"
	}
	for _, char := range code {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' {
			continue
		}
		return "image_generation_failed"
	}
	return code
}

func (s *WorkspaceStore) readPromptVersionUnlocked(id string) (ArtworkPromptVersion, error) {
	path, err := s.path("prompts", id)
	if err != nil {
		return ArtworkPromptVersion{}, err
	}
	var prompt ArtworkPromptVersion
	if err := readJSONFile(path, &prompt); err != nil {
		if os.IsNotExist(err) {
			return ArtworkPromptVersion{}, ErrNotFound
		}
		return ArtworkPromptVersion{}, err
	}
	if prompt.SchemaVersion != WorkspaceSchemaVersion || prompt.ID != id || prompt.Prompt == "" {
		return ArtworkPromptVersion{}, errors.New("immutable artwork prompt version is invalid")
	}
	return prompt, nil
}

func (s *WorkspaceStore) ExecutionSnapshot(jobID string) (ImageJob, ArtworkPromptVersion, Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.readJobUnlocked(jobID)
	if err != nil {
		return ImageJob{}, ArtworkPromptVersion{}, Draft{}, err
	}
	prompt, err := s.readPromptVersionUnlocked(job.PromptVersionID)
	if err != nil {
		return ImageJob{}, ArtworkPromptVersion{}, Draft{}, err
	}
	draft, err := s.readDraftUnlocked(job.DraftID)
	if err != nil {
		// A terminal draft may be deleted after submission. Reconstruct only the
		// immutable generation fields needed to finalize the already charged job.
		if !errors.Is(err, ErrNotFound) {
			return ImageJob{}, ArtworkPromptVersion{}, Draft{}, err
		}
		draft = Draft{ID: job.DraftID, Version: job.DraftVersion, Prompt: prompt.Prompt, PromptSource: prompt.Source, SourceAssetID: prompt.SourceAssetID}
	}
	return job, prompt, draft, nil
}
