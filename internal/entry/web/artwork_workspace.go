package web

import (
	"errors"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	artworkpkg "github.com/voocel/ainovel-cli/internal/artwork"
)

type artworkDraftRequest struct {
	WorkType       artworkpkg.WorkType `json:"work_type"`
	Scope          string              `json:"scope"`
	ScopeID        string              `json:"scope_id"`
	Prompt         string              `json:"prompt"`
	ModelID        string              `json:"model_id"`
	Size           string              `json:"size"`
	IdempotencyKey string              `json:"idempotency_key"`
}

type artworkDraftPatchRequest struct {
	ExpectedVersion int64                `json:"expected_version"`
	WorkType        *artworkpkg.WorkType `json:"work_type"`
	Scope           *string              `json:"scope"`
	ScopeID         *string              `json:"scope_id"`
	Prompt          *string              `json:"prompt"`
	ModelID         *string              `json:"model_id"`
	Size            *string              `json:"size"`
}

type artworkGenerationRequest struct {
	ExpectedVersion int64  `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type artworkAssetHTTPView struct {
	artworkpkg.AssetView
	ContentURL  string `json:"content_url"`
	DownloadURL string `json:"download_url"`
}

func (s *Server) handleProjectArtwork(w http.ResponseWriter, r *http.Request, projectID, action string) {
	manifest, err := s.store.OpenProject(projectID)
	if err != nil {
		writeProjectManifestError(w, err)
		return
	}
	workspace, err := s.artworkRuntime.storeFor(manifest)
	if err != nil {
		writeArtworkWorkspaceError(w, err, false)
		return
	}
	parts := splitArtworkAction(action)
	if len(parts) == 0 {
		s.handleArtworkWorkspaceRoot(w, r, projectID, workspace)
		return
	}
	switch parts[0] {
	case "drafts":
		s.handleArtworkDraftRoute(w, r, projectID, workspace, parts[1:])
	case "jobs":
		s.handleArtworkJobRoute(w, r, workspace, parts[1:])
	case "assets":
		s.handleArtworkAssetRoute(w, r, projectID, workspace, parts[1:])
	default:
		http.NotFound(w, r)
	}
}

func splitArtworkAction(action string) []string {
	action = strings.Trim(action, "/")
	if action == "" {
		return nil
	}
	return strings.Split(action, "/")
}

func (s *Server) handleArtworkWorkspaceRoot(w http.ResponseWriter, r *http.Request, projectID string, store *artworkpkg.WorkspaceStore) {
	if r.Method != http.MethodGet {
		writeArtworkWorkspaceMethodError(w)
		return
	}
	limit, err := artworkListLimit(r)
	if err != nil {
		writeArtworkWorkspaceError(w, err, true)
		return
	}
	snapshot, err := store.Workspace(limit)
	if err != nil {
		writeArtworkWorkspaceError(w, err, false)
		return
	}
	assets := make([]artworkAssetHTTPView, 0, len(snapshot.Assets.Items))
	for _, asset := range snapshot.Assets.Items {
		assets = append(assets, artworkAssetResponse(projectID, asset))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": snapshot.SchemaVersion,
		"drafts":         snapshot.Drafts, "jobs": snapshot.Jobs,
		"assets":  map[string]any{"items": assets, "next_cursor": snapshot.Assets.NextCursor},
		"applied": snapshot.Applied,
	})
}

func (s *Server) handleArtworkDraftRoute(w http.ResponseWriter, r *http.Request, projectID string, store *artworkpkg.WorkspaceStore, parts []string) {
	if len(parts) == 0 {
		s.handleArtworkDraftCollection(w, r, store)
		return
	}
	if len(parts) > 2 {
		http.NotFound(w, r)
		return
	}
	draftID := parts[0]
	if len(parts) == 2 {
		switch parts[1] {
		case "generate-image":
			s.handleArtworkGenerate(w, r, projectID, store, draftID)
		case "confirm-stale-prompt":
			s.handleArtworkConfirmStale(w, r, store, draftID)
		default:
			http.NotFound(w, r)
		}
		return
	}
	switch r.Method {
	case http.MethodGet:
		draft, err := store.GetDraft(draftID)
		if err != nil {
			writeArtworkWorkspaceError(w, err, false)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"draft": draft.Public()})
	case http.MethodPatch:
		var request artworkDraftPatchRequest
		if err := decodeJSONBody(r, &request); err != nil {
			writeArtworkWorkspaceError(w, err, true)
			return
		}
		draft, err := store.UpdateDraft(draftID, artworkpkg.DraftPatch{
			ExpectedVersion: request.ExpectedVersion, WorkType: request.WorkType,
			Scope: request.Scope, ScopeID: request.ScopeID, Prompt: request.Prompt,
			ModelID: request.ModelID, Size: request.Size,
		})
		if err != nil {
			writeArtworkWorkspaceError(w, err, true)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"draft": draft.Public()})
	case http.MethodDelete:
		expected, err := strconv.ParseInt(r.URL.Query().Get("expected_version"), 10, 64)
		if err != nil || expected <= 0 {
			writeArtworkWorkspaceError(w, errors.New("expected_version query parameter must be positive"), true)
			return
		}
		if err := store.DeleteDraft(draftID, expected); err != nil {
			writeArtworkWorkspaceError(w, err, true)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeArtworkWorkspaceMethodError(w)
	}
}

func (s *Server) handleArtworkDraftCollection(w http.ResponseWriter, r *http.Request, store *artworkpkg.WorkspaceStore) {
	switch r.Method {
	case http.MethodGet:
		limit, err := artworkListLimit(r)
		if err != nil {
			writeArtworkWorkspaceError(w, err, true)
			return
		}
		page, err := store.ListDrafts(r.URL.Query().Get("cursor"), limit)
		if err != nil {
			writeArtworkWorkspaceError(w, err, false)
			return
		}
		writeJSON(w, http.StatusOK, page)
	case http.MethodPost:
		var request artworkDraftRequest
		if err := decodeJSONBody(r, &request); err != nil {
			writeArtworkWorkspaceError(w, err, true)
			return
		}
		key, err := artworkIdempotencyKey(r, request.IdempotencyKey, false)
		if err != nil {
			writeArtworkWorkspaceError(w, err, true)
			return
		}
		draft, err := store.CreateDraft(artworkpkg.DraftInput{
			WorkType: request.WorkType, Scope: request.Scope, ScopeID: request.ScopeID,
			Prompt: request.Prompt, PromptSource: artworkpkg.PromptSourceManual,
			ModelID: request.ModelID, Size: request.Size,
		}, key)
		if err != nil {
			writeArtworkWorkspaceError(w, err, true)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"draft": draft.Public()})
	default:
		writeArtworkWorkspaceMethodError(w)
	}
}

func (s *Server) handleArtworkGenerate(w http.ResponseWriter, r *http.Request, projectID string, store *artworkpkg.WorkspaceStore, draftID string) {
	if r.Method != http.MethodPost {
		writeArtworkWorkspaceMethodError(w)
		return
	}
	var request artworkGenerationRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeArtworkWorkspaceError(w, err, true)
		return
	}
	key, err := artworkIdempotencyKey(r, request.IdempotencyKey, true)
	if err != nil {
		writeArtworkWorkspaceError(w, err, true)
		return
	}
	config, err := effectiveArtworkGatewayConfig(s.currentConfig())
	if err != nil || config.BaseURL == "" || config.APIKey == "" {
		writeArtworkError(w, http.StatusConflict, "gateway_not_configured", "image gateway is not configured", artworkpkg.DeliveryNotSent)
		return
	}
	job, reused, err := store.SubmitGeneration(draftID, request.ExpectedVersion, key, config.BaseURL)
	if err != nil {
		writeArtworkWorkspaceError(w, err, true)
		return
	}
	manifest, err := s.store.projectManifestWithoutTouch(projectID)
	if err != nil {
		writeProjectManifestError(w, err)
		return
	}
	s.artworkRuntime.schedule(manifest, store, job.ID)
	status := http.StatusAccepted
	if reused {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"job": job.Public(), "reused": reused})
}

func (s *Server) handleArtworkConfirmStale(w http.ResponseWriter, r *http.Request, store *artworkpkg.WorkspaceStore, draftID string) {
	if r.Method != http.MethodPost {
		writeArtworkWorkspaceMethodError(w)
		return
	}
	var request struct {
		ExpectedVersion int64  `json:"expected_version"`
		SourceSignature string `json:"source_signature"`
	}
	if err := decodeJSONBody(r, &request); err != nil {
		writeArtworkWorkspaceError(w, err, true)
		return
	}
	draft, err := store.ConfirmStalePrompt(draftID, request.ExpectedVersion, request.SourceSignature)
	if err != nil {
		writeArtworkWorkspaceError(w, err, true)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"draft": draft.Public()})
}

func (s *Server) handleArtworkJobRoute(w http.ResponseWriter, r *http.Request, store *artworkpkg.WorkspaceStore, parts []string) {
	if r.Method != http.MethodGet {
		writeArtworkWorkspaceMethodError(w)
		return
	}
	if len(parts) == 0 {
		limit, err := artworkListLimit(r)
		if err != nil {
			writeArtworkWorkspaceError(w, err, true)
			return
		}
		page, err := store.ListJobs(r.URL.Query().Get("cursor"), limit)
		if err != nil {
			writeArtworkWorkspaceError(w, err, false)
			return
		}
		writeJSON(w, http.StatusOK, page)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	job, err := store.GetJob(parts[0])
	if err != nil {
		writeArtworkWorkspaceError(w, err, false)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": job.Public()})
}

func (s *Server) handleArtworkAssetRoute(w http.ResponseWriter, r *http.Request, projectID string, store *artworkpkg.WorkspaceStore, parts []string) {
	if len(parts) == 0 {
		if r.Method != http.MethodGet {
			writeArtworkWorkspaceMethodError(w)
			return
		}
		limit, err := artworkListLimit(r)
		if err != nil {
			writeArtworkWorkspaceError(w, err, true)
			return
		}
		page, err := store.ListAssets(r.URL.Query().Get("cursor"), limit)
		if err != nil {
			writeArtworkWorkspaceError(w, err, false)
			return
		}
		items := make([]artworkAssetHTTPView, 0, len(page.Items))
		for _, asset := range page.Items {
			items = append(items, artworkAssetResponse(projectID, asset))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": page.NextCursor})
		return
	}
	if len(parts) > 2 {
		http.NotFound(w, r)
		return
	}
	assetID := parts[0]
	if len(parts) == 2 {
		switch parts[1] {
		case "content":
			s.handleArtworkAssetContent(w, r, store, assetID, false)
		case "download":
			s.handleArtworkAssetContent(w, r, store, assetID, true)
		case "apply":
			if r.Method != http.MethodPost {
				writeArtworkWorkspaceMethodError(w)
				return
			}
			state, err := store.ApplyAsset(assetID)
			if err != nil {
				writeArtworkWorkspaceError(w, err, false)
				return
			}
			asset, _ := store.GetAsset(assetID)
			writeJSON(w, http.StatusOK, map[string]any{"asset": artworkAssetResponse(projectID, asset), "applied_to": state.Target})
		case "reuse-as-draft":
			s.handleArtworkAssetReuse(w, r, store, assetID)
		default:
			http.NotFound(w, r)
		}
		return
	}
	switch r.Method {
	case http.MethodGet:
		asset, err := store.GetAsset(assetID)
		if err != nil {
			writeArtworkWorkspaceError(w, err, false)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"asset": artworkAssetResponse(projectID, asset)})
	case http.MethodDelete:
		if err := store.DeleteAsset(assetID); err != nil {
			writeArtworkWorkspaceError(w, err, false)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeArtworkWorkspaceMethodError(w)
	}
}

func (s *Server) handleArtworkAssetContent(w http.ResponseWriter, r *http.Request, store *artworkpkg.WorkspaceStore, assetID string, download bool) {
	if r.Method != http.MethodGet {
		writeArtworkWorkspaceMethodError(w)
		return
	}
	asset, content, err := store.ReadAssetContent(assetID)
	if err != nil {
		writeArtworkWorkspaceError(w, err, false)
		return
	}
	w.Header().Set("Content-Type", asset.MIMEType)
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if download {
		disposition := mime.FormatMediaType("attachment", map[string]string{"filename": asset.FileName})
		w.Header().Set("Content-Disposition", disposition)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func (s *Server) handleArtworkAssetReuse(w http.ResponseWriter, r *http.Request, store *artworkpkg.WorkspaceStore, assetID string) {
	if r.Method != http.MethodPost {
		writeArtworkWorkspaceMethodError(w)
		return
	}
	var request struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := decodeJSONBody(r, &request); err != nil {
		writeArtworkWorkspaceError(w, err, true)
		return
	}
	key, err := artworkIdempotencyKey(r, request.IdempotencyKey, true)
	if err != nil {
		writeArtworkWorkspaceError(w, err, true)
		return
	}
	draft, reused, err := store.ReuseAssetAsDraft(assetID, key)
	if err != nil {
		writeArtworkWorkspaceError(w, err, true)
		return
	}
	status := http.StatusCreated
	if reused {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"draft": draft.Public(), "reused": reused})
}

func artworkAssetResponse(projectID string, asset artworkpkg.AssetView) artworkAssetHTTPView {
	base := "/api/projects/" + url.PathEscape(projectID) + "/artwork/assets/" + url.PathEscape(asset.ID)
	return artworkAssetHTTPView{AssetView: asset, ContentURL: base + "/content", DownloadURL: base + "/download"}
}

func artworkListLimit(r *http.Request) (int, error) {
	value := strings.TrimSpace(r.URL.Query().Get("limit"))
	if value == "" {
		return 50, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 100 {
		return 0, errors.New("limit must be between 1 and 100")
	}
	return limit, nil
}

func artworkIdempotencyKey(r *http.Request, bodyValue string, required bool) (string, error) {
	headerValue := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	bodyValue = strings.TrimSpace(bodyValue)
	if headerValue != "" && bodyValue != "" && headerValue != bodyValue {
		return "", errors.New("Idempotency-Key header and idempotency_key body must match")
	}
	value := headerValue
	if value == "" {
		value = bodyValue
	}
	if required && value == "" {
		return "", errors.New("idempotency key is required")
	}
	if len(value) > 256 {
		return "", errors.New("idempotency key must not exceed 256 characters")
	}
	return value, nil
}

func writeArtworkWorkspaceMethodError(w http.ResponseWriter) {
	writeArtworkError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", artworkpkg.DeliveryNotSent)
}

func writeArtworkWorkspaceError(w http.ResponseWriter, err error, requestMayBeInvalid bool) {
	var versionErr *artworkpkg.VersionConflictError
	switch {
	case errors.Is(err, artworkpkg.ErrNotFound), errors.Is(err, os.ErrNotExist):
		writeArtworkError(w, http.StatusNotFound, "artwork_not_found", "artwork record was not found", artworkpkg.DeliveryNotSent)
	case errors.As(err, &versionErr):
		writeArtworkError(w, http.StatusConflict, "draft_version_conflict", "artwork draft was changed by another request", artworkpkg.DeliveryNotSent)
	case errors.Is(err, artworkpkg.ErrIdempotencyConflict):
		writeArtworkError(w, http.StatusConflict, "idempotency_conflict", "idempotency key was already used for another artwork request", artworkpkg.DeliveryNotSent)
	case errors.Is(err, artworkpkg.ErrAppliedAsset):
		writeArtworkError(w, http.StatusConflict, "asset_is_applied", "applied artwork assets cannot be deleted", artworkpkg.DeliveryNotSent)
	case errors.Is(err, artworkpkg.ErrConflict):
		writeArtworkError(w, http.StatusConflict, "artwork_conflict", "artwork operation conflicts with current project state", artworkpkg.DeliveryNotSent)
	case errors.Is(err, artworkpkg.ErrInvalidCursor):
		writeArtworkError(w, http.StatusBadRequest, "invalid_cursor", "artwork cursor is invalid", artworkpkg.DeliveryNotSent)
	case requestMayBeInvalid:
		writeArtworkError(w, http.StatusBadRequest, "invalid_artwork_request", "artwork request is invalid", artworkpkg.DeliveryNotSent)
	default:
		writeArtworkError(w, http.StatusInternalServerError, "artwork_storage_failed", "artwork workspace operation failed", artworkpkg.DeliveryNotSent)
	}
}
