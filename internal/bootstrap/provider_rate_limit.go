package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
	"github.com/voocel/litellm"
)

var sharedProviderGovernors = newProviderGovernorRegistry()

type providerGovernorRegistry struct {
	mu        sync.Mutex
	governors map[string]*providerRequestGovernor
}

func newProviderGovernorRegistry() *providerGovernorRegistry {
	return &providerGovernorRegistry{governors: make(map[string]*providerRequestGovernor)}
}

func (r *providerGovernorRegistry) get(identity string, config ProviderRateLimitConfig) *providerRequestGovernor {
	r.mu.Lock()
	defer r.mu.Unlock()
	governor := r.governors[identity]
	if governor == nil {
		governor = newProviderRequestGovernor(config)
		r.governors[identity] = governor
		return governor
	}
	governor.configure(config)
	return governor
}

type providerRequestGovernor struct {
	mu            sync.Mutex
	requestGap    time.Duration
	maxConcurrent int
	active        int
	nextStart     time.Time
	changed       chan struct{}
}

func newProviderRequestGovernor(config ProviderRateLimitConfig) *providerRequestGovernor {
	governor := &providerRequestGovernor{changed: make(chan struct{})}
	governor.configure(config)
	return governor
}

func (g *providerRequestGovernor) configure(config ProviderRateLimitConfig) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if config.RequestsPerMinute > 0 {
		g.requestGap = time.Minute / time.Duration(config.RequestsPerMinute)
	} else {
		g.requestGap = 0
	}
	g.maxConcurrent = config.MaxConcurrentRequests
	g.signalLocked()
}

func (g *providerRequestGovernor) acquire(ctx context.Context) (func(), time.Duration, error) {
	startedWaiting := time.Now()
	for {
		g.mu.Lock()
		now := time.Now()
		concurrencyBlocked := g.maxConcurrent > 0 && g.active >= g.maxConcurrent
		paceDelay := time.Duration(0)
		if g.requestGap > 0 && now.Before(g.nextStart) {
			paceDelay = g.nextStart.Sub(now)
		}
		if !concurrencyBlocked && paceDelay <= 0 {
			g.active++
			if g.requestGap > 0 {
				g.nextStart = now.Add(g.requestGap)
			}
			g.mu.Unlock()
			return g.release, time.Since(startedWaiting), nil
		}
		changed := g.changed
		g.mu.Unlock()

		if concurrencyBlocked {
			select {
			case <-ctx.Done():
				return nil, time.Since(startedWaiting), ctx.Err()
			case <-changed:
			}
			continue
		}

		timer := time.NewTimer(paceDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, time.Since(startedWaiting), ctx.Err()
		case <-changed:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}

func (g *providerRequestGovernor) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active > 0 {
		g.active--
	}
	g.signalLocked()
}

func (g *providerRequestGovernor) signalLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}

type providerGovernedModel struct {
	provider      string
	model         agentcore.ChatModel
	governor      *providerRequestGovernor
	retryInterval time.Duration
}

func wrapProviderGovernance(provider string, config ProviderConfig, model agentcore.ChatModel) agentcore.ChatModel {
	if model == nil || !config.RateLimit.Enabled() {
		return model
	}
	governor := sharedProviderGovernors.get(providerGovernorIdentity(provider, config), config.RateLimit)
	return &providerGovernedModel{
		provider:      provider,
		model:         model,
		governor:      governor,
		retryInterval: time.Duration(config.RateLimit.RetryIntervalSeconds) * time.Second,
	}
}

func providerGovernorIdentity(provider string, config ProviderConfig) string {
	credential := strings.Join([]string{
		strings.TrimSpace(provider),
		strings.TrimSpace(config.BaseURL),
		strings.TrimSpace(config.Auth),
		strings.TrimSpace(config.AccountID),
		strings.TrimSpace(config.AuthFile),
		strings.TrimSpace(config.APIKey),
	}, "\x00")
	digest := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(digest[:])
}

func (m *providerGovernedModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	for attempt := 1; ; attempt++ {
		release, err := m.acquire(ctx)
		if err != nil {
			return nil, err
		}
		response, callErr := m.model.Generate(ctx, messages, tools, opts...)
		release()
		if callErr == nil {
			return response, nil
		}
		delay, retry := m.rateLimitRetryDelay(callErr, attempt)
		if !retry {
			return nil, callErr
		}
		m.logRetry(delay, attempt)
		if err := retrypolicy.Wait(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func (m *providerGovernedModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	out := make(chan agentcore.StreamEvent, 100)
	go m.runStream(ctx, out, messages, tools, opts...)
	return out, nil
}

func (m *providerGovernedModel) runStream(ctx context.Context, out chan<- agentcore.StreamEvent, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) {
	defer close(out)
	for attempt := 1; ; attempt++ {
		release, err := m.acquire(ctx)
		if err != nil {
			m.sendStreamEvent(ctx, out, agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: err})
			return
		}
		source, callErr := m.model.GenerateStream(ctx, messages, tools, opts...)
		if callErr != nil {
			release()
			delay, retry := m.rateLimitRetryDelay(callErr, attempt)
			if retry {
				m.logRetry(delay, attempt)
				if err := retrypolicy.Wait(ctx, delay); err != nil {
					m.sendStreamEvent(ctx, out, agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: err})
					return
				}
				continue
			}
			m.sendStreamEvent(ctx, out, agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: callErr})
			return
		}
		if m.forwardStreamAttempt(ctx, out, source, release, attempt) {
			return
		}
	}
}

// forwardStreamAttempt returns false only when an unstarted rate-limited
// stream should be retried. Once material output is visible, errors are
// forwarded instead of replaying a partial response.
func (m *providerGovernedModel) forwardStreamAttempt(ctx context.Context, out chan<- agentcore.StreamEvent, source <-chan agentcore.StreamEvent, release func(), attempt int) bool {
	materialOutput := false
	pendingPrelude := make([]agentcore.StreamEvent, 0, 2)
	released := false
	releaseOnce := func() {
		if !released {
			released = true
			release()
		}
	}
	defer releaseOnce()

	for {
		select {
		case <-ctx.Done():
			m.sendStreamEvent(ctx, out, agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: ctx.Err()})
			return true
		case event, ok := <-source:
			if !ok {
				for _, prelude := range pendingPrelude {
					if !m.sendStreamEvent(ctx, out, prelude) {
						return true
					}
				}
				return true
			}
			if event.Type == agentcore.StreamEventError {
				releaseOnce()
				delay, retry := m.rateLimitRetryDelay(event.Err, attempt)
				if retry && !materialOutput {
					m.logRetry(delay, attempt)
					if err := retrypolicy.Wait(ctx, delay); err != nil {
						m.sendStreamEvent(ctx, out, agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: err})
						return true
					}
					return false
				}
				for _, prelude := range pendingPrelude {
					if !m.sendStreamEvent(ctx, out, prelude) {
						return true
					}
				}
				m.sendStreamEvent(ctx, out, event)
				return true
			}
			if !materialOutput && !streamEventHasMaterialOutput(event) {
				pendingPrelude = append(pendingPrelude, event)
				continue
			}
			if !materialOutput {
				materialOutput = true
				for _, prelude := range pendingPrelude {
					if !m.sendStreamEvent(ctx, out, prelude) {
						return true
					}
				}
				pendingPrelude = nil
			}
			if !m.sendStreamEvent(ctx, out, event) {
				return true
			}
			if event.Type == agentcore.StreamEventDone {
				return true
			}
		}
	}
}

func (m *providerGovernedModel) acquire(ctx context.Context) (func(), error) {
	release, waited, err := m.governor.acquire(ctx)
	if err == nil && waited >= 100*time.Millisecond {
		slog.Info("provider request waited for configured rate limit", "provider", m.provider, "wait", waited.Round(time.Millisecond))
	}
	return release, err
}

func (m *providerGovernedModel) rateLimitRetryDelay(err error, attempt int) (time.Duration, bool) {
	if attempt >= retrypolicy.MaxAttempts || !isRuntimeRateLimitErrorMessage(err) {
		return 0, false
	}
	if seconds := litellm.GetRetryAfter(err); seconds > 0 {
		return time.Duration(seconds) * time.Second, true
	}
	return m.retryInterval, m.retryInterval > 0
}

func (m *providerGovernedModel) logRetry(delay time.Duration, attempt int) {
	slog.Warn("provider rate limit reached; waiting before retry", "provider", m.provider, "delay", delay, "attempt", attempt+1)
}

func (m *providerGovernedModel) sendStreamEvent(ctx context.Context, out chan<- agentcore.StreamEvent, event agentcore.StreamEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- event:
		return true
	}
}

func (m *providerGovernedModel) SupportsTools() bool {
	return m.model != nil && m.model.SupportsTools()
}

func (m *providerGovernedModel) ProviderName() string {
	if namer, ok := m.model.(agentcore.ProviderNamer); ok {
		return namer.ProviderName()
	}
	return m.provider
}

func (m *providerGovernedModel) ModelName() string {
	if namer, ok := m.model.(agentcore.ModelNamer); ok {
		return namer.ModelName()
	}
	return ""
}

func (m *providerGovernedModel) Info() llm.ModelInfo {
	if provider, ok := m.model.(interface{ Info() llm.ModelInfo }); ok {
		return provider.Info()
	}
	return llm.ModelInfo{}
}

func (m *providerGovernedModel) Capabilities() llm.Capabilities {
	if provider, ok := m.model.(llm.CapabilityProvider); ok {
		return provider.Capabilities()
	}
	return llm.Capabilities{}
}
