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
	sourcePath  string
	granularity int
}

type adaptChoice struct {
	value string
	label string
	desc  string
}

var adaptGranularityChoices = []adaptChoice{
	{domain.AdaptationGranularityChapter, "chapter", "One target chapter maps to one source chapter."},
	{domain.AdaptationGranularityArc, "arc", "Allow chapter merge/split inside source arcs."},
	{domain.AdaptationGranularityFree, "free", "Allow broader chapter restructuring while keeping source coverage."},
}

func newAdaptModeConfirmState(sourcePath string) *adaptModeConfirmState {
	return &adaptModeConfirmState{
		sourcePath:  strings.TrimSpace(sourcePath),
		granularity: 0,
	}
}

func (s *adaptModeConfirmState) selectedGranularity() string {
	return adaptGranularityChoices[s.granularity].value
}

func (s *adaptModeConfirmState) selectedRewritePolicy() string {
	return domain.AdaptationRewritePolicyForGranularity(s.selectedGranularity())
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
		m.adaptConfirm = nil
		m.resizeTextarea()
		m.textarea.SetValue(state.sourcePath)
		m.setTextareaPlaceholder(placeholderForNewMode(startupModeAdapt))
		return m, m.textarea.Focus()
	case tea.KeyUp, tea.KeyLeft:
		state.moveChoice(-1)
		return m, nil
	case tea.KeyDown, tea.KeyTab, tea.KeyRight:
		state.moveChoice(1)
		return m, nil
	case tea.KeyRunes:
		if len(msg.Runes) == 1 && state.selectNumber(msg.Runes[0]) {
			return m.startAdaptCoCreateWithMode(state)
		}
		return m, nil
	case tea.KeyEnter, tea.KeyCtrlS:
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
	s.granularity = wrapIndex(s.granularity+delta, len(adaptGranularityChoices))
}

func (s *adaptModeConfirmState) selectNumber(value rune) bool {
	idx := int(value - '1')
	if idx < 0 || idx >= len(adaptGranularityChoices) {
		return false
	}
	s.granularity = idx
	return true
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
	if boxH > 22 {
		boxH = 22
	}
	contentW := paddedModalContentWidth(boxW)

	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	bodyStyle := lipgloss.NewStyle().Foreground(bodyTextColor)

	lines := []string{
		titleStyle.Render("选择章节结构"),
		"",
		bodyStyle.Render("Choose chapter structure. rewrite_policy is fixed by structure."),
		"",
	}
	lines = append(lines, renderAdaptOptionRows(adaptGranularityChoices, state.granularity, contentW)...)
	lines = append(lines,
		"",
		dimStyle.Render("Fixed policy: chapter => preserve_details"),
		dimStyle.Render("arc => full_rewrite; free => full_rewrite"),
		dimStyle.Render("Selected rewrite_policy: "+state.selectedRewritePolicy()),
		dimStyle.Render("Up/Down switch | 1/2/3 start | Enter start | Esc back"),
		"",
	)

	modal := renderPaddedModalFrame(boxW, boxH, "Adaptation", "",
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
