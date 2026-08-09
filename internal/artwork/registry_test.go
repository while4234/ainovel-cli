package artwork

import (
	"reflect"
	"testing"
)

func TestArtworkRegistryPublishesVerifiedBaselineAndDisabledDiscoveryModels(t *testing.T) {
	registry := Registry()
	if registry.Version != CapabilityRegistryVersion {
		t.Fatalf("registry version = %q", registry.Version)
	}
	var enabled, disabled []string
	for _, model := range registry.Models {
		if model.Enabled {
			if !model.Verified {
				t.Fatalf("enabled model %q is not verified", model.ID)
			}
			enabled = append(enabled, model.ID)
		} else {
			if model.Verified {
				t.Fatalf("disabled discovery model %q unexpectedly verified", model.ID)
			}
			disabled = append(disabled, model.ID)
		}
	}
	wantEnabled := []string{
		"a2e",
		"grok-cli-oauth:grok-imagine-image",
		"grok-cli-oauth:grok-imagine-image-quality",
		"openai-codex-oauth:gpt-image-2",
		"a2e:seedream",
		"a2e:qwen-image-2.0",
		"a2e:qwen-image-3.0",
		"a2e:qwen-image-2.0-pro",
		"a2e:kling-image-3.0",
		"a2e:flux-2-pro",
		"a2e:wan2.7-image",
		"a2e:wan2.7-image-pro",
	}
	wantDisabled := []string{
		"a2e:wan2.6-image",
		"a2e:qwen-image-3.0-pro",
		"grok-cli-oauth:grok-imagine-image-pro",
	}
	if !reflect.DeepEqual(enabled, wantEnabled) {
		t.Fatalf("enabled models = %#v, want %#v", enabled, wantEnabled)
	}
	if !reflect.DeepEqual(disabled, wantDisabled) {
		t.Fatalf("disabled models = %#v, want %#v", disabled, wantDisabled)
	}
}

func TestArtworkRegistryMatchesStarWriterFamilyCapabilityCounts(t *testing.T) {
	wantCounts := map[string]int{
		"a2e":                                       16,
		"a2e:seedream":                              16,
		"a2e:wan2.6-image":                          8,
		"a2e:wan2.7-image":                          8,
		"a2e:wan2.7-image-pro":                      8,
		"a2e:qwen-image-2.0":                        8,
		"a2e:qwen-image-2.0-pro":                    8,
		"a2e:qwen-image-3.0":                        8,
		"a2e:qwen-image-3.0-pro":                    15,
		"a2e:kling-image-3.0":                       16,
		"a2e:flux-2-pro":                            14,
		"openai-codex-oauth:gpt-image-2":            10,
		"grok-cli-oauth:grok-imagine-image":         18,
		"grok-cli-oauth:grok-imagine-image-quality": 18,
		"grok-cli-oauth:grok-imagine-image-pro":     18,
	}
	for modelID, want := range wantCounts {
		model, ok := LookupModel(modelID)
		if !ok {
			t.Fatalf("model %q missing", modelID)
		}
		if got := len(model.SupportedSizes); got != want {
			t.Errorf("%s size count = %d, want %d", modelID, got, want)
		}
	}
}

func TestArtworkRegistryDerivesOnlyFamilySupportedFields(t *testing.T) {
	tests := []struct {
		model string
		size  string
		want  ImageRequestOptions
	}{
		{"a2e", "2688x1152", ImageRequestOptions{Model: "a2e", Size: "2688x1152", AspectRatio: "21:9"}},
		{"a2e:seedream", "4096x2304", ImageRequestOptions{Model: "a2e:seedream", Size: "4096x2304", AspectRatio: "16:9"}},
		{"a2e:wan2.7-image-pro", "1512x648", ImageRequestOptions{Model: "a2e:wan2.7-image-pro", Size: "1512x648", AspectRatio: "21:9"}},
		{"a2e:qwen-image-2.0-pro", "1024x1024", ImageRequestOptions{Model: "a2e:qwen-image-2.0-pro", Size: "1024x1024"}},
		{"a2e:qwen-image-3.0-pro", "1536x2048", ImageRequestOptions{Model: "a2e:qwen-image-3.0-pro", Size: "1536x2048"}},
		{"a2e:kling-image-3.0", "1536x2048", ImageRequestOptions{Model: "a2e:kling-image-3.0", Size: "1536x2048", AspectRatio: "3:4", Resolution: "2k"}},
		{"a2e:flux-2-pro", "720x1280", ImageRequestOptions{Model: "a2e:flux-2-pro", Size: "720x1280", AspectRatio: "9:16", Resolution: "1k"}},
		{"openai-codex-oauth:gpt-image-2", "3840x2160", ImageRequestOptions{Model: "openai-codex-oauth:gpt-image-2", Size: "3840x2160"}},
		{"grok-cli-oauth:grok-imagine-image-quality", "768x1024", ImageRequestOptions{Model: "grok-cli-oauth:grok-imagine-image-quality", Size: "768x1024", AspectRatio: "3:4", Resolution: "1k"}},
		{"grok-cli-oauth:grok-imagine-image-pro", "2048x1024", ImageRequestOptions{Model: "grok-cli-oauth:grok-imagine-image-quality", Size: "2048x1024", AspectRatio: "2:1", Resolution: "2k"}},
	}
	for _, test := range tests {
		t.Run(test.model+"/"+test.size, func(t *testing.T) {
			got, err := ResolveImageRequest(test.model, test.size)
			if err != nil {
				t.Fatalf("ResolveImageRequest: %v", err)
			}
			if got != test.want {
				t.Fatalf("request options = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestArtworkRegistryKeepsQwen20ProSeparateFromQwen30Pro(t *testing.T) {
	qwen20, _ := LookupModel("a2e:qwen-image-2.0-pro")
	if qwen20.Label != "Qwen Image 2.0 Pro" {
		t.Fatalf("Qwen 2.0 Pro label = %q", qwen20.Label)
	}
	if _, err := ResolveImageRequest(qwen20.ID, "1536x2048"); err == nil {
		t.Fatal("Qwen 2.0 Pro unexpectedly accepted a 2K size")
	}
	if _, err := ResolveImageRequest("a2e:qwen-image-3.0-pro", "1536x2048"); err != nil {
		t.Fatalf("Qwen 3.0 Pro should accept 2K: %v", err)
	}
}
