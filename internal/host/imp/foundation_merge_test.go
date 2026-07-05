package imp

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestMergeFoundationFromReportsBatchedSplitsAndRetries(t *testing.T) {
	reports := []domain.AdaptationSourceReport{
		testSourceReport(1, "Opening", "Alpha"),
		testSourceReport(2, "Turn", "Beta"),
		testSourceReport(3, "Reveal", "Gamma"),
	}
	llm := &scriptedStructuredLLM{
		responses: []structuredLLMResponse{
			{text: "not a valid foundation envelope"},
			{text: testFoundationMergeEnvelope("Batch Alpha")},
			{text: testFoundationMergeEnvelope("Batch Gamma")},
			{text: testFoundationMergeEnvelope("Partial Summary A")},
			{text: testFoundationMergeEnvelope("Partial Summary B")},
			{text: testFoundationMergeEnvelope("Final Source")},
		},
	}
	var events []FoundationMergeBatchEvent

	got, err := MergeFoundationFromReportsBatched(
		context.Background(),
		llm,
		"system ${chapter_count}",
		reports,
		StructuredCallOptions{MaxAttempts: 2, DisableStream: true, Sleep: noStructuredTestSleep},
		1800,
		func(ev FoundationMergeBatchEvent) { events = append(events, ev) },
	)
	if err != nil {
		t.Fatalf("MergeFoundationFromReportsBatched: %v", err)
	}
	if llm.calls < 4 {
		t.Fatalf("llm calls=%d, want at least 4", llm.calls)
	}
	if !strings.HasPrefix(got.Premise, "# ") {
		t.Fatalf("final premise missing heading: %q", got.Premise)
	}
	if outline := domain.FlattenOutline(got.Volumes); len(outline) != len(reports) {
		t.Fatalf("outline chapters=%d, want %d", len(outline), len(reports))
	}
	if len(events) < 3 || !events[len(events)-1].Final {
		t.Fatalf("expected batch progress plus final merge event, got %+v", events)
	}
}

func testSourceReport(chapter int, title, marker string) domain.AdaptationSourceReport {
	return domain.AdaptationSourceReport{
		Chapter:        chapter,
		Title:          title,
		Summary:        strings.Repeat(marker+" summary fact. ", 80),
		Characters:     []string{"Hero", marker},
		CharacterFacts: []string{marker + " changes the source causality."},
		KeyEvents:      []string{marker + " irreversible event"},
		WorldRules:     []string{marker + " continuity rule"},
		HookType:       "mystery",
		DominantStrand: "quest",
	}
}

func testFoundationMergeEnvelope(name string) string {
	return `=== PREMISE ===
# ` + name + `

Source facts merged in order.

=== CHARACTERS ===
[
  {"name":"Hero","role":"lead","description":"tracks source causality","arc":"keeps moving","traits":["focused"]}
]

=== WORLD_RULES ===
[
  {"category":"continuity","rule":"source facts remain causal","boundary":"do not invent unsupported events"}
]

=== COMPASS ===
{
  "ending_direction":"preserve the source causal chain",
  "open_threads":["source mystery"],
  "estimated_scale":"source-sized"
}
`
}
