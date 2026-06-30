package imp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
)

type LLMStreamChat interface {
	GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error)
}

type StructuredRetryEvent struct {
	Attempt     int
	MaxAttempts int
	Err         error
}

type StructuredCallOptions struct {
	MaxAttempts   int
	MaxTokens     int
	DisableStream bool
	OnRetry       func(StructuredRetryEvent)
	Sleep         func(context.Context, time.Duration) error
}

func runStructuredCall[T any](
	ctx context.Context,
	llm LLMChat,
	messages []agentcore.Message,
	parse func(string) (T, error),
	opts StructuredCallOptions,
) (T, error) {
	var zero T
	if llm == nil {
		return zero, fmt.Errorf("llm is nil")
	}
	if parse == nil {
		return zero, fmt.Errorf("parse is nil")
	}

	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	formatRetried := false
	currentMessages := append([]agentcore.Message(nil), messages...)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}

		text, err := generateStructuredText(ctx, llm, currentMessages, opts)
		if err != nil {
			if !agentcore.IsFailoverEligible(err) || attempt == maxAttempts {
				return zero, err
			}
			if err := waitBeforeStructuredRetry(ctx, opts, attempt, maxAttempts, err); err != nil {
				return zero, err
			}
			continue
		}

		parsed, err := parse(text)
		if err == nil {
			return parsed, nil
		}
		if formatRetried || attempt == maxAttempts {
			return zero, err
		}

		formatRetried = true
		currentMessages = append(currentMessages, agentcore.UserMsg(formatRetryPrompt(err)))
		if err := waitBeforeStructuredRetry(ctx, opts, attempt, maxAttempts, err); err != nil {
			return zero, err
		}
	}
	return zero, fmt.Errorf("structured call exhausted %d attempts", maxAttempts)
}

func generateStructuredText(ctx context.Context, llm LLMChat, messages []agentcore.Message, opts StructuredCallOptions) (string, error) {
	callOpts := callOptions(opts)
	if streamer, ok := llm.(LLMStreamChat); ok && !opts.DisableStream {
		ch, err := streamer.GenerateStream(ctx, messages, nil, callOpts...)
		if err == nil {
			return collectStreamText(ch)
		}
		if agentcore.IsFailoverEligible(err) {
			return "", err
		}
	}

	resp, err := llm.Generate(ctx, messages, nil, callOpts...)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("llm returned nil response")
	}
	return resp.Message.TextContent(), nil
}

func collectStreamText(ch <-chan agentcore.StreamEvent) (string, error) {
	var sb strings.Builder
	for ev := range ch {
		switch ev.Type {
		case agentcore.StreamEventTextDelta:
			sb.WriteString(ev.Delta)
		case agentcore.StreamEventDone:
			if sb.Len() > 0 {
				return sb.String(), nil
			}
			return ev.Message.TextContent(), nil
		case agentcore.StreamEventError:
			if ev.Err != nil {
				return "", ev.Err
			}
			return "", fmt.Errorf("llm stream returned error event")
		}
	}
	return "", fmt.Errorf("llm stream closed before done")
}

func callOptions(opts StructuredCallOptions) []agentcore.CallOption {
	if opts.MaxTokens <= 0 {
		return nil
	}
	return []agentcore.CallOption{agentcore.WithMaxTokens(opts.MaxTokens)}
}

func waitBeforeStructuredRetry(ctx context.Context, opts StructuredCallOptions, attempt, maxAttempts int, err error) error {
	if opts.OnRetry != nil {
		opts.OnRetry(StructuredRetryEvent{Attempt: attempt + 1, MaxAttempts: maxAttempts, Err: err})
	}
	delay := structuredRetryDelay(attempt)
	if opts.Sleep != nil {
		return opts.Sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func structuredRetryDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return time.Second
	}
	delay := time.Second << (attempt - 1)
	if delay > 8*time.Second {
		return 8 * time.Second
	}
	return delay
}

func formatRetryPrompt(err error) string {
	return "The previous response could not be parsed as the required structured output.\n" +
		"Parse error: " + cleanLLMText(err.Error()) + "\n\n" +
		"Return the complete answer again using only the required === TAG === sections. " +
		"All JSON sections must be valid JSON. Do not add explanations, apologies, markdown fences, or extra text."
}
