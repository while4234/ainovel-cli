package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
)

type adaptModeConfirmState struct {
	cocreate      *cocreateState
	cursor        int
	granularity   int
	rewritePolicy int
	tolerance     int
}

var adaptGranularityChoices = []struct {
	value string
	label string
	desc  string
}{
	{domain.AdaptationGranularityChapter, "chapter", "目标章节与原文章节一一对应"},
	{domain.AdaptationGranularityArc, "arc", "允许合并/拆分，但每章保留来源范围"},
	{domain.AdaptationGranularityFree, "free", "允许重构章节结构，但保留全书覆盖表"},
}

var adaptRewriteChoices = []struct {
	value string
	label string
	desc  string
}{
	{domain.AdaptationRewritePreserveDetails, "preserve_details", "未受影响内容可复用原文，字数为硬契约"},
	{domain.AdaptationRewriteFullRewrite, "full_rewrite", "完全重写，不强制贴近原文字数"},
}

var adaptToleranceChoices = []float64{0.10, 0.15, 0.20, 0.25}

func newAdaptModeConfirmState(c *cocreateState) *adaptModeConfirmState {
	return &adaptModeConfirmState{
		cocreate:      c,
		granularity:   0,
		rewritePolicy: 0,
		tolerance:     1,
	}
}

func (s *adaptModeConfirmState) selectedGranularity() string {
	return adaptGranularityChoices[s.granularity].value
}

func (s *adaptModeConfirmState) selectedRewritePolicy() string {
	return adaptRewriteChoices[s.rewritePolicy].value
}

func (s *adaptModeConfirmState) selectedTolerance() float64 {
	return adaptToleranceChoices[s.tolerance]
}

func (m Model) handleAdaptModeConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	state := m.adaptConfirm
	if state == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.cocreate = state.cocreate
		m.adaptConfirm = nil
		m.resizeTextarea()
		m.textarea.Placeholder = placeholderForCoCreate(m.cocreate)
		return m, m.textarea.Focus()
	case tea.KeyUp:
		if state.cursor > 0 {
			state.cursor--
		}
		return m, nil
	case tea.KeyDown, tea.KeyTab:
		if state.cursor < 2 {
			state.cursor++
		} else {
			state.cursor = 0
		}
		return m, nil
	case tea.KeyLeft:
		state.moveChoice(-1)
		return m, nil
	case tea.KeyRight:
		state.moveChoice(1)
		return m, nil
	case tea.KeyEnter, tea.KeyCtrlS:
		plan, err := state.buildPlan()
		if err != nil {
			m.err = err
			return m, nil
		}
		m.adaptConfirm = nil
		m.err = nil
		return m, startRuntime(m.runtime, plan)
	}
	return m, nil
}

func (s *adaptModeConfirmState) moveChoice(delta int) {
	switch s.cursor {
	case 0:
		s.granularity = wrapIndex(s.granularity+delta, len(adaptGranularityChoices))
	case 1:
		s.rewritePolicy = wrapIndex(s.rewritePolicy+delta, len(adaptRewriteChoices))
	case 2:
		s.tolerance = wrapIndex(s.tolerance+delta, len(adaptToleranceChoices))
	}
}

func (s *adaptModeConfirmState) buildPlan() (startup.Plan, error) {
	if s == nil || s.cocreate == nil {
		return startup.Plan{}, fmt.Errorf("adaptation confirmation state missing")
	}
	return startup.PrepareAdaptNovel(startup.Request{
		Mode:               startup.ModeAdaptNovel,
		UserPrompt:         s.cocreate.draftPrompt(),
		NovelPath:          s.cocreate.sourcePath,
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
	lines = append(lines, titleStyle.Render("确认改编模式"))
	lines = append(lines, "")
	lines = append(lines, bodyStyle.Render("开始写作前必须显式确认章节结构和正文策略。"))
	lines = append(lines, dimStyle.Render("默认已按你的偏好高亮：chapter + preserve_details + ±15%。"))
	lines = append(lines, "")
	lines = append(lines, renderAdaptChoiceRow("结构粒度", adaptGranularityChoices[state.granularity].label, adaptGranularityChoices[state.granularity].desc, state.cursor == 0, contentW))
	lines = append(lines, renderAdaptChoiceRow("改写策略", adaptRewriteChoices[state.rewritePolicy].label, adaptRewriteChoices[state.rewritePolicy].desc, state.cursor == 1, contentW))
	lines = append(lines, renderAdaptChoiceRow("字数容差", fmt.Sprintf("±%.0f%%", state.selectedTolerance()*100), "preserve_details 下作为硬契约；full_rewrite 下仅记录", state.cursor == 2, contentW))
	lines = append(lines, "")
	lines = append(lines, dimStyle.Render("↑↓ 选择项目 · ←→ 切换选项 · Enter 确认 · Esc 返回共创"))

	modal := renderPaddedModalFrame(boxW, boxH, "小说改编模式确认", "",
		strings.Split(lipgloss.NewStyle().Width(contentW).Render(strings.Join(lines, "\n")), "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

func renderAdaptChoiceRow(name, value, desc string, active bool, width int) string {
	prefix := "  "
	nameStyle := lipgloss.NewStyle().Foreground(colorMuted)
	valueStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(colorDim)
	if active {
		prefix = "> "
		nameStyle = nameStyle.Foreground(colorAccent2).Bold(true)
	}
	line := fmt.Sprintf("%s%s  %s", prefix, nameStyle.Render(name), valueStyle.Render(value))
	if desc != "" {
		line += "\n  " + descStyle.Render(wrapText(desc, max(20, width-4)))
	}
	return line
}
