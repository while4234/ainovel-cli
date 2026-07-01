package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
)

func TestHandlerCreatesProjectsUnderRuntimeRoot(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	handler := NewHandler(bootstrap.Config{}, assets.Bundle{}, runtimeRoot)

	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":"Web Novel"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var manifest ProjectManifest
	if err := json.NewDecoder(rec.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if !strings.HasPrefix(filepath.Clean(manifest.RootDir), filepath.Clean(runtimeRoot)) {
		t.Fatalf("project root %q should be under runtime root %q", manifest.RootDir, runtimeRoot)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), manifest.ID) {
		t.Fatalf("project list should include %q: %s", manifest.ID, rec.Body.String())
	}
}

func TestModelConfigResponseRedactsProviderSecrets(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	handler := NewHandler(testWebConfig(t), assets.Load("default"), runtimeRoot)

	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":"Secret Safety"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var manifest ProjectManifest
	if err := json.NewDecoder(rec.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/models", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("models status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "sk-test") || strings.Contains(body, "api_key") {
		t.Fatalf("model config response exposed provider secret: %s", body)
	}
	if !strings.Contains(body, `"providers"`) || !strings.Contains(body, `"roles"`) {
		t.Fatalf("model config response missing expected sections: %s", body)
	}
}

func TestBackendManualTestEndpointIsNoTokenCall(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	handler := NewHandler(testWebConfig(t), assets.Load("default"), runtimeRoot)

	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":"Backend Test"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var manifest ProjectManifest
	if err := json.NewDecoder(rec.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/backend/test", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("backend test status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Backend apiBackendStatus `json:"backend"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode backend test: %v", err)
	}
	if body.Backend.ManualTest == nil {
		t.Fatalf("manual_test missing from backend response: %+v", body.Backend)
	}
	if !body.Backend.ManualTest.NoTokenCall {
		t.Fatalf("manual_test.no_token_call = false, want true: %+v", body.Backend.ManualTest)
	}
}

func TestAPIUsageFromSnapshotProjectsCacheFields(t *testing.T) {
	summary := apiUsageFromSnapshot(host.UISnapshot{
		TotalInputTokens:       100,
		TotalOutputTokens:      40,
		TotalCacheReadTokens:   70,
		TotalCacheWriteTokens:  15,
		TotalCostUSD:           0.0123,
		TotalSavedUSD:          0.0456,
		OverallCacheCapable:    true,
		OverallRecentCacheRead: 30,
		OverallRecentInput:     50,
		OverallRecentSamples:   2,
		MissingAssistantUsage:  1,
		CachePerAgent: []host.AgentCacheStat{{
			Role:      "writer",
			Input:     100,
			CacheRead: 70,
		}},
		CachePerModel: []host.AgentCacheStat{{
			Model:     "gpt-test",
			Input:     100,
			CacheRead: 70,
		}},
	})
	if summary.Overall.InputTokens != 100 || summary.Overall.CacheReadTokens != 70 || !summary.Overall.CacheCapable {
		t.Fatalf("overall usage projection lost cache fields: %+v", summary.Overall)
	}
	if len(summary.ByRole) != 1 || summary.ByRole[0].Role != "writer" {
		t.Fatalf("by-role usage projection = %+v", summary.ByRole)
	}
	if len(summary.ByModel) != 1 || summary.ByModel[0].Model != "gpt-test" {
		t.Fatalf("by-model usage projection = %+v", summary.ByModel)
	}
	if summary.MissingAssistantUsage != 1 {
		t.Fatalf("missing usage = %d, want 1", summary.MissingAssistantUsage)
	}
}
