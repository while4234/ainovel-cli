package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
)

// adaptPreparationState tracks source-novel analysis before adaptation co-create.
type adaptPreparationState struct {
	reqID      int
	source     string
	stage      adapt.Stage
	current    int
	total      int
	startedAt  time.Time
	finishedAt time.Time
	history    []adaptPreparationLine
	err        error
	done       bool
	cancel     context.CancelFunc
	viewport   viewport.Model
}

type adaptPreparationLine struct {
	at      time.Time
	stage   adapt.Stage
	current int
	total   int
	message string
	err     error
}

type adaptEventMsg struct {
	reqID int
	ev    adapt.Event
	ch    <-chan adapt.Event
}

func newAdaptPreparationState(reqID int, source string, width, height int, cancel context.CancelFunc) *adaptPreparationState {
	boxW, boxH := reportModalSize(width, height)
	contentW := paddedModalContentWidth(boxW)
	vp := viewport.New(contentW, boxH-4)
	s := &adaptPreparationState{
		reqID:     reqID,
		source:    source,
		startedAt: time.Now(),
		stage:     adapt.StageSplitting,
		cancel:    cancel,
		viewport:  vp,
	}
	s.refresh(contentW)
	return s
}

func (s *adaptPreparationState) appendEvent(ev adapt.Event, contentW int) {
	s.stage = ev.Stage
	s.current = ev.Current
	s.total = ev.Total
	if ev.Err != nil {
		s.err = ev.Err
		slog.Error("adaptation preparation failed", "module", "tui", "stage", ev.Stage, "message", ev.Message, "err", ev.Err)
	} else if ev.Stage == adapt.StageError {
		message := strings.TrimSpace(ev.Message)
		if message == "" {
			message = "源书分析失败"
		}
		s.err = fmt.Errorf("%s", message)
	}
	s.history = append(s.history, adaptPreparationLine{
		at: ev.Time, stage: ev.Stage, current: ev.Current, total: ev.Total,
		message: ev.Message, err: ev.Err,
	})
	if ev.Stage == adapt.StageDone || ev.Stage == adapt.StageError {
		s.done = true
		s.finishedAt = ev.Time
	}
	s.refresh(contentW)
}

func (s *adaptPreparationState) refresh(contentW int) {
	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	okStyle := lipgloss.NewStyle().Foreground(colorSuccess)
	errStyle := lipgloss.NewStyle().Foreground(colorError)
	stageStyle := lipgloss.NewStyle().Foreground(colorAccent2)

	var b strings.Builder
	b.WriteString(titleStyle.Render("分析改编源书"))
	b.WriteString("\n\n")
	b.WriteString(dimStyle.Render("源文件 "))
	b.WriteString(s.source)
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("开始 "))
	b.WriteString(formatReportTime(s.startedAt))
	if !s.finishedAt.IsZero() {
		b.WriteString(dimStyle.Render("  完成 "))
		b.WriteString(formatReportTime(s.finishedAt))
	}
	b.WriteString("\n\n")

	b.WriteString(mutedStyle.Render("阶段 "))
	b.WriteString(stageStyle.Render(string(s.stage)))
	if s.total > 0 {
		b.WriteString(mutedStyle.Render("  进度 "))
		if s.current > 0 {
			b.WriteString(fmt.Sprintf("%d/%d", s.current, s.total))
		} else {
			b.WriteString(fmt.Sprintf("0/%d", s.total))
		}
	}
	b.WriteString("\n\n")

	b.WriteString(titleStyle.Render("流程日志"))
	b.WriteString(" ")
	b.WriteString(dimStyle.Render(fmt.Sprintf("(%d 条)", len(s.history))))
	b.WriteString("\n")
	for _, ln := range s.history {
		b.WriteString("\n")
		b.WriteString(dimStyle.Render(ln.at.Format("15:04:05")))
		b.WriteString(" ")
		b.WriteString(stageStyle.Render(string(ln.stage)))
		if ln.total > 0 {
			b.WriteString(mutedStyle.Render(fmt.Sprintf(" %d/%d", ln.current, ln.total)))
		}
		b.WriteString(" ")
		if ln.err != nil {
			b.WriteString(errStyle.Render(ln.message + " - " + ln.err.Error()))
		} else {
			b.WriteString(wrapText(ln.message, contentW))
		}
	}

	b.WriteString("\n\n")
	switch {
	case !s.done:
		b.WriteString(dimStyle.Render("Esc 取消源书分析"))
	case s.err != nil:
		b.WriteString(errStyle.Render("源书分析失败"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("Esc 关闭面板"))
	default:
		b.WriteString(okStyle.Render("源书分析完成，正在进入模式选择"))
	}

	s.viewport.SetContent(b.String())
	if !s.done {
		s.viewport.GotoBottom()
	}
}

func renderAdaptPreparationModal(width, height int, s *adaptPreparationState) string {
	if s == nil {
		return ""
	}
	boxW, boxH := reportModalSize(width, height)
	contentW := paddedModalContentWidth(boxW)
	if s.viewport.Width != contentW {
		s.viewport.Width = contentW
		s.refresh(contentW)
	}
	if s.viewport.Height != boxH-4 {
		s.viewport.Height = boxH - 4
	}

	hint := "  ↑↓ 滚动 · Esc 取消/关闭"
	modal := renderPaddedModalFrame(boxW, boxH, "小说改编准备", hint,
		strings.Split(s.viewport.View(), "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

func (m Model) handleAdaptPreparationKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.adaptPreparation == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		if !m.adaptPreparation.done && m.adaptPreparation.cancel != nil {
			m.adaptPreparation.cancel()
			return m, nil
		}
		source := m.adaptPreparation.source
		m.adaptPreparation = nil
		m.textarea.SetValue(source)
		m.setTextareaPlaceholder(placeholderForNewMode(startupModeAdapt))
		return m, m.textarea.Focus()
	case tea.KeyUp:
		m.adaptPreparation.viewport.ScrollUp(1)
	case tea.KeyDown:
		m.adaptPreparation.viewport.ScrollDown(1)
	case tea.KeyPgUp:
		m.adaptPreparation.viewport.HalfPageUp()
	case tea.KeyPgDown:
		m.adaptPreparation.viewport.HalfPageDown()
	}
	return m, nil
}

func startAdaptPreparation(rt *host.Host, reqID int, sourcePath string, width, height int) (*adaptPreparationState, tea.Cmd, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return nil, nil, fmt.Errorf("请输入原小说路径")
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := rt.PrepareAdaptationSource(ctx, sourcePath)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	state := newAdaptPreparationState(reqID, sourcePath, width, height, cancel)
	return state, listenAdaptEvent(reqID, ch), nil
}

func listenAdaptEvent(reqID int, ch <-chan adapt.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return adaptEventMsg{reqID: reqID, ev: ev, ch: ch}
	}
}
