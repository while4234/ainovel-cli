package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func (s *Server) handleProjectFoundation(w http.ResponseWriter, r *http.Request, id, action string) {
	manifest, err := s.store.OpenProject(id)
	if err != nil {
		writeProjectManifestError(w, err)
		return
	}
	service := host.NewFoundationRevisionService(storepkg.NewStore(manifest.OutputDir))
	switch action {
	case "foundation":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		state, err := service.State()
		if err != nil {
			writeFoundationRevisionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "foundation": state})
	case "foundation/preview":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var request host.FoundationPreviewRequest
		if err := decodeFoundationJSONBody(r, &request); err != nil {
			if foundationSourceMutationAttempt(err) {
				log.Printf("foundation source mutation attempt rejected project=%s endpoint=preview", id)
				writeFoundationRevisionError(w, &host.FoundationRevisionError{Code: host.FoundationErrorSourceMutation, Err: errors.New("Foundation preview accepts only a complete target candidate")})
				return
			}
			writeError(w, http.StatusBadRequest, "invalid Foundation preview request: "+err.Error())
			return
		}
		preview, err := service.Preview(request)
		if err != nil {
			writeFoundationRevisionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "preview": preview})
	case "foundation/apply":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var request host.FoundationApplyRequest
		if err := decodeFoundationJSONBody(r, &request); err != nil {
			if foundationSourceMutationAttempt(err) {
				log.Printf("foundation source mutation attempt rejected project=%s endpoint=apply", id)
				writeFoundationRevisionError(w, &host.FoundationRevisionError{Code: host.FoundationErrorSourceMutation, Err: errors.New("Foundation apply accepts only preview_id and idempotency_key")})
				return
			}
			writeError(w, http.StatusBadRequest, "invalid Foundation apply request: "+err.Error())
			return
		}
		projectSession, _, err := s.sessions.Open(id)
		if err != nil {
			writeProjectSessionError(w, err)
			return
		}
		unlock, err := projectSession.beginActionKind("foundation_revision")
		if err != nil {
			writeFoundationRevisionError(w, &host.FoundationRevisionError{Code: host.FoundationErrorBusy, Err: err})
			return
		}
		runtime, err := service.Apply(request)
		unlock()
		if err != nil {
			writeFoundationRevisionError(w, err)
			return
		}
		if runtime.Stage == "regenerating" {
			var resumeErr error
			if runtime.ProjectMode == "adaptation" {
				_, resumeErr = projectSession.ResumeAdaptationFoundationRevision()
			} else {
				_, resumeErr = projectSession.ResumeFoundationRevision()
			}
			if resumeErr != nil {
				_ = service.MarkRegenerationFailure(resumeErr)
				writeFoundationRevisionError(w, &host.FoundationRevisionError{Code: host.FoundationErrorRecovery, Err: resumeErr})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "revision": runtime})
	case "foundation/retry":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var request struct {
			IdempotencyKey string `json:"idempotency_key"`
		}
		if err := decodeFoundationJSONBody(r, &request); err != nil {
			if foundationSourceMutationAttempt(err) {
				log.Printf("foundation source mutation attempt rejected project=%s endpoint=retry", id)
				writeFoundationRevisionError(w, &host.FoundationRevisionError{Code: host.FoundationErrorSourceMutation, Err: errors.New("Foundation retry accepts only idempotency_key")})
				return
			}
			writeError(w, http.StatusBadRequest, "invalid Foundation retry request: "+err.Error())
			return
		}
		projectSession, _, err := s.sessions.Open(id)
		if err != nil {
			writeProjectSessionError(w, err)
			return
		}
		unlock, err := projectSession.beginActionKind("foundation_revision")
		if err != nil {
			writeFoundationRevisionError(w, &host.FoundationRevisionError{Code: host.FoundationErrorBusy, Err: err})
			return
		}
		runtime, err := service.Retry(strings.TrimSpace(request.IdempotencyKey))
		unlock()
		if err != nil {
			writeFoundationRevisionError(w, err)
			return
		}
		if runtime.Stage == "regenerating" {
			var resumeErr error
			if runtime.ProjectMode == "adaptation" {
				_, resumeErr = projectSession.ResumeAdaptationFoundationRevision()
			} else {
				_, resumeErr = projectSession.ResumeFoundationRevision()
			}
			if resumeErr != nil {
				_ = service.MarkRegenerationFailure(resumeErr)
				writeFoundationRevisionError(w, &host.FoundationRevisionError{Code: host.FoundationErrorRecovery, Err: resumeErr})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "revision": runtime})
	case "foundation/recovery/preview":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		preview, err := host.PreviewLegacyRecovery(storepkg.NewStore(manifest.OutputDir))
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "legacy_recovery_blocked", "message": err.Error()}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "preview": preview})
	case "foundation/recovery/apply":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var request struct {
			PreviewID                string `json:"preview_id"`
			FoundationRevision       int64  `json:"foundation_revision"`
			FoundationAuditSignature string `json:"foundation_audit_signature"`
			ExplicitlyConfirmed      bool   `json:"explicitly_confirmed"`
		}
		if err := decodeFoundationJSONBody(r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "恢复请求格式无效："+err.Error())
			return
		}
		if !request.ExplicitlyConfirmed {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "explicit_confirmation_required", "message": "必须在查看恢复内容、来源、冲突和影响后显式确认"}})
			return
		}
		projectSession, _, err := s.sessions.Open(id)
		if err != nil {
			writeProjectSessionError(w, err)
			return
		}
		unlock, err := projectSession.beginActionKind("legacy_foundation_recovery")
		if err != nil {
			writeProjectLifecycleError(w, err)
			return
		}
		preview, err := host.ApplyLegacyRecovery(storepkg.NewStore(manifest.OutputDir), request.PreviewID, request.FoundationRevision, request.FoundationAuditSignature)
		unlock()
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "legacy_recovery_failed", "message": err.Error()}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "recovery": preview, "message": "旧项目设定已恢复并重新绑定，可以继续创作"})
	default:
		http.NotFound(w, r)
	}
}

func decodeFoundationJSONBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("request body must contain exactly one JSON object")
		}
		return err
	}
	return nil
}

func foundationSourceMutationAttempt(err error) bool {
	message := strings.ToLower(fmt.Sprint(err))
	for _, field := range []string{`"source"`, `"source_foundation"`, `"patch"`, `"mode"`} {
		if strings.Contains(message, field) {
			return true
		}
	}
	return strings.Contains(message, "exactly one json object")
}

func writeFoundationRevisionError(w http.ResponseWriter, err error) {
	var classified *host.FoundationRevisionError
	if !errors.As(err, &classified) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"code": "foundation_revision_error", "message": err.Error()}})
		return
	}
	status := http.StatusConflict
	switch classified.Code {
	case host.FoundationErrorInvalid, host.FoundationErrorSourceMutation:
		status = http.StatusBadRequest
	case host.FoundationErrorModeNotEnabled, host.FoundationErrorReadonly:
		status = http.StatusConflict
	case host.FoundationErrorRecovery:
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": classified.Code, "message": classified.Error()}})
}
