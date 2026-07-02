package host

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
)

func TestCoCreateRetryUsesSharedPolicy(t *testing.T) {
	if coCreateMaxAttempts != retrypolicy.MaxAttempts {
		t.Fatalf("coCreateMaxAttempts=%d, want %d", coCreateMaxAttempts, retrypolicy.MaxAttempts)
	}
	if got := coCreateRetryDelay(1); got != retrypolicy.Delay(1) {
		t.Fatalf("retry delay=%s, want %s", got, retrypolicy.Delay(1))
	}
}

func TestCoCreateStreamRetriesTransientStreamEOF(t *testing.T) {
	restore := stubCoCreateRetrySleep(t)
	defer restore()

	model := &scriptedCoCreateModel{
		streams: [][]agentcore.StreamEvent{
			{
				{Type: agentcore.StreamEventTextDelta, Delta: "<reply>partial"},
				{Type: agentcore.StreamEventError, Err: io.ErrUnexpectedEOF},
			},
			{
				{Type: agentcore.StreamEventTextDelta, Delta: validCoCreateXML("ok")},
				{Type: agentcore.StreamEventDone},
			},
		},
	}
	var progress []coCreateProgress

	reply, err := coCreateStream(
		context.Background(),
		newCoCreateModelSet(model),
		nil,
		"system",
		[]CoCreateMessage{{Role: "user", Content: "start"}},
		func(kind, text string) {
			progress = append(progress, coCreateProgress{kind: kind, text: text})
		},
	)
	if err != nil {
		t.Fatalf("coCreateStream: %v", err)
	}
	if model.streamCalls != 2 {
		t.Fatalf("stream calls = %d, want 2", model.streamCalls)
	}
	if reply.Message != "ok" || reply.Prompt != "## plan" || !reply.Ready {
		t.Fatalf("reply = %+v", reply)
	}
	if !hasCoCreateProgress(progress, CoCreateProgressReply, "") {
		t.Fatalf("retry should clear the partial reply preview, progress=%+v", progress)
	}
}

func TestCoCreateStreamDoesNotRetryCancellation(t *testing.T) {
	restore := stubCoCreateRetrySleep(t)
	defer restore()

	model := &scriptedCoCreateModel{
		streams: [][]agentcore.StreamEvent{{
			{Type: agentcore.StreamEventError, Err: context.Canceled},
		}},
	}

	_, err := coCreateStream(
		context.Background(),
		newCoCreateModelSet(model),
		nil,
		"system",
		[]CoCreateMessage{{Role: "user", Content: "start"}},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("err = %v, want context canceled", err)
	}
	if model.streamCalls != 1 {
		t.Fatalf("stream calls = %d, want 1", model.streamCalls)
	}
}

func TestCoCreateStreamErrorIncludesSelectedModel(t *testing.T) {
	restore := stubCoCreateRetrySleep(t)
	defer restore()

	model := &scriptedCoCreateModel{
		streams: [][]agentcore.StreamEvent{{
			{Type: agentcore.StreamEventError, Err: context.Canceled},
		}},
	}

	_, err := coCreateStream(
		context.Background(),
		newCoCreateModelSet(model),
		nil,
		"system",
		[]CoCreateMessage{{Role: "user", Content: "start"}},
		nil,
	)
	if err == nil {
		t.Fatal("coCreateStream should fail")
	}
	if !strings.Contains(err.Error(), "selected model test/scripted-cocreate") {
		t.Fatalf("err = %v, want selected model label", err)
	}
}

type scriptedCoCreateModel struct {
	streams     [][]agentcore.StreamEvent
	streamCalls int
}

func (m *scriptedCoCreateModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return nil, io.ErrUnexpectedEOF
}

func (m *scriptedCoCreateModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	ch := make(chan agentcore.StreamEvent, 8)
	if m.streamCalls >= len(m.streams) {
		close(ch)
		return ch, nil
	}
	events := m.streams[m.streamCalls]
	m.streamCalls++
	go func() {
		defer close(ch)
		for _, ev := range events {
			ch <- ev
		}
	}()
	return ch, nil
}

func (m *scriptedCoCreateModel) SupportsTools() bool { return false }

type coCreateProgress struct {
	kind string
	text string
}

func newCoCreateModelSet(model agentcore.ChatModel) *bootstrap.ModelSet {
	return &bootstrap.ModelSet{
		Default: bootstrap.NewSwappableModel("test", "scripted-cocreate", model),
	}
}

func stubCoCreateRetrySleep(t *testing.T) func() {
	t.Helper()
	original := coCreateRetrySleep
	coCreateRetrySleep = func(context.Context, time.Duration) error { return nil }
	return func() { coCreateRetrySleep = original }
}

func validCoCreateXML(message string) string {
	return "<reply>" + message + "</reply>" +
		"<draft>## plan</draft>" +
		"<ready>true</ready>" +
		"<suggestions></suggestions>"
}

func hasCoCreateProgress(progress []coCreateProgress, kind, text string) bool {
	for _, item := range progress {
		if item.kind == kind && item.text == text {
			return true
		}
	}
	return false
}
