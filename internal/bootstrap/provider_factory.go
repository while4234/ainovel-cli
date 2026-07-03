package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/globalprompt"
	"github.com/voocel/ainovel-cli/internal/grokauth"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/provider/anthropic"
	"github.com/voocel/litellm/provider/bedrock"
	"github.com/voocel/litellm/provider/compat"
	"github.com/voocel/litellm/provider/deepseek"
	"github.com/voocel/litellm/provider/gemini"
	"github.com/voocel/litellm/provider/glm"
	"github.com/voocel/litellm/provider/grok"
	"github.com/voocel/litellm/provider/mimo"
	"github.com/voocel/litellm/provider/minimax"
	"github.com/voocel/litellm/provider/ollama"
	"github.com/voocel/litellm/provider/openai"
	"github.com/voocel/litellm/provider/openrouter"
	"github.com/voocel/litellm/provider/qwen"
)

func newProviderModelWithRuntimeOptions(cfg Config, providerKey, model string, pc ProviderConfig) (agentcore.ChatModel, bool, error) {
	transport, _, err := ProviderTransport(cfg, providerKey, model, pc)
	if err != nil {
		return nil, true, err
	}
	if transport == nil {
		return nil, false, nil
	}
	client, err := newProviderClientWithTransport(cfg, providerKey, model, pc, transport)
	if err != nil {
		return nil, true, err
	}
	wrapped := globalprompt.WrapModel(llm.NewLiteLLMAdapter(model, client))
	if timeout := ProviderRequestTimeout(pc); timeout > 0 {
		wrapped = &requestTimeoutModel{model: wrapped, timeout: timeout}
	}
	return wrapped, true, nil
}

func newProviderClientWithTransport(cfg Config, providerKey, model string, pc ProviderConfig, transport http.RoundTripper) (*litellm.Client, error) {
	provider, err := newLiteLLMProviderWithTransport(cfg, providerKey, model, pc, transport)
	if err != nil {
		return nil, err
	}
	return litellm.New(provider, litellm.WithStreamIdleTimeout(streamIdleTimeout))
}

// DiscoverProviderModels returns a live provider model list when the provider
// exposes one. supported=false means the provider has no list-models endpoint.
func DiscoverProviderModels(ctx context.Context, cfg Config, providerKey, model string, pc ProviderConfig) ([]string, bool, error) {
	transport, _, err := ProviderTransport(cfg, providerKey, model, pc)
	if err != nil {
		return nil, true, err
	}
	client, err := newProviderClientWithTransport(cfg, providerKey, model, pc, transport)
	if err != nil {
		return nil, true, err
	}
	if timeout := ProviderConnectivityTimeout(pc); timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	modelInfos, err := client.ListModels(ctx)
	if err != nil {
		if isModelListingUnsupported(err) {
			return nil, false, nil
		}
		return nil, true, err
	}
	models := make([]string, 0, len(modelInfos))
	seen := make(map[string]bool, len(modelInfos))
	for _, info := range modelInfos {
		id := strings.TrimSpace(info.ID)
		if id == "" {
			id = strings.TrimSpace(info.Name)
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, id)
	}
	sort.Strings(models)
	return models, true, nil
}

func isModelListingUnsupported(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "does not support model listing")
}

func newLiteLLMProviderWithTransport(cfg Config, providerKey, model string, pc ProviderConfig, transport http.RoundTripper) (litellm.Provider, error) {
	providerType, err := pc.ProviderType(providerKey)
	if err != nil {
		return nil, fmt.Errorf("resolve provider type: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(pc.Auth), ProviderAuthGrokOAuth) {
		if strings.ToLower(strings.TrimSpace(providerType)) != "grok" {
			return nil, fmt.Errorf("provider %s auth %q requires grok type: %w", providerKey, pc.Auth, errs.ErrConfig)
		}
		return newGrokOAuthProviderWithTransport(cfg, providerKey, pc, transport)
	}

	headers, err := headersFromProviderExtra(pc.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider %s extra.headers: %w", providerKey, err)
	}
	userAgent := stringFromProviderExtra(pc.Extra, "user_agent")
	providerType = strings.ToLower(strings.TrimSpace(providerType))
	switch providerType {
	case "openai":
		return openai.New(openai.Config{
			API:        providerAPI(pc),
			APIKeyFunc: staticAPIKeyFunc(pc.APIKey),
			BaseURL:    pc.BaseURL,
			Headers:    headers,
			Transport:  transport,
			UserAgent:  userAgent,
		})
	case "anthropic":
		return anthropic.New(anthropic.Config{
			APIKeyFunc: staticAPIKeyFunc(pc.APIKey),
			BaseURL:    pc.BaseURL,
			Beta:       stringFromProviderExtra(pc.Extra, "anthropic_beta"),
			Headers:    headers,
			Transport:  transport,
			UserAgent:  userAgent,
		})
	case "bedrock":
		return bedrock.New(bedrockConfigWithTransport(pc, transport))
	case "gemini":
		return gemini.New(gemini.Config{
			APIKeyFunc: staticAPIKeyFunc(pc.APIKey),
			BaseURL:    pc.BaseURL,
			Transport:  transport,
		})
	case "deepseek":
		return deepseek.New(compatConfigWithTransport(pc, headers, userAgent, transport))
	case "glm":
		return glm.New(compatConfigWithTransport(pc, headers, userAgent, transport))
	case "grok":
		return grok.New(compatConfigWithTransport(pc, headers, userAgent, transport))
	case "minimax":
		return minimax.New(compatConfigWithTransport(pc, headers, userAgent, transport))
	case "mimo":
		return mimo.New(compatConfigWithTransport(pc, headers, userAgent, transport))
	case "ollama":
		return ollama.New(compatConfigWithTransport(pc, headers, userAgent, transport))
	case "openrouter":
		return openrouter.New(compatConfigWithTransport(pc, headers, userAgent, transport))
	case "qwen":
		return qwen.New(compatConfigWithTransport(pc, headers, userAgent, transport))
	default:
		return nil, fmt.Errorf("unknown provider %q", providerType)
	}
}

func newGrokOAuthProviderWithTransport(cfg Config, providerKey string, pc ProviderConfig, transport http.RoundTripper) (litellm.Provider, error) {
	headers, err := headersFromProviderExtra(pc.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider %s extra.headers: %w", providerKey, err)
	}
	baseURL := strings.TrimSpace(pc.BaseURL)
	if baseURL == "" {
		baseURL = grokauth.DefaultBaseURL
	}
	return grok.New(grok.Config{
		APIKeyFunc: func(ctx context.Context) (string, error) {
			credentials, err := grokauth.ResolveRuntimeCredentials(ctx, pc.AccountID)
			if err != nil {
				return "", err
			}
			return credentials.APIKey, nil
		},
		BaseURL:                     baseURL,
		Headers:                     headers,
		Transport:                   transport,
		UserAgent:                   stringFromProviderExtra(pc.Extra, "user_agent"),
		AllowUnknownProviderOptions: true,
	})
}

func compatConfigWithTransport(pc ProviderConfig, headers map[string]string, userAgent string, transport http.RoundTripper) compat.Config {
	return compat.Config{
		APIKeyFunc:                  staticAPIKeyFunc(pc.APIKey),
		BaseURL:                     pc.BaseURL,
		Headers:                     headers,
		Transport:                   transport,
		UserAgent:                   userAgent,
		AllowUnknownProviderOptions: true,
	}
}

func bedrockConfigWithTransport(pc ProviderConfig, transport http.RoundTripper) bedrock.Config {
	return bedrock.Config{
		Region:              firstProviderExtraString(pc.Extra, "region", "aws_region"),
		BaseURL:             pc.BaseURL,
		ControlPlaneBaseURL: stringFromProviderExtra(pc.Extra, "control_plane_base_url"),
		Credentials: bedrock.StaticCredentials(
			firstProviderExtraString(pc.Extra, "access_key_id", "aws_access_key_id"),
			firstProviderExtraString(pc.Extra, "secret_access_key", "aws_secret_access_key"),
			firstProviderExtraString(pc.Extra, "session_token", "aws_session_token"),
		),
		Transport: transport,
	}
}

func staticAPIKeyFunc(key string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		return key, nil
	}
}

func firstProviderExtraString(extra map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringFromProviderExtra(extra, key); value != "" {
			return value
		}
	}
	return ""
}

func providerAPI(pc ProviderConfig) string {
	if value := firstProviderExtraString(pc.Extra, "api", "api_mode"); value != "" {
		return value
	}
	return pc.API
}

type requestTimeoutModel struct {
	model   agentcore.ChatModel
	timeout time.Duration
}

func (m *requestTimeoutModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	if m.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.timeout)
		defer cancel()
	}
	return m.model.Generate(ctx, messages, tools, opts...)
}

func (m *requestTimeoutModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	if m.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.timeout)
		ch, err := m.model.GenerateStream(ctx, messages, tools, opts...)
		if err != nil {
			cancel()
			return nil, err
		}
		return cancelOnStreamDone(ch, cancel), nil
	}
	return m.model.GenerateStream(ctx, messages, tools, opts...)
}

func (m *requestTimeoutModel) SupportsTools() bool {
	return m.model != nil && m.model.SupportsTools()
}

func (m *requestTimeoutModel) ProviderName() string {
	if namer, ok := m.model.(interface{ ProviderName() string }); ok {
		return namer.ProviderName()
	}
	return ""
}

func (m *requestTimeoutModel) Info() llm.ModelInfo {
	if info, ok := m.model.(interface{ Info() llm.ModelInfo }); ok {
		return info.Info()
	}
	return llm.ModelInfo{}
}

func (m *requestTimeoutModel) Capabilities() llm.Capabilities {
	if cp, ok := m.model.(llm.CapabilityProvider); ok {
		return cp.Capabilities()
	}
	return llm.Capabilities{}
}

func cancelOnStreamDone(source <-chan agentcore.StreamEvent, cancel context.CancelFunc) <-chan agentcore.StreamEvent {
	out := make(chan agentcore.StreamEvent, 100)
	go func() {
		defer close(out)
		defer cancel()
		for ev := range source {
			out <- ev
		}
	}()
	return out
}
