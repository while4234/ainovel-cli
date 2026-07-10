package adapt

import (
	"slices"
	"testing"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestAuditProjectArcAndConfirmedRepairQueue(t *testing.T) {
	const addedBody = "绑匪打来电话。"
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	meet := domain.AdaptationEvent{ID: "meet", Description: "百里冰遇劫，林逸飞出手相救并相识", Origin: domain.AdaptationEventOriginSource, Importance: domain.AdaptationEventMainline, SourceChapter: 13, Required: true}
	caseEvent := domain.AdaptationEvent{ID: "case", Description: "案件线索进入医院", Origin: domain.AdaptationEventOriginSource, Importance: domain.AdaptationEventMainline, SourceChapter: 14, Required: true}
	plan := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Status:        domain.AdaptationPlanStatusConfirmed,
		SourceEvents:  []domain.AdaptationEvent{meet, caseEvent},
		Volumes: []domain.AdaptationVolumePlan{{
			Index: 1, Title: "初遇与案件", TargetFrom: 1, TargetTo: 2, SourceFrom: 13, SourceTo: 14,
			MainlineEventIDs: []string{"meet", "case"},
		}},
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "案件", SourceChapters: []int{13, 14}, SourceRange: domain.SourceRange{From: 13, To: 14}, EventIDs: []string{"case"}},
			{Chapter: 2, Title: "绑架", SourceChapters: []int{13, 14}, SourceRange: domain.SourceRange{From: 13, To: 14}, AddedEventIDs: []string{"kidnap"}},
		},
	}
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "案件线索进入医院，众人追查病历。"); err != nil {
		t.Fatalf("SaveFinalChapter 1: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(2, addedBody); err != nil {
		t.Fatalf("SaveFinalChapter 2: %v", err)
	}
	if err := st.Adaptation.SaveCheck(domain.AdaptationCheck{
		Chapter: 1, DraftSHA256: store.TextSHA256("案件线索进入医院，众人追查病历。"), Passed: true,
		BodyEvidence: []domain.AdaptationBodyEvidence{{EventID: "case", Quote: "案件线索进入医院"}}, CheckedAt: "2026-07-10T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveCheck: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		NovelName: "audit", Phase: domain.PhaseWriting, Flow: domain.FlowWriting,
		TotalChapters: 2, CurrentChapter: 2, CompletedChapters: []int{1, 2},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	report, err := AuditProject(st, AuditOptions{SourceFrom: 13, SourceTo: 14, TargetFrom: 1, TargetTo: 2})
	if err != nil {
		t.Fatalf("AuditProject: %v", err)
	}
	for _, code := range []string{"missing_mainline_plan_binding", "missing_mainline_body_evidence", "added_event_displaces_mainline"} {
		if !reportHasFinding(report, code) {
			t.Fatalf("missing %s: %+v", code, report.Findings)
		}
	}
	if err := st.Drafts.SaveFinalChapter(2, addedBody+"审计后变化"); err != nil {
		t.Fatalf("modify chapter after audit: %v", err)
	}
	staleRequest := adaptaudit.ConfirmationRequest{
		ReportDigest: report.Digest, Decision: "apply",
		AcknowledgedFindingIDs: append([]string(nil), report.Confirmation.BlockingFindingIDs...),
	}
	if _, err := ApplyProjectAuditRepair(st, staleRequest); err == nil {
		t.Fatal("audit created before a draft change must be rejected as stale")
	}
	if err := st.Drafts.SaveFinalChapter(2, addedBody); err != nil {
		t.Fatalf("restore chapter after stale check: %v", err)
	}
	application, err := ApplyProjectAuditRepair(st, adaptaudit.ConfirmationRequest{
		ReportDigest: report.Digest, Decision: "apply",
		AcknowledgedFindingIDs: append([]string(nil), report.Confirmation.BlockingFindingIDs...),
	})
	if err != nil {
		t.Fatalf("ApplyProjectAuditRepair: %v", err)
	}
	if application.BackupPath == "" || !slices.Contains(application.QueuedChapters, 1) {
		t.Fatalf("application=%+v", application)
	}
	repaired, err := st.Adaptation.LoadPlan()
	if err != nil || repaired == nil {
		t.Fatalf("LoadPlan: %v plan=%+v", err, repaired)
	}
	if !slices.Contains(repaired.Chapters[0].EventIDs, "meet") || len(repaired.Chapters[0].RequiredChanges) == 0 {
		t.Fatalf("missing mainline event was not inserted into structured chapter duty: %+v", repaired.Chapters[0])
	}
}

func reportHasFinding(report *adaptaudit.Report, code string) bool {
	for _, finding := range report.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func TestMergeRewriteReasonPreservesExistingQueueContext(t *testing.T) {
	if got := mergeRewriteReason("manual chapter repair", "adaptation audit repair"); got != "manual chapter repair; adaptation audit repair" {
		t.Fatalf("mergeRewriteReason() = %q", got)
	}
	if got := mergeRewriteReason("manual; adaptation audit repair", "adaptation audit repair"); got != "manual; adaptation audit repair" {
		t.Fatalf("duplicate reason was not preserved: %q", got)
	}
}

func TestBestChapterForSourceEventUsesNarrativeEvidence(t *testing.T) {
	chapters := []domain.AdaptationChapterPlan{
		{Chapter: 1, Title: "新增追逐", SourceRange: domain.SourceRange{From: 1, To: 20}, AddedEventIDs: []string{"chase"}},
		{Chapter: 2, Title: "二人初遇", SourceRange: domain.SourceRange{From: 1, To: 20}},
	}
	event := domain.AdaptationEvent{SourceChapter: 13, Description: "百里冰与林逸飞初遇"}
	if got := bestChapterForSourceEvent(chapters, event); got != 1 {
		t.Fatalf("best chapter index=%d", got)
	}
}

func TestAuditProjectFreeUsesTargetRelationshipAndSettingContracts(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	body := "Their relationship becomes lovers, and magic is allowed."
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityFree, RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Status:                   domain.AdaptationPlanStatusConfirmed,
		TargetRelationshipStates: map[string]string{"hero|partner": "strangers"},
		TargetSettingLocks:       []domain.AdaptationSettingLock{{Key: "magic", Value: "forbidden"}},
		TargetEventLedger: []domain.AdaptationEvent{{
			ID: "love", Origin: domain.AdaptationEventOriginTarget, Description: "become lovers",
			Relationship: &domain.AdaptationRelationshipTransition{
				Pair: "hero|partner", From: "strangers", To: "lovers",
				AllowedFrom: []string{"trust"}, RequiresEventIDs: []string{"meet"},
			},
			SettingClaims: []domain.AdaptationSettingClaim{{Key: "magic", Value: "allowed"}},
		}},
		Chapters: []domain.AdaptationChapterPlan{{Chapter: 1, Title: "jump", EventIDs: []string{"love"}}},
	}
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(1, body); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := st.Adaptation.SaveCheck(domain.AdaptationCheck{
		Chapter: 1, DraftSHA256: store.TextSHA256(body), Passed: true,
		BodyEvidence: []domain.AdaptationBodyEvidence{{EventID: "love", Quote: "relationship becomes lovers, and magic is allowed"}},
	}); err != nil {
		t.Fatalf("SaveCheck: %v", err)
	}
	report, err := AuditProject(st, AuditOptions{})
	if err != nil {
		t.Fatalf("AuditProject: %v", err)
	}
	for _, code := range []string{"relationship_state_jump", "setting_lock_conflict"} {
		if !reportHasFinding(report, code) {
			t.Fatalf("missing %s: %+v", code, report.Findings)
		}
	}
}

func TestAuditProjectChapterRejectsLegacyLongChapterWithoutSegments(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		ChapterCount: 1, Chapters: []domain.AdaptationSource{{Chapter: 1, Runes: 10_000}},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	if err := st.Adaptation.SavePlan(domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter, Status: domain.AdaptationPlanStatusConfirmed,
		RewritePolicy: domain.AdaptationRewritePreserveDetails,
		Chapters:      []domain.AdaptationChapterPlan{{Chapter: 1, SourceChapters: []int{1}, SourceRange: domain.SourceRange{From: 1, To: 1}}},
	}); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	report, err := AuditProject(st, AuditOptions{})
	if err != nil {
		t.Fatalf("AuditProject: %v", err)
	}
	for _, code := range []string{"segment_contract_missing", "insufficient_segments"} {
		if !reportHasFinding(report, code) {
			t.Fatalf("missing %s: %+v", code, report.Findings)
		}
	}
}
