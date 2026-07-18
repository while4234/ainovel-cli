package agents

import (
	"encoding/json"
	"testing"
)

func TestWriterShouldStopAfterBudgetRecoveryToolResult(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		result   string
		want     bool
	}{
		{name: "completed segment", toolName: "edit_chapter", result: `{"budget_segment":0,"changed":true}`, want: true},
		{name: "deferred read", toolName: "read_chapter", result: `{"deferred_to_host":true}`, want: true},
		{name: "deferred repair", toolName: "repair_de_ai_batch", result: `{"deferred_to_host":true}`, want: true},
		{name: "ordinary edit", toolName: "edit_chapter", result: `{"changed":true}`, want: false},
		{name: "unrelated segment field", toolName: "read_chapter", result: `{"budget_segment":2}`, want: false},
		{name: "invalid payload", toolName: "edit_chapter", result: `{`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := writerShouldStopAfterToolResult(tc.toolName, json.RawMessage(tc.result)); got != tc.want {
				t.Fatalf("writerShouldStopAfterToolResult() = %v, want %v", got, tc.want)
			}
		})
	}
}
