package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/host"
)

func TestViewFitsTerminalHeightInDoneMode(t *testing.T) {
	m := NewModel(nil, nil, "vtest")
	m.width = 150
	m.height = 36
	m.mode = modeDone
	m.snapshot = host.UISnapshot{
		RuntimeState:   "completed",
		CompletedCount: 3,
		TotalChapters:  3,
		NovelName:      "xfk",
	}
	m.resizeTextarea()
	m.setTextareaPlaceholder("DONE_PROMPT")

	// Regression: a stale multi-line textarea height used to make every redraw
	// exceed the terminal, leaving repeated completed placeholders in scrollback.
	m.textarea.SetHeight(6)

	view := m.View()
	if got := lipgloss.Height(view); got != m.height {
		t.Fatalf("view height=%d, want %d\n%s", got, m.height, view)
	}
	if got := strings.Count(view, "DONE_PROMPT"); got != 1 {
		t.Fatalf("done prompt should render once, got %d\n%s", got, view)
	}
}
