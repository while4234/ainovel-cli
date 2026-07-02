package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/grokauth"
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

	saved, err := bootstrap.LoadConfigFile(filepath.Join(home, ".ainovel", "config.json"))
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if saved.Provider != "openai" || saved.ModelName != "gpt-next" {
		t.Fatalf("saved default = %s/%s", saved.Provider, saved.ModelName)
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

func TestGlobalModelAddGrokOAuthProvider(t *testing.T) {
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
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/add", `{"role":"default","provider":"grok-oauth","model":"grok-4.3-latest","type":"grok","auth":"grok_oauth","account_id":"default"}`, &added)
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

	saved, err := bootstrap.LoadConfigFile(filepath.Join(home, ".ainovel", "config.json"))
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if saved.Provider != "grok-oauth" || saved.ModelName != "grok-4.3-latest" {
		t.Fatalf("saved default = %s/%s", saved.Provider, saved.ModelName)
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

func findModelRoute(routes []apiModelRoute, role string) apiModelRoute {
	for _, route := range routes {
		if route.Role == role {
			return route
		}
	}
	return apiModelRoute{}
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
	if fake.addProviderRole != "writer" || fake.addProviderName != "openrouter" || fake.addProviderModel != "new-model" {
		t.Fatalf("add model args role=%q provider=%q model=%q", fake.addProviderRole, fake.addProviderName, fake.addProviderModel)
	}
	if fake.addProviderConfig.Type != "" || fake.addProviderConfig.APIKey != "" || len(fake.addProviderConfig.Models) != 0 {
		t.Fatalf("existing provider should use empty config: %+v", fake.addProviderConfig)
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
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
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
