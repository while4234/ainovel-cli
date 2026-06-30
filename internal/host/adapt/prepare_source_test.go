package adapt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type scriptedAdaptLLM struct {
	responses []adaptLLMResponse
	calls     int
	got       [][]agentcore.Message
}

type adaptLLMResponse struct {
	text string
	err  error
}

func (m *scriptedAdaptLLM) Generate(_ context.Context, msgs []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.got = append(m.got, msgs)
	if m.calls >= len(m.responses) {
		return nil, context.Canceled
	}
	resp := m.responses[m.calls]
	m.calls++
	if resp.err != nil {
		return nil, resp.err
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:      agentcore.RoleAssistant,
		Content:   []agentcore.ContentBlock{agentcore.TextBlock(resp.text)},
		Timestamp: time.Now(),
	}}, nil
}

func TestPrepareSourceResumesMissingChapterAndMergesWithoutRawBody(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	sourcePath := writeAdaptSource(t, t.TempDir(), []string{
		"RAW_BODY_ONE_UNIQUE",
		"RAW_BODY_TWO_UNIQUE",
		"RAW_BODY_THREE_UNIQUE",
	})

	first := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: adaptAnalyzerEnvelope(1)},
		{text: adaptAnalyzerEnvelope(2)},
		{err: context.Canceled},
	}}
	err := PrepareSource(context.Background(), Deps{
		Store: st,
		LLM:   first,
		Prompts: Prompts{
			Analyzer:        "analyzer",
			FoundationMerge: "merge",
		},
	}, sourcePath, nil)
	if err == nil {
		t.Fatal("want first interrupted run to fail")
	}
	if first.calls != 3 {
		t.Fatalf("first calls=%d, want 3", first.calls)
	}
	if report, err := st.Adaptation.LoadSourceReport(1); err != nil || report == nil {
		t.Fatalf("chapter 1 report should be saved: report=%+v err=%v", report, err)
	}
	if report, err := st.Adaptation.LoadSourceReport(2); err != nil || report == nil {
		t.Fatalf("chapter 2 report should be saved: report=%+v err=%v", report, err)
	}
	if report, err := st.Adaptation.LoadSourceReport(3); err != nil || report != nil {
		t.Fatalf("chapter 3 report should not be saved: report=%+v err=%v", report, err)
	}
	if foundation, err := st.Adaptation.LoadSourceFoundation(); err != nil || foundation != nil {
		t.Fatalf("foundation should not be saved after interrupted run: foundation=%+v err=%v", foundation, err)
	}

	second := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: adaptAnalyzerEnvelope(3)},
		{text: adaptFoundationMergeEnvelope()},
	}}
	if err := PrepareSource(context.Background(), Deps{
		Store: st,
		LLM:   second,
		Prompts: Prompts{
			Analyzer:        "analyzer",
			FoundationMerge: "merge",
		},
	}, sourcePath, nil); err != nil {
		t.Fatalf("PrepareSource resume: %v", err)
	}
	if second.calls != 2 {
		t.Fatalf("resume calls=%d, want missing chapter plus merge", second.calls)
	}
	reports, err := st.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		t.Fatalf("LoadCompleteSourceReports: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("reports=%d, want 3", len(reports))
	}
	foundation, err := st.Adaptation.LoadSourceFoundation()
	if err != nil {
		t.Fatalf("LoadSourceFoundation: %v", err)
	}
	if foundation == nil || len(domain.FlattenOutline(foundation.Volumes)) != 3 {
		t.Fatalf("foundation outline should have 3 chapters: %+v", foundation)
	}

	mergePrompt := second.got[len(second.got)-1][1].TextContent()
	for _, raw := range []string{"RAW_BODY_ONE_UNIQUE", "RAW_BODY_TWO_UNIQUE", "RAW_BODY_THREE_UNIQUE"} {
		if strings.Contains(mergePrompt, raw) {
			t.Fatalf("merge prompt must not contain raw source body %q: %s", raw, mergePrompt)
		}
	}
}

func TestPrepareSourceSourceChangeResetsOldReports(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	dir := t.TempDir()
	sourcePath := writeAdaptSource(t, dir, []string{"OLD_BODY_ONE", "OLD_BODY_TWO"})
	first := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: adaptAnalyzerEnvelope(1)},
		{text: adaptAnalyzerEnvelope(2)},
		{text: adaptFoundationMergeEnvelope()},
	}}
	if err := PrepareSource(context.Background(), Deps{
		Store: st,
		LLM:   first,
		Prompts: Prompts{
			Analyzer:        "analyzer",
			FoundationMerge: "merge",
		},
	}, sourcePath, nil); err != nil {
		t.Fatalf("PrepareSource first: %v", err)
	}

	sourcePath = writeAdaptSource(t, dir, []string{"OLD_BODY_ONE", "NEW_BODY_TWO"})
	second := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: adaptAnalyzerEnvelope(1)},
		{text: adaptAnalyzerEnvelope(2)},
		{text: adaptFoundationMergeEnvelope()},
	}}
	if err := PrepareSource(context.Background(), Deps{
		Store: st,
		LLM:   second,
		Prompts: Prompts{
			Analyzer:        "analyzer",
			FoundationMerge: "merge",
		},
	}, sourcePath, nil); err != nil {
		t.Fatalf("PrepareSource changed source: %v", err)
	}
	if second.calls != 3 {
		t.Fatalf("changed source should reanalyze all chapters and merge, calls=%d", second.calls)
	}
}

func writeAdaptSource(t *testing.T, dir string, bodies []string) string {
	t.Helper()
	var sb strings.Builder
	for i, body := range bodies {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("Chapter ")
		sb.WriteString(string(rune('1' + i)))
		sb.WriteString(": Title\n")
		sb.WriteString(body)
		sb.WriteString("\n")
	}
	path := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	return path
}

func adaptAnalyzerEnvelope(chapter int) string {
	return `=== SUMMARY ===
Chapter summary.

=== CHARACTERS ===
["Ari"]

=== CHARACTER_FACTS ===
["Ari advances chapter facts."]

=== WORLD_RULES ===
["The city keeps strict records."]

=== KEY_EVENTS ===
["Key event happens."]

=== TIMELINE ===
[]

=== FORESHADOW ===
[]

=== RELATIONSHIPS ===
[]

=== STATE_CHANGES ===
[]

=== HOOK_TYPE ===
mystery

=== DOMINANT_STRAND ===
quest
`
}

func adaptFoundationMergeEnvelope() string {
	return `=== PREMISE ===
# Source Book

Ari follows the source causal chain.

=== CHARACTERS ===
[{"name":"Ari","role":"lead","description":"Follows the central case.","arc":"chooses courage","traits":["focused"]}]

=== WORLD_RULES ===
[{"category":"society","rule":"The city keeps strict records.","boundary":"Records cannot be ignored."}]

=== COMPASS ===
{"ending_direction":"Ari resolves the source case.","open_threads":["who controls the records"],"estimated_scale":"short"}
`
}
