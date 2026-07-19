package host

import (
	"testing"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestRestoreConfiguredModelRoutesResetsRuntimeStageFailover(t *testing.T) {
	cfg := bootstrap.Config{
		Provider:      "deepseek-suifeng",
		ModelName:     "deepseek-v4-pro",
		ContextWindow: 1_048_576,
		Providers: map[string]bootstrap.ProviderConfig{
			"deepseek-suifeng":  {Type: "openai", APIKey: "primary-key", Models: []string{"deepseek-v4-pro"}},
			"deepseek-yuanyu-0": {Type: "openai", APIKey: "fallback-key", Models: []string{"deepseek-v4-pro"}},
		},
	}
	cfg.FillDefaults()
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}
	contextManager := corecontext.NewEngine(corecontext.EngineConfig{ContextWindow: cfg.ContextWindow})
	coordinator := agentcore.NewAgent(
		agentcore.WithModel(models.ForRole("coordinator")),
		agentcore.WithContextManager(contextManager),
	)
	host := &Host{
		cfg:               cloneHostRuntimeConfig(cfg),
		models:            models,
		coordinator:       coordinator,
		coordinatorCtxMgr: contextManager,
	}
	writingRoute := bootstrap.StageRouteKey(bootstrap.StageWriting)

	if err := models.Swap(writingRoute, "deepseek-yuanyu-0", "deepseek-v4-pro"); err != nil {
		t.Fatalf("simulate runtime failover: %v", err)
	}
	provider, _, _ := host.CurrentModelSelection(writingRoute)
	if provider != "deepseek-yuanyu-0" {
		t.Fatalf("runtime provider before restore = %q, want fallback", provider)
	}

	if err := host.restoreConfiguredModelRoutes(); err != nil {
		t.Fatalf("restoreConfiguredModelRoutes: %v", err)
	}
	provider, model, explicit := host.CurrentModelSelection(writingRoute)
	if provider != "deepseek-suifeng" || model != "deepseek-v4-pro" || explicit {
		t.Fatalf("restored writing route = %s/%s explicit=%v, want inherited configured primary", provider, model, explicit)
	}
	if got := contextManager.ContextWindow(); got != 64_000 {
		t.Fatalf("restored coordinator context window = %d, want model-profile bound 64000", got)
	}
}
