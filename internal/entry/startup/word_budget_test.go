package startup

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
)

func TestPrepareQuickUsesAPITargetTotalWords(t *testing.T) {
	plan, err := PrepareQuick(Request{
		Mode:             ModeQuick,
		UserPrompt:       "写一部20万字的小说，但以 API 为准",
		TargetTotalWords: 120000,
	})
	if err != nil {
		t.Fatalf("PrepareQuick: %v", err)
	}
	if plan.WordBudget == nil || plan.WordBudget.TargetTotalWords != 120000 {
		t.Fatalf("word budget = %+v, want api target", plan.WordBudget)
	}
	if plan.WordBudget.Source != domain.WordBudgetSourceAPI {
		t.Fatalf("source = %q, want api", plan.WordBudget.Source)
	}
	if !strings.Contains(plan.StartPrompt, "[篇幅契约]") ||
		!strings.Contains(plan.StartPrompt, "target_total_words=120000") {
		t.Fatalf("start prompt missing contract: %q", plan.StartPrompt)
	}
}

func TestCoCreateBuildPlanParsesDraftTotalWords(t *testing.T) {
	session := NewCoCreateSession("seed")
	session.ApplyReply(hostlessCoCreateReply("## 方向\n全书约10万字", true))

	plan, err := session.BuildPlan()
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.WordBudget == nil || plan.WordBudget.TargetTotalWords != 100000 {
		t.Fatalf("word budget = %+v, want prompt target", plan.WordBudget)
	}
	if !strings.Contains(plan.StartPrompt, "[篇幅契约]") {
		t.Fatalf("start prompt missing contract: %q", plan.StartPrompt)
	}
}

func TestCoCreateBuildPlanPreservesExplicitSingleChapterWithAPIBudget(t *testing.T) {
	session := NewCoCreateSession("seed")
	session.ApplyReply(hostlessCoCreateReply("## 篇幅\n全书约5000字，按一篇连续短篇处理，不拆多章", true))

	plan, err := session.BuildPlanWithWordBudget(5000)
	if err != nil {
		t.Fatalf("BuildPlanWithWordBudget: %v", err)
	}
	if plan.WordBudget == nil || plan.WordBudget.TargetTotalWords != 5000 || plan.WordBudget.RequestedChapters != 1 {
		t.Fatalf("word budget = %+v, want 5000 words and one requested chapter", plan.WordBudget)
	}
	if !strings.Contains(plan.StartPrompt, "requested_chapters=1") ||
		!strings.Contains(plan.StartPrompt, "outline 正文条目数必须严格等于该值") {
		t.Fatalf("start prompt missing hard chapter-count contract: %q", plan.StartPrompt)
	}
}

func hostlessCoCreateReply(prompt string, ready bool) host.CoCreateReply {
	return host.CoCreateReply{Prompt: prompt, Ready: ready, Message: "ok", Raw: "ok"}
}
