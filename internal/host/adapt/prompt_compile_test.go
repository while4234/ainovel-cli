package adapt

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/promptcompile"
)

type fixedPromptCounter struct{ tokens int }

func (c fixedPromptCounter) CountTokens(context.Context, string) (int, error) { return c.tokens, nil }

type oversizedEvidenceCounter struct{}

func (oversizedEvidenceCounter) CountTokens(_ context.Context, text string) (int, error) {
	if strings.Contains(text, `"events"`) {
		return 50_000, nil
	}
	return 10, nil
}

func TestCompilePlannerCallLoadsOnlyCurrentMode(t *testing.T) {
	systemPrompt, userPrompt, diagnostics, err := compilePlannerCall(
		t.Context(),
		"planner role",
		`Planning input: {"granularity":"arc","events":["meet"]}`,
		fixedPromptCounter{tokens: 10},
	)
	if err != nil {
		t.Fatalf("compilePlannerCall: %v", err)
	}
	if diagnostics == nil || diagnostics.Mode != promptcompile.ModeArc {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	if strings.Contains(systemPrompt, "target_coherence") || strings.Contains(systemPrompt, "detail_preservation_with_split") {
		t.Fatalf("system prompt mixed adaptation modes: %s", systemPrompt)
	}
	if !strings.Contains(userPrompt, `"events":["meet"]`) {
		t.Fatalf("evidence payload was changed: %s", userPrompt)
	}
}

func TestCompilePlannerCallDoesNotTruncateOversizedJSON(t *testing.T) {
	payload := `{"granularity":"arc","events":["meet","case"]}`
	_, _, diagnostics, err := compilePlannerCall(t.Context(), "planner role", payload, oversizedEvidenceCounter{})
	var split *promptcompile.SplitRequiredError
	if !errors.As(err, &split) {
		t.Fatalf("expected SplitRequiredError, got %v", err)
	}
	if diagnostics != nil {
		t.Fatal("oversized prompt must not return a partial compiled payload")
	}
}

func TestCompilePlannerCallUsesExplicitModeForRepairPayloads(t *testing.T) {
	ctx := withAdaptationPromptContract(t.Context(), fixedPromptCounter{tokens: 10}, "free", "不得让人物无前因恋爱")
	_, userPrompt, diagnostics, err := compilePlannerCall(ctx, "planner role", `{"candidates":[{"chapter":2}]}`, promptTokenCounterFromContext(ctx))
	if err != nil {
		t.Fatalf("compile repair payload: %v", err)
	}
	if diagnostics == nil || diagnostics.Mode != promptcompile.ModeFree || diagnostics.RuleCount != 1 {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	if !strings.Contains(userPrompt, "不得让人物无前因恋爱") {
		t.Fatalf("active structured rule was not compiled: %s", userPrompt)
	}
}

func TestCompilePlannerCallFailsClosedWithoutMode(t *testing.T) {
	if _, _, _, err := compilePlannerCall(t.Context(), "planner role", `{"candidates":[]}`, fixedPromptCounter{tokens: 10}); err == nil {
		t.Fatal("planner call without an explicit or structured mode must fail before model invocation")
	}
}
