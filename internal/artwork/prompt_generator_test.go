package artwork

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

func TestArtworkPromptGeneratorMakesExactlyOnePlainTextCall(t *testing.T) {
	snapshot := mustSourceSnapshot(t, WorkTypeIllustration, "chapter", "one", ArtworkPromptTemplateVersion, []SourceFragment{{Kind: "summary", ID: "one", Content: "A supported scene."}})
	model := &artworkPromptTestModel{response: "  a single editable image prompt  "}
	prompt, usage, err := (PromptGenerator{Model: model, Template: "dedicated artwork system"}).GeneratePrompt(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "a single editable image prompt" || model.calls != 1 {
		t.Fatalf("prompt=%q calls=%d", prompt, model.calls)
	}
	if !usage.Present || usage.InputTokens != 11 || usage.OutputTokens != 7 || usage.TotalTokens != 18 {
		t.Fatalf("usage=%+v", usage)
	}
	if len(model.messages) != 2 || model.messages[0].Role != agentcore.RoleSystem || model.messages[0].TextContent() != "dedicated artwork system" {
		t.Fatalf("messages=%+v", model.messages)
	}
	if !strings.Contains(model.messages[1].TextContent(), snapshot.Digest) || !strings.Contains(model.messages[1].TextContent(), "A supported scene.") {
		t.Fatalf("bounded source missing from user message: %+v", model.messages)
	}
}

func TestArtworkPromptGeneratorNeverRepairsOrRetriesInvalidOutput(t *testing.T) {
	snapshot := mustSourceSnapshot(t, WorkTypeCover, "project", "", ArtworkPromptTemplateVersion, []SourceFragment{{Kind: "foundation", ID: "foundation", Content: "source"}})
	tests := []struct {
		name     string
		response string
		callErr  error
		wantErr  error
	}{
		{name: "empty", response: " \n ", wantErr: ErrPromptEmpty},
		{name: "too long", response: strings.Repeat("画", MaxAIPromptRunes+1), wantErr: ErrPromptTooLong},
		{name: "provider", callErr: errors.New("provider secret must stay internal"), wantErr: ErrPromptModel},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := &artworkPromptTestModel{response: test.response, err: test.callErr}
			_, _, err := (PromptGenerator{Model: model, Template: "artwork"}).GeneratePrompt(context.Background(), snapshot)
			if !errors.Is(err, test.wantErr) || model.calls != 1 {
				t.Fatalf("err=%v calls=%d want=%v", err, model.calls, test.wantErr)
			}
		})
	}
}

func TestArtworkPromptJobsPreserveImmutableLineageAndStaleConfirmation(t *testing.T) {
	root := t.TempDir()
	workspace, err := NewWorkspaceStore(root)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := workspace.CreateDraft(testDraftInput(), "lineage-draft")
	if err != nil {
		t.Fatal(err)
	}
	firstSource := mustSourceSnapshot(t, WorkTypeCover, "project", "", ArtworkPromptTemplateVersion, []SourceFragment{{Kind: "foundation", ID: "foundation", Content: "first canon"}})
	model := TextModelSnapshot{Provider: "fake-provider", Model: "fake-model", ReasoningEffort: "low"}
	firstJob, reused, err := workspace.CreatePromptJob(draft.ID, draft.Version, "prompt-one", firstSource, model)
	if err != nil || reused {
		t.Fatalf("first create job=%+v reused=%v err=%v", firstJob, reused, err)
	}
	if _, err := workspace.BeginPromptJob(firstJob.ID); err != nil {
		t.Fatal(err)
	}
	firstJob, draft, err = workspace.CompletePromptJob(firstJob.ID, "first AI prompt", PromptUsageSnapshot{Present: true, TotalTokens: 20})
	if err != nil {
		t.Fatal(err)
	}
	firstPromptPath := filepath.Join(root, "artwork", "prompts", firstJob.PromptVersionID+".json")
	firstPromptBefore, err := os.ReadFile(firstPromptPath)
	if err != nil {
		t.Fatal(err)
	}

	secondJob, reused, err := workspace.CreatePromptJob(draft.ID, draft.Version, "prompt-two", firstSource, TextModelSnapshot{Provider: "other-provider", Model: "other-model"})
	if err != nil || reused {
		t.Fatalf("second create job=%+v reused=%v err=%v", secondJob, reused, err)
	}
	if _, err := workspace.BeginPromptJob(secondJob.ID); err != nil {
		t.Fatal(err)
	}
	secondJob, draft, err = workspace.CompletePromptJob(secondJob.ID, "second AI prompt", PromptUsageSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if draft.CurrentPromptJobID != secondJob.ID || draft.PreviousPromptJobID != firstJob.ID || secondJob.PreviousPromptJobID != firstJob.ID {
		t.Fatalf("job lineage was not retained: first=%+v second=%+v draft=%+v", firstJob, secondJob, draft)
	}
	secondVersion, err := workspace.readPromptVersionUnlocked(secondJob.PromptVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if secondVersion.PreviousPromptVersionID != firstJob.PromptVersionID || secondVersion.Model == nil || secondVersion.Model.Provider != "other-provider" || secondVersion.SourceSnapshot == nil || secondVersion.SourceSnapshot.Digest != firstSource.Digest {
		t.Fatalf("second immutable prompt provenance=%+v", secondVersion)
	}
	firstPromptAfter, err := os.ReadFile(firstPromptPath)
	if err != nil || string(firstPromptBefore) != string(firstPromptAfter) {
		t.Fatalf("first prompt audit changed: err=%v", err)
	}
	jobs, err := workspace.ListPromptJobs("", 100, draft.ID)
	if err != nil || len(jobs.Items) != 2 {
		t.Fatalf("prompt history=%+v err=%v", jobs, err)
	}

	currentSource := mustSourceSnapshot(t, WorkTypeCover, "project", "", ArtworkPromptTemplateVersion, []SourceFragment{{Kind: "foundation", ID: "foundation", Content: "changed canon"}})
	if _, _, err := workspace.SubmitGenerationChecked(draft.ID, draft.Version, "image-before-confirm", "https://gateway.invalid", currentSource); !errors.Is(err, ErrStalePrompt) {
		t.Fatalf("stale submit err=%v", err)
	}
	if _, err := workspace.ConfirmStalePromptCurrent(draft.ID, draft.Version, firstSource.Digest, currentSource); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong confirmation digest err=%v", err)
	}
	confirmed, err := workspace.ConfirmStalePromptCurrent(draft.ID, draft.Version, currentSource.Digest, currentSource)
	if err != nil {
		t.Fatal(err)
	}
	imageJob, reused, err := workspace.SubmitGenerationChecked(draft.ID, confirmed.Version, "image-after-confirm", "https://gateway.invalid", currentSource)
	if err != nil || reused || imageJob.StaleConfirmation == nil || imageJob.StaleConfirmation.OriginalSourceDigest != firstSource.Digest || imageJob.StaleConfirmation.ConfirmedSourceDigest != currentSource.Digest {
		t.Fatalf("confirmed image job=%+v reused=%v err=%v", imageJob, reused, err)
	}
	if _, err := workspace.BeginJob(imageJob.ID); err != nil {
		t.Fatal(err)
	}
	asset, err := workspace.FinalizeJob(imageJob.ID, fixedPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	if asset.SourceSnapshot == nil || asset.SourceSnapshot.Digest != currentSource.Digest || asset.StaleConfirmation == nil || asset.PromptVersionID != secondJob.PromptVersionID || asset.Prompt != "second AI prompt" {
		t.Fatalf("asset provenance=%+v", asset)
	}
}

type artworkPromptTestModel struct {
	calls    int
	response string
	err      error
	messages []agentcore.Message
}

func (m *artworkPromptTestModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.calls++
	m.messages = append([]agentcore.Message(nil), messages...)
	if m.err != nil {
		return nil, m.err
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock(m.response)},
		Usage: &agentcore.Usage{Input: 11, Output: 7, TotalTokens: 18},
	}}, nil
}

func (m *artworkPromptTestModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, errors.New("unexpected stream call")
}

func (m *artworkPromptTestModel) SupportsTools() bool { return false }
