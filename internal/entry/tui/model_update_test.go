package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/host"
)

func TestSteerResultErrorShowsErrorEventAndRefocusesInput(t *testing.T) {
	wantErr := errors.New("steer persist failed")
	m := NewModel(&host.Host{}, nil, "")
	m.width = 120
	m.height = 36
	m.textarea.Blur()

	next, cmd, handled := m.handleRuntimeMsg(steerResultMsg{err: wantErr})
	if !handled {
		t.Fatal("steer result should be handled")
	}
	got := next.(Model)

	if got.err != wantErr {
		t.Fatalf("model error = %v, want %v", got.err, wantErr)
	}
	if len(got.events) != 1 {
		t.Fatalf("events len = %d, want 1", len(got.events))
	}
	ev := got.events[0]
	if ev.Category != "ERROR" || ev.Level != "error" || !strings.Contains(ev.Summary, wantErr.Error()) {
		t.Fatalf("unexpected error event: %+v", ev)
	}
	if !strings.Contains(got.viewport.View(), wantErr.Error()) {
		t.Fatalf("event viewport should display steer error, got:\n%s", got.viewport.View())
	}
	if !got.textarea.Focused() {
		t.Fatal("textarea should be focused after steer failure")
	}
	if cmd == nil {
		t.Fatal("steer failure should fetch a snapshot and return focus command")
	}
}
