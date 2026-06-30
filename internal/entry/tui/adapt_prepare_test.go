package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
)

func TestAdaptPreparationStateRendersProgress(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := newAdaptPreparationState(1, "D:\\books\\source.txt", 120, 40, cancel)
	state.appendEvent(adapt.Event{
		Time:    time.Now(),
		Stage:   adapt.StageChapter,
		Current: 3,
		Total:   12,
		Message: "分析原文第 3/12 章：初遇",
	}, 80)

	out := renderAdaptPreparationModal(120, 40, state)
	for _, want := range []string{"D:\\books\\source.txt", "chapter", "3/12", "分析原文第 3/12 章"} {
		if !strings.Contains(out, want) {
			t.Fatalf("modal missing %q:\n%s", want, out)
		}
	}
}

func TestAdaptPreparationChapterEventContinuesListening(t *testing.T) {
	ch := make(chan adapt.Event)
	m := NewModel(&host.Host{}, nil, "")
	m.adaptPreparation = newAdaptPreparationState(7, "source.txt", 120, 40, func() {})

	next, cmd, handled := m.handleRuntimeMsg(adaptEventMsg{
		reqID: 7,
		ev: adapt.Event{
			Time:    time.Now(),
			Stage:   adapt.StageChapter,
			Current: 2,
			Total:   5,
			Message: "分析原文第 2/5 章",
		},
		ch: ch,
	})
	if !handled {
		t.Fatal("adapt event should be handled")
	}
	got := next.(Model)
	if got.adaptPreparation == nil || got.adaptPreparation.current != 2 || got.adaptPreparation.total != 5 {
		t.Fatalf("adapt progress not updated: %+v", got.adaptPreparation)
	}
	if cmd == nil {
		t.Fatal("chapter event should return a listener command")
	}
}

func TestAdaptPreparationDoneOpensModeSelectionBeforeCoCreate(t *testing.T) {
	m := NewModel(&host.Host{}, nil, "")
	m.adaptPreparation = newAdaptPreparationState(3, "source.txt", 120, 40, func() {})

	next, cmd, handled := m.handleRuntimeMsg(adaptEventMsg{
		reqID: 3,
		ev: adapt.Event{
			Time:    time.Now(),
			Stage:   adapt.StageDone,
			Current: 5,
			Total:   5,
			Message: "原书分析完成",
		},
	})
	if !handled {
		t.Fatal("adapt done event should be handled")
	}
	got := next.(Model)
	if got.adaptPreparation != nil {
		t.Fatalf("adapt preparation should close on done: %+v", got.adaptPreparation)
	}
	if got.adaptConfirm == nil || got.adaptConfirm.sourcePath != "source.txt" {
		t.Fatalf("adapt mode selection not opened: %+v", got.adaptConfirm)
	}
	if got.cocreate != nil {
		t.Fatalf("co-create should wait for mode selection: %+v", got.cocreate)
	}
	if cmd != nil {
		t.Fatal("done event should not start co-create before mode selection")
	}
}

func TestAdaptPreparationErrorKeepsModal(t *testing.T) {
	wantErr := errors.New("source analysis failed")
	m := NewModel(&host.Host{}, nil, "")
	m.adaptPreparation = newAdaptPreparationState(4, "bad.txt", 120, 40, func() {})

	next, _, handled := m.handleRuntimeMsg(adaptEventMsg{
		reqID: 4,
		ev: adapt.Event{
			Time:    time.Now(),
			Stage:   adapt.StageError,
			Message: "改编源书分析失败",
			Err:     wantErr,
		},
	})
	if !handled {
		t.Fatal("adapt error event should be handled")
	}
	got := next.(Model)
	if got.adaptPreparation == nil || got.adaptPreparation.err != wantErr || !got.adaptPreparation.done {
		t.Fatalf("error should stay visible in modal: %+v", got.adaptPreparation)
	}
	if got.cocreate != nil {
		t.Fatalf("co-create should not start on error: %+v", got.cocreate)
	}
}

func TestAdaptPreparationEscCloseRestoresSourcePath(t *testing.T) {
	m := NewModel(&host.Host{}, nil, "")
	m.adaptPreparation = newAdaptPreparationState(5, "source.txt", 120, 40, func() {})
	m.adaptPreparation.done = true

	next, _ := m.handleAdaptPreparationKey(tea.KeyMsg{Type: tea.KeyEsc})
	got := next.(Model)
	if got.adaptPreparation != nil {
		t.Fatalf("adapt modal should close: %+v", got.adaptPreparation)
	}
	if got.textarea.Value() != "source.txt" {
		t.Fatalf("source path should be restored, got %q", got.textarea.Value())
	}
}
