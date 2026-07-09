package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

type Options struct {
	Host           string
	Port           int
	RuntimeRoot    string
	Open           bool
	RepositoryRoot string
	Stdout         io.Writer
	Stderr         io.Writer
}

type Server struct {
	cfgMu       sync.RWMutex
	cfg         bootstrap.Config
	bundle      assets.Bundle
	runtimeRoot string
	store       *ProjectStore
	sessions    *SessionManager
	libraries   *LibraryService
	static      fs.FS
}

func Run(ctx context.Context, cfg bootstrap.Config, bundle assets.Bundle, opts Options) error {
	host := strings.TrimSpace(opts.Host)
	if host == "" {
		host = DefaultHost
	}
	port := opts.Port
	if port == 0 {
		port = DefaultPort
	}
	if port < 0 || port > 65535 {
		return fmt.Errorf("port must be in 1-65535")
	}
	repoRoot := opts.RepositoryRoot
	if repoRoot == "" {
		repoRoot = FindRepositoryRoot("")
	}
	if repoRoot == "" {
		if exe, err := os.Executable(); err == nil {
			repoRoot = FindRepositoryRoot(filepath.Dir(exe))
		}
	}
	runtimeRoot, source, err := ResolveRuntimeRoot(opts.RuntimeRoot, cfg, repoRoot)
	if err != nil {
		return err
	}
	if err := EnsureRuntimeRoot(runtimeRoot); err != nil {
		return err
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	defer listener.Close()

	app := NewServer(cfg, bundle, runtimeRoot)
	defer app.Close()

	server := &http.Server{
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	url := "http://" + listener.Addr().String()
	if opts.Stdout != nil {
		fmt.Fprintf(opts.Stdout, "ainovel web listening on %s\nruntime root: %s (%s)\n", url, runtimeRoot, source)
	}
	if opts.Open {
		if err := openBrowser(url); err != nil && opts.Stderr != nil {
			fmt.Fprintf(opts.Stderr, "open browser: %v\n", err)
		}
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return ctx.Err()
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func NewServer(cfg bootstrap.Config, bundle assets.Bundle, runtimeRoot string) *Server {
	store := NewProjectStore(runtimeRoot)
	s := &Server{
		cfg:         cfg,
		bundle:      bundle,
		runtimeRoot: runtimeRoot,
		store:       store,
		libraries:   NewLibraryService(runtimeRoot),
		static:      StaticFS(),
	}
	s.sessions = NewSessionManager(cfg, bundle, store)
	return s
}

func NewHandler(cfg bootstrap.Config, bundle assets.Bundle, runtimeRoot string) http.Handler {
	return NewServer(cfg, bundle, runtimeRoot).Handler()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/runtime", s.handleRuntime)
	mux.HandleFunc("/api/styles", s.handleStyles)
	mux.HandleFunc("/api/libraries/simulation", s.handleSimulationLibrary)
	mux.HandleFunc("/api/libraries/simulation/upload", s.handleSimulationLibraryUpload)
	mux.HandleFunc("/api/libraries/novels", s.handleNovelLibrary)
	mux.HandleFunc("/api/models", s.handleModels)
	mux.HandleFunc("/api/models/default", s.handleDefaultModel)
	mux.HandleFunc("/api/models/switch", s.handleModelSwitch)
	mux.HandleFunc("/api/models/cocreate-timeout", s.handleCoCreateTimeout)
	mux.HandleFunc("/api/models/cocreate-max-tokens", s.handleCoCreateMaxTokens)
	mux.HandleFunc("/api/models/retry-settings", s.handleRetrySettings)
	mux.HandleFunc("/api/models/add", s.handleModelAdd)
	mux.HandleFunc("/api/models/test", s.handleModelTest)
	mux.HandleFunc("/api/models/discover", s.handleModelDiscover)
	mux.HandleFunc("/api/models/codex-auth/status", s.handleCodexAuthStatus)
	mux.HandleFunc("/api/models/grok-login/", s.handleGrokLogin)
	mux.HandleFunc("/api/projects/trash", s.handleProjectTrash)
	mux.HandleFunc("/api/projects", s.handleProjects)
	mux.HandleFunc("/api/projects/", s.handleProject)
	mux.HandleFunc("/api/trash/projects", s.handleTrashProjects)
	mux.HandleFunc("/api/trash/projects/", s.handleTrashProject)
	return mux
}

func (s *Server) Close() {
	if s.sessions != nil {
		s.sessions.CloseAll()
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if strings.Contains(path, "..") {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(s.static, path)
	if err != nil {
		data, err = fs.ReadFile(s.static, "index.html")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "web UI assets are not embedded; run npm --prefix web run build")
			return
		}
		path = "index.html"
	}
	if ctype := mime.TypeByExtension(filepath.Ext(path)); ctype != "" {
		w.Header().Set("content-type", ctype)
	} else if path == "index.html" {
		w.Header().Set("content-type", "text/html; charset=utf-8")
	}
	setStaticCacheHeaders(w, path)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func setStaticCacheHeaders(w http.ResponseWriter, path string) {
	if path == "index.html" {
		w.Header().Set("cache-control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("pragma", "no-cache")
		w.Header().Set("expires", "0")
		return
	}
	if strings.HasPrefix(path, "assets/") {
		w.Header().Set("cache-control", "public, max-age=31536000, immutable")
	}
}

func (s *Server) handleRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg := s.currentConfig()
	writeJSON(w, http.StatusOK, s.runtimePayload(cfg))
}

func (s *Server) currentConfig() bootstrap.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return cloneWebConfig(s.cfg)
}

func (s *Server) setCurrentConfig(cfg bootstrap.Config) {
	cfg = cloneWebConfig(cfg)
	s.cfgMu.Lock()
	s.cfg = cfg
	s.cfgMu.Unlock()
	if s.sessions != nil {
		s.sessions.SetConfig(cfg)
	}
}

func (s *Server) runtimePayload(cfg bootstrap.Config) map[string]any {
	return map[string]any{
		"runtime_root": s.runtimeRoot,
		"projects_dir": s.store.ProjectsDir(),
		"config": map[string]any{
			"provider":                      cfg.Provider,
			"model":                         cfg.ModelName,
			"style":                         cfg.Style,
			"proxy":                         cfg.Proxy,
			"reasoning_effort":              cfg.ReasoningEffort,
			"cocreate_timeout_seconds":      cfg.EffectiveCoCreateTimeoutSeconds(),
			"cocreate_max_tokens":           cfg.EffectiveCoCreateMaxTokens(),
			"model_call_max_attempts":       cfg.ModelAutoSwitch.EffectiveNetworkMaxAttempts(),
			"structure_repair_max_attempts": cfg.EffectiveStructureRepairMaxAttempts(),
			"budget_quality_max_attempts":   cfg.EffectiveBudgetQualityMaxAttempts(),
			"simulation_mode":               cfg.EffectiveSimulationMode(),
			"roles":                         cfg.Roles,
		},
		"active_projects": s.sessions.ActiveProjectIDs(),
	}
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		projects, err := s.store.ListProjects()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
	case http.MethodPost:
		var req struct {
			Name  string `json:"name"`
			Style string `json:"style"`
		}
		if r.Body != nil {
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
				writeError(w, http.StatusBadRequest, "invalid project request: "+err.Error())
				return
			}
		}
		style, err := s.resolveStyleID(req.Style)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		manifest, err := s.store.CreateProjectWithStyle(req.Name, style)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, manifest)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleTrashProjects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		projects, err := s.store.ListTrashProjects()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
	case http.MethodDelete:
		removed, err := s.store.EmptyTrashProjects()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleTrashProject(w http.ResponseWriter, r *http.Request) {
	id, action, ok := splitTrashProjectRoute(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if action != "restore" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	manifest, err := s.store.RestoreTrashProject(id)
	if err != nil {
		writeProjectManifestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": manifest})
}

func splitTrashProjectRoute(path string) (string, string, bool) {
	rest := strings.TrimPrefix(path, "/api/trash/projects/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return "", "", false
	}
	if len(parts) == 1 {
		return parts[0], "", true
	}
	return parts[0], strings.Join(parts[1:], "/"), true
}

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	id, action, ok := splitProjectRoute(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "":
		s.handleProjectResource(w, r, id)
	case "open":
		s.handleProjectOpen(w, r, id)
	case "snapshot":
		s.handleProjectSnapshot(w, r, id)
	case "resume":
		s.handleProjectResume(w, r, id)
	case "start":
		s.handleProjectStart(w, r, id)
	case "pause":
		s.handleProjectPause(w, r, id)
	case "rollback/preview":
		s.handleProjectRollbackPreview(w, r, id)
	case "rollback":
		s.handleProjectRollback(w, r, id)
	case "continue":
		s.handleProjectContinue(w, r, id)
	case "chapters/revise":
		s.handleProjectChapterRevise(w, r, id)
	case "outline/chapters/revise":
		s.handleProjectChapterOutlineRevise(w, r, id)
	case "steer":
		s.handleProjectSteer(w, r, id)
	case "events/history":
		s.handleProjectEventsHistory(w, r, id)
	case "events":
		s.handleProjectEvents(w, r, id)
	case "models":
		s.handleProjectModels(w, r, id)
	case "models/switch":
		s.handleProjectModelSwitch(w, r, id)
	case "models/thinking":
		s.handleProjectModelThinking(w, r, id)
	case "models/cocreate-timeout":
		s.handleProjectCoCreateTimeout(w, r, id)
	case "models/cocreate-max-tokens":
		s.handleProjectCoCreateMaxTokens(w, r, id)
	case "models/retry-settings":
		s.handleProjectRetrySettings(w, r, id)
	case "models/add":
		s.handleProjectModelAdd(w, r, id)
	case "models/test":
		s.handleProjectModelTest(w, r, id)
	case "models/discover":
		s.handleProjectModelDiscover(w, r, id)
	case "models/add-openai-compatible":
		s.handleProjectModelAddOpenAICompatible(w, r, id)
	case "style":
		s.handleProjectStyle(w, r, id)
	case "simulation-mode":
		s.handleProjectSimulationMode(w, r, id)
	case "models/grok-login/start":
		s.handleProjectGrokLoginStart(w, r, id)
	case "models/grok-login/poll":
		s.handleProjectGrokLoginPoll(w, r, id)
	case "models/grok-login/complete":
		s.handleProjectGrokLoginComplete(w, r, id)
	case "models/grok-login/status":
		s.handleProjectGrokLoginStatus(w, r, id)
	case "models/codex-auth/status":
		s.handleProjectCodexAuthStatus(w, r, id)
	case "usage":
		s.handleProjectUsage(w, r, id)
	case "backend/status":
		s.handleProjectBackendStatus(w, r, id)
	case "backend/test":
		s.handleProjectBackendTest(w, r, id)
	case "import":
		s.handleProjectImport(w, r, id)
	case "export":
		s.handleProjectExport(w, r, id)
	case "export/download":
		s.handleProjectExportDownload(w, r, id)
	case "diag":
		s.handleProjectDiag(w, r, id)
	case "simulate/files":
		s.handleProjectSimulateFiles(w, r, id)
	case "simulate/analyze":
		s.handleProjectSimulateAnalyze(w, r, id)
	case "simulate/import":
		s.handleProjectSimulateImport(w, r, id)
	case "simulate/library/save":
		s.handleProjectSimulationLibrarySave(w, r, id)
	case "simulate/library/load":
		s.handleProjectSimulationLibraryLoad(w, r, id)
	case "adapt/source":
		s.handleProjectAdaptSource(w, r, id)
	case "adapt/analyze":
		s.handleProjectAdaptAnalyze(w, r, id)
	case "adapt/proposal":
		s.handleProjectAdaptProposal(w, r, id)
	case "adapt/proposal/volumes":
		s.handleProjectAdaptProposalVolumes(w, r, id)
	case "adapt/proposal/volumes/revise":
		s.handleProjectAdaptProposalVolumesRevise(w, r, id)
	case "adapt/proposal/details":
		s.handleProjectAdaptProposalDetails(w, r, id)
	case "adapt/proposal/revise":
		s.handleProjectAdaptProposalRevise(w, r, id)
	case "adapt/confirm":
		s.handleProjectAdaptConfirm(w, r, id)
	case "adapt/start":
		s.handleProjectAdaptStart(w, r, id)
	case "adapt/library/save":
		s.handleProjectNovelLibrarySave(w, r, id)
	case "adapt/library/load":
		s.handleProjectNovelLibraryLoad(w, r, id)
	case "cocreate/begin":
		s.handleProjectCoCreateBegin(w, r, id)
	case "cocreate/send":
		s.handleProjectCoCreateSend(w, r, id)
	case "cocreate/revise":
		s.handleProjectCoCreateRevise(w, r, id)
	case "cocreate/decision":
		s.handleProjectCoCreateDecision(w, r, id)
	case "cocreate/resume":
		s.handleProjectCoCreateResume(w, r, id)
	case "cocreate/commit":
		s.handleProjectCoCreateCommit(w, r, id)
	case "cocreate/planning/revise":
		s.handleProjectCoCreatePlanningRevise(w, r, id)
	case "cocreate/confirm":
		s.handleProjectCoCreateConfirm(w, r, id)
	case "cocreate/cancel":
		s.handleProjectCoCreateCancel(w, r, id)
	default:
		if strings.HasPrefix(action, "chapters/") {
			s.handleProjectChapter(w, r, id, strings.TrimPrefix(action, "chapters/"))
			return
		}
		http.NotFound(w, r)
	}
}

func splitProjectRoute(path string) (string, string, bool) {
	rest := strings.TrimPrefix(path, "/api/projects/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return "", "", false
	}
	if len(parts) == 1 {
		return parts[0], "", true
	}
	return parts[0], strings.Join(parts[1:], "/"), true
}

func (s *Server) handleProjectResource(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodPatch:
		var req struct {
			Name string `json:"name"`
		}
		if err := decodeJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid project request: "+err.Error())
			return
		}
		manifest, err := s.store.RenameProject(id, req.Name)
		if err != nil {
			writeProjectManifestError(w, err)
			return
		}
		if session := s.sessions.Project(id); session != nil {
			session.SetManifest(manifest)
		}
		writeJSON(w, http.StatusOK, manifest)
	case http.MethodDelete:
		s.sessions.CloseProject(id)
		manifest, target, err := s.store.TrashProject(id)
		if err != nil {
			writeProjectManifestError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"project":    manifest,
			"trash_path": target,
		})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleProjectTrash(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		projects, err := s.store.ListTrashProjects()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"projects":  projects,
			"trash_dir": s.store.ProjectTrashDir(),
		})
	case http.MethodDelete:
		count, err := s.store.EmptyTrashProjects()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"deleted_count": count,
			"removed":       count,
		})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleProjectOpen(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	response, err := buildProjectSnapshotResponse(session, manifest)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleProjectSnapshot(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	response, err := buildProjectSnapshotResponse(session, manifest)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleProjectResume(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	label, err := session.Resume()
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, projectActionResponse{
		Project:  manifest,
		Snapshot: snapshot,
		Label:    label,
		Running:  snapshot.IsRunning,
	})
}

func (s *Server) handleProjectContinue(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	text, err := decodeTextRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	if err := session.Continue(text); err != nil {
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

func (s *Server) handleProjectSteer(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	text, err := decodeTextRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	if err := session.Steer(text); err != nil {
		writeProjectSteerError(w, err)
		return
	}
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, projectActionResponse{
		Project:  manifest,
		Snapshot: snapshot,
		Running:  snapshot.IsRunning,
	})
}

func (s *Server) handleProjectEvents(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	after, err := parseAfter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, _, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	if err := session.ServeEvents(r.Context(), w, after); err != nil {
		slog.Warn("serve web events failed", "module", "web", "project", id, "err", err)
	}
}

func (s *Server) handleProjectEventsHistory(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	after, err := parseAfter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, _, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session.EventHistory(after))
}

type projectSnapshotResponse struct {
	Project    ProjectManifest     `json:"project"`
	Snapshot   any                 `json:"snapshot"`
	Adaptation apiAdaptationStatus `json:"adaptation"`
	Simulation apiSimulationStatus `json:"simulation"`
	CoCreate   *webCoCreateState   `json:"cocreate,omitempty"`
}

func buildProjectSnapshotResponse(session *ProjectSession, manifest ProjectManifest) (projectSnapshotResponse, error) {
	adaptation, err := projectAdaptationStatus(manifest, session.isActionRunning(projectActionKindAdaptationAnalysis))
	if err != nil {
		return projectSnapshotResponse{}, err
	}
	simulation, err := projectSimulationStatus(
		manifest,
		session.isActionRunning(projectActionKindSimulationAnalysis),
		session.isActionRunning(projectActionKindSimulationImport),
	)
	if err != nil {
		return projectSnapshotResponse{}, err
	}
	return projectSnapshotResponse{
		Project:    manifest,
		Snapshot:   session.Snapshot(),
		Adaptation: adaptation,
		Simulation: simulation,
		CoCreate:   session.CoCreateState(),
	}, nil
}

type projectActionResponse struct {
	Project  ProjectManifest     `json:"project"`
	Snapshot any                 `json:"snapshot"`
	Label    string              `json:"label,omitempty"`
	Running  bool                `json:"running"`
	Revision *apiChapterRevision `json:"revision,omitempty"`
	Export   *apiExportResult    `json:"export,omitempty"`
}

type projectChapterOutlineRevisionResponse struct {
	Project  ProjectManifest           `json:"project"`
	Snapshot any                       `json:"snapshot"`
	Running  bool                      `json:"running"`
	Revision apiChapterOutlineRevision `json:"revision"`
}

func decodeTextRequest(r *http.Request) (string, error) {
	var req struct {
		Text string `json:"text"`
	}
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			if !errors.Is(err, io.EOF) {
				return "", fmt.Errorf("invalid request body: %w", err)
			}
		}
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return "", fmt.Errorf("text is required")
	}
	return text, nil
}

func parseAfter(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("after"))
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	if raw == "" {
		return 0, nil
	}
	after, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || after < 0 {
		return 0, fmt.Errorf("after must be a non-negative integer")
	}
	return after, nil
}

func writeProjectSessionError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrProjectNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func writeProjectManifestError(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if strings.Contains(err.Error(), "project name is required") ||
		strings.Contains(err.Error(), "project id") {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func writeProjectLifecycleError(w http.ResponseWriter, err error) {
	writeError(w, http.StatusConflict, err.Error())
}

func writeProjectSteerError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrSessionActionInProgress) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Warn("write web json response failed", "module", "web", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Start()
}
