package tui

import (
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/grokauth"
)

type modelAddMode int

const (
	modelAddExisting modelAddMode = iota
	modelAddPreset
	modelAddCustom
	modelAddGrokOAuth
)

type modelAddStep int

const (
	addStepMode modelAddStep = iota
	addStepRole
	addStepExistingProvider
	addStepExistingModel
	addStepPreset
	addStepPresetProviderKey
	addStepPresetAPIKey
	addStepPresetBaseURL
	addStepPresetModel
	addStepCustomProviderKey
	addStepCustomProtocol
	addStepCustomAPI
	addStepCustomAPIKey
	addStepCustomBaseURL
	addStepCustomModel
	addStepGrokProviderKey
	addStepGrokAccountID
	addStepGrokAccountName
	addStepGrokModel
	addStepGrokLogin
)

type providerPreset struct {
	Label        string
	ProviderKey  string
	Type         string
	BaseURL      string
	API          string
	DefaultModel string
	RequiresKey  bool
}

var modelAddModeLabels = []string{"已有 provider", "内置 provider", "Custom Proxy", "Grok 登录"}

var modelAddPresets = []providerPreset{
	{Label: "DeepSeek", ProviderKey: "deepseek", Type: "deepseek", DefaultModel: "deepseek-chat", RequiresKey: true},
	{Label: "OpenAI", ProviderKey: "openai", Type: "openai", DefaultModel: "gpt-4.1", RequiresKey: true},
	{Label: "Anthropic", ProviderKey: "anthropic", Type: "anthropic", DefaultModel: "claude-sonnet-4-5", RequiresKey: true},
	{Label: "Gemini", ProviderKey: "gemini", Type: "gemini", DefaultModel: "gemini-2.5-pro", RequiresKey: true},
	{Label: "Qwen", ProviderKey: "qwen", Type: "qwen", DefaultModel: "qwen-max", RequiresKey: true},
	{Label: "GLM", ProviderKey: "glm", Type: "glm", DefaultModel: "glm-4.5", RequiresKey: true},
	{Label: "OpenRouter", ProviderKey: "openrouter", Type: "openrouter", DefaultModel: "openai/gpt-4.1", RequiresKey: true},
	{Label: "Grok API Key", ProviderKey: "grok", Type: "grok", DefaultModel: "grok-4.3-latest", RequiresKey: true},
	{Label: "Ollama", ProviderKey: "ollama", Type: "ollama", BaseURL: "http://localhost:11434", DefaultModel: "qwen3:8b"},
}

var customProtocolOptions = []string{"openai", "anthropic", "gemini", "grok"}
var customAPIOptions = []string{"chat", "responses"}

type modelAddState struct {
	step modelAddStep

	roleIdx     int
	modeIdx     int
	providerIdx int
	presetIdx   int
	providers   []string

	customProtocolIdx int
	customAPIIdx      int

	providerKey string
	apiKey      string
	baseURL     string
	modelName   string
	accountID   string
	accountName string

	grokStarted  bool
	grokComplete bool
	authorizeURL string
	callbackText string

	submitting bool
	message    string
}

type modelAddSubmittedMsg struct{ err error }

type grokLoginStartedMsg struct {
	result grokauth.LoginStart
	err    error
}

type grokLoginPolledMsg struct {
	result grokauth.LoginPoll
	err    error
}

type grokLoginCompletedMsg struct {
	status grokauth.AuthStatus
	err    error
}

func newModelAddState(rt modelRuntime, roleHint string) *modelAddState {
	state := &modelAddState{
		step:      addStepMode,
		providers: rt.ConfiguredProviders(),
		accountID: grokauth.DefaultAccountID,
	}
	roleHint = normalizeRoleKey(roleHint)
	for i, opt := range modelRoleOptions {
		if opt.Key == roleHint {
			state.roleIdx = i
			break
		}
	}
	return state
}

func (m Model) handleModelAddKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.modelAdd == nil {
		return m, nil
	}
	state := m.modelAdd
	switch msg.Type {
	case tea.KeyEsc:
		m.modelAdd = nil
		return m, m.textarea.Focus()
	case tea.KeyEnter, tea.KeyTab:
		return m, state.advance(m.runtime)
	case tea.KeyShiftTab:
		state.back()
		return m, nil
	case tea.KeyLeft, tea.KeyUp:
		state.cycle(-1)
		return m, nil
	case tea.KeyRight, tea.KeyDown:
		state.cycle(1)
		return m, nil
	case tea.KeyBackspace, tea.KeyCtrlH:
		state.deleteInput()
		return m, nil
	case tea.KeyCtrlU:
		state.clearInput()
		return m, nil
	case tea.KeySpace:
		state.appendInput(" ")
		return m, nil
	case tea.KeyRunes:
		state.appendInput(msg.String())
		return m, nil
	default:
		return m, nil
	}
}

func (m Model) handleModelAddMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	if m.modelAdd == nil {
		return m, nil, false
	}
	state := m.modelAdd
	switch msg := msg.(type) {
	case modelAddSubmittedMsg:
		state.submitting = false
		if msg.err != nil {
			state.message = msg.err.Error()
			return m, nil, true
		}
		m.modelAdd = nil
		return m, tea.Batch(m.textarea.Focus(), fetchSnapshot(m.runtime)), true
	case grokLoginStartedMsg:
		state.submitting = false
		if msg.err != nil {
			state.message = msg.err.Error()
			return m, nil, true
		}
		state.grokStarted = true
		state.authorizeURL = msg.result.AuthorizeURL
		state.message = "浏览器登录已启动；粘贴 xAI 页面显示的一次性代码后按 Enter"
		return m, nil, true
	case grokLoginPolledMsg:
		state.submitting = false
		if msg.err != nil {
			state.message = msg.err.Error()
			return m, nil, true
		}
		if msg.result.Done {
			state.grokComplete = true
			return m, state.submit(m.runtime), true
		}
		if msg.result.Message != "" {
			state.message = msg.result.Message
		} else {
			state.message = "还没有收到 Grok 登录结果；请粘贴 xAI 页面显示的一次性代码"
		}
		return m, nil, true
	case grokLoginCompletedMsg:
		state.submitting = false
		if msg.err != nil {
			state.message = msg.err.Error()
			return m, nil, true
		}
		state.grokComplete = true
		state.callbackText = ""
		return m, state.submit(m.runtime), true
	default:
		return m, nil, false
	}
}

func (s *modelAddState) advance(rt modelRuntime) tea.Cmd {
	s.message = ""
	switch s.step {
	case addStepMode:
		s.step = addStepRole
	case addStepRole:
		s.step = s.firstModeStep()
	case addStepExistingProvider:
		if len(s.providers) == 0 {
			s.message = "当前没有可用 provider"
			return nil
		}
		s.step = addStepExistingModel
	case addStepExistingModel:
		if !s.requireText(s.modelName, "模型名") {
			return nil
		}
		return s.submit(rt)
	case addStepPreset:
		s.applyPresetDefaults(false)
		s.step = addStepPresetProviderKey
	case addStepPresetProviderKey:
		if !s.requireText(s.providerKey, "provider key") {
			return nil
		}
		s.step = addStepPresetAPIKey
	case addStepPresetAPIKey:
		if s.preset().RequiresKey && !s.requireText(s.apiKey, "API key") {
			return nil
		}
		s.step = addStepPresetBaseURL
	case addStepPresetBaseURL:
		s.step = addStepPresetModel
	case addStepPresetModel:
		if !s.requireText(s.modelName, "模型名") {
			return nil
		}
		return s.submit(rt)
	case addStepCustomProviderKey:
		if !s.requireText(s.providerKey, "provider key") {
			return nil
		}
		s.step = addStepCustomProtocol
	case addStepCustomProtocol:
		s.step = addStepCustomAPI
	case addStepCustomAPI:
		s.step = addStepCustomAPIKey
	case addStepCustomAPIKey:
		s.step = addStepCustomBaseURL
	case addStepCustomBaseURL:
		if !s.requireText(s.baseURL, "Base URL") {
			return nil
		}
		s.step = addStepCustomModel
	case addStepCustomModel:
		if !s.requireText(s.modelName, "模型名") {
			return nil
		}
		return s.submit(rt)
	case addStepGrokProviderKey:
		if !s.requireText(s.providerKey, "provider key") {
			return nil
		}
		s.step = addStepGrokAccountID
	case addStepGrokAccountID:
		if strings.TrimSpace(s.accountID) == "" {
			s.accountID = grokauth.DefaultAccountID
		}
		s.step = addStepGrokAccountName
	case addStepGrokAccountName:
		s.step = addStepGrokModel
	case addStepGrokModel:
		if !s.requireText(s.modelName, "模型名") {
			return nil
		}
		status := rt.GrokLoginStatus(s.accountID)
		if status.LoggedIn {
			s.grokComplete = true
			return s.submit(rt)
		}
		s.step = addStepGrokLogin
	case addStepGrokLogin:
		if s.grokComplete {
			return s.submit(rt)
		}
		if !s.grokStarted {
			return s.startGrok(rt)
		}
		if strings.TrimSpace(s.callbackText) != "" {
			return s.completeGrok(rt)
		}
		return s.pollGrok(rt)
	}
	return nil
}

func (s *modelAddState) back() {
	s.message = ""
	switch s.step {
	case addStepMode:
	case addStepRole:
		s.step = addStepMode
	case addStepExistingProvider, addStepPreset, addStepCustomProviderKey, addStepGrokProviderKey:
		s.step = addStepRole
	case addStepExistingModel:
		s.step = addStepExistingProvider
	case addStepPresetProviderKey:
		s.step = addStepPreset
	case addStepPresetAPIKey:
		s.step = addStepPresetProviderKey
	case addStepPresetBaseURL:
		s.step = addStepPresetAPIKey
	case addStepPresetModel:
		s.step = addStepPresetBaseURL
	case addStepCustomProtocol:
		s.step = addStepCustomProviderKey
	case addStepCustomAPI:
		s.step = addStepCustomProtocol
	case addStepCustomAPIKey:
		s.step = addStepCustomAPI
	case addStepCustomBaseURL:
		s.step = addStepCustomAPIKey
	case addStepCustomModel:
		s.step = addStepCustomBaseURL
	case addStepGrokAccountID:
		s.step = addStepGrokProviderKey
	case addStepGrokAccountName:
		s.step = addStepGrokAccountID
	case addStepGrokModel:
		s.step = addStepGrokAccountName
	case addStepGrokLogin:
		s.step = addStepGrokModel
	}
}

func (s *modelAddState) cycle(delta int) {
	s.message = ""
	switch s.step {
	case addStepMode:
		s.modeIdx = wrapIndex(s.modeIdx+delta, len(modelAddModeLabels))
		s.applyModeDefaults()
	case addStepRole:
		s.roleIdx = wrapIndex(s.roleIdx+delta, len(modelRoleOptions))
	case addStepExistingProvider:
		s.providerIdx = wrapIndex(s.providerIdx+delta, len(s.providers))
	case addStepPreset:
		s.presetIdx = wrapIndex(s.presetIdx+delta, len(modelAddPresets))
		s.applyPresetDefaults(true)
	case addStepCustomProtocol:
		s.customProtocolIdx = wrapIndex(s.customProtocolIdx+delta, len(customProtocolOptions))
	case addStepCustomAPI:
		s.customAPIIdx = wrapIndex(s.customAPIIdx+delta, len(customAPIOptions))
	}
}

func (s *modelAddState) firstModeStep() modelAddStep {
	switch modelAddMode(s.modeIdx) {
	case modelAddExisting:
		return addStepExistingProvider
	case modelAddPreset:
		return addStepPreset
	case modelAddCustom:
		return addStepCustomProviderKey
	case modelAddGrokOAuth:
		return addStepGrokProviderKey
	default:
		return addStepExistingProvider
	}
}

func (s *modelAddState) submit(rt modelRuntime) tea.Cmd {
	provider, config, model, err := s.registration()
	if err != nil {
		s.message = err.Error()
		return nil
	}
	s.submitting = true
	return func() tea.Msg {
		return modelAddSubmittedMsg{err: rt.AddProviderModel(s.role(), provider, config, model)}
	}
}

func (s *modelAddState) startGrok(rt modelRuntime) tea.Cmd {
	s.submitting = true
	return func() tea.Msg {
		result, err := rt.StartGrokLogin(s.accountID, s.accountName)
		if err == nil && result.AuthorizeURL != "" {
			openBrowser(result.AuthorizeURL)
		}
		return grokLoginStartedMsg{result: result, err: err}
	}
}

func (s *modelAddState) pollGrok(rt modelRuntime) tea.Cmd {
	s.submitting = true
	return func() tea.Msg {
		result, err := rt.PollGrokLogin()
		return grokLoginPolledMsg{result: result, err: err}
	}
}

func (s *modelAddState) completeGrok(rt modelRuntime) tea.Cmd {
	callback := strings.TrimSpace(s.callbackText)
	s.submitting = true
	return func() tea.Msg {
		status, err := rt.CompleteGrokLogin(callback)
		return grokLoginCompletedMsg{status: status, err: err}
	}
}

func (s *modelAddState) registration() (string, bootstrap.ProviderConfig, string, error) {
	switch modelAddMode(s.modeIdx) {
	case modelAddExisting:
		return s.provider(), bootstrap.ProviderConfig{}, strings.TrimSpace(s.modelName), nil
	case modelAddPreset:
		preset := s.preset()
		return strings.TrimSpace(s.providerKey), bootstrap.ProviderConfig{
			Type:    preset.Type,
			API:     preset.API,
			APIKey:  strings.TrimSpace(s.apiKey),
			BaseURL: strings.TrimSpace(s.baseURL),
			Models:  []string{strings.TrimSpace(s.modelName)},
		}, strings.TrimSpace(s.modelName), nil
	case modelAddCustom:
		protocol := s.customProtocol()
		api := ""
		if protocol == "openai" {
			api = s.customAPI()
		}
		return strings.TrimSpace(s.providerKey), bootstrap.ProviderConfig{
			Type:    protocol,
			API:     api,
			APIKey:  strings.TrimSpace(s.apiKey),
			BaseURL: strings.TrimSpace(s.baseURL),
			Models:  []string{strings.TrimSpace(s.modelName)},
		}, strings.TrimSpace(s.modelName), nil
	case modelAddGrokOAuth:
		return strings.TrimSpace(s.providerKey), bootstrap.ProviderConfig{
			Type:      "grok",
			Auth:      bootstrap.ProviderAuthGrokOAuth,
			AccountID: strings.TrimSpace(s.accountID),
			BaseURL:   "",
			Models:    []string{strings.TrimSpace(s.modelName)},
		}, strings.TrimSpace(s.modelName), nil
	default:
		return "", bootstrap.ProviderConfig{}, "", nil
	}
}

func (s *modelAddState) applyModeDefaults() {
	switch modelAddMode(s.modeIdx) {
	case modelAddPreset:
		s.applyPresetDefaults(true)
	case modelAddCustom:
		if s.providerKey == "" || s.providerKey == "grok-oauth" || isPresetProviderKey(s.providerKey) {
			s.providerKey = "custom-openai"
		}
		if s.modelName == "" || s.modelName == "grok-4.3-latest" {
			s.modelName = "model-name"
		}
	case modelAddGrokOAuth:
		if s.providerKey == "" || strings.HasPrefix(s.providerKey, "custom-") || isPresetProviderKey(s.providerKey) {
			s.providerKey = "grok-oauth"
		}
		if s.modelName == "" || s.modelName == "model-name" || !strings.Contains(strings.ToLower(s.modelName), "grok") {
			s.modelName = "grok-4.3-latest"
		}
	}
}

func (s *modelAddState) applyPresetDefaults(force bool) {
	preset := s.preset()
	if force || s.providerKey == "" || strings.HasPrefix(s.providerKey, "custom-") || s.providerKey == "grok-oauth" {
		s.providerKey = preset.ProviderKey
	}
	if force || s.baseURL == "" {
		s.baseURL = preset.BaseURL
	}
	if force || s.modelName == "" || s.modelName == "model-name" || s.modelName == "grok-4.3-latest" {
		s.modelName = preset.DefaultModel
	}
}

func isPresetProviderKey(key string) bool {
	for _, preset := range modelAddPresets {
		if key == preset.ProviderKey {
			return true
		}
	}
	return false
}

func (s *modelAddState) requireText(value, label string) bool {
	if strings.TrimSpace(value) != "" {
		return true
	}
	s.message = label + "不能为空"
	return false
}

func (s *modelAddState) appendInput(value string) {
	input := s.currentInput()
	if input == nil {
		return
	}
	*input += value
}

func (s *modelAddState) deleteInput() {
	input := s.currentInput()
	if input == nil || *input == "" {
		return
	}
	runes := []rune(*input)
	*input = string(runes[:len(runes)-1])
}

func (s *modelAddState) clearInput() {
	input := s.currentInput()
	if input == nil {
		return
	}
	*input = ""
}

func (s *modelAddState) currentInput() *string {
	switch s.step {
	case addStepExistingModel, addStepPresetModel, addStepCustomModel, addStepGrokModel:
		return &s.modelName
	case addStepPresetProviderKey, addStepCustomProviderKey, addStepGrokProviderKey:
		return &s.providerKey
	case addStepPresetAPIKey, addStepCustomAPIKey:
		return &s.apiKey
	case addStepPresetBaseURL, addStepCustomBaseURL:
		return &s.baseURL
	case addStepGrokAccountID:
		return &s.accountID
	case addStepGrokAccountName:
		return &s.accountName
	case addStepGrokLogin:
		return &s.callbackText
	default:
		return nil
	}
}

func (s *modelAddState) role() string {
	if s.roleIdx < 0 || s.roleIdx >= len(modelRoleOptions) {
		return "default"
	}
	return modelRoleOptions[s.roleIdx].Key
}

func (s *modelAddState) roleLabel() string {
	if s.roleIdx < 0 || s.roleIdx >= len(modelRoleOptions) {
		return "默认"
	}
	return modelRoleOptions[s.roleIdx].Label
}

func (s *modelAddState) provider() string {
	if len(s.providers) == 0 || s.providerIdx < 0 || s.providerIdx >= len(s.providers) {
		return ""
	}
	return s.providers[s.providerIdx]
}

func (s *modelAddState) preset() providerPreset {
	if s.presetIdx < 0 || s.presetIdx >= len(modelAddPresets) {
		return modelAddPresets[0]
	}
	return modelAddPresets[s.presetIdx]
}

func (s *modelAddState) customProtocol() string {
	if s.customProtocolIdx < 0 || s.customProtocolIdx >= len(customProtocolOptions) {
		return customProtocolOptions[0]
	}
	return customProtocolOptions[s.customProtocolIdx]
}

func (s *modelAddState) customAPI() string {
	if s.customAPIIdx < 0 || s.customAPIIdx >= len(customAPIOptions) {
		return customAPIOptions[0]
	}
	return customAPIOptions[s.customAPIIdx]
}

func renderModelAddModal(width, height int, state *modelAddState) string {
	if state == nil {
		return ""
	}
	boxW := width - 10
	if boxW > 84 {
		boxW = 84
	}
	if boxW < 64 {
		boxW = 64
	}
	innerW := boxW - 6
	title := lipgloss.NewStyle().Foreground(colorMuted).Bold(true).Render("/model add 添加模型")
	lines := []string{
		title,
		"",
		renderModelAddChoice("方式", modelAddModeLabels[state.modeIdx], optionPosition(state.modeIdx, len(modelAddModeLabels)), state.step == addStepMode),
		renderModelAddChoice("角色", state.roleLabel(), optionPosition(state.roleIdx, len(modelRoleOptions)), state.step == addStepRole),
	}
	lines = append(lines, state.renderModeRows(innerW)...)
	hint := "←→ 切选项   Enter/Tab 下一步   Shift+Tab 返回   Ctrl+U 清空   Esc 取消"
	if state.submitting {
		hint = "正在处理..."
	}
	lines = append(lines, "", lipgloss.NewStyle().Foreground(colorDim).Italic(true).Render(hint))
	if state.message != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(colorError).Render(truncate(state.message, innerW)))
	}

	body := strings.Join(lines, "\n")
	box := lipgloss.NewStyle().
		Width(boxW).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Render(body)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

func (s *modelAddState) renderModeRows(width int) []string {
	switch modelAddMode(s.modeIdx) {
	case modelAddExisting:
		return []string{
			renderModelAddChoice("Provider", s.provider(), optionPosition(s.providerIdx, len(s.providers)), s.step == addStepExistingProvider),
			renderModelAddInput("模型", s.modelName, false, s.step == addStepExistingModel),
		}
	case modelAddPreset:
		preset := s.preset()
		apiKeyLabel := "API Key"
		if !preset.RequiresKey {
			apiKeyLabel = "API Key(可空)"
		}
		return []string{
			renderModelAddChoice("Provider", preset.Label, optionPosition(s.presetIdx, len(modelAddPresets)), s.step == addStepPreset),
			renderModelAddInput("Key", s.providerKey, false, s.step == addStepPresetProviderKey),
			renderModelAddInput(apiKeyLabel, s.apiKey, true, s.step == addStepPresetAPIKey),
			renderModelAddInput("Base URL", s.baseURL, false, s.step == addStepPresetBaseURL),
			renderModelAddInput("模型", s.modelName, false, s.step == addStepPresetModel),
		}
	case modelAddCustom:
		return []string{
			renderModelAddInput("Key", s.providerKey, false, s.step == addStepCustomProviderKey),
			renderModelAddChoice("协议", s.customProtocol(), optionPosition(s.customProtocolIdx, len(customProtocolOptions)), s.step == addStepCustomProtocol),
			renderModelAddChoice("OpenAI API", s.customAPI(), optionPosition(s.customAPIIdx, len(customAPIOptions)), s.step == addStepCustomAPI),
			renderModelAddInput("API Key(可空)", s.apiKey, true, s.step == addStepCustomAPIKey),
			renderModelAddInput("Base URL", s.baseURL, false, s.step == addStepCustomBaseURL),
			renderModelAddInput("模型", s.modelName, false, s.step == addStepCustomModel),
		}
	case modelAddGrokOAuth:
		rows := []string{
			renderModelAddInput("Key", s.providerKey, false, s.step == addStepGrokProviderKey),
			renderModelAddInput("账号 ID", s.accountID, false, s.step == addStepGrokAccountID),
			renderModelAddInput("账号名", s.accountName, false, s.step == addStepGrokAccountName),
			renderModelAddInput("模型", s.modelName, false, s.step == addStepGrokModel),
		}
		loginValue := "按 Enter 开始登录"
		if s.grokComplete {
			loginValue = "登录完成"
		} else if s.grokStarted {
			loginValue = "粘贴 xAI 页面显示的一次性代码"
		}
		rows = append(rows, renderModelAddChoice("Grok", loginValue, "", s.step == addStepGrokLogin))
		if s.authorizeURL != "" {
			rows = append(rows, lipgloss.NewStyle().Foreground(colorDim).Render("登录链接（不是 callback）："))
			rows = append(rows, lipgloss.NewStyle().Foreground(colorAccent).Render(truncate(s.authorizeURL, width)))
		}
		if s.callbackText != "" {
			rows = append(rows, renderModelAddInput("一次性代码", "<已输入>", false, s.step == addStepGrokLogin))
		}
		return rows
	default:
		return nil
	}
}

func renderModelAddChoice(label, value, position string, focused bool) string {
	if strings.TrimSpace(value) == "" {
		value = "未设置"
	}
	rendered := modelAddLabel(label) + modelAddValue(value, focused)
	if position != "" {
		rendered += " " + lipgloss.NewStyle().Foreground(colorDim).Render(position)
	}
	return rendered
}

func renderModelAddInput(label, value string, secret, focused bool) string {
	if secret && value != "" {
		value = strings.Repeat("*", minInt(10, len([]rune(value))))
	}
	if value == "" {
		value = " "
	}
	return modelAddLabel(label) + modelAddValue(value, focused)
}

func modelAddLabel(label string) string {
	return lipgloss.NewStyle().Foreground(colorMuted).Width(16).Render(label + ":")
}

func modelAddValue(value string, focused bool) string {
	style := lipgloss.NewStyle().Padding(0, 1).Foreground(bodyTextColor)
	if focused {
		style = style.Foreground(colorAccent).Bold(true).Underline(true)
	}
	return style.Render("[" + value + "]")
}

func openBrowser(rawURL string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	case "darwin":
		cmd = exec.Command("open", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	_ = cmd.Start()
}
