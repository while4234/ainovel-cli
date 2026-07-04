package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/host"
)

type apiChapterRevision struct {
	Chapter         int    `json:"chapter"`
	Instruction     string `json:"instruction"`
	Mode            string `json:"mode"`
	Label           string `json:"label,omitempty"`
	PendingRewrites []int  `json:"pending_rewrites,omitempty"`
	StaleNotice     string `json:"stale_notice,omitempty"`
}

func (s *Server) handleProjectChapterRevise(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := decodeChapterRevisionRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	result, err := session.ReviseChapter(req)
	if err != nil {
		writeChapterRevisionError(w, err)
		return
	}
	snapshot := session.Snapshot()
	revision := apiChapterRevisionFromHost(result)
	writeJSON(w, http.StatusOK, projectActionResponse{
		Project:  manifest,
		Snapshot: snapshot,
		Running:  snapshot.IsRunning,
		Revision: &revision,
	})
}

func decodeChapterRevisionRequest(r *http.Request) (host.ChapterRevisionRequest, error) {
	var req struct {
		Chapter     int    `json:"chapter"`
		Instruction string `json:"instruction"`
		Mode        string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return host.ChapterRevisionRequest{}, fmt.Errorf("invalid chapter revision request: %w", err)
	}
	out := host.ChapterRevisionRequest{
		Chapter:     req.Chapter,
		Instruction: strings.TrimSpace(req.Instruction),
		Mode:        strings.TrimSpace(req.Mode),
	}
	if out.Chapter <= 0 {
		return out, fmt.Errorf("chapter must be > 0")
	}
	if out.Instruction == "" {
		return out, fmt.Errorf("instruction is required")
	}
	if out.Mode != "" && out.Mode != host.ChapterRevisionModeRewrite && out.Mode != host.ChapterRevisionModePolish {
		return out, fmt.Errorf("mode must be %q or %q", host.ChapterRevisionModeRewrite, host.ChapterRevisionModePolish)
	}
	return out, nil
}

func apiChapterRevisionFromHost(result host.ChapterRevisionResult) apiChapterRevision {
	return apiChapterRevision{
		Chapter:         result.Chapter,
		Instruction:     result.Instruction,
		Mode:            result.Mode,
		Label:           result.Label,
		PendingRewrites: append([]int(nil), result.PendingRewrites...),
		StaleNotice:     result.StaleNotice,
	}
}

func writeChapterRevisionError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	if errors.Is(err, ErrSessionActionInProgress) {
		status = http.StatusConflict
	} else if errors.Is(err, errs.ErrToolArgs) {
		status = http.StatusBadRequest
	} else if errors.Is(err, errs.ErrToolPrecondition) || errors.Is(err, errs.ErrToolConflict) {
		status = http.StatusConflict
	}
	writeError(w, status, err.Error())
}
