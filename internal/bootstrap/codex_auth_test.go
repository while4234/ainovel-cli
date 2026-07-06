package bootstrap

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCodexAuthTransportAddsAccountHeaderAndRewritesResponsesPath(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	data, err := json.Marshal(map[string]any{
		"tokens": map[string]any{
			"access_token": "codex-access-token",
			"account_id":   "acct-test",
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(authPath, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("CODEX_AUTH_FILE", authPath)

	var gotPath string
	var gotAccount string
	transport := newCodexAuthTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotPath = req.URL.Path
		gotAccount = req.Header.Get("chatgpt-account-id")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}), "")

	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/v1/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if gotPath != "/backend-api/codex/responses" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAccount != "acct-test" {
		t.Fatalf("account header = %q", gotAccount)
	}
	if req.Header.Get("x-client-request-id") == "" {
		t.Fatal("missing request id header")
	}
}

func TestCodexAuthProviderConfigValidatesWithoutAPIKey(t *testing.T) {
	cfg := Config{
		Provider:  "codex-login",
		ModelName: "gpt-5.5",
		Providers: map[string]ProviderConfig{
			"codex-login": {
				Type:    "openai",
				Auth:    ProviderAuthCodex,
				API:     "responses",
				BaseURL: "https://chatgpt.com/backend-api/codex",
			},
		},
	}
	cfg.FillDefaults()
	if err := cfg.ValidateBase(); err != nil {
		t.Fatalf("ValidateBase: %v", err)
	}
}
