package globalprompt

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
)

func TestApplyPrefixesSystemPrompt(t *testing.T) {
	prefix := Text()
	if prefix == "" {
		t.Fatal("global prompt template must not be empty")
	}

	got := Apply("role prompt")

	if !strings.HasPrefix(got, prefix+"\n\n") {
		t.Fatalf("global prompt was not prepended:\n%s", got)
	}
	if !strings.HasSuffix(got, "role prompt") {
		t.Fatalf("role prompt should remain after the prefix:\n%s", got)
	}
}

func TestApplyForModelSelectsGPTPrompt(t *testing.T) {
	gptPrefix := TextForModel("openai/gpt-5.5")
	deepSeekPrefix := TextForModel("deepseek/deepseek-v4-pro")
	if gptPrefix == "" || deepSeekPrefix == "" {
		t.Fatal("model-specific global prompts must not be empty")
	}

	got := ApplyForModel("openai/gpt-5.5", "role prompt")

	if !strings.HasPrefix(got, gptPrefix+"\n\n") {
		t.Fatalf("GPT prompt was not selected:\n%s", got)
	}
	if body := Strip(got); body != "role prompt" {
		t.Fatalf("global prompt should strip back to the role prompt, got %q", body)
	}
}

func TestApplyForModelSelectsGrokPrompt(t *testing.T) {
	grokPrefix := TextForModel("xai/grok-4.3-latest")
	deepSeekPrefix := TextForModel("deepseek/deepseek-v4-pro")
	gptPrefix := TextForModel("openai/gpt-5.5")
	if grokPrefix == "" {
		t.Fatal("Grok global prompt must not be empty")
	}
	if grokPrefix == deepSeekPrefix || grokPrefix == gptPrefix {
		t.Fatal("Grok prompt should be distinct from DeepSeek/GPT prompts")
	}

	got := ApplyForModel("grok-oauth/grok-4.3-latest", "role prompt")

	if !strings.HasPrefix(got, grokPrefix+"\n\n") {
		t.Fatalf("Grok prompt was not selected:\n%s", got)
	}
	if body := Strip(got); body != "role prompt" {
		t.Fatalf("global prompt should strip back to the role prompt, got %q", body)
	}
}

func TestApplyForModelReplacesExistingGlobalPrompt(t *testing.T) {
	deepSeekPrompt := ApplyForModel("deepseek/deepseek-v4-pro", "role prompt")

	got := ApplyForModel("openai/gpt-5.5", deepSeekPrompt)
	wantPrefix := TextForModel("openai/gpt-5.5")

	if !strings.HasPrefix(got, wantPrefix+"\n\n") {
		t.Fatalf("model switch should replace the existing prefix:\n%s", got)
	}
	if strings.Count(got, "role prompt") != 1 {
		t.Fatalf("role prompt should remain exactly once:\n%s", got)
	}
}

func TestApplyForModelReplacesDeepSeekWithGrokPrompt(t *testing.T) {
	deepSeekPrompt := ApplyForModel("deepseek/deepseek-v4-pro", "role prompt")

	got := ApplyForModel("xai/grok-4.3-latest", deepSeekPrompt)
	wantPrefix := TextForModel("xai/grok-4.3-latest")

	if !strings.HasPrefix(got, wantPrefix+"\n\n") {
		t.Fatalf("model switch should replace the existing prefix:\n%s", got)
	}
	if strings.Count(got, "role prompt") != 1 {
		t.Fatalf("role prompt should remain exactly once:\n%s", got)
	}
}

func TestWrapModelAppliesPromptForCurrentModel(t *testing.T) {
	capture := &captureModel{provider: "openai", model: "gpt-5.5"}
	wrapped := WrapModel(capture)

	_, err := wrapped.Generate(context.Background(), []agentcore.Message{
		agentcore.SystemMsg(ApplyForModel("deepseek/deepseek-v4-pro", "role prompt")),
		agentcore.UserMsg("hello"),
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	systemPrompt := capture.messages[0].TextContent()
	if !strings.HasPrefix(systemPrompt, TextForModel("openai/gpt-5.5")+"\n\n") {
		t.Fatalf("wrapped model did not apply GPT prompt:\n%s", systemPrompt)
	}
	if body := Strip(systemPrompt); body != "role prompt" {
		t.Fatalf("wrapped model should preserve only the role prompt body, got %q", body)
	}
}

type captureModel struct {
	provider string
	model    string
	messages []agentcore.Message
}

func (m *captureModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.messages = messages
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:    agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.TextBlock("ok")},
	}}, nil
}

func (m *captureModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	ch := make(chan agentcore.StreamEvent)
	close(ch)
	return ch, nil
}

func (m *captureModel) SupportsTools() bool { return true }

func (m *captureModel) ProviderName() string { return m.provider }

func (m *captureModel) ModelName() string { return m.model }

func (m *captureModel) Info() llm.ModelInfo {
	return llm.ModelInfo{Provider: m.provider, Name: m.model}
}

func TestApplyIsIdempotent(t *testing.T) {
	first := Apply("role prompt")
	second := Apply(first)

	if second != first {
		t.Fatalf("Apply should not duplicate the global prompt:\nfirst=%q\nsecond=%q", first, second)
	}
}
