package tui

import (
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/grokauth"
)

func TestModelCommandDocumentsAddUsage(t *testing.T) {
	spec, ok := commandRegistryInstance().Find("model")
	if !ok {
		t.Fatal("expected /model command")
	}
	if !strings.Contains(spec.Usage, "add") {
		t.Fatalf("/model usage should mention add flow: %q", spec.Usage)
	}
}

func TestModelAddStateUsesRoleHint(t *testing.T) {
	state := newModelAddState(&fakeModelRuntime{
		providers: []string{"openai"},
		models:    map[string][]string{"openai": {"gpt-base"}},
	}, "writer")

	if state.role() != "writer" {
		t.Fatalf("role hint = %q, want writer", state.role())
	}
}

func TestModelAddExistingProviderSubmitsRegistration(t *testing.T) {
	rt := &fakeModelRuntime{
		providers: []string{"openai"},
		models:    map[string][]string{"openai": {"gpt-base"}},
	}
	state := newModelAddState(rt, "editor")
	state.step = addStepExistingModel
	state.modelName = "gpt-new"

	cmd := state.advance(rt)
	if cmd == nil {
		t.Fatal("expected submit command")
	}
	if msg := cmd(); msg.(modelAddSubmittedMsg).err != nil {
		t.Fatalf("submit returned error: %v", msg.(modelAddSubmittedMsg).err)
	}
	if rt.addedRole != "editor" || rt.addedProvider != "openai" || rt.addedModel != "gpt-new" {
		t.Fatalf("submitted %s %s/%s", rt.addedRole, rt.addedProvider, rt.addedModel)
	}
}

type fakeModelRuntime struct {
	providers     []string
	models        map[string][]string
	addedRole     string
	addedProvider string
	addedModel    string
}

func (f *fakeModelRuntime) ConfiguredProviders() []string {
	return append([]string(nil), f.providers...)
}

func (f *fakeModelRuntime) ConfiguredModels(provider string) []string {
	return append([]string(nil), f.models[provider]...)
}

func (f *fakeModelRuntime) CurrentModelSelection(role string) (string, string, bool) {
	if len(f.providers) == 0 {
		return "", "", false
	}
	provider := f.providers[0]
	models := f.models[provider]
	if len(models) == 0 {
		return provider, "", false
	}
	return provider, models[0], true
}

func (f *fakeModelRuntime) AvailableThinking(role string) []agentcore.ThinkingLevel {
	return nil
}

func (f *fakeModelRuntime) CurrentThinking(role string) string { return "" }

func (f *fakeModelRuntime) SwitchModel(role, provider, model string) error { return nil }

func (f *fakeModelRuntime) AddProviderModel(role, providerName string, providerConfig bootstrap.ProviderConfig, model string) error {
	f.addedRole = role
	f.addedProvider = providerName
	f.addedModel = model
	return nil
}

func (f *fakeModelRuntime) SetRoleThinking(role, level string) error { return nil }

func (f *fakeModelRuntime) StartGrokLogin(accountID, accountName string) (grokauth.LoginStart, error) {
	return grokauth.LoginStart{}, nil
}

func (f *fakeModelRuntime) PollGrokLogin() (grokauth.LoginPoll, error) {
	return grokauth.LoginPoll{}, nil
}

func (f *fakeModelRuntime) CompleteGrokLogin(callbackInput string) (grokauth.AuthStatus, error) {
	return grokauth.AuthStatus{}, nil
}

func (f *fakeModelRuntime) GrokLoginStatus(accountID string) grokauth.AuthStatus {
	return grokauth.AuthStatus{}
}
