package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/grokauth"
	hostpkg "github.com/voocel/ainovel-cli/internal/host"
)

func TestGlobalModelAddWithoutProjectPersistsAndUpdatesRuntime(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return nil
	})
	defer restore()

	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(t.TempDir(), "config.json")
	server := NewServer(cfg, assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()

	var body struct {
		Models  apiModelConfig `json:"models"`
		Runtime struct {
			Config map[string]any `json:"config"`
		} `json:"runtime"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/add", `{
		"role":"default",
		"provider":"yuanai-deepseek",
		"model":"deepseek-v4-pro",
		"type":"openai",
		"api":"chat",
		"api_key":"sk-new",
		"base_url":"https://yuanyuaicloud.cn/v1"
	}`, &body)

	if body.Runtime.Config["provider"] != "yuanai-deepseek" || body.Runtime.Config["model"] != "deepseek-v4-pro" {
		t.Fatalf("runtime config = %+v", body.Runtime.Config)
	}
	defaultRoute := findModelRoute(body.Models.Roles, "default")
	if defaultRoute.Provider != "yuanai-deepseek" || defaultRoute.Model != "deepseek-v4-pro" {
		t.Fatalf("default route = %+v", defaultRoute)
	}
	saved, err := bootstrap.LoadConfigFile(cfg.PersistPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if saved.Provider != "yuanai-deepseek" || saved.ModelName != "deepseek-v4-pro" {
		t.Fatalf("saved default = %s/%s", saved.Provider, saved.ModelName)
	}
	if saved.Providers["yuanai-deepseek"].APIKey != "sk-new" {
		t.Fatalf("saved provider = %+v", saved.Providers["yuanai-deepseek"])
	}
}

func TestGlobalModelAddWithoutProjectRejectsFailedProbe(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return errors.New("probe rejected")
	})
	defer restore()

	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(t.TempDir(), "config.json")
	server := NewServer(cfg, assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/models/add", bytes.NewBufferString(`{
		"role":"default",
		"provider":"broken-proxy",
		"model":"deepseek-v4-pro",
		"type":"openai",
		"api_key":"sk-new",
		"base_url":"https://proxy.example/v1"
	}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "probe rejected") {
		t.Fatalf("global model add status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := bootstrap.LoadConfigFile(cfg.PersistPath); err == nil {
		t.Fatal("failed probe should not persist global config")
	}
	var runtime struct {
		Config map[string]any `json:"config"`
	}
	serveJSON(t, server.Handler(), http.MethodGet, "/api/runtime", ``, &runtime)
	if runtime.Config["provider"] != "openai" || runtime.Config["model"] != "gpt-test" {
		t.Fatalf("runtime changed after failed probe: %+v", runtime.Config)
	}
}

func TestGlobalModelSwitchWithoutProjectPersistsDefault(t *testing.T) {
	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(t.TempDir(), "config.json")
	cfg.Providers["yuanai-deepseek"] = bootstrap.ProviderConfig{
		Type:    "openai",
		API:     "chat",
		APIKey:  "sk-new",
		BaseURL: "https://yuanyuaicloud.cn/v1",
		Models:  []string{"deepseek-v4-pro"},
	}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()

	var body struct {
		Models  apiModelConfig `json:"models"`
		Runtime struct {
			Config map[string]any `json:"config"`
		} `json:"runtime"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/switch", `{
		"role":"default",
		"provider":"yuanai-deepseek",
		"model":"deepseek-v4-pro"
	}`, &body)

	if body.Runtime.Config["provider"] != "yuanai-deepseek" {
		t.Fatalf("runtime config = %+v", body.Runtime.Config)
	}
	if route := findModelRoute(body.Models.Roles, "default"); route.Provider != "yuanai-deepseek" || route.Model != "deepseek-v4-pro" {
		t.Fatalf("default route = %+v", route)
	}
	saved, err := bootstrap.LoadConfigFile(cfg.PersistPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if saved.Provider != "yuanai-deepseek" || saved.ModelName != "deepseek-v4-pro" {
		t.Fatalf("saved default = %s/%s", saved.Provider, saved.ModelName)
	}
}

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

func findModelRoute(routes []apiModelRoute, role string) apiModelRoute {
	for _, route := range routes {
		if route.Role == role {
			return route
		}
	}
	return apiModelRoute{}
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

func TestProjectModelAddGrokOAuthPassesProviderConfig(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Grok OAuth Model Add")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/models/add", bytes.NewBufferString(`{"role":"writer","provider":"grok-oauth","type":"grok","auth":"grok_oauth","account_id":"work","model":"grok-4.3-latest"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("model add status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.addProviderName != "grok-oauth" || fake.addProviderModel != "grok-4.3-latest" {
		t.Fatalf("grok add args provider=%q model=%q", fake.addProviderName, fake.addProviderModel)
	}
	if fake.addProviderConfig.Type != "grok" || fake.addProviderConfig.Auth != bootstrap.ProviderAuthGrokOAuth || fake.addProviderConfig.AccountID != "work" {
		t.Fatalf("grok provider config = %+v", fake.addProviderConfig)
	}
	if len(fake.addProviderConfig.Models) != 1 || fake.addProviderConfig.Models[0] != "grok-4.3-latest" {
		t.Fatalf("grok model list = %+v", fake.addProviderConfig.Models)
	}
	if fake.addProviderConfig.APIKey != "" {
		t.Fatalf("grok_oauth config should not receive api key: %+v", fake.addProviderConfig)
	}
}

func TestProjectGrokLoginEndpointsUseHostFlow(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
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
