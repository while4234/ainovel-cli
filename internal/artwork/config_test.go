package artwork

import "testing"

func TestArtworkImageGatewayConfigNormalizesRootVersionAndFullEndpoints(t *testing.T) {
	tests := map[string]string{
		"https://gateway.example":                              "https://gateway.example",
		"HTTPS://gateway.example/v1":                           "https://gateway.example",
		"https://gateway.example/":                             "https://gateway.example",
		"https://gateway.example/v1":                           "https://gateway.example",
		"https://gateway.example/v1/models":                    "https://gateway.example",
		"https://gateway.example/v1/images/generations":        "https://gateway.example",
		"https://gateway.example/proxy/v1/images/generations/": "https://gateway.example/proxy",
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			got, err := (ImageGatewayConfig{BaseURL: input}).Normalized()
			if err != nil {
				t.Fatalf("Normalized: %v", err)
			}
			if got.BaseURL != want {
				t.Fatalf("base URL = %q, want %q", got.BaseURL, want)
			}
			if got.DefaultModel != DefaultModelID || got.RequestTimeoutSeconds != DefaultGatewayRequestTimeoutSeconds {
				t.Fatalf("defaults = %+v", got)
			}
		})
	}
	config, err := (ImageGatewayConfig{DefaultModel: "A2E:QWEN-IMAGE-2.0"}).Normalized()
	if err != nil || config.DefaultModel != "a2e:qwen-image-2.0" {
		t.Fatalf("default model normalization = %+v, err=%v", config, err)
	}
}

func TestArtworkImageGatewayConfigRejectsUnsafeURLs(t *testing.T) {
	for _, input := range []string{
		"gateway.example/v1",
		"ftp://gateway.example",
		"https://user:pass@gateway.example",
		"https://gateway.example/v1?token=secret",
		"https://gateway.example/v1#fragment",
		"https://gateway.example/%2e%2e/v1",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := (ImageGatewayConfig{BaseURL: input}).Normalized(); err == nil {
				t.Fatalf("unsafe URL %q was accepted", input)
			}
		})
	}
	if _, err := (ImageGatewayConfig{APIKey: "unsafe\nkey"}).Normalized(); err == nil {
		t.Fatal("API key with control characters was accepted")
	}
}

func TestArtworkImageGatewayConfigBoundsRequestTimeout(t *testing.T) {
	for _, seconds := range []int{-1, MaxGatewayRequestTimeoutSeconds + 1} {
		if _, err := (ImageGatewayConfig{RequestTimeoutSeconds: seconds}).Normalized(); err == nil {
			t.Fatalf("timeout %d was accepted", seconds)
		}
	}
	for _, seconds := range []int{0, MinGatewayRequestTimeoutSeconds, MaxGatewayRequestTimeoutSeconds} {
		if _, err := (ImageGatewayConfig{RequestTimeoutSeconds: seconds}).Normalized(); err != nil {
			t.Fatalf("timeout %d was rejected: %v", seconds, err)
		}
	}
}
