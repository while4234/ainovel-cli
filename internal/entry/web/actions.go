package web

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/diag"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/exp"
	"github.com/voocel/ainovel-cli/internal/store"
)

type apiExportResult struct {
	Path     string `json:"path"`
	Chapters int    `json:"chapters"`
	Bytes    int    `json:"bytes"`
	Skipped  []int  `json:"skipped"`
}

func (s *Server) handleProjectStart(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := decodeProjectStartRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	if snapshotHasExistingBook(session.Snapshot()) {
		writeError(w, http.StatusConflict, "project already has writing state; use continue/resume or create a new project")
		return
	}
	if err := session.StartQuick(req.Text, req.TargetTotalWords); err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, projectActionResponse{
		Project:  manifest,
		Snapshot: snapshot,
		Running:  snapshot.IsRunning,
	})
}

type projectStartRequest struct {
	Text             string `json:"text"`
	TargetTotalWords int    `json:"target_total_words"`
}

func decodeProjectStartRequest(r *http.Request) (projectStartRequest, error) {
	var req projectStartRequest
	if err := decodeJSONBody(r, &req); err != nil {
		return req, fmt.Errorf("invalid request body: %w", err)
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		return req, fmt.Errorf("text is required")
	}
	if req.TargetTotalWords < 0 {
		return req, fmt.Errorf("target_total_words must be a non-negative integer")
	}
	return req, nil
}

func (s *Server) handleProjectPause(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	stopped := session.Pause()
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"snapshot": snapshot,
		"stopped":  stopped,
		"running":  snapshot.IsRunning,
	})
}

func (s *Server) handleProjectExport(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Path      string `json:"path"`
		Format    string `json:"format"`
		From      int    `json:"from"`
		To        int    `json:"to"`
		Overwrite bool   `json:"overwrite"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid export request: "+err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	outPath, err := projectExportPath(manifest, req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	format, err := exportFormat(req.Format)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result, err := session.Export(ctx, exp.Options{
		Format:    format,
		OutPath:   outPath,
		From:      req.From,
		To:        req.To,
		Overwrite: req.Overwrite,
	})
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"snapshot": snapshot,
		"export":   apiExportResultFromExp(result),
		"running":  snapshot.IsRunning,
	})
}

func (s *Server) handleProjectDiag(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	manifest, err := s.store.OpenProject(id)
	if err != nil {
		writeProjectSessionError(w, fmt.Errorf("%w: %v", ErrProjectNotFound, err))
		return
	}
	st := store.NewStore(manifest.OutputDir)
	report, capture := diag.Diagnose(st)
	exportPath, _ := diag.WriteExport(st, report, capture)
	writeJSON(w, http.StatusOK, map[string]any{
		"project":     manifest,
		"report":      report,
		"runtime":     capture,
		"export_path": exportPath,
	})
}

func snapshotHasExistingBook(snapshot host.UISnapshot) bool {
	return strings.TrimSpace(snapshot.NovelName) != "" ||
		strings.TrimSpace(snapshot.Phase) != "" ||
		snapshot.TotalChapters > 0 ||
		snapshot.CompletedCount > 0 ||
		snapshot.TotalWordCount > 0
}

func exportFormat(value string) (exp.Format, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case string(exp.FormatTXT):
		return exp.FormatTXT, nil
	case string(exp.FormatEPUB):
		return exp.FormatEPUB, nil
	default:
		return "", fmt.Errorf("export format must be txt or epub")
	}
}

func projectExportPath(manifest ProjectManifest, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if filepath.IsAbs(raw) || isWindowsAbsolutePath(raw) {
		return "", fmt.Errorf("export path must be relative to the project exports directory")
	}
	clean := filepath.Clean(filepath.FromSlash(raw))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("export path must stay inside the project exports directory")
	}
	if strings.HasSuffix(raw, "/") || strings.HasSuffix(raw, `\`) {
		return "", fmt.Errorf("export path must include a file name")
	}
	if strings.Trim(filepath.Base(clean), ". ") == "" {
		return "", fmt.Errorf("export path must include a valid file name")
	}
	base := filepath.Join(manifest.RootDir, "exports")
	target := filepath.Join(base, clean)
	if !isSameOrChild(base, target) || filepath.Clean(target) == filepath.Clean(base) {
		return "", fmt.Errorf("export path must stay inside the project exports directory")
	}
	return target, nil
}

func apiExportResultFromExp(result *exp.Result) apiExportResult {
	if result == nil {
		return apiExportResult{}
	}
	return apiExportResult{
		Path:     result.Path,
		Chapters: result.Chapters,
		Bytes:    result.Bytes,
		Skipped:  append([]int(nil), result.Skipped...),
	}
}

func parseNonNegativeFormInt(raw, name string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return n, nil
}
