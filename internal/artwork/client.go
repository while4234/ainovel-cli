package artwork

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	MaxImageBytes            = 25 * 1024 * 1024
	maxGenerationJSONBytes   = 36 * 1024 * 1024
	maxVerificationJSONBytes = 4 * 1024 * 1024
)

type DeliveryState string

const (
	DeliveryNotSent   DeliveryState = "not_sent"
	DeliveryUncertain DeliveryState = "uncertain"
	DeliveryResponded DeliveryState = "responded"
)

type GatewayError struct {
	Code       string
	Message    string
	StatusCode int
	Delivery   DeliveryState
}

func (e *GatewayError) Error() string { return e.Message }

type GenerateRequest struct {
	Model  string
	Prompt string
	Size   string
}

type GeneratedImage struct {
	Content []byte
}

type VerificationResult struct {
	ModelCount int
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type GatewayClient struct {
	config ImageGatewayConfig
	http   HTTPDoer
}

func NewGatewayClient(config ImageGatewayConfig, httpClient HTTPDoer) (*GatewayClient, error) {
	normalized, err := config.Normalized()
	if err != nil {
		return nil, &GatewayError{Code: "invalid_gateway_config", Message: err.Error(), Delivery: DeliveryNotSent}
	}
	if normalized.BaseURL == "" {
		return nil, &GatewayError{Code: "gateway_not_configured", Message: "image gateway base_url is required", Delivery: DeliveryNotSent}
	}
	if normalized.APIKey == "" {
		return nil, &GatewayError{Code: "gateway_not_configured", Message: "image gateway api_key is required", Delivery: DeliveryNotSent}
	}
	if httpClient == nil {
		httpClient = &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &GatewayClient{config: normalized, http: httpClient}, nil
}

func (c *GatewayClient) Verify(ctx context.Context) (VerificationResult, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.config.RequestTimeout())
	defer cancel()
	request, err := c.newRequest(requestCtx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return VerificationResult{}, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return VerificationResult{}, &GatewayError{Code: "gateway_unreachable", Message: "image gateway verification failed", Delivery: DeliveryNotSent}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return VerificationResult{}, gatewayHTTPError("image gateway verification", response.StatusCode, DeliveryResponded)
	}
	body, err := readLimitedResponse(response, maxVerificationJSONBytes)
	if err != nil {
		return VerificationResult{}, err
	}
	var payload struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Data == nil {
		return VerificationResult{}, &GatewayError{Code: "gateway_malformed_response", Message: "image gateway returned an invalid model list", Delivery: DeliveryResponded}
	}
	return VerificationResult{ModelCount: len(payload.Data)}, nil
}

func (c *GatewayClient) Generate(ctx context.Context, input GenerateRequest) (GeneratedImage, error) {
	if err := ctx.Err(); err != nil {
		return GeneratedImage{}, &GatewayError{Code: "request_cancelled", Message: "image request was cancelled before submission", Delivery: DeliveryNotSent}
	}
	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" {
		return GeneratedImage{}, &GatewayError{Code: "invalid_request", Message: "image prompt is required", Delivery: DeliveryNotSent}
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = c.config.DefaultModel
	}
	options, err := ResolveImageRequest(model, input.Size)
	if err != nil {
		return GeneratedImage{}, &GatewayError{Code: "invalid_request", Message: err.Error(), Delivery: DeliveryNotSent}
	}
	payload := struct {
		Model          string `json:"model"`
		Prompt         string `json:"prompt"`
		N              int    `json:"n"`
		ResponseFormat string `json:"response_format"`
		Size           string `json:"size"`
		AspectRatio    string `json:"aspect_ratio,omitempty"`
		Resolution     string `json:"resolution,omitempty"`
	}{
		Model:          options.Model,
		Prompt:         prompt,
		N:              1,
		ResponseFormat: "b64_json",
		Size:           options.Size,
		AspectRatio:    options.AspectRatio,
		Resolution:     options.Resolution,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return GeneratedImage{}, &GatewayError{Code: "invalid_request", Message: "image request could not be encoded", Delivery: DeliveryNotSent}
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.config.RequestTimeout())
	defer cancel()
	request, err := c.newRequest(requestCtx, http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	if err != nil {
		return GeneratedImage{}, err
	}
	request.Header.Set("content-type", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return GeneratedImage{}, &GatewayError{
			Code:     "gateway_delivery_uncertain",
			Message:  "image gateway connection ended before delivery could be confirmed",
			Delivery: DeliveryUncertain,
		}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return GeneratedImage{}, gatewayHTTPError("image generation", response.StatusCode, DeliveryResponded)
	}
	responseBody, err := readLimitedResponse(response, maxGenerationJSONBytes)
	if err != nil {
		return GeneratedImage{}, err
	}
	var result struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil || len(result.Data) != 1 {
		return GeneratedImage{}, &GatewayError{Code: "gateway_malformed_response", Message: "image gateway returned invalid image data", Delivery: DeliveryResponded}
	}
	encoded := strings.TrimSpace(result.Data[0].B64JSON)
	if encoded == "" || base64.StdEncoding.DecodedLen(len(encoded)) > MaxImageBytes {
		return GeneratedImage{}, &GatewayError{Code: "gateway_image_too_large", Message: "image gateway response exceeds the image size limit", Delivery: DeliveryResponded}
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 {
		return GeneratedImage{}, &GatewayError{Code: "gateway_malformed_response", Message: "image gateway returned invalid image data", Delivery: DeliveryResponded}
	}
	if len(decoded) > MaxImageBytes {
		return GeneratedImage{}, &GatewayError{Code: "gateway_image_too_large", Message: "image gateway response exceeds the image size limit", Delivery: DeliveryResponded}
	}
	return GeneratedImage{Content: decoded}, nil
}

func (c *GatewayClient) newRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.config.BaseURL+endpoint, body)
	if err != nil {
		return nil, &GatewayError{Code: "invalid_gateway_config", Message: "image gateway request URL is invalid", Delivery: DeliveryNotSent}
	}
	request.Header.Set("accept", "application/json")
	request.Header.Set("authorization", "Bearer "+c.config.APIKey)
	return request, nil
}

func readLimitedResponse(response *http.Response, limit int64) ([]byte, error) {
	if response.ContentLength > limit {
		return nil, &GatewayError{Code: "gateway_response_too_large", Message: "image gateway response exceeds the response size limit", Delivery: DeliveryResponded}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, &GatewayError{Code: "gateway_response_interrupted", Message: "image gateway response ended unexpectedly", Delivery: DeliveryResponded}
	}
	if int64(len(body)) > limit {
		return nil, &GatewayError{Code: "gateway_response_too_large", Message: "image gateway response exceeds the response size limit", Delivery: DeliveryResponded}
	}
	return body, nil
}

func gatewayHTTPError(operation string, status int, delivery DeliveryState) *GatewayError {
	code := "gateway_request_failed"
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		code = "gateway_auth_failed"
	} else if status == http.StatusTooManyRequests {
		code = "gateway_rate_limited"
	}
	return &GatewayError{
		Code:       code,
		Message:    fmt.Sprintf("%s failed with HTTP %d", operation, status),
		StatusCode: status,
		Delivery:   delivery,
	}
}

func IsDeliveryUncertain(err error) bool {
	var gatewayErr *GatewayError
	return errors.As(err, &gatewayErr) && gatewayErr.Delivery == DeliveryUncertain
}
