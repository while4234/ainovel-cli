package host

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestAddProviderModelRegistersAndPersists(t *testing.T) {
	withModelConnectivityProbe(t, nil)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cfg := bootstrap.Config{
		Provider:  "openai",
		ModelName: "gpt-base",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "old-key", Models: []string{"gpt-base"}},
		},
	}
	cfg.FillDefaults()
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}
	host := &Host{
		cfg:    cfg,
		models: models,
		events: make(chan Event, 10),
	}

	err = host.AddProviderModel("writer", "proxy-openai", bootstrap.ProviderConfig{
		Type:    "openai",
		APIKey:  "proxy-key",
		BaseURL: "https://proxy.example/v1",
	}, "proxy-model")
	if err != nil {
		t.Fatalf("AddProviderModel: %v", err)
	}

	provider, model, explicit := host.models.CurrentSelection("writer")
	if !explicit || provider != "proxy-openai" || model != "proxy-model" {
		t.Fatalf("writer selection = %s/%s explicit=%v", provider, model, explicit)
	}
	if got := host.cfg.CandidateModels("proxy-openai"); len(got) != 1 || got[0] != "proxy-model" {
		t.Fatalf("proxy candidates = %#v", got)
	}

	saved, err := bootstrap.LoadConfigFile(filepath.Join(home, ".ainovel", "config.json"))
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if saved.Roles["writer"].Provider != "proxy-openai" || saved.Roles["writer"].Model != "proxy-model" {
		t.Fatalf("saved writer role = %#v", saved.Roles["writer"])
	}
	if saved.Providers["proxy-openai"].APIKey != "proxy-key" {
		t.Fatalf("saved provider = %#v", saved.Providers["proxy-openai"])
	}
}

func TestAddProviderModelDoesNotOverwriteExistingProvider(t *testing.T) {
	cfg := bootstrap.Config{
		Provider:  "openai",
		ModelName: "gpt-base",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "old-key", Models: []string{"gpt-base"}},
		},
	}
	cfg.FillDefaults()
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}
	host := &Host{cfg: cfg, models: models, events: make(chan Event, 10)}

	err = host.AddProviderModel("default", "openai", bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "different-key",
	}, "gpt-new")
	if err == nil {
		t.Fatal("expected different existing provider config to be rejected")
	}
	if got := host.cfg.Providers["openai"].APIKey; got != "old-key" {
		t.Fatalf("existing provider was overwritten: %q", got)
	}
}

func TestAddProviderModelRejectsFailedConnectivityProbe(t *testing.T) {
	probeErr := errors.New("probe rejected")
	withModelConnectivityProbe(t, probeErr)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cfg := bootstrap.Config{
		Provider:  "openai",
		ModelName: "gpt-base",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "old-key", Models: []string{"gpt-base"}},
		},
	}
	cfg.FillDefaults()
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}
	host := &Host{
		cfg:    cfg,
		models: models,
		events: make(chan Event, 10),
	}

	err = host.AddProviderModel("writer", "proxy-openai", bootstrap.ProviderConfig{
		Type:    "openai",
		APIKey:  "proxy-key",
		BaseURL: "https://proxy.example/v1",
	}, "proxy-model")
	if err == nil || !strings.Contains(err.Error(), "probe rejected") {
		t.Fatalf("AddProviderModel error = %v, want probe rejection", err)
	}
	if _, ok := host.cfg.Providers["proxy-openai"]; ok {
		t.Fatal("failed probe should not register provider")
	}
	provider, model, explicit := host.models.CurrentSelection("writer")
	if explicit || provider != "openai" || model != "gpt-base" {
		t.Fatalf("writer selection changed after failed probe: %s/%s explicit=%v", provider, model, explicit)
	}
	if _, err := bootstrap.LoadConfigFile(filepath.Join(home, ".ainovel", "config.json")); err == nil {
		t.Fatal("failed probe should not persist config")
	}
}

func withModelConnectivityProbe(t *testing.T, err error) {
	t.Helper()
	previous := addedModelConnectivityProbe
	addedModelConnectivityProbe = func(context.Context, agentcore.ChatModel) error {
		return err
	}
	t.Cleanup(func() {
		addedModelConnectivityProbe = previous
	})
}
