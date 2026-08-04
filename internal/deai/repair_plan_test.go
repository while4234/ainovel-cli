package deai

import (
	"strings"
	"testing"
)

func TestRepairPlanGroupsMultipleFindingsIntoBoundedBatches(t *testing.T) {
	report := Report{Findings: []Finding{
		{Code: "em_dash_overuse", Severity: SeverityRepair, Actual: 11, Limit: 3},
		{Code: "reaction_template_overuse", Severity: SeverityRepair, Actual: 8, Limit: 2},
		{Code: "simile_overuse", Severity: SeverityRepair, Actual: 6, Limit: 4},
		{Code: "fragmented_paragraph_rhythm", Severity: SeverityAttention, Actual: 34, Limit: 45},
	}}

	plan := report.RepairPlan()
	if plan.Mode != "batched" {
		t.Fatalf("mode = %q, want batched", plan.Mode)
	}
	if len(plan.Batches) != 3 {
		t.Fatalf("batches = %#v, want punctuation/expression/rhythm", plan.Batches)
	}
	if plan.Batches[0].ID != "punctuation" || plan.Batches[0].SuggestedEdits != 8 {
		t.Fatalf("punctuation batch = %#v", plan.Batches[0])
	}
	if plan.Batches[1].ID != "expression" || plan.Batches[1].SuggestedEdits != 2 {
		t.Fatalf("expression batch = %#v", plan.Batches[1])
	}
	if plan.Batches[2].ID != "rhythm" || plan.Batches[2].SuggestedEdits != 6 {
		t.Fatalf("rhythm batch = %#v", plan.Batches[2])
	}
	if len(plan.Attention) != 1 || plan.Attention[0].Code != "fragmented_paragraph_rhythm" {
		t.Fatalf("attention = %#v", plan.Attention)
	}
	if !strings.Contains(plan.FinalCheck, "同一版草稿") {
		t.Fatalf("final check must require one stable draft, got %q", plan.FinalCheck)
	}
	for _, want := range []string{"人物声口", "自然句群", "场景连续", "仍像本书"} {
		if !strings.Contains(plan.FinalCheck, want) {
			t.Fatalf("final check must protect prose style with %q, got %q", want, plan.FinalCheck)
		}
	}
	if !strings.Contains(plan.Batches[0].Instruction, "相邻句群的呼吸") {
		t.Fatalf("punctuation guidance must preserve prose cadence: %q", plan.Batches[0].Instruction)
	}
	if !strings.Contains(plan.Batches[1].Instruction, "本书声纹") {
		t.Fatalf("expression guidance must preserve the book voice: %q", plan.Batches[1].Instruction)
	}
	for _, want := range []string{"自然虚词", "完整动作链"} {
		if !strings.Contains(plan.Batches[2].Instruction, want) {
			t.Fatalf("rhythm guidance must preserve %q: %q", want, plan.Batches[2].Instruction)
		}
	}
}

func TestRepairPlanForPassedReportDoesNotScheduleWork(t *testing.T) {
	plan := (Report{Findings: []Finding{{Code: "fragmented_paragraph_rhythm", Severity: SeverityAttention}}}).RepairPlan()
	if plan.Mode != "passed" || len(plan.Batches) != 0 {
		t.Fatalf("plan = %#v", plan)
	}
}
