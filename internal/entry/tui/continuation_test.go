package tui

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestParseContinuationCommand(t *testing.T) {
	tests := []struct {
		args        []string
		wantAction  string
		wantPrompt  string
		wantFailure bool
	}{
		{wantAction: "status"},
		{args: []string{"show"}, wantAction: "status"},
		{args: []string{"approve"}, wantAction: "approve"},
		{args: []string{"revise", "加强", "冲突"}, wantAction: "revise", wantPrompt: "加强 冲突"},
		{args: []string{"revise"}, wantFailure: true},
		{args: []string{"unknown"}, wantFailure: true},
	}
	for _, test := range tests {
		action, prompt, err := parseContinuationCommand(test.args)
		if (err != nil) != test.wantFailure {
			t.Fatalf("args=%v err=%v wantFailure=%v", test.args, err, test.wantFailure)
		}
		if action != test.wantAction || prompt != test.wantPrompt {
			t.Fatalf("args=%v got (%q, %q), want (%q, %q)", test.args, action, prompt, test.wantAction, test.wantPrompt)
		}
	}
}

func TestFormatContinuationSnapshot(t *testing.T) {
	snapshot := &domain.ContinuationSnapshot{
		Workflow: domain.ContinuationWorkflow{Stage: domain.ContinuationStageProposalReviewPending, BaseChapterCount: 10, Revision: 4, Draft: "向北追凶"},
		Proposal: &domain.ContinuationProposal{Summary: "追查旧案", Direction: "北境", Structure: domain.ContinuationStructureSingle, TargetChapterCount: 2},
	}
	got := formatContinuationSnapshot(snapshot)
	for _, want := range []string{"proposal_review_pending", "1-10", "向北追凶", "追查旧案", "计划 2 章"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted snapshot missing %q: %s", want, got)
		}
	}
}
