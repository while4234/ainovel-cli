package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/grokauth"
	"github.com/voocel/ainovel-cli/internal/host"
)

var modelConfigRoles = []string{"default", "coordinator", "architect", "writer", "editor"}

var openAuthBrowser = openBrowser

type apiModelConfig struct {
	Providers      []apiModelProvider `json:"providers"`
	Roles          []apiModelRoute    `json:"roles"`
	ThinkingLevels []string           `json:"thinking_levels"`
	ThinkingRule   string             `json:"thinking_rule"`
}

type apiModelProvider struct {
	Name   string   `json:"name"`
	Models []string `json:"models"`
}

type apiModelRoute struct {
	Role            string `json:"role"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	Explicit        bool   `json:"explicit"`
	ReasoningEffort string `json:"reasoning_effort"`
}

func (s *Server) handleProjectModels(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"models":  session.ModelConfig(),
	})
}

func (s *Server) handleProjectModelSwitch(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Role     string `json:"role"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	models, err := session.SwitchModel(req.Role, req.Provider, req.Model)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"models":   models,
		"snapshot": session.Snapshot(),
	})
}

func (s *Server) handleProjectModelThinking(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Role  string `json:"role"`
		Level string `json:"level"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	models, err := session.SetRoleThinking(req.Role, req.Level)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"models":   models,
		"snapshot": session.Snapshot(),
	})
}

func (s *Server) handleProjectModelAdd(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Role      string `json:"role"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		Type      string `json:"type"`
		Auth      string `json:"auth"`
		AccountID string `json:"account_id"`
		APIKey    string `json:"api_key"`
		BaseURL   string `json:"base_url"`
		API       string `json:"api"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	pc := bootstrap.ProviderConfig{
		Type:      strings.TrimSpace(req.Type),
		Auth:      strings.TrimSpace(req.Auth),
		AccountID: strings.TrimSpace(req.AccountID),
		APIKey:    strings.TrimSpace(req.APIKey),
		BaseURL:   strings.TrimSpace(req.BaseURL),
		API:       strings.TrimSpace(req.API),
	}
	if !providerConfigRequestIsEmpty(pc) {
		pc.Models = []string{strings.TrimSpace(req.Model)}
	}
	models, err := session.AddProviderModel(req.Role, req.Provider, req.Model, pc)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"models":   models,
		"snapshot": session.Snapshot(),
	})
}

func providerConfigRequestIsEmpty(pc bootstrap.ProviderConfig) bool {
	return pc.Type == "" &&
		pc.Auth == "" &&
		pc.AccountID == "" &&
		pc.API == "" &&
		pc.APIKey == "" &&
		pc.BaseURL == ""
}

func (s *Server) handleProjectModelAddOpenAICompatible(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Role     string `json:"role"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		BaseURL  string `json:"base_url"`
		APIKey   string `json:"api_key"`
		API      string `json:"api"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	models, err := session.AddOpenAICompatibleModel(req.Role, req.Provider, req.Model, req.BaseURL, req.APIKey, req.API)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"models":   models,
		"snapshot": session.Snapshot(),
	})
}

func (s *Server) handleProjectGrokLoginStart(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		AccountID   string `json:"account_id"`
		AccountName string `json:"account_name"`
		OpenBrowser bool   `json:"open_browser"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	login, err := session.StartGrokLogin(defaultGrokAccountID(req.AccountID), req.AccountName)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	browserOpened := false
	browserOpenError := ""
	if req.OpenBrowser {
		authorizeURL := strings.TrimSpace(login.AuthorizeURL)
		if authorizeURL == "" {
			browserOpenError = "Grok authorize URL is empty"
		} else if err := openAuthBrowser(authorizeURL); err != nil {
			browserOpenError = err.Error()
		} else {
			browserOpened = true
		}
	}
	response := map[string]any{
		"project": manifest,
		"login":   login,
	}
	if req.OpenBrowser {
		response["browser_opened"] = browserOpened
		if browserOpenError != "" {
			response["browser_open_error"] = browserOpenError
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleProjectGrokLoginPoll(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	login, err := session.PollGrokLogin()
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"login":   login,
	})
}

func (s *Server) handleProjectGrokLoginComplete(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Callback string `json:"callback"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Callback) == "" {
		writeError(w, http.StatusBadRequest, "callback is required")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	status, err := session.CompleteGrokLogin(req.Callback)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"status":  status,
	})
}

func (s *Server) handleProjectGrokLoginStatus(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	accountID := r.URL.Query().Get("account_id")
	if r.Method == http.MethodPost {
		var req struct {
			AccountID string `json:"account_id"`
		}
		if err := decodeJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		accountID = req.AccountID
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"status":  session.GrokLoginStatus(defaultGrokAccountID(accountID)),
	})
}

func defaultGrokAccountID(value string) string {
	if strings.TrimSpace(value) == "" {
		return grokauth.DefaultAccountID
	}
	return value
}

func (s *Server) handleProjectUsage(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"snapshot": snapshot,
		"usage":    apiUsageFromSnapshot(snapshot),
	})
}

func (s *Server) handleProjectBackendStatus(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"backend": session.BackendStatus(false),
	})
}

func (s *Server) handleProjectBackendTest(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"backend": session.BackendStatus(true),
	})
}

func normalizeModelRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		return "default"
	}
	return role
}

type apiUsageSummary struct {
	Overall               apiUsageTotals        `json:"overall"`
	ByRole                []host.AgentCacheStat `json:"by_role"`
	ByModel               []host.AgentCacheStat `json:"by_model"`
	MissingAssistantUsage int                   `json:"missing_assistant_usage"`
	UpdatedAt             time.Time             `json:"updated_at"`
}

type apiUsageTotals struct {
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	SavedUSD         float64 `json:"saved_usd"`
	CacheCapable     bool    `json:"cache_capable"`
	RecentCacheRead  int     `json:"recent_cache_read_tokens"`
	RecentInput      int     `json:"recent_input_tokens"`
	RecentSamples    int     `json:"recent_samples"`
}

func apiUsageFromSnapshot(snapshot host.UISnapshot) apiUsageSummary {
	return apiUsageSummary{
		Overall: apiUsageTotals{
			InputTokens:      snapshot.TotalInputTokens,
			OutputTokens:     snapshot.TotalOutputTokens,
			CacheReadTokens:  snapshot.TotalCacheReadTokens,
			CacheWriteTokens: snapshot.TotalCacheWriteTokens,
			CostUSD:          snapshot.TotalCostUSD,
			SavedUSD:         snapshot.TotalSavedUSD,
			CacheCapable:     snapshot.OverallCacheCapable,
			RecentCacheRead:  snapshot.OverallRecentCacheRead,
			RecentInput:      snapshot.OverallRecentInput,
			RecentSamples:    snapshot.OverallRecentSamples,
		},
		ByRole:                snapshot.CachePerAgent,
		ByModel:               snapshot.CachePerModel,
		MissingAssistantUsage: snapshot.MissingAssistantUsage,
		UpdatedAt:             time.Now().UTC(),
	}
}
