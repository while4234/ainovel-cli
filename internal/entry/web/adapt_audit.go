package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func (s *Server) handleProjectAdaptAudit(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		manifest, err := s.store.OpenProject(id)
		if err != nil {
			writeProjectSessionError(w, err)
			return
		}
		report, err := storepkg.NewStore(manifest.OutputDir).Adaptation.LoadAuditReport()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"report": report})
	case http.MethodPost:
		var options adapt.AuditOptions
		if r.Body != nil {
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&options); err != nil && !errors.Is(err, io.EOF) {
				writeError(w, http.StatusBadRequest, "invalid adaptation audit request: "+err.Error())
				return
			}
		}
		session, manifest, err := s.sessions.Open(id)
		if err != nil {
			writeProjectSessionError(w, err)
			return
		}
		if session.Snapshot().IsRunning {
			writeError(w, http.StatusConflict, "pause the project before running an adaptation audit")
			return
		}
		unlock, err := session.beginAction()
		if err != nil {
			writeProjectSessionError(w, err)
			return
		}
		defer unlock()
		report, err := adapt.AuditProject(storepkg.NewStore(manifest.OutputDir), options)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"report":   report,
			"snapshot": session.Snapshot(),
			"applied":  false,
		})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleProjectAdaptAuditApply(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request adaptaudit.ConfirmationRequest
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid adaptation audit confirmation: "+err.Error())
			return
		}
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	if session.Snapshot().IsRunning {
		writeError(w, http.StatusConflict, "pause the project before applying an adaptation repair")
		return
	}
	unlock, err := session.beginAction()
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	defer unlock()
	application, err := adapt.ApplyProjectAuditRepair(storepkg.NewStore(manifest.OutputDir), request)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"application": application,
		"snapshot":    session.Snapshot(),
		"applied":     true,
		"running":     false,
	})
}
