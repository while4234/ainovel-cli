package artwork

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	xdraw "golang.org/x/image/draw"
)

const AppliedDerivativeVersion = "artwork-applied/v1"

type appliedDerivativeSpec struct {
	Width  int
	Height int
	Fit    string
}

func appliedDerivativeSpecFor(workType WorkType) (appliedDerivativeSpec, error) {
	switch workType {
	case WorkTypeCover:
		return appliedDerivativeSpec{Width: 1200, Height: 1800, Fit: "crop"}, nil
	case WorkTypeCharacterPortrait:
		return appliedDerivativeSpec{Width: 600, Height: 900, Fit: "crop"}, nil
	case WorkTypeIllustration:
		return appliedDerivativeSpec{Width: 1600, Height: 900, Fit: "contain"}, nil
	default:
		return appliedDerivativeSpec{}, errors.New("artwork work_type has no applied derivative rule")
	}
}

func buildAppliedDerivative(asset Asset, content []byte) (AppliedDerivative, []byte, error) {
	if err := verifyOriginalImageContent(asset, content); err != nil {
		return AppliedDerivative{}, nil, err
	}
	decoded, _, err := image.Decode(bytes.NewReader(content))
	if err != nil {
		return AppliedDerivative{}, nil, errors.New("managed artwork image could not be decoded")
	}
	orientation := jpegEXIFOrientation(content)
	oriented := orientImage(decoded, orientation)
	spec, err := appliedDerivativeSpecFor(asset.WorkType)
	if err != nil {
		return AppliedDerivative{}, nil, err
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, spec.Width, spec.Height))
	switch spec.Fit {
	case "crop":
		source := centeredCrop(oriented.Bounds(), spec.Width, spec.Height)
		xdraw.CatmullRom.Scale(canvas, canvas.Bounds(), oriented, source, xdraw.Over, nil)
	case "contain":
		destination := centeredContain(oriented.Bounds(), spec.Width, spec.Height)
		xdraw.CatmullRom.Scale(canvas, destination, oriented, oriented.Bounds(), xdraw.Over, nil)
	default:
		return AppliedDerivative{}, nil, errors.New("artwork applied derivative fit is invalid")
	}

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		return AppliedDerivative{}, nil, err
	}
	payload := encoded.Bytes()
	digest := sha256.Sum256(payload)
	fileName := deterministicID(
		"derivative",
		asset.ID,
		asset.SHA256,
		AppliedDerivativeVersion,
		spec.Fit,
		strconv.Itoa(spec.Width),
		strconv.Itoa(spec.Height),
	) + ".png"
	derivative := AppliedDerivative{
		Version: AppliedDerivativeVersion, FileName: fileName, MIMEType: "image/png",
		Width: spec.Width, Height: spec.Height, SHA256: hex.EncodeToString(digest[:]),
		Fit: spec.Fit, SourceOrientation: orientation,
	}
	return derivative, append([]byte(nil), payload...), nil
}

func verifyOriginalImageContent(asset Asset, content []byte) error {
	validated, err := ValidateImage(content)
	if err != nil {
		return err
	}
	if validated.SHA256 != asset.SHA256 || validated.MIMEType != asset.MIMEType || validated.Width != asset.Width || validated.Height != asset.Height {
		return errors.New("immutable artwork image does not match its metadata")
	}
	return nil
}

func validateAppliedDerivative(derivative AppliedDerivative) error {
	if derivative.Version != AppliedDerivativeVersion || derivative.MIMEType != "image/png" ||
		derivative.Width <= 0 || derivative.Height <= 0 || int64(derivative.Width)*int64(derivative.Height) > MaxImagePixels ||
		len(derivative.SHA256) != 64 || (derivative.Fit != "crop" && derivative.Fit != "contain") ||
		derivative.SourceOrientation < 1 || derivative.SourceOrientation > 8 {
		return errors.New("artwork applied derivative metadata is invalid")
	}
	if _, err := hex.DecodeString(derivative.SHA256); err != nil {
		return errors.New("artwork applied derivative digest is invalid")
	}
	if filepath.Base(derivative.FileName) != derivative.FileName || filepath.Ext(derivative.FileName) != ".png" || !strings.HasPrefix(derivative.FileName, "derivative-") {
		return errors.New("artwork applied derivative file name is invalid")
	}
	return nil
}

func verifyAppliedDerivativeFile(path string, derivative AppliedDerivative) error {
	if err := validateAppliedDerivative(derivative); err != nil {
		return err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	validated, err := ValidateImage(content)
	if err != nil {
		return err
	}
	if validated.SHA256 != derivative.SHA256 || validated.MIMEType != derivative.MIMEType ||
		validated.Width != derivative.Width || validated.Height != derivative.Height || filepath.Base(path) != derivative.FileName {
		return errors.New("artwork applied derivative does not match its metadata")
	}
	return nil
}

func (s *WorkspaceStore) installAppliedDerivativeUnlocked(asset Asset) (AppliedDerivative, error) {
	originalPath := filepath.Join(s.root, "images", asset.FileName)
	if err := ensureContained(s.root, originalPath); err != nil {
		return AppliedDerivative{}, err
	}
	content, err := os.ReadFile(originalPath)
	if err != nil {
		return AppliedDerivative{}, err
	}
	derivative, payload, err := buildAppliedDerivative(asset, content)
	if err != nil {
		return AppliedDerivative{}, err
	}
	finalPath := filepath.Join(s.root, "derivatives", derivative.FileName)
	if err := ensureContained(s.root, finalPath); err != nil {
		return AppliedDerivative{}, err
	}
	if err := verifyAppliedDerivativeFile(finalPath, derivative); err == nil {
		return derivative, nil
	} else if !os.IsNotExist(err) {
		_ = os.Chmod(finalPath, 0o600)
		if removeErr := os.Remove(finalPath); removeErr != nil && !os.IsNotExist(removeErr) {
			return AppliedDerivative{}, removeErr
		}
	}
	stageName := derivative.FileName + ".pending"
	stagePath := filepath.Join(s.root, "staging", stageName)
	if err := ensureContained(s.root, stagePath); err != nil {
		return AppliedDerivative{}, err
	}
	if err := writeFileAtomic(stagePath, payload, false, 0o600); err != nil {
		return AppliedDerivative{}, err
	}
	if err := s.injectFault("apply_after_derivative_stage"); err != nil {
		return AppliedDerivative{}, err
	}
	if err := os.Rename(stagePath, finalPath); err != nil {
		return AppliedDerivative{}, err
	}
	if err := os.Chmod(finalPath, 0o444); err != nil {
		return AppliedDerivative{}, err
	}
	if err := syncDirectory(filepath.Dir(finalPath)); err != nil {
		return AppliedDerivative{}, err
	}
	return derivative, nil
}

func (s *WorkspaceStore) removeDerivativeUnlocked(fileName string) error {
	if strings.TrimSpace(fileName) == "" {
		return nil
	}
	path := filepath.Join(s.root, "derivatives", fileName)
	if err := ensureContained(s.root, path); err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func (s *WorkspaceStore) ReadAppliedDerivative(workType WorkType, scope, scopeID string) (ApplyState, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := appliedTarget(workType, scope, scopeID)
	states, err := s.readAppliedUnlocked()
	if err != nil {
		return ApplyState{}, nil, err
	}
	for _, state := range states {
		if state.Target != target {
			continue
		}
		asset, err := s.readCommittedAssetUnlocked(state.AssetID)
		if err != nil || !appliedStateMatchesAsset(state, asset) {
			return ApplyState{}, nil, ErrNotFound
		}
		path := filepath.Join(s.root, "derivatives", state.Derivative.FileName)
		if err := verifyAppliedDerivativeFile(path, state.Derivative); err != nil {
			return ApplyState{}, nil, ErrNotFound
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return ApplyState{}, nil, ErrNotFound
		}
		return state, content, nil
	}
	return ApplyState{}, nil, ErrNotFound
}

func appliedTarget(workType WorkType, scope, scopeID string) string {
	return string(workType) + ":" + strings.TrimSpace(scope) + ":" + strings.TrimSpace(scopeID)
}

func appliedStateMatchesAsset(state ApplyState, asset Asset) bool {
	return state.Target == appliedTarget(asset.WorkType, asset.Scope, asset.ScopeID) && state.AssetID == asset.ID &&
		state.WorkType == asset.WorkType && state.Scope == asset.Scope && state.ScopeID == asset.ScopeID &&
		validateAppliedDerivative(state.Derivative) == nil
}

func centeredCrop(bounds image.Rectangle, targetWidth, targetHeight int) image.Rectangle {
	width, height := bounds.Dx(), bounds.Dy()
	if int64(width)*int64(targetHeight) > int64(height)*int64(targetWidth) {
		cropWidth := max(1, int(int64(height)*int64(targetWidth)/int64(targetHeight)))
		left := bounds.Min.X + (width-cropWidth)/2
		return image.Rect(left, bounds.Min.Y, left+cropWidth, bounds.Max.Y)
	}
	cropHeight := max(1, int(int64(width)*int64(targetHeight)/int64(targetWidth)))
	top := bounds.Min.Y + (height-cropHeight)/2
	return image.Rect(bounds.Min.X, top, bounds.Max.X, top+cropHeight)
}

func centeredContain(bounds image.Rectangle, targetWidth, targetHeight int) image.Rectangle {
	width, height := bounds.Dx(), bounds.Dy()
	containedWidth, containedHeight := targetWidth, targetHeight
	if int64(width)*int64(targetHeight) > int64(height)*int64(targetWidth) {
		containedHeight = max(1, int(int64(height)*int64(targetWidth)/int64(width)))
	} else {
		containedWidth = max(1, int(int64(width)*int64(targetHeight)/int64(height)))
	}
	left := (targetWidth - containedWidth) / 2
	top := (targetHeight - containedHeight) / 2
	return image.Rect(left, top, left+containedWidth, top+containedHeight)
}

func orientImage(source image.Image, orientation int) image.Image {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	outputWidth, outputHeight := width, height
	if orientation >= 5 && orientation <= 8 {
		outputWidth, outputHeight = height, width
	}
	output := image.NewNRGBA(image.Rect(0, 0, outputWidth, outputHeight))
	for y := 0; y < outputHeight; y++ {
		for x := 0; x < outputWidth; x++ {
			sx, sy := orientedSourcePoint(x, y, width, height, orientation)
			output.Set(x, y, source.At(bounds.Min.X+sx, bounds.Min.Y+sy))
		}
	}
	return output
}

func orientedSourcePoint(x, y, width, height, orientation int) (int, int) {
	switch orientation {
	case 2:
		return width - 1 - x, y
	case 3:
		return width - 1 - x, height - 1 - y
	case 4:
		return x, height - 1 - y
	case 5:
		return y, x
	case 6:
		return y, height - 1 - x
	case 7:
		return width - 1 - y, height - 1 - x
	case 8:
		return width - 1 - y, x
	default:
		return x, y
	}
}

func jpegEXIFOrientation(content []byte) int {
	if len(content) < 4 || content[0] != 0xff || content[1] != 0xd8 {
		return 1
	}
	for offset := 2; offset+4 <= len(content); {
		if content[offset] != 0xff {
			return 1
		}
		marker := content[offset+1]
		if marker == 0xd9 || marker == 0xda {
			return 1
		}
		segmentLength := int(binary.BigEndian.Uint16(content[offset+2 : offset+4]))
		if segmentLength < 2 || offset+2+segmentLength > len(content) {
			return 1
		}
		payload := content[offset+4 : offset+2+segmentLength]
		if marker == 0xe1 && len(payload) >= 6 && bytes.Equal(payload[:6], []byte("Exif\x00\x00")) {
			return tiffOrientation(payload[6:])
		}
		offset += 2 + segmentLength
	}
	return 1
}

func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(tiff[2:4]) != 42 {
		return 1
	}
	ifdOffset := int(order.Uint32(tiff[4:8]))
	if ifdOffset < 0 || ifdOffset+2 > len(tiff) {
		return 1
	}
	count := int(order.Uint16(tiff[ifdOffset : ifdOffset+2]))
	for index := 0; index < count; index++ {
		entryOffset := ifdOffset + 2 + index*12
		if entryOffset+12 > len(tiff) {
			return 1
		}
		entry := tiff[entryOffset : entryOffset+12]
		if order.Uint16(entry[:2]) != 0x0112 || order.Uint16(entry[2:4]) != 3 || order.Uint32(entry[4:8]) != 1 {
			continue
		}
		orientation := int(order.Uint16(entry[8:10]))
		if orientation >= 1 && orientation <= 8 {
			return orientation
		}
		return 1
	}
	return 1
}
