package artwork

import (
	"fmt"
	"strings"
)

const (
	CapabilityRegistryVersion = "artwork-image-capabilities/v1"
	DefaultModelID            = "a2e"
)

type SizeCapability struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
}

type ModelCapability struct {
	ID             string           `json:"id"`
	Label          string           `json:"label"`
	Family         string           `json:"family"`
	Enabled        bool             `json:"enabled"`
	Verified       bool             `json:"verified"`
	AliasFor       string           `json:"alias_for,omitempty"`
	RequestFields  []string         `json:"request_fields"`
	SupportedSizes []SizeCapability `json:"supported_sizes"`
}

type CapabilityRegistry struct {
	Version string            `json:"version"`
	Models  []ModelCapability `json:"models"`
}

type ImageRequestOptions struct {
	Model       string `json:"model"`
	Size        string `json:"size"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
}

var modelCapabilities = buildModelCapabilities()

func Registry() CapabilityRegistry {
	models := make([]ModelCapability, len(modelCapabilities))
	for i, model := range modelCapabilities {
		models[i] = cloneModelCapability(model)
	}
	return CapabilityRegistry{Version: CapabilityRegistryVersion, Models: models}
}

func LookupModel(id string) (ModelCapability, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, model := range modelCapabilities {
		if model.ID == id {
			return cloneModelCapability(model), true
		}
	}
	return ModelCapability{}, false
}

func ResolveImageRequest(modelID, requestedSize string) (ImageRequestOptions, error) {
	model, ok := LookupModel(modelID)
	if !ok {
		return ImageRequestOptions{}, fmt.Errorf("unknown image model %q", strings.TrimSpace(modelID))
	}
	requestedSize = strings.ToLower(strings.TrimSpace(requestedSize))
	for _, size := range model.SupportedSizes {
		if strings.ToLower(size.Value) != requestedSize {
			continue
		}
		resolvedModel := model.ID
		if model.AliasFor != "" {
			resolvedModel = model.AliasFor
		}
		return ImageRequestOptions{
			Model:       resolvedModel,
			Size:        size.Value,
			AspectRatio: size.AspectRatio,
			Resolution:  size.Resolution,
		}, nil
	}
	return ImageRequestOptions{}, fmt.Errorf("size %q is not supported by image model %q", requestedSize, model.ID)
}

func buildModelCapabilities() []ModelCapability {
	a2e1080 := []SizeCapability{
		preset("1080x1080", "1080P · 1:1 方图", "1:1", ""),
		preset("1620x1080", "1080P · 3:2 横图", "3:2", ""),
		preset("1080x1620", "1080P · 2:3 竖图", "2:3", ""),
		preset("1440x1080", "1080P · 4:3 横图", "4:3", ""),
		preset("1080x1440", "1080P · 3:4 竖图", "3:4", ""),
		preset("1920x1080", "1080P · 16:9 横图", "16:9", ""),
		preset("1080x1920", "1080P · 9:16 竖图", "9:16", ""),
		preset("2520x1080", "1080P · 21:9 超宽图", "21:9", ""),
	}
	ratios1K := []SizeCapability{
		preset("1024x1024", "1K · 1:1 方图", "1:1", "1k"),
		preset("1328x880", "1K · 3:2 横图", "3:2", "1k"),
		preset("880x1328", "1K · 2:3 竖图", "2:3", "1k"),
		preset("1152x864", "1K · 4:3 横图", "4:3", "1k"),
		preset("864x1152", "1K · 3:4 竖图", "3:4", "1k"),
		preset("1280x720", "1K · 16:9 横图", "16:9", "1k"),
		preset("720x1280", "1K · 9:16 竖图", "9:16", "1k"),
		preset("1512x648", "1K · 21:9 超宽图", "21:9", "1k"),
	}
	ratios2K := []SizeCapability{
		preset("2048x2048", "2K · 1:1 方图", "1:1", "2k"),
		preset("2048x1365", "2K · 3:2 横图", "3:2", "2k"),
		preset("1365x2048", "2K · 2:3 竖图", "2:3", "2k"),
		preset("2048x1536", "2K · 4:3 横图", "4:3", "2k"),
		preset("1536x2048", "2K · 3:4 竖图", "3:4", "2k"),
		preset("2048x1152", "2K · 16:9 横图", "16:9", "2k"),
		preset("1152x2048", "2K · 9:16 竖图", "9:16", "2k"),
	}
	ratio2192K := preset("2688x1152", "2K · 21:9 超宽图", "21:9", "2k")
	seedream4K := []SizeCapability{
		preset("4096x4096", "4K · 1:1 方图", "1:1", ""),
		preset("4096x2731", "4K · 3:2 横图", "3:2", ""),
		preset("2731x4096", "4K · 2:3 竖图", "2:3", ""),
		preset("4096x3072", "4K · 4:3 横图", "4:3", ""),
		preset("3072x4096", "4K · 3:4 竖图", "3:4", ""),
		preset("4096x2304", "4K · 16:9 横图", "16:9", ""),
		preset("2304x4096", "4K · 9:16 竖图", "9:16", ""),
		preset("4096x1755", "4K · 21:9 超宽图", "21:9", ""),
	}
	gptImage2 := []SizeCapability{
		preset("1024x1024", "1024×1024 · 1:1 方图", "", ""),
		preset("768x1024", "768×1024 · 3:4 竖图", "", ""),
		preset("1024x1536", "1024×1536 · 2:3 竖图", "", ""),
		preset("1536x1024", "1536×1024 · 3:2 横图", "", ""),
		preset("1536x2048", "1536×2048 · 3:4 竖图", "", ""),
		preset("2048x1536", "2048×1536 · 4:3 横图", "", ""),
		preset("2048x1152", "2048×1152 · 16:9 横图", "", ""),
		preset("2048x2048", "2048×2048 · 1:1 方图", "", ""),
		preset("2160x3840", "2160×3840 · 4K 竖图", "", ""),
		preset("3840x2160", "3840×2160 · 4K 横图", "", ""),
	}
	grok1K := []SizeCapability{
		preset("1024x1024", "1K · 1:1 方图", "1:1", "1k"),
		preset("1024x683", "1K · 3:2 横图", "3:2", "1k"),
		preset("683x1024", "1K · 2:3 竖图", "2:3", "1k"),
		preset("1024x768", "1K · 4:3 横图", "4:3", "1k"),
		preset("768x1024", "1K · 3:4 竖图", "3:4", "1k"),
		preset("1024x576", "1K · 16:9 横图", "16:9", "1k"),
		preset("576x1024", "1K · 9:16 竖图", "9:16", "1k"),
		preset("1024x512", "1K · 2:1 横图", "2:1", "1k"),
		preset("512x1024", "1K · 1:2 竖图", "1:2", "1k"),
	}
	grok2K := append(cloneSizes(ratios2K),
		preset("2048x1024", "2K · 2:1 横图", "2:1", "2k"),
		preset("1024x2048", "2K · 1:2 竖图", "1:2", "2k"),
	)

	withoutResolution := func(values []SizeCapability) []SizeCapability {
		out := cloneSizes(values)
		for i := range out {
			out[i].Resolution = ""
		}
		return out
	}
	withoutAspectRatio := func(values []SizeCapability) []SizeCapability {
		out := cloneSizes(values)
		for i := range out {
			out[i].AspectRatio = ""
			out[i].Resolution = ""
		}
		return out
	}
	wanRatios := cloneSizes(ratios1K)
	for i := range wanRatios {
		orientation := " 超宽图"
		switch {
		case strings.Contains(wanRatios[i].Label, "横图"):
			orientation = " 横图"
		case strings.Contains(wanRatios[i].Label, "竖图"):
			orientation = " 竖图"
		case strings.Contains(wanRatios[i].Label, "方图"):
			orientation = " 方图"
		}
		wanRatios[i].Label = "模型默认 · " + wanRatios[i].AspectRatio + orientation
		wanRatios[i].Resolution = ""
	}
	flux := make([]SizeCapability, 0, 14)
	for _, option := range append(cloneSizes(ratios1K), ratios2K...) {
		if option.AspectRatio == "21:9" {
			continue
		}
		flux = append(flux, option)
	}

	a2e2K := append(cloneSizes(ratios2K), ratio2192K)
	qwen1K := withoutAspectRatio(ratios1K)
	qwen3Pro := withoutAspectRatio(append(cloneSizes(ratios1K), ratios2K...))
	kling := append(append(cloneSizes(ratios1K), ratios2K...), ratio2192K)
	grok := append(cloneSizes(grok1K), grok2K...)

	model := func(id, label, family string, enabled, verified bool, fields []string, sizes []SizeCapability) ModelCapability {
		return ModelCapability{
			ID:             id,
			Label:          label,
			Family:         family,
			Enabled:        enabled,
			Verified:       verified,
			RequestFields:  append([]string(nil), fields...),
			SupportedSizes: cloneSizes(sizes),
		}
	}
	models := []ModelCapability{
		model("a2e", "A2E", "a2e", true, true, []string{"size", "aspect_ratio"}, append(cloneSizes(a2e1080), withoutResolution(a2e2K)...)),
		model("grok-cli-oauth:grok-imagine-image", "Grok Imagine Image", "grok-imagine", true, true, []string{"size", "aspect_ratio", "resolution"}, grok),
		model("grok-cli-oauth:grok-imagine-image-quality", "Grok Imagine Image Quality", "grok-imagine", true, true, []string{"size", "aspect_ratio", "resolution"}, grok),
		model("openai-codex-oauth:gpt-image-2", "GPT Image 2", "gpt-image-2", true, true, []string{"size"}, gptImage2),
		model("a2e:seedream", "Seedream", "a2e-seedream", true, true, []string{"size", "aspect_ratio"}, append(withoutResolution(a2e2K), seedream4K...)),
		model("a2e:qwen-image-2.0", "Qwen Image 2.0", "a2e-qwen", true, true, []string{"size"}, qwen1K),
		model("a2e:qwen-image-3.0", "Qwen Image 3.0", "a2e-qwen", true, true, []string{"size"}, qwen1K),
		model("a2e:qwen-image-2.0-pro", "Qwen Image 2.0 Pro", "a2e-qwen", true, true, []string{"size"}, qwen1K),
		model("a2e:kling-image-3.0", "Kling Image 3.0", "a2e-kling", true, true, []string{"size", "aspect_ratio", "resolution"}, kling),
		model("a2e:flux-2-pro", "Flux 2 Pro", "a2e-flux", true, true, []string{"size", "aspect_ratio", "resolution"}, flux),
		model("a2e:wan2.7-image", "Wan 2.7 Image", "a2e-wan", true, true, []string{"size", "aspect_ratio"}, wanRatios),
		model("a2e:wan2.7-image-pro", "Wan 2.7 Image Pro", "a2e-wan", true, true, []string{"size", "aspect_ratio"}, wanRatios),
		model("a2e:wan2.6-image", "Wan 2.6 Image", "a2e-wan", false, false, []string{"size", "aspect_ratio"}, wanRatios),
		model("a2e:qwen-image-3.0-pro", "Qwen Image 3.0 Pro", "a2e-qwen", false, false, []string{"size"}, qwen3Pro),
		model("grok-cli-oauth:grok-imagine-image-pro", "Grok Imagine Image Pro", "grok-imagine", false, false, []string{"size", "aspect_ratio", "resolution"}, grok),
	}
	models[len(models)-1].AliasFor = "grok-cli-oauth:grok-imagine-image-quality"
	return models
}

func preset(value, label, aspectRatio, resolution string) SizeCapability {
	return SizeCapability{Value: value, Label: label, AspectRatio: aspectRatio, Resolution: resolution}
}

func cloneSizes(values []SizeCapability) []SizeCapability {
	return append([]SizeCapability(nil), values...)
}

func cloneModelCapability(model ModelCapability) ModelCapability {
	model.RequestFields = append([]string(nil), model.RequestFields...)
	model.SupportedSizes = cloneSizes(model.SupportedSizes)
	return model
}
