package artwork

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/globalprompt"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

const artworkPromptInputLimitBytes = 48 * 1024

type PromptGenerator struct {
	Model    agentcore.ChatModel
	Template string
	Store    *storepkg.Store
}

func (g PromptGenerator) GeneratePrompt(ctx context.Context, snapshot SourceSnapshot) (string, PromptUsageSnapshot, error) {
	if g.Model == nil {
		return "", PromptUsageSnapshot{}, fmt.Errorf("%w: configured text model is unavailable", ErrPromptModel)
	}
	if err := validateSourceSnapshot(snapshot); err != nil {
		return "", PromptUsageSnapshot{}, err
	}
	template := strings.TrimSpace(g.Template)
	if template == "" {
		return "", PromptUsageSnapshot{}, errors.New("artwork prompt template is empty")
	}
	contextText := renderPromptSource(snapshot)
	recorder, err := modeldiag.Begin(modeldiag.Request{
		Store: g.Store, Task: "artwork_prompt", System: template, User: []byte(contextText),
		InputLimitBytes: artworkPromptInputLimitBytes, OutputLimitTokens: 3000,
		SelectorCounts:    map[string]int{"source_fragments": len(snapshot.Fragments)},
		ContractSignature: snapshot.Digest,
	})
	if err != nil {
		return "", PromptUsageSnapshot{}, err
	}
	response, callErr := g.Model.Generate(
		globalprompt.WithoutModelPrompt(ctx),
		[]agentcore.Message{agentcore.SystemMsg(template), agentcore.UserMsg(contextText)},
		nil,
		agentcore.WithMaxTokens(3000),
	)
	if callErr != nil {
		_ = recorder.Finish(modeldiag.StatusProviderError, "", nil)
		return "", PromptUsageSnapshot{}, fmt.Errorf("%w: %v", ErrPromptModel, callErr)
	}
	if response == nil {
		_ = recorder.Finish(modeldiag.StatusEmptyResponse, "", nil)
		return "", PromptUsageSnapshot{}, ErrPromptEmpty
	}
	prompt := strings.TrimSpace(response.Message.TextContent())
	usage := promptUsageSnapshot(response.Message.Usage)
	if prompt == "" {
		_ = recorder.Finish(modeldiag.StatusEmptyResponse, prompt, response.Message.Usage)
		return "", usage, ErrPromptEmpty
	}
	if utf8.RuneCountInString(prompt) > MaxAIPromptRunes {
		_ = recorder.Finish(modeldiag.StatusInvalidSchema, prompt, response.Message.Usage)
		return "", usage, ErrPromptTooLong
	}
	if err := recorder.Finish(modeldiag.StatusCompleted, prompt, response.Message.Usage); err != nil {
		return "", usage, err
	}
	return prompt, usage, nil
}

func renderPromptSource(snapshot SourceSnapshot) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "work_type: %s\nscope: %s\nscope_id: %s\nsource_digest: %s\n", snapshot.WorkType, snapshot.Scope, snapshot.ScopeID, snapshot.Digest)
	for _, fragment := range snapshot.Fragments {
		fmt.Fprintf(&builder, "\n[%s | %s | %s]\n%s\n", fragment.Kind, fragment.ID, fragment.Label, fragment.Content)
	}
	return builder.String()
}

func promptUsageSnapshot(usage *agentcore.Usage) PromptUsageSnapshot {
	if usage == nil {
		return PromptUsageSnapshot{}
	}
	return PromptUsageSnapshot{
		Present: true, InputTokens: usage.Input, OutputTokens: usage.Output,
		TotalTokens: usage.TotalTokens,
	}
}
