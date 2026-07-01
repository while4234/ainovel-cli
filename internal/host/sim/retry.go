package sim

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
)

type structuredJSONRetryEvent struct {
	Attempt     int
	MaxAttempts int
	Err         error
}

type structuredJSONCallOptions struct {
	MaxAttempts int
	OnRetry     func(structuredJSONRetryEvent)
	Sleep       func(context.Context, time.Duration) error
}

var structuredJSONRetrySleep = retrypolicy.Wait

func runStructuredJSONCall[T any](
	ctx context.Context,
	llm LLMChat,
	messages []agentcore.Message,
	parse func(string) (T, error),
	opts structuredJSONCallOptions,
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
		maxAttempts = retrypolicy.MaxAttempts
	}

	formatRetried := false
	currentMessages := append([]agentcore.Message(nil), messages...)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}

		resp, err := llm.Generate(ctx, currentMessages, nil)
		if err != nil {
			if !agentcore.IsFailoverEligible(err) || attempt == maxAttempts {
				return zero, err
			}
			if err := waitBeforeStructuredJSONRetry(ctx, opts, attempt, maxAttempts, err); err != nil {
				return zero, err
			}
			continue
		}
		if resp == nil {
			return zero, fmt.Errorf("llm returned nil response")
		}

		parsed, err := parse(resp.Message.TextContent())
		if err == nil {
			return parsed, nil
		}
		if formatRetried || attempt == maxAttempts {
			return zero, err
		}

		formatRetried = true
		currentMessages = append(currentMessages, agentcore.UserMsg(formatJSONRetryPrompt(err)))
		if err := waitBeforeStructuredJSONRetry(ctx, opts, attempt, maxAttempts, err); err != nil {
			return zero, err
		}
	}
	return zero, fmt.Errorf("structured JSON call exhausted %d attempts", maxAttempts)
}

func waitBeforeStructuredJSONRetry(ctx context.Context, opts structuredJSONCallOptions, attempt, maxAttempts int, err error) error {
	if opts.OnRetry != nil {
		opts.OnRetry(structuredJSONRetryEvent{Attempt: attempt + 1, MaxAttempts: maxAttempts, Err: err})
	}
	delay := retrypolicy.Delay(attempt)
	if opts.Sleep != nil {
		return opts.Sleep(ctx, delay)
	}
	return structuredJSONRetrySleep(ctx, delay)
}

func formatJSONRetryPrompt(err error) string {
	return "The previous response could not be parsed as the required JSON object.\n" +
		"Parse error: " + cleanRetryError(err.Error()) + "\n\n" +
		"Return the complete answer again as exactly one valid JSON object. " +
		"Do not use markdown fences, explanations, comments, trailing commas, or text outside JSON."
}

func cleanRetryError(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	text = strings.ReplaceAll(text, "\n", " ")
	const maxLen = 1000
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "...[truncated]"
}
