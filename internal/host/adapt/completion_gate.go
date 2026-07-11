package adapt

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// CompletionGate adapts the existing deterministic adaptation audit to the
// narrow tools.CompletionGate boundary.
type CompletionGate struct {
	store *store.Store
}

func NewCompletionGate(st *store.Store) *CompletionGate {
	return &CompletionGate{store: st}
}

func (g *CompletionGate) EvaluateCompletion() (tools.CompletionAuditResult, error) {
	if g == nil || g.store == nil {
		return tools.CompletionAuditResult{}, fmt.Errorf("completion audit store is required")
	}
	if !g.store.Adaptation.Active() {
		return tools.CompletionAuditResult{Allowed: true, Status: "not_applicable"}, nil
	}
	if incomplete := incompleteAdaptationReason(g.store); incomplete != "" {
		return tools.CompletionAuditResult{
			Applicable: true, Allowed: false, Status: "incomplete", Warning: incomplete,
		}, nil
	}
	report, err := AuditProject(g.store, AuditOptions{Trigger: adaptaudit.AuditTriggerCompletion})
	if err != nil {
		return tools.CompletionAuditResult{}, err
	}
	if err := ProtectAuditReport(g.store, report, "completion"); err != nil {
		return tools.CompletionAuditResult{}, err
	}
	legacyWarning := report.Status == "inconclusive" && reportHasFindingCode(report.Findings, "audit_contract_unavailable")
	result := tools.CompletionAuditResult{
		Applicable:   true,
		Allowed:      report.Status == "pass" || legacyWarning,
		Status:       report.Status,
		ReportDigest: report.Digest,
	}
	if legacyWarning {
		result.Warning = "legacy adaptation contract is unavailable; completion is allowed with an inconclusive read-only warning and automatic repair remains disabled"
	}
	return result, nil
}

// ProtectAuditReport pins the just-created immutable run so retention cannot
// remove reports used for completion or publish decisions.
func ProtectAuditReport(st *store.Store, report *adaptaudit.Report, reason string) error {
	if st == nil || report == nil {
		return fmt.Errorf("audit report is required for protection")
	}
	run, err := st.Adaptation.LatestAuditRun()
	if err != nil {
		return fmt.Errorf("load latest audit run for protection: %w", err)
	}
	if run == nil || run.ReportDigest != report.Digest {
		return fmt.Errorf("latest audit run does not match report %s", report.Digest)
	}
	if err := st.Adaptation.ProtectAuditRun(run.RunID, reason); err != nil {
		return fmt.Errorf("protect audit run: %w", err)
	}
	return nil
}

func incompleteAdaptationReason(st *store.Store) string {
	plan, err := st.Adaptation.LoadPlan()
	if err != nil || plan == nil || len(plan.Chapters) == 0 {
		return "confirmed adaptation plan is required before completion"
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		return "project progress is required before completion"
	}
	completed := make(map[int]bool, len(progress.CompletedChapters))
	for _, chapter := range progress.CompletedChapters {
		completed[chapter] = true
	}
	for _, chapterPlan := range plan.Chapters {
		chapter := chapterPlan.Chapter
		if chapter <= 0 || !completed[chapter] {
			return fmt.Sprintf("adaptation chapter %d is not complete", chapter)
		}
		body, bodyErr := st.Drafts.LoadChapterText(chapter)
		if bodyErr != nil || strings.TrimSpace(body) == "" {
			return fmt.Sprintf("adaptation chapter %d final prose is missing or empty", chapter)
		}
	}
	return ""
}

func reportHasFindingCode(findings []adaptaudit.Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
