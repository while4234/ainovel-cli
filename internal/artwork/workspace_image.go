package artwork

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "golang.org/x/image/webp"
)

const MaxImagePixels int64 = 40_000_000

type ValidatedImage struct {
	Content   []byte
	MIMEType  string
	Extension string
	Width     int
	Height    int
	SHA256    string
}

func ValidateImage(content []byte) (ValidatedImage, error) {
	if len(content) == 0 {
		return ValidatedImage{}, errors.New("image is empty")
	}
	if len(content) > MaxImageBytes {
		return ValidatedImage{}, errors.New("image exceeds 25 MiB limit")
	}
	mimeType, extension := imageType(content)
	if mimeType == "" {
		return ValidatedImage{}, errors.New("image format must be PNG, JPEG, or WebP")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil || !matchesImageFormat(mimeType, format) {
		return ValidatedImage{}, errors.New("image could not be decoded safely")
	}
	if err := validateImageDimensions(config.Width, config.Height); err != nil {
		return ValidatedImage{}, err
	}
	decoded, decodedFormat, err := image.Decode(bytes.NewReader(content))
	if err != nil || decoded == nil || !matchesImageFormat(mimeType, decodedFormat) {
		return ValidatedImage{}, errors.New("image could not be decoded safely")
	}
	bounds := decoded.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if err := validateImageDimensions(width, height); err != nil {
		return ValidatedImage{}, err
	}
	if width != config.Width || height != config.Height {
		return ValidatedImage{}, errors.New("image dimensions changed while decoding")
	}
	digest := sha256.Sum256(content)
	return ValidatedImage{
		Content: append([]byte(nil), content...), MIMEType: mimeType, Extension: extension,
		Width: width, Height: height, SHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func imageType(content []byte) (string, string) {
	switch {
	case bytes.HasPrefix(content, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", ".png"
	case bytes.HasPrefix(content, []byte("\xff\xd8\xff")):
		return "image/jpeg", ".jpg"
	case len(content) >= 12 && bytes.Equal(content[:4], []byte("RIFF")) && bytes.Equal(content[8:12], []byte("WEBP")):
		return "image/webp", ".webp"
	default:
		return "", ""
	}
}

func matchesImageFormat(mimeType, format string) bool {
	return (mimeType == "image/png" && format == "png") ||
		(mimeType == "image/jpeg" && format == "jpeg") ||
		(mimeType == "image/webp" && format == "webp")
}

func validateImageDimensions(width, height int) error {
	if width <= 0 || height <= 0 || int64(width)*int64(height) > MaxImagePixels {
		return errors.New("image dimensions exceed the safe pixel limit")
	}
	return nil
}

type finalizeJournal struct {
	SchemaVersion int       `json:"schema_version"`
	Operation     string    `json:"operation"`
	JobID         string    `json:"job_id"`
	Asset         Asset     `json:"asset"`
	StageName     string    `json:"stage_name"`
	Phase         string    `json:"phase"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (s *WorkspaceStore) FinalizeJob(jobID string, content []byte) (Asset, error) {
	image, err := ValidateImage(content)
	if err != nil {
		return Asset{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.readJobUnlocked(jobID)
	if err != nil {
		return Asset{}, err
	}
	if job.Status == JobSucceeded {
		asset, err := s.readAssetUnlocked(job.AssetID)
		if err != nil {
			return Asset{}, fmt.Errorf("succeeded artwork job is missing its asset: %w", err)
		}
		if asset.SHA256 != image.SHA256 {
			return Asset{}, fmt.Errorf("%w: finalized image differs from the immutable asset", ErrConflict)
		}
		return asset, nil
	}
	if job.Status != JobRunning {
		return Asset{}, fmt.Errorf("%w: artwork job is %s", ErrConflict, job.Status)
	}
	prompt, err := s.readPromptVersionUnlocked(job.PromptVersionID)
	if err != nil {
		return Asset{}, err
	}
	asset := assetFromJob(job, prompt, image, s.now())
	stageName := job.ID + image.Extension + ".pending"
	journal := finalizeJournal{
		SchemaVersion: WorkspaceSchemaVersion, Operation: "finalize", JobID: job.ID,
		Asset: asset, StageName: stageName, Phase: "prepared", UpdatedAt: s.now(),
	}
	journalPath, _ := s.path("journals", "finalize-"+job.ID)
	if err := writeJSONAtomic(journalPath, journal, false); err != nil {
		return Asset{}, fmt.Errorf("persist artwork finalization journal: %w", err)
	}
	if err := s.injectFault("finalize_after_journal"); err != nil {
		return Asset{}, err
	}
	stagePath := filepath.Join(s.root, "staging", stageName)
	if err := ensureContained(s.root, stagePath); err != nil {
		return Asset{}, err
	}
	if err := writeFileAtomic(stagePath, image.Content, false, 0o600); err != nil {
		return Asset{}, fmt.Errorf("stage artwork image: %w", err)
	}
	if err := s.injectFault("finalize_after_stage_sync"); err != nil {
		return Asset{}, err
	}
	finalPath := filepath.Join(s.root, "images", asset.FileName)
	if err := ensureContained(s.root, finalPath); err != nil {
		return Asset{}, err
	}
	if _, err := os.Lstat(finalPath); os.IsNotExist(err) {
		if err := os.Rename(stagePath, finalPath); err != nil {
			return Asset{}, fmt.Errorf("install immutable artwork image: %w", err)
		}
		if err := os.Chmod(finalPath, 0o444); err != nil {
			return Asset{}, err
		}
		if err := syncDirectory(filepath.Dir(finalPath)); err != nil {
			return Asset{}, err
		}
	} else if err != nil {
		return Asset{}, err
	} else if err := verifyImageFile(finalPath, asset); err != nil {
		return Asset{}, err
	}
	journal.Phase = "image_installed"
	journal.UpdatedAt = s.now()
	if err := writeJSONAtomic(journalPath, journal, false); err != nil {
		return Asset{}, err
	}
	if err := s.injectFault("finalize_after_image_rename"); err != nil {
		return Asset{}, err
	}
	assetPath, _ := s.path("assets", asset.ID)
	if err := writeJSONAtomic(assetPath, asset, true); err != nil && !os.IsExist(err) {
		return Asset{}, fmt.Errorf("persist immutable artwork asset metadata: %w", err)
	}
	journal.Phase = "asset_committed"
	journal.UpdatedAt = s.now()
	if err := writeJSONAtomic(journalPath, journal, false); err != nil {
		return Asset{}, err
	}
	if err := s.injectFault("finalize_after_asset_metadata"); err != nil {
		return Asset{}, err
	}
	if err := s.completeJobSuccessUnlocked(job, asset); err != nil {
		return Asset{}, err
	}
	if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
		return Asset{}, err
	}
	_ = os.Remove(stagePath)
	if err := syncDirectory(filepath.Dir(journalPath)); err != nil {
		return Asset{}, err
	}
	return asset, nil
}

func assetFromJob(job ImageJob, prompt ArtworkPromptVersion, image ValidatedImage, createdAt time.Time) Asset {
	return Asset{
		SchemaVersion: WorkspaceSchemaVersion, ID: job.AssetID, DraftID: job.DraftID,
		DraftVersion: job.DraftVersion, PromptVersionID: prompt.ID, JobID: job.ID,
		WorkType: job.WorkType, Scope: job.Scope, ScopeID: job.ScopeID,
		Prompt: prompt.Prompt, PromptSource: prompt.Source,
		ReusedFromAssetID: prompt.SourceAssetID,
		SourceSnapshot:    cloneSourceSnapshot(job.SourceSnapshot),
		StaleConfirmation: cloneStaleConfirmation(job.StaleConfirmation),
		Request:           job.Request, Origin: "generation",
		FileName: job.AssetID + image.Extension, MIMEType: image.MIMEType,
		Width: image.Width, Height: image.Height, SHA256: image.SHA256, CreatedAt: createdAt,
	}
}

func cloneStaleConfirmation(value *StalePromptConfirmation) *StalePromptConfirmation {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (s *WorkspaceStore) completeJobSuccessUnlocked(job ImageJob, asset Asset) error {
	now := s.now()
	job.Status = JobSucceeded
	job.ErrorCode = ""
	job.Internal.Delivery = DeliveryResponded
	job.Internal.ProviderCode = ""
	job.UpdatedAt = now
	job.FinishedAt = &now
	path, _ := s.path("jobs", job.ID)
	return writeJSONAtomic(path, job, false)
}

func verifyImageFile(path string, asset Asset) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	image, err := ValidateImage(content)
	if err != nil {
		return err
	}
	if image.SHA256 != asset.SHA256 || image.MIMEType != asset.MIMEType || image.Width != asset.Width || image.Height != asset.Height || filepath.Base(path) != asset.FileName {
		return errors.New("immutable artwork image does not match its metadata")
	}
	return nil
}

func (s *WorkspaceStore) GetAsset(id string) (AssetView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, err := s.readCommittedAssetUnlocked(id)
	if err != nil {
		return AssetView{}, err
	}
	applied, err := s.isAssetAppliedUnlocked(id)
	return AssetView{Asset: asset, Applied: applied}, err
}

func (s *WorkspaceStore) readCommittedAssetUnlocked(id string) (Asset, error) {
	asset, err := s.readAssetUnlocked(id)
	if err != nil {
		return Asset{}, err
	}
	job, err := s.readJobUnlocked(asset.JobID)
	if err != nil || job.Status != JobSucceeded || job.AssetID != asset.ID || job.PromptVersionID != asset.PromptVersionID {
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Asset{}, err
		}
		return Asset{}, ErrNotFound
	}
	return asset, nil
}

func (s *WorkspaceStore) readAssetUnlocked(id string) (Asset, error) {
	path, err := s.path("assets", id)
	if err != nil {
		return Asset{}, err
	}
	var asset Asset
	if err := readJSONFile(path, &asset); err != nil {
		if os.IsNotExist(err) {
			return Asset{}, ErrNotFound
		}
		return Asset{}, err
	}
	if err := validateAsset(asset); err != nil {
		return Asset{}, err
	}
	return asset, nil
}

func validateAsset(asset Asset) error {
	if asset.SchemaVersion != WorkspaceSchemaVersion || validateRecordID(asset.ID) != nil || validateRecordID(asset.JobID) != nil || asset.Origin != "generation" {
		return errors.New("artwork asset schema is invalid")
	}
	extension := filepath.Ext(asset.FileName)
	if asset.FileName != asset.ID+extension || !assetMIMEAndExtensionMatch(asset.MIMEType, extension) {
		return errors.New("artwork asset file name is invalid")
	}
	if len(asset.SHA256) != 64 || asset.Width <= 0 || asset.Height <= 0 || int64(asset.Width)*int64(asset.Height) > MaxImagePixels || asset.CreatedAt.IsZero() {
		return errors.New("artwork asset metadata is invalid")
	}
	if _, err := hex.DecodeString(asset.SHA256); err != nil {
		return errors.New("artwork asset digest is invalid")
	}
	if asset.SourceSnapshot != nil {
		if asset.PromptSource != PromptSourceAI || validateSourceSnapshot(*asset.SourceSnapshot) != nil || asset.SourceSnapshot.WorkType != asset.WorkType || asset.SourceSnapshot.Scope != asset.Scope || asset.SourceSnapshot.ScopeID != asset.ScopeID {
			return errors.New("artwork asset source provenance is invalid")
		}
	}
	if asset.StaleConfirmation != nil {
		if asset.SourceSnapshot == nil || asset.StaleConfirmation.OriginalSourceDigest == "" || asset.StaleConfirmation.ConfirmedSourceDigest != asset.SourceSnapshot.Digest || asset.StaleConfirmation.ConfirmedAt.IsZero() {
			return errors.New("artwork asset stale confirmation is invalid")
		}
	}
	return nil
}

func assetMIMEAndExtensionMatch(mimeType, extension string) bool {
	return (mimeType == "image/png" && extension == ".png") ||
		(mimeType == "image/jpeg" && extension == ".jpg") ||
		(mimeType == "image/webp" && extension == ".webp")
}

func (s *WorkspaceStore) ReadAssetContent(id string) (AssetView, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, err := s.readCommittedAssetUnlocked(id)
	if err != nil {
		return AssetView{}, nil, err
	}
	path := filepath.Join(s.root, "images", asset.FileName)
	if err := ensureContained(s.root, path); err != nil {
		return AssetView{}, nil, err
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(s.root)
	if err != nil {
		return AssetView{}, nil, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Join(s.root, "images"))
	if err != nil {
		return AssetView{}, nil, err
	}
	if err := ensureContained(resolvedWorkspace, resolvedRoot); err != nil {
		return AssetView{}, nil, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return AssetView{}, nil, ErrNotFound
	}
	if err := ensureContained(resolvedRoot, resolved); err != nil {
		return AssetView{}, nil, err
	}
	content, err := os.ReadFile(resolved)
	if err != nil {
		return AssetView{}, nil, err
	}
	image, err := ValidateImage(content)
	if err != nil || image.SHA256 != asset.SHA256 || image.MIMEType != asset.MIMEType || image.Width != asset.Width || image.Height != asset.Height {
		return AssetView{}, nil, errors.New("managed artwork image is unavailable")
	}
	applied, err := s.isAssetAppliedUnlocked(id)
	return AssetView{Asset: asset, Applied: applied}, content, err
}

func (s *WorkspaceStore) ListAssets(cursor string, limit int) (Page[AssetView], error) {
	decoded, err := decodeCursor(cursor)
	if err != nil {
		return Page[AssetView]{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	assets, err := s.readAllAssetsUnlocked()
	if err != nil {
		return Page[AssetView]{}, err
	}
	filtered := assets[:0]
	for _, asset := range assets {
		if !recordBeforeCursor(asset.CreatedAt, asset.ID, decoded) {
			continue
		}
		job, err := s.readJobUnlocked(asset.JobID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return Page[AssetView]{}, err
		}
		if job.Status == JobSucceeded && job.AssetID == asset.ID {
			filtered = append(filtered, asset)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if !filtered[i].CreatedAt.Equal(filtered[j].CreatedAt) {
			return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
		}
		return filtered[i].ID < filtered[j].ID
	})
	limit = normalizeLimit(limit)
	page := Page[AssetView]{Items: make([]AssetView, 0, min(limit, len(filtered)))}
	for _, asset := range filtered[:min(limit, len(filtered))] {
		applied, err := s.isAssetAppliedUnlocked(asset.ID)
		if err != nil {
			return Page[AssetView]{}, err
		}
		page.Items = append(page.Items, AssetView{Asset: asset, Applied: applied})
	}
	if len(filtered) > limit {
		last := filtered[limit-1]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func (s *WorkspaceStore) readAllAssetsUnlocked() ([]Asset, error) {
	paths, err := jsonRecordPaths(filepath.Join(s.root, "assets"))
	if err != nil {
		return nil, err
	}
	assets := make([]Asset, 0, len(paths))
	for _, path := range paths {
		var asset Asset
		if err := readJSONFile(path, &asset); err != nil {
			return nil, err
		}
		if err := validateAsset(asset); err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	return assets, nil
}

func (s *WorkspaceStore) ReuseAssetAsDraft(assetID, idempotencyKey string) (Draft, bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 256 {
		return Draft{}, false, errors.New("idempotency_key must contain 1-256 characters")
	}
	keyHash := hashString(idempotencyKey)
	draftID := deterministicID("draft", "reuse", keyHash)
	s.mu.Lock()
	defer s.mu.Unlock()
	path, _ := s.path("drafts", draftID)
	var existing Draft
	if err := readJSONFile(path, &existing); err == nil {
		if existing.CreateKeyHash != keyHash || existing.SourceAssetID != assetID {
			return Draft{}, false, ErrIdempotencyConflict
		}
		return existing, true, validateDraft(existing)
	} else if !os.IsNotExist(err) {
		return Draft{}, false, err
	}
	asset, err := s.readCommittedAssetUnlocked(assetID)
	if err != nil {
		return Draft{}, false, err
	}
	input := DraftInput{
		WorkType: asset.WorkType, Scope: asset.Scope, ScopeID: asset.ScopeID,
		Prompt: asset.Prompt, PromptSource: PromptSourceReuse, SourceAssetID: asset.ID,
		ModelID: asset.Request.ModelID, Size: asset.Request.Size,
	}
	fingerprintValue, _ := fingerprint(input)
	now := s.now()
	draft := Draft{
		SchemaVersion: WorkspaceSchemaVersion, ID: draftID, Version: 1,
		WorkType: input.WorkType, Scope: input.Scope, ScopeID: input.ScopeID,
		Prompt: input.Prompt, PromptSource: PromptSourceReuse, SourceAssetID: asset.ID,
		ModelID: input.ModelID, Size: input.Size, SourceStatus: "current",
		CreatedAt: now, UpdatedAt: now, CreateKeyHash: keyHash, CreateFingerprint: fingerprintValue,
	}
	if err := validateDraft(draft); err != nil {
		return Draft{}, false, err
	}
	if err := writeJSONAtomic(path, draft, true); err != nil {
		return Draft{}, false, err
	}
	return draft, false, nil
}

func copyReaderBounded(reader io.Reader, limit int64) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, errors.New("content exceeds limit")
	}
	return content, nil
}
