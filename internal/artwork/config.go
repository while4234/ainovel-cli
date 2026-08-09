package artwork

import (
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
)

const (
	DefaultGatewayRequestTimeoutSeconds = 330
	MinGatewayRequestTimeoutSeconds     = 1
	MaxGatewayRequestTimeoutSeconds     = 3600
)

// ImageGatewayConfig is the single global credential and transport policy for
// the AI2API image gateway. Project overlays must never persist this value.
type ImageGatewayConfig struct {
	BaseURL               string `json:"base_url,omitempty"`
	APIKey                string `json:"api_key,omitempty"`
	DefaultModel          string `json:"default_model,omitempty"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds,omitempty"`
}

func (c ImageGatewayConfig) Normalized() (ImageGatewayConfig, error) {
	normalized := c
	normalized.APIKey = strings.TrimSpace(c.APIKey)
	if strings.IndexFunc(normalized.APIKey, unicode.IsControl) >= 0 {
		return ImageGatewayConfig{}, fmt.Errorf("image gateway api_key contains control characters")
	}
	normalized.DefaultModel = strings.TrimSpace(c.DefaultModel)
	if normalized.DefaultModel == "" {
		normalized.DefaultModel = DefaultModelID
	}
	model, ok := LookupModel(normalized.DefaultModel)
	if !ok {
		return ImageGatewayConfig{}, fmt.Errorf("unknown image model %q", normalized.DefaultModel)
	}
	normalized.DefaultModel = model.ID
	if c.RequestTimeoutSeconds < 0 || c.RequestTimeoutSeconds > MaxGatewayRequestTimeoutSeconds {
		return ImageGatewayConfig{}, fmt.Errorf("request_timeout_seconds must be between %d and %d, or 0 for the default", MinGatewayRequestTimeoutSeconds, MaxGatewayRequestTimeoutSeconds)
	}
	if normalized.RequestTimeoutSeconds == 0 {
		normalized.RequestTimeoutSeconds = DefaultGatewayRequestTimeoutSeconds
	}
	baseURL, err := normalizeGatewayBaseURL(c.BaseURL)
	if err != nil {
		return ImageGatewayConfig{}, err
	}
	normalized.BaseURL = baseURL
	return normalized, nil
}

func (c ImageGatewayConfig) RequestTimeout() time.Duration {
	seconds := c.RequestTimeoutSeconds
	if seconds <= 0 {
		seconds = DefaultGatewayRequestTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

func normalizeGatewayBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("image gateway base_url is invalid")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("image gateway base_url must use http or https")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("image gateway base_url must include a host")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("image gateway base_url must not include userinfo")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", fmt.Errorf("image gateway base_url must not include a query")
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("image gateway base_url must not include a fragment")
	}
	if parsed.RawPath != "" {
		return "", fmt.Errorf("image gateway base_url must not contain an encoded path")
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("image gateway base_url path is invalid")
		}
	}

	cleanPath := strings.TrimSuffix(path.Clean("/"+strings.TrimSpace(parsed.Path)), "/")
	if cleanPath == "." || cleanPath == "/" {
		cleanPath = ""
	}
	for _, suffix := range []string{"/v1/images/generations", "/v1/models", "/v1"} {
		if strings.HasSuffix(cleanPath, suffix) {
			cleanPath = strings.TrimSuffix(cleanPath, suffix)
			break
		}
	}
	parsed.Path = cleanPath
	return strings.TrimSuffix(parsed.String(), "/"), nil
}
