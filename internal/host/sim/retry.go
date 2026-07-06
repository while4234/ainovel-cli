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
	Kind        string
	Err         error
}

type structuredJSONCallOptions struct {
	MaxAttempts                int
	ModelCallMaxAttempts       int
	StructureRepairMaxAttempts int
	OnRetry                    func(structuredJSONRetryEvent)
	Sleep                      func(context.Context, time.Duration) error
}

var structuredJSONRetrySleep = retrypolicy.Wait

const (
	structuredJSONRetryKindModelCall       = "model_call"
	structuredJSONRetryKindStructureRepair = "structure_repair"
	defaultStructureRepairMaxAttempts      = 2
)

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

	currentMessages := append([]agentcore.Message(nil), messages...)
	maxStructureRepairAttempts := structuredJSONStructureRepairMaxAttempts(opts)
	for repairAttempt := 0; ; repairAttempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}

		resp, err := generateStructuredJSONResponse(ctx, llm, currentMessages, opts)
		if err != nil {
			return zero, err
		}

		parsed, err := parse(resp.Message.TextContent())
		if err == nil {
			return parsed, nil
		}
		if repairAttempt >= maxStructureRepairAttempts {
			return zero, err
		}

		currentMessages = append(currentMessages, agentcore.UserMsg(formatJSONRetryPrompt(err)))
		if opts.OnRetry != nil {
			opts.OnRetry(structuredJSONRetryEvent{
				Attempt:     repairAttempt + 1,
				MaxAttempts: maxStructureRepairAttempts,
				Kind:        structuredJSONRetryKindStructureRepair,
				Err:         err,
			})
		}
		if err := waitStructuredJSONRetryDelay(ctx, opts, repairAttempt+1); err != nil {
			return zero, err
		}
	}
}

func generateStructuredJSONResponse(ctx context.Context, llm LLMChat, messages []agentcore.Message, opts structuredJSONCallOptions) (*agentcore.LLMResponse, error) {
	maxAttempts := structuredJSONModelCallMaxAttempts(opts)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := llm.Generate(ctx, messages, nil)
		if err == nil && resp == nil {
			err = fmt.Errorf("llm returned nil response")
		}
		if err == nil {
			return resp, nil
		}
		if !shouldRetryStructuredJSONModelCall(ctx, err, attempt, maxAttempts) {
			return nil, err
		}
		if opts.OnRetry != nil {
			opts.OnRetry(structuredJSONRetryEvent{
				Attempt:     attempt + 1,
				MaxAttempts: maxAttempts,
				Kind:        structuredJSONRetryKindModelCall,
				Err:         err,
			})
		}
		if err := waitStructuredJSONRetryDelay(ctx, opts, attempt); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("structured JSON model call exhausted %d attempts", maxAttempts)
}

func shouldRetryStructuredJSONModelCall(ctx context.Context, err error, attempt, maxAttempts int) bool {
	if err == nil || ctx.Err() != nil || attempt >= maxAttempts {
		return false
	}
	if agentcore.IsFailoverEligible(err) {
		return true
	}
	if retrypolicy.IsProviderGatewayError(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "nil response") ||
		strings.Contains(msg, "empty response") ||
		strings.Contains(msg, "system is busy") ||
		strings.Contains(msg, "try again later") ||
		strings.Contains(msg, "overloaded") ||
		strings.Contains(msg, "temporarily unavailable") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "429") ||
		strings.Contains(msg, "503")
}

func structuredJSONModelCallMaxAttempts(opts structuredJSONCallOptions) int {
	if opts.ModelCallMaxAttempts > 0 {
		return opts.ModelCallMaxAttempts
	}
	if opts.MaxAttempts > 0 {
		return opts.MaxAttempts
	}
	return retrypolicy.MaxAttempts
}

func structuredJSONStructureRepairMaxAttempts(opts structuredJSONCallOptions) int {
	if opts.StructureRepairMaxAttempts > 0 {
		return opts.StructureRepairMaxAttempts
	}
	return defaultStructureRepairMaxAttempts
}

func waitStructuredJSONRetryDelay(ctx context.Context, opts structuredJSONCallOptions, attempt int) error {
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
