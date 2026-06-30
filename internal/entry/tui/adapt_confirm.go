package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
)

type adaptConfirmStep int

const (
	adaptConfirmGranularity adaptConfirmStep = iota
	adaptConfirmRewritePolicy
)

type adaptModeConfirmState struct {
	sourcePath    string
	step          adaptConfirmStep
	granularity   int
	rewritePolicy int
}

type adaptChoice struct {
	value string
	label string
	desc  string
}

var adaptGranularityChoices = []adaptChoice{
	{domain.AdaptationGranularityChapter, "chapter", "目标章节与原文章节一一对应"},
	{domain.AdaptationGranularityArc, "arc", "允许合并/拆分，但每章保留来源范围"},
	{domain.AdaptationGranularityFree, "free", "允许重构章节结构，但保留全书覆盖表"},
}

var adaptRewriteChoices = []adaptChoice{
	{domain.AdaptationRewritePreserveDetails, "preserve_details", "未受影响内容可复用原文，字数为硬契约"},
	{domain.AdaptationRewriteFullRewrite, "full_rewrite", "完全重写，不强制贴近原文字数"},
}

func newAdaptModeConfirmState(sourcePath string) *adaptModeConfirmState {
	return &adaptModeConfirmState{
		sourcePath:    strings.TrimSpace(sourcePath),
		granularity:   0,
		rewritePolicy: 0,
	}
}

func (s *adaptModeConfirmState) selectedGranularity() string {
	return adaptGranularityChoices[s.granularity].value
}

func (s *adaptModeConfirmState) selectedRewritePolicy() string {
	return adaptRewriteChoices[s.rewritePolicy].value
}

func (s *adaptModeConfirmState) selectedTolerance() float64 {
	return startup.DefaultAdaptationWordTolerance
}

func (m Model) handleAdaptModeConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	state := m.adaptConfirm
	if state == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		if state.step == adaptConfirmRewritePolicy {
			state.step = adaptConfirmGranularity
			return m, nil
		}
		m.adaptConfirm = nil
		m.resizeTextarea()
		m.textarea.SetValue(state.sourcePath)
		m.setTextareaPlaceholder(placeholderForNewMode(startupModeAdapt))
		return m, m.textarea.Focus()
	case tea.KeyUp:
		state.moveChoice(-1)
		return m, nil
	case tea.KeyDown, tea.KeyTab:
		state.moveChoice(1)
		return m, nil
	case tea.KeyLeft:
		state.moveChoice(-1)
		return m, nil
	case tea.KeyRight:
		state.moveChoice(1)
		return m, nil
	case tea.KeyRunes:
		if len(msg.Runes) == 1 {
			if final := state.selectNumber(msg.Runes[0]); final {
				return m.startAdaptCoCreateWithMode(state)
			}
		}
		return m, nil
	case tea.KeyEnter, tea.KeyCtrlS:
		if state.step == adaptConfirmGranularity {
			state.step = adaptConfirmRewritePolicy
			return m, nil
		}
		return m.startAdaptCoCreateWithMode(state)
	}
	return m, nil
}

func (m Model) startAdaptCoCreateWithMode(state *adaptModeConfirmState) (tea.Model, tea.Cmd) {
	m.adaptConfirm = nil
	m.err = nil
	m.cocreate = newAdaptCoCreateStateWithOptions(
		state.sourcePath,
		state.selectedGranularity(),
		state.selectedRewritePolicy(),
		state.selectedTolerance(),
	)
	m.textarea.Blur()
	return m, m.sendCoCreate()
}

func (s *adaptModeConfirmState) moveChoice(delta int) {
	switch s.step {
	case adaptConfirmGranularity:
		s.granularity = wrapIndex(s.granularity+delta, len(adaptGranularityChoices))
	case adaptConfirmRewritePolicy:
		s.rewritePolicy = wrapIndex(s.rewritePolicy+delta, len(adaptRewriteChoices))
	}
}

func (s *adaptModeConfirmState) selectNumber(value rune) bool {
	idx := int(value - '1')
	switch s.step {
	case adaptConfirmGranularity:
		if idx < 0 || idx >= len(adaptGranularityChoices) {
			return false
		}
		s.granularity = idx
		s.step = adaptConfirmRewritePolicy
		return false
	case adaptConfirmRewritePolicy:
		if idx < 0 || idx >= len(adaptRewriteChoices) {
			return false
		}
		s.rewritePolicy = idx
		return true
	default:
		return false
	}
}

func (s *adaptModeConfirmState) buildPlan() (startup.Plan, error) {
	if s == nil {
		return startup.Plan{}, fmt.Errorf("adaptation confirmation state missing")
	}
	brief := startup.DefaultAdaptationBrief(s.selectedGranularity(), s.selectedRewritePolicy(), s.selectedTolerance())
	return startup.PrepareAdaptNovel(startup.Request{
		Mode:               startup.ModeAdaptNovel,
		UserPrompt:         brief,
		NovelPath:          s.sourcePath,
		AdaptGranularity:   s.selectedGranularity(),
		AdaptRewritePolicy: s.selectedRewritePolicy(),
		AdaptWordTolerance: s.selectedTolerance(),
	})
}

func wrapIndex(value, length int) int {
	if length <= 0 {
		return 0
	}
	for value < 0 {
		value += length
	}
	return value % length
}

func renderAdaptModeConfirmModal(width, height int, state *adaptModeConfirmState) string {
	boxW, boxH := reportModalSize(width, height)
	if boxW > 96 {
		boxW = 96
	}
	if boxH > 24 {
		boxH = 24
	}
	contentW := paddedModalContentWidth(boxW)

	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	bodyStyle := lipgloss.NewStyle().Foreground(bodyTextColor)

	var lines []string
	if state.step == adaptConfirmGranularity {
		lines = append(lines, titleStyle.Render("第 1 步：选择章节结构"))
	} else {
		lines = append(lines, titleStyle.Render("第 2 步：选择改写策略"))
	}
	lines = append(lines, "")
	lines = append(lines, bodyStyle.Render("小说准备完成后只做固定模式选择，不再让 AI 追问这些必选项。"))
	lines = append(lines, "")
	if state.step == adaptConfirmGranularity {
		lines = append(lines, renderAdaptOptionRows(adaptGranularityChoices, state.granularity, contentW)...)
		lines = append(lines, "")
		lines = append(lines, dimStyle.Render("默认选中 chapter。↑↓/←→ 切换 · 1/2/3 直选 · Enter 下一步 · Esc 返回路径输入"))
	} else {
		lines = append(lines, dimStyle.Render("已选结构粒度："+state.selectedGranularity()))
		lines = append(lines, "")
		lines = append(lines, renderAdaptOptionRows(adaptRewriteChoices, state.rewritePolicy, contentW)...)
		lines = append(lines, "")
		lines = append(lines, dimStyle.Render("默认选中 preserve_details，字数容差固定为 ±15%。↑↓/←→ 切换 · 1/2 直选 · Enter 进入共创 · Esc 返回上一步"))
	}
	lines = append(lines, "")

	modal := renderPaddedModalFrame(boxW, boxH, "小说改编模式确认", "",
		strings.Split(lipgloss.NewStyle().Width(contentW).Render(strings.Join(lines, "\n")), "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

func renderAdaptOptionRows(choices []adaptChoice, selected int, width int) []string {
	lines := make([]string, 0, len(choices))
	for i, choice := range choices {
		lines = append(lines, renderAdaptChoiceRow(fmt.Sprintf("%d. %s", i+1, choice.label), choice.desc, i == selected, width))
	}
	return lines
}

func renderAdaptChoiceRow(label, desc string, active bool, width int) string {
	prefix := "  "
	labelStyle := lipgloss.NewStyle().Foreground(colorMuted)
	descStyle := lipgloss.NewStyle().Foreground(colorDim)
	if active {
		prefix = "> "
		labelStyle = labelStyle.Foreground(colorAccent2).Bold(true)
	}
	line := prefix + labelStyle.Render(label)
	if desc != "" {
		line += "\n  " + descStyle.Render(wrapText(desc, max(20, width-4)))
	}
	return line
}
