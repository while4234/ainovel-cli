package bootstrap

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

func TestProviderRequestGovernorPacesStarts(t *testing.T) {
	governor := newProviderRequestGovernor(ProviderRateLimitConfig{RequestsPerMinute: 6000})
	release, _, err := governor.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()

	started := time.Now()
	release, _, err = governor.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if elapsed := time.Since(started); elapsed < 8*time.Millisecond {
		t.Fatalf("second request waited %s, want about 10ms", elapsed)
	}
}

func TestProviderRequestGovernorLimitsConcurrency(t *testing.T) {
	governor := newProviderRequestGovernor(ProviderRateLimitConfig{MaxConcurrentRequests: 1})
	firstRelease, _, err := governor.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan func(), 1)
	go func() {
		release, _, acquireErr := governor.acquire(context.Background())
		if acquireErr == nil {
			acquired <- release
		}
	}()
	select {
	case <-acquired:
		t.Fatal("second request acquired while first remained active")
	case <-time.After(20 * time.Millisecond):
	}
	firstRelease()
	select {
	case release := <-acquired:
		release()
	case <-time.After(time.Second):
		t.Fatal("second request did not acquire after release")
	}
}

func TestProviderGovernedModelRetriesRateLimitAfterConfiguredDelay(t *testing.T) {
	model := &rateLimitScriptModel{generateErrors: []error{errors.New("rate_limit_exceeded")}}
	governed := &providerGovernedModel{
		provider:      "limited",
		model:         model,
		governor:      newProviderRequestGovernor(ProviderRateLimitConfig{}),
		retryInterval: 10 * time.Millisecond,
	}
	started := time.Now()
	if _, err := governed.Generate(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if model.generateCalls != 2 {
		t.Fatalf("generate calls = %d, want 2", model.generateCalls)
	}
	if elapsed := time.Since(started); elapsed < 8*time.Millisecond {
		t.Fatalf("retry waited %s, want configured delay", elapsed)
	}
}

func TestProviderGovernedStreamRetriesBeforeMaterialOutput(t *testing.T) {
	model := &rateLimitScriptModel{streamErrors: []error{errors.New("rate_limit_exceeded")}}
	governed := &providerGovernedModel{
		provider:      "limited",
		model:         model,
		governor:      newProviderRequestGovernor(ProviderRateLimitConfig{}),
		retryInterval: time.Millisecond,
	}
	stream, err := governed.GenerateStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var done bool
	for event := range stream {
		if event.Type == agentcore.StreamEventError {
			t.Fatalf("unexpected stream error: %v", event.Err)
		}
		done = done || event.Type == agentcore.StreamEventDone
	}
	if !done {
		t.Fatal("retried stream did not finish")
	}
	if model.streamCalls != 2 {
		t.Fatalf("stream calls = %d, want 2", model.streamCalls)
	}
}

func TestProviderGovernedStreamDoesNotReplayPartialOutput(t *testing.T) {
	model := &partialRateLimitStreamModel{}
	governed := &providerGovernedModel{
		provider:      "limited",
		model:         model,
		governor:      newProviderRequestGovernor(ProviderRateLimitConfig{}),
		retryInterval: time.Millisecond,
	}
	stream, err := governed.GenerateStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var sawPartial, sawRateLimit bool
	for event := range stream {
		sawPartial = sawPartial || event.Type == agentcore.StreamEventTextDelta && event.Delta == "partial"
		sawRateLimit = sawRateLimit || event.Type == agentcore.StreamEventError && isRuntimeRateLimitErrorMessage(event.Err)
	}
	if !sawPartial || !sawRateLimit {
		t.Fatalf("stream output partial=%v rate_limit=%v", sawPartial, sawRateLimit)
	}
	if model.calls != 1 {
		t.Fatalf("stream calls = %d, want no replay after partial output", model.calls)
	}
}

func TestProviderRateLimitDefaultsToUnlimited(t *testing.T) {
	model := &rateLimitScriptModel{}
	if got := wrapProviderGovernance("unlimited", ProviderConfig{}, model); got != model {
		t.Fatalf("zero-value rate limit wrapped model as %T", got)
	}
}

type rateLimitScriptModel struct {
	mu             sync.Mutex
	generateCalls  int
	streamCalls    int
	generateErrors []error
	streamErrors   []error
}

func (m *rateLimitScriptModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.generateCalls
	m.generateCalls++
	if index < len(m.generateErrors) {
		return nil, m.generateErrors[index]
	}
	return &agentcore.LLMResponse{}, nil
}

func (m *rateLimitScriptModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.mu.Lock()
	index := m.streamCalls
	m.streamCalls++
	var streamErr error
	if index < len(m.streamErrors) {
		streamErr = m.streamErrors[index]
	}
	m.mu.Unlock()
	stream := make(chan agentcore.StreamEvent, 1)
	if streamErr != nil {
		stream <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: streamErr}
	} else {
		stream <- agentcore.StreamEvent{Type: agentcore.StreamEventDone}
	}
	close(stream)
	return stream, nil
}

func (m *rateLimitScriptModel) SupportsTools() bool { return true }

type partialRateLimitStreamModel struct {
	calls int
}

func (*partialRateLimitStreamModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return &agentcore.LLMResponse{}, nil
}

func (m *partialRateLimitStreamModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.calls++
	stream := make(chan agentcore.StreamEvent, 2)
	stream <- agentcore.StreamEvent{Type: agentcore.StreamEventTextDelta, Delta: "partial"}
	stream <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: errors.New("rate_limit_exceeded")}
	close(stream)
	return stream, nil
}

func (*partialRateLimitStreamModel) SupportsTools() bool { return true }
