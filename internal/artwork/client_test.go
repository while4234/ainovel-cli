package artwork

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestArtworkGatewayClientSubmitsExactlyOnceWithDerivedFields(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/generations" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("authorization"); got != "Bearer gateway-test-key" {
			t.Errorf("authorization = %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		want := map[string]any{
			"model":           "a2e:kling-image-3.0",
			"prompt":          "paint a quiet harbor",
			"n":               float64(1),
			"response_format": "b64_json",
			"size":            "1536x2048",
			"aspect_ratio":    "3:4",
			"resolution":      "2k",
		}
		if !mapsEqual(payload, want) {
			t.Errorf("payload = %#v, want %#v", payload, want)
		}
		writeTestGatewayJSON(t, w, map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString([]byte("image"))}},
		})
	}))
	defer server.Close()

	client, err := NewGatewayClient(ImageGatewayConfig{
		BaseURL: server.URL + "/v1/images/generations",
		APIKey:  "gateway-test-key",
	}, nil)
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	image, err := client.Generate(context.Background(), GenerateRequest{
		Model:  "a2e:kling-image-3.0",
		Prompt: "paint a quiet harbor",
		Size:   "1536x2048",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if string(image.Content) != "image" || calls.Load() != 1 {
		t.Fatalf("result = %q, calls = %d", image.Content, calls.Load())
	}
}

func TestArtworkGatewayClientTransportInterruptionIsUncertainAndNeverRetried(t *testing.T) {
	var calls atomic.Int32
	client, err := NewGatewayClient(ImageGatewayConfig{
		BaseURL: "https://gateway.invalid",
		APIKey:  "not-a-real-key",
	}, roundTripDoer(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, io.ErrUnexpectedEOF
	}))
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	_, err = client.Generate(context.Background(), GenerateRequest{
		Model: "a2e:qwen-image-2.0", Prompt: "test", Size: "1024x1024",
	})
	if !IsDeliveryUncertain(err) {
		t.Fatalf("error = %#v, want uncertain delivery", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("transport calls = %d, want exactly 1", calls.Load())
	}
}

func TestArtworkGatewayClientVerifyOnlyGetsModels(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("verification request = %s %s", r.Method, r.URL.Path)
		}
		if strings.Contains(r.URL.Path, "images/generations") {
			t.Error("verification called the generation endpoint")
		}
		if r.Header.Get("authorization") != "Bearer verify-key" {
			t.Error("verification request is not authenticated")
		}
		writeTestGatewayJSON(t, w, map[string]any{"data": []map[string]string{{"id": "a2e"}, {"id": "gpt-image-2"}}})
	}))
	defer server.Close()
	client, err := NewGatewayClient(ImageGatewayConfig{BaseURL: server.URL + "/v1", APIKey: "verify-key"}, nil)
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	result, err := client.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if result.ModelCount != 2 || calls.Load() != 1 {
		t.Fatalf("result = %+v calls=%d", result, calls.Load())
	}
}

func TestArtworkGatewayClientEnforcesResponseLimitAndSafeHTTPError(t *testing.T) {
	client, err := NewGatewayClient(ImageGatewayConfig{BaseURL: "https://gateway.invalid", APIKey: "top-secret-key"}, roundTripDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusBadGateway,
			Body:          io.NopCloser(strings.NewReader(`{"error":"top-secret-upstream-body"}`)),
			Header:        make(http.Header),
			ContentLength: int64(len(`{"error":"top-secret-upstream-body"}`)),
		}, nil
	}))
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	_, err = client.Generate(context.Background(), GenerateRequest{Model: "a2e", Prompt: "test", Size: "1080x1080"})
	if err == nil || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("unsafe HTTP error = %v", err)
	}

	client, err = NewGatewayClient(ImageGatewayConfig{BaseURL: "https://gateway.invalid", APIKey: "key"}, roundTripDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(strings.NewReader("{}")),
			Header:        make(http.Header),
			ContentLength: maxGenerationJSONBytes + 1,
		}, nil
	}))
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	_, err = client.Generate(context.Background(), GenerateRequest{Model: "a2e", Prompt: "test", Size: "1080x1080"})
	var gatewayErr *GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.Code != "gateway_response_too_large" {
		t.Fatalf("oversize error = %#v", err)
	}
}

type roundTripDoer func(*http.Request) (*http.Response, error)

func (do roundTripDoer) Do(request *http.Request) (*http.Response, error) { return do(request) }

func writeTestGatewayJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("content-type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func mapsEqual(got, want map[string]any) bool {
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	return string(gotJSON) == string(wantJSON)
}
