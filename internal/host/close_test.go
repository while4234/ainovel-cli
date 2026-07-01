package host

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestHostCloseWhileWaitDonePendingDoesNotPanic(t *testing.T) {
	model := newCloseRaceModel()
	coordinator := agentcore.NewAgent(agentcore.WithModel(model))
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	h := &Host{
		store:       st,
		coordinator: coordinator,
		usage:       NewUsageTracker(nil, nil),
		events:      make(chan Event, 16),
		streamCh:    make(chan string, 16),
		done:        make(chan struct{}, 1),
		lifecycle:   lifecycleRunning,
	}
	h.observer = newObserver(coordinator, st, h.emitEvent, h.emitDelta, h.emitClear)

	if err := coordinator.Prompt(context.Background(), "start"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitForCloseRaceSignal(t, model.started, "model call to start")

	waitDoneReturned := make(chan struct{})
	panicCh := make(chan any, 1)
	go func() {
		defer close(waitDoneReturned)
		defer func() {
			if recovered := recover(); recovered != nil {
				panicCh <- recovered
			}
		}()
		h.waitDone()
	}()

	h.Close()
	select {
	case _, ok := <-h.Done():
		if ok {
			t.Fatal("Host.Done yielded a completion value after Close; want closed channel")
		}
	case <-time.After(time.Second):
		t.Fatal("Host.Done was not closed by Close")
	}

	model.releaseOnce.Do(func() {
		close(model.release)
	})

	select {
	case <-waitDoneReturned:
	case <-time.After(time.Second):
		t.Fatal("waitDone did not return after the blocked model completed")
	}
	select {
	case recovered := <-panicCh:
		t.Fatalf("waitDone panicked after Host.Close: %v", recovered)
	default:
	}
}

type closeRaceModel struct {
	started     chan struct{}
	startedOnce sync.Once
	release     chan struct{}
	releaseOnce sync.Once
}

func newCloseRaceModel() *closeRaceModel {
	return &closeRaceModel{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (m *closeRaceModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.markStarted()
	<-m.release
	return &agentcore.LLMResponse{Message: closeRaceAssistantMessage("done")}, nil
}

func (m *closeRaceModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.markStarted()
	ch := make(chan agentcore.StreamEvent, 1)
	go func() {
		defer close(ch)
		<-m.release
		msg := closeRaceAssistantMessage("done")
		ch <- agentcore.StreamEvent{
			Type:       agentcore.StreamEventDone,
			Message:    msg,
			StopReason: agentcore.StopReasonStop,
		}
	}()
	return ch, nil
}

func (m *closeRaceModel) SupportsTools() bool {
	return false
}

func (m *closeRaceModel) markStarted() {
	m.startedOnce.Do(func() {
		close(m.started)
	})
}

func closeRaceAssistantMessage(text string) agentcore.Message {
	return agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(text)},
		StopReason: agentcore.StopReasonStop,
	}
}

func waitForCloseRaceSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}
