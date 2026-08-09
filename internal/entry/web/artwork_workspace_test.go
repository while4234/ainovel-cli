package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/assets"
	artworkpkg "github.com/voocel/ainovel-cli/internal/artwork"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/globalprompt"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestArtworkPromptAPIUsesOneCallAndRequiresCurrentDigestConfirmation(t *testing.T) {
	imageBytes := fixedArtworkPNG(t)
	var gatewayCalls atomic.Int32
	var forwardedPrompt atomic.Value
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayCalls.Add(1)
		var request struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode image request: %v", err)
		}
		forwardedPrompt.Store(request.Prompt)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(imageBytes)}}})
	}))
	defer gateway.Close()

	cfg := testWebConfig(t)
	cfg.ImageGateway = &artworkpkg.ImageGatewayConfig{BaseURL: gateway.URL, APIKey: "fake-image-key", DefaultModel: "a2e", RequestTimeoutSeconds: 10}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	project, err := server.store.CreateProject("Artwork Prompt API")
	if err != nil {
		t.Fatal(err)
	}
	projectStore := storepkg.NewStore(project.OutputDir)
	if err := projectStore.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := projectStore.Foundation.SaveCAS(domain.StoryFoundation{Premise: "A lighthouse defends a winter harbor."}, 0); err != nil {
		t.Fatal(err)
	}
	volumeID := domain.LegacyStructureID(project.ID, domain.StructureKindVolume, "volume-1")
	arcID := domain.LegacyStructureID(project.ID, domain.StructureKindArc, "volume-1/arc-1")
	chapterID := domain.LegacyStructureID(project.ID, domain.StructureKindChapter, "volume-1/arc-1/chapter-1")
	if err := projectStore.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		ID: volumeID, Index: 1, Title: "Winter Harbor", Theme: "hope",
		Arcs: []domain.ArcOutline{{ID: arcID, Index: 1, Title: "Storm", Goal: "relight the beacon", Chapters: []domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "The Beacon", CoreEvent: "the lamp fails", Hook: "a blue flame appears"}}}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := projectStore.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 1, Summary: "The keeper discovers a blue flame."}); err != nil {
		t.Fatal(err)
	}

	textModel := &artworkWebPromptModel{response: "exact editable generated prompt"}
	server.artworkRuntime.promptModelFactory = func(ProjectManifest) (agentcore.ChatModel, artworkpkg.TextModelSnapshot, error) {
		return globalprompt.WrapModel(textModel), artworkpkg.TextModelSnapshot{Provider: "fake-provider", Model: "fake-text", ReasoningEffort: "low"}, nil
	}
	handler := server.Handler()
	base := "/api/projects/" + project.ID + "/artwork"
	created := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts", `{"work_type":"cover","scope":"project","prompt":"manual fallback","model_id":"a2e","size":"1080x1080","idempotency_key":"prompt-draft"}`, "")
	var createPayload struct {
		Draft artworkpkg.DraftView `json:"draft"`
	}
	decodeArtworkResponse(t, created, &createPayload)
	draft := createPayload.Draft

	promptRequest := `{"expected_version":1,"idempotency_key":"prompt-call-one"}`
	generated := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts/"+draft.ID+"/generate-prompt", promptRequest, "")
	if generated.Code != http.StatusCreated {
		t.Fatalf("generate prompt status=%d body=%s", generated.Code, generated.Body.String())
	}
	var generatedPayload struct {
		Job   artworkpkg.PromptJobView `json:"job"`
		Draft artworkpkg.DraftView     `json:"draft"`
	}
	decodeArtworkResponse(t, generated, &generatedPayload)
	draft = generatedPayload.Draft
	if textModel.calls != 1 || draft.Prompt != textModel.response || draft.PromptSource != artworkpkg.PromptSourceAI || generatedPayload.Job.Status != artworkpkg.PromptJobSucceeded {
		t.Fatalf("model calls=%d draft=%+v job=%+v", textModel.calls, draft, generatedPayload.Job)
	}
	if len(textModel.messages) != 2 || textModel.messages[0].TextContent() != server.bundle.Prompts.Artwork || strings.HasPrefix(textModel.messages[0].TextContent(), globalprompt.Text()) {
		t.Fatalf("artwork system prompt was not isolated: %+v", textModel.messages)
	}
	repeated := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts/"+draft.ID+"/generate-prompt", promptRequest, "")
	if repeated.Code != http.StatusOK || textModel.calls != 1 {
		t.Fatalf("idempotent prompt status=%d calls=%d body=%s", repeated.Code, textModel.calls, repeated.Body.String())
	}

	jobDetail := performProjectArtworkRequest(t, handler, http.MethodGet, base+"/prompt-jobs/"+generatedPayload.Job.ID, "", "")
	history := performProjectArtworkRequest(t, handler, http.MethodGet, base+"/drafts/"+draft.ID+"/prompt-jobs", "", "")
	workspace := performProjectArtworkRequest(t, handler, http.MethodGet, base, "", "")
	if jobDetail.Code != http.StatusOK || history.Code != http.StatusOK || workspace.Code != http.StatusOK {
		t.Fatalf("prompt query status detail/history/workspace=%d/%d/%d", jobDetail.Code, history.Code, workspace.Code)
	}
	if strings.Contains(jobDetail.Body.String(), "fake-image-key") || strings.Contains(jobDetail.Body.String(), gateway.URL) || !strings.Contains(jobDetail.Body.String(), generatedPayload.Job.SourceDigest) {
		t.Fatalf("prompt detail leaked configuration or lost provenance: %s", jobDetail.Body.String())
	}

	if err := projectStore.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 1, Summary: "The blue flame moves into the storm."}); err != nil {
		t.Fatal(err)
	}
	detail := performProjectArtworkRequest(t, handler, http.MethodGet, base+"/drafts/"+draft.ID, "", "")
	var detailPayload struct {
		Draft artworkpkg.DraftView `json:"draft"`
	}
	decodeArtworkResponse(t, detail, &detailPayload)
	stale := detailPayload.Draft
	if !stale.IsStale || stale.SourceStatus != "stale" || stale.CurrentSourceSignature == "" || stale.CurrentSourceSignature == stale.SourceSignature || !stale.UpdatedAt.Equal(draft.UpdatedAt) {
		t.Fatalf("stale detail mutated or missed freshness: before=%+v after=%+v", draft, stale)
	}
	staleSubmit := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts/"+draft.ID+"/generate-image", `{"expected_version":2,"idempotency_key":"stale-image"}`, "")
	if staleSubmit.Code != http.StatusConflict || !strings.Contains(staleSubmit.Body.String(), "stale_prompt_confirmation_required") || gatewayCalls.Load() != 0 {
		t.Fatalf("stale submit status=%d calls=%d body=%s", staleSubmit.Code, gatewayCalls.Load(), staleSubmit.Body.String())
	}
	wrongConfirm := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts/"+draft.ID+"/confirm-stale-prompt", `{"expected_version":2,"source_signature":"wrong-digest"}`, "")
	if wrongConfirm.Code != http.StatusConflict {
		t.Fatalf("wrong confirmation status=%d body=%s", wrongConfirm.Code, wrongConfirm.Body.String())
	}
	confirmBody, _ := json.Marshal(map[string]any{"expected_version": 2, "source_signature": stale.CurrentSourceSignature})
	confirmedResponse := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts/"+draft.ID+"/confirm-stale-prompt", string(confirmBody), "")
	if confirmedResponse.Code != http.StatusOK {
		t.Fatalf("confirm status=%d body=%s", confirmedResponse.Code, confirmedResponse.Body.String())
	}
	var confirmedPayload struct {
		Draft artworkpkg.DraftView `json:"draft"`
	}
	decodeArtworkResponse(t, confirmedResponse, &confirmedPayload)
	confirmed := confirmedPayload.Draft
	if confirmed.Version != 3 || confirmed.ConfirmedSignature != stale.CurrentSourceSignature || confirmed.ConfirmedAt == nil {
		t.Fatalf("confirmed draft=%+v", confirmed)
	}

	imageResponse := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts/"+draft.ID+"/generate-image", `{"expected_version":3,"idempotency_key":"confirmed-image"}`, "")
	if imageResponse.Code != http.StatusAccepted {
		t.Fatalf("confirmed image status=%d body=%s", imageResponse.Code, imageResponse.Body.String())
	}
	var imagePayload struct {
		Job artworkpkg.ImageJobView `json:"job"`
	}
	decodeArtworkResponse(t, imageResponse, &imagePayload)
	completed := waitForArtworkJob(t, handler, base, imagePayload.Job.ID, artworkpkg.JobSucceeded)
	if gatewayCalls.Load() != 1 || forwardedPrompt.Load() != textModel.response || completed.StaleConfirmation == nil || completed.StaleConfirmation.ConfirmedSourceDigest != stale.CurrentSourceSignature {
		t.Fatalf("image forwarding calls=%d prompt=%v job=%+v", gatewayCalls.Load(), forwardedPrompt.Load(), completed)
	}
	assetResponse := performProjectArtworkRequest(t, handler, http.MethodGet, base+"/assets/"+completed.AssetID, "", "")
	if !strings.Contains(assetResponse.Body.String(), stale.CurrentSourceSignature) || !strings.Contains(assetResponse.Body.String(), textModel.response) {
		t.Fatalf("asset lost prompt/source provenance: %s", assetResponse.Body.String())
	}
}

func TestArtworkPromptModelUnavailableKeepsManualImageGenerationUsable(t *testing.T) {
	imageBytes := fixedArtworkPNG(t)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(imageBytes)}}})
	}))
	defer gateway.Close()
	cfg := testWebConfig(t)
	cfg.ImageGateway = &artworkpkg.ImageGatewayConfig{BaseURL: gateway.URL, APIKey: "fake", DefaultModel: "a2e", RequestTimeoutSeconds: 10}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	server.artworkRuntime.promptModelFactory = func(ProjectManifest) (agentcore.ChatModel, artworkpkg.TextModelSnapshot, error) {
		return nil, artworkpkg.TextModelSnapshot{}, errors.New("text model unavailable")
	}
	project, _ := server.store.CreateProject("Manual Artwork Fallback")
	projectStore := storepkg.NewStore(project.OutputDir)
	_ = projectStore.Init()
	_, _ = projectStore.Foundation.SaveCAS(domain.StoryFoundation{Premise: "manual fallback source"}, 0)
	handler := server.Handler()
	base := "/api/projects/" + project.ID + "/artwork"
	created := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts", `{"work_type":"cover","scope":"project","prompt":"manual prompt remains usable","model_id":"a2e","size":"1080x1080","idempotency_key":"manual-fallback"}`, "")
	var payload struct {
		Draft artworkpkg.DraftView `json:"draft"`
	}
	decodeArtworkResponse(t, created, &payload)
	unavailable := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts/"+payload.Draft.ID+"/generate-prompt", `{"expected_version":1,"idempotency_key":"unavailable-prompt"}`, "")
	if unavailable.Code != http.StatusConflict || !strings.Contains(unavailable.Body.String(), "prompt_model_unavailable") {
		t.Fatalf("unavailable prompt status=%d body=%s", unavailable.Code, unavailable.Body.String())
	}
	image := performProjectArtworkRequest(t, handler, http.MethodPost, base+"/drafts/"+payload.Draft.ID+"/generate-image", `{"expected_version":1,"idempotency_key":"manual-image"}`, "")
	if image.Code != http.StatusAccepted {
		t.Fatalf("manual image status=%d body=%s", image.Code, image.Body.String())
	}
}

type artworkWebPromptModel struct {
	calls    int
	response string
	messages []agentcore.Message
}

func (m *artworkWebPromptModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.calls++
	m.messages = append([]agentcore.Message(nil), messages...)
	return &agentcore.LLMResponse{Message: agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock(m.response)}}}, nil
}

func (m *artworkWebPromptModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, errors.New("unexpected stream call")
}

func (m *artworkWebPromptModel) SupportsTools() bool { return false }

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
	if !strings.Contains(applyFirst.Body.String(), "/applied-content") {
		t.Fatalf("applied response has no derivative URL: %s", applyFirst.Body.String())
	}
	appliedContent := performProjectArtworkRequest(t, handler, http.MethodGet, base+"/assets/"+job.AssetID+"/applied-content", "", "")
	if appliedContent.Code != http.StatusOK || appliedContent.Header().Get("Content-Type") != "image/png" || bytes.Equal(appliedContent.Body.Bytes(), imageBytes) {
		t.Fatalf("applied derivative status=%d headers=%v", appliedContent.Code, appliedContent.Header())
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
	unapplySecond := performProjectArtworkRequest(t, handler, http.MethodDelete, base+"/assets/"+secondJob.AssetID+"/apply", "", "")
	if unapplySecond.Code != http.StatusOK || strings.Contains(unapplySecond.Body.String(), "/applied-content") {
		t.Fatalf("unapply second status=%d body=%s", unapplySecond.Code, unapplySecond.Body.String())
	}
	missingAppliedContent := performProjectArtworkRequest(t, handler, http.MethodGet, base+"/assets/"+secondJob.AssetID+"/applied-content", "", "")
	if missingAppliedContent.Code != http.StatusNotFound {
		t.Fatalf("unapplied derivative status=%d body=%s", missingAppliedContent.Code, missingAppliedContent.Body.String())
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
	sourceApplied, err := workspace.ApplyAsset(firstJob.AssetID)
	if err != nil {
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
	clonedApplied, clonedDerivative, err := clonedWorkspace.ReadAppliedDerivative(artworkpkg.WorkTypeCover, "project", "")
	if err != nil || clonedApplied.AssetID != firstJob.AssetID || clonedApplied.Derivative.SHA256 != sourceApplied.Derivative.SHA256 || len(clonedDerivative) == 0 {
		t.Fatalf("cloned applied artwork = %+v bytes=%d err=%v", clonedApplied, len(clonedDerivative), err)
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
