package artwork

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestArtworkWorkspaceDraftCASAndIdempotentSubmission(t *testing.T) {
	store := newTestArtworkStore(t)
	draft, err := store.CreateDraft(testDraftInput(), "create-1")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.CreateDraft(testDraftInput(), "create-1")
	if err != nil || repeated.ID != draft.ID {
		t.Fatalf("repeat draft = %+v, %v", repeated, err)
	}
	changed := testDraftInput()
	changed.Prompt = "different"
	if _, err := store.CreateDraft(changed, "create-1"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed idempotent create error = %v", err)
	}
	newPrompt := "updated prompt"
	updated, err := store.UpdateDraft(draft.ID, DraftPatch{ExpectedVersion: 1, Prompt: &newPrompt})
	if err != nil || updated.Version != 2 {
		t.Fatalf("updated draft = %+v, %v", updated, err)
	}
	var conflict *VersionConflictError
	if _, err := store.UpdateDraft(draft.ID, DraftPatch{ExpectedVersion: 1, Prompt: &newPrompt}); !errors.As(err, &conflict) || conflict.Actual != 2 {
		t.Fatalf("stale update error = %#v", err)
	}

	job, reused, err := store.SubmitGeneration(draft.ID, 2, "generate-1", "https://gateway.example")
	if err != nil || reused || job.Status != JobQueued {
		t.Fatalf("job = %+v reused=%v err=%v", job, reused, err)
	}
	repeatedJob, reused, err := store.SubmitGeneration(draft.ID, 2, "generate-1", "https://gateway.example")
	if err != nil || !reused || repeatedJob.ID != job.ID || repeatedJob.AssetID != job.AssetID {
		t.Fatalf("repeated job = %+v reused=%v err=%v", repeatedJob, reused, err)
	}
	if _, _, err := store.SubmitGeneration(draft.ID, 2, "generate-2", "https://gateway.example"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second active job error = %v", err)
	}
	publicJSON, _ := json.Marshal(job.Public())
	if strings.Contains(string(publicJSON), "gateway.example") || strings.Contains(string(publicJSON), "idempotency") {
		t.Fatalf("public job leaked internal snapshot: %s", publicJSON)
	}
}

func TestArtworkWorkerCreatesOneImmutableAssetAndFinalizeIsIdempotent(t *testing.T) {
	store := newTestArtworkStore(t)
	draft, _ := store.CreateDraft(testDraftInput(), "create")
	job, _, _ := store.SubmitGeneration(draft.ID, draft.Version, "generate", "https://gateway.invalid")
	imageBytes := fixedPNG(t)
	var calls atomic.Int32
	runner := ImageJobRunner{
		ResolveConfig: func() (ImageGatewayConfig, error) {
			return ImageGatewayConfig{APIKey: "current-key", RequestTimeoutSeconds: 10}, nil
		},
		HTTPClient: roundTripDoer(func(request *http.Request) (*http.Response, error) {
			calls.Add(1)
			if request.URL.String() != "https://gateway.invalid/v1/images/generations" || request.Header.Get("authorization") != "Bearer current-key" {
				t.Errorf("request = %s auth=%q", request.URL, request.Header.Get("authorization"))
			}
			payload := `{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(imageBytes) + `"}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
		}),
	}
	if err := runner.Run(context.Background(), store, job.ID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	finished, _ := store.GetJob(job.ID)
	asset, err := store.GetAsset(job.AssetID)
	if err != nil || finished.Status != JobSucceeded || calls.Load() != 1 || asset.Origin != "generation" {
		t.Fatalf("finished=%+v asset=%+v calls=%d err=%v", finished, asset, calls.Load(), err)
	}
	firstPath := filepath.Join(store.Root(), "images", asset.FileName)
	before, _ := os.Stat(firstPath)
	if _, err := store.FinalizeJob(job.ID, imageBytes); err != nil {
		t.Fatalf("idempotent finalize: %v", err)
	}
	after, _ := os.Stat(firstPath)
	if before.Size() != after.Size() {
		t.Fatal("idempotent finalize changed immutable image")
	}
	assets, _ := store.ListAssets("", 100)
	if len(assets.Items) != 1 {
		t.Fatalf("asset count = %d", len(assets.Items))
	}
}

func TestArtworkWorkerKnownAndUncertainFailuresNeverRetry(t *testing.T) {
	tests := []struct {
		name       string
		do         HTTPDoer
		wantStatus JobStatus
		wantCode   string
	}{
		{
			name: "known response", wantStatus: JobFailed, wantCode: "gateway_rate_limited",
			do: roundTripDoer(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader(`{"secret":"not exposed"}`)), Header: make(http.Header)}, nil
			}),
		},
		{
			name: "uncertain transport", wantStatus: JobInterruptedUnknown, wantCode: "gateway_delivery_uncertain",
			do: roundTripDoer(func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF }),
		},
		{
			name: "timeout after submission", wantStatus: JobInterruptedUnknown, wantCode: "gateway_delivery_uncertain",
			do: roundTripDoer(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded }),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestArtworkStore(t)
			draft, _ := store.CreateDraft(testDraftInput(), "create")
			job, _, _ := store.SubmitGeneration(draft.ID, 1, "generate", "https://gateway.invalid")
			var calls atomic.Int32
			runner := ImageJobRunner{
				ResolveConfig: func() (ImageGatewayConfig, error) { return ImageGatewayConfig{APIKey: "key"}, nil },
				HTTPClient: roundTripDoer(func(request *http.Request) (*http.Response, error) {
					calls.Add(1)
					return test.do.Do(request)
				}),
			}
			_ = runner.Run(context.Background(), store, job.ID)
			finished, _ := store.GetJob(job.ID)
			if calls.Load() != 1 || finished.Status != test.wantStatus || finished.ErrorCode != test.wantCode {
				t.Fatalf("calls=%d job=%+v", calls.Load(), finished)
			}
		})
	}
}

func TestArtworkRecoveryRepairsAssetBeforeJobTerminalWithoutSubmitting(t *testing.T) {
	for _, faultStage := range []string{"finalize_after_image_rename", "finalize_after_asset_metadata"} {
		t.Run(faultStage, func(t *testing.T) {
			store := newTestArtworkStore(t)
			draft, _ := store.CreateDraft(testDraftInput(), "create")
			job, _, _ := store.SubmitGeneration(draft.ID, 1, "generate", "https://gateway.invalid")
			if _, err := store.BeginJob(job.ID); err != nil {
				t.Fatal(err)
			}
			store.fault = func(stage string) error {
				if stage == faultStage {
					return errors.New("injected crash")
				}
				return nil
			}
			if _, err := store.FinalizeJob(job.ID, fixedPNG(t)); err == nil {
				t.Fatal("fault injection did not stop finalization")
			}
			store.fault = nil
			if faultStage == "finalize_after_asset_metadata" {
				journalPath, _ := store.path("journals", "finalize-"+job.ID)
				if err := os.Remove(journalPath); err != nil {
					t.Fatal(err)
				}
			}
			restarted, err := NewWorkspaceStore(filepath.Dir(store.Root()))
			if err != nil {
				t.Fatal(err)
			}
			result, err := restarted.Reconcile()
			if err != nil || result.FinalizedAssets != 1 {
				t.Fatalf("reconcile = %+v, %v", result, err)
			}
			finished, _ := restarted.GetJob(job.ID)
			if finished.Status != JobSucceeded {
				t.Fatalf("recovered job = %+v", finished)
			}
			assets, _ := restarted.ListAssets("", 100)
			if len(assets.Items) != 1 {
				t.Fatalf("recovered assets = %d", len(assets.Items))
			}
		})
	}
}

func TestArtworkReconcileRemovesOrphanAndFailsMissingCompletedAsset(t *testing.T) {
	store, asset := createSucceededTestAsset(t)
	orphanPath := filepath.Join(store.Root(), "images", "orphan.png")
	if err := os.WriteFile(orphanPath, fixedPNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := store.Reconcile()
	if err != nil || result.RemovedOrphans != 1 {
		t.Fatalf("orphan reconcile = %+v, %v", result, err)
	}
	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Fatalf("orphan still exists: %v", err)
	}

	imagePath := filepath.Join(store.Root(), "images", asset.FileName)
	_ = os.Chmod(imagePath, 0o600)
	if err := os.Remove(imagePath); err != nil {
		t.Fatal(err)
	}
	result, err = store.Reconcile()
	if err != nil || result.RemovedMissingAsset != 1 {
		t.Fatalf("missing reconcile = %+v, %v", result, err)
	}
	job, err := store.GetJob(asset.JobID)
	if err != nil || job.Status != JobFailed || job.ErrorCode != "asset_missing" {
		t.Fatalf("missing asset job = %+v, %v", job, err)
	}
	if _, err := store.GetAsset(asset.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing asset metadata remained: %v", err)
	}
}

func TestArtworkRestartNeverRepeatsRunningRequestAndResumesQueuedOnly(t *testing.T) {
	store := newTestArtworkStore(t)
	draft, _ := store.CreateDraft(testDraftInput(), "create")
	job, _, _ := store.SubmitGeneration(draft.ID, 1, "generate", "https://gateway.invalid")
	_, _ = store.BeginJob(job.ID)
	result, err := store.Reconcile()
	if err != nil || result.InterruptedRunning != 1 || len(result.ResumedQueued) != 0 {
		t.Fatalf("running reconcile = %+v, %v", result, err)
	}
	interrupted, _ := store.GetJob(job.ID)
	if interrupted.Status != JobInterruptedUnknown || interrupted.ErrorCode != "restart_delivery_unknown" {
		t.Fatalf("interrupted job = %+v", interrupted)
	}

	queuedStore := newTestArtworkStore(t)
	queuedDraft, _ := queuedStore.CreateDraft(testDraftInput(), "create")
	queued, _, _ := queuedStore.SubmitGeneration(queuedDraft.ID, 1, "generate", "https://gateway.invalid")
	queuedResult, err := queuedStore.Reconcile()
	if err != nil || len(queuedResult.ResumedQueued) != 1 || queuedResult.ResumedQueued[0] != queued.ID {
		t.Fatalf("queued reconcile = %+v, %v", queuedResult, err)
	}
}

func TestArtworkApplyDeleteAndJournalRecovery(t *testing.T) {
	store, asset := createSucceededTestAsset(t)
	store.fault = func(stage string) error {
		if stage == "apply_after_state" {
			return errors.New("injected apply crash")
		}
		return nil
	}
	if _, err := store.ApplyAsset(asset.ID); err == nil {
		t.Fatal("apply fault was not injected")
	}
	store.fault = nil
	if _, err := store.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAsset(asset.ID); !errors.Is(err, ErrAppliedAsset) {
		t.Fatalf("applied delete error = %v", err)
	}

	otherStore, otherAsset := createSucceededTestAsset(t)
	otherStore.fault = func(stage string) error {
		if stage == "delete_after_image_rename" {
			return errors.New("injected delete crash")
		}
		return nil
	}
	if err := otherStore.DeleteAsset(otherAsset.ID); err == nil {
		t.Fatal("delete fault was not injected")
	}
	otherStore.fault = nil
	if _, err := otherStore.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := otherStore.ReadAssetContent(otherAsset.ID); err != nil {
		t.Fatalf("delete recovery did not restore consistent asset: %v", err)
	}
	if err := otherStore.DeleteAsset(otherAsset.ID); err != nil {
		t.Fatalf("delete after recovery: %v", err)
	}
}

func TestArtworkImageValidationAndRequiredB64JSON(t *testing.T) {
	if _, err := ValidateImage([]byte("not an image")); err == nil {
		t.Fatal("invalid image was accepted")
	}
	if _, err := ValidateImage(oversizedPNGHeader(8000, 6000)); err == nil || !strings.Contains(err.Error(), "pixel") {
		t.Fatalf("oversized dimensions error = %v", err)
	}
	client, err := NewGatewayClient(ImageGatewayConfig{BaseURL: "https://gateway.invalid", APIKey: "key"}, roundTripDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[{"url":"https://untrusted.example/image.png"}]}`)), Header: make(http.Header)}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Generate(context.Background(), GenerateRequest{Model: "a2e", Prompt: "test", Size: "1080x1080"})
	var gatewayErr *GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.Code != "gateway_missing_b64_json" {
		t.Fatalf("URL fallback error = %#v", err)
	}
}

func TestArtworkContentRejectsCorruptAssetMetadata(t *testing.T) {
	store, assetView := createSucceededTestAsset(t)
	asset := assetView.Asset
	asset.Width++
	assetPath, err := store.path("assets", asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(assetPath, asset, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ReadAssetContent(asset.ID); err == nil {
		t.Fatal("content handler accepted dimensions inconsistent with immutable image")
	}
}

func TestArtworkCursorOrderingIsStableAndOpaque(t *testing.T) {
	store := newTestArtworkStore(t)
	fixed := time.Date(2026, 8, 9, 2, 3, 4, 0, time.UTC)
	store.now = func() time.Time { return fixed }
	for index := 0; index < 3; index++ {
		input := testDraftInput()
		input.Prompt += string(rune('a' + index))
		if _, err := store.CreateDraft(input, ""); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ListDrafts("", 2)
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" || strings.Contains(first.NextCursor, "draft-") {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	second, err := store.ListDrafts(first.NextCursor, 2)
	if err != nil || len(second.Items) != 1 {
		t.Fatalf("second page = %+v, %v", second, err)
	}
	seen := map[string]bool{}
	for _, item := range append(first.Items, second.Items...) {
		if seen[item.ID] {
			t.Fatalf("duplicate cursor item %s", item.ID)
		}
		seen[item.ID] = true
	}
}

func TestArtworkConcurrentIdempotencyAndFinalizationCreateOneJobAndAsset(t *testing.T) {
	store := newTestArtworkStore(t)
	draft, _ := store.CreateDraft(testDraftInput(), "create")
	type submitResult struct {
		job ImageJob
		err error
	}
	results := make(chan submitResult, 16)
	var submitWG sync.WaitGroup
	for index := 0; index < 16; index++ {
		submitWG.Add(1)
		go func() {
			defer submitWG.Done()
			job, _, err := store.SubmitGeneration(draft.ID, 1, "same-generation-key", "https://gateway.invalid")
			results <- submitResult{job: job, err: err}
		}()
	}
	submitWG.Wait()
	close(results)
	jobID := ""
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent submit: %v", result.err)
		}
		if jobID == "" {
			jobID = result.job.ID
		} else if result.job.ID != jobID {
			t.Fatalf("concurrent idempotency created %q and %q", jobID, result.job.ID)
		}
	}
	if _, err := store.BeginJob(jobID); err != nil {
		t.Fatal(err)
	}
	imageBytes := fixedPNG(t)
	finalizeErrors := make(chan error, 16)
	var finalizeWG sync.WaitGroup
	for index := 0; index < 16; index++ {
		finalizeWG.Add(1)
		go func() {
			defer finalizeWG.Done()
			_, err := store.FinalizeJob(jobID, imageBytes)
			finalizeErrors <- err
		}()
	}
	finalizeWG.Wait()
	close(finalizeErrors)
	for err := range finalizeErrors {
		if err != nil {
			t.Fatalf("concurrent finalize: %v", err)
		}
	}
	jobs, _ := store.ListJobs("", 100)
	assets, _ := store.ListAssets("", 100)
	if len(jobs.Items) != 1 || len(assets.Items) != 1 || jobs.Items[0].Status != JobSucceeded {
		t.Fatalf("jobs=%+v assets=%+v", jobs, assets)
	}
}

func newTestArtworkStore(t *testing.T) *WorkspaceStore {
	t.Helper()
	store, err := NewWorkspaceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testDraftInput() DraftInput {
	return DraftInput{
		WorkType: WorkTypeCover, Scope: "project", Prompt: "paint a quiet harbor",
		ModelID: "a2e", Size: "1080x1080", PromptSource: PromptSourceManual,
	}
}

func createSucceededTestAsset(t *testing.T) (*WorkspaceStore, AssetView) {
	t.Helper()
	store := newTestArtworkStore(t)
	draft, _ := store.CreateDraft(testDraftInput(), "create")
	job, _, _ := store.SubmitGeneration(draft.ID, 1, "generate", "https://gateway.invalid")
	_, _ = store.BeginJob(job.ID)
	if _, err := store.FinalizeJob(job.ID, fixedPNG(t)); err != nil {
		t.Fatal(err)
	}
	asset, err := store.GetAsset(job.AssetID)
	if err != nil {
		t.Fatal(err)
	}
	return store, asset
}

func fixedPNG(t *testing.T) []byte {
	t.Helper()
	encoded := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	content, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func oversizedPNGHeader(width, height uint32) []byte {
	content := append([]byte(nil), []byte("\x89PNG\r\n\x1a\n")...)
	chunk := make([]byte, 4+4+13+4)
	binary.BigEndian.PutUint32(chunk[0:4], 13)
	copy(chunk[4:8], "IHDR")
	binary.BigEndian.PutUint32(chunk[8:12], width)
	binary.BigEndian.PutUint32(chunk[12:16], height)
	chunk[16] = 8
	chunk[17] = 2
	binary.BigEndian.PutUint32(chunk[21:25], crc32.ChecksumIEEE(chunk[4:21]))
	return append(content, chunk...)
}

func TestArtworkNoFixtureMutation(t *testing.T) {
	// Keep the fixed image visibly local to this test package; this guard also
	// ensures no test helper ever needs a network fixture.
	if !bytes.HasPrefix(fixedPNG(t), []byte("\x89PNG")) {
		t.Fatal("fixed PNG fixture changed")
	}
}
