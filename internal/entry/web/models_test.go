package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
)

func TestProjectModelAddExistingProviderUsesEmptyConfig(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Existing Model Add")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/models/add", bytes.NewBufferString(`{"role":"writer","provider":"openrouter","model":"new-model"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("model add status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.addProviderRole != "writer" || fake.addProviderName != "openrouter" || fake.addProviderModel != "new-model" {
		t.Fatalf("add model args role=%q provider=%q model=%q", fake.addProviderRole, fake.addProviderName, fake.addProviderModel)
	}
	if fake.addProviderConfig.Type != "" || fake.addProviderConfig.APIKey != "" || len(fake.addProviderConfig.Models) != 0 {
		t.Fatalf("existing provider should use empty config: %+v", fake.addProviderConfig)
	}
}

func TestProjectModelAddPresetPassesProviderConfig(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Preset Model Add")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/models/add", bytes.NewBufferString(`{"role":"default","provider":"anthropic","type":"anthropic","api_key":"sk-test","model":"claude-sonnet-4-5"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("model add status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.addProviderName != "anthropic" || fake.addProviderConfig.Type != "anthropic" || fake.addProviderConfig.APIKey != "sk-test" {
		t.Fatalf("preset provider config = %+v provider=%q", fake.addProviderConfig, fake.addProviderName)
	}
	if len(fake.addProviderConfig.Models) != 1 || fake.addProviderConfig.Models[0] != "claude-sonnet-4-5" {
		t.Fatalf("preset model list = %+v", fake.addProviderConfig.Models)
	}
}
