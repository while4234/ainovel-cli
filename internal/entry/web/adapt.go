package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
)

type apiAdaptationEvent struct {
	Time    time.Time `json:"time"`
	Stage   string    `json:"stage"`
	Current int       `json:"current"`
	Total   int       `json:"total"`
	Message string    `json:"message"`
	Error   string    `json:"error,omitempty"`
}

type adaptationRunError struct {
	message string
}

func (e adaptationRunError) Error() string {
	return e.message
}

func (s *Server) handleProjectAdaptSource(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	manifest, err := s.store.OpenProject(id)
	if err != nil {
		writeProjectSessionError(w, fmt.Errorf("%w: %v", ErrProjectNotFound, err))
		return
	}
	headers, cleanup, err := parseMultipartFiles(w, r)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(headers) != 1 {
		writeError(w, http.StatusBadRequest, "exactly one source novel file is required")
		return
	}

	sourceDir := projectAdaptationUploadDir(manifest)
	uploads, err := readPendingUploads(headers, textUploadExtensions, maxTextUploadBytes, sourceDir)
	if err != nil {
		writeUploadValidationError(w, err)
		return
	}
	if err := writePendingUploads(uploads, sourceDir); err != nil {
		writeUploadValidationError(w, err)
		return
	}
	sourceFile := uploads[0].apiUploadedFile
	writeJSON(w, http.StatusOK, map[string]any{
		"project":     manifest,
		"source_file": sourceFile,
		"files":       []apiUploadedFile{sourceFile},
		"message":     "uploaded adaptation source file",
	})
}

func (s *Server) handleProjectAdaptAnalyze(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	sourcePath, err := adaptationSourcePathFromRequest(r, manifest)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	events, err := session.PrepareAdaptationSource(r.Context(), sourcePath)
	if err != nil {
		writeAdaptationActionError(w, err, events)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"snapshot": session.Snapshot(),
		"events":   events,
		"running":  session.Snapshot().IsRunning,
	})
}

func (s *Server) handleProjectAdaptStart(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Mode       string `json:"mode"`
		Brief      string `json:"brief"`
		SourceFile string `json:"source_file"`
	}
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid adaptation start request: "+err.Error())
			return
		}
	}
	mode := strings.TrimSpace(req.Mode)
	rewritePolicy, err := adaptationRewritePolicyForMode(mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	brief := strings.TrimSpace(req.Brief)
	if brief == "" {
		writeError(w, http.StatusBadRequest, "adaptation brief is required")
		return
	}

	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	sourcePath, err := adaptationSourcePathFromName(req.SourceFile, manifest, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := session.StartAdaptationPrepared(adapt.ProposalOptions{
		Brief:         brief,
		SourcePath:    sourcePath,
		Granularity:   mode,
		RewritePolicy: rewritePolicy,
		WordTolerance: adapt.DefaultWordTolerance,
	}); err != nil {
		writeAdaptationStartError(w, err)
		return
	}
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":        manifest,
		"snapshot":       snapshot,
		"mode":           mode,
		"rewrite_policy": rewritePolicy,
		"running":        snapshot.IsRunning,
	})
}

func (s *Server) handleProjectAdaptProposal(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	options, mode, rewritePolicy, err := decodeAdaptationProposalRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	sourcePath, err := adaptationSourcePathFromName(options.SourcePath, manifest, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	options.SourcePath = sourcePath
	proposal, err := session.BuildAdaptationProposal(options)
	if err != nil {
		writeAdaptationStartError(w, err)
		return
	}
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":        manifest,
		"snapshot":       snapshot,
		"proposal":       proposal,
		"mode":           mode,
		"rewrite_policy": rewritePolicy,
		"running":        snapshot.IsRunning,
	})
}

func (s *Server) handleProjectAdaptConfirm(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	plan, err := session.ConfirmAdaptationProposal()
	if err != nil {
		writeAdaptationStartError(w, err)
		return
	}
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"snapshot": snapshot,
		"plan":     plan,
		"running":  snapshot.IsRunning,
	})
}

func decodeAdaptationProposalRequest(r *http.Request) (adapt.ProposalOptions, string, string, error) {
	var req struct {
		Mode       string `json:"mode"`
		Brief      string `json:"brief"`
		SourceFile string `json:"source_file"`
	}
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			return adapt.ProposalOptions{}, "", "", fmt.Errorf("invalid adaptation proposal request: %w", err)
		}
	}
	mode := strings.TrimSpace(req.Mode)
	rewritePolicy, err := adaptationRewritePolicyForMode(mode)
	if err != nil {
		return adapt.ProposalOptions{}, "", "", err
	}
	brief := strings.TrimSpace(req.Brief)
	if brief == "" {
		return adapt.ProposalOptions{}, "", "", fmt.Errorf("adaptation brief is required")
	}
	return adapt.ProposalOptions{
		Brief:         brief,
		SourcePath:    strings.TrimSpace(req.SourceFile),
		Granularity:   mode,
		RewritePolicy: rewritePolicy,
		WordTolerance: adapt.DefaultWordTolerance,
	}, mode, rewritePolicy, nil
}

func adaptationSourcePathFromRequest(r *http.Request, manifest ProjectManifest) (string, error) {
	var req struct {
		SourceFile string `json:"source_file"`
	}
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("invalid adaptation analyze request: %w", err)
		}
	}
	sourceDir := projectAdaptationUploadDir(manifest)
	if strings.TrimSpace(req.SourceFile) == "" {
		return onlyAdaptationSourcePath(sourceDir)
	}
	return adaptationSourcePathFromName(req.SourceFile, manifest, true)
}

func adaptationSourcePathFromName(sourceFile string, manifest ProjectManifest, allowInfer bool) (string, error) {
	sourceDir := projectAdaptationUploadDir(manifest)
	if strings.TrimSpace(sourceFile) == "" {
		if !allowInfer {
			return "", fmt.Errorf("source_file is required")
		}
		return onlyAdaptationSourcePath(sourceDir)
	}
	name, err := sanitizeUploadedFilename(sourceFile, textUploadExtensions)
	if err != nil {
		return "", err
	}
	sourcePath, err := safeUploadTarget(sourceDir, name)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("adaptation source file %q was not uploaded", name)
		}
		return "", fmt.Errorf("stat adaptation source file %q: %w", name, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("adaptation source file %q is a directory", name)
	}
	return sourcePath, nil
}

func onlyAdaptationSourcePath(sourceDir string) (string, error) {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("upload an adaptation source file before analysis")
		}
		return "", fmt.Errorf("list adaptation source files: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := textUploadExtensions[strings.ToLower(filepath.Ext(entry.Name()))]; ok {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("upload an adaptation source file before analysis")
	}
	if len(names) > 1 {
		return "", fmt.Errorf("source_file is required when multiple adaptation source files exist")
	}
	return safeUploadTarget(sourceDir, names[0])
}

func apiAdaptationEventFromAdapt(ev adapt.Event) apiAdaptationEvent {
	api := apiAdaptationEvent{
		Time:    ev.Time,
		Stage:   string(ev.Stage),
		Current: ev.Current,
		Total:   ev.Total,
		Message: ev.Message,
	}
	if api.Time.IsZero() {
		api.Time = time.Now().UTC()
	}
	if ev.Err != nil {
		api.Error = ev.Err.Error()
	}
	if api.Message == "" && api.Error != "" {
		api.Message = api.Error
	}
	return api
}

func projectAdaptationUploadDir(manifest ProjectManifest) string {
	return filepath.Join(manifest.RootDir, "uploads", "adaptation")
}

func adaptationRewritePolicyForMode(mode string) (string, error) {
	switch mode {
	case domain.AdaptationGranularityChapter:
		return domain.AdaptationRewritePreserveDetails, nil
	case domain.AdaptationGranularityArc, domain.AdaptationGranularityFree:
		return domain.AdaptationRewriteFullRewrite, nil
	default:
		return "", fmt.Errorf("adaptation mode must be one of chapter, arc, free")
	}
}

func writeAdaptationActionError(w http.ResponseWriter, err error, events []apiAdaptationEvent) {
	status := http.StatusInternalServerError
	var runErr adaptationRunError
	switch {
	case errors.Is(err, ErrSessionActionInProgress):
		status = http.StatusConflict
	case errors.As(err, &runErr):
		status = http.StatusBadRequest
	default:
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{
		"error":  err.Error(),
		"events": events,
	})
}

func writeAdaptationStartError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrSessionActionInProgress) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeError(w, http.StatusConflict, err.Error())
}
