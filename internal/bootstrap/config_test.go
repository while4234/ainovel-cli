package bootstrap

import "testing"

func boolPointer(value bool) *bool { return &value }

func TestNormalizeResumeSchedule(t *testing.T) {
	got, err := NormalizeResumeSchedule(ResumeScheduleConfig{
		DailyTimes: []string{"16:00", "15:00", "16:00"},
	})
	if err != nil {
		t.Fatalf("NormalizeResumeSchedule: %v", err)
	}
	if got.Timezone != DefaultResumeScheduleTimezone {
		t.Fatalf("timezone = %q, want %q", got.Timezone, DefaultResumeScheduleTimezone)
	}
	if len(got.DailyTimes) != 2 || got.DailyTimes[0] != "15:00" || got.DailyTimes[1] != "16:00" {
		t.Fatalf("daily times = %#v", got.DailyTimes)
	}
}

func TestNormalizeResumeScheduleRejectsInvalidTime(t *testing.T) {
	if _, err := NormalizeResumeSchedule(ResumeScheduleConfig{DailyTimes: []string{"3:00"}}); err == nil {
		t.Fatal("non-canonical time should be rejected")
	}
}

func TestScheduledResumeDefaultsEnabled(t *testing.T) {
	if !((Config{}).EffectiveScheduledResumeEnabled()) {
		t.Fatal("scheduled resume should default enabled")
	}
	if (Config{ScheduledResumeEnabled: boolPointer(false)}).EffectiveScheduledResumeEnabled() {
		t.Fatal("explicit false should disable scheduled resume")
	}
}

func TestCoCreateTimeoutDefaultsAndValidation(t *testing.T) {
	cfg := Config{}
	if got := cfg.EffectiveCoCreateTimeoutSeconds(); got != DefaultCoCreateTimeoutSeconds {
		t.Fatalf("default timeout = %d, want %d", got, DefaultCoCreateTimeoutSeconds)
	}
	cfg.CoCreateTimeoutSeconds = 45
	if got := cfg.EffectiveCoCreateTimeoutSeconds(); got != 45 {
		t.Fatalf("configured timeout = %d, want 45", got)
	}
	if _, err := NormalizeCoCreateTimeoutSeconds(MaxCoCreateTimeoutSeconds + 1); err == nil {
		t.Fatal("timeout above max should be rejected")
	}
}

func TestCoCreateMaxTokensDefaultsAndValidation(t *testing.T) {
	cfg := Config{}
	if got := cfg.EffectiveCoCreateMaxTokens(); got != DefaultCoCreateMaxTokens {
		t.Fatalf("default max tokens = %d, want %d", got, DefaultCoCreateMaxTokens)
	}
	cfg.CoCreateMaxTokens = 12288
	if got := cfg.EffectiveCoCreateMaxTokens(); got != 12288 {
		t.Fatalf("configured max tokens = %d, want 12288", got)
	}
	if _, err := NormalizeCoCreateMaxTokens(MaxCoCreateMaxTokens + 1); err == nil {
		t.Fatal("max tokens above max should be rejected")
	}
}

func TestAdaptationOutlineAuditRetryDefaultsAndValidation(t *testing.T) {
	cfg := Config{}
	if got := cfg.EffectiveAdaptationOutlineAuditRetryMaxAttempts(); got != DefaultAdaptationOutlineAuditRetryMaxAttempts {
		t.Fatalf("default adaptation outline audit attempts = %d, want %d", got, DefaultAdaptationOutlineAuditRetryMaxAttempts)
	}
	cfg.AdaptationOutlineAuditRetryMaxAttempts = 5
	if got := cfg.EffectiveAdaptationOutlineAuditRetryMaxAttempts(); got != 5 {
		t.Fatalf("configured adaptation outline audit attempts = %d, want 5", got)
	}
	if _, err := NormalizeAdaptationOutlineAuditRetryMaxAttempts(MaxAdaptationOutlineAuditRetryMaxAttempts + 1); err == nil {
		t.Fatal("adaptation outline audit retry attempts above max should be rejected")
	}
}

func TestSimulationModeDefaultsAndValidation(t *testing.T) {
	cases := []struct {
		name string
		mode string
		want string
	}{
		{name: "empty", mode: "", want: SimulationModeNormal},
		{name: "normal", mode: SimulationModeNormal, want: SimulationModeNormal},
		{name: "reinforced", mode: SimulationModeReinforced, want: SimulationModeReinforced},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeSimulationMode(tc.mode)
			if err != nil {
				t.Fatalf("NormalizeSimulationMode(%q): %v", tc.mode, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeSimulationMode(%q) = %q, want %q", tc.mode, got, tc.want)
			}
			if effective := (Config{SimulationMode: tc.mode}).EffectiveSimulationMode(); effective != tc.want {
				t.Fatalf("EffectiveSimulationMode(%q) = %q, want %q", tc.mode, effective, tc.want)
			}
		})
	}
	if _, err := NormalizeSimulationMode("experimental"); err == nil {
		t.Fatal("invalid simulation mode should be rejected")
	}
	if got := (Config{SimulationMode: "experimental"}).EffectiveSimulationMode(); got != SimulationModeNormal {
		t.Fatalf("invalid EffectiveSimulationMode = %q, want %q", got, SimulationModeNormal)
	}
}

func TestValidateBaseRejectsInvalidSimulationMode(t *testing.T) {
	cfg := Config{
		Provider:       "openai",
		ModelName:      "gpt-test",
		SimulationMode: "experimental",
		Providers: map[string]ProviderConfig{
			"openai": {Type: "openai", APIKey: "sk-test"},
		},
	}
	if err := cfg.ValidateBase(); err == nil {
		t.Fatal("ValidateBase accepted invalid simulation_mode")
	}
	cfg.SimulationMode = ""
	if err := cfg.ValidateBase(); err != nil {
		t.Fatalf("ValidateBase should allow empty simulation_mode: %v", err)
	}
}

func TestRememberModelCandidateKeepsSwitchedAwayProviderSelectable(t *testing.T) {
	cfg := Config{
		Provider:  "deepseek",
		ModelName: "deepseek-chat",
		Providers: map[string]ProviderConfig{
			"deepseek": {},
			"zapi-pro": {Models: []string{"gpt-5.5"}},
		},
	}

	cfg.RememberModelCandidate(cfg.Provider, cfg.ModelName)
	cfg.Provider = "zapi-pro"
	cfg.ModelName = "gpt-5.5"
	cfg.RememberModelCandidate(cfg.Provider, cfg.ModelName)

	if got := cfg.CandidateModels("deepseek"); len(got) != 1 || got[0] != "deepseek-chat" {
		t.Fatalf("deepseek candidates = %#v, want [deepseek-chat]", got)
	}
	if got := cfg.CandidateModels("zapi-pro"); len(got) != 1 || got[0] != "gpt-5.5" {
		t.Fatalf("zapi-pro candidates = %#v, want [gpt-5.5]", got)
	}
}

func TestRememberModelCandidateDeduplicatesAndIgnoresBlankValues(t *testing.T) {
	cfg := Config{}

	cfg.RememberModelCandidate("deepseek", " deepseek-chat ")
	cfg.RememberModelCandidate("deepseek", "deepseek-chat")
	cfg.RememberModelCandidate("", "ignored")
	cfg.RememberModelCandidate("deepseek", "")

	got := cfg.CandidateModels("deepseek")
	if len(got) != 1 || got[0] != "deepseek-chat" {
		t.Fatalf("deepseek candidates = %#v, want one trimmed candidate", got)
	}
}

func TestConfigResolveReasoningEffort(t *testing.T) {
	cfg := Config{
		ReasoningEffort: "low", // 顶层默认
		Roles: map[string]RoleConfig{
			"writer":    {Provider: "p", Model: "m", ReasoningEffort: "high"}, // 角色覆盖
			"architect": {Provider: "p", Model: "m"},                          // 无 reasoning_effort，应回落默认
		},
	}

	cases := []struct {
		role string
		want string
	}{
		{"writer", "high"},     // 角色覆盖优先
		{"architect", "low"},   // 角色未配 → 回落顶层默认
		{"editor", "low"},      // 角色不存在 → 顶层默认
		{"", "low"},            // 空 → 顶层默认
		{"default", "low"},     // default → 顶层默认
		{"coordinator", "low"}, // 未配 → 顶层默认
	}
	for _, c := range cases {
		if got := cfg.ResolveReasoningEffort(c.role); got != c.want {
			t.Errorf("ResolveReasoningEffort(%q) = %q, want %q", c.role, got, c.want)
		}
	}

	// 顶层默认也为空时，未覆盖角色返回 ""（不覆盖）。
	empty := Config{Roles: map[string]RoleConfig{"writer": {ReasoningEffort: "xhigh"}}}
	if got := empty.ResolveReasoningEffort("editor"); got != "" {
		t.Errorf("空默认下 editor 应返回 \"\"，得 %q", got)
	}
	if got := empty.ResolveReasoningEffort("writer"); got != "xhigh" {
		t.Errorf("空默认下 writer 覆盖应生效，得 %q", got)
	}
}

func TestGrokOAuthProviderAllowsMissingAPIKey(t *testing.T) {
	cfg := Config{
		Provider:  "grok-oauth",
		ModelName: "grok-4.3-latest",
		Providers: map[string]ProviderConfig{
			"grok-oauth": {
				Type:      "grok",
				Auth:      ProviderAuthGrokOAuth,
				AccountID: "default",
				API:       "chat",
				Models:    []string{"grok-4.3-latest"},
			},
		},
	}

	if cfg.Providers["grok-oauth"].RequiresAPIKey("grok-oauth") {
		t.Fatal("grok_oauth provider should not require api_key in config")
	}
	if err := cfg.ValidateBase(); err != nil {
		t.Fatalf("Grok OAuth config should validate without api_key: %v", err)
	}
	if got := cfg.CandidateModels("grok-oauth"); len(got) != 1 || got[0] != "grok-4.3-latest" {
		t.Fatalf("grok candidates = %#v", got)
	}
}

func TestGrokOAuthProviderRejectsNonGrokType(t *testing.T) {
	cfg := Config{
		Provider:  "xai-proxy",
		ModelName: "grok-4.3-latest",
		Providers: map[string]ProviderConfig{
			"xai-proxy": {
				Type: "openai",
				Auth: ProviderAuthGrokOAuth,
			},
		},
	}

	if err := cfg.ValidateBase(); err == nil {
		t.Fatal("grok_oauth should require provider type grok")
	}
}
