package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

type CharacterRunMode string

const (
	CharacterRunAnalyze CharacterRunMode = "analyze"
	CharacterRunReview  CharacterRunMode = "review"
)

const (
	characterContextReportLimit = 120
	characterContextListLimit   = 80
)

// CharacterRunRegistry binds every run ID to one mode and to the exact
// evidence snapshot returned by character_context. It is shared by all three
// Character Agent tools and survives model failover within the live Agent.
type CharacterRunRegistry struct {
	mu   sync.Mutex
	runs map[string]characterRunState
}

type characterRunState struct {
	Mode      CharacterRunMode
	Context   domain.CharacterCardBinding
	Submitted bool
	Tool      string
}

func NewCharacterRunRegistry() *CharacterRunRegistry {
	return &CharacterRunRegistry{runs: make(map[string]characterRunState)}
}

func (r *CharacterRunRegistry) bindContext(runID string, mode CharacterRunMode, binding domain.CharacterCardBinding) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, exists := r.runs[runID]
	if exists && state.Mode != mode {
		return fmt.Errorf("character run %q is already bound to mode %q: %w", runID, state.Mode, errs.ErrToolConflict)
	}
	if state.Submitted {
		return fmt.Errorf("character run %q already submitted through %s: %w", runID, state.Tool, errs.ErrToolConflict)
	}
	state.Mode = mode
	state.Context = binding
	r.runs[runID] = state
	return nil
}

func (r *CharacterRunRegistry) requireSubmission(
	runID string,
	mode CharacterRunMode,
	tool string,
	expected domain.CharacterCardBinding,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, exists := r.runs[runID]
	if !exists {
		return fmt.Errorf("character run %q must call character_context before %s: %w", runID, tool, errs.ErrToolPrecondition)
	}
	if state.Mode != mode {
		return fmt.Errorf("character run %q is mode %q, not %q: %w", runID, state.Mode, mode, errs.ErrToolConflict)
	}
	if state.Submitted {
		return fmt.Errorf("character run %q already submitted through %s: %w", runID, state.Tool, errs.ErrToolConflict)
	}
	if !sameCharacterBinding(state.Context, expected) {
		return fmt.Errorf("character run %q evidence snapshot is stale; call character_context in a new run: %w", runID, errs.ErrToolConflict)
	}
	return nil
}

func (r *CharacterRunRegistry) markSubmitted(runID, tool string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.runs[runID]
	state.Submitted = true
	state.Tool = tool
	r.runs[runID] = state
}

func sameCharacterBinding(left, right domain.CharacterCardBinding) bool {
	return left.Candidate == right.Candidate && left.InputDigest == right.InputDigest
}

type characterToolBase struct {
	store    *store.Store
	registry *CharacterRunRegistry
}

func newCharacterToolBase(st *store.Store, registry *CharacterRunRegistry) characterToolBase {
	if registry == nil {
		registry = NewCharacterRunRegistry()
	}
	return characterToolBase{store: st, registry: registry}
}

type CharacterContextTool struct {
	characterToolBase
}

func NewCharacterContextTool(st *store.Store, registry *CharacterRunRegistry) *CharacterContextTool {
	return &CharacterContextTool{characterToolBase: newCharacterToolBase(st, registry)}
}

func (t *CharacterContextTool) Name() string { return "character_context" }
func (t *CharacterContextTool) Description() string {
	return "Read the bounded, current character evidence packet for exactly one analyze or review run. " +
		"It returns the Foundation revision/audit, candidate digest, input digest, current candidate, user constraints, " +
		"and adaptation-only structured source evidence without raw source chapters."
}
func (t *CharacterContextTool) Label() string                        { return "读取角色证据" }
func (t *CharacterContextTool) ReadOnly(json.RawMessage) bool        { return true }
func (t *CharacterContextTool) ConcurrencySafe(json.RawMessage) bool { return true }
func (t *CharacterContextTool) StrictSchema() bool                   { return true }
func (t *CharacterContextTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("run_id", schema.String("Stable non-empty ID for this Character Agent run")).Required(),
		schema.Property("mode", schema.Enum("Single responsibility for this run", string(CharacterRunAnalyze), string(CharacterRunReview))).Required(),
	)
}

type characterContextArgs struct {
	RunID string           `json:"run_id"`
	Mode  CharacterRunMode `json:"mode"`
}

func (t *CharacterContextTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var request characterContextArgs
	if err := decodeCharacterToolArgs(args, &request); err != nil {
		return nil, err
	}
	if err := validateCharacterRunIdentity(request.RunID, request.Mode); err != nil {
		return nil, err
	}
	packet, binding, err := buildCharacterContext(t.store, request.Mode)
	if err != nil {
		return nil, err
	}
	if err := t.registry.bindContext(request.RunID, request.Mode, binding); err != nil {
		return nil, err
	}
	packet["run_id"] = strings.TrimSpace(request.RunID)
	packet["mode"] = request.Mode
	return json.Marshal(packet)
}

type SaveCharacterCandidateTool struct {
	characterToolBase
}

func NewSaveCharacterCandidateTool(st *store.Store, registry *CharacterRunRegistry) *SaveCharacterCandidateTool {
	return &SaveCharacterCandidateTool{characterToolBase: newCharacterToolBase(st, registry)}
}

func (t *SaveCharacterCandidateTool) Name() string { return "save_character_candidate" }
func (t *SaveCharacterCandidateTool) Description() string {
	return "Analyze-mode only. Atomically replaces only StoryFoundation characters and planned relationships, " +
		"then stores the signature-bound CharacterCard candidate lifecycle, completeness, rationale, and source mappings."
}
func (t *SaveCharacterCandidateTool) Label() string                        { return "保存角色候选" }
func (t *SaveCharacterCandidateTool) ReadOnly(json.RawMessage) bool        { return false }
func (t *SaveCharacterCandidateTool) ConcurrencySafe(json.RawMessage) bool { return false }
func (t *SaveCharacterCandidateTool) StrictSchema() bool                   { return true }
func (t *SaveCharacterCandidateTool) Schema() map[string]any {
	return schema.Object(
		characterRunProperties(CharacterRunAnalyze)...,
	)
}

func characterRunProperties(mode CharacterRunMode) []schema.Prop {
	common := []schema.Prop{
		schema.Property("run_id", schema.String("Stable non-empty Character Agent run ID")).Required(),
		schema.Property("mode", schema.Enum("Single run mode", string(mode))).Required(),
		schema.Property("idempotency_key", schema.String("Non-empty key stable across retries of the same submission")).Required(),
		schema.Property("base_revision", schema.Int("Foundation revision returned by character_context")).Required(),
		schema.Property("base_audit_signature", schema.String("Foundation audit signature returned by character_context")).Required(),
		schema.Property("candidate_digest", schema.String("Character content digest returned by character_context")).Required(),
		schema.Property("input_digest", schema.String("Applicable input digest returned by character_context")).Required(),
	}
	if mode == CharacterRunReview {
		return append(common,
			schema.Property("verdict", schema.Enum("Requested review verdict", "pass", "needs_revision")).Required(),
			schema.Property("summary", schema.String("Evidence-grounded review summary")).Required(),
			schema.Property("findings", schema.Array("Structured review findings", characterFindingSchema())).Required(),
		)
	}
	return append(common,
		schema.Property("analysis_summary", schema.String("Compact generation rationale and uncertain decisions")).Required(),
		schema.Property("characters", schema.Array("Unified original/adaptation character cards", characterSchema())).Required(),
		schema.Property("relationships", schema.Array("Complete planned relationships", characterRelationshipSchema())).Required(),
		schema.Property("relationships_reviewed", schema.Bool("True when absence of additional relationships was explicitly reviewed")).Required(),
		schema.Property("source_mappings", schema.Array("Adaptation source mappings; [] for original projects", characterSourceMappingSchema())).Required(),
	)
}

type saveCharacterCandidateArgs struct {
	RunID                 string                          `json:"run_id"`
	Mode                  CharacterRunMode                `json:"mode"`
	IdempotencyKey        string                          `json:"idempotency_key"`
	BaseRevision          int64                           `json:"base_revision"`
	BaseAuditSignature    string                          `json:"base_audit_signature"`
	CandidateDigest       string                          `json:"candidate_digest"`
	InputDigest           string                          `json:"input_digest"`
	AnalysisSummary       string                          `json:"analysis_summary"`
	Characters            []domain.Character              `json:"characters"`
	Relationships         []domain.CharacterRelationship  `json:"relationships"`
	RelationshipsReviewed bool                            `json:"relationships_reviewed"`
	SourceMappings        []domain.CharacterSourceMapping `json:"source_mappings"`
}

func (t *SaveCharacterCandidateTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var request saveCharacterCandidateArgs
	if err := decodeCharacterToolArgs(args, &request); err != nil {
		return nil, err
	}
	if err := validateCharacterRunIdentity(request.RunID, request.Mode); err != nil {
		return nil, err
	}
	if request.Mode != CharacterRunAnalyze {
		return nil, fmt.Errorf("save_character_candidate requires mode=analyze: %w", errs.ErrToolConflict)
	}
	if err := validateCharacterSubmissionIdentity(request.IdempotencyKey, request.BaseRevision, request.BaseAuditSignature, request.CandidateDigest, request.InputDigest); err != nil {
		return nil, err
	}
	current, binding, inputs, projectMode, coreCast, err := currentCharacterBinding(t.store)
	if err != nil {
		return nil, err
	}
	candidate := domain.CloneStoryFoundation(current)
	candidate.Characters = request.Characters
	candidate.Relationships = request.Relationships
	candidate.RelationshipsReviewed = request.RelationshipsReviewed
	normalized, err := domain.NormalizeStoryFoundation(candidate)
	if err != nil {
		return nil, fmt.Errorf("normalize character candidate: %w: %w", errs.ErrToolArgs, err)
	}
	submissionDigest, err := characterSubmissionDigest(request)
	if err != nil {
		return nil, err
	}
	existing, err := t.store.CharacterCards.Load(binding)
	if err != nil {
		return nil, fmt.Errorf("load character lifecycle before candidate save: %w", err)
	}
	if characterCandidateRetryMatches(existing, request, normalized, binding, submissionDigest) {
		return characterCandidateResult(*existing, binding, true)
	}
	if err := requireCharacterBinding(request.BaseRevision, request.BaseAuditSignature, request.CandidateDigest, request.InputDigest, binding); err != nil {
		return nil, err
	}
	if err := t.registry.requireSubmission(request.RunID, request.Mode, t.Name(), binding); err != nil {
		return nil, err
	}
	if projectMode == domain.CharacterCardProjectOriginal && len(request.SourceMappings) != 0 {
		return nil, fmt.Errorf("original character candidate must not fabricate source mappings: %w", errs.ErrToolArgs)
	}
	if projectMode == domain.CharacterCardProjectAdaptation {
		sourceIDs := adaptationSourceCharacterIDs(t.store)
		targetIDs := foundationCharacterIDs(normalized)
		if err := domain.ValidateCharacterSourceCoverage(request.SourceMappings, sourceIDs, targetIDs); err != nil {
			return nil, fmt.Errorf("adaptation character source coverage: %w: %w", errs.ErrToolArgs, err)
		}
	}
	completeness, err := domain.EvaluateCharacterCardCompleteness(normalized, coreCast)
	if err != nil {
		return nil, fmt.Errorf("evaluate character candidate completeness: %w", err)
	}
	saved, err := t.store.Foundation.SaveRevisionCAS(normalized, current.Revision)
	if err != nil {
		return nil, fmt.Errorf("save character candidate conflict/stale: %w: %w", errs.ErrToolConflict, err)
	}
	savedBinding, err := domain.CharacterCardBindingFromFoundation(saved, inputs)
	if err != nil {
		return nil, fmt.Errorf("bind saved character candidate: %w", err)
	}
	existing, err = t.store.CharacterCards.Load(savedBinding)
	if err != nil {
		return nil, fmt.Errorf("load character lifecycle after candidate save: %w", err)
	}
	expectedLifecycleRevision := int64(0)
	createdAt := ""
	if existing != nil {
		expectedLifecycleRevision = existing.Revision
		createdAt = existing.CreatedAt
	}
	lifecycle := domain.CharacterCardLifecycle{
		Version:            domain.CharacterCardLifecycleVersion,
		Mode:               projectMode,
		Candidate:          savedBinding.Candidate,
		Inputs:             savedBinding.Inputs,
		InputDigest:        savedBinding.InputDigest,
		AnalysisSummary:    strings.TrimSpace(request.AnalysisSummary),
		Completeness:       completeness,
		AnalysisStatus:     domain.CharacterCardAnalysisCandidateReady,
		ReviewStatus:       domain.CharacterCardReviewNotReviewed,
		Findings:           []domain.CharacterCardReviewFinding{},
		ConfirmationStatus: domain.CharacterCardUnconfirmed,
		RunID:              strings.TrimSpace(request.RunID),
		IdempotencyKey:     strings.TrimSpace(request.IdempotencyKey),
		SubmissionDigest:   submissionDigest,
		SourceMappings:     request.SourceMappings,
		CreatedAt:          createdAt,
	}
	savedLifecycle, err := t.store.CharacterCards.SaveCAS(lifecycle, expectedLifecycleRevision, savedBinding)
	if err != nil {
		return nil, fmt.Errorf("save character candidate lifecycle conflict/stale: %w: %w", errs.ErrToolConflict, err)
	}
	t.registry.markSubmitted(request.RunID, t.Name())
	return characterCandidateResult(savedLifecycle, savedBinding, false)
}

type SaveCharacterReviewTool struct {
	characterToolBase
}

func NewSaveCharacterReviewTool(st *store.Store, registry *CharacterRunRegistry) *SaveCharacterReviewTool {
	return &SaveCharacterReviewTool{characterToolBase: newCharacterToolBase(st, registry)}
}

func (t *SaveCharacterReviewTool) Name() string { return "save_character_review" }
func (t *SaveCharacterReviewTool) Description() string {
	return "Review-mode only. Saves findings without modifying candidate content. A requested pass is deterministically " +
		"downgraded to needs_revision when any blocking finding or CharacterCard completeness failure exists."
}
func (t *SaveCharacterReviewTool) Label() string                        { return "保存角色审核" }
func (t *SaveCharacterReviewTool) ReadOnly(json.RawMessage) bool        { return false }
func (t *SaveCharacterReviewTool) ConcurrencySafe(json.RawMessage) bool { return false }
func (t *SaveCharacterReviewTool) StrictSchema() bool                   { return true }
func (t *SaveCharacterReviewTool) Schema() map[string]any {
	return schema.Object(characterRunProperties(CharacterRunReview)...)
}

type saveCharacterReviewArgs struct {
	RunID              string                              `json:"run_id"`
	Mode               CharacterRunMode                    `json:"mode"`
	IdempotencyKey     string                              `json:"idempotency_key"`
	BaseRevision       int64                               `json:"base_revision"`
	BaseAuditSignature string                              `json:"base_audit_signature"`
	CandidateDigest    string                              `json:"candidate_digest"`
	InputDigest        string                              `json:"input_digest"`
	Verdict            string                              `json:"verdict"`
	Summary            string                              `json:"summary"`
	Findings           []domain.CharacterCardReviewFinding `json:"findings"`
}

func (t *SaveCharacterReviewTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var request saveCharacterReviewArgs
	if err := decodeCharacterToolArgs(args, &request); err != nil {
		return nil, err
	}
	if err := validateCharacterRunIdentity(request.RunID, request.Mode); err != nil {
		return nil, err
	}
	if request.Mode != CharacterRunReview {
		return nil, fmt.Errorf("save_character_review requires mode=review: %w", errs.ErrToolConflict)
	}
	if err := validateCharacterSubmissionIdentity(request.IdempotencyKey, request.BaseRevision, request.BaseAuditSignature, request.CandidateDigest, request.InputDigest); err != nil {
		return nil, err
	}
	if request.Verdict != "pass" && request.Verdict != "needs_revision" {
		return nil, fmt.Errorf("character review verdict %q is invalid: %w", request.Verdict, errs.ErrToolArgs)
	}
	if strings.TrimSpace(request.Summary) == "" {
		return nil, fmt.Errorf("character review summary is required: %w", errs.ErrToolArgs)
	}
	submissionDigest, err := characterSubmissionDigest(request)
	if err != nil {
		return nil, err
	}
	foundation, binding, _, _, coreCast, err := currentCharacterBinding(t.store)
	if err != nil {
		return nil, err
	}
	lifecycle, err := t.store.CharacterCards.Load(binding)
	if err != nil {
		return nil, fmt.Errorf("load character candidate for review: %w", err)
	}
	if lifecycle == nil || lifecycle.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady ||
		lifecycle.Candidate != binding.Candidate || lifecycle.InputDigest != binding.InputDigest {
		return nil, fmt.Errorf("character candidate is missing or stale; run analyze first: %w", errs.ErrToolConflict)
	}
	completeness, err := domain.EvaluateCharacterCardCompleteness(foundation, coreCast)
	if err != nil {
		return nil, fmt.Errorf("evaluate reviewed character completeness: %w", err)
	}
	findings := append([]domain.CharacterCardReviewFinding(nil), request.Findings...)
	findings = appendCompletenessFindings(findings, completeness)
	finalStatus := domain.CharacterCardReviewPassed
	if request.Verdict != "pass" || hasBlockingCharacterFinding(findings) {
		finalStatus = domain.CharacterCardReviewNeedsRevision
	}
	if lifecycle.RunID == strings.TrimSpace(request.RunID) {
		if lifecycle.IdempotencyKey == strings.TrimSpace(request.IdempotencyKey) &&
			lifecycle.SubmissionDigest == submissionDigest &&
			lifecycle.ReviewedCandidate == binding.Candidate &&
			lifecycle.ReviewedInputDigest == binding.InputDigest {
			return characterReviewResult(*lifecycle, binding, true)
		}
		return nil, fmt.Errorf("character review run is already submitted with different content: %w", errs.ErrToolConflict)
	}
	if err := requireCharacterBinding(request.BaseRevision, request.BaseAuditSignature, request.CandidateDigest, request.InputDigest, binding); err != nil {
		return nil, err
	}
	if err := t.registry.requireSubmission(request.RunID, request.Mode, t.Name(), binding); err != nil {
		return nil, err
	}
	reviewed := *lifecycle
	reviewed.Completeness = completeness
	reviewed.ReviewStatus = finalStatus
	reviewed.ReviewedCandidate = binding.Candidate
	reviewed.ReviewedInputDigest = binding.InputDigest
	reviewed.ReviewSummary = strings.TrimSpace(request.Summary)
	reviewed.Findings = findings
	reviewed.ConfirmationStatus = domain.CharacterCardUnconfirmed
	reviewed.RunID = strings.TrimSpace(request.RunID)
	reviewed.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	reviewed.SubmissionDigest = submissionDigest
	reviewed.Error = nil
	saved, err := t.store.CharacterCards.SaveCAS(reviewed, lifecycle.Revision, binding)
	if err != nil {
		return nil, fmt.Errorf("save character review conflict/stale: %w: %w", errs.ErrToolConflict, err)
	}
	t.registry.markSubmitted(request.RunID, t.Name())
	return characterReviewResult(saved, binding, false)
}

func characterCandidateRetryMatches(
	existing *domain.CharacterCardLifecycle,
	request saveCharacterCandidateArgs,
	candidate domain.StoryFoundation,
	current domain.CharacterCardBinding,
	submissionDigest string,
) bool {
	if existing == nil ||
		existing.RunID != strings.TrimSpace(request.RunID) ||
		existing.IdempotencyKey != strings.TrimSpace(request.IdempotencyKey) ||
		existing.SubmissionDigest != submissionDigest ||
		existing.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady ||
		existing.Candidate != current.Candidate ||
		existing.InputDigest != current.InputDigest ||
		existing.AnalysisSummary != strings.TrimSpace(request.AnalysisSummary) {
		return false
	}
	digest, err := domain.CharacterCardContentDigest(candidate)
	if err != nil || digest != current.Candidate.CharacterContentDigest {
		return false
	}
	retryLifecycle := *existing
	retryLifecycle.SourceMappings = request.SourceMappings
	normalized, err := domain.NormalizeCharacterCardLifecycle(retryLifecycle)
	return err == nil && reflect.DeepEqual(normalized.SourceMappings, existing.SourceMappings)
}

func characterSubmissionDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode character submission digest: %w", err)
	}
	return store.TextSHA256(string(encoded)), nil
}

func characterCandidateResult(
	lifecycle domain.CharacterCardLifecycle,
	binding domain.CharacterCardBinding,
	idempotent bool,
) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"saved":               true,
		"idempotent":          idempotent,
		"mode":                CharacterRunAnalyze,
		"run_id":              lifecycle.RunID,
		"foundation_revision": binding.Candidate.FoundationRevision,
		"candidate_digest":    binding.Candidate.CharacterContentDigest,
		"input_digest":        binding.InputDigest,
		"lifecycle_revision":  lifecycle.Revision,
		"completeness":        lifecycle.Completeness,
		"ready_for_review":    true,
	})
}

func characterReviewResult(
	lifecycle domain.CharacterCardLifecycle,
	binding domain.CharacterCardBinding,
	idempotent bool,
) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"saved":              true,
		"idempotent":         idempotent,
		"mode":               CharacterRunReview,
		"run_id":             lifecycle.RunID,
		"final_status":       lifecycle.ReviewStatus,
		"passed":             lifecycle.ReviewStatus == domain.CharacterCardReviewPassed,
		"candidate_digest":   binding.Candidate.CharacterContentDigest,
		"input_digest":       binding.InputDigest,
		"lifecycle_revision": lifecycle.Revision,
		"completeness":       lifecycle.Completeness,
		"findings":           lifecycle.Findings,
	})
}

func buildCharacterContext(st *store.Store, mode CharacterRunMode) (map[string]any, domain.CharacterCardBinding, error) {
	foundation, binding, _, projectMode, coreCast, err := currentCharacterBinding(st)
	if err != nil {
		return nil, domain.CharacterCardBinding{}, err
	}
	lifecycle, err := st.CharacterCards.Load(binding)
	if err != nil {
		return nil, domain.CharacterCardBinding{}, fmt.Errorf("load character lifecycle: %w", err)
	}
	if mode == CharacterRunReview && (lifecycle == nil || lifecycle.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady) {
		return nil, domain.CharacterCardBinding{}, fmt.Errorf("review requires a current persisted character candidate: %w", errs.ErrToolPrecondition)
	}
	userRules, err := st.UserRules.Load()
	if err != nil {
		return nil, domain.CharacterCardBinding{}, fmt.Errorf("load character user constraints: %w", err)
	}
	packet := map[string]any{
		"project_mode":           projectMode,
		"base_revision":          binding.Candidate.FoundationRevision,
		"base_audit_signature":   binding.Candidate.FoundationAuditSignature,
		"candidate_digest":       binding.Candidate.CharacterContentDigest,
		"input_digest":           binding.InputDigest,
		"input_signatures":       binding.Inputs,
		"premise":                foundation.Premise,
		"world_rules":            foundation.WorldRules,
		"current_characters":     foundation.Characters,
		"current_relationships":  foundation.Relationships,
		"relationships_reviewed": foundation.RelationshipsReviewed,
		"core_cast":              coreCast,
		"user_constraints":       userRules,
		"lifecycle":              lifecycle,
		"evidence_policy": map[string]any{
			"raw_source_included": false,
			"review_must_reread":  mode == CharacterRunReview,
		},
	}
	if projectMode == domain.CharacterCardProjectAdaptation {
		adaptation, adaptationErr := buildAdaptationCharacterEvidence(st)
		if adaptationErr != nil {
			return nil, domain.CharacterCardBinding{}, adaptationErr
		}
		packet["adaptation_evidence"] = adaptation
	} else {
		review, reviewErr := st.RunMeta.PlanningReview()
		if reviewErr != nil {
			return nil, domain.CharacterCardBinding{}, fmt.Errorf("load original character brief: %w", reviewErr)
		}
		packet["creative_brief"] = review
	}
	return packet, binding, nil
}

func currentCharacterBinding(
	st *store.Store,
) (
	domain.StoryFoundation,
	domain.CharacterCardBinding,
	domain.CharacterCardInputSignatures,
	domain.CharacterCardProjectMode,
	*domain.CoreCastContract,
	error,
) {
	foundation, err := st.Foundation.Load()
	if err != nil {
		return domain.StoryFoundation{}, domain.CharacterCardBinding{}, domain.CharacterCardInputSignatures{}, "", nil,
			fmt.Errorf("load character foundation: %w", err)
	}
	coreCast, err := st.CoreCast.Load()
	if err != nil {
		return domain.StoryFoundation{}, domain.CharacterCardBinding{}, domain.CharacterCardInputSignatures{}, "", nil,
			fmt.Errorf("load character core cast: %w", err)
	}
	inputs, mode, err := currentCharacterInputs(st, coreCast)
	if err != nil {
		return domain.StoryFoundation{}, domain.CharacterCardBinding{}, domain.CharacterCardInputSignatures{}, "", nil, err
	}
	binding, err := domain.CharacterCardBindingFromFoundation(foundation, inputs)
	if err != nil {
		return domain.StoryFoundation{}, domain.CharacterCardBinding{}, domain.CharacterCardInputSignatures{}, "", nil,
			fmt.Errorf("bind current character evidence: %w", err)
	}
	return foundation, binding, inputs, mode, coreCast, nil
}

func currentCharacterInputs(
	st *store.Store,
	coreCast *domain.CoreCastContract,
) (domain.CharacterCardInputSignatures, domain.CharacterCardProjectMode, error) {
	inputs := domain.CharacterCardInputSignatures{}
	mode := domain.CharacterCardProjectOriginal
	if coreCast != nil {
		inputs.CoreCast = coreCast.ContentSignature
		if coreCast.Mode == domain.CoreCastModeAdaptation {
			mode = domain.CharacterCardProjectAdaptation
		}
	}
	userRules, err := st.UserRules.Load()
	if err != nil {
		return inputs, mode, fmt.Errorf("load character input user rules: %w", err)
	}
	appendNamedCharacterSignature(&inputs, "user_rules", userRules)
	if mode == domain.CharacterCardProjectOriginal {
		review, reviewErr := st.RunMeta.PlanningReview()
		if reviewErr != nil {
			return inputs, mode, fmt.Errorf("load original character input brief: %w", reviewErr)
		}
		if review != nil {
			inputs.CreativeBrief = signatureForCharacterInput(struct {
				Brief       string `json:"brief"`
				StartPrompt string `json:"start_prompt"`
			}{review.Brief, review.StartPrompt})
		}
		return inputs, mode, nil
	}
	sourceFoundation, err := st.Adaptation.LoadSourceFoundation()
	if err != nil {
		return inputs, mode, fmt.Errorf("load adaptation character source foundation: %w", err)
	}
	if sourceFoundation != nil {
		inputs.SourceFoundation = sourceFoundation.SourceSignature
		if inputs.SourceFoundation == "" {
			inputs.SourceFoundation = signatureForCharacterInput(sourceFoundation)
		}
	}
	intent, err := st.Adaptation.LoadCoCreateIntent()
	if err != nil {
		return inputs, mode, fmt.Errorf("load adaptation character intent: %w", err)
	}
	if intent != nil {
		inputs.AdaptationIntent = intent.IntentHash
		if inputs.AdaptationIntent == "" {
			inputs.AdaptationIntent = signatureForCharacterInput(intent)
		}
	}
	dossier, err := st.Adaptation.LoadCoCreateDossier()
	if err != nil {
		return inputs, mode, fmt.Errorf("load adaptation character dossier: %w", err)
	}
	appendNamedCharacterSignature(&inputs, "adaptation_dossier", dossier)
	briefing, err := st.Adaptation.LoadCoCreateBriefing()
	if err != nil {
		return inputs, mode, fmt.Errorf("load adaptation character briefing: %w", err)
	}
	appendNamedCharacterSignature(&inputs, "adaptation_briefing", briefing)
	reports, err := st.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		return inputs, mode, fmt.Errorf("load adaptation character reports: %w", err)
	}
	appendNamedCharacterSignature(&inputs, "source_reports", reports)
	return inputs, mode, nil
}

func appendNamedCharacterSignature(inputs *domain.CharacterCardInputSignatures, name string, value any) {
	if value == nil {
		return
	}
	inputs.Additional = append(inputs.Additional, domain.CharacterCardNamedSignature{
		Name:      name,
		Signature: signatureForCharacterInput(value),
	})
}

func signatureForCharacterInput(value any) string {
	data, _ := json.Marshal(value)
	return domain.ContentSignature(data)
}

func buildAdaptationCharacterEvidence(st *store.Store) (map[string]any, error) {
	sourceFoundation, err := st.Adaptation.LoadSourceFoundation()
	if err != nil {
		return nil, fmt.Errorf("load adaptation source foundation: %w", err)
	}
	dossier, err := st.Adaptation.LoadCoCreateDossier()
	if err != nil {
		return nil, fmt.Errorf("load adaptation dossier: %w", err)
	}
	intent, err := st.Adaptation.LoadCoCreateIntent()
	if err != nil {
		return nil, fmt.Errorf("load adaptation intent: %w", err)
	}
	briefing, err := st.Adaptation.LoadCoCreateBriefing()
	if err != nil {
		return nil, fmt.Errorf("load adaptation briefing: %w", err)
	}
	reports, err := st.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		return nil, fmt.Errorf("load adaptation source reports: %w", err)
	}
	return map[string]any{
		"source_foundation": sourceFoundation,
		"dossier":           compactCharacterDossier(dossier),
		"intent":            intent,
		"briefing":          compactCharacterBriefing(briefing),
		"chapter_reports":   compactCharacterReports(reports),
		"report_count":      len(reports),
		"reports_truncated": len(reports) > characterContextReportLimit,
	}, nil
}

type characterSourceReport struct {
	Chapter        int                        `json:"chapter"`
	Title          string                     `json:"title"`
	Characters     []string                   `json:"characters"`
	CharacterFacts []string                   `json:"character_facts"`
	Relationships  []domain.RelationshipEntry `json:"relationships"`
}

func compactCharacterReports(reports []domain.AdaptationSourceReport) []characterSourceReport {
	if len(reports) > characterContextReportLimit {
		reports = reports[:characterContextReportLimit]
	}
	out := make([]characterSourceReport, 0, len(reports))
	for _, report := range reports {
		out = append(out, characterSourceReport{
			Chapter:        report.Chapter,
			Title:          report.Title,
			Characters:     limitStrings(report.Characters, characterContextListLimit),
			CharacterFacts: limitStrings(report.CharacterFacts, characterContextListLimit),
			Relationships:  limitSlice(report.Relationships, characterContextListLimit),
		})
	}
	return out
}

func compactCharacterDossier(value *domain.AdaptationCoCreateDossier) any {
	if value == nil {
		return nil
	}
	return struct {
		SourceSignature  string                                `json:"source_signature"`
		Overview         string                                `json:"overview"`
		CharacterArcs    []string                              `json:"character_arcs"`
		RelationshipMap  []domain.AdaptationRelationshipSignal `json:"relationship_map"`
		AmbiguityRisks   []domain.AdaptationRelationshipRisk   `json:"ambiguity_risks"`
		CoupleMilestones []domain.AdaptationRelationshipSignal `json:"couple_milestones"`
		AdaptationNotes  []string                              `json:"adaptation_notes"`
	}{
		value.SourceSignature,
		value.Overview,
		limitStrings(value.CharacterArcs, characterContextListLimit),
		limitSlice(value.RelationshipMap, characterContextListLimit),
		limitSlice(value.AmbiguityRisks, characterContextListLimit),
		limitSlice(value.CoupleMilestones, characterContextListLimit),
		limitStrings(value.AdaptationNotes, characterContextListLimit),
	}
}

func compactCharacterBriefing(value *domain.AdaptationCoCreateBriefing) any {
	if value == nil {
		return nil
	}
	return struct {
		SourceSignature       string                              `json:"source_signature"`
		IntentHash            string                              `json:"intent_hash"`
		Overview              string                              `json:"overview"`
		ConfirmedFacts        []string                            `json:"confirmed_facts"`
		IntentRelevantRisks   []domain.AdaptationBriefingRisk     `json:"intent_relevant_risks"`
		AdaptationSuggestions []string                            `json:"adaptation_suggestions"`
		Decisions             []domain.AdaptationBriefingDecision `json:"decisions"`
		ResolvedDecisions     []domain.AdaptationResolvedDecision `json:"resolved_decisions"`
	}{
		value.SourceSignature,
		value.IntentHash,
		value.Overview,
		limitStrings(value.ConfirmedFacts, characterContextListLimit),
		limitSlice(value.IntentRelevantRisks, characterContextListLimit),
		limitStrings(value.AdaptationSuggestions, characterContextListLimit),
		limitSlice(value.Decisions, characterContextListLimit),
		limitSlice(value.ResolvedDecisions, characterContextListLimit),
	}
}

func limitStrings(values []string, limit int) []string {
	return limitSlice(values, limit)
}

func limitSlice[T any](values []T, limit int) []T {
	if len(values) > limit {
		values = values[:limit]
	}
	return append([]T(nil), values...)
}

func adaptationSourceCharacterIDs(st *store.Store) []string {
	source, err := st.Adaptation.LoadSourceFoundation()
	if err != nil || source == nil {
		return nil
	}
	return foundationCharacterIDs(domain.StoryFoundation{Characters: source.Characters})
}

func foundationCharacterIDs(foundation domain.StoryFoundation) []string {
	ids := make([]string, 0, len(foundation.Characters))
	for _, character := range foundation.Characters {
		if id := strings.TrimSpace(character.ID); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func requireCharacterBinding(
	revision int64,
	auditSignature, candidateDigest, inputDigest string,
	current domain.CharacterCardBinding,
) error {
	if revision != current.Candidate.FoundationRevision ||
		strings.TrimSpace(auditSignature) != current.Candidate.FoundationAuditSignature ||
		strings.TrimSpace(candidateDigest) != current.Candidate.CharacterContentDigest ||
		strings.TrimSpace(inputDigest) != current.InputDigest {
		return fmt.Errorf("character candidate or evidence signature is stale/conflict: %w", errs.ErrToolConflict)
	}
	return nil
}

func validateCharacterRunIdentity(runID string, mode CharacterRunMode) error {
	if strings.TrimSpace(runID) == "" {
		return fmt.Errorf("character run_id is required: %w", errs.ErrToolArgs)
	}
	if mode != CharacterRunAnalyze && mode != CharacterRunReview {
		return fmt.Errorf("character mode %q is invalid: %w", mode, errs.ErrToolArgs)
	}
	return nil
}

func validateCharacterSubmissionIdentity(idempotencyKey string, revision int64, audit, candidate, input string) error {
	if strings.TrimSpace(idempotencyKey) == "" {
		return fmt.Errorf("character idempotency_key is required: %w", errs.ErrToolArgs)
	}
	if revision < 0 || len(strings.TrimSpace(audit)) != 64 || len(strings.TrimSpace(candidate)) != 64 || len(strings.TrimSpace(input)) != 64 {
		return fmt.Errorf("character base revision/signatures are invalid: %w", errs.ErrToolArgs)
	}
	return nil
}

func appendCompletenessFindings(
	findings []domain.CharacterCardReviewFinding,
	completeness []domain.CharacterCardCompletenessResult,
) []domain.CharacterCardReviewFinding {
	existing := make(map[string]struct{}, len(findings))
	for _, finding := range findings {
		existing[finding.ID] = struct{}{}
	}
	for _, result := range completeness {
		for _, missing := range result.Missing {
			if missing.Severity != domain.CharacterCardSeverityBlocking {
				continue
			}
			id := "completeness:" + result.CharacterID + ":" + missing.Code
			if _, exists := existing[id]; exists {
				continue
			}
			findings = append(findings, domain.CharacterCardReviewFinding{
				ID:              id,
				Scope:           domain.CharacterCardFindingCharacter,
				CharacterID:     result.CharacterID,
				Location:        missing.Field,
				Severity:        domain.CharacterCardSeverityBlocking,
				IssueType:       "deterministic_completeness",
				Description:     missing.Description,
				EvidenceSummary: "CharacterCard deterministic completeness gate",
				Suggestion:      "complete the required field and run a fresh review",
				Blocking:        true,
			})
			existing[id] = struct{}{}
		}
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].ID < findings[j].ID })
	return findings
}

func hasBlockingCharacterFinding(findings []domain.CharacterCardReviewFinding) bool {
	for _, finding := range findings {
		if finding.Blocking || finding.Severity == domain.CharacterCardSeverityBlocking {
			return true
		}
	}
	return false
}

func decodeCharacterToolArgs(data json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid character tool args: %w: %w", errs.ErrToolArgs, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid character tool args trailing JSON: %w", errs.ErrToolArgs)
	}
	return nil
}

func characterSchema() map[string]any {
	contrast := schema.Object(
		schema.Property("surface", schema.String("Observable presentation")).Required(),
		schema.Property("depth", schema.String("Contrasting motive or behavior")).Required(),
	)
	backstory := schema.Object(
		schema.Property("event", schema.String("Past event relevant to current choices")).Required(),
		schema.Property("impact", schema.String("Present causal impact")).Required(),
	)
	initial := schema.Object(
		schema.Property("identity", schema.String("Chapter-zero identity")).Required(),
		schema.Property("situation", schema.String("Chapter-zero situation")).Required(),
		schema.Property("emotion", schema.String("Chapter-zero emotional state")).Required(),
		schema.Property("resources", schema.Array("Chapter-zero resources", schema.String(""))).Required(),
		schema.Property("relationships", schema.String("Chapter-zero relationship state")).Required(),
	)
	knowledge := schema.Object(
		schema.Property("known", schema.Array("Facts the character knows", schema.String(""))).Required(),
		schema.Property("unknown", schema.Array("Facts the character does not know", schema.String(""))).Required(),
		schema.Property("misconceptions", schema.Array("False beliefs", schema.String(""))).Required(),
		schema.Property("forbidden", schema.Array("Knowledge the character must not use", schema.String(""))).Required(),
	)
	return schema.Object(
		schema.Property("id", schema.String("Stable character ID; empty only when deterministic generation is intended")).Required(),
		schema.Property("name", schema.String("Character name")).Required(),
		schema.Property("aliases", schema.Array("Aliases", schema.String(""))).Required(),
		schema.Property("role", schema.String("Identity or story responsibility")).Required(),
		schema.Property("description", schema.String("Character description")).Required(),
		schema.Property("arc", schema.String("Causal character arc")).Required(),
		schema.Property("traits", schema.Array("Distinct traits", schema.String(""))).Required(),
		schema.Property("tier", schema.Enum("Information-density tier", "core", "important", "secondary", "decorative")).Required(),
		schema.Property("faction", schema.String("Faction or empty string")).Required(),
		schema.Property("goal", schema.String("External goal or empty string")).Required(),
		schema.Property("motivation", schema.String("Internal motivation or empty string")).Required(),
		schema.Property("conflict", schema.String("Core conflict or empty string")).Required(),
		schema.Property("voice", schema.String("Language/behavior voice or empty string")).Required(),
		schema.Property("constraints", schema.Array("Behavior constraints", schema.String(""))).Required(),
		schema.Property("contrast_details", schema.Array("Character contrasts", contrast)).Required(),
		schema.Property("key_backstory", schema.Array("Causally relevant backstory", backstory)).Required(),
		schema.Property("initial_state", initial).Required(),
		schema.Property("knowledge_boundary", knowledge).Required(),
		schema.Property("notes", schema.String("Compact notes or empty string")).Required(),
	)
}

func characterRelationshipSchema() map[string]any {
	return schema.Object(
		schema.Property("id", schema.String("Stable relationship ID or empty string")).Required(),
		schema.Property("source_character_id", schema.String("Source character ID")).Required(),
		schema.Property("target_character_id", schema.String("Target character ID")).Required(),
		schema.Property("type", schema.Enum("Relationship type", "ally", "rival", "family", "romantic", "mentor", "professional", "other")).Required(),
		schema.Property("label", schema.String("Readable relationship label or empty string")).Required(),
		schema.Property("direction", schema.Enum("Direction", "directed", "bidirectional", "undirected")).Required(),
		schema.Property("status", schema.Enum("Planned state", "planned", "active", "strained", "broken", "resolved")).Required(),
		schema.Property("description", schema.String("Relationship dynamics or empty string")).Required(),
		schema.Property("since", schema.String("Starting point or empty string")).Required(),
		schema.Property("tags", schema.Array("Relationship tags", schema.String(""))).Required(),
		schema.Property("constraints", schema.Array("Relationship constraints", schema.String(""))).Required(),
	)
}

func characterSourceMappingSchema() map[string]any {
	evidence := schema.Object(
		schema.Property("kind", schema.Enum("Evidence classification", "source_fact", "adaptation_decision", "target_original_addition")).Required(),
		schema.Property("reference", schema.String("Bounded evidence reference")).Required(),
		schema.Property("summary", schema.String("Compact evidence summary or empty string")).Required(),
	)
	return schema.Object(
		schema.Property("id", schema.String("Stable mapping ID")).Required(),
		schema.Property("action", schema.Enum("Mapping action", "keep", "rename", "merge", "split", "exclude", "target_original")).Required(),
		schema.Property("source_character_ids", schema.Array("Source IDs", schema.String(""))).Required(),
		schema.Property("target_character_ids", schema.Array("Target IDs", schema.String(""))).Required(),
		schema.Property("rationale", schema.String("Adaptation rationale")).Required(),
		schema.Property("evidence", schema.Array("Classified evidence", evidence)).Required(),
	)
}

func characterFindingSchema() map[string]any {
	return schema.Object(
		schema.Property("id", schema.String("Stable finding ID")).Required(),
		schema.Property("scope", schema.Enum("Finding scope", "global", "character")).Required(),
		schema.Property("character_id", schema.String("Character ID or empty string for global findings")).Required(),
		schema.Property("location", schema.String("Field/path or empty string")).Required(),
		schema.Property("severity", schema.Enum("Severity", "warning", "blocking")).Required(),
		schema.Property("issue_type", schema.String("Knowledge/voice/behavior/arc/relationship/coverage/duplication issue type")).Required(),
		schema.Property("description", schema.String("Finding description")).Required(),
		schema.Property("evidence_summary", schema.String("Compact evidence summary")).Required(),
		schema.Property("suggestion", schema.String("Repair suggestion or empty string")).Required(),
		schema.Property("blocking", schema.Bool("Whether this finding blocks pass")).Required(),
	)
}
