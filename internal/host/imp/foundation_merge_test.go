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

func TestFoundationMergeReportBatchesRespectCompiledByteLimit(t *testing.T) {
	reports := make([]domain.AdaptationSourceReport, 17)
	for index := range reports {
		marker := strings.Repeat("梦中女孩因果事实", 80)
		reports[index] = domain.AdaptationSourceReport{
			Chapter:        index + 1,
			Title:          "章节",
			Summary:        marker,
			CharacterFacts: []string{marker},
			KeyEvents:      []string{marker},
			WorldRules:     []string{marker},
			Timeline: []domain.TimelineEvent{{
				Time:  "当晚",
				Event: marker,
			}},
		}
	}
	prompt := strings.Repeat("汇总规则", 400) + " ${chapter_count}"
	if bytes := foundationMergeReportRequestBytes(prompt, reports); bytes <= structuredInputLimitBytes {
		t.Fatalf("test setup request bytes=%d, want over %d", bytes, structuredInputLimitBytes)
	}

	batches := FoundationMergeReportBatchesForPrompt(reports, DefaultFoundationMergeRunes, prompt)
	if len(batches) <= 1 {
		t.Fatalf("compiled byte limit should split reports, got %d batch", len(batches))
	}
	for index, batch := range batches {
		if bytes := foundationMergeReportRequestBytes(prompt, batch); bytes > structuredInputLimitBytes {
			t.Fatalf("batch %d request bytes=%d, limit=%d", index+1, bytes, structuredInputLimitBytes)
		}
	}
}

func TestFoundationMergeCharacterDecoderMigratesLegacyFieldsWithoutSilentLoss(t *testing.T) {
	envelope := testFoundationMergeEnvelope("Legacy")
	envelope = replaceEnvelopeBody(t, envelope, "CHARACTERS", `[
	  {
	    "name":"Hero",
	    "role":"lead",
	    "description":"tracks source causality",
	    "arc":"keeps moving",
	    "traits":["focused"],
	    "goals":["find the truth","protect the witness"],
	    "relationships":["trusts Mentor"]
	  }
	]`)
	result, err := parseFoundationMergeOutput(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Characters) != 1 ||
		result.Characters[0].Goal != "find the truth；protect the witness" ||
		!strings.Contains(result.Characters[0].Notes, "trusts Mentor") {
		t.Fatalf("migrated character = %+v", result.Characters)
	}
}

func TestFoundationMergeCharacterDecoderRejectsUnknownDriftField(t *testing.T) {
	envelope := testFoundationMergeEnvelope("Unknown")
	envelope = replaceEnvelopeBody(t, envelope, "CHARACTERS", `[
	  {"name":"Hero","role":"lead","description":"x","arc":"y","traits":[],"future_destiny":"invented"}
	]`)
	if _, err := parseFoundationMergeOutput(envelope); err == nil ||
		!strings.Contains(err.Error(), "unsupported field") {
		t.Fatalf("unknown drift err = %v", err)
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
