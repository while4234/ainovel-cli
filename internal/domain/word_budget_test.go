package domain

import "testing"

func TestParseWordBudgetFromTextTotalTarget(t *testing.T) {
	budget, ok := ParseWordBudgetFromText("写一部约20万字的都市悬疑小说", WordBudgetSourcePrompt)
	if !ok {
		t.Fatal("expected word budget")
	}
	if budget.TargetTotalWords != 200000 {
		t.Fatalf("target_total_words = %d, want 200000", budget.TargetTotalWords)
	}
	if budget.TotalMinWords != 180000 || budget.TotalMaxWords != 220000 {
		t.Fatalf("range = %d-%d, want 180000-220000", budget.TotalMinWords, budget.TotalMaxWords)
	}
	if budget.Source != WordBudgetSourcePrompt {
		t.Fatalf("source = %q, want prompt", budget.Source)
	}
}

func TestParseWordBudgetFromTextShortFiveThousand(t *testing.T) {
	budget, ok := ParseWordBudgetFromText("我想写一本短篇约5000字的NTR小说", WordBudgetSourcePrompt)
	if !ok {
		t.Fatal("expected word budget")
	}
	if budget.TargetTotalWords != 5000 || budget.TotalMinWords != 4500 || budget.TotalMaxWords != 5500 {
		t.Fatalf("budget = %+v", budget)
	}
}

func TestParseWordBudgetFromTextRange(t *testing.T) {
	budget, ok := ParseWordBudgetFromText("全书80-100万字，长篇群像", "")
	if !ok {
		t.Fatal("expected word budget")
	}
	if budget.TotalMinWords != 800000 || budget.TotalMaxWords != 1000000 || budget.TargetTotalWords != 900000 {
		t.Fatalf("budget = %+v", budget)
	}
}

func TestParseWordBudgetFromTextIgnoresPerChapter(t *testing.T) {
	if budget, ok := ParseWordBudgetFromText("每章3000字，共20章", ""); ok || budget != nil {
		t.Fatalf("per-chapter budget should not become total budget: %+v", budget)
	}
}

func TestWordBudgetWithPlannedChapters(t *testing.T) {
	base, ok := NewWordBudgetFromTarget(100000, WordBudgetSourceAPI)
	if !ok {
		t.Fatal("expected budget")
	}
	budget := base.WithPlannedChapters(20)
	if budget.PlannedChapters != 20 || budget.ChapterMinWords != 4500 || budget.ChapterMaxWords != 5500 {
		t.Fatalf("planned budget = %+v", budget)
	}
}

func TestParseRequestedChapterCount(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
		ok   bool
	}{
		{name: "single continuous short story", text: "篇幅：全书约 5000 字，按一篇连续短篇处理，不拆多章", want: 1, ok: true},
		{name: "bare single chapter choice", text: "5000字，单章", want: 1, ok: true},
		{name: "explicit total", text: "全书共9章，每章约4000字", want: 9, ok: true},
		{name: "chapter count field", text: "总章数：二十章", want: 20, ok: true},
		{name: "per chapter only", text: "单章5000字，节奏紧凑", ok: false},
		{name: "ordinal chapter", text: "第一章从雨夜开始", ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ParseRequestedChapterCount(test.text)
			if got != test.want || ok != test.ok {
				t.Fatalf("ParseRequestedChapterCount(%q) = (%d, %v), want (%d, %v)", test.text, got, ok, test.want, test.ok)
			}
		})
	}
}
