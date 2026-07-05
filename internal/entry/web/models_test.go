package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/grokauth"
	hostpkg "github.com/voocel/ainovel-cli/internal/host"
)

func TestGlobalModelsAndDefaultSwitch(t *testing.T) {
	home := testTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cfg := testWebConfig(t)
	cfg.Providers["openai"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-test",
		Models: []string{"gpt-test", "gpt-next"},
	}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var listed struct {
		Models apiModelConfig `json:"models"`
	}
	serveJSON(t, server.Handler(), http.MethodGet, "/api/models", "", &listed)
	if len(listed.Models.Providers) != 1 || listed.Models.Providers[0].Name != "openai" {
		t.Fatalf("global providers = %+v", listed.Models.Providers)
	}
	if listed.Models.Roles[0].Provider != "openai" || listed.Models.Roles[0].Model != "gpt-test" {
		t.Fatalf("global default route = %+v", listed.Models.Roles[0])
	}

	var switched struct {
		Models  apiModelConfig `json:"models"`
		Runtime struct {
			Config struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			} `json:"config"`
		} `json:"runtime"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/default", `{"provider":"openai","model":"gpt-next"}`, &switched)
	if switched.Runtime.Config.Provider != "openai" || switched.Runtime.Config.Model != "gpt-next" {
		t.Fatalf("runtime default = %+v", switched.Runtime.Config)
	}
	if got := server.currentConfig().ModelName; got != "gpt-next" {
		t.Fatalf("server default model = %q, want gpt-next", got)
	}
	for _, role := range []string{"coordinator", "architect", "writer", "editor"} {
		if route := findModelRoute(switched.Models.Roles, role); route.Provider != "openai" || route.Model != "gpt-next" {
			t.Fatalf("%s route after default switch = %+v", role, route)
		}
	}

	saved, err := bootstrap.LoadConfigFile(filepath.Join(home, ".ainovel", "config.json"))
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if saved.Provider != "openai" || saved.ModelName != "gpt-next" {
		t.Fatalf("saved default = %s/%s", saved.Provider, saved.ModelName)
	}
	for _, role := range []string{"coordinator", "architect", "writer", "editor"} {
		if rc := saved.Roles[role]; rc.Provider != "openai" || rc.Model != "gpt-next" {
			t.Fatalf("saved %s route = %+v", role, rc)
		}
	}

	manifest, err := server.store.CreateProject("Global Default")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	session, _, err := server.sessions.Open(manifest.ID)
	if err != nil {
		t.Fatalf("Open session: %v", err)
	}
	if snap := session.Snapshot(); snap.ModelName != "gpt-next" {
		t.Fatalf("new project model = %q, want gpt-next", snap.ModelName)
	}
	if _, err := os.Stat(ProjectConfigPath(manifest)); !os.IsNotExist(err) {
		t.Fatalf("new project should inherit global default without project overlay, stat err=%v", err)
	}
}

func TestGlobalModelSwitchRoutePersistsRole(t *testing.T) {
	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	cfg.Providers["deepseek"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-test",
		Models: []string{"deepseek-chat"},
	}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var switched struct {
		Models apiModelConfig `json:"models"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/switch", `{"role":"writer","provider":"deepseek","model":"deepseek-chat"}`, &switched)
	if route := findModelRoute(switched.Models.Roles, "writer"); route.Provider != "deepseek" || route.Model != "deepseek-chat" || !route.Explicit {
		t.Fatalf("writer route = %+v", route)
	}
	saved, err := bootstrap.LoadConfigFile(cfg.PersistPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if saved.Roles["writer"].Provider != "deepseek" || saved.Roles["writer"].Model != "deepseek-chat" {
		t.Fatalf("saved writer role = %+v", saved.Roles["writer"])
	}
}

func TestGlobalCoCreateTimeoutPersists(t *testing.T) {
	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var response struct {
		Models  apiModelConfig `json:"models"`
		Runtime struct {
			Config struct {
				CoCreateTimeoutSeconds int `json:"cocreate_timeout_seconds"`
			} `json:"config"`
		} `json:"runtime"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/cocreate-timeout", `{"seconds":45}`, &response)
	if response.Models.CoCreateTimeoutSeconds != 45 || response.Runtime.Config.CoCreateTimeoutSeconds != 45 {
		t.Fatalf("timeout response models=%d runtime=%d", response.Models.CoCreateTimeoutSeconds, response.Runtime.Config.CoCreateTimeoutSeconds)
	}
	if got := server.currentConfig().CoCreateTimeoutSeconds; got != 45 {
		t.Fatalf("server timeout = %d, want 45", got)
	}
	saved, err := bootstrap.LoadConfigFile(cfg.PersistPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if saved.CoCreateTimeoutSeconds != 45 {
		t.Fatalf("saved timeout = %d, want 45", saved.CoCreateTimeoutSeconds)
	}
}

func TestProjectCoCreateTimeoutUsesProjectHost(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Project Timeout")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	var response struct {
		Models apiModelConfig `json:"models"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/projects/"+manifest.ID+"/models/cocreate-timeout", `{"seconds":30}`, &response)
	if fake.setCoCreateTimeoutCalls != 1 || fake.coCreateTimeoutSeconds != 30 {
		t.Fatalf("host timeout calls=%d seconds=%d", fake.setCoCreateTimeoutCalls, fake.coCreateTimeoutSeconds)
	}
	if response.Models.CoCreateTimeoutSeconds != 30 {
		t.Fatalf("response timeout = %d, want 30", response.Models.CoCreateTimeoutSeconds)
	}
}

func TestProjectModelDeleteUsesProjectHost(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Project Model Delete")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	var response struct {
		Models apiModelConfig `json:"models"`
	}
	serveJSON(t, server.Handler(), http.MethodDelete, "/api/projects/"+manifest.ID+"/models", `{"provider":"openrouter","model":"model-b"}`, &response)
	if fake.removeProviderCalls != 1 || fake.removeProviderName != "openrouter" || fake.removeProviderModel != "model-b" {
		t.Fatalf("remove model args calls=%d provider=%q model=%q", fake.removeProviderCalls, fake.removeProviderName, fake.removeProviderModel)
	}
	if response.Models.Roles[0].Provider != "openrouter" {
		t.Fatalf("models response = %+v", response.Models)
	}
}

func TestGlobalModelAddGrokOAuthProvider(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return nil
	})
	t.Cleanup(restore)

	home := testTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var added struct {
		Models  apiModelConfig `json:"models"`
		Runtime struct {
			Config struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			} `json:"config"`
		} `json:"runtime"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/add", `{"role":"default","provider":"grok-oauth","model":"grok-4.3-latest","type":"grok","auth":"grok_oauth","account_id":"default","api":"chat","api_key":"should-not-save"}`, &added)
	if added.Runtime.Config.Provider != "grok-oauth" || added.Runtime.Config.Model != "grok-4.3-latest" {
		t.Fatalf("runtime default = %+v", added.Runtime.Config)
	}
	if !modelConfigHasProvider(added.Models, "grok-oauth", "grok-4.3-latest") {
		t.Fatalf("models missing grok provider: %+v", added.Models.Providers)
	}
	cfg := server.currentConfig()
	pc := cfg.Providers["grok-oauth"]
	if pc.Type != "grok" || pc.Auth != bootstrap.ProviderAuthGrokOAuth || pc.AccountID != "default" {
		t.Fatalf("grok provider config = %+v", pc)
	}
	if pc.API != "" || pc.APIKey != "" {
		t.Fatalf("grok_oauth should not persist OpenAI api or api_key fields: %+v", pc)
	}

	saved, err := bootstrap.LoadConfigFile(filepath.Join(home, ".ainovel", "config.json"))
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if saved.Provider != "grok-oauth" || saved.ModelName != "grok-4.3-latest" {
		t.Fatalf("saved default = %s/%s", saved.Provider, saved.ModelName)
	}
}

func TestGlobalModelAddCanSaveWithoutSwitchingDefault(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return nil
	})
	t.Cleanup(restore)

	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var added struct {
		Models  apiModelConfig `json:"models"`
		Runtime struct {
			Config struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			} `json:"config"`
		} `json:"runtime"`
	}
	body := `{"select_after_save":false,"provider":"deepseek2","model":"deepseek-v4-pro","label":"DeepSeek Relay","type":"openai","api":"chat","api_key":"sk-test","base_url":"https://api.example/v1"}`
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/add", body, &added)
	if !modelConfigHasProvider(added.Models, "deepseek2", "deepseek-v4-pro") {
		t.Fatalf("models missing saved provider: %+v", added.Models.Providers)
	}
	if added.Runtime.Config.Provider != cfg.Provider || added.Runtime.Config.Model != cfg.ModelName {
		t.Fatalf("runtime default changed to %+v, want %s/%s", added.Runtime.Config, cfg.Provider, cfg.ModelName)
	}
	if server.currentConfig().Provider != cfg.Provider || server.currentConfig().ModelName != cfg.ModelName {
		t.Fatalf("server default changed to %s/%s", server.currentConfig().Provider, server.currentConfig().ModelName)
	}
}

func TestGlobalModelEditRenamesProviderAndPreservesBlankAPIKey(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return nil
	})
	t.Cleanup(restore)

	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	cfg.Providers["custom-openai"] = bootstrap.ProviderConfig{
		Label:   "Wrong",
		Type:    "openai",
		API:     "chat",
		APIKey:  "sk-secret",
		BaseURL: "https://old.example/v1",
		Models:  []string{"old-model"},
	}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var edited struct {
		Models apiModelConfig `json:"models"`
	}
	body := `{"role":"default","original_provider":"custom-openai","provider":"fixed-openai","model":"new-model","label":"Fixed","type":"openai","api":"responses","base_url":"https://new.example/v1","network_disconnect_max_attempts":4,"auto_switch_candidate_pool":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/models/add", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("model edit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-secret") {
		t.Fatalf("model edit response leaked api key: %s", rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &edited); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	provider := findModelProvider(edited.Models.Providers, "fixed-openai")
	if provider.Name == "" || !provider.APIKeyConfigured || provider.NetworkDisconnectMaxAttempts != 4 || !provider.AutoSwitchCandidatePool {
		t.Fatalf("edited provider response = %+v", provider)
	}
	if route := findModelRoute(edited.Models.Roles, "writer"); route.Provider != "fixed-openai" || route.Model != "new-model" {
		t.Fatalf("writer route after default edit = %+v", route)
	}
	next := server.currentConfig()
	if _, ok := next.Providers["custom-openai"]; ok {
		t.Fatal("old provider key still configured")
	}
	pc := next.Providers["fixed-openai"]
	if pc.APIKey != "sk-secret" || pc.Label != "Fixed" || pc.API != "responses" || pc.BaseURL != "https://new.example/v1" {
		t.Fatalf("edited provider config = %+v", pc)
	}
	if next.ModelAutoSwitch.EffectiveNetworkMaxAttempts() != 4 || !modelAutoSwitchHasProvider(next.ModelAutoSwitch, "fixed-openai") {
		t.Fatalf("auto switch config = %+v", next.ModelAutoSwitch)
	}
	saved, err := bootstrap.LoadConfigFile(cfg.PersistPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if saved.Providers["fixed-openai"].APIKey != "sk-secret" {
		t.Fatalf("saved provider = %+v", saved.Providers["fixed-openai"])
	}
}

func TestGlobalModelEditRefreshesProjectProviderReferences(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return nil
	})
	t.Cleanup(restore)

	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	cfg.Providers["custom-openai"] = bootstrap.ProviderConfig{
		Label:  "Old Label",
		Type:   "openai",
		API:    "chat",
		APIKey: "sk-secret",
		Models: []string{"old-model", "new-model"},
	}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	closedProject, err := server.store.CreateProject("Closed Project")
	if err != nil {
		t.Fatalf("CreateProject closed: %v", err)
	}
	writeProjectModelOverlay(t, closedProject, bootstrap.Config{
		Provider:  "custom-openai",
		ModelName: "old-model",
		Providers: map[string]bootstrap.ProviderConfig{
			"custom-openai": {Label: "Stale Label", Models: []string{"old-model"}},
		},
		Roles: map[string]bootstrap.RoleConfig{
			"writer": {Provider: "custom-openai", Model: "old-model"},
		},
		ModelAutoSwitch: bootstrap.ModelAutoSwitchConfig{
			FallbackBackends: []string{"custom-openai"},
		},
	})

	activeProject, err := server.store.CreateProject("Active Project")
	if err != nil {
		t.Fatalf("CreateProject active: %v", err)
	}
	writeProjectModelOverlay(t, activeProject, bootstrap.Config{
		Provider:  "custom-openai",
		ModelName: "old-model",
		Providers: map[string]bootstrap.ProviderConfig{
			"custom-openai": {Label: "Stale Label", Models: []string{"old-model"}},
		},
		Roles: map[string]bootstrap.RoleConfig{
			"editor": {Provider: "custom-openai", Model: "old-model"},
		},
	})
	activeSession, _, err := server.sessions.Open(activeProject.ID)
	if err != nil {
		t.Fatalf("Open active session: %v", err)
	}
	if route := findModelRoute(activeSession.ModelConfig().Roles, "default"); route.Provider != "custom-openai" {
		t.Fatalf("precondition active default route = %+v", route)
	}

	var edited struct {
		Models apiModelConfig `json:"models"`
	}
	body := `{"role":"default","original_provider":"custom-openai","provider":"fixed-openai","model":"new-model","label":"Fixed Label","type":"openai","api":"chat"}`
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/add", body, &edited)
	if route := findModelRoute(edited.Models.Roles, "default"); route.Provider != "fixed-openai" || route.Model != "new-model" {
		t.Fatalf("global default route = %+v", route)
	}

	closedOverlay := readProjectOverlay(t, closedProject)
	if _, ok := closedOverlay.Providers["custom-openai"]; ok {
		t.Fatalf("closed overlay still has old provider: %+v", closedOverlay.Providers)
	}
	if closedOverlay.Provider != "fixed-openai" || closedOverlay.ModelName != "old-model" {
		t.Fatalf("closed overlay default = %s/%s", closedOverlay.Provider, closedOverlay.ModelName)
	}
	if rc := closedOverlay.Roles["writer"]; rc.Provider != "fixed-openai" || rc.Model != "old-model" {
		t.Fatalf("closed writer route = %+v", rc)
	}
	if provider := closedOverlay.ModelAutoSwitch.FallbackBackends[0]; provider != "fixed-openai" {
		t.Fatalf("closed fallback provider = %q", provider)
	}
	if pc := closedOverlay.Providers["fixed-openai"]; pc.Label != "Fixed Label" || pc.Type != "" || pc.APIKey != "" || !containsString(pc.Models, "old-model") {
		t.Fatalf("closed inherited provider metadata = %+v", pc)
	}

	activeModels := activeSession.ModelConfig()
	if route := findModelRoute(activeModels.Roles, "default"); route.Provider != "fixed-openai" || route.Model != "old-model" {
		t.Fatalf("active default route = %+v", route)
	}
	if route := findModelRoute(activeModels.Roles, "editor"); route.Provider != "fixed-openai" || route.Model != "old-model" {
		t.Fatalf("active editor route = %+v", route)
	}
	activeProvider := findModelProvider(activeModels.Providers, "fixed-openai")
	if activeProvider.Name == "" || activeProvider.Label != "Fixed Label" {
		t.Fatalf("active provider = %+v", activeProvider)
	}

	var reopened struct {
		Models apiModelConfig `json:"models"`
	}
	serveJSON(t, server.Handler(), http.MethodGet, "/api/projects/"+closedProject.ID+"/models", "", &reopened)
	if modelConfigHasProvider(reopened.Models, "custom-openai", "old-model") {
		t.Fatalf("reopened project still exposes old provider: %+v", reopened.Models.Providers)
	}
	reopenedProvider := findModelProvider(reopened.Models.Providers, "fixed-openai")
	if reopenedProvider.Name == "" || reopenedProvider.Label != "Fixed Label" {
		t.Fatalf("reopened provider = %+v", reopenedProvider)
	}
	if route := findModelRoute(reopened.Models.Roles, "default"); route.Provider != "fixed-openai" {
		t.Fatalf("reopened default route = %+v", route)
	}
}

func TestGlobalModelEditProbeFailureDoesNotPersist(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return errors.New("probe rejected")
	})
	t.Cleanup(restore)

	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	cfg.Providers["custom-openai"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-secret",
		Models: []string{"old-model"},
	}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/models/add", bytes.NewBufferString(`{"role":"default","original_provider":"custom-openai","provider":"fixed-openai","model":"new-model","type":"openai"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("model edit status = %d body=%s, want failure", rec.Code, rec.Body.String())
	}
	if _, ok := server.currentConfig().Providers["fixed-openai"]; ok {
		t.Fatal("failed probe persisted renamed provider")
	}
	if server.currentConfig().Providers["custom-openai"].APIKey != "sk-secret" {
		t.Fatalf("original provider changed: %+v", server.currentConfig().Providers["custom-openai"])
	}
}

func writeProjectModelOverlay(t *testing.T, manifest ProjectManifest, cfg bootstrap.Config) {
	t.Helper()
	if err := bootstrap.SaveConfig(ProjectConfigPath(manifest), cfg); err != nil {
		t.Fatalf("write project overlay: %v", err)
	}
}

func TestGlobalModelTestDoesNotPersistOrLeakAPIKey(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return errors.New("probe failed for sk-secret")
	})
	t.Cleanup(restore)

	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/models/test", bytes.NewBufferString(`{"role":"default","provider":"probe-openai","model":"probe-model","type":"openai","api_key":"sk-secret","base_url":"https://proxy.example/v1"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("model test status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-secret") {
		t.Fatalf("model test response leaked api key: %s", rec.Body.String())
	}
	if _, ok := server.currentConfig().Providers["probe-openai"]; ok {
		t.Fatal("model test should not persist provider config")
	}
}

func TestGlobalModelTestExistingProviderUsesEditFlow(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return nil
	})
	t.Cleanup(restore)

	cfg := testWebConfig(t)
	cfg.Providers["deepseek"] = bootstrap.ProviderConfig{
		Label:   "DeepSeek",
		Type:    "openai",
		API:     "chat",
		APIKey:  "sk-old",
		BaseURL: "https://api.sfkey.cn/v1",
		Models:  []string{"deepseek-v4-pro"},
	}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	body := `{"role":"default","original_provider":"deepseek","provider":"deepseek","model":"deepseek-v4-pro","label":"DeepSeek","type":"openai","api":"chat","base_url":"https://api.sfkey.cn/v1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/models/test", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("model test status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "already exists") {
		t.Fatalf("model test used add-provider flow: %s", rec.Body.String())
	}
	var response struct {
		Test hostpkg.ProviderModelTestResult `json:"test"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Test.Status != "ok" || response.Test.Provider != "deepseek" {
		t.Fatalf("model test response = %+v", response.Test)
	}
	if server.currentConfig().Providers["deepseek"].APIKey != "sk-old" {
		t.Fatalf("existing provider changed: %+v", server.currentConfig().Providers["deepseek"])
	}
}

func TestGlobalModelDeleteRemovesProviderAndRoleRoute(t *testing.T) {
	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	cfg.Providers["proxy"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-proxy",
		Models: []string{"proxy-model"},
	}
	cfg.Roles = map[string]bootstrap.RoleConfig{
		"writer": {Provider: "proxy", Model: "proxy-model"},
	}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var deleted struct {
		Models  apiModelConfig `json:"models"`
		Runtime struct {
			Config struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			} `json:"config"`
		} `json:"runtime"`
	}
	serveJSON(t, server.Handler(), http.MethodDelete, "/api/models", `{"provider":"proxy","model":"proxy-model"}`, &deleted)
	if modelConfigHasProvider(deleted.Models, "proxy", "proxy-model") {
		t.Fatalf("deleted models still include proxy: %+v", deleted.Models.Providers)
	}
	if route := findModelRoute(deleted.Models.Roles, "writer"); route.Provider != "openai" || route.Model != "gpt-test" || route.Explicit {
		t.Fatalf("writer route after delete = %+v", route)
	}
	if deleted.Runtime.Config.Provider != "openai" || deleted.Runtime.Config.Model != "gpt-test" {
		t.Fatalf("runtime default after delete = %+v", deleted.Runtime.Config)
	}

	saved, err := bootstrap.LoadConfigFile(cfg.PersistPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if _, ok := saved.Providers["proxy"]; ok {
		t.Fatalf("saved providers still include proxy: %+v", saved.Providers["proxy"])
	}
	if _, ok := saved.Roles["writer"]; ok {
		t.Fatalf("saved writer route still exists: %+v", saved.Roles["writer"])
	}
}

func TestGlobalModelDeleteRejectsCurrentDefault(t *testing.T) {
	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	req := httptest.NewRequest(http.MethodDelete, "/api/models", bytes.NewBufferString(`{"provider":"openai","model":"gpt-test"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("delete default status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := server.currentConfig().Provider + "/" + server.currentConfig().ModelName; got != "openai/gpt-test" {
		t.Fatalf("default changed after rejected delete: %s", got)
	}
}

func modelConfigHasProvider(models apiModelConfig, providerName, modelName string) bool {
	for _, provider := range models.Providers {
		if provider.Name != providerName {
			continue
		}
		for _, model := range provider.Models {
			if model == modelName {
				return true
			}
		}
	}
	return false
}

func findModelProvider(providers []apiModelProvider, name string) apiModelProvider {
	for _, provider := range providers {
		if provider.Name == name {
			return provider
		}
	}
	return apiModelProvider{}
}

func findModelRoute(routes []apiModelRoute, role string) apiModelRoute {
	for _, route := range routes {
		if route.Role == role {
			return route
		}
	}
	return apiModelRoute{}
}

func TestProjectModelConfigureExistingPassesRetryAndPoolSettings(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Existing Model Configure")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	body := `{"role":"default","original_provider":"openrouter","provider":"fixed-router","model":"model-b","label":"Fixed Router","type":"openai","base_url":"https://router.example/v1","network_disconnect_max_attempts":5,"auto_switch_candidate_pool":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/models/add", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("model configure status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.configureOriginalProvider != "openrouter" || fake.configureProviderName != "fixed-router" || fake.configureProviderModel != "model-b" {
		t.Fatalf("configure args original=%q provider=%q model=%q", fake.configureOriginalProvider, fake.configureProviderName, fake.configureProviderModel)
	}
	if fake.configureProviderConfig.Label != "Fixed Router" || fake.configureProviderConfig.BaseURL != "https://router.example/v1" || fake.configureProviderConfig.Type != "openai" {
		t.Fatalf("configure provider config = %+v", fake.configureProviderConfig)
	}
	if fake.configureNetworkAttempts != 5 || !fake.configureAutoSwitchPool {
		t.Fatalf("configure retry/pool attempts=%d pool=%v", fake.configureNetworkAttempts, fake.configureAutoSwitchPool)
	}
}

func TestProjectModelAddExistingProviderUsesEmptyConfig(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
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
	if fake.configureProviderRole != "writer" || fake.configureProviderName != "openrouter" || fake.configureProviderModel != "new-model" {
		t.Fatalf("configure model args role=%q provider=%q model=%q", fake.configureProviderRole, fake.configureProviderName, fake.configureProviderModel)
	}
	if fake.configureProviderConfig.Type != "" || fake.configureProviderConfig.APIKey != "" || len(fake.configureProviderConfig.Models) != 0 {
		t.Fatalf("existing provider should use empty config: %+v", fake.configureProviderConfig)
	}
}

func TestProjectModelAddPresetPassesProviderConfig(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Preset Model Add")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/models/add", bytes.NewBufferString(`{"role":"default","provider":"anthropic","label":"Anthropic","template_provider":"anthropic","type":"anthropic","api_key":"sk-test","model":"claude-sonnet-4-5","use_proxy":false,"request_timeout_seconds":120,"connectivity_timeout_seconds":12}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("model add status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.configureProviderName != "anthropic" || fake.configureProviderConfig.Type != "anthropic" || fake.configureProviderConfig.APIKey != "sk-test" {
		t.Fatalf("preset provider config = %+v provider=%q", fake.configureProviderConfig, fake.configureProviderName)
	}
	if fake.configureProviderConfig.Label != "Anthropic" || fake.configureProviderConfig.TemplateProvider != "anthropic" {
		t.Fatalf("preset provider metadata = %+v", fake.configureProviderConfig)
	}
	if fake.configureProviderConfig.UseProxy == nil || *fake.configureProviderConfig.UseProxy {
		t.Fatalf("preset use_proxy = %#v, want explicit false", fake.configureProviderConfig.UseProxy)
	}
	if fake.configureProviderConfig.RequestTimeoutSeconds != 120 || fake.configureProviderConfig.ConnectivityTimeoutSeconds != 12 {
		t.Fatalf("preset timeouts = %d/%d", fake.configureProviderConfig.RequestTimeoutSeconds, fake.configureProviderConfig.ConnectivityTimeoutSeconds)
	}
	if len(fake.configureProviderConfig.Models) != 0 || fake.configureProviderModel != "claude-sonnet-4-5" {
		t.Fatalf("preset model list = %+v model=%q", fake.configureProviderConfig.Models, fake.configureProviderModel)
	}
}

func TestProjectModelAddGrokOAuthPassesProviderConfig(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Grok OAuth Model Add")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/models/add", bytes.NewBufferString(`{"role":"writer","provider":"grok-oauth","type":"grok","auth":"grok_oauth","account_id":"work","model":"grok-4.3-latest","api":"chat","api_key":"should-not-forward"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("model add status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.configureProviderName != "grok-oauth" || fake.configureProviderModel != "grok-4.3-latest" {
		t.Fatalf("grok add args provider=%q model=%q", fake.configureProviderName, fake.configureProviderModel)
	}
	if fake.configureProviderConfig.Type != "grok" || fake.configureProviderConfig.Auth != bootstrap.ProviderAuthGrokOAuth || fake.configureProviderConfig.AccountID != "work" {
		t.Fatalf("grok provider config = %+v", fake.configureProviderConfig)
	}
	if len(fake.configureProviderConfig.Models) != 0 {
		t.Fatalf("grok model list = %+v", fake.configureProviderConfig.Models)
	}
	if fake.configureProviderConfig.API != "" || fake.configureProviderConfig.APIKey != "" {
		t.Fatalf("grok_oauth config should not receive OpenAI api or api key: %+v", fake.configureProviderConfig)
	}
}

func TestProjectGrokLoginEndpointsUseHostFlow(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Grok OAuth Login")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.grokLoginStart = grokauth.LoginStart{
		Status:               grokauth.AuthStatus{AccountID: "work", AccountName: "Work", ActiveLogin: "pending"},
		AuthorizeURL:         "https://auth.x.ai/authorize",
		RedirectURI:          "http://127.0.0.1:56121/callback",
		ManualPasteSupported: true,
		LoopbackListening:    true,
	}
	fake.grokLoginPoll = grokauth.LoginPoll{
		Status:  grokauth.AuthStatus{AccountID: "work", AccountName: "Work", ActiveLogin: "pending"},
		State:   "pending",
		Message: "waiting",
	}
	fake.grokCompleteStatus = grokauth.AuthStatus{LoggedIn: true, Provider: grokauth.ProviderID, AccountID: "work", AccountName: "Work"}
	fake.grokStatus = fake.grokCompleteStatus

	var start struct {
		Login grokauth.LoginStart `json:"login"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/projects/"+manifest.ID+"/models/grok-login/start", `{"account_id":"work","account_name":"Work"}`, &start)
	if fake.grokStartAccountID != "work" || fake.grokStartAccountName != "Work" {
		t.Fatalf("start args accountID=%q accountName=%q", fake.grokStartAccountID, fake.grokStartAccountName)
	}
	if start.Login.AuthorizeURL != "https://auth.x.ai/authorize" || !start.Login.ManualPasteSupported {
		t.Fatalf("start login = %+v", start.Login)
	}

	var poll struct {
		Login grokauth.LoginPoll `json:"login"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/projects/"+manifest.ID+"/models/grok-login/poll", `{}`, &poll)
	if poll.Login.State != "pending" || poll.Login.Message != "waiting" {
		t.Fatalf("poll login = %+v", poll.Login)
	}

	var complete struct {
		Status grokauth.AuthStatus `json:"status"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/projects/"+manifest.ID+"/models/grok-login/complete", `{"callback":"?code=abc&state=state"}`, &complete)
	if fake.grokCompleteCallback != "?code=abc&state=state" || !complete.Status.LoggedIn {
		t.Fatalf("complete callback=%q status=%+v", fake.grokCompleteCallback, complete.Status)
	}

	var status struct {
		Status grokauth.AuthStatus `json:"status"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/projects/"+manifest.ID+"/models/grok-login/status", `{"account_id":"work"}`, &status)
	if fake.grokStatusAccountID != "work" || !status.Status.LoggedIn {
		t.Fatalf("status accountID=%q status=%+v", fake.grokStatusAccountID, status.Status)
	}
}

func TestProjectGrokLoginStartCanOpenAuthorizeURL(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Grok OAuth Browser Open")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.grokLoginStart = grokauth.LoginStart{
		Status:               grokauth.AuthStatus{AccountID: "work", AccountName: "Work", ActiveLogin: "pending"},
		AuthorizeURL:         "https://auth.x.ai/authorize",
		RedirectURI:          "http://127.0.0.1:56121/callback",
		ManualPasteSupported: true,
		LoopbackListening:    true,
	}

	previousOpenAuthBrowser := openAuthBrowser
	var openedURL string
	openAuthBrowser = func(rawURL string) error {
		openedURL = rawURL
		return nil
	}
	t.Cleanup(func() {
		openAuthBrowser = previousOpenAuthBrowser
	})

	var start struct {
		Login         grokauth.LoginStart `json:"login"`
		BrowserOpened bool                `json:"browser_opened"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/projects/"+manifest.ID+"/models/grok-login/start", `{"account_id":"work","account_name":"Work","open_browser":true}`, &start)
	if openedURL != "https://auth.x.ai/authorize" {
		t.Fatalf("opened URL = %q", openedURL)
	}
	if !start.BrowserOpened {
		t.Fatalf("browser_opened = false, response = %+v", start)
	}
}

func TestGrokLoginStartCanRunWithoutProject(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	previousStartGrokAuthLogin := startGrokAuthLogin
	startGrokAuthLogin = func(accountID, accountName string) (grokauth.LoginStart, error) {
		if accountID != "work" || accountName != "Work" {
			t.Fatalf("start args accountID=%q accountName=%q", accountID, accountName)
		}
		return grokauth.LoginStart{
			Status:               grokauth.AuthStatus{AccountID: accountID, AccountName: accountName, ActiveLogin: "pending"},
			AuthorizeURL:         "https://auth.x.ai/authorize",
			RedirectURI:          "http://127.0.0.1:56121/callback",
			ManualPasteSupported: true,
			LoopbackListening:    true,
		}, nil
	}
	previousOpenAuthBrowser := openAuthBrowser
	var openedURL string
	openAuthBrowser = func(rawURL string) error {
		openedURL = rawURL
		return nil
	}
	t.Cleanup(func() {
		startGrokAuthLogin = previousStartGrokAuthLogin
		openAuthBrowser = previousOpenAuthBrowser
	})

	var start struct {
		Login         grokauth.LoginStart `json:"login"`
		BrowserOpened bool                `json:"browser_opened"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/grok-login/start", `{"account_id":"work","account_name":"Work","open_browser":true}`, &start)
	if openedURL != "https://auth.x.ai/authorize" || !start.BrowserOpened {
		t.Fatalf("openedURL=%q browser_opened=%v", openedURL, start.BrowserOpened)
	}
}

func serveJSON(t *testing.T, handler http.Handler, method, path, body string, out any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s status = %d body=%s", method, path, rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(out); err != nil {
		t.Fatalf("decode %s %s: %v", method, path, err)
	}
}
