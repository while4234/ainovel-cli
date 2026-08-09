package web

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	artworkpkg "github.com/voocel/ainovel-cli/internal/artwork"
)

type artworkRuntime struct {
	server *Server
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	stores  map[string]*artworkpkg.WorkspaceStore
	running map[string]struct{}
	http    artworkpkg.HTTPDoer
	wg      sync.WaitGroup
}

func newArtworkRuntime(server *Server) *artworkRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &artworkRuntime{
		server: server, ctx: ctx, cancel: cancel,
		stores: make(map[string]*artworkpkg.WorkspaceStore), running: make(map[string]struct{}),
	}
	runtime.recoverExistingProjects()
	return runtime
}

func (a *artworkRuntime) close() {
	a.cancel()
	a.wg.Wait()
}

func (a *artworkRuntime) recoverExistingProjects() {
	projects, err := a.server.store.ListProjects()
	if err != nil {
		return
	}
	for _, manifest := range projects {
		if _, err := os.Stat(filepath.Join(manifest.OutputDir, "artwork", "schema.json")); err != nil {
			continue
		}
		_, _ = a.storeFor(manifest)
	}
}

func (a *artworkRuntime) storeFor(manifest ProjectManifest) (*artworkpkg.WorkspaceStore, error) {
	key := filepath.Clean(manifest.OutputDir)
	a.mu.Lock()
	if store := a.stores[key]; store != nil {
		a.mu.Unlock()
		return store, nil
	}
	created, err := artworkpkg.NewWorkspaceStore(manifest.OutputDir)
	if err != nil {
		a.mu.Unlock()
		return nil, err
	}
	recovery, err := created.Reconcile()
	if err != nil {
		a.mu.Unlock()
		return nil, err
	}
	a.stores[key] = created
	a.mu.Unlock()
	for _, jobID := range recovery.ResumedQueued {
		a.schedule(manifest, created, jobID)
	}
	return created, nil
}

func (a *artworkRuntime) schedule(manifest ProjectManifest, store *artworkpkg.WorkspaceStore, jobID string) {
	key := filepath.Clean(manifest.OutputDir) + "\x00" + jobID
	a.mu.Lock()
	if _, exists := a.running[key]; exists || a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	a.running[key] = struct{}{}
	httpClient := a.http
	a.wg.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.wg.Done()
		defer func() {
			a.mu.Lock()
			delete(a.running, key)
			a.mu.Unlock()
		}()
		runner := artworkpkg.ImageJobRunner{
			HTTPClient: httpClient,
			ResolveConfig: func() (artworkpkg.ImageGatewayConfig, error) {
				return effectiveArtworkGatewayConfig(a.server.currentConfig())
			},
		}
		_ = runner.Run(a.ctx, store, jobID)
	}()
}
