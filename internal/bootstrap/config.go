package bootstrap

import (
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/models"
	"github.com/voocel/ainovel-cli/internal/utils"
)

// DefaultContextWindow 模型未在 registry 登记时的兜底窗口大小。
const DefaultContextWindow = 200000

// CompactRatio 触发上下文压缩的相对阈值：tokens >= window * CompactRatio 时压缩。
// 0.85 是经验值，给"下一轮 prompt + 大工具结果"留 15% 头部空间，同时让大窗口
// 模型也能在 85% 主动压缩，避免在 1M 名义窗口下吃满才压（注意力衰退区）。
//
// 不暴露给用户配置：与已删除的 context_window 同源——多模型架构下让用户调
// 数字旋钮反复横跳，不如代码内固定一个合理值。
const CompactRatio = 0.85

// MinCompactReserve 是 ReserveTokens 的下限。小窗口模型（如 32k 本地 qwen3:8b）
// 按 0.15 比例算 reserve 仅 4800，单次 commit_chapter 工具响应就能塞 5-8k，
// 一章正文 8-15k——会出现"压完立刻又超"。8000 兜底保证最坏场景下还有半轮缓冲。
const MinCompactReserve = 8000

// CompactReserveTokens 按 CompactRatio 反算 ReserveTokens 并应用 MinCompactReserve floor：
//
//	threshold = window - reserve = window * CompactRatio
//	reserve   = max(MinCompactReserve, window * (1 - CompactRatio))
//
// 给 agentcore.context.Engine 的 EngineConfig.ReserveTokens 用。
func CompactReserveTokens(window int) int {
	if window <= 0 {
		return 0
	}
	reserve := window - int(float64(window)*CompactRatio)
	if reserve < MinCompactReserve {
		return MinCompactReserve
	}
	return reserve
}

// ProviderConfig 定义单个 LLM 提供商的凭证。
type ProviderConfig struct {
	Type      string   `json:"type,omitempty"`       // API 协议类型（openai/anthropic/gemini），自定义代理时指定
	Auth      string   `json:"auth,omitempty"`       // 认证模式：空/api_key/grok_oauth
	AccountID string   `json:"account_id,omitempty"` // Grok OAuth 账号 ID；token 存在 ~/.ainovel/auth/grok.json
	API       string   `json:"api,omitempty"`        // OpenAI 协议 endpoint：chat（默认）/ responses
	APIKey    string   `json:"api_key,omitempty"`    // API Key
	BaseURL   string   `json:"base_url,omitempty"`   // API Base URL
	Models    []string `json:"models,omitempty"`     // 可选模型列表，供 TUI 切换时展示
	// ExtraBody 透传给该 provider 每次请求的额外参数（如 temperature/top_p/min_p/
	// presence_penalty，或厂商特有键如 nvidia 开 think 的 chat_template_kwargs）。
	// OpenAI 兼容端逐字并入请求体（即 extra_body 约定）；值由用户自负其责。
	ExtraBody map[string]any `json:"extra_body,omitempty"`
	// Extra 透传给 provider 级配置（litellm.ProviderConfig.Extra），用于 HTTP
	// headers、user_agent、anthropic_beta 等客户端/传输层选项。
	Extra map[string]any `json:"extra,omitempty"`
}

const ProviderAuthGrokOAuth = "grok_oauth"

// RequiresAPIKey 返回该 provider 是否必须显式配置 api_key。
// 约定：
// 1. ollama / bedrock 允许无 key；
// 2. 显式指定 Type 的配置视为自定义代理，允许无 key；
// 3. 其他 provider 默认要求 key，保持对官方托管接口的保守校验。
func (pc ProviderConfig) RequiresAPIKey(name string) bool {
	if strings.EqualFold(strings.TrimSpace(pc.Auth), ProviderAuthGrokOAuth) {
		return false
	}
	switch name {
	case "ollama", "bedrock":
		return false
	}
	return pc.Type == ""
}

// ProviderType 返回有效的 API 协议类型。
// 优先使用显式 Type；否则要求 provider 名本身已在 litellm 注册表中。
func (pc ProviderConfig) ProviderType(name string) (string, error) {
	if pc.Type != "" {
		return pc.Type, nil
	}
	if llm.IsProviderRegistered(name) {
		return name, nil
	}
	return "", fmt.Errorf("provider %q 缺少 type，且不在 litellm 已知 provider 列表中: %w", name, errs.ErrConfig)
}

// ModelRef 表示一个 provider/model 组合。
type ModelRef struct {
	Provider string `json:"provider"` // provider 名称（Providers map 中的 key）
	Model    string `json:"model"`    // 模型名（原样透传，不做任何解析）
}

// RoleConfig 定义单个角色的模型覆盖。
type RoleConfig struct {
	Provider  string     `json:"provider"`            // 主 provider 名称（Providers map 中的 key）
	Model     string     `json:"model"`               // 主模型名（原样透传，不做任何解析）
	Fallbacks []ModelRef `json:"fallbacks,omitempty"` // 显式备用 provider/model 列表
	// ReasoningEffort 该角色的推理强度（off/low/medium/high/xhigh/max），空=继承顶层默认。
	// 由 agents.ParseThinkingLevel 校验后应用，越级值视为空。
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// knownRoles 支持的角色名。
var knownRoles = map[string]bool{
	"coordinator": true,
	"architect":   true,
	"writer":      true,
	"editor":      true,
}

// Config 小说应用配置。
type Config struct {
	// 运行时字段（不序列化到 JSON）
	OutputDir string `json:"-"` // 输出根目录

	// 默认 LLM 配置
	Provider  string `json:"provider"` // 默认 provider（Providers map 中的 key）
	ModelName string `json:"model"`    // 默认模型名
	// ReasoningEffort 顶层默认推理强度（off/low/medium/high/xhigh/max），空=不覆盖（沿用模型/provider 默认）。
	// 角色未单独配置 reasoning_effort 时回落到此值。
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	// Provider 凭证库
	Providers map[string]ProviderConfig `json:"providers,omitempty"`

	// 角色级模型覆盖
	Roles map[string]RoleConfig `json:"roles,omitempty"`

	// 创作参数
	Style string `json:"style,omitempty"`

	// ContextWindow 上下文压缩使用的窗口大小。留空（0）时按模型名自动解析：
	// registry 命中用模型真实窗口，未命中兜底 DefaultContextWindow。
	// 显式配置则优先生效——用于给 registry 查不到的自定义模型指定真实窗口，
	// 或把大窗口模型钉在更小的值上提前触发压缩（1M 名义窗口在 200k+ 通常已注意力衰退）。
	// 仅影响压缩阈值，不改变 LLM API 实际请求长度；配置值由用户自负其责。
	ContextWindow int `json:"context_window,omitempty"`

	// Budget 单本书的成本预算政策；book_usd > 0 才启用。
	Budget BudgetConfig `json:"budget,omitzero"`

	// Notify 无人值守告警配置；缺省启用（system 通道兜底）。
	Notify NotifyConfig `json:"notify,omitzero"`
}

// BudgetConfig 是用户对单本书钱包的政策声明。越线停机等同于用户在那一刻
// 手动 Abort——Host 只代为执行，不评估模型行为（架构 §10 合宪边界）。
type BudgetConfig struct {
	BookUSD   float64 `json:"book_usd,omitempty"`   // 必填才启用；0/缺省 = 不限
	WarnRatio float64 `json:"warn_ratio,omitempty"` // 告警水位，默认 0.8
	HardStop  bool    `json:"hard_stop,omitempty"`  // true=越线立即停；默认等当前子代理任务结束
}

// Enabled 返回预算政策是否启用。
func (b BudgetConfig) Enabled() bool { return b.BookUSD > 0 }

// NotifyConfig 无人值守告警通道配置。
type NotifyConfig struct {
	Enabled *bool    `json:"enabled,omitempty"` // 缺省 true（system 通道零配置可用）
	Command string   `json:"command,omitempty"` // 可选，配置后替代 system 通道（手机推送走这里）
	Events  []string `json:"events,omitempty"`  // 可选，过滤 kind（run_end/repeat/budget），缺省全开
}

// IsEnabled 返回告警是否启用（缺省 true）。
func (n NotifyConfig) IsEnabled() bool { return n.Enabled == nil || *n.Enabled }

// ValidateBase 校验基础配置。
func (c *Config) ValidateBase() error {
	if err := validateConfigText("provider", c.Provider); err != nil {
		return err
	}
	if err := validateConfigText("model", c.ModelName); err != nil {
		return err
	}

	if c.Provider == "" {
		return fmt.Errorf("provider is required: %w", errs.ErrConfig)
	}
	if c.ModelName == "" {
		return fmt.Errorf("model is required: %w", errs.ErrConfig)
	}

	// 默认 provider 必须有凭证
	pc, ok := c.Providers[c.Provider]
	if !ok {
		return fmt.Errorf("provider %q 未在 providers 中配置凭证；若在 ./.ainovel/config.json 里覆盖了 provider，需同时声明 providers.%s（含 api_key/base_url），不能只改顶层 provider: %w", c.Provider, c.Provider, errs.ErrConfig)
	}
	if pc.RequiresAPIKey(c.Provider) && pc.APIKey == "" {
		return fmt.Errorf("provider %q has no api_key configured: %w", c.Provider, errs.ErrConfig)
	}
	if err := validateProviderConfigText(c.Provider, pc); err != nil {
		return err
	}
	if err := c.validateProviderAPI("default", c.Provider, pc); err != nil {
		return err
	}
	for name, provider := range c.Providers {
		if err := validateConfigText("provider name", name); err != nil {
			return err
		}
		if err := validateProviderConfigText(name, provider); err != nil {
			return err
		}
		if err := c.validateProviderAPI(fmt.Sprintf("provider %q", name), name, provider); err != nil {
			return err
		}
	}

	// 校验角色覆盖
	for role, rc := range c.Roles {
		if err := validateConfigText("role name", role); err != nil {
			return err
		}
		if err := validateConfigText(fmt.Sprintf("role %q provider", role), rc.Provider); err != nil {
			return err
		}
		if err := validateConfigText(fmt.Sprintf("role %q model", role), rc.Model); err != nil {
			return err
		}
		if !knownRoles[role] {
			return fmt.Errorf("unknown role %q in roles config (valid: coordinator/architect/writer/editor): %w", role, errs.ErrConfig)
		}
		if rc.Provider == "" || rc.Model == "" {
			return fmt.Errorf("role %q must have both provider and model: %w", role, errs.ErrConfig)
		}
		if err := c.validateModelRef(
			fmt.Sprintf("role %q", role),
			ModelRef{Provider: rc.Provider, Model: rc.Model},
		); err != nil {
			return err
		}
		for i, fallback := range rc.Fallbacks {
			if err := validateConfigText(fmt.Sprintf("role %q fallback[%d] provider", role, i), fallback.Provider); err != nil {
				return err
			}
			if err := validateConfigText(fmt.Sprintf("role %q fallback[%d] model", role, i), fallback.Model); err != nil {
				return err
			}
			if err := c.validateModelRef(
				fmt.Sprintf("role %q fallback[%d]", role, i),
				fallback,
			); err != nil {
				return err
			}
		}
	}

	// 校验预算政策
	if c.Budget.BookUSD < 0 {
		return fmt.Errorf("budget.book_usd must be >= 0: %w", errs.ErrConfig)
	}
	if c.Budget.Enabled() && (c.Budget.WarnRatio <= 0 || c.Budget.WarnRatio >= 1) {
		return fmt.Errorf("budget.warn_ratio must be in (0, 1): %w", errs.ErrConfig)
	}

	// 校验告警配置
	if err := validateConfigText("notify.command", c.Notify.Command); err != nil {
		return err
	}
	for _, ev := range c.Notify.Events {
		if !knownNotifyEvents[ev] {
			return fmt.Errorf("unknown notify event %q (valid: run_end/repeat/budget): %w", ev, errs.ErrConfig)
		}
	}

	return nil
}

var knownNotifyEvents = map[string]bool{"run_end": true, "repeat": true, "budget": true}

func validateProviderConfigText(name string, pc ProviderConfig) error {
	fields := []struct {
		label string
		value string
	}{
		{label: fmt.Sprintf("provider %q type", name), value: pc.Type},
		{label: fmt.Sprintf("provider %q auth", name), value: pc.Auth},
		{label: fmt.Sprintf("provider %q account_id", name), value: pc.AccountID},
		{label: fmt.Sprintf("provider %q api", name), value: pc.API},
		{label: fmt.Sprintf("provider %q api_key", name), value: pc.APIKey},
		{label: fmt.Sprintf("provider %q base_url", name), value: pc.BaseURL},
	}
	for _, field := range fields {
		if err := validateConfigText(field.label, field.value); err != nil {
			return err
		}
	}
	for i, model := range pc.Models {
		if err := validateConfigText(fmt.Sprintf("provider %q models[%d]", name, i), model); err != nil {
			return err
		}
	}
	switch pc.API {
	case "", "chat", "responses":
	default:
		return fmt.Errorf("provider %q api must be chat or responses: %w", name, errs.ErrConfig)
	}
	switch auth := strings.ToLower(strings.TrimSpace(pc.Auth)); auth {
	case "", "api_key":
	case ProviderAuthGrokOAuth:
		providerType, err := pc.ProviderType(name)
		if err != nil {
			return fmt.Errorf("provider %q auth %q 无法解析 provider type: %w", name, auth, err)
		}
		if strings.ToLower(strings.TrimSpace(providerType)) != "grok" {
			return fmt.Errorf("provider %q auth %q only supports grok type: %w", name, auth, errs.ErrConfig)
		}
		if pc.BaseURL != "" && !isGrokOAuthBaseURL(pc.BaseURL) {
			return fmt.Errorf("provider %q grok_oauth base_url must be https://api.x.ai/v1 or empty: %w", name, errs.ErrConfig)
		}
	default:
		return fmt.Errorf("provider %q auth must be api_key or grok_oauth: %w", name, errs.ErrConfig)
	}
	return nil
}

func isGrokOAuthBaseURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return parsed.Scheme == "https" && strings.EqualFold(parsed.Hostname(), "api.x.ai")
}

func validateConfigText(name, value string) error {
	if utils.ContainsControl(value) {
		return fmt.Errorf("%s contains control character: %w", name, errs.ErrConfig)
	}
	return nil
}

// DefaultProviderConfig 返回默认 provider 的凭证配置。
func (c *Config) DefaultProviderConfig() ProviderConfig {
	if c.Providers == nil {
		return ProviderConfig{}
	}
	return c.Providers[c.Provider]
}

// FillDefaults 填充默认值。
func (c *Config) FillDefaults() {
	if c.OutputDir == "" {
		c.OutputDir = filepath.Join("output", "novel")
	}
	if c.Providers == nil {
		c.Providers = make(map[string]ProviderConfig)
	}
	if c.Roles == nil {
		c.Roles = make(map[string]RoleConfig)
	}
	if c.Style == "" {
		c.Style = "default"
	}
	if c.Budget.Enabled() && c.Budget.WarnRatio == 0 {
		c.Budget.WarnRatio = 0.8
	}
}

// ContextWindowSource 标记窗口取值的来源，供日志/诊断使用。
type ContextWindowSource string

const (
	CtxWindowConfig   ContextWindowSource = "config"   // 配置文件 context_window 显式指定
	CtxWindowRegistry ContextWindowSource = "registry" // OpenRouter 基线命中
	CtxWindowDefault  ContextWindowSource = "default"  // 兜底（自定义代理/未知模型）
)

// ResolveContextWindow 解析上下文压缩使用的有效窗口，按优先级：
//  1. 配置文件 ContextWindow > 0 → 直接用（最高优先级，可超过模型真窗口）
//  2. models.DefaultRegistry 按模型名查询（OpenRouter 基线 + 24h 刷新）
//  3. 兜底 DefaultContextWindow（自定义代理 / 未知模型）
//
// 注意：返回值仅用于压缩阈值计算，不会缩小 LLM API 真实可发请求长度。
func (c Config) ResolveContextWindow(modelName string) (int, ContextWindowSource) {
	if c.ContextWindow > 0 {
		return c.ContextWindow, CtxWindowConfig
	}
	if rw := models.DefaultRegistry().ResolveContextWindow(modelName); rw > 0 {
		return rw, CtxWindowRegistry
	}
	return DefaultContextWindow, CtxWindowDefault
}

// ResolveReasoningEffort 返回某角色生效的推理强度原始串（off/low/medium/high/xhigh/max 或空）。
// 优先级：角色级 Roles[role].ReasoningEffort → 顶层默认 ReasoningEffort → ""（不覆盖，沿用模型/provider 默认）。
// role 为空或 "default" 时直接取顶层默认。值的合法性由 agents.ParseThinkingLevel 把关。
func (c Config) ResolveReasoningEffort(role string) string {
	if role != "" && role != "default" {
		if rc, ok := c.Roles[role]; ok && rc.ReasoningEffort != "" {
			return rc.ReasoningEffort
		}
	}
	return c.ReasoningEffort
}

// LogContextWindowChoice 打印某个角色的窗口决策。source=default 时发 Warn 提示
// 该模型未在 registry 命中（OpenRouter 也未收录），后续上下文压缩会按兜底窗口
// 触发——若模型实际窗口更大，可在配置文件用 context_window 显式指定，避免被提前压缩、丢史。
func LogContextWindowChoice(role, model string, window int, source ContextWindowSource) {
	attrs := []any{"module", "context", "role", role, "model", model, "window", window, "source", source}
	switch source {
	case CtxWindowDefault:
		slog.Warn("未识别的模型，使用兜底窗口（自定义代理或 OpenRouter 未收录，可用 context_window 显式指定）", attrs...)
	case CtxWindowConfig:
		slog.Info("上下文窗口（来自配置文件 context_window）", attrs...)
	default:
		slog.Info("上下文窗口", attrs...)
	}
}

// CandidateModels 返回某个 provider 下可供切换的模型列表。
// 优先使用 provider 显式声明的 models；同时补充当前配置中已出现过的该 provider 模型。
func (c Config) CandidateModels(provider string) []string {
	if provider == "" {
		return nil
	}

	seen := make(map[string]bool)
	models := make([]string, 0, 4)
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			return
		}
		seen[model] = true
		models = append(models, model)
	}

	if pc, ok := c.Providers[provider]; ok {
		for _, model := range pc.Models {
			add(model)
		}
	}
	if c.Provider == provider {
		add(c.ModelName)
	}
	for _, rc := range c.Roles {
		if rc.Provider == provider {
			add(rc.Model)
		}
		for _, fallback := range rc.Fallbacks {
			if fallback.Provider == provider {
				add(fallback.Model)
			}
		}
	}
	return models
}

// RememberModelCandidate persists a model under its provider so /model can keep
// switching back to models that were selected earlier.
func (c *Config) RememberModelCandidate(provider, model string) {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return
	}
	if c.Providers == nil {
		c.Providers = make(map[string]ProviderConfig)
	}
	pc := c.Providers[provider]
	for _, existing := range pc.Models {
		if strings.TrimSpace(existing) == model {
			return
		}
	}
	pc.Models = append(pc.Models, model)
	c.Providers[provider] = pc
}

func (c Config) validateModelRef(owner string, ref ModelRef) error {
	if ref.Provider == "" || ref.Model == "" {
		return fmt.Errorf("%s must have both provider and model: %w", owner, errs.ErrConfig)
	}

	pc, ok := c.Providers[ref.Provider]
	if !ok {
		return fmt.Errorf("%s references provider %q which is not configured: %w", owner, ref.Provider, errs.ErrConfig)
	}
	if pc.RequiresAPIKey(ref.Provider) && pc.APIKey == "" {
		return fmt.Errorf("%s references provider %q which has no api_key: %w", owner, ref.Provider, errs.ErrConfig)
	}
	if err := c.validateProviderAPI(owner, ref.Provider, pc); err != nil {
		return err
	}
	return nil
}

func (c Config) validateProviderAPI(owner, providerName string, pc ProviderConfig) error {
	if pc.API == "" {
		return nil
	}
	providerType, err := pc.ProviderType(providerName)
	if err != nil {
		return fmt.Errorf("%s provider %q api 配置无法解析协议类型: %w", owner, providerName, err)
	}
	if strings.ToLower(strings.TrimSpace(providerType)) != "openai" {
		return fmt.Errorf("%s provider %q api 仅支持 OpenAI 协议 provider: %w", owner, providerName, errs.ErrConfig)
	}
	return nil
}
