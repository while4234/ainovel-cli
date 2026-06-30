package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/host"
)

func TestCoCreateBodyClampsInputViewToOneLine(t *testing.T) {
	state := newAdaptCoCreateState("source.txt")
	state.awaiting = false
	state.apply(host.CoCreateReply{
		Prompt: "## brief\n\nkeep source line",
	})

	rendered := renderCoCreateBody(100, 24, state, "", "INPUT-LINE\nINPUT-LINE\nINPUT-LINE", 0)
	if got := strings.Count(rendered, "INPUT-LINE"); got != 1 {
		t.Fatalf("input view should render once, got %d:\n%s", got, rendered)
	}
}

func TestCoCreateDoneErrorShowsRetryPlaceholder(t *testing.T) {
	m := NewModel(&host.Host{}, nil, "")
	m.cocreate = newAdaptCoCreateState("source.txt")
	m.cocreate.reqID = 7
	m.cocreate.awaiting = true
	m.cocreate.apply(host.CoCreateReply{Prompt: "## brief"})

	next, _ := m.handleCoCreateDoneMsg(cocreateDoneMsg{
		reqID: 7,
		err:   errors.New("cocreate generate: stream read error: unexpected EOF"),
	})
	got := next.(Model)

	if got.cocreate == nil || got.cocreate.awaiting {
		t.Fatalf("co-create should stay open and stop awaiting: %+v", got.cocreate)
	}
	if got.err == nil {
		t.Fatal("error should remain visible")
	}
	if !strings.Contains(got.textarea.Placeholder, "AI 回复失败") ||
		!strings.Contains(got.textarea.Placeholder, "Ctrl+S 开始改编") {
		t.Fatalf("placeholder should explain retry/start options: %q", got.textarea.Placeholder)
	}
}

func TestCoCreateEnterRetriesFailedEmptyInput(t *testing.T) {
	m := NewModel(&host.Host{}, nil, "")
	m.cocreate = newAdaptCoCreateState("source.txt")
	m.cocreate.awaiting = false
	m.cocreate.apply(host.CoCreateReply{Prompt: "## brief"})
	m.err = errors.New("previous co-create failed")
	m.textarea.Reset()

	next, cmd := m.handleCoCreateKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(Model)
	if cmd == nil {
		t.Fatal("empty Enter after a co-create error should retry")
	}
	if got.err != nil {
		t.Fatalf("error should clear before retry: %v", got.err)
	}
	if got.cocreate == nil || !got.cocreate.awaiting {
		t.Fatalf("co-create should be awaiting retry: %+v", got.cocreate)
	}
}
