package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

const manuscriptCompiledRequestBudgetBytes = 96 * 1024

type modelManuscriptWriter struct {
	model   agentcore.ChatModel
	prompts assets.Prompts
}

type modelManuscriptAuditor struct {
	model              agentcore.ChatModel
	contractLocator    agentcore.ChatModel
	contractVerifier   agentcore.ChatModel
	adaptationLocator  agentcore.ChatModel
	adaptationVerifier agentcore.ChatModel
	absenceVerifier    agentcore.ChatModel
	prompts            assets.Prompts
	store              *storepkg.Store
}

func (h *Host) ManuscriptRevisionService() *ManuscriptRevisionService {
	if h == nil || h.store == nil || h.models == nil {
		return nil
	}
	writer := &modelManuscriptWriter{model: h.models.ForStageWithFailover(bootstrap.StageWriting, nil), prompts: h.bundle.Prompts}
	auditor := &modelManuscriptAuditor{model: h.models.ForRoleWithFailover("auditor", nil), contractLocator: h.models.ForRoleWithFailover("contract_locator", nil), contractVerifier: h.models.ForRoleWithFailover("contract_verifier", nil), adaptationLocator: h.models.ForRoleWithFailover("adaptation_locator", nil), adaptationVerifier: h.models.ForRoleWithFailover("adaptation_semantic_verifier", nil), absenceVerifier: h.models.ForRoleWithFailover("whole_document_absence_verifier", nil), prompts: h.bundle.Prompts, store: h.store}
	return NewManuscriptRevisionServiceWithRuntime(h.store, writer, auditor)
}

func (w *modelManuscriptWriter) PlanManuscriptRevision(ctx context.Context, baseline domain.ManuscriptBaseline, instruction string, kind domain.ManuscriptInstructionKind) (ManuscriptPlan, error) {
	payload, _ := json.Marshal(struct {
		Baseline    domain.ManuscriptBaseline        `json:"baseline"`
		Instruction string                           `json:"instruction"`
		Kind        domain.ManuscriptInstructionKind `json:"kind"`
	}{baseline, instruction, kind})
	system := "Return JSON with a proposed outline only when the instruction changes story facts or structure, plus impacted_chapter_ids. The server derives and compares narrative contracts. Never invent source evidence."
	response, err := callManuscriptModel(ctx, w.model, system, payload, 2400)
	if err != nil {
		return ManuscriptPlan{}, err
	}
	var envelope struct {
		Outline            domain.OutlineEntry `json:"outline"`
		ImpactedChapterIDs []string            `json:"impacted_chapter_ids"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(response)), &envelope); err != nil {
		return ManuscriptPlan{}, fmt.Errorf("decode manuscript plan: %w", err)
	}
	if strings.TrimSpace(envelope.Outline.CoreEvent) == "" && strings.TrimSpace(envelope.Outline.Title) == "" && strings.TrimSpace(envelope.Outline.Hook) == "" {
		return ManuscriptPlan{ImpactedChapterIDs: envelope.ImpactedChapterIDs}, nil
	}
	envelope.Outline.ID = baseline.ChapterID
	envelope.Outline.Chapter = baseline.DisplayChapter
	contract := narrativeContractFromProposedEntry(envelope.Outline, baseline.NarrativeContract)
	changed := manuscriptContractSignature(contract) != manuscriptContractSignature(baseline.NarrativeContract)
	return ManuscriptPlan{StoryChanged: changed, Outline: envelope.Outline, Contract: contract, ImpactedChapterIDs: envelope.ImpactedChapterIDs}, nil
}

func (w *modelManuscriptWriter) GenerateManuscriptSegment(ctx context.Context, runtime domain.ManuscriptRevisionRuntime, item domain.ManuscriptReworkItem, generationContext ManuscriptGenerationContext, attempt, segment int, prior string) (ManuscriptGeneratedSegment, error) {
	system := w.prompts.NormalManuscriptRewrite
	if runtime.InstructionKind == domain.ManuscriptInstructionPolish {
		system = w.prompts.NormalManuscriptPolish
	}
	if runtime.Mode == domain.RevisionModeAdaptation {
		system = w.prompts.AdaptationManuscriptRewrite
		if runtime.InstructionKind == domain.ManuscriptInstructionPolish {
			system = w.prompts.AdaptationManuscriptPolish
		}
	}
	payload, _ := json.Marshal(struct {
		Runtime      domain.ManuscriptRevisionRuntime `json:"runtime"`
		Item         domain.ManuscriptReworkItem      `json:"item"`
		Context      ManuscriptGenerationContext      `json:"generation_context"`
		Attempt      int                              `json:"attempt"`
		Segment      int                              `json:"segment"`
		PriorSegment string                           `json:"prior_segment,omitempty"`
	}{runtime, item, generationContext, attempt, segment, prior})
	system += "\nReturn one JSON object with chapter_id, attempt, segment, prose, complete, truncated, and on the final segment sidecars with summary/events/timeline/cast_state/relationships/foreshadow/world_facts/carry_forward. Do not return narrative_contract or any protected-state hash: the server derives those from the approved outline and authoritative state. Echo the exact requested identity."
	if runtime.Mode == domain.RevisionModeAdaptation {
		system += "\nThe events sidecar contains story events only; do not self-report adaptation compliance or absence findings. An independent auditor creates those findings from the complete prose."
	}
	if len(system)+len(payload) > manuscriptCompiledRequestBudgetBytes {
		return ManuscriptGeneratedSegment{}, &domain.ManuscriptRevisionError{Class: "request_budget_exceeded", Err: fmt.Errorf("compiled writer request is %d bytes (limit %d); complete formal prose was not truncated", len(system)+len(payload), manuscriptCompiledRequestBudgetBytes)}
	}
	response, err := callManuscriptModel(ctx, w.model, system, payload, 7000)
	if err != nil {
		return ManuscriptGeneratedSegment{}, err
	}
	var envelope struct {
		ChapterID string                     `json:"chapter_id"`
		Attempt   int                        `json:"attempt"`
		Segment   int                        `json:"segment"`
		Prose     string                     `json:"prose"`
		Complete  bool                       `json:"complete"`
		Truncated bool                       `json:"truncated"`
		Sidecars  map[string]json.RawMessage `json:"sidecars"`
	}
	payload = []byte(extractJSONObject(response))
	var decodeErr error
	if runtime.Mode == domain.RevisionModeAdaptation {
		decodeErr = json.Unmarshal(payload, &envelope)
	} else {
		decodeErr = decodeNormalManuscriptEnvelope(payload, &envelope)
	}
	if decodeErr != nil {
		return ManuscriptGeneratedSegment{}, fmt.Errorf("decode manuscript segment: %w", decodeErr)
	}
	if err := validateGeneratedManuscriptSegment(runtime.Mode, ManuscriptGeneratedSegment{Sidecars: envelope.Sidecars, Complete: envelope.Complete}); err != nil {
		return ManuscriptGeneratedSegment{}, err
	}
	if envelope.ChapterID != item.ChapterID || envelope.Attempt != attempt || envelope.Segment != segment {
		return ManuscriptGeneratedSegment{}, &domain.ManuscriptRevisionError{Class: "segment_identity_mismatch", Err: fmt.Errorf("writer returned chapter=%q attempt=%d segment=%d", envelope.ChapterID, envelope.Attempt, envelope.Segment)}
	}
	return ManuscriptGeneratedSegment{ChapterID: envelope.ChapterID, Attempt: envelope.Attempt, Segment: envelope.Segment, Prose: envelope.Prose, Complete: envelope.Complete, Truncated: envelope.Truncated, Sidecars: envelope.Sidecars}, nil
}

func (a *modelManuscriptAuditor) AuditManuscriptCandidate(ctx context.Context, runtime domain.ManuscriptRevisionRuntime, candidate domain.ManuscriptCandidate) (bool, string, error) {
	system := a.prompts.NormalManuscriptAudit
	if runtime.Mode == domain.RevisionModeAdaptation {
		system = a.prompts.AdaptationManuscriptAudit
	}
	prose, err := a.store.ManuscriptRevisions.Content().Read(candidate.Prose)
	if err != nil {
		return false, "", err
	}
	sidecars := make(map[string]json.RawMessage)
	for name, ref := range map[string]domain.ManuscriptContentRef{"summary": candidate.Sidecar.Summary, "events": candidate.Sidecar.Events, "timeline": candidate.Sidecar.Timeline, "cast_state": candidate.Sidecar.CastState, "relationships": candidate.Sidecar.Relationships, "foreshadow": candidate.Sidecar.Foreshadow, "world_facts": candidate.Sidecar.WorldFacts, "carry_forward": candidate.Sidecar.CarryForward} {
		content, readErr := a.store.ManuscriptRevisions.Content().Read(ref)
		if readErr != nil {
			return false, "", readErr
		}
		sidecars[name] = content
	}
	manifest, err := a.store.Adaptation.LoadSourceManifest()
	if err != nil {
		return false, "", err
	}
	plan, err := a.store.Adaptation.LoadPlan()
	if err != nil {
		return false, "", err
	}
	check, err := a.store.Adaptation.LoadCheck(candidate.DisplayChapter)
	if err != nil {
		return false, "", err
	}
	payload, _ := json.Marshal(struct {
		Runtime              domain.ManuscriptRevisionRuntime `json:"runtime"`
		Candidate            domain.ManuscriptCandidate       `json:"candidate"`
		Prose                string                           `json:"prose"`
		Sidecars             map[string]json.RawMessage       `json:"sidecars"`
		SourceManifest       *domain.AdaptationSourceManifest `json:"source_manifest,omitempty"`
		AdaptationPlan       *domain.AdaptationPlan           `json:"adaptation_plan,omitempty"`
		PriorAdaptationCheck *domain.AdaptationCheck          `json:"prior_adaptation_check,omitempty"`
	}{runtime, candidate, string(prose), sidecars, manifest, plan, check})
	response, err := callManuscriptModel(ctx, a.model, system+"\nReturn JSON with passed (boolean) and report (string).", payload, 3000)
	if err != nil {
		return false, "", err
	}
	var envelope struct {
		Passed bool   `json:"passed"`
		Report string `json:"report"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(response)), &envelope); err != nil {
		return false, "", fmt.Errorf("decode manuscript audit: %w", err)
	}
	if strings.TrimSpace(envelope.Report) == "" {
		return false, "", fmt.Errorf("independent manuscript audit report is empty")
	}
	return envelope.Passed, envelope.Report, nil
}

func (a *modelManuscriptAuditor) AuditCandidateContract(ctx context.Context, task ManuscriptContractAuditTask, prose string) (ManuscriptContractAuditDecision, error) {
	payload, _ := json.Marshal(struct {
		Task  ManuscriptContractAuditTask `json:"task"`
		Prose string                      `json:"complete_candidate_prose"`
	}{task, prose})
	system := `Act only as the independent contract locator. Derive the candidate's narrative contract from complete prose and approved outline. Return role contract_locator, task_signature, candidate_sha256, contract and evidence. Contract must include chapter_id, outline_sha256, desire, obstacle, choice, cost, result, exit_state, future_commitments and protected_state_sha256 from the task. For each of desire, obstacle, choice, cost, result, exit_state and future_commitments, cite one distinct non-overlapping exact half-open Unicode-rune range in narrative prose as {field,start_rune,end_rune,quote}. Do not copy contract values from writer sidecars. Reject rather than invent evidence; metadata, quotations about a possibility, negated claims and repeated locations are not evidence. A separate verifier decides entailment.`
	response, err := callManuscriptModel(ctx, manuscriptAuditRoleModel(a.contractLocator, a.model), system, payload, 3600)
	if err != nil {
		return ManuscriptContractAuditDecision{}, err
	}
	var decision ManuscriptContractAuditDecision
	if err := json.Unmarshal([]byte(extractJSONObject(response)), &decision); err != nil {
		return ManuscriptContractAuditDecision{}, fmt.Errorf("decode candidate contract audit: %w", err)
	}
	return decision, nil
}

func (a *modelManuscriptAuditor) VerifyCandidateContract(ctx context.Context, task ManuscriptContractVerificationTask, locator ManuscriptContractAuditDecision, approved domain.NarrativeContract, prose string) (ManuscriptContractVerificationDecision, error) {
	payload, _ := json.Marshal(map[string]any{"task": task, "locator": locator, "approved_contract": approved, "complete_candidate_prose": prose})
	system := `You are the contract semantic verifier, separate from the locator. Reread every located quote in complete prose. Return role contract_verifier, task_signature, candidate_sha256 and exactly seven receipts. Each receipt must echo field, derived value, approved_value, exact range and quote, use verdict entailed only when that quote semantically entails the field value and does not contradict the approved value. Reject arbitrary characters, reused meaning, metadata, quotation and negation.`
	response, err := callManuscriptModel(ctx, manuscriptAuditRoleModel(a.contractVerifier, a.model), system, payload, 4200)
	if err != nil {
		return ManuscriptContractVerificationDecision{}, err
	}
	var result ManuscriptContractVerificationDecision
	if err := json.Unmarshal([]byte(extractJSONObject(response)), &result); err != nil {
		return result, fmt.Errorf("decode contract verification: %w", err)
	}
	return result, nil
}

func (a *modelManuscriptAuditor) AuditAdaptationCandidate(ctx context.Context, runtime domain.ManuscriptRevisionRuntime, candidate domain.ManuscriptCandidate, task ManuscriptAdaptationAuditTask) (ManuscriptAdaptationAuditDecision, error) {
	prose, err := a.store.ManuscriptRevisions.Content().Read(candidate.Prose)
	if err != nil {
		return ManuscriptAdaptationAuditDecision{}, err
	}
	manifest, err := a.store.Adaptation.LoadSourceManifest()
	if err != nil {
		return ManuscriptAdaptationAuditDecision{}, err
	}
	plan, err := a.store.Adaptation.LoadPlan()
	if err != nil {
		return ManuscriptAdaptationAuditDecision{}, err
	}
	priorCheck, err := a.store.Adaptation.LoadCheck(candidate.DisplayChapter)
	if err != nil {
		return ManuscriptAdaptationAuditDecision{}, err
	}
	payload, _ := json.Marshal(struct {
		Task           ManuscriptAdaptationAuditTask    `json:"task"`
		Runtime        domain.ManuscriptRevisionRuntime `json:"runtime"`
		Candidate      domain.ManuscriptCandidate       `json:"candidate"`
		Prose          string                           `json:"prose"`
		SourceManifest *domain.AdaptationSourceManifest `json:"source_manifest"`
		AdaptationPlan *domain.AdaptationPlan           `json:"adaptation_plan"`
		PriorCheck     *domain.AdaptationCheck          `json:"prior_adaptation_check"`
	}{task, runtime, candidate, string(prose), manifest, plan, priorCheck})
	system := a.prompts.AdaptationManuscriptAudit + "\nAct only as the adaptation locator. Return role adaptation_locator, passed, report, task_signature, candidate_sha256, source_manifest_sha256, adaptation_plan_sha256 and findings; omit absence_receipt. Every affirmed event/change finding must cite an exact half-open Unicode-rune range and verbatim quote; for events echo the exact server-provided source_description. A separate semantic verifier and a separate whole-document absence verifier make all decisions. Echo all task bindings exactly."
	response, err := callManuscriptModel(ctx, manuscriptAuditRoleModel(a.adaptationLocator, a.model), system, payload, 4000)
	if err != nil {
		return ManuscriptAdaptationAuditDecision{}, err
	}
	var decision ManuscriptAdaptationAuditDecision
	if err := json.Unmarshal([]byte(extractJSONObject(response)), &decision); err != nil {
		return ManuscriptAdaptationAuditDecision{}, fmt.Errorf("decode structured adaptation audit: %w", err)
	}
	return decision, nil
}

func (a *modelManuscriptAuditor) VerifyAdaptationCandidate(ctx context.Context, task ManuscriptAdaptationVerificationTask, locator ManuscriptAdaptationAuditDecision, prose string) (ManuscriptAdaptationVerificationDecision, error) {
	payload, _ := json.Marshal(map[string]any{"task": task, "locator": locator, "complete_candidate_prose": prose})
	system := `You are an adaptation semantic verifier separate from the locator. Return role adaptation_semantic_verifier, task_signature, candidate_sha256 and one receipt for every affirmed locator finding. Echo kind, id, source_description, range and quote. Verdict entailed is allowed only if the quote itself semantically realizes the source event or required change; unrelated prose and echoed descriptions fail.`
	response, err := callManuscriptModel(ctx, manuscriptAuditRoleModel(a.adaptationVerifier, a.model), system, payload, 4000)
	if err != nil {
		return ManuscriptAdaptationVerificationDecision{}, err
	}
	var result ManuscriptAdaptationVerificationDecision
	if err := json.Unmarshal([]byte(extractJSONObject(response)), &result); err != nil {
		return result, fmt.Errorf("decode adaptation verification: %w", err)
	}
	return result, nil
}

func (a *modelManuscriptAuditor) VerifyWholeDocumentAbsence(ctx context.Context, task ManuscriptWholeDocumentAbsenceTask, prose string) (ManuscriptWholeDocumentAbsenceReceipt, error) {
	payload, _ := json.Marshal(map[string]any{"task": task, "complete_candidate_prose": prose})
	system := `You are the whole-document absence verifier, separate from both adaptation locator and semantic verifier. Inspect all prose against every forbidden ID. If and only if all are absent, return task_signature, candidate_sha256, prose_runes and the complete forbidden_ids. Do not return success when any forbidden meaning occurs.`
	response, err := callManuscriptModel(ctx, manuscriptAuditRoleModel(a.absenceVerifier, a.model), system, payload, 2400)
	if err != nil {
		return ManuscriptWholeDocumentAbsenceReceipt{}, err
	}
	var result ManuscriptWholeDocumentAbsenceReceipt
	if err := json.Unmarshal([]byte(extractJSONObject(response)), &result); err != nil {
		return result, fmt.Errorf("decode whole-document absence verification: %w", err)
	}
	return result, nil
}

func callManuscriptModel(ctx context.Context, model agentcore.ChatModel, system string, payload []byte, maxTokens int) (string, error) {
	if model == nil {
		return "", fmt.Errorf("manuscript model is unavailable")
	}
	response, err := model.Generate(ctx, []agentcore.Message{agentcore.SystemMsg(system), agentcore.UserMsg(string(payload))}, nil, agentcore.WithMaxTokens(maxTokens), agentcore.WithJSONMode())
	if err != nil {
		return "", err
	}
	if response == nil || strings.TrimSpace(response.Message.TextContent()) == "" {
		return "", fmt.Errorf("manuscript model returned an empty response")
	}
	return response.Message.TextContent(), nil
}

func manuscriptAuditRoleModel(preferred, fallback agentcore.ChatModel) agentcore.ChatModel {
	if preferred != nil {
		return preferred
	}
	return fallback
}
