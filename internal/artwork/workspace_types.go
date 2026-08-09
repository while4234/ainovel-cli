package artwork

import (
	"errors"
	"fmt"
	"time"
)

const (
	WorkspaceSchemaVersion       = 1
	MaxPromptBytes               = 64 * 1024
	MaxAIPromptRunes             = 4000
	ArtworkSourceSchemaVersion   = 1
	ArtworkPromptTemplateVersion = "artwork-prompt/v1"
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
	PromptSourceAI     PromptSource = "ai"
)

type SourceFragment struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Label     string `json:"label"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}

type SourceSnapshot struct {
	SchemaVersion   int              `json:"schema_version"`
	WorkType        WorkType         `json:"work_type"`
	Scope           string           `json:"scope"`
	ScopeID         string           `json:"scope_id,omitempty"`
	TemplateVersion string           `json:"template_version"`
	Fragments       []SourceFragment `json:"fragments"`
	Digest          string           `json:"digest"`
}

type StalePromptConfirmation struct {
	OriginalSourceDigest  string    `json:"original_source_digest"`
	ConfirmedSourceDigest string    `json:"confirmed_source_digest"`
	ConfirmedAt           time.Time `json:"confirmed_at"`
}

type TextModelSnapshot struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type PromptUsageSnapshot struct {
	Present      bool `json:"present"`
	InputTokens  int  `json:"input_tokens,omitempty"`
	OutputTokens int  `json:"output_tokens,omitempty"`
	TotalTokens  int  `json:"total_tokens,omitempty"`
}

type PromptJobStatus string

const (
	PromptJobQueued    PromptJobStatus = "queued"
	PromptJobRunning   PromptJobStatus = "running"
	PromptJobSucceeded PromptJobStatus = "succeeded"
	PromptJobFailed    PromptJobStatus = "failed"
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
	ErrStalePrompt         = errors.New("artwork prompt source is stale")
	ErrSourceUnavailable   = errors.New("artwork prompt source is unavailable")
	ErrPromptModel         = errors.New("artwork prompt model call failed")
	ErrPromptEmpty         = errors.New("artwork prompt model returned empty text")
	ErrPromptTooLong       = errors.New("artwork prompt model returned too much text")
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
	ConfirmedAt            *time.Time   `json:"confirmed_at,omitempty"`
	SourceStatus           string       `json:"source_status"`
	IsStale                bool         `json:"is_stale"`
	CurrentPromptVersionID string       `json:"current_prompt_version_id,omitempty"`
	CurrentPromptJobID     string       `json:"current_prompt_job_id,omitempty"`
	PreviousPromptJobID    string       `json:"previous_prompt_job_id,omitempty"`
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
	ConfirmedAt            *time.Time   `json:"confirmed_at,omitempty"`
	SourceStatus           string       `json:"source_status"`
	IsStale                bool         `json:"is_stale"`
	CurrentPromptVersionID string       `json:"current_prompt_version_id,omitempty"`
	CurrentPromptJobID     string       `json:"current_prompt_job_id,omitempty"`
	PreviousPromptJobID    string       `json:"previous_prompt_job_id,omitempty"`
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
		ConfirmedAt: d.ConfirmedAt, IsStale: d.IsStale,
		CurrentPromptVersionID: d.CurrentPromptVersionID,
		CurrentPromptJobID:     d.CurrentPromptJobID, PreviousPromptJobID: d.PreviousPromptJobID,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
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
	SchemaVersion           int                `json:"schema_version"`
	ID                      string             `json:"id"`
	DraftID                 string             `json:"draft_id"`
	DraftVersion            int64              `json:"draft_version"`
	Prompt                  string             `json:"prompt"`
	Source                  PromptSource       `json:"source"`
	SourceAssetID           string             `json:"source_asset_id,omitempty"`
	PromptJobID             string             `json:"prompt_job_id,omitempty"`
	PreviousPromptVersionID string             `json:"previous_prompt_version_id,omitempty"`
	SourceSnapshot          *SourceSnapshot    `json:"source_snapshot,omitempty"`
	Model                   *TextModelSnapshot `json:"model,omitempty"`
	CreatedAt               time.Time          `json:"created_at"`
}

type PromptJob struct {
	SchemaVersion       int                 `json:"schema_version"`
	ID                  string              `json:"id"`
	DraftID             string              `json:"draft_id"`
	DraftVersion        int64               `json:"draft_version"`
	Status              PromptJobStatus     `json:"status"`
	Source              SourceSnapshot      `json:"source"`
	Model               TextModelSnapshot   `json:"model"`
	PromptVersionID     string              `json:"prompt_version_id,omitempty"`
	PreviousPromptJobID string              `json:"previous_prompt_job_id,omitempty"`
	IdempotencyKeyHash  string              `json:"idempotency_key_hash"`
	RequestFingerprint  string              `json:"request_fingerprint"`
	AttemptCount        int                 `json:"attempt_count"`
	ErrorCode           string              `json:"error_code,omitempty"`
	Usage               PromptUsageSnapshot `json:"usage"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
	StartedAt           *time.Time          `json:"started_at,omitempty"`
	FinishedAt          *time.Time          `json:"finished_at,omitempty"`
}

type PromptJobView struct {
	ID                  string              `json:"id"`
	DraftID             string              `json:"draft_id"`
	DraftVersion        int64               `json:"draft_version"`
	Status              PromptJobStatus     `json:"status"`
	SourceDigest        string              `json:"source_digest"`
	SourceSchemaVersion int                 `json:"source_schema_version"`
	TemplateVersion     string              `json:"template_version"`
	Model               TextModelSnapshot   `json:"model"`
	PromptVersionID     string              `json:"prompt_version_id,omitempty"`
	PreviousPromptJobID string              `json:"previous_prompt_job_id,omitempty"`
	AttemptCount        int                 `json:"attempt_count"`
	ErrorCode           string              `json:"error_code,omitempty"`
	Usage               PromptUsageSnapshot `json:"usage"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
	StartedAt           *time.Time          `json:"started_at,omitempty"`
	FinishedAt          *time.Time          `json:"finished_at,omitempty"`
}

func (j PromptJob) Public() PromptJobView {
	return PromptJobView{
		ID: j.ID, DraftID: j.DraftID, DraftVersion: j.DraftVersion, Status: j.Status,
		SourceDigest: j.Source.Digest, SourceSchemaVersion: j.Source.SchemaVersion,
		TemplateVersion: j.Source.TemplateVersion, Model: j.Model,
		PromptVersionID: j.PromptVersionID, PreviousPromptJobID: j.PreviousPromptJobID,
		AttemptCount: j.AttemptCount, ErrorCode: j.ErrorCode, Usage: j.Usage,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, StartedAt: j.StartedAt,
		FinishedAt: j.FinishedAt,
	}
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
	SchemaVersion      int                      `json:"schema_version"`
	ID                 string                   `json:"id"`
	DraftID            string                   `json:"draft_id"`
	DraftVersion       int64                    `json:"draft_version"`
	WorkType           WorkType                 `json:"work_type"`
	Scope              string                   `json:"scope"`
	ScopeID            string                   `json:"scope_id,omitempty"`
	PromptSource       PromptSource             `json:"prompt_source"`
	SourceAssetID      string                   `json:"source_asset_id,omitempty"`
	PromptVersionID    string                   `json:"prompt_version_id"`
	AssetID            string                   `json:"asset_id"`
	Status             JobStatus                `json:"status"`
	Request            ProviderRequestSnapshot  `json:"request"`
	Internal           JobInternalSnapshot      `json:"internal"`
	IdempotencyKeyHash string                   `json:"idempotency_key_hash"`
	RequestFingerprint string                   `json:"request_fingerprint"`
	SourceSnapshot     *SourceSnapshot          `json:"source_snapshot,omitempty"`
	StaleConfirmation  *StalePromptConfirmation `json:"stale_confirmation,omitempty"`
	AttemptCount       int                      `json:"attempt_count"`
	ErrorCode          string                   `json:"error_code,omitempty"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
	StartedAt          *time.Time               `json:"started_at,omitempty"`
	FinishedAt         *time.Time               `json:"finished_at,omitempty"`
}

type ImageJobView struct {
	ID                string                   `json:"id"`
	DraftID           string                   `json:"draft_id"`
	DraftVersion      int64                    `json:"draft_version"`
	WorkType          WorkType                 `json:"work_type"`
	Scope             string                   `json:"scope"`
	ScopeID           string                   `json:"scope_id,omitempty"`
	PromptSource      PromptSource             `json:"prompt_source"`
	SourceAssetID     string                   `json:"source_asset_id,omitempty"`
	PromptVersionID   string                   `json:"prompt_version_id"`
	AssetID           string                   `json:"asset_id"`
	Status            JobStatus                `json:"status"`
	Request           ProviderRequestSnapshot  `json:"request"`
	SourceDigest      string                   `json:"source_digest,omitempty"`
	StaleConfirmation *StalePromptConfirmation `json:"stale_confirmation,omitempty"`
	AttemptCount      int                      `json:"attempt_count"`
	ErrorCode         string                   `json:"error_code,omitempty"`
	CreatedAt         time.Time                `json:"created_at"`
	UpdatedAt         time.Time                `json:"updated_at"`
	StartedAt         *time.Time               `json:"started_at,omitempty"`
	FinishedAt        *time.Time               `json:"finished_at,omitempty"`
}

func (j ImageJob) Public() ImageJobView {
	view := ImageJobView{
		ID: j.ID, DraftID: j.DraftID, DraftVersion: j.DraftVersion,
		WorkType: j.WorkType, Scope: j.Scope, ScopeID: j.ScopeID,
		PromptSource: j.PromptSource, SourceAssetID: j.SourceAssetID,
		PromptVersionID: j.PromptVersionID, AssetID: j.AssetID, Status: j.Status,
		Request: j.Request, AttemptCount: j.AttemptCount, ErrorCode: j.ErrorCode,
		StaleConfirmation: j.StaleConfirmation,
		CreatedAt:         j.CreatedAt, UpdatedAt: j.UpdatedAt, StartedAt: j.StartedAt,
		FinishedAt: j.FinishedAt,
	}
	if j.SourceSnapshot != nil {
		view.SourceDigest = j.SourceSnapshot.Digest
	}
	return view
}

type Asset struct {
	SchemaVersion     int                      `json:"schema_version"`
	ID                string                   `json:"id"`
	DraftID           string                   `json:"draft_id"`
	DraftVersion      int64                    `json:"draft_version"`
	PromptVersionID   string                   `json:"prompt_version_id"`
	JobID             string                   `json:"job_id"`
	WorkType          WorkType                 `json:"work_type"`
	Scope             string                   `json:"scope"`
	ScopeID           string                   `json:"scope_id,omitempty"`
	Prompt            string                   `json:"prompt"`
	PromptSource      PromptSource             `json:"prompt_source"`
	ReusedFromAssetID string                   `json:"reused_from_asset_id,omitempty"`
	SourceSnapshot    *SourceSnapshot          `json:"source_snapshot,omitempty"`
	StaleConfirmation *StalePromptConfirmation `json:"stale_confirmation,omitempty"`
	Request           ProviderRequestSnapshot  `json:"request"`
	Origin            string                   `json:"origin"`
	FileName          string                   `json:"file_name"`
	MIMEType          string                   `json:"mime_type"`
	Width             int                      `json:"width"`
	Height            int                      `json:"height"`
	SHA256            string                   `json:"sha256"`
	CreatedAt         time.Time                `json:"created_at"`
}

type AssetView struct {
	Asset
	Applied bool `json:"applied"`
}

type ApplyState struct {
	SchemaVersion int               `json:"schema_version"`
	Target        string            `json:"target"`
	AssetID       string            `json:"asset_id"`
	WorkType      WorkType          `json:"work_type,omitempty"`
	Scope         string            `json:"scope,omitempty"`
	ScopeID       string            `json:"scope_id,omitempty"`
	Derivative    AppliedDerivative `json:"derivative,omitempty"`
	AppliedAt     time.Time         `json:"applied_at"`
}

type AppliedDerivative struct {
	Version           string `json:"version"`
	FileName          string `json:"file_name"`
	MIMEType          string `json:"mime_type"`
	Width             int    `json:"width"`
	Height            int    `json:"height"`
	SHA256            string `json:"sha256"`
	Fit               string `json:"fit"`
	SourceOrientation int    `json:"source_orientation"`
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type WorkspaceSnapshot struct {
	SchemaVersion int                 `json:"schema_version"`
	Drafts        Page[DraftView]     `json:"drafts"`
	PromptJobs    Page[PromptJobView] `json:"prompt_jobs"`
	Jobs          Page[ImageJobView]  `json:"jobs"`
	Assets        Page[AssetView]     `json:"assets"`
	Applied       []ApplyState        `json:"applied"`
}
