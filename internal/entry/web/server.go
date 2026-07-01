package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
	cfg         bootstrap.Config
	bundle      assets.Bundle
	runtimeRoot string
	store       *ProjectStore
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

	server := &http.Server{
		Handler:           NewHandler(cfg, bundle, runtimeRoot),
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

func NewHandler(cfg bootstrap.Config, bundle assets.Bundle, runtimeRoot string) http.Handler {
	s := &Server{
		cfg:         cfg,
		bundle:      bundle,
		runtimeRoot: runtimeRoot,
		store:       NewProjectStore(runtimeRoot),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/runtime", s.handleRuntime)
	mux.HandleFunc("/api/projects", s.handleProjects)
	mux.HandleFunc("/api/projects/", s.handleProject)
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("content-type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, indexHTML)
}

func (s *Server) handleRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"runtime_root": s.runtimeRoot,
		"projects_dir": s.store.ProjectsDir(),
	})
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
			Name string `json:"name"`
		}
		if r.Body != nil {
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
				writeError(w, http.StatusBadRequest, "invalid project request: "+err.Error())
				return
			}
		}
		manifest, err := s.store.CreateProject(req.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, manifest)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/projects/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 || parts[1] != "open" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	manifest, err := s.store.OpenProject(parts[0])
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	h, err := s.store.OpenProjectHost(s.cfg, s.bundle, manifest)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.Close()
	writeJSON(w, http.StatusOK, manifest)
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

const indexHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>ainovel</title>
  <style>
    body { margin: 0; font-family: system-ui, sans-serif; background: #f7f7f4; color: #1f2428; }
    main { max-width: 920px; margin: 12vh auto; padding: 0 24px; }
    h1 { font-size: 34px; margin: 0 0 12px; }
    p { color: #586069; line-height: 1.6; }
    code { background: #ecebe6; padding: 2px 6px; border-radius: 4px; }
  </style>
</head>
<body>
  <main>
    <h1>ainovel web</h1>
    <p>Web server is running. Project APIs are available under <code>/api</code>.</p>
  </main>
</body>
</html>`
