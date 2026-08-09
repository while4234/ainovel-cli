package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/voocel/ainovel-cli/internal/artwork"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

type artworkGatewayConfigRequest struct {
	BaseURL               *string `json:"base_url"`
	APIKey                *string `json:"api_key"`
	ClearAPIKey           bool    `json:"clear_api_key"`
	DefaultModel          *string `json:"default_model"`
	RequestTimeoutSeconds *int    `json:"request_timeout_seconds"`
}

type artworkGatewayConfigResponse struct {
	BaseURL               string `json:"base_url"`
	DefaultModel          string `json:"default_model"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
	HasAPIKey             bool   `json:"has_api_key"`
}

type artworkGatewayConfigSaveError struct{ err error }

func (e *artworkGatewayConfigSaveError) Error() string { return e.err.Error() }

func (s *Server) handleArtworkConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		config, err := effectiveArtworkGatewayConfig(s.currentConfig())
		if err != nil {
			writeArtworkError(w, http.StatusInternalServerError, "invalid_gateway_config", "saved image gateway configuration is invalid", artwork.DeliveryNotSent)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"config": publicArtworkGatewayConfig(config)})
	case http.MethodPut:
		var request artworkGatewayConfigRequest
		if err := decodeJSONBody(r, &request); err != nil {
			writeArtworkError(w, http.StatusBadRequest, "invalid_request", err.Error(), artwork.DeliveryNotSent)
			return
		}
		config, err := s.saveArtworkGatewayConfig(request)
		if err != nil {
			var saveErr *artworkGatewayConfigSaveError
			if errors.As(err, &saveErr) {
				writeArtworkError(w, http.StatusInternalServerError, "config_save_failed", "image gateway configuration could not be saved", artwork.DeliveryNotSent)
				return
			}
			writeArtworkError(w, http.StatusBadRequest, "invalid_gateway_config", err.Error(), artwork.DeliveryNotSent)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"config": publicArtworkGatewayConfig(config)})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleArtworkConfigVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request artworkGatewayConfigRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeArtworkError(w, http.StatusBadRequest, "invalid_request", err.Error(), artwork.DeliveryNotSent)
		return
	}
	config, err := effectiveArtworkGatewayConfig(s.currentConfig())
	if err == nil {
		config, err = applyArtworkGatewayConfigRequest(config, request)
	}
	if err != nil {
		writeArtworkError(w, http.StatusBadRequest, "invalid_gateway_config", err.Error(), artwork.DeliveryNotSent)
		return
	}
	client, err := artwork.NewGatewayClient(config, nil)
	if err != nil {
		writeGatewayClientError(w, err)
		return
	}
	result, err := client.Verify(r.Context())
	if err != nil {
		writeGatewayClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"verified":    true,
		"model_count": result.ModelCount,
		"config":      publicArtworkGatewayConfig(config),
	})
}

func (s *Server) handleArtworkModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, artwork.Registry())
}

func (s *Server) saveArtworkGatewayConfig(request artworkGatewayConfigRequest) (artwork.ImageGatewayConfig, error) {
	s.cfgMu.Lock()
	next := cloneWebConfig(s.cfg)
	config, err := effectiveArtworkGatewayConfig(next)
	if err == nil {
		config, err = applyArtworkGatewayConfigRequest(config, request)
	}
	if err == nil {
		next.ImageGateway = &config
		if saveErr := saveWebConfig(next); saveErr != nil {
			err = &artworkGatewayConfigSaveError{err: saveErr}
		}
	}
	if err == nil {
		s.cfg = cloneWebConfig(next)
	}
	s.cfgMu.Unlock()
	if err != nil {
		return artwork.ImageGatewayConfig{}, err
	}
	if s.sessions != nil {
		s.sessions.SetConfig(next)
	}
	return config, nil
}

func effectiveArtworkGatewayConfig(config bootstrap.Config) (artwork.ImageGatewayConfig, error) {
	if config.ImageGateway == nil {
		return (artwork.ImageGatewayConfig{}).Normalized()
	}
	return config.ImageGateway.Normalized()
}

func applyArtworkGatewayConfigRequest(current artwork.ImageGatewayConfig, request artworkGatewayConfigRequest) (artwork.ImageGatewayConfig, error) {
	if request.APIKey != nil && request.ClearAPIKey {
		return artwork.ImageGatewayConfig{}, errors.New("api_key and clear_api_key cannot be used together")
	}
	next := current
	if request.BaseURL != nil {
		next.BaseURL = strings.TrimSpace(*request.BaseURL)
	}
	if request.DefaultModel != nil {
		next.DefaultModel = strings.TrimSpace(*request.DefaultModel)
	}
	if request.RequestTimeoutSeconds != nil {
		next.RequestTimeoutSeconds = *request.RequestTimeoutSeconds
	}
	if request.ClearAPIKey {
		next.APIKey = ""
	}
	if request.APIKey != nil {
		apiKey := strings.TrimSpace(*request.APIKey)
		if apiKey == "" {
			return artwork.ImageGatewayConfig{}, errors.New("api_key cannot be empty; use clear_api_key to remove it")
		}
		next.APIKey = apiKey
	}
	return next.Normalized()
}

func publicArtworkGatewayConfig(config artwork.ImageGatewayConfig) artworkGatewayConfigResponse {
	return artworkGatewayConfigResponse{
		BaseURL:               config.BaseURL,
		DefaultModel:          config.DefaultModel,
		RequestTimeoutSeconds: config.RequestTimeoutSeconds,
		HasAPIKey:             strings.TrimSpace(config.APIKey) != "",
	}
}

func writeGatewayClientError(w http.ResponseWriter, err error) {
	var gatewayErr *artwork.GatewayError
	if !errors.As(err, &gatewayErr) {
		writeArtworkError(w, http.StatusBadGateway, "gateway_request_failed", "image gateway request failed", artwork.DeliveryNotSent)
		return
	}
	status := http.StatusBadGateway
	if gatewayErr.Delivery == artwork.DeliveryNotSent {
		status = http.StatusBadRequest
	}
	writeArtworkError(w, status, gatewayErr.Code, gatewayErr.Message, gatewayErr.Delivery)
}

func writeArtworkError(w http.ResponseWriter, status int, code, message string, delivery artwork.DeliveryState) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":     code,
			"message":  message,
			"delivery": delivery,
		},
	})
}
