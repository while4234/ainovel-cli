package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestManuscriptRevisionPublishesSignedCandidateWithoutTouchingCurrentEarly(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	service := NewManuscriptRevisionServiceWithAuditor(st, passingManuscriptAuditor{})

	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "改善节奏", Kind: domain.ManuscriptInstructionPolish}, "preview-1")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if preview.Runtime.Mode != domain.RevisionModeNormal || preview.Runtime.Baseline.AdaptationPlanSHA256 != "" || preview.Runtime.Baseline.SourceManifestSHA256 != "" {
		t.Fatalf("normal baseline leaked adaptation fields: %+v", preview.Runtime.Baseline)
	}
	if current, _ := st.Drafts.LoadChapterText(1); current != "旧正文" {
		t.Fatalf("preview changed current prose: %q", current)
	}

	candidateProse := manuscriptContractFixtureProse(preview.Runtime.Baseline.NarrativeContract)
	candidate, err := service.SubmitCandidate(preview.Runtime.RevisionID, preview.Runtime.Revision, "candidate-1", ManuscriptCandidateInput{
		ChapterID: chapterID, Prose: candidateProse, Sidecars: completeManuscriptSidecars(),
	})
	if err != nil {
		t.Fatalf("SubmitCandidate: %v", err)
	}
	if current, _ := st.Drafts.LoadChapterText(1); current != "旧正文" {
		t.Fatalf("candidate generation changed current prose: %q", current)
	}
	audited, err := service.RunAudit(t.Context(), candidate.RevisionID, candidate.Revision, "audit-1")
	if err != nil {
		t.Fatalf("RecordAudit: %v", err)
	}
	if audited.Candidates[0].AuditArtifact == nil || audited.Candidates[0].AuditArtifact.Validate() != nil {
		t.Fatalf("signed content-addressed audit artifact = %+v", audited.Candidates[0].AuditArtifact)
	}
	approved, err := service.Approve(audited.RevisionID, audited.Revision, "approve-1")
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	completed, err := service.Publish(approved.RevisionID, approved.Revision, "publish-1")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if completed.Stage != "completed" || completed.PublicationStatus != domain.ManuscriptPublicationCompleted {
		t.Fatalf("completed runtime = %+v", completed)
	}
	if current, _ := st.Drafts.LoadChapterText(1); current != candidateProse {
		t.Fatalf("published prose = %q", current)
	}
	if summary, _ := st.Summaries.LoadSummary(1); summary == nil || summary.Summary != "新摘要" {
		t.Fatalf("published summary = %+v", summary)
	}
	if active, err := st.ManuscriptRevisions.Active(); err != nil || active != nil {
		t.Fatalf("active manuscript after completion = %+v err=%v", active, err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "externally changed after commit"); err != nil {
		t.Fatalf("external change: %v", err)
	}
	replayed, err := service.Publish(approved.RevisionID, approved.Revision, "publish-1")
	if err != nil || replayed.Stage != "completed" {
		t.Fatalf("publish receipt replay before stale/CAS checks = %+v err=%v", replayed, err)
	}
}

func TestFailedManuscriptAuditPersistsSignedReportWithoutApprovalSignature(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	service := NewManuscriptRevisionServiceWithAuditor(st, failingManuscriptAuditor{})
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "rewrite"}, "failed-audit-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	candidate, err := service.SubmitCandidate(preview.Runtime.RevisionID, preview.Runtime.Revision, "failed-audit-candidate", ManuscriptCandidateInput{ChapterID: chapterID, Prose: manuscriptContractFixtureProse(preview.Runtime.Baseline.NarrativeContract), Sidecars: completeManuscriptSidecars()})
	if err != nil {
		t.Fatalf("SubmitCandidate: %v", err)
	}
	audited, err := service.RunAudit(t.Context(), candidate.RevisionID, candidate.Revision, "failed-audit-run")
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}
	got := audited.Candidates[0]
	if audited.Stage != "failed" || got.AuditSignature != "" || got.AuditArtifact == nil || got.AuditArtifact.Validate() != nil {
		t.Fatalf("failed signed audit = stage=%q candidate=%+v", audited.Stage, got)
	}
}

func TestManuscriptAuditorErrorPersistsDurableSignedFinding(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	service := NewManuscriptRevisionServiceWithAuditor(st, errorManuscriptAuditor{})
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "rewrite"}, "auditor-error-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	candidate, err := service.SubmitCandidate(preview.Runtime.RevisionID, preview.Runtime.Revision, "auditor-error-candidate", ManuscriptCandidateInput{ChapterID: chapterID, Prose: manuscriptContractFixtureProse(preview.Runtime.Baseline.NarrativeContract), Sidecars: completeManuscriptSidecars()})
	if err != nil {
		t.Fatalf("SubmitCandidate: %v", err)
	}
	failed, err := service.RunAudit(t.Context(), candidate.RevisionID, candidate.Revision, "auditor-error-run")
	if err == nil || failed == nil || failed.Stage != "failed" || failed.LastErrorClass != "auditor_failure" || failed.Candidates[0].AuditArtifact == nil || failed.Candidates[0].AuditArtifact.Validate() != nil {
		t.Fatalf("durable auditor failure = %+v err=%v", failed, err)
	}
}

func TestManuscriptDeterministicEvidenceFailurePersistsSignedFinding(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	service := NewManuscriptRevisionServiceWithAuditor(st, passingManuscriptAuditor{})
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "rewrite"}, "evidence-error-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	candidate, err := service.SubmitCandidate(preview.Runtime.RevisionID, preview.Runtime.Revision, "evidence-error-candidate", ManuscriptCandidateInput{ChapterID: chapterID, Prose: manuscriptContractFixtureProse(preview.Runtime.Baseline.NarrativeContract), Sidecars: completeManuscriptSidecars()})
	if err != nil {
		t.Fatalf("SubmitCandidate: %v", err)
	}
	ref := candidate.Candidates[0].Prose
	path := filepath.Join(st.Dir(), "meta", "revisions", "content", "sha256", ref.SHA256[:2], ref.SHA256+".md")
	if err := os.WriteFile(path, []byte("tampered prose"), 0o644); err != nil {
		t.Fatalf("tamper prose: %v", err)
	}
	failed, err := service.RunAudit(t.Context(), candidate.RevisionID, candidate.Revision, "evidence-error-run")
	if err == nil || failed == nil || failed.Stage != "failed" || failed.LastErrorClass != "signature_drift" || failed.Candidates[0].AuditArtifact == nil || failed.Candidates[0].AuditArtifact.Validate() != nil {
		t.Fatalf("durable deterministic failure = %+v err=%v", failed, err)
	}
}

func TestManuscriptApprovalRereadsAuditContent(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	service := NewManuscriptRevisionServiceWithAuditor(st, passingManuscriptAuditor{})
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "rewrite"}, "tamper-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	candidate, err := service.SubmitCandidate(preview.Runtime.RevisionID, preview.Runtime.Revision, "tamper-candidate", ManuscriptCandidateInput{ChapterID: chapterID, Prose: manuscriptContractFixtureProse(preview.Runtime.Baseline.NarrativeContract), Sidecars: completeManuscriptSidecars()})
	if err != nil {
		t.Fatalf("SubmitCandidate: %v", err)
	}
	audited, err := service.RunAudit(t.Context(), candidate.RevisionID, candidate.Revision, "tamper-audit")
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}
	ref := audited.Candidates[0].AuditArtifact.Report
	path := filepath.Join(st.Dir(), "meta", "revisions", "content", "sha256", ref.SHA256[:2], ref.SHA256+".md")
	if err := os.WriteFile(path, []byte("tampered report"), 0o644); err != nil {
		t.Fatalf("tamper report: %v", err)
	}
	if _, err := service.Approve(audited.RevisionID, audited.Revision, "tamper-approve"); err == nil {
		t.Fatal("approval accepted tampered audit content")
	}
}

func TestManuscriptGenerationPersistsFailedAttemptAndRetriesOnlyFailedItem(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	writer := &retryingManuscriptWriter{}
	service := NewManuscriptRevisionServiceWithRuntime(st, writer, passingManuscriptAuditor{})
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "rewrite", Kind: domain.ManuscriptInstructionRewrite}, "generate-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	failed, err := service.GenerateCandidates(t.Context(), preview.Runtime.RevisionID, preview.Runtime.Revision, 1, "generate-first")
	if err == nil || failed == nil || failed.Stage != "failed" || failed.Queue[0].Attempt != 1 || failed.Queue[0].ErrorClass != "truncated_response" {
		t.Fatalf("first generation = %+v err=%v", failed, err)
	}
	generated, err := service.GenerateCandidates(t.Context(), failed.RevisionID, failed.Revision, 2, "generate-retry")
	if err != nil {
		t.Fatalf("retry GenerateCandidates: %v", err)
	}
	if generated.Stage != "audit_pending" || len(generated.Batches) != 3 || generated.Batches[2].ExpectedAttempt != 2 || len(generated.Batches[2].Receipts) != 2 || generated.Batches[2].SegmentPlan == nil || generated.Batches[2].SegmentPlan.Validate() != nil {
		t.Fatalf("retried generation runtime = %+v", generated)
	}
	if generated.Batches[2].Receipts[0].Attempt != 1 || generated.Batches[2].Receipts[0].Segment != 1 || generated.Batches[2].Receipts[1].Attempt != 2 || generated.Batches[2].Receipts[1].Segment != 2 || generated.Batches[2].Receipts[1].Content.SHA256 == "" {
		t.Fatalf("retry did not preserve only the compatible completed prefix: %+v", generated.Batches[2].Receipts)
	}
	if writer.calls != 3 {
		t.Fatalf("writer calls = %d, want 3 (two initial segments and only the failed retry segment)", writer.calls)
	}
}

type retryingManuscriptWriter struct{ calls int }

func (w *retryingManuscriptWriter) PlanManuscriptRevision(context.Context, domain.ManuscriptBaseline, string, domain.ManuscriptInstructionKind) (ManuscriptPlan, error) {
	return ManuscriptPlan{}, nil
}

func (w *retryingManuscriptWriter) GenerateManuscriptSegment(_ context.Context, runtime domain.ManuscriptRevisionRuntime, _ domain.ManuscriptReworkItem, _ ManuscriptGenerationContext, attempt, segment int, _ string) (ManuscriptGeneratedSegment, error) {
	w.calls++
	if attempt == 1 && segment == 1 {
		return ManuscriptGeneratedSegment{Prose: "first-", Complete: false}, nil
	}
	if attempt == 1 {
		return ManuscriptGeneratedSegment{Prose: "partial", Truncated: true}, nil
	}
	return ManuscriptGeneratedSegment{Prose: "second", Complete: true, Sidecars: manuscriptSidecarsForContract(runtime.Baseline.NarrativeContract)}, nil
}

func TestManuscriptGenerationClassifiesProviderFailure(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	service := NewManuscriptRevisionServiceWithRuntime(st, failingManuscriptWriter{}, passingManuscriptAuditor{})
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "rewrite"}, "provider-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	failed, err := service.GenerateCandidates(t.Context(), preview.Runtime.RevisionID, preview.Runtime.Revision, 1, "provider-generate")
	if err == nil || failed == nil || failed.LastErrorClass != "provider_error" {
		t.Fatalf("provider failure = %+v err=%v", failed, err)
	}
}

func TestManuscriptStableIDBatchGeneratesAndPublishesEachServerDerivedImpact(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	secondID := "ch_22222222222222222222222222222222"
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "one", CoreEvent: "one", Hook: "one"}, {ID: secondID, Chapter: 2, Title: "two", CoreEvent: "two", Hook: "two"}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(2, "old two"); err != nil {
		t.Fatalf("SaveFinalChapter 2: %v", err)
	}
	writer := &batchManuscriptWriter{impactedID: secondID}
	service := NewManuscriptRevisionServiceWithRuntime(st, writer, passingManuscriptAuditor{})
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "change both"}, "batch-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(preview.Runtime.Queue) != 2 || len(preview.Runtime.Queue[1].DependencySourceIDs) == 0 {
		t.Fatalf("server-derived queue = %+v", preview.Runtime.Queue)
	}
	if preview.Runtime.Queue[1].DependencyArtifact == nil || preview.Runtime.Queue[1].DependencyArtifact.Validate() != nil || preview.Runtime.Queue[1].ImpactConfirmed {
		t.Fatalf("unsigned or prematurely confirmed dependency artifact = %+v", preview.Runtime.Queue[1])
	}
	if len(preview.Runtime.Queue[1].DependencyArtifact.ContractDeltas) == 0 || preview.Runtime.Queue[1].DependencyArtifact.TargetBaselineSignature == "" {
		t.Fatalf("dependency artifact lacks real delta or target baseline: %+v", preview.Runtime.Queue[1].DependencyArtifact)
	}
	confirmed, err := service.ConfirmAdditionalImpacts(preview.Runtime.RevisionID, preview.Runtime.Revision, "batch-confirm")
	if err != nil {
		t.Fatalf("ConfirmAdditionalImpacts: %v", err)
	}
	first, err := service.GenerateCandidates(t.Context(), confirmed.RevisionID, confirmed.Revision, 1, "batch-first")
	if err != nil {
		t.Fatalf("generate first: %v", err)
	}
	if first.Stage != "candidate_generating" {
		t.Fatalf("first stage = %q", first.Stage)
	}
	second, err := service.GenerateCandidates(t.Context(), first.RevisionID, first.Revision, 1, "batch-second")
	if err != nil {
		t.Fatalf("generate second: %v", err)
	}
	if second.Stage != "audit_pending" || len(second.Candidates) != 2 {
		t.Fatalf("second generation = %+v", second)
	}
	for _, candidate := range second.Candidates {
		baseline, _, loadErr := service.CurrentChapter(candidate.ChapterID)
		if loadErr != nil {
			t.Fatalf("CurrentChapter(%s): %v", candidate.ChapterID, loadErr)
		}
		payload, _ := json.Marshal(baseline)
		if candidate.BaselineSignature != domain.ContentSignature(payload) {
			t.Fatalf("candidate %s declared another chapter baseline", candidate.ChapterID)
		}
	}
	audited, err := service.RunAudit(t.Context(), second.RevisionID, second.Revision, "batch-audit")
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}
	approved, err := service.Approve(audited.RevisionID, audited.Revision, "batch-approve")
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if _, err := service.Publish(approved.RevisionID, approved.Revision, "batch-publish"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if prose, _ := st.Drafts.LoadChapterText(1); prose != "new:"+chapterID {
		t.Fatalf("chapter one prose=%q", prose)
	}
	if prose, _ := st.Drafts.LoadChapterText(2); prose != "new:"+secondID {
		t.Fatalf("chapter two prose=%q", prose)
	}
}

func TestAdaptationManuscriptGenerationRequiresFreshServerReadEvidence(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	manifest := domain.AdaptationSourceManifest{SourcePath: "source", ChapterCount: 1, Chapters: []domain.AdaptationSource{{Chapter: 1, Title: "source", SHA256: domain.ContentSignature([]byte("source")), Path: "meta/adaptation/source_chapters/0001.md", Runes: 6}}}
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	plan := domain.AdaptationPlan{Granularity: domain.AdaptationGranularityChapter, RewritePolicy: domain.AdaptationRewriteFullRewrite, Brief: "adapt", Chapters: []domain.AdaptationChapterPlan{{OutlineEntry: domain.OutlineEntry{ID: chapterID, Chapter: 1, Title: "one", CoreEvent: "event", Hook: "hook"}, Chapter: 1, Title: "one", SourceChapters: []int{1}, CoverageNote: "covered"}}}
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	writer := &batchManuscriptWriter{}
	service := NewManuscriptRevisionServiceWithRuntime(st, writer, passingManuscriptAuditor{})
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "polish", Kind: domain.ManuscriptInstructionPolish}, "adapt-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if _, err := service.GenerateCandidates(t.Context(), preview.Runtime.RevisionID, preview.Runtime.Revision, 1, "adapt-generate"); err == nil || !strings.Contains(err.Error(), "AdaptationCheck") {
		t.Fatalf("missing adaptation evidence err=%v", err)
	}
}

func TestProductionAdaptationAuditorReceivesServerReadProsePlanSourceAndCheck(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	source, err := st.Adaptation.SaveSourceChapter(1, "source", "source")
	if err != nil {
		t.Fatalf("SaveSourceChapter: %v", err)
	}
	manifest := domain.AdaptationSourceManifest{SourcePath: "trusted-source", ChapterCount: 1, Chapters: []domain.AdaptationSource{source}}
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	plan := domain.AdaptationPlan{Granularity: domain.AdaptationGranularityChapter, RewritePolicy: domain.AdaptationRewriteFullRewrite, Brief: "trusted-plan", Chapters: []domain.AdaptationChapterPlan{{OutlineEntry: domain.OutlineEntry{ID: chapterID, Chapter: 1, Title: "one", CoreEvent: "event", Hook: "hook"}, Chapter: 1, Title: "one", SourceChapters: []int{1}, CoverageNote: "covered", ForbiddenMoves: []string{"forbidden resurrection"}}}}
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if err := st.Adaptation.SaveCheck(domain.AdaptationCheck{Chapter: 1, DraftSHA256: domain.ContentSignature([]byte("旧正文")), Passed: true, Summary: "trusted-check", CheckedAt: "now"}); err != nil {
		t.Fatalf("SaveCheck: %v", err)
	}
	capture := &captureManuscriptAuditModel{}
	auditor := &modelManuscriptAuditor{model: capture, prompts: assets.Load("default").Prompts, store: st}
	service := NewManuscriptRevisionServiceWithAuditor(st, auditor)
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "polish", Kind: domain.ManuscriptInstructionPolish}, "audit-evidence-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	candidateProse := manuscriptContractFixtureProse(preview.Runtime.Baseline.NarrativeContract) + "\nserver-generated adaptation prose"
	candidate, err := service.SubmitCandidate(preview.Runtime.RevisionID, preview.Runtime.Revision, "audit-evidence-candidate", ManuscriptCandidateInput{ChapterID: chapterID, Prose: candidateProse, Sidecars: completeManuscriptSidecars()})
	if err != nil {
		t.Fatalf("SubmitCandidate: %v", err)
	}
	audited, err := service.RunAudit(t.Context(), candidate.RevisionID, candidate.Revision, "audit-evidence-run")
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}
	if audited.Candidates[0].AdaptationCheck == nil || audited.Candidates[0].AdaptationCheck.DraftSHA256 != audited.Candidates[0].Prose.SHA256 {
		t.Fatalf("audit did not persist a candidate-bound adaptation check: %+v", audited.Candidates[0])
	}
	for _, required := range []string{"server-generated adaptation prose", "trusted-source", "trusted-plan", "trusted-check"} {
		if !strings.Contains(capture.prompt, required) {
			t.Fatalf("auditor prompt missing %q", required)
		}
	}
	for _, role := range []string{"contract_locator", "contract_verifier", "adaptation_locator", "adaptation_semantic_verifier", "whole_document_absence_verifier"} {
		if capture.roles[role] != 1 {
			t.Fatalf("auditor role %q calls = %d, want 1", role, capture.roles[role])
		}
	}
}

func TestManuscriptGenerationContextUsesServerEvidenceAndNormalSourceFirewall(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	if err := st.Outline.SavePremise("trusted foundation"); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	service := NewManuscriptRevisionService(st)
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "polish"}, "context-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	context, err := service.buildGenerationContext(*preview.Runtime, preview.Runtime.Queue[0])
	if err != nil {
		t.Fatalf("buildGenerationContext: %v", err)
	}
	if context.CurrentProse == "" || context.Foundation != "trusted foundation" || len(context.Contracts) < 2 || len(context.SourceSegments) != 0 || len(context.OwnedEvents) != 0 {
		t.Fatalf("normal generation context = %+v", context)
	}
}

func TestProductionManuscriptWriterBindsServerContextAndDerivesContract(t *testing.T) {
	model := &scriptedManuscriptModel{responses: []string{
		`{"story_changed":false,"outline":{"title":"changed","core_event":"new event","hook":"new hook"},"contract":{"result":"forged"}}`,
		`{"chapter_id":"ch_0123456789abcdef0123456789abcdef","attempt":1,"segment":1,"prose":"candidate prose","complete":false,"truncated":false}`,
	}}
	writer := &modelManuscriptWriter{model: model, prompts: assets.Load("default").Prompts}
	baseline := domain.ManuscriptBaseline{ChapterID: "ch_0123456789abcdef0123456789abcdef", DisplayChapter: 1, NarrativeContract: domain.NarrativeContract{ChapterID: "ch_0123456789abcdef0123456789abcdef", OutlineSHA256: "old", Desire: "old", Obstacle: "old", Choice: "old", Cost: "old", Result: "old", ExitState: "old", StateSHA256: "state"}}
	plan, err := writer.PlanManuscriptRevision(t.Context(), baseline, "change story", domain.ManuscriptInstructionRewrite)
	if err != nil {
		t.Fatalf("PlanManuscriptRevision: %v", err)
	}
	if !plan.StoryChanged || plan.Contract.Result != "complete new event" || plan.Contract.StateSHA256 != "state" {
		t.Fatalf("server-derived narrative contract = %+v", plan)
	}
	context := ManuscriptGenerationContext{Mode: domain.RevisionModeNormal, CurrentProse: "trusted current prose", Contracts: []string{"trusted outline"}, BudgetBytes: 100}
	if _, err := writer.GenerateManuscriptSegment(t.Context(), domain.ManuscriptRevisionRuntime{Mode: domain.RevisionModeNormal}, domain.ManuscriptReworkItem{ChapterID: baseline.ChapterID}, context, 1, 1, ""); err != nil {
		t.Fatalf("GenerateManuscriptSegment: %v", err)
	}
	if !strings.Contains(model.prompts[1], "trusted current prose") || !strings.Contains(model.prompts[1], "trusted outline") || strings.Contains(model.prompts[1], "source_segments") {
		t.Fatalf("production writer payload did not enforce server context/firewall: %s", model.prompts[1])
	}
}

type scriptedManuscriptModel struct {
	responses []string
	prompts   []string
}

func (m *scriptedManuscriptModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	var prompt strings.Builder
	for _, message := range messages {
		prompt.WriteString(message.TextContent())
	}
	m.prompts = append(m.prompts, prompt.String())
	response := m.responses[0]
	m.responses = m.responses[1:]
	return &agentcore.LLMResponse{Message: agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock(response)}}}, nil
}

func (*scriptedManuscriptModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, errors.New("unused")
}
func (*scriptedManuscriptModel) SupportsTools() bool { return false }

func TestManuscriptGenerationErrorClassesArePrecise(t *testing.T) {
	for message, want := range map[string]string{
		"status 401 unauthorized": "provider_auth", "rate limit quota": "provider_quota", "manuscript model returned an empty response": "empty_response",
		"decode manuscript segment: invalid character": "invalid_json", "missing segment 2": "missing_segment", "candidate signature drift": "signature_drift",
	} {
		if got := classifyManuscriptGenerationError(errors.New(message)); got != want {
			t.Fatalf("classify %q = %q, want %q", message, got, want)
		}
	}
}

func TestStructuredAdaptationDecisionRejectsCopiedBindingsAndSourceMismatch(t *testing.T) {
	prose := "主角主动交出钥匙并推开大门。雨水敲击窗户，行动发生在深夜。"
	task := ManuscriptAdaptationAuditTask{CandidateSHA256: domain.ContentSignature([]byte(prose)), SourceManifestSHA256: domain.ContentSignature([]byte("manifest")), AdaptationPlanSHA256: domain.ContentSignature([]byte("plan")), Events: map[string]string{"event-1": "主角交出钥匙"}, RequiredChanges: []string{"改为雨夜"}, ForbiddenMoves: []string{"禁止复活"}, Signature: domain.ContentSignature([]byte("task"))}
	valid := ManuscriptAdaptationAuditDecision{Passed: true, Report: "meaning verified from complete prose", TaskSignature: task.Signature, CandidateSHA256: task.CandidateSHA256, SourceManifestSHA256: task.SourceManifestSHA256, AdaptationPlanSHA256: task.AdaptationPlanSHA256, Findings: []ManuscriptAdaptationFinding{
		{Kind: "event", ID: "event-1", Verdict: "affirmed", Evidence: "主角主动交出钥匙并推开大门", StartRune: 0, EndRune: 13, SourceDescription: "主角交出钥匙"},
		{Kind: "change", ID: "改为雨夜", Verdict: "affirmed", Evidence: "雨水敲击窗户，行动发生在深夜", StartRune: 14, EndRune: 28},
	}, AbsenceReceipt: &ManuscriptWholeDocumentAbsenceReceipt{TaskSignature: task.Signature, CandidateSHA256: task.CandidateSHA256, ProseRunes: len([]rune(prose)), ForbiddenIDs: []string{"禁止复活"}}}
	task.Role = "adaptation_locator"
	valid.Role = "adaptation_locator"
	if err := validateAdaptationAuditDecision(task, valid, prose); err != nil {
		t.Fatalf("valid structured decision: %v", err)
	}
	for name, mutate := range map[string]func(*ManuscriptAdaptationAuditDecision){
		"copied candidate sha": func(value *ManuscriptAdaptationAuditDecision) {
			value.CandidateSHA256 = domain.ContentSignature([]byte("other"))
		},
		"source mismatch": func(value *ManuscriptAdaptationAuditDecision) {
			value.Findings[0].SourceDescription = "另一个事件"
		},
		"copied finding": func(value *ManuscriptAdaptationAuditDecision) {
			value.Findings[1].StartRune, value.Findings[1].EndRune, value.Findings[1].Evidence = value.Findings[0].StartRune, value.Findings[0].EndRune, value.Findings[0].Evidence
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Findings = append([]ManuscriptAdaptationFinding(nil), valid.Findings...)
			mutate(&candidate)
			if err := validateAdaptationAuditDecision(task, candidate, prose); err == nil {
				t.Fatal("invalid structured decision was accepted")
			}
		})
	}
}

func TestCandidateContractAuditRejectsContradictionAndUnboundEvidence(t *testing.T) {
	prose := "她追上列车，撞开封锁，选择公开证据，因此失去职位，最终救下证人并承担新的承诺。"
	entry := domain.OutlineEntry{ID: "ch_contract", Chapter: 1, Title: "追上列车", CoreEvent: "突破封锁", Hook: "承担新的承诺", Scenes: []string{"公开证据", "失去职位", "救下证人"}}
	state := manuscriptProtectedState{Character: strings.Repeat("a", 64), Relationship: strings.Repeat("b", 64), Timeline: strings.Repeat("c", 64), Foreshadow: strings.Repeat("d", 64)}
	task := newManuscriptContractAuditTask("revision", entry.ID, domain.ContentSignature([]byte(prose)), domain.ContentSignature([]byte("outline")), entry, state)
	expected := narrativeContractFromEntry(entry, nil)
	expected.OutlineSHA256 = task.OutlineSHA256
	expected.StateSHA256 = task.ProtectedStateSHA256
	valid := contractAuditDecisionForTest(task, prose)
	if err := validateManuscriptContractAuditDecision(task, valid, prose, expected); err != nil {
		t.Fatalf("valid contract decision: %v", err)
	}
	for name, mutate := range map[string]func(*ManuscriptContractAuditDecision){
		"conflicting prose contract": func(value *ManuscriptContractAuditDecision) { value.Contract.Result = "证人死亡" },
		"false candidate sha": func(value *ManuscriptContractAuditDecision) {
			value.CandidateSHA256 = domain.ContentSignature([]byte("other"))
		},
		"false location":          func(value *ManuscriptContractAuditDecision) { value.Evidence[0].StartRune++ },
		"missing protected field": func(value *ManuscriptContractAuditDecision) { value.Evidence = value.Evidence[1:] },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Evidence = append([]ManuscriptEvidenceLocation(nil), valid.Evidence...)
			mutate(&candidate)
			if err := validateManuscriptContractAuditDecision(task, candidate, prose, expected); err == nil {
				t.Fatal("invalid candidate contract evidence was accepted")
			}
		})
	}
}

func TestContractVerifierRejectsArbitraryOrReusedLocatorEvidence(t *testing.T) {
	prose := "abcdefghi"
	entry := domain.OutlineEntry{ID: "ch-contract-bypass", Chapter: 1, Title: "desire", CoreEvent: "obstacle", Hook: "exit", Scenes: []string{"choice", "cost", "result"}}
	state := manuscriptProtectedState{Character: strings.Repeat("a", 64), Relationship: strings.Repeat("b", 64), Timeline: strings.Repeat("c", 64), Foreshadow: strings.Repeat("d", 64)}
	locatorTask := newManuscriptContractAuditTask("revision", entry.ID, domain.ContentSignature([]byte(prose)), domain.JSONContentSignature([]byte(`{}`)), entry, state)
	approved := narrativeContractFromEntry(entry, nil)
	approved.OutlineSHA256 = locatorTask.OutlineSHA256
	approved.StateSHA256 = locatorTask.ProtectedStateSHA256
	locator := contractAuditDecisionForTest(locatorTask, prose)
	verifierTask := newManuscriptContractVerificationTask(locatorTask, locator)
	verification := contractVerificationDecisionForTest(verifierTask, locator, approved)
	verification.Receipts[0].Verdict = "contradicted"
	if err := validateManuscriptContractVerification(locatorTask, locator, verifierTask, verification, prose, approved); err == nil {
		t.Fatal("arbitrary locator evidence passed independent verifier")
	}
	locator.Evidence[1].StartRune, locator.Evidence[1].EndRune, locator.Evidence[1].Quote = locator.Evidence[0].StartRune, locator.Evidence[0].EndRune, locator.Evidence[0].Quote
	if err := validateManuscriptContractAuditDecision(locatorTask, locator, prose, approved); err == nil {
		t.Fatal("reused contract range was accepted")
	}
}

func TestAdaptationVerifierRejectsCorrectDescriptionWithUnrelatedQuote(t *testing.T) {
	prose := "sunny unrelated prose"
	locatorTask := ManuscriptAdaptationAuditTask{CandidateSHA256: domain.ContentSignature([]byte(prose)), SourceManifestSHA256: strings.Repeat("a", 64), AdaptationPlanSHA256: strings.Repeat("b", 64), Events: map[string]string{"event-1": "hero hands over the key"}, Role: "adaptation_locator", Signature: strings.Repeat("c", 64)}
	locator := ManuscriptAdaptationAuditDecision{Role: "adaptation_locator", Passed: true, Report: "located", TaskSignature: locatorTask.Signature, CandidateSHA256: locatorTask.CandidateSHA256, SourceManifestSHA256: locatorTask.SourceManifestSHA256, AdaptationPlanSHA256: locatorTask.AdaptationPlanSHA256, Findings: []ManuscriptAdaptationFinding{{Kind: "event", ID: "event-1", Verdict: "affirmed", SourceDescription: "hero hands over the key", Evidence: prose, StartRune: 0, EndRune: len([]rune(prose))}}}
	task := newAdaptationVerificationTask(locatorTask, locator)
	decision := ManuscriptAdaptationVerificationDecision{Role: "adaptation_semantic_verifier", TaskSignature: task.Signature, CandidateSHA256: task.CandidateSHA256, Receipts: []ManuscriptAdaptationVerificationReceipt{{Kind: "event", ID: "event-1", SourceDescription: "hero hands over the key", Quote: prose, StartRune: 0, EndRune: len([]rune(prose)), Verdict: "contradicted", TaskSignature: task.Signature, CandidateSHA256: task.CandidateSHA256}}}
	if err := validateAdaptationVerification(locatorTask, locator, task, decision, prose); err == nil {
		t.Fatal("unrelated quote passed semantic verifier")
	}
}

func TestWholeDocumentAbsenceRejectsFormalReceiptWhenForbiddenProseExists(t *testing.T) {
	prose := "the hero performs forbidden resurrection"
	locatorTask := ManuscriptAdaptationAuditTask{CandidateSHA256: domain.ContentSignature([]byte(prose)), ForbiddenMoves: []string{"forbidden resurrection"}, Role: "adaptation_locator", Signature: strings.Repeat("d", 64)}
	task := newWholeDocumentAbsenceTask(locatorTask, prose)
	receipt := ManuscriptWholeDocumentAbsenceReceipt{TaskSignature: task.Signature, CandidateSHA256: task.CandidateSHA256, ProseRunes: task.ProseRunes, ForbiddenIDs: append([]string(nil), task.ForbiddenIDs...)}
	if err := validateSeparateAbsenceReceipt(task, receipt, prose); err == nil {
		t.Fatal("formal absence assertion hid forbidden candidate prose")
	}
}

func TestCandidateEvidenceRereadsCurrentOutlineByStableID(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	service := NewManuscriptRevisionServiceWithAuditor(st, passingManuscriptAuditor{})
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "polish"}, "outline-cas-preview")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.SubmitCandidate(preview.Runtime.RevisionID, preview.Runtime.Revision, "outline-cas-candidate", ManuscriptCandidateInput{ChapterID: chapterID, Prose: manuscriptContractFixtureProse(preview.Runtime.Baseline.NarrativeContract), Sidecars: completeManuscriptSidecars()})
	if err != nil {
		t.Fatal(err)
	}
	outline, err := st.Outline.LoadOutline()
	if err != nil {
		t.Fatal(err)
	}
	outline[0].CoreEvent += " drift"
	payload, _ := json.MarshalIndent(outline, "", "  ")
	if err := os.WriteFile(filepath.Join(st.Dir(), "outline.json"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.validateCandidateBoundEvidence(*runtime, runtime.Candidates[0]); err == nil || !strings.Contains(err.Error(), "authoritative outline") {
		t.Fatalf("stored-old/current-drifted outline accepted: %v", err)
	}
}

func TestCandidateEvidenceLocationRejectsNegationMetadataLongQuoteAndFalseRange(t *testing.T) {
	long := strings.Repeat("证", 241)
	for name, test := range map[string]struct {
		prose string
		start int
		end   int
		quote string
	}{
		"post negation":  {prose: "主角交出钥匙，但这并非真实行动。", start: 0, end: 6, quote: "主角交出钥匙"},
		"metadata only":  {prose: "metadata: 主角交出钥匙", start: 10, end: 16, quote: "主角交出钥匙"},
		"long quotation": {prose: long, start: 0, end: 241, quote: long},
		"false range":    {prose: "主角交出钥匙", start: 1, end: 8, quote: "主角交出钥匙"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := rereadCandidateEvidence(test.prose, test.start, test.end, test.quote); err == nil {
				t.Fatal("unsafe candidate evidence was accepted")
			}
		})
	}
}

func TestManuscriptGenerationContextRejectsOversizeFormalProseWithoutTruncation(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	formal := strings.Repeat("完整正文", 20_000)
	if err := st.Drafts.SaveFinalChapter(1, formal); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	service := NewManuscriptRevisionService(st)
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "rewrite"}, "oversize-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	context, err := service.buildGenerationContext(*preview.Runtime, preview.Runtime.Queue[0])
	if err == nil || !strings.Contains(err.Error(), "60 KiB") {
		t.Fatalf("oversize context = %+v err=%v", context, err)
	}
	if context.CurrentProse != formal {
		t.Fatalf("formal prose was silently truncated: got=%d want=%d", len([]rune(context.CurrentProse)), len([]rune(formal)))
	}
}

func TestProductionWriterRejectsTotalCompiledPayloadAndSegmentIdentity(t *testing.T) {
	model := &scriptedManuscriptModel{responses: []string{`{"chapter_id":"wrong","attempt":1,"segment":1,"prose":"x","complete":false}`}}
	writer := &modelManuscriptWriter{model: model, prompts: assets.Load("default").Prompts}
	item := domain.ManuscriptReworkItem{ChapterID: "ch_expected"}
	context := ManuscriptGenerationContext{Mode: domain.RevisionModeNormal, CurrentProse: strings.Repeat("x", manuscriptCompiledRequestBudgetBytes), BudgetBytes: manuscriptCompiledRequestBudgetBytes}
	if _, err := writer.GenerateManuscriptSegment(t.Context(), domain.ManuscriptRevisionRuntime{}, item, context, 1, 1, ""); err == nil || !strings.Contains(err.Error(), "request_budget_exceeded") {
		t.Fatalf("compiled request budget err=%v", err)
	}
	context.CurrentProse, context.BudgetBytes = "short", 5
	if _, err := writer.GenerateManuscriptSegment(t.Context(), domain.ManuscriptRevisionRuntime{}, item, context, 1, 1, ""); err == nil || !strings.Contains(err.Error(), "segment_identity_mismatch") {
		t.Fatalf("segment identity err=%v", err)
	}
}

func TestManuscriptInvalidSidecarSchemaPersistsDurableFailure(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	service := NewManuscriptRevisionServiceWithRuntime(st, invalidSidecarManuscriptWriter{}, passingManuscriptAuditor{})
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "rewrite"}, "schema-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	failed, err := service.GenerateCandidates(t.Context(), preview.Runtime.RevisionID, preview.Runtime.Revision, 1, "schema-generate")
	if err == nil || failed == nil || failed.Stage != "failed" || failed.LastErrorClass != "invalid_schema" || failed.Queue[0].ErrorClass != "invalid_schema" {
		t.Fatalf("durable schema failure = %+v err=%v", failed, err)
	}
}

func TestNarrativeContractComparisonChecksEveryProtectedField(t *testing.T) {
	contract := domain.NarrativeContract{ChapterID: "ch_contract", OutlineSHA256: domain.ContentSignature([]byte("outline")), Desire: "d", Obstacle: "o", Choice: "c", Cost: "cost", Result: "r", ExitState: "e", StateSHA256: domain.ContentSignature([]byte("state"))}
	expected := newNarrativeContractArtifact(contract, domain.ContentSignature([]byte("old")), contract.OutlineSHA256)
	candidate := newNarrativeContractArtifact(contract, domain.ContentSignature([]byte("new")), contract.OutlineSHA256)
	candidate.ProtectedFields["timeline_state"] = "forged"
	unsigned := candidate
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	candidate.Signature = domain.ContentSignature(payload)
	if err := compareNarrativeContractArtifacts(expected, candidate); err == nil || !strings.Contains(err.Error(), "timeline_state") {
		t.Fatalf("protected comparison err=%v", err)
	}
}

func TestManuscriptRejectsWriterSuppliedNarrativeContract(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	service := NewManuscriptRevisionService(st)
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "polish"}, "contract-firewall-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	sidecars := completeManuscriptSidecars()
	sidecars["narrative_contract"] = json.RawMessage(`{"desire":"copied expected value","state_sha256":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}`)
	if _, err := service.SubmitCandidate(preview.Runtime.RevisionID, preview.Runtime.Revision, "contract-firewall-candidate", ManuscriptCandidateInput{ChapterID: chapterID, Prose: "candidate", Sidecars: sidecars}); err == nil || !strings.Contains(err.Error(), "writer-supplied narrative_contract is forbidden") {
		t.Fatalf("writer contract firewall err=%v", err)
	}
}

type invalidSidecarManuscriptWriter struct{}

func (invalidSidecarManuscriptWriter) PlanManuscriptRevision(context.Context, domain.ManuscriptBaseline, string, domain.ManuscriptInstructionKind) (ManuscriptPlan, error) {
	return ManuscriptPlan{}, nil
}
func (invalidSidecarManuscriptWriter) GenerateManuscriptSegment(_ context.Context, _ domain.ManuscriptRevisionRuntime, item domain.ManuscriptReworkItem, _ ManuscriptGenerationContext, attempt, segment int, _ string) (ManuscriptGeneratedSegment, error) {
	return ManuscriptGeneratedSegment{ChapterID: item.ChapterID, Attempt: attempt, Segment: segment, Prose: "candidate", Complete: true, Sidecars: map[string]json.RawMessage{}}, nil
}

func TestManuscriptRejectsEmptySemanticSidecars(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	service := NewManuscriptRevisionService(st)
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "polish"}, "empty-sidecar-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	sidecars := completeManuscriptSidecars()
	sidecars["events"] = json.RawMessage(`[]`)
	if _, err := service.SubmitCandidate(preview.Runtime.RevisionID, preview.Runtime.Revision, "empty-sidecar-candidate", ManuscriptCandidateInput{ChapterID: chapterID, Prose: "candidate", Sidecars: sidecars}); err == nil || !strings.Contains(err.Error(), "semantic sidecar events is empty") {
		t.Fatalf("empty semantic sidecar err=%v", err)
	}
}

type captureManuscriptAuditModel struct {
	prompt string
	roles  map[string]int
}

func (m *captureManuscriptAuditModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	for _, message := range messages {
		m.prompt += message.TextContent()
	}
	payload := []byte(messages[len(messages)-1].TextContent())
	var requestRole struct {
		Task struct {
			Role string `json:"role"`
		} `json:"task"`
	}
	_ = json.Unmarshal(payload, &requestRole)
	if requestRole.Task.Role != "" {
		if m.roles == nil {
			m.roles = make(map[string]int)
		}
		m.roles[requestRole.Task.Role]++
	}

	var responsePayload any
	switch requestRole.Task.Role {
	case "contract_locator":
		var request struct {
			Task  ManuscriptContractAuditTask `json:"task"`
			Prose string                      `json:"complete_candidate_prose"`
		}
		_ = json.Unmarshal(payload, &request)
		responsePayload = contractAuditDecisionForTest(request.Task, request.Prose)
	case "contract_verifier":
		var request struct {
			Task     ManuscriptContractVerificationTask `json:"task"`
			Locator  ManuscriptContractAuditDecision    `json:"locator"`
			Approved domain.NarrativeContract           `json:"approved_contract"`
		}
		_ = json.Unmarshal(payload, &request)
		responsePayload = contractVerificationDecisionForTest(request.Task, request.Locator, request.Approved)
	case "adaptation_locator":
		var request struct {
			Task ManuscriptAdaptationAuditTask `json:"task"`
		}
		_ = json.Unmarshal(payload, &request)
		responsePayload = ManuscriptAdaptationAuditDecision{Role: "adaptation_locator", Passed: true, Report: "trusted independent adaptation locator", TaskSignature: request.Task.Signature, CandidateSHA256: request.Task.CandidateSHA256, SourceManifestSHA256: request.Task.SourceManifestSHA256, AdaptationPlanSHA256: request.Task.AdaptationPlanSHA256}
	case "adaptation_semantic_verifier":
		var request struct {
			Task    ManuscriptAdaptationVerificationTask `json:"task"`
			Locator ManuscriptAdaptationAuditDecision    `json:"locator"`
		}
		_ = json.Unmarshal(payload, &request)
		receipts := make([]ManuscriptAdaptationVerificationReceipt, 0, len(request.Locator.Findings))
		for _, finding := range request.Locator.Findings {
			if finding.Verdict != "affirmed" {
				continue
			}
			receipts = append(receipts, ManuscriptAdaptationVerificationReceipt{Kind: finding.Kind, ID: finding.ID, SourceDescription: finding.SourceDescription, StartRune: finding.StartRune, EndRune: finding.EndRune, Quote: finding.Evidence, Verdict: "entailed", TaskSignature: request.Task.Signature, CandidateSHA256: request.Task.CandidateSHA256})
		}
		responsePayload = ManuscriptAdaptationVerificationDecision{Role: "adaptation_semantic_verifier", TaskSignature: request.Task.Signature, CandidateSHA256: request.Task.CandidateSHA256, Receipts: receipts}
	case "whole_document_absence_verifier":
		var request struct {
			Task ManuscriptWholeDocumentAbsenceTask `json:"task"`
		}
		_ = json.Unmarshal(payload, &request)
		responsePayload = ManuscriptWholeDocumentAbsenceReceipt{TaskSignature: request.Task.Signature, CandidateSHA256: request.Task.CandidateSHA256, ProseRunes: request.Task.ProseRunes, ForbiddenIDs: append([]string(nil), request.Task.ForbiddenIDs...)}
	default:
		responsePayload = struct {
			Passed bool   `json:"passed"`
			Report string `json:"report"`
		}{Passed: true, Report: "trusted independent audit"}
	}
	response, _ := json.Marshal(responsePayload)
	return &agentcore.LLMResponse{Message: agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock(string(response))}}}, nil
}
func (*captureManuscriptAuditModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, errors.New("unused")
}
func (*captureManuscriptAuditModel) SupportsTools() bool { return false }

type batchManuscriptWriter struct{ impactedID string }

func (w *batchManuscriptWriter) PlanManuscriptRevision(_ context.Context, baseline domain.ManuscriptBaseline, _ string, _ domain.ManuscriptInstructionKind) (ManuscriptPlan, error) {
	if w.impactedID == "" {
		return ManuscriptPlan{}, nil
	}
	contract := baseline.NarrativeContract
	contract.Result = "server-derived changed result"
	return ManuscriptPlan{StoryChanged: true, Outline: domain.OutlineEntry{ID: baseline.ChapterID, Chapter: baseline.DisplayChapter, Title: "one", CoreEvent: contract.Result, Hook: contract.ExitState}, Contract: contract, ImpactedChapterIDs: []string{w.impactedID}}, nil
}
func (w *batchManuscriptWriter) GenerateManuscriptSegment(_ context.Context, runtime domain.ManuscriptRevisionRuntime, item domain.ManuscriptReworkItem, _ ManuscriptGenerationContext, _, _ int, _ string) (ManuscriptGeneratedSegment, error) {
	contract := runtime.Baseline.NarrativeContract
	if item.ChapterID == runtime.Baseline.ChapterID && runtime.OutlinePreview != nil {
		contract = runtime.OutlinePreview.Contract
	} else if item.ChapterID != runtime.Baseline.ChapterID {
		contract = domain.NarrativeContract{Desire: "two", Obstacle: "two", Choice: "two", Cost: "two", Result: "two", ExitState: "two"}
	}
	return ManuscriptGeneratedSegment{Prose: "new:" + item.ChapterID, Complete: true, Sidecars: manuscriptSidecarsForContract(contract)}, nil
}

type failingManuscriptWriter struct{}

func (failingManuscriptWriter) PlanManuscriptRevision(context.Context, domain.ManuscriptBaseline, string, domain.ManuscriptInstructionKind) (ManuscriptPlan, error) {
	return ManuscriptPlan{}, nil
}
func (failingManuscriptWriter) GenerateManuscriptSegment(context.Context, domain.ManuscriptRevisionRuntime, domain.ManuscriptReworkItem, ManuscriptGenerationContext, int, int, string) (ManuscriptGeneratedSegment, error) {
	return ManuscriptGeneratedSegment{}, errors.New("provider unavailable")
}

type passingManuscriptAuditor struct{}

func (passingManuscriptAuditor) AuditCandidateContract(_ context.Context, task ManuscriptContractAuditTask, prose string) (ManuscriptContractAuditDecision, error) {
	return contractAuditDecisionForTest(task, prose), nil
}

func (passingManuscriptAuditor) VerifyCandidateContract(_ context.Context, task ManuscriptContractVerificationTask, locator ManuscriptContractAuditDecision, approved domain.NarrativeContract, _ string) (ManuscriptContractVerificationDecision, error) {
	return contractVerificationDecisionForTest(task, locator, approved), nil
}

func (passingManuscriptAuditor) AuditManuscriptCandidate(_ context.Context, _ domain.ManuscriptRevisionRuntime, _ domain.ManuscriptCandidate) (bool, string, error) {
	return true, "independent audit passed", nil
}

type failingManuscriptAuditor struct{}

func (failingManuscriptAuditor) AuditCandidateContract(_ context.Context, task ManuscriptContractAuditTask, prose string) (ManuscriptContractAuditDecision, error) {
	return contractAuditDecisionForTest(task, prose), nil
}
func (failingManuscriptAuditor) VerifyCandidateContract(_ context.Context, task ManuscriptContractVerificationTask, locator ManuscriptContractAuditDecision, approved domain.NarrativeContract, _ string) (ManuscriptContractVerificationDecision, error) {
	return contractVerificationDecisionForTest(task, locator, approved), nil
}

func (failingManuscriptAuditor) AuditManuscriptCandidate(_ context.Context, _ domain.ManuscriptRevisionRuntime, _ domain.ManuscriptCandidate) (bool, string, error) {
	return false, "blocking finding", nil
}

type errorManuscriptAuditor struct{}

func (errorManuscriptAuditor) AuditCandidateContract(_ context.Context, task ManuscriptContractAuditTask, prose string) (ManuscriptContractAuditDecision, error) {
	return contractAuditDecisionForTest(task, prose), nil
}
func (errorManuscriptAuditor) VerifyCandidateContract(_ context.Context, task ManuscriptContractVerificationTask, locator ManuscriptContractAuditDecision, approved domain.NarrativeContract, _ string) (ManuscriptContractVerificationDecision, error) {
	return contractVerificationDecisionForTest(task, locator, approved), nil
}

func contractAuditDecisionForTest(task ManuscriptContractAuditTask, prose string) ManuscriptContractAuditDecision {
	contract := narrativeContractFromEntry(task.Outline, nil)
	contract.ChapterID = task.ChapterID
	contract.OutlineSHA256 = task.OutlineSHA256
	contract.StateSHA256 = task.ProtectedStateSHA256
	clauses := manuscriptContractFixtureClauses(contract)
	evidence := make([]ManuscriptEvidenceLocation, 0, len(clauses))
	searchByte := 0
	for _, clause := range clauses {
		relative := strings.Index(prose[searchByte:], clause.text)
		if relative < 0 {
			return contractAuditDecisionFromDistinctRunes(task, contract, prose)
		}
		startByte := searchByte + relative
		endByte := startByte + len(clause.text)
		evidence = append(evidence, ManuscriptEvidenceLocation{Field: clause.field, StartRune: len([]rune(prose[:startByte])), EndRune: len([]rune(prose[:endByte])), Quote: clause.text})
		searchByte = endByte
	}
	return ManuscriptContractAuditDecision{Role: "contract_locator", TaskSignature: task.Signature, CandidateSHA256: task.CandidateSHA256, Contract: contract, Evidence: evidence}
}

func contractAuditDecisionFromDistinctRunes(task ManuscriptContractAuditTask, contract domain.NarrativeContract, prose string) ManuscriptContractAuditDecision {
	runes := []rune(prose)
	positions := make([]int, 0, len(runes))
	for index, value := range runes {
		if !strings.ContainsRune(" \t\r\n", value) {
			positions = append(positions, index)
		}
	}
	fields := []string{"desire", "obstacle", "choice", "cost", "result", "exit_state", "future_commitments"}
	decision := ManuscriptContractAuditDecision{Role: "contract_locator", TaskSignature: task.Signature, CandidateSHA256: task.CandidateSHA256, Contract: contract}
	if len(positions) < len(fields) {
		return decision
	}
	decision.Evidence = make([]ManuscriptEvidenceLocation, 0, len(fields))
	for index, field := range fields {
		position := positions[index]
		decision.Evidence = append(decision.Evidence, ManuscriptEvidenceLocation{Field: field, StartRune: position, EndRune: position + 1, Quote: string(runes[position : position+1])})
	}
	return decision
}

type manuscriptContractFixtureClause struct {
	field string
	text  string
}

func manuscriptContractFixtureClauses(contract domain.NarrativeContract) []manuscriptContractFixtureClause {
	return []manuscriptContractFixtureClause{
		{field: "desire", text: "Desire is " + contract.Desire + "."},
		{field: "obstacle", text: "Obstacle is " + contract.Obstacle + "."},
		{field: "choice", text: "The choice is " + contract.Choice + "."},
		{field: "cost", text: "The cost is " + contract.Cost + "."},
		{field: "result", text: "The result is " + contract.Result + "."},
		{field: "exit_state", text: "The exit state is " + contract.ExitState + "."},
		{field: "future_commitments", text: "Future commitments are " + strings.Join(contract.FutureCommitments, "\n") + "."},
	}
}

func manuscriptContractFixtureProse(contract domain.NarrativeContract) string {
	clauses := manuscriptContractFixtureClauses(contract)
	parts := make([]string, 0, len(clauses))
	for _, clause := range clauses {
		parts = append(parts, clause.text)
	}
	return strings.Join(parts, "\n")
}

func contractVerificationDecisionForTest(task ManuscriptContractVerificationTask, locator ManuscriptContractAuditDecision, approved domain.NarrativeContract) ManuscriptContractVerificationDecision {
	receipts := make([]ManuscriptContractVerificationReceipt, 0, len(locator.Evidence))
	for _, location := range locator.Evidence {
		receipts = append(receipts, ManuscriptContractVerificationReceipt{Field: location.Field, Value: narrativeContractReceiptValue(locator.Contract, location.Field), ApprovedValue: narrativeContractReceiptValue(approved, location.Field), StartRune: location.StartRune, EndRune: location.EndRune, Quote: location.Quote, Verdict: "entailed", TaskSignature: task.Signature, CandidateSHA256: task.CandidateSHA256})
	}
	return ManuscriptContractVerificationDecision{Role: "contract_verifier", TaskSignature: task.Signature, CandidateSHA256: task.CandidateSHA256, Receipts: receipts}
}

func (errorManuscriptAuditor) AuditManuscriptCandidate(_ context.Context, _ domain.ManuscriptRevisionRuntime, _ domain.ManuscriptCandidate) (bool, string, error) {
	return false, "", errors.New("auditor unavailable")
}

func TestManuscriptPolishContractChangeEscalatesAndCancelPreservesCurrent(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	service := NewManuscriptRevisionServiceWithRuntime(st, storyChangingManuscriptWriter{}, nil)
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "改变结局", Kind: domain.ManuscriptInstructionPolish}, "preview-escalate")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !preview.Escalated || preview.Runtime.InstructionKind != domain.ManuscriptInstructionOutlineRevision {
		t.Fatalf("contract change did not escalate: %+v", preview)
	}
	if _, err := service.Cancel(preview.Runtime.RevisionID, preview.Runtime.Revision, "cancel-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if current, _ := st.Drafts.LoadChapterText(1); current != "旧正文" {
		t.Fatalf("cancel changed current prose: %q", current)
	}
}

type storyChangingManuscriptWriter struct{}

func (storyChangingManuscriptWriter) PlanManuscriptRevision(_ context.Context, baseline domain.ManuscriptBaseline, _ string, _ domain.ManuscriptInstructionKind) (ManuscriptPlan, error) {
	contract := baseline.NarrativeContract
	contract.Result = "不同结局"
	return ManuscriptPlan{StoryChanged: true, Outline: domain.OutlineEntry{ID: baseline.ChapterID, Chapter: baseline.DisplayChapter, Title: "修订提纲", CoreEvent: "不同结局", Hook: "新钩子"}, Contract: contract}, nil
}

func (storyChangingManuscriptWriter) GenerateManuscriptSegment(context.Context, domain.ManuscriptRevisionRuntime, domain.ManuscriptReworkItem, ManuscriptGenerationContext, int, int, string) (ManuscriptGeneratedSegment, error) {
	return ManuscriptGeneratedSegment{}, nil
}

func TestManuscriptPublicationFailureRollsBackCurrentAndDerivedState(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	service := NewManuscriptRevisionServiceWithAuditor(st, passingManuscriptAuditor{})
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "rewrite", Kind: domain.ManuscriptInstructionRewrite}, "rollback-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	sidecars := completeManuscriptSidecars()
	sidecars["summary"] = json.RawMessage(`[1]`)
	candidate, err := service.SubmitCandidate(preview.Runtime.RevisionID, preview.Runtime.Revision, "rollback-candidate", ManuscriptCandidateInput{ChapterID: chapterID, Prose: manuscriptContractFixtureProse(preview.Runtime.Baseline.NarrativeContract), Sidecars: sidecars})
	if err != nil {
		t.Fatalf("SubmitCandidate: %v", err)
	}
	audited, err := service.RunAudit(t.Context(), candidate.RevisionID, candidate.Revision, "rollback-audit")
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}
	approved, err := service.Approve(audited.RevisionID, audited.Revision, "rollback-approve")
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if _, err := service.Publish(approved.RevisionID, approved.Revision, "rollback-publish"); err == nil {
		t.Fatal("expected malformed summary publication to fail")
	}
	if prose, _ := st.Drafts.LoadChapterText(1); prose != "旧正文" {
		t.Fatalf("publication failure left prose=%q", prose)
	}
	if summary, _ := st.Summaries.LoadSummary(1); summary == nil || summary.Summary != "旧摘要" {
		t.Fatalf("publication failure left summary=%+v", summary)
	}
}

func TestCompleteManuscriptPublicationPersistsRevalidationBeforeReleasingOwner(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	if err := st.Progress.UpdatePhase(domain.PhaseComplete); err != nil {
		t.Fatalf("UpdatePhase complete: %v", err)
	}
	service := NewManuscriptRevisionServiceWithAuditor(st, passingManuscriptAuditor{})
	approved := prepareApprovedManuscriptCandidate(t, service, chapterID, "complete-ok")
	completed, err := service.Publish(approved.RevisionID, approved.Revision, "complete-ok-publish")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if completed.Stage != "completed" || completed.CompletionRevalidationStatus != "completed" || completed.CompletionRevalidationSignature == "" {
		t.Fatalf("completion revalidation = %+v", completed)
	}
	if active, err := st.ManuscriptRevisions.Active(); err != nil || active != nil {
		t.Fatalf("active after revalidation = %+v err=%v", active, err)
	}
}

func TestCompleteManuscriptPublicationKeepsOwnerWhenRevalidationFails(t *testing.T) {
	st, chapterID := seedManuscriptRevisionProject(t)
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "one", CoreEvent: "event", Hook: "hook"}, {ID: "ch_11111111111111111111111111111111", Chapter: 2, Title: "missing", CoreEvent: "missing", Hook: "missing"}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseComplete); err != nil {
		t.Fatalf("UpdatePhase complete: %v", err)
	}
	service := NewManuscriptRevisionServiceWithAuditor(st, passingManuscriptAuditor{})
	approved := prepareApprovedManuscriptCandidate(t, service, chapterID, "complete-fail")
	if _, err := service.Publish(approved.RevisionID, approved.Revision, "complete-fail-publish"); err == nil {
		t.Fatal("expected completion revalidation failure")
	}
	active, err := st.ManuscriptRevisions.Active()
	if err != nil || active == nil || active.Stage != "completion_revalidation_pending" {
		t.Fatalf("active pending revalidation = %+v err=%v", active, err)
	}
}

func prepareApprovedManuscriptCandidate(t *testing.T, service *ManuscriptRevisionService, chapterID, prefix string) *domain.ManuscriptRevisionRuntime {
	t.Helper()
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "rewrite"}, prefix+"-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	candidate, err := service.SubmitCandidate(preview.Runtime.RevisionID, preview.Runtime.Revision, prefix+"-candidate", ManuscriptCandidateInput{ChapterID: chapterID, Prose: manuscriptContractFixtureProse(preview.Runtime.Baseline.NarrativeContract), Sidecars: completeManuscriptSidecars()})
	if err != nil {
		t.Fatalf("SubmitCandidate: %v", err)
	}
	audited, err := service.RunAudit(t.Context(), candidate.RevisionID, candidate.Revision, prefix+"-audit")
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}
	approved, err := service.Approve(audited.RevisionID, audited.Revision, prefix+"-approve")
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	return approved
}

func seedManuscriptRevisionProject(t *testing.T) (*storepkg.Store, string) {
	t.Helper()
	return seedManuscriptRevisionProjectAt(t, t.TempDir())
}

func seedManuscriptRevisionProjectAt(t *testing.T, root string) (*storepkg.Store, string) {
	t.Helper()
	st := storepkg.NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	chapterID := "ch_0123456789abcdef0123456789abcdef"
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "第一章", CoreEvent: "主角作出选择", Hook: "代价显现", Scenes: []string{"兑现承诺"}}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Progress.Init("测试书", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("UpdatePhase: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "旧正文"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := st.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 1, Summary: "旧摘要"}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	return st, chapterID
}

func completeManuscriptSidecars() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"summary":       json.RawMessage(`{"chapter":1,"summary":"新摘要","characters":[],"key_events":[]}`),
		"events":        json.RawMessage(`["event"]`),
		"timeline":      json.RawMessage(`[{"event":"event"}]`),
		"cast_state":    json.RawMessage(`[{"entity":"hero","field":"status","new_value":"changed"}]`),
		"relationships": json.RawMessage(`[{"character_a":"hero","character_b":"ally","relation":"trusted"}]`),
		"foreshadow":    json.RawMessage(`[{"id":"seed","description":"seed","status":"planted"}]`),
		"world_facts":   json.RawMessage(`[{"category":"other","rule":"rule","boundary":"boundary"}]`),
		"carry_forward": json.RawMessage(`{"character_snapshots":[{"name":"hero","status":"ready","motivation":"act"}]}`),
	}
}

func manuscriptSidecarsForContract(contract domain.NarrativeContract) map[string]json.RawMessage {
	_ = contract
	return completeManuscriptSidecars()
}
