package host

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestCoCreateSystemPromptWithSimulationNormalEqualsBasePrompt(t *testing.T) {
	st := newSimulationPromptTestStore(t, true)

	if got := coCreateSystemPromptWithSimulation(st, bootstrap.SimulationModeNormal); got != coCreateSystemPrompt {
		t.Fatalf("normal co-create prompt changed")
	}
}

func TestStageSystemPromptWithSimulationNormalDoesNotInjectProfile(t *testing.T) {
	st := newSimulationPromptTestStore(t, true)
	if err := st.Progress.Init("星河试炼", 12); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}

	got := stageSystemPromptWithSimulation(st, bootstrap.SimulationModeNormal)
	if want := stageSystemPrompt(st); got != want {
		t.Fatalf("normal stage co-create prompt changed")
	}
	for _, blocked := range []string{"强化仿写", "仿写画像", "## 仿写方向", "冷峻旁观叙述"} {
		if strings.Contains(got, blocked) {
			t.Fatalf("normal stage prompt contains %q:\n%s", blocked, got)
		}
	}
}

func TestCoCreateSystemPromptWithSimulationReinforcedWithoutProfileUsesBasePrompt(t *testing.T) {
	st := newSimulationPromptTestStore(t, false)

	got := coCreateSystemPromptWithSimulation(st, bootstrap.SimulationModeReinforced)
	if got != coCreateSystemPrompt {
		t.Fatalf("reinforced co-create prompt without profile changed")
	}
	for _, blocked := range []string{"强化仿写", "仿写画像", "## 仿写方向"} {
		if strings.Contains(got, blocked) {
			t.Fatalf("prompt without profile contains %q:\n%s", blocked, got)
		}
	}
}

func TestStageSystemPromptWithSimulationReinforcedWithoutProfileUsesBasePrompt(t *testing.T) {
	st := newSimulationPromptTestStore(t, false)
	if err := st.Progress.Init("星河试炼", 12); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}

	got := stageSystemPromptWithSimulation(st, bootstrap.SimulationModeReinforced)
	if want := stageSystemPrompt(st); got != want {
		t.Fatalf("reinforced stage co-create prompt without profile changed")
	}
	for _, blocked := range []string{"强化仿写", "仿写画像", "## 仿写方向"} {
		if strings.Contains(got, blocked) {
			t.Fatalf("stage prompt without profile contains %q:\n%s", blocked, got)
		}
	}
}

func TestCoCreateSystemPromptWithSimulationReinforcedInjectsCompactProfile(t *testing.T) {
	st := newSimulationPromptTestStore(t, true)

	got := coCreateSystemPromptWithSimulation(st, bootstrap.SimulationModeReinforced)
	for _, want := range []string{
		"强化仿写",
		"用户不需要显式说",
		"## 仿写方向",
		"禁止复制",
		"源文本句子",
		"固定桥段",
		`"mode": "reinforced"`,
		"冷峻旁观叙述",
		"先展示代价再揭示规则",
		"章尾抛出反转钩子",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("reinforced prompt missing %q:\n%s", want, got)
		}
	}
	assertNoRawSimulationSourceInfo(t, got)
}

func TestStageSystemPromptWithSimulationPreservesStoryState(t *testing.T) {
	st := newSimulationPromptTestStore(t, true)
	if err := st.Progress.Init("星河试炼", 12); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatalf("Progress.Load: %v", err)
	}
	progress.CompletedChapters = []int{1, 2}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}

	got := stageSystemPromptWithSimulation(st, bootstrap.SimulationModeReinforced)
	for _, want := range []string{"## 当前故事状态", "星河试炼", "强化仿写", "仿写画像", "## 仿写方向", "冷峻旁观叙述"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stage prompt missing %q:\n%s", want, got)
		}
	}
	assertNoRawSimulationSourceInfo(t, got)
}

func TestAdaptSystemPromptIgnoresSimulationProfile(t *testing.T) {
	st := newSimulationPromptTestStore(t, false)
	before := adaptSystemPrompt(st)
	if err := st.Simulation.Save(simulationPromptTestProfile()); err != nil {
		t.Fatalf("Simulation.Save: %v", err)
	}
	after := adaptSystemPrompt(st)

	if after != before {
		t.Fatalf("adapt prompt changed after saving simulation profile")
	}
	for _, blocked := range []string{"强化仿写", "仿写画像", "## 仿写方向"} {
		if strings.Contains(after, blocked) {
			t.Fatalf("adapt prompt contains simulation guidance %q:\n%s", blocked, after)
		}
	}
}

func newSimulationPromptTestStore(t *testing.T, withProfile bool) *store.Store {
	t.Helper()
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Store.Init: %v", err)
	}
	if withProfile {
		if err := st.Simulation.Save(simulationPromptTestProfile()); err != nil {
			t.Fatalf("Simulation.Save: %v", err)
		}
	}
	return st
}

func assertNoRawSimulationSourceInfo(t *testing.T, prompt string) {
	t.Helper()
	for _, blocked := range []string{
		"source_files",
		"simulate/",
		"source_reports",
		"RAW_SOURCE_REPORT_SENTENCE",
		"原始报告里的专属桥段",
		"这个原始报告细节不应进入共创提示",
	} {
		if strings.Contains(prompt, blocked) {
			t.Fatalf("prompt leaked raw simulation source info %q:\n%s", blocked, prompt)
		}
	}
}

func simulationPromptTestProfile() domain.SimulationProfile {
	return domain.SimulationProfile{
		Version:   domain.SimulationProfileVersion,
		UpdatedAt: "2026-07-09T00:00:00Z",
		Corpus: domain.SimulationCorpusManifest{
			Sources: []domain.SimulationSource{
				{RelativePath: "simulate/a.txt", SHA256: "sha-a"},
				{RelativePath: "simulate/b.txt", SHA256: "sha-b"},
				{RelativePath: "simulate/c.txt", SHA256: "sha-c"},
				{RelativePath: "simulate/d.txt", SHA256: "sha-d"},
				{RelativePath: "simulate/e.txt", SHA256: "sha-e"},
				{RelativePath: "simulate/f.txt", SHA256: "sha-f"},
			},
		},
		SourceReports: []domain.SimulationSourceReport{{
			RelativePath:      "simulate/raw.txt",
			Summary:           "RAW_SOURCE_REPORT_SENTENCE 原始报告里的专属桥段",
			StyleObservations: []string{"这个原始报告细节不应进入共创提示"},
		}},
		Synthesis: domain.SimulationSynthesis{
			Style: domain.SimulationStyle{
				NarrativeVoice: []string{"冷峻旁观叙述", "有限视角压住解释"},
				SentenceRhythm: []string{"短句接长句形成停顿"},
				ProseTexture:   []string{"雾灯、潮湿金属、低频警报"},
				DoNotCopy:      []string{"不要复制人物、地名和专有设定"},
			},
			Lexicon: domain.SimulationLexicon{
				CommonWords: []string{"裂隙", "回声", "余温"},
			},
			PlotDesign: domain.SimulationPlotDesign{
				OpeningPatterns: []string{"先展示代价再揭示规则"},
				PayoffPatterns:  []string{"小承诺在三幕后回收"},
			},
			HookDesign: domain.SimulationHookDesign{
				HookTypes:           []string{"章尾抛出反转钩子"},
				CliffhangerPatterns: []string{"胜利后暴露更高代价"},
			},
			PacingDensity: domain.SimulationPacingDensity{
				InformationRelease: []string{"每场只释放一个核心谜底"},
			},
		},
	}
}
