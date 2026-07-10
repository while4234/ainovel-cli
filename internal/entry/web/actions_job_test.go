package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestActionRegistryDeduplicatesIdempotencyKey(t *testing.T) {
	registry, err := NewActionRegistry("project-actions", filepath.Join(t.TempDir(), "meta", "actions.json"))
	if err != nil {
		t.Fatalf("NewActionRegistry: %v", err)
	}
	var runs atomic.Int32
	finished := make(chan struct{})
	runner := func(context.Context) error {
		runs.Add(1)
		close(finished)
		return nil
	}

	first, created, err := registry.Start("proposal", "request-1", runner)
	if err != nil {
		t.Fatalf("Start first: %v", err)
	}
	if !created {
		t.Fatal("first action was not created")
	}
	second, created, err := registry.Start("proposal", "request-1", runner)
	if err != nil {
		t.Fatalf("Start duplicate: %v", err)
	}
	if created {
		t.Fatal("duplicate action was reported as created")
	}
	if second.ActionID != first.ActionID {
		t.Fatalf("duplicate action ID = %q, want %q", second.ActionID, first.ActionID)
	}

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("background action did not finish")
	}
	waitForActionStatus(t, registry, first.ActionID, ActionStatusCompleted)
	if got := runs.Load(); got != 1 {
		t.Fatalf("runner executions = %d, want 1", got)
	}
}

func TestActionRegistryMarksRunningActionInterruptedAfterReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta", "actions.json")
	registry, err := NewActionRegistry("project-restart", path)
	if err != nil {
		t.Fatalf("NewActionRegistry: %v", err)
	}
	release := make(chan struct{})
	action, _, err := registry.Start("outlines", "request-2", func(context.Context) error {
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	reloaded, err := NewActionRegistry("project-restart", path)
	if err != nil {
		close(release)
		t.Fatalf("reload registry: %v", err)
	}
	interrupted, err := reloaded.Get(action.ActionID)
	if err != nil {
		close(release)
		t.Fatalf("Get interrupted action: %v", err)
	}
	if interrupted.Status != ActionStatusInterrupted || !interrupted.Recoverable {
		close(release)
		t.Fatalf("interrupted action = %+v", interrupted)
	}
	if interrupted.FinishedAt == nil {
		close(release)
		t.Fatal("interrupted action has no finished_at")
	}
	close(release)
	waitForActionStatus(t, registry, action.ActionID, ActionStatusCompleted)
}

func TestActionRegistryRequiresIdempotencyKey(t *testing.T) {
	registry, err := NewActionRegistry("project-key", "")
	if err != nil {
		t.Fatalf("NewActionRegistry: %v", err)
	}
	_, _, err = registry.Start("proposal", "", func(context.Context) error { return nil })
	if !errors.Is(err, ErrActionKeyRequired) {
		t.Fatalf("Start error = %v, want ErrActionKeyRequired", err)
	}
}

func TestActionRegistryLatestSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta", "actions.json")
	registry, err := NewActionRegistry("project-latest", path)
	if err != nil {
		t.Fatalf("NewActionRegistry: %v", err)
	}
	action, _, err := registry.Start("proposal", "request-latest", func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForActionStatus(t, registry, action.ActionID, ActionStatusCompleted)

	reloaded, err := NewActionRegistry("project-latest", path)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	latest := reloaded.Latest()
	if latest == nil || latest.ActionID != action.ActionID || latest.Status != ActionStatusCompleted {
		t.Fatalf("latest action = %+v, want completed %s", latest, action.ActionID)
	}
}

func TestContinuationLongMutationAsyncReturns202AndSupportsStatusQuery(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	store := NewProjectStore(runtimeRoot)
	manifest, err := store.CreateProject("async continuation")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := newFakeProjectHost()
	session, err := NewProjectSession(manifest, fake)
	if err != nil {
		t.Fatalf("NewProjectSession: %v", err)
	}
	defer session.Close()
	manager := NewSessionManager(testWebConfig(t), assets.Load("default"), store)
	manager.sessions[manifest.ID] = session
	server := &Server{store: store, sessions: manager}

	started := make(chan struct{})
	release := make(chan struct{})
	mutation := func(ctx context.Context, _ *ProjectSession, _ continuationMutationRequest) (*domain.ContinuationSnapshot, error) {
		close(started)
		<-release
		return nil, ctx.Err()
	}
	body := `{"expected_revision":1,"async":true,"idempotency_key":"continuation-request"}`
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/continuation/proposal/generate", strings.NewReader(body))
	response := httptest.NewRecorder()
	server.handleContinuationLongMutation(response, request, manifest.ID, "continuation_proposal_generate", mutation)
	if response.Code != http.StatusAccepted {
		close(release)
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var accepted struct {
		ActionID string `json:"action_id"`
		Created  bool   `json:"created"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		close(release)
		t.Fatalf("decode accepted response: %v", err)
	}
	if accepted.ActionID == "" || !accepted.Created {
		close(release)
		t.Fatalf("accepted response = %+v", accepted)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("async mutation did not start")
	}

	duplicateRequest := httptest.NewRequest(http.MethodPost, request.URL.Path, strings.NewReader(body))
	duplicateResponse := httptest.NewRecorder()
	server.handleContinuationLongMutation(duplicateResponse, duplicateRequest, manifest.ID, "continuation_proposal_generate", mutation)
	if duplicateResponse.Code != http.StatusAccepted {
		close(release)
		t.Fatalf("duplicate status = %d body=%s", duplicateResponse.Code, duplicateResponse.Body.String())
	}
	var duplicate struct {
		ActionID string `json:"action_id"`
		Created  bool   `json:"created"`
	}
	if err := json.Unmarshal(duplicateResponse.Body.Bytes(), &duplicate); err != nil {
		close(release)
		t.Fatalf("decode duplicate response: %v", err)
	}
	if duplicate.ActionID != accepted.ActionID || duplicate.Created {
		close(release)
		t.Fatalf("duplicate response = %+v, accepted = %+v", duplicate, accepted)
	}

	query := httptest.NewRequest(http.MethodGet, request.URL.Path+"?action_id="+accepted.ActionID, nil)
	queryResponse := httptest.NewRecorder()
	server.handleContinuationLongMutation(queryResponse, query, manifest.ID, "continuation_proposal_generate", mutation)
	if queryResponse.Code != http.StatusOK {
		close(release)
		t.Fatalf("query status = %d body=%s", queryResponse.Code, queryResponse.Body.String())
	}
	close(release)
	waitForActionStatus(t, session.actions, accepted.ActionID, ActionStatusCompleted)
}

func waitForActionStatus(t *testing.T, registry *ActionRegistry, actionID string, want ActionStatus) ActionRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		action, err := registry.Get(actionID)
		if err != nil {
			t.Fatalf("Get action: %v", err)
		}
		if action.Status == want {
			return action
		}
		time.Sleep(10 * time.Millisecond)
	}
	action, _ := registry.Get(actionID)
	t.Fatalf("action status = %q, want %q", action.Status, want)
	return ActionRecord{}
}
