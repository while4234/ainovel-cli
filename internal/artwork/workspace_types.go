package artwork

import (
	"errors"
	"fmt"
	"time"
)

const (
	WorkspaceSchemaVersion = 1
	MaxPromptBytes         = 64 * 1024
)

type WorkType string

const (
	WorkTypeCover             WorkType = "cover"
	WorkTypeIllustration      WorkType = "illustration"
	WorkTypeCharacterPortrait WorkType = "character_portrait"
)

type PromptSource string

const (
	PromptSourceManual PromptSource = "manual"
	PromptSourceReuse  PromptSource = "reuse"
)

type JobStatus string

const (
	JobQueued             JobStatus = "queued"
	JobRunning            JobStatus = "running"
	JobSucceeded          JobStatus = "succeeded"
	JobFailed             JobStatus = "failed"
	JobInterruptedUnknown JobStatus = "interrupted_unknown"
)

func (s JobStatus) Terminal() bool {
	return s == JobSucceeded || s == JobFailed || s == JobInterruptedUnknown
}

var (
	ErrNotFound            = errors.New("artwork record not found")
	ErrConflict            = errors.New("artwork operation conflict")
	ErrInvalidCursor       = errors.New("invalid artwork cursor")
	ErrAppliedAsset        = errors.New("applied artwork asset cannot be deleted")
	ErrIdempotencyConflict = errors.New("idempotency key was already used for a different request")
)

type VersionConflictError struct {
	Expected int64
	Actual   int64
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("artwork draft version conflict: expected %d, actual %d", e.Expected, e.Actual)
}

type Draft struct {
	SchemaVersion          int          `json:"schema_version"`
	ID                     string       `json:"id"`
	Version                int64        `json:"version"`
	WorkType               WorkType     `json:"work_type"`
	Scope                  string       `json:"scope"`
	ScopeID                string       `json:"scope_id,omitempty"`
	Prompt                 string       `json:"prompt"`
	PromptSource           PromptSource `json:"prompt_source"`
	SourceAssetID          string       `json:"source_asset_id,omitempty"`
	ModelID                string       `json:"model_id"`
	Size                   string       `json:"size"`
	SourceSignature        string       `json:"source_signature,omitempty"`
	CurrentSourceSignature string       `json:"current_source_signature,omitempty"`
	ConfirmedSignature     string       `json:"confirmed_source_signature,omitempty"`
	SourceStatus           string       `json:"source_status"`
	IsStale                bool         `json:"is_stale"`
	CreatedAt              time.Time    `json:"created_at"`
	UpdatedAt              time.Time    `json:"updated_at"`
	CreateKeyHash          string       `json:"create_key_hash,omitempty"`
	CreateFingerprint      string       `json:"create_fingerprint,omitempty"`
}

type DraftView struct {
	ID                     string       `json:"id"`
	Version                int64        `json:"version"`
	WorkType               WorkType     `json:"work_type"`
	Scope                  string       `json:"scope"`
	ScopeID                string       `json:"scope_id,omitempty"`
	Prompt                 string       `json:"prompt"`
	PromptSource           PromptSource `json:"prompt_source"`
	SourceAssetID          string       `json:"source_asset_id,omitempty"`
	ModelID                string       `json:"model_id"`
	Size                   string       `json:"size"`
	SourceSignature        string       `json:"source_signature,omitempty"`
	CurrentSourceSignature string       `json:"current_source_signature,omitempty"`
	ConfirmedSignature     string       `json:"confirmed_source_signature,omitempty"`
	SourceStatus           string       `json:"source_status"`
	IsStale                bool         `json:"is_stale"`
	CreatedAt              time.Time    `json:"created_at"`
	UpdatedAt              time.Time    `json:"updated_at"`
}

func (d Draft) Public() DraftView {
	return DraftView{
		ID: d.ID, Version: d.Version, WorkType: d.WorkType, Scope: d.Scope,
		ScopeID: d.ScopeID, Prompt: d.Prompt, PromptSource: d.PromptSource,
		SourceAssetID: d.SourceAssetID, ModelID: d.ModelID, Size: d.Size,
		SourceSignature: d.SourceSignature, CurrentSourceSignature: d.CurrentSourceSignature,
		ConfirmedSignature: d.ConfirmedSignature, SourceStatus: d.SourceStatus,
		IsStale: d.IsStale, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

type DraftInput struct {
	WorkType      WorkType
	Scope         string
	ScopeID       string
	Prompt        string
	PromptSource  PromptSource
	SourceAssetID string
	ModelID       string
	Size          string
}

type DraftPatch struct {
	ExpectedVersion int64
	WorkType        *WorkType
	Scope           *string
	ScopeID         *string
	Prompt          *string
	ModelID         *string
	Size            *string
}

type ArtworkPromptVersion struct {
	SchemaVersion int          `json:"schema_version"`
	ID            string       `json:"id"`
	DraftID       string       `json:"draft_id"`
	DraftVersion  int64        `json:"draft_version"`
	Prompt        string       `json:"prompt"`
	Source        PromptSource `json:"source"`
	SourceAssetID string       `json:"source_asset_id,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
}

type ProviderRequestSnapshot struct {
	CatalogVersion string `json:"catalog_version"`
	ModelID        string `json:"model_id"`
	ResolvedModel  string `json:"resolved_model"`
	Size           string `json:"size"`
	AspectRatio    string `json:"aspect_ratio,omitempty"`
	Resolution     string `json:"resolution,omitempty"`
	ResponseFormat string `json:"response_format"`
}

type JobInternalSnapshot struct {
	GatewayEndpoint string        `json:"gateway_endpoint"`
	Delivery        DeliveryState `json:"delivery,omitempty"`
	ProviderCode    string        `json:"provider_code,omitempty"`
}

type ImageJob struct {
	SchemaVersion      int                     `json:"schema_version"`
	ID                 string                  `json:"id"`
	DraftID            string                  `json:"draft_id"`
	DraftVersion       int64                   `json:"draft_version"`
	WorkType           WorkType                `json:"work_type"`
	Scope              string                  `json:"scope"`
	ScopeID            string                  `json:"scope_id,omitempty"`
	PromptSource       PromptSource            `json:"prompt_source"`
	SourceAssetID      string                  `json:"source_asset_id,omitempty"`
	PromptVersionID    string                  `json:"prompt_version_id"`
	AssetID            string                  `json:"asset_id"`
	Status             JobStatus               `json:"status"`
	Request            ProviderRequestSnapshot `json:"request"`
	Internal           JobInternalSnapshot     `json:"internal"`
	IdempotencyKeyHash string                  `json:"idempotency_key_hash"`
	RequestFingerprint string                  `json:"request_fingerprint"`
	AttemptCount       int                     `json:"attempt_count"`
	ErrorCode          string                  `json:"error_code,omitempty"`
	CreatedAt          time.Time               `json:"created_at"`
	UpdatedAt          time.Time               `json:"updated_at"`
	StartedAt          *time.Time              `json:"started_at,omitempty"`
	FinishedAt         *time.Time              `json:"finished_at,omitempty"`
}

type ImageJobView struct {
	ID              string                  `json:"id"`
	DraftID         string                  `json:"draft_id"`
	DraftVersion    int64                   `json:"draft_version"`
	WorkType        WorkType                `json:"work_type"`
	Scope           string                  `json:"scope"`
	ScopeID         string                  `json:"scope_id,omitempty"`
	PromptSource    PromptSource            `json:"prompt_source"`
	SourceAssetID   string                  `json:"source_asset_id,omitempty"`
	PromptVersionID string                  `json:"prompt_version_id"`
	AssetID         string                  `json:"asset_id"`
	Status          JobStatus               `json:"status"`
	Request         ProviderRequestSnapshot `json:"request"`
	AttemptCount    int                     `json:"attempt_count"`
	ErrorCode       string                  `json:"error_code,omitempty"`
	CreatedAt       time.Time               `json:"created_at"`
	UpdatedAt       time.Time               `json:"updated_at"`
	StartedAt       *time.Time              `json:"started_at,omitempty"`
	FinishedAt      *time.Time              `json:"finished_at,omitempty"`
}

func (j ImageJob) Public() ImageJobView {
	return ImageJobView{
		ID: j.ID, DraftID: j.DraftID, DraftVersion: j.DraftVersion,
		WorkType: j.WorkType, Scope: j.Scope, ScopeID: j.ScopeID,
		PromptSource: j.PromptSource, SourceAssetID: j.SourceAssetID,
		PromptVersionID: j.PromptVersionID, AssetID: j.AssetID, Status: j.Status,
		Request: j.Request, AttemptCount: j.AttemptCount, ErrorCode: j.ErrorCode,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, StartedAt: j.StartedAt,
		FinishedAt: j.FinishedAt,
	}
}

type Asset struct {
	SchemaVersion     int                     `json:"schema_version"`
	ID                string                  `json:"id"`
	DraftID           string                  `json:"draft_id"`
	DraftVersion      int64                   `json:"draft_version"`
	PromptVersionID   string                  `json:"prompt_version_id"`
	JobID             string                  `json:"job_id"`
	WorkType          WorkType                `json:"work_type"`
	Scope             string                  `json:"scope"`
	ScopeID           string                  `json:"scope_id,omitempty"`
	Prompt            string                  `json:"prompt"`
	PromptSource      PromptSource            `json:"prompt_source"`
	ReusedFromAssetID string                  `json:"reused_from_asset_id,omitempty"`
	Request           ProviderRequestSnapshot `json:"request"`
	Origin            string                  `json:"origin"`
	FileName          string                  `json:"file_name"`
	MIMEType          string                  `json:"mime_type"`
	Width             int                     `json:"width"`
	Height            int                     `json:"height"`
	SHA256            string                  `json:"sha256"`
	CreatedAt         time.Time               `json:"created_at"`
}

type AssetView struct {
	Asset
	Applied bool `json:"applied"`
}

type ApplyState struct {
	SchemaVersion int       `json:"schema_version"`
	Target        string    `json:"target"`
	AssetID       string    `json:"asset_id"`
	AppliedAt     time.Time `json:"applied_at"`
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type WorkspaceSnapshot struct {
	SchemaVersion int                `json:"schema_version"`
	Drafts        Page[DraftView]    `json:"drafts"`
	Jobs          Page[ImageJobView] `json:"jobs"`
	Assets        Page[AssetView]    `json:"assets"`
	Applied       []ApplyState       `json:"applied"`
}
