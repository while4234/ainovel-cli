package bootstrap

import "testing"

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
