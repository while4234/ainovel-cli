package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/host"
)

var modelConfigRoles = []string{"default", "coordinator", "architect", "writer", "editor"}

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
