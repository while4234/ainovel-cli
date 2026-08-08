package bootstrap

import (
	"net/http"
	"testing"

	"github.com/voocel/litellm"
)

func TestOpenAICompatibleDeepSeekV4UsesNativeThinkingProtocol(t *testing.T) {
	provider, err := newLiteLLMProviderWithTransport(Config{}, "opencode", "deepseek-v4-flash", ProviderConfig{
		Type:    "openai",
		API:     "chat",
		APIKey:  "test-key",
		BaseURL: "https://opencode.ai/zen/go/v1",
	}, http.DefaultTransport)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	if got := provider.Name(); got != "deepseek" {
		t.Fatalf("provider name = %q, want deepseek", got)
	}
	client, err := litellm.New(provider)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if !client.Capabilities("deepseek-v4-flash").Thinking.SupportsEffort("max") {
		t.Fatal("DeepSeek V4 Flash provider must support max reasoning")
	}
}

func TestOpenAICompatibleNonDeepSeekModelKeepsOpenAIProtocol(t *testing.T) {
	provider, err := newLiteLLMProviderWithTransport(Config{}, "custom-openai", "gpt-5.6-sol", ProviderConfig{
		Type:    "openai",
		API:     "chat",
		APIKey:  "test-key",
		BaseURL: "https://example.invalid/v1",
	}, http.DefaultTransport)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	if got := provider.Name(); got != "openai" {
		t.Fatalf("provider name = %q, want openai", got)
	}
}
