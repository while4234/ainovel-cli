package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/artwork"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestArtworkConfigNeverReturnsAPIKeyAndSupportsPreserveAndClear(t *testing.T) {
	var modelCalls atomic.Int32
	var generationCalls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			modelCalls.Add(1)
			if r.Method != http.MethodGet || r.Header.Get("authorization") != "Bearer gateway-secret" {
				t.Errorf("verify request = %s auth=%q", r.Method, r.Header.Get("authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "a2e"}}})
		case "/v1/images/generations":
			generationCalls.Add(1)
			http.Error(w, "must not be called", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()

	cfg := testWebConfig(t)
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	handler := server.Handler()

	putBody := `{"base_url":` + quoteJSON(gateway.URL+"/v1/images/generations") + `,"api_key":"gateway-secret","default_model":"a2e:kling-image-3.0","request_timeout_seconds":45}`
	put := performArtworkRequest(t, handler, http.MethodPut, "/api/artwork/config", putBody)
	assertArtworkResponseHasNoSecret(t, put.Body.String(), "gateway-secret")
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", put.Code, put.Body.String())
	}
	var savedResponse struct {
		Config artworkGatewayConfigResponse `json:"config"`
	}
	decodeArtworkResponse(t, put, &savedResponse)
	if savedResponse.Config.BaseURL != gateway.URL || !savedResponse.Config.HasAPIKey || savedResponse.Config.DefaultModel != "a2e:kling-image-3.0" || savedResponse.Config.RequestTimeoutSeconds != 45 {
		t.Fatalf("saved public config = %+v", savedResponse.Config)
	}

	preserve := performArtworkRequest(t, handler, http.MethodPut, "/api/artwork/config", `{"request_timeout_seconds":60}`)
	assertArtworkResponseHasNoSecret(t, preserve.Body.String(), "gateway-secret")
	if preserve.Code != http.StatusOK || server.currentConfig().ImageGateway.APIKey != "gateway-secret" {
		t.Fatalf("omitted key was not preserved: status=%d config=%+v", preserve.Code, server.currentConfig().ImageGateway)
	}

	verify := performArtworkRequest(t, handler, http.MethodPost, "/api/artwork/config/verify", "")
	assertArtworkResponseHasNoSecret(t, verify.Body.String(), "gateway-secret")
	if verify.Code != http.StatusOK || modelCalls.Load() != 1 || generationCalls.Load() != 0 {
		t.Fatalf("verify status=%d models=%d generations=%d body=%s", verify.Code, modelCalls.Load(), generationCalls.Load(), verify.Body.String())
	}

	clear := performArtworkRequest(t, handler, http.MethodPut, "/api/artwork/config", `{"clear_api_key":true}`)
	assertArtworkResponseHasNoSecret(t, clear.Body.String(), "gateway-secret")
	if clear.Code != http.StatusOK || server.currentConfig().ImageGateway.APIKey != "" {
		t.Fatalf("clear status=%d config=%+v", clear.Code, server.currentConfig().ImageGateway)
	}
	var clearedResponse struct {
		Config artworkGatewayConfigResponse `json:"config"`
	}
	decodeArtworkResponse(t, clear, &clearedResponse)
	if clearedResponse.Config.HasAPIKey {
		t.Fatal("cleared config still reports an API key")
	}

	saved, err := bootstrap.LoadConfigFile(cfg.PersistPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if saved.ImageGateway == nil || saved.ImageGateway.APIKey != "" || saved.ImageGateway.BaseURL != gateway.URL {
		t.Fatalf("saved gateway after clear = %+v", saved.ImageGateway)
	}
}

func TestArtworkConfigVerifyAcceptsUnsavedPatchWithoutPersisting(t *testing.T) {
	var calls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/models" || r.Method != http.MethodGet || r.Header.Get("authorization") != "Bearer temporary-key" {
			t.Errorf("request = %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer gateway.Close()

	cfg := testWebConfig(t)
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	body := `{"base_url":` + quoteJSON(gateway.URL+"/v1") + `,"api_key":"temporary-key"}`
	response := performArtworkRequest(t, server.Handler(), http.MethodPost, "/api/artwork/config/verify", body)
	assertArtworkResponseHasNoSecret(t, response.Body.String(), "temporary-key")
	if response.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("verify status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
	if server.currentConfig().ImageGateway != nil {
		t.Fatalf("verify persisted temporary config: %+v", server.currentConfig().ImageGateway)
	}
}

func TestArtworkModelsPublishesVersionedRegistry(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	response := performArtworkRequest(t, server.Handler(), http.MethodGet, "/api/artwork/models", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var registry artwork.CapabilityRegistry
	decodeArtworkResponse(t, response, &registry)
	if registry.Version != artwork.CapabilityRegistryVersion || len(registry.Models) != 15 {
		t.Fatalf("registry = version %q models %d", registry.Version, len(registry.Models))
	}
	enabled := 0
	for _, model := range registry.Models {
		if model.Enabled {
			enabled++
		}
	}
	if enabled != 12 {
		t.Fatalf("enabled models = %d, want 12", enabled)
	}
}

func TestArtworkProjectOverlayDoesNotPersistGlobalGateway(t *testing.T) {
	base := testWebConfig(t)
	base.ImageGateway = &artwork.ImageGatewayConfig{
		BaseURL:      "https://gateway.example",
		APIKey:       "global-artwork-secret",
		DefaultModel: "a2e",
	}
	base.Providers["openai"] = bootstrap.ProviderConfig{
		Type: "openai", APIKey: "llm-secret", Models: []string{"gpt-test", "writer-model"},
	}
	store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	manifest, err := store.CreateProject("Artwork Overlay")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	host, err := store.OpenProjectHost(base, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("OpenProjectHost: %v", err)
	}
	defer host.Close()
	if err := host.SwitchModel("writer", "openai", "writer-model"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	data, err := os.ReadFile(ProjectConfigPath(manifest))
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	text := string(data)
	for _, forbidden := range []string{"global-artwork-secret", "image_gateway"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("project overlay leaked %q: %s", forbidden, text)
		}
	}
}

func performArtworkRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeArtworkResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(bytes.NewReader(response.Body.Bytes())).Decode(target); err != nil {
		t.Fatalf("decode response: %v body=%s", err, response.Body.String())
	}
}

func assertArtworkResponseHasNoSecret(t *testing.T, body, secret string) {
	t.Helper()
	if strings.Contains(body, secret) || strings.Contains(body, `"api_key"`) {
		t.Fatalf("JSON response leaked API key material: %s", body)
	}
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
