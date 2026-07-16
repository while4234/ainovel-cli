package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type manuscriptCommandRequest struct {
	Action           string `json:"action"`
	RevisionID       string `json:"revision_id"`
	ExpectedRevision int    `json:"expected_revision"`
	ExpectedAttempt  int    `json:"expected_attempt,omitempty"`
	IdempotencyKey   string `json:"idempotency_key"`
}

func (s *Server) handleManuscriptRoute(w http.ResponseWriter, r *http.Request, id, action string) {
	manifest, err := s.store.OpenProject(id)
	if err != nil {
		writeManuscriptError(w, fmt.Errorf("%w: %v", ErrProjectNotFound, err))
		return
	}
	st := storepkg.NewStore(manifest.OutputDir)
	service := host.NewManuscriptRevisionService(st)
	if action == "manuscript/revision/preview" || action == "manuscript/revision/command" || strings.HasPrefix(action, "manuscript/workspace/restore") {
		session, _, openErr := s.sessions.Open(id)
		if openErr != nil {
			writeManuscriptError(w, openErr)
			return
		}
		service = session.ManuscriptRevisionService()
		if service == nil {
			writeManuscriptError(w, fmt.Errorf("production manuscript writer and auditor are unavailable"))
			return
		}
	}
	if s.handleManuscriptWorkspaceRoute(w, r, manifest, st, service, action) {
		return
	}
	switch {
	case action == "manuscript/tree":
		s.handleManuscriptTree(w, r, manifest, st)
	case strings.HasPrefix(action, "manuscript/chapters/"):
		s.handleManuscriptChapter(w, r, manifest, service, strings.TrimPrefix(action, "manuscript/chapters/"))
	case action == "manuscript/revision/preview":
		s.handleManuscriptPreview(w, r, manifest, service)
	case action == "manuscript/revision/command":
		s.handleManuscriptCommand(w, r, manifest, service)
	case strings.HasPrefix(action, "manuscript/revisions/") && strings.HasSuffix(action, "/batches"):
		revisionID := strings.TrimSuffix(strings.TrimPrefix(action, "manuscript/revisions/"), "/batches")
		s.handleManuscriptBatches(w, r, manifest, st, revisionID)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleManuscriptTree(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, st *storepkg.Store) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	progress, err := st.Progress.Load()
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	if progress == nil || (progress.Phase != domain.PhaseWriting && progress.Phase != domain.PhaseComplete) {
		writeManuscriptError(w, fmt.Errorf("manuscript is only readable in writing or complete phase"))
		return
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	if len(volumes) == 0 {
		outline, loadErr := st.Outline.LoadOutline()
		if loadErr != nil {
			writeManuscriptError(w, loadErr)
			return
		}
		volumes = []domain.VolumeOutline{{ID: domain.LegacyStructureID(st.Dir(), domain.StructureKindVolume, "flat"), Index: 1, Title: "正文", Arcs: []domain.ArcOutline{{ID: domain.LegacyStructureID(st.Dir(), domain.StructureKindArc, "flat"), Index: 1, Title: "正文", Chapters: outline}}}}
	}
	active, err := st.ManuscriptRevisions.Active()
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "phase": progress.Phase, "tree": volumes, "active_revision": active})
}

func (s *Server) handleManuscriptChapter(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, service *host.ManuscriptRevisionService, stableID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	baseline, prose, err := service.CurrentChapter(strings.TrimSpace(stableID))
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "chapter": map[string]any{"stable_id": baseline.ChapterID, "display_chapter": baseline.DisplayChapter, "content": prose, "content_signature": baseline.CurrentProseSHA256, "baseline": baseline}})
}

func (s *Server) handleManuscriptPreview(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, service *host.ManuscriptRevisionService) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request struct {
		host.ManuscriptPreviewRequest
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid manuscript preview request")
		return
	}
	preview, err := service.PreviewContext(r.Context(), request.ManuscriptPreviewRequest, request.IdempotencyKey)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"project": manifest, "preview": preview, "awaiting_confirmation": true})
}

func (s *Server) handleManuscriptCommand(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, service *host.ManuscriptRevisionService) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request manuscriptCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid manuscript command request")
		return
	}
	if strings.TrimSpace(request.RevisionID) == "" || request.ExpectedRevision <= 0 || strings.TrimSpace(request.IdempotencyKey) == "" {
		writeError(w, http.StatusBadRequest, "revision_id, expected_revision, and idempotency_key are required")
		return
	}
	var runtime *domain.ManuscriptRevisionRuntime
	var err error
	switch strings.TrimSpace(request.Action) {
	case "confirm_impacts":
		runtime, err = service.ConfirmAdditionalImpacts(request.RevisionID, request.ExpectedRevision, request.IdempotencyKey)
	case "generate":
		runtime, err = service.GenerateCandidates(r.Context(), request.RevisionID, request.ExpectedRevision, request.ExpectedAttempt, request.IdempotencyKey)
	case "audit":
		runtime, err = service.RunAudit(r.Context(), request.RevisionID, request.ExpectedRevision, request.IdempotencyKey)
	case "approve":
		runtime, err = service.Approve(request.RevisionID, request.ExpectedRevision, request.IdempotencyKey)
	case "publish":
		runtime, err = service.Publish(request.RevisionID, request.ExpectedRevision, request.IdempotencyKey)
	case "revalidate_completion":
		runtime, err = service.RevalidateCompletion(request.RevisionID, request.ExpectedRevision, request.IdempotencyKey)
	case "cancel":
		runtime, err = service.Cancel(request.RevisionID, request.ExpectedRevision, request.IdempotencyKey)
	default:
		writeError(w, http.StatusBadRequest, "unsupported manuscript command")
		return
	}
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	if scope := manuscriptMutationScope(request.Action); scope != "" && runtime != nil {
		if session, _, openErr := s.sessions.Open(manifest.ID); openErr == nil {
			session.appendManuscriptMutation(scope, runtime.Baseline.ChapterID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "revision": runtime})
}

func manuscriptMutationScope(action string) string {
	switch strings.TrimSpace(action) {
	case "generate":
		return "generation"
	case "audit":
		return "audit"
	case "approve", "publish", "revalidate_completion":
		return "prose_publish"
	case "confirm_impacts":
		return "structure_publish"
	case "cancel":
		return "cancel"
	default:
		return ""
	}
}

func (s *Server) handleManuscriptBatches(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, st *storepkg.Store, revisionID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	runtime, err := st.ManuscriptRevisions.Load(strings.TrimSpace(revisionID))
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	candidateViews := make([]map[string]any, 0, len(runtime.Candidates))
	for _, candidate := range runtime.Candidates {
		prose, readErr := st.ManuscriptRevisions.Content().Read(candidate.Prose)
		if readErr != nil {
			writeManuscriptError(w, readErr)
			return
		}
		view := map[string]any{"candidate": candidate, "content": string(prose)}
		if candidate.AuditArtifact != nil {
			report, reportErr := st.ManuscriptRevisions.Content().Read(candidate.AuditArtifact.Report)
			findings, findingsErr := st.ManuscriptRevisions.Content().Read(candidate.AuditArtifact.Findings)
			if reportErr != nil || findingsErr != nil {
				writeManuscriptError(w, fmt.Errorf("read signed audit artifact: report=%v findings=%v", reportErr, findingsErr))
				return
			}
			var findingList []string
			if err := json.Unmarshal(findings, &findingList); err != nil {
				writeManuscriptError(w, err)
				return
			}
			view["audit"] = map[string]any{"signature": candidate.AuditArtifact.Signature, "report": string(report), "findings": findingList}
		}
		candidateViews = append(candidateViews, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "revision_id": runtime.RevisionID, "revision": runtime.Revision, "stage": runtime.Stage, "publication_status": runtime.PublicationStatus, "last_error_class": runtime.LastErrorClass, "recovery_class": manuscriptRecoveryClass(*runtime), "batches": runtime.Batches, "queue": runtime.Queue, "candidate_views": candidateViews})
}

func manuscriptRecoveryClass(runtime domain.ManuscriptRevisionRuntime) string {
	if runtime.PublicationStatus != domain.ManuscriptPublicationNone && runtime.PublicationStatus != domain.ManuscriptPublicationCompleted {
		return "publication_recovery_required"
	}
	if runtime.Stage == "failed" {
		if strings.TrimSpace(runtime.LastErrorClass) != "" {
			return runtime.LastErrorClass
		}
		return "revision_failed"
	}
	return ""
}

func writeManuscriptError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	code := "manuscript_revision_error"
	message := err.Error()
	switch {
	case errors.Is(err, ErrProjectNotFound), errors.Is(err, storepkg.ErrManuscriptRevisionNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, storepkg.ErrManuscriptRevisionConflict):
		code = "revision_conflict"
	case errors.Is(err, storepkg.ErrManuscriptIdempotencyConflict):
		code = "idempotency_conflict"
	case errors.Is(err, storepkg.ErrRevisionCommandInProgress):
		code = "active_revision"
	case strings.Contains(err.Error(), "stale baseline") || strings.Contains(err.Error(), "signature drift"):
		code = "preview_stale"
	case strings.Contains(err.Error(), "publication recovery"):
		code = "publication_recovery_required"
	case strings.Contains(err.Error(), "human confirmation required"):
		code = "human_confirmation_required"
	case strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "invalid"):
		status, code = http.StatusBadRequest, "invalid_request"
	}
	var revisionErr *domain.ManuscriptRevisionError
	if errors.As(err, &revisionErr) && strings.TrimSpace(revisionErr.Class) != "" {
		code = revisionErr.Class
		message = "manuscript operation failed: " + code
		if code == "invalid_json" || code == "invalid_schema" || code == "missing_segment" || code == "empty_response" || code == "truncated_response" {
			status = http.StatusUnprocessableEntity
		}
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func stableChapterIDForNumber(st *storepkg.Store, chapter int) (string, error) {
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return "", err
	}
	entries := domain.FlattenOutline(volumes)
	if len(entries) == 0 {
		entries, err = st.Outline.LoadOutline()
		if err != nil {
			return "", err
		}
	}
	for _, entry := range entries {
		if entry.Chapter == chapter {
			if strings.TrimSpace(entry.ID) == "" {
				return domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, fmt.Sprintf("chapters/%04d", chapter)), nil
			}
			return entry.ID, nil
		}
	}
	return "", fmt.Errorf("chapter %d is not in the current display projection", chapter)
}

func parseExpectedRevision(value string) int {
	revision, _ := strconv.Atoi(strings.TrimSpace(value))
	return revision
}
