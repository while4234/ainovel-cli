package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestContextToolInjectsCompactSimulationProfile(t *testing.T) {
	dir := testStoreDir(t)
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	profile := domain.SimulationProfile{
		Version: domain.SimulationProfileVersion,
		Corpus: domain.SimulationCorpusManifest{
			Sources: []domain.SimulationSource{{
				RelativePath: "a.txt",
				SHA256:       "sha-a",
				Fingerprint:  domain.SimulationSourceFingerprint("a.txt", "sha-a"),
			}},
		},
		SourceReports: []domain.SimulationSourceReport{{
			RelativePath: "a.txt",
			SHA256:       "sha-a",
			Fingerprint:  domain.SimulationSourceFingerprint("a.txt", "sha-a"),
			Summary:      "full report should not be injected",
		}},
		Synthesis: domain.SimulationSynthesis{
			Style: domain.SimulationStyle{
				NarrativeVoice: []string{"close third"},
			},
			RoleGuidance: domain.SimulationRoleGuidance{
				Coordinator: []string{"keep tasks aligned"},
				Architect:   []string{"escalate costs"},
				Writer:      []string{"borrow technique only"},
				Editor:      []string{"check non-copying"},
			},
		},
	}
	if err := st.Simulation.Save(profile); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "Start", CoreEvent: "Begin"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("test", 1); err != nil {
		t.Fatal(err)
	}

	tool := NewContextTool(st, References{}, "default")
	architectRaw, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("architect Execute: %v", err)
	}
	var architect map[string]any
	if err := json.Unmarshal(architectRaw, &architect); err != nil {
		t.Fatal(err)
	}
	assertCompactSimulationProfile(t, architect, "planning_memory")
	if _, exists := architect["simulation_mode"]; exists {
		t.Fatal("normal planning context must not include top-level simulation_mode")
	}

	chapterRaw, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatalf("chapter Execute: %v", err)
	}
	if bytes.Contains(chapterRaw, []byte("source_reports")) {
		t.Fatal("normal chapter context JSON must not include source_reports")
	}
	if bytes.Contains(chapterRaw, []byte("full report should not be injected")) {
		t.Fatal("normal chapter context JSON leaked source report text")
	}
	var chapter map[string]any
	if err := json.Unmarshal(chapterRaw, &chapter); err != nil {
		t.Fatal(err)
	}
	assertCompactSimulationProfile(t, chapter, "working_memory")
	if _, exists := chapter["simulation_mode"]; exists {
		t.Fatal("normal chapter context must not include top-level simulation_mode")
	}
}

func TestContextToolChapterSimulationDoesNotOverrideOutlineViewpoint(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	profile := domain.SimulationProfile{
		Version: domain.SimulationProfileVersion,
		Synthesis: domain.SimulationSynthesis{
			Style: domain.SimulationStyle{
				NarrativeVoice: []string{"strictly follow the male lead"},
				Perspective:    []string{"never reveal the female lead's interior"},
				SentenceRhythm: []string{"alternate compressed action and reflective pauses"},
				ProseTexture:   []string{"use concrete sensory detail"},
			},
			RoleGuidance: domain.SimulationRoleGuidance{
				Writer: []string{"borrow information-control technique only"},
			},
		},
	}
	if err := st.Simulation.Save(profile); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1,
		Title:   "Start",
		Scenes:  []string{"scene one follows the female lead"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("test", 1); err != nil {
		t.Fatal(err)
	}

	raw, err := NewContextTool(st, References{}, "default").Execute(t.Context(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("strictly follow the male lead")) ||
		bytes.Contains(raw, []byte("never reveal the female lead")) {
		t.Fatalf("chapter context leaked corpus viewpoint ownership: %s", raw)
	}
	if !bytes.Contains(raw, []byte("alternate compressed action")) ||
		!bytes.Contains(raw, []byte("concrete sensory detail")) {
		t.Fatalf("chapter context lost reusable prose technique: %s", raw)
	}
}

func TestContextToolReinforcedSimulationProfileUsesExpandedCompactProfile(t *testing.T) {
	dir := testStoreDir(t)
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	profile := domain.SimulationProfile{
		Version: domain.SimulationProfileVersion,
		Corpus: domain.SimulationCorpusManifest{
			Sources: []domain.SimulationSource{
				{RelativePath: "source-1.txt", SHA256: "sha-1", Fingerprint: domain.SimulationSourceFingerprint("source-1.txt", "sha-1")},
				{RelativePath: "source-2.txt", SHA256: "sha-2", Fingerprint: domain.SimulationSourceFingerprint("source-2.txt", "sha-2")},
				{RelativePath: "source-3.txt", SHA256: "sha-3", Fingerprint: domain.SimulationSourceFingerprint("source-3.txt", "sha-3")},
				{RelativePath: "source-4.txt", SHA256: "sha-4", Fingerprint: domain.SimulationSourceFingerprint("source-4.txt", "sha-4")},
				{RelativePath: "source-5.txt", SHA256: "sha-5", Fingerprint: domain.SimulationSourceFingerprint("source-5.txt", "sha-5")},
				{RelativePath: "source-6.txt", SHA256: "sha-6", Fingerprint: domain.SimulationSourceFingerprint("source-6.txt", "sha-6")},
			},
		},
		SourceReports: []domain.SimulationSourceReport{{
			RelativePath: "source-1.txt",
			SHA256:       "sha-1",
			Fingerprint:  domain.SimulationSourceFingerprint("source-1.txt", "sha-1"),
			Summary:      "source_reports must not be injected into novel_context",
		}},
		Synthesis: domain.SimulationSynthesis{
			Style: domain.SimulationStyle{
				NarrativeVoice: simulationItems("voice", 6),
			},
			PlotDesign: domain.SimulationPlotDesign{
				OpeningPatterns: simulationItems("opening", 6),
			},
			HookDesign: domain.SimulationHookDesign{
				HookTypes: simulationItems("hook", 6),
			},
			RoleGuidance: domain.SimulationRoleGuidance{
				Architect: simulationItems("architect", 6),
				Writer:    simulationItems("writer", 6),
				Editor:    simulationItems("editor", 6),
			},
		},
	}
	if err := st.Simulation.Save(profile); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "Start", CoreEvent: "Begin"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("test", 1); err != nil {
		t.Fatal(err)
	}

	normalTool := NewContextTool(st, References{}, "default")
	normalRaw, err := normalTool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("normal Execute: %v", err)
	}
	var normal map[string]any
	if err := json.Unmarshal(normalRaw, &normal); err != nil {
		t.Fatal(err)
	}
	normalCompact := compactSimulationProfileFromSection(t, normal, "planning_memory")

	reinforcedTool := NewContextToolWithOptions(st, References{}, "default", ContextToolOptions{SimulationMode: "reinforced"})
	reinforcedRaw, err := reinforcedTool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("reinforced Execute: %v", err)
	}
	if bytes.Contains(reinforcedRaw, []byte("source_reports")) {
		t.Fatal("reinforced novel_context JSON must not include source_reports")
	}
	if bytes.Contains(reinforcedRaw, []byte("source_reports must not be injected")) {
		t.Fatal("reinforced novel_context JSON leaked source report text")
	}
	var reinforced map[string]any
	if err := json.Unmarshal(reinforcedRaw, &reinforced); err != nil {
		t.Fatal(err)
	}
	reinforcedCompact := compactSimulationProfileFromSection(t, reinforced, "planning_memory")

	if got := reinforced["simulation_mode"]; got != "reinforced" {
		t.Fatalf("top-level simulation_mode = %#v, want reinforced", got)
	}
	if got := reinforcedCompact["mode"]; got != "reinforced" {
		t.Fatalf("compact profile mode = %#v, want reinforced", got)
	}
	if !strings.Contains(stringValue(reinforced["_loading_summary"]), "仿写模式:reinforced") {
		t.Fatalf("loading summary = %q, want reinforced simulation mode", reinforced["_loading_summary"])
	}
	if got := normalCompact["mode"]; got != nil {
		t.Fatalf("normal compact mode = %#v, want absent", got)
	}
	if _, exists := normal["simulation_mode"]; exists {
		t.Fatal("normal context must not include top-level simulation_mode")
	}
	if !strings.Contains(stringValue(normal["_loading_summary"]), "仿写模式:ok") {
		t.Fatalf("normal loading summary = %q, want ok simulation mode", normal["_loading_summary"])
	}
	if style, exists := reinforcedCompact["style"].(map[string]any); exists && len(style) > 0 {
		t.Fatal("planning simulation profile must omit prose style owned by chapter work")
	}
	if lexicon, exists := reinforcedCompact["lexicon"].(map[string]any); exists && len(lexicon) > 0 {
		t.Fatal("planning simulation profile must omit prose lexicon owned by chapter work")
	}
	plot := nestedMap(t, reinforcedCompact, "plot_design")
	if sliceLenAny(plot["opening_patterns"]) == 0 {
		t.Fatal("planning simulation profile must retain structural plot guidance")
	}

	reinforcedChapterRaw, err := reinforcedTool.Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatalf("reinforced chapter Execute: %v", err)
	}
	if bytes.Contains(reinforcedChapterRaw, []byte("source_reports")) {
		t.Fatal("reinforced chapter context JSON must not include source_reports")
	}
	if bytes.Contains(reinforcedChapterRaw, []byte("source_reports must not be injected")) {
		t.Fatal("reinforced chapter context JSON leaked source report text")
	}
	var reinforcedChapter map[string]any
	if err := json.Unmarshal(reinforcedChapterRaw, &reinforcedChapter); err != nil {
		t.Fatal(err)
	}
	if got := reinforcedChapter["simulation_mode"]; got != "reinforced" {
		t.Fatalf("chapter simulation_mode = %#v, want reinforced", got)
	}
	reinforcedChapterCompact := compactSimulationProfileFromSection(t, reinforcedChapter, "working_memory")
	if got := reinforcedChapterCompact["mode"]; got != "reinforced" {
		t.Fatalf("chapter compact profile mode = %#v, want reinforced", got)
	}
}

func assertCompactSimulationProfile(t *testing.T, payload map[string]any, section string) map[string]any {
	t.Helper()
	if got := payload["simulation_profile"]; got != true {
		t.Fatalf("expected top-level simulation_profile marker, got %#v", got)
	}
	sectionMap, ok := payload[section].(map[string]any)
	if !ok {
		t.Fatalf("expected %s", section)
	}
	compact, ok := sectionMap["simulation_profile"].(map[string]any)
	if !ok {
		t.Fatalf("expected simulation_profile under %s", section)
	}
	if _, exists := compact["source_reports"]; exists {
		t.Fatal("compact simulation_profile must not include source_reports")
	}
	if got := compact["source_count"]; got != float64(1) {
		t.Fatalf("source_count = %v, want 1", got)
	}
	return compact
}

func compactSimulationProfileFromSection(t *testing.T, payload map[string]any, section string) map[string]any {
	t.Helper()
	sectionMap, ok := payload[section].(map[string]any)
	if !ok {
		t.Fatalf("expected %s", section)
	}
	compact, ok := sectionMap["simulation_profile"].(map[string]any)
	if !ok {
		t.Fatalf("expected simulation_profile under %s", section)
	}
	return compact
}

func nestedMap(t *testing.T, payload map[string]any, key string) map[string]any {
	t.Helper()
	nested, ok := payload[key].(map[string]any)
	if !ok {
		t.Fatalf("expected nested map %s", key)
	}
	return nested
}

func sliceLenAny(v any) int {
	items, ok := v.([]any)
	if !ok {
		return 0
	}
	return len(items)
}

func stringValue(v any) string {
	text, _ := v.(string)
	return text
}

func simulationItems(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + "-" + string(rune('a'+i))
	}
	return out
}
