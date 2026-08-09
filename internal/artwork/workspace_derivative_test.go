package artwork

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestAppliedDerivativeNormalizesEXIFDeterministicallyWithoutChangingOriginal(t *testing.T) {
	store := newTestArtworkStore(t)
	original := orientedJPEGFixture(t, 6)
	asset := createSucceededAssetWithImage(t, store, testDraftInput(), "oriented", original)
	originalPath := filepath.Join(store.Root(), "images", asset.FileName)
	before, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.ApplyAsset(asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Derivative.SourceOrientation != 6 || first.Derivative.Fit != "crop" || first.Derivative.Width != 1200 || first.Derivative.Height != 1800 {
		t.Fatalf("applied derivative = %+v", first.Derivative)
	}
	_, firstPayload, err := store.ReadAppliedDerivative(WorkTypeCover, "project", "")
	if err != nil {
		t.Fatal(err)
	}
	assertRotatedFixture(t, firstPayload)
	if err := store.UnapplyAsset(asset.ID); err != nil {
		t.Fatal(err)
	}
	second, err := store.ApplyAsset(asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, secondPayload, err := store.ReadAppliedDerivative(WorkTypeCover, "project", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Derivative.FileName != second.Derivative.FileName || !bytes.Equal(firstPayload, secondPayload) {
		t.Fatal("same immutable asset did not produce the same applied derivative")
	}
	after, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !bytes.Equal(original, after) {
		t.Fatal("applying an asset changed its immutable original bytes")
	}
}

func TestAppliedDerivativeUsesDeterministicCropAndContainRules(t *testing.T) {
	wide := stripedPNGFixture(t, 400, 100)
	validated, err := ValidateImage(wide)
	if err != nil {
		t.Fatal(err)
	}
	base := Asset{
		ID: "asset-derivative-rules", WorkType: WorkTypeCover,
		MIMEType: validated.MIMEType, Width: validated.Width, Height: validated.Height, SHA256: validated.SHA256,
	}
	cropped, cropPayload, err := buildAppliedDerivative(base, wide)
	if err != nil {
		t.Fatal(err)
	}
	if cropped.Fit != "crop" || cropped.Width != 1200 || cropped.Height != 1800 {
		t.Fatalf("crop derivative = %+v", cropped)
	}
	cropImage := decodeTestImage(t, cropPayload)
	assertMostlyGreen(t, cropImage.At(600, 900))

	base.WorkType = WorkTypeIllustration
	contained, containPayload, err := buildAppliedDerivative(base, wide)
	if err != nil {
		t.Fatal(err)
	}
	if contained.Fit != "contain" || contained.Width != 1600 || contained.Height != 900 {
		t.Fatalf("contain derivative = %+v", contained)
	}
	containImage := decodeTestImage(t, containPayload)
	_, _, _, topAlpha := containImage.At(800, 100).RGBA()
	_, _, _, centerAlpha := containImage.At(800, 450).RGBA()
	if topAlpha != 0 || centerAlpha < 0xff00 {
		t.Fatalf("contain padding alpha top=%d center=%d", topAlpha, centerAlpha)
	}
}

func TestApplyPointerSwitchIsAtomicAndReconcileRepairsDerivatives(t *testing.T) {
	store := newTestArtworkStore(t)
	firstAsset := createSucceededAssetWithImage(t, store, testDraftInput(), "first", stripedPNGFixture(t, 120, 180))
	firstState, err := store.ApplyAsset(firstAsset.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondInput := testDraftInput()
	secondInput.Prompt = "second cover"
	secondAsset := createSucceededAssetWithImage(t, store, secondInput, "second", stripedPNGFixture(t, 180, 120))
	store.fault = func(stage string) error {
		if stage == "apply_after_derivative_install" {
			return errInjectedCrash
		}
		return nil
	}
	if _, err := store.ApplyAsset(secondAsset.ID); err == nil {
		t.Fatal("apply fault was not injected")
	}
	states, err := store.ListApplied()
	if err != nil || len(states) != 1 || states[0].AssetID != firstAsset.ID {
		t.Fatalf("pointer changed before derivative transaction: states=%+v err=%v", states, err)
	}
	store.fault = nil
	result, err := store.Reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedDerivatives != 1 {
		t.Fatalf("reconcile result = %+v, want one pre-journal derivative orphan removed", result)
	}

	derivativePath := filepath.Join(store.Root(), "derivatives", firstState.Derivative.FileName)
	if err := os.Chmod(derivativePath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(derivativePath); err != nil {
		t.Fatal(err)
	}
	result, err = store.Reconcile()
	if err != nil || result.RepairedApplied != 1 {
		t.Fatalf("missing derivative reconcile = %+v, %v", result, err)
	}
	if _, _, err := store.ReadAppliedDerivative(WorkTypeCover, "project", ""); err != nil {
		t.Fatalf("repaired derivative is unavailable: %v", err)
	}
}

func TestUnapplyRecoveryCompletesAndAllowsDeletion(t *testing.T) {
	store, asset := createSucceededTestAsset(t)
	if _, err := store.ApplyAsset(asset.ID); err != nil {
		t.Fatal(err)
	}
	store.fault = func(stage string) error {
		if stage == "unapply_after_state" {
			return errInjectedCrash
		}
		return nil
	}
	if err := store.UnapplyAsset(asset.ID); err == nil {
		t.Fatal("unapply fault was not injected")
	}
	store.fault = nil
	if _, err := store.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if states, err := store.ListApplied(); err != nil || len(states) != 0 {
		t.Fatalf("applied states after unapply recovery = %+v, %v", states, err)
	}
	if err := store.DeleteAsset(asset.ID); err != nil {
		t.Fatalf("delete after unapply recovery: %v", err)
	}
}

func TestAppliedReconcileDoesNotTransitionQueuedImageJobs(t *testing.T) {
	store, asset := createSucceededTestAsset(t)
	if _, err := store.ApplyAsset(asset.ID); err != nil {
		t.Fatal(err)
	}
	draftInput := testDraftInput()
	draftInput.Prompt = "queued image must stay queued"
	draft, err := store.CreateDraft(draftInput, "queued-for-applied-reconcile")
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.SubmitGeneration(draft.ID, draft.Version, "queued-job-for-applied-reconcile", "https://fixture.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileApplied(); err != nil {
		t.Fatal(err)
	}
	unchanged, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != JobQueued {
		t.Fatalf("applied-only reconcile transitioned queued job to %s", unchanged.Status)
	}
}

var errInjectedCrash = &injectedCrashError{}

type injectedCrashError struct{}

func (*injectedCrashError) Error() string { return "injected crash" }

func createSucceededAssetWithImage(t *testing.T, store *WorkspaceStore, input DraftInput, key string, content []byte) AssetView {
	t.Helper()
	draft, err := store.CreateDraft(input, "create-"+key)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.SubmitGeneration(draft.ID, draft.Version, "generate-"+key, "https://gateway.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginJob(job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeJob(job.ID, content); err != nil {
		t.Fatal(err)
	}
	asset, err := store.GetAsset(job.AssetID)
	if err != nil {
		t.Fatal(err)
	}
	return asset
}

func orientedJPEGFixture(t *testing.T, orientation uint16) []byte {
	t.Helper()
	canvas := image.NewNRGBA(image.Rect(0, 0, 120, 80))
	draw.Draw(canvas, image.Rect(0, 0, 60, 80), &image.Uniform{C: color.RGBA{R: 240, G: 20, B: 20, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(60, 0, 120, 80), &image.Uniform{C: color.RGBA{R: 20, G: 30, B: 240, A: 255}}, image.Point{}, draw.Src)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, canvas, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	tiff := make([]byte, 26)
	copy(tiff[:2], "MM")
	binary.BigEndian.PutUint16(tiff[2:4], 42)
	binary.BigEndian.PutUint32(tiff[4:8], 8)
	binary.BigEndian.PutUint16(tiff[8:10], 1)
	binary.BigEndian.PutUint16(tiff[10:12], 0x0112)
	binary.BigEndian.PutUint16(tiff[12:14], 3)
	binary.BigEndian.PutUint32(tiff[14:18], 1)
	binary.BigEndian.PutUint16(tiff[18:20], orientation)
	payload := append([]byte("Exif\x00\x00"), tiff...)
	segment := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(segment[2:4], uint16(len(payload)+2))
	jpegBytes := encoded.Bytes()
	result := make([]byte, 0, len(jpegBytes)+len(segment)+len(payload))
	result = append(result, jpegBytes[:2]...)
	result = append(result, segment...)
	result = append(result, payload...)
	result = append(result, jpegBytes[2:]...)
	return result
}

func stripedPNGFixture(t *testing.T, width, height int) []byte {
	t.Helper()
	canvas := image.NewNRGBA(image.Rect(0, 0, width, height))
	third := width / 3
	draw.Draw(canvas, image.Rect(0, 0, third, height), &image.Uniform{C: color.RGBA{R: 240, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(third, 0, width-third, height), &image.Uniform{C: color.RGBA{G: 240, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(width-third, 0, width, height), &image.Uniform{C: color.RGBA{B: 240, A: 255}}, image.Point{}, draw.Src)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func decodeTestImage(t *testing.T, payload []byte) image.Image {
	t.Helper()
	decoded, _, err := image.Decode(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func assertRotatedFixture(t *testing.T, payload []byte) {
	t.Helper()
	decoded := decodeTestImage(t, payload)
	rTop, _, bTop, _ := decoded.At(600, 200).RGBA()
	rBottom, _, bBottom, _ := decoded.At(600, 1600).RGBA()
	if rTop <= bTop || bBottom <= rBottom {
		t.Fatalf("EXIF orientation was not applied: top=(%d,%d) bottom=(%d,%d)", rTop, bTop, rBottom, bBottom)
	}
}

func assertMostlyGreen(t *testing.T, value color.Color) {
	t.Helper()
	r, g, b, _ := value.RGBA()
	if g <= r || g <= b {
		t.Fatalf("center crop did not retain the center stripe: rgb=(%d,%d,%d)", r, g, b)
	}
}
