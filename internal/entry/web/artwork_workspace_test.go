package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	artworkpkg "github.com/voocel/ainovel-cli/internal/artwork"
)

func TestArtworkWorkspaceAPIEndToEndWithFakeGateway(t *testing.T) {
	imageBytes := fixedArtworkPNG(t)
	var calls atomic.Int32
	var failRequests atomic.Bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/generations" || r.Header.Get("authorization") != "Bearer current-artwork-key" {
			t.Errorf("gateway request = %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("authorization"))
		}
		if failRequests.Load() {
			http.Error(w, "raw-provider-secret-must-not-escape", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(imageBytes)}},
		})
	}))
	defer gateway.Close()

	cfg := testWebConfig(t)
	cfg.ImageGateway = &artworkpkg.ImageGatewayConfig{BaseURL: gateway.URL, APIKey: "current-artwork-key", DefaultModel: "a2e", RequestTimeoutSeconds: 10}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	project, err := server.store.CreateProject("Artwork API")
	if err != nil {
		t.Fatal(err)
	}
	otherProject, err := server.store.CreateProject("Artwork Isolation")
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	base := "/api/projects/" + project.ID + "/artwork"

	createBody := `{"work_type":"cover","scope":"project","prompt":"paint a quiet harbor","model_id":"a2e","size":"1080x1080","idempotency_key":"draft-create-1"}`
	created := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts", createBody, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var createdPayload struct {
		Draft artworkpkg.DraftView `json:"draft"`
	}
	decodeArtworkResponse(t, created, &createdPayload)
	draft := createdPayload.Draft

	repeatedCreate := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts", createBody, "")
	var repeatedCreatePayload struct {
		Draft artworkpkg.DraftView `json:"draft"`
	}
	decodeArtworkResponse(t, repeatedCreate, &repeatedCreatePayload)
	if repeatedCreatePayload.Draft.ID != draft.ID {
		t.Fatalf("idempotent draft IDs = %q and %q", draft.ID, repeatedCreatePayload.Draft.ID)
	}

	getDraft := performProjectArtworkRequest(t, handler, http.MethodGet, base+"/drafts/"+draft.ID, "", "")
	listDrafts := performProjectArtworkRequest(t, handler, http.MethodGet, base+"/drafts?limit=1", "", "")
	if getDraft.Code != http.StatusOK || listDrafts.Code != http.StatusOK {
		t.Fatalf("draft get/list = %d/%d", getDraft.Code, listDrafts.Code)
	}
	patch := performProjectArtworkRequest(t, handler, http.MethodPatch, base+"/drafts/"+draft.ID, `{"expected_version":1,"prompt":"paint a moonlit quiet harbor"}`, "")
	if patch.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", patch.Code, patch.Body.String())
	}
	var patchPayload struct {
		Draft artworkpkg.DraftView `json:"draft"`
	}
	decodeArtworkResponse(t, patch, &patchPayload)
	draft = patchPayload.Draft
	stalePatch := performProjectArtworkRequest(t, handler, http.MethodPatch, base+"/drafts/"+draft.ID, `{"expected_version":1,"prompt":"stale"}`, "")
	if stalePatch.Code != http.StatusConflict {
		t.Fatalf("stale patch status=%d body=%s", stalePatch.Code, stalePatch.Body.String())
	}
	staleConfirm := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts/"+draft.ID+"/confirm-stale-prompt", `{"expected_version":2,"source_signature":"future-source"}`, "")
	if staleConfirm.Code != http.StatusConflict {
		t.Fatalf("manual stale confirm status=%d body=%s", staleConfirm.Code, staleConfirm.Body.String())
	}

	generateBody := `{"expected_version":2,"idempotency_key":"generate-1"}`
	generated := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts/"+draft.ID+"/generate-image", generateBody, "")
	if generated.Code != http.StatusAccepted {
		t.Fatalf("generate status=%d body=%s", generated.Code, generated.Body.String())
	}
	var generationPayload struct {
		Job artworkpkg.ImageJobView `json:"job"`
	}
	decodeArtworkResponse(t, generated, &generationPayload)
	repeatedGeneration := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts/"+draft.ID+"/generate-image", generateBody, "")
	if repeatedGeneration.Code != http.StatusOK && repeatedGeneration.Code != http.StatusAccepted {
		t.Fatalf("repeat generation status=%d body=%s", repeatedGeneration.Code, repeatedGeneration.Body.String())
	}
	job := waitForArtworkJob(t, handler, base, generationPayload.Job.ID, artworkpkg.JobSucceeded)
	if calls.Load() != 1 {
		t.Fatalf("gateway calls after idempotent POST = %d", calls.Load())
	}

	jobs := performProjectArtworkRequest(t, handler, http.MethodGet, base+"/jobs", "", "")
	assetGet := performProjectArtworkRequest(t, handler, http.MethodGet, base+"/assets/"+job.AssetID, "", "")
	assetsList := performProjectArtworkRequest(t, handler, http.MethodGet, base+"/assets", "", "")
	workspace := performProjectArtworkRequest(t, handler, http.MethodGet, base, "", "")
	for label, response := range map[string]*httptest.ResponseRecorder{"jobs": jobs, "asset": assetGet, "assets": assetsList, "workspace": workspace} {
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", label, response.Code, response.Body.String())
		}
		assertArtworkResponseHasNoSecret(t, response.Body.String(), "current-artwork-key")
		if strings.Contains(response.Body.String(), gateway.URL) {
			t.Fatalf("%s leaked locked endpoint: %s", label, response.Body.String())
		}
	}

	content := performProjectArtworkRequest(t, handler, http.MethodGet, base+"/assets/"+job.AssetID+"/content", "", "")
	download := performProjectArtworkRequest(t, handler, http.MethodGet, base+"/assets/"+job.AssetID+"/download", "", "")
	if content.Code != http.StatusOK || content.Header().Get("Content-Type") != "image/png" || !bytes.Equal(content.Body.Bytes(), imageBytes) || content.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("content response status=%d headers=%v", content.Code, content.Header())
	}
	if download.Code != http.StatusOK || !strings.HasPrefix(download.Header().Get("Content-Disposition"), "attachment;") || !bytes.Equal(download.Body.Bytes(), imageBytes) {
		t.Fatalf("download response status=%d headers=%v", download.Code, download.Header())
	}

	crossProject := performProjectArtworkRequest(t, handler, http.MethodGet, "/api/projects/"+otherProject.ID+"/artwork/assets/"+job.AssetID+"/content", "", "")
	if crossProject.Code != http.StatusNotFound {
		t.Fatalf("cross-project content status=%d body=%s", crossProject.Code, crossProject.Body.String())
	}
	traversal := performProjectArtworkRequest(t, handler, http.MethodGet, base+"/assets/not-an-id/content", "", "")
	if traversal.Code != http.StatusNotFound && traversal.Code != http.StatusBadRequest {
		t.Fatalf("invalid ID status=%d body=%s", traversal.Code, traversal.Body.String())
	}

	applyFirst := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/assets/"+job.AssetID+"/apply", `{}`, "")
	applyAgain := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/assets/"+job.AssetID+"/apply", `{}`, "")
	if applyFirst.Code != http.StatusOK || applyAgain.Code != http.StatusOK || applyFirst.Body.String() != applyAgain.Body.String() {
		t.Fatalf("idempotent apply status=%d/%d body=%s / %s", applyFirst.Code, applyAgain.Code, applyFirst.Body.String(), applyAgain.Body.String())
	}
	protectedDelete := performProjectArtworkRequest(t, handler, http.MethodDelete, base+"/assets/"+job.AssetID, "", "")
	if protectedDelete.Code != http.StatusConflict {
		t.Fatalf("protected delete status=%d body=%s", protectedDelete.Code, protectedDelete.Body.String())
	}

	reuseBody := `{"idempotency_key":"reuse-1"}`
	reused := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/assets/"+job.AssetID+"/reuse-as-draft", reuseBody, "")
	reusedAgain := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/assets/"+job.AssetID+"/reuse-as-draft", reuseBody, "")
	if reused.Code != http.StatusCreated || reusedAgain.Code != http.StatusOK {
		t.Fatalf("reuse status=%d/%d bodies=%s / %s", reused.Code, reusedAgain.Code, reused.Body.String(), reusedAgain.Body.String())
	}
	var reusePayload struct {
		Draft artworkpkg.DraftView `json:"draft"`
	}
	decodeArtworkResponse(t, reused, &reusePayload)
	if reusePayload.Draft.PromptSource != artworkpkg.PromptSourceReuse || reusePayload.Draft.SourceAssetID != job.AssetID {
		t.Fatalf("reuse draft = %+v", reusePayload.Draft)
	}

	secondGeneration := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts/"+reusePayload.Draft.ID+"/generate-image", `{"expected_version":1,"idempotency_key":"generate-2"}`, "")
	var secondPayload struct {
		Job artworkpkg.ImageJobView `json:"job"`
	}
	decodeArtworkResponse(t, secondGeneration, &secondPayload)
	secondJob := waitForArtworkJob(t, handler, base, secondPayload.Job.ID, artworkpkg.JobSucceeded)
	applySecond := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/assets/"+secondJob.AssetID+"/apply", `{}`, "")
	if applySecond.Code != http.StatusOK {
		t.Fatalf("apply second status=%d body=%s", applySecond.Code, applySecond.Body.String())
	}
	deleteFirst := performProjectArtworkRequest(t, handler, http.MethodDelete, base+"/assets/"+job.AssetID, "", "")
	if deleteFirst.Code != http.StatusNoContent {
		t.Fatalf("delete un-applied first status=%d body=%s", deleteFirst.Code, deleteFirst.Body.String())
	}

	failRequests.Store(true)
	failureDraftBody := `{"work_type":"illustration","scope":"chapter","scope_id":"chapter-1","prompt":"failing illustration","model_id":"a2e","size":"1080x1080","idempotency_key":"draft-failure"}`
	failureDraftResponse := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts", failureDraftBody, "")
	var failureDraftPayload struct {
		Draft artworkpkg.DraftView `json:"draft"`
	}
	decodeArtworkResponse(t, failureDraftResponse, &failureDraftPayload)
	failureGeneration := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts/"+failureDraftPayload.Draft.ID+"/generate-image", `{"expected_version":1,"idempotency_key":"generate-failure"}`, "")
	var failureGenerationPayload struct {
		Job artworkpkg.ImageJobView `json:"job"`
	}
	decodeArtworkResponse(t, failureGeneration, &failureGenerationPayload)
	failedJob := waitForArtworkJob(t, handler, base, failureGenerationPayload.Job.ID, artworkpkg.JobFailed)
	failedResponse := performProjectArtworkRequest(t, handler, http.MethodGet, base+"/jobs/"+failedJob.ID, "", "")
	if strings.Contains(failedResponse.Body.String(), "raw-provider-secret") || strings.Contains(failedResponse.Body.String(), gateway.URL) {
		t.Fatalf("failed job leaked provider details: %s", failedResponse.Body.String())
	}

	deleteDraft := performProjectArtworkRequest(t, handler, http.MethodDelete, base+"/drafts/"+draft.ID+"?expected_version=2", "", "")
	if deleteDraft.Code != http.StatusNoContent {
		t.Fatalf("delete draft status=%d body=%s", deleteDraft.Code, deleteDraft.Body.String())
	}
}

func TestArtworkStartupRecoveryResumesQueuedButNeverRunningJobs(t *testing.T) {
	imageBytes := fixedArtworkPNG(t)
	var calls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(imageBytes)}}})
	}))
	defer gateway.Close()

	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	projectStore := NewProjectStore(runtimeRoot)
	queuedProject, _ := projectStore.CreateProject("Queued Artwork")
	runningProject, _ := projectStore.CreateProject("Running Artwork")
	queuedStore, _ := artworkpkg.NewWorkspaceStore(queuedProject.OutputDir)
	runningStore, _ := artworkpkg.NewWorkspaceStore(runningProject.OutputDir)
	queuedDraft, _ := queuedStore.CreateDraft(artworkAPITestDraftInput(), "queued-draft")
	runningDraft, _ := runningStore.CreateDraft(artworkAPITestDraftInput(), "running-draft")
	queuedJob, _, _ := queuedStore.SubmitGeneration(queuedDraft.ID, 1, "queued-job", gateway.URL)
	runningJob, _, _ := runningStore.SubmitGeneration(runningDraft.ID, 1, "running-job", gateway.URL)
	_, _ = runningStore.BeginJob(runningJob.ID)

	cfg := testWebConfig(t)
	cfg.ImageGateway = &artworkpkg.ImageGatewayConfig{BaseURL: gateway.URL, APIKey: "current-key", DefaultModel: "a2e", RequestTimeoutSeconds: 10}
	server := NewServer(cfg, assets.Load("default"), runtimeRoot)
	defer server.Close()
	handler := server.Handler()
	queuedBase := "/api/projects/" + queuedProject.ID + "/artwork"
	runningBase := "/api/projects/" + runningProject.ID + "/artwork"
	waitForArtworkJob(t, handler, queuedBase, queuedJob.ID, artworkpkg.JobSucceeded)
	interrupted := waitForArtworkJob(t, handler, runningBase, runningJob.ID, artworkpkg.JobInterruptedUnknown)
	if calls.Load() != 1 || interrupted.ErrorCode != "restart_delivery_unknown" {
		t.Fatalf("startup calls=%d running=%+v", calls.Load(), interrupted)
	}
}

func TestArtworkRuntimeLeavesUnusedProjectsUntouched(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	projectStore := NewProjectStore(runtimeRoot)
	project, err := projectStore.CreateProject("No Artwork Yet")
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	server.Close()
	if _, err := os.Stat(filepath.Join(project.OutputDir, "artwork")); !os.IsNotExist(err) {
		t.Fatalf("startup created an unused artwork workspace: %v", err)
	}
}

func TestArtworkProjectCloneCopiesAssetsButNeverReplaysQueuedJob(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	projectStore := NewProjectStore(runtimeRoot)
	source, err := projectStore.CreateProject("Artwork Clone Source")
	if err != nil {
		t.Fatal(err)
	}
	workspace, _ := artworkpkg.NewWorkspaceStore(source.OutputDir)
	firstDraft, _ := workspace.CreateDraft(artworkAPITestDraftInput(), "first-draft")
	firstJob, _, _ := workspace.SubmitGeneration(firstDraft.ID, 1, "first-job", "https://gateway.invalid")
	_, _ = workspace.BeginJob(firstJob.ID)
	if _, err := workspace.FinalizeJob(firstJob.ID, fixedArtworkPNG(t)); err != nil {
		t.Fatal(err)
	}
	queuedDraft, _ := workspace.CreateDraft(artworkAPITestDraftInput(), "queued-draft")
	queuedJob, _, _ := workspace.SubmitGeneration(queuedDraft.ID, 1, "queued-job", "https://gateway.invalid")

	clone, err := projectStore.CloneProject(source.ID, "Artwork Clone")
	if err != nil {
		t.Fatal(err)
	}
	clonedWorkspace, err := artworkpkg.NewWorkspaceStore(clone.OutputDir)
	if err != nil {
		t.Fatal(err)
	}
	clonedAsset, err := clonedWorkspace.GetAsset(firstJob.AssetID)
	if err != nil || clonedAsset.SHA256 == "" {
		t.Fatalf("cloned asset = %+v, %v", clonedAsset, err)
	}
	clonedQueued, err := clonedWorkspace.GetJob(queuedJob.ID)
	if err != nil || clonedQueued.Status != artworkpkg.JobFailed || clonedQueued.ErrorCode != "cloned_job_not_resumed" {
		t.Fatalf("cloned queued job = %+v, %v", clonedQueued, err)
	}
	sourceQueued, _ := workspace.GetJob(queuedJob.ID)
	if sourceQueued.Status != artworkpkg.JobQueued {
		t.Fatalf("clone mutated source queued job: %+v", sourceQueued)
	}
}

func performProjectArtworkRequest(t *testing.T, handler http.Handler, method, path, body, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("content-type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func waitForArtworkJob(t *testing.T, handler http.Handler, base, jobID string, want artworkpkg.JobStatus) artworkpkg.ImageJobView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response := performProjectArtworkRequest(t, handler, http.MethodGet, base+"/jobs/"+jobID, "", "")
		if response.Code != http.StatusOK {
			t.Fatalf("job poll status=%d body=%s", response.Code, response.Body.String())
		}
		var payload struct {
			Job artworkpkg.ImageJobView `json:"job"`
		}
		decodeArtworkResponse(t, response, &payload)
		if payload.Job.Status == want {
			return payload.Job
		}
		if payload.Job.Status.Terminal() {
			t.Fatalf("job reached %s, want %s: %+v", payload.Job.Status, want, payload.Job)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach %s", jobID, want)
	return artworkpkg.ImageJobView{}
}

func fixedArtworkPNG(t *testing.T) []byte {
	t.Helper()
	content, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func artworkAPITestDraftInput() artworkpkg.DraftInput {
	return artworkpkg.DraftInput{WorkType: artworkpkg.WorkTypeCover, Scope: "project", Prompt: "startup recovery", ModelID: "a2e", Size: "1080x1080", PromptSource: artworkpkg.PromptSourceManual}
}
