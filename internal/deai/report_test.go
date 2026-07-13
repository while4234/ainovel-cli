package deai

import (
	"strings"
	"testing"
)

func TestAnalyzeFlagsConcreteDeAISymptoms(t *testing.T) {
	text := `# 第一章

## 一
然后他没有说话——不是因为不想说，而是因为不能说——像一盏灯一样。

然后他没有回答——不是因为害怕，而是因为没有办法——仿佛一块石头。

然后他沉默了——不是因为冷静，而是因为无法离开——如同一扇门，没有灯，没有门，没有路。`
	report := Analyze(text)
	if report.Passed() {
		t.Fatalf("expected repair findings, got %+v", report)
	}
	if report.Metrics.MarkdownSubheadings != 1 {
		t.Fatalf("subheadings = %d", report.Metrics.MarkdownSubheadings)
	}
	if report.Metrics.EmDashes == 0 || report.Metrics.TripleParallelPatterns == 0 {
		t.Fatalf("expected dash/parallel metrics, got %+v", report.Metrics)
	}
}

func TestAnalyzeAllowsAPlainChapter(t *testing.T) {
	text := `# 第一章

窗外的雨刚停。许舟把湿伞靠在门边，没急着进屋。

厨房里传来碗筷碰撞声。他听了一会儿，才抬手敲门。`
	if report := Analyze(text); !report.Passed() {
		t.Fatalf("plain chapter should pass: %+v", report)
	}
}

func TestAnalyzeRequiresAUniqueH1ChapterTitle(t *testing.T) {
	report := Analyze("## 第一章\n\n正文。")
	if report.Metrics.InvalidChapterTitles != 1 || report.Passed() {
		t.Fatalf("invalid title report = %+v", report)
	}
}

func TestAuditRepairSummary(t *testing.T) {
	report := Report{Findings: []Finding{{Code: "x", Severity: SeverityAttention}, {Code: "y", Severity: SeverityRepair, RepairHint: "fix it"}}}
	if got := report.RepairSummary(); got != "y: fix it" {
		t.Fatalf("RepairSummary = %q", got)
	}
}

func TestAnalyzeIncludesVerbatimRepairExamples(t *testing.T) {
	report := Analyze("# 第一章\n\n他停在门外——没有进去。\n\n他又走开——没有回头。\n\n他第三次停下——仍旧没有进去。\n\n他第四次停下——没有说话。")
	var dash Finding
	for _, finding := range report.Findings {
		if finding.Code == "em_dash_overuse" {
			dash = finding
			break
		}
	}
	if len(dash.Examples) == 0 || !strings.Contains(dash.Examples[0], "——") {
		t.Fatalf("dash examples = %+v", dash)
	}
}
